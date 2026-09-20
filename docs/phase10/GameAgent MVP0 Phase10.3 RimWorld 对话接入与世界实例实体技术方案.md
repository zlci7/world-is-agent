# GameAgent MVP0 Phase10.3 RimWorld 对话接入与世界实例实体技术方案

> 状态：**已定稿（Accepted for Implementation）**。10.3-1 已实现并实机验收（见 [10.3-1 实机验收记录](GameAgent%20MVP0%20Phase10.3-1%20实机验收记录.md)）；10.3-2 已实机验收，含 save/load、远行队往返、entity_id 唯一性与 tick（见 [10.3-2 实机验收记录](GameAgent%20MVP0%20Phase10.3-2%20实机验收记录.md)）；10.3-3 与 10.3-4 已实现并实机验收，证据见 [10.3-3/10.3-4 实现与证据记录](GameAgent%20MVP0%20Phase10.3-3与10.3-4%20实现与证据记录.md)。
> 本文只描述确认后的范围与验收条件；实现进度与实机结论另行记录。
> 上位文档：[Phase10 技术开发与验收总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md) §4。

## 1. 目标与范围

### 1.1 本阶段做什么

第二款真实游戏的**最小可用接入**：玩家在 RimWorld 里与一个殖民者对话，对话内容由 Agent 生成，
并参考该殖民者的性格、背景与既有历史。

```text
Identity + Time + bounded Observation + 一类玩家交互事件 + 一个 capability
```

**"只做对话"不等于绕开感知层。** 它只是把 Agent 的环境行动能力暂时限制在 `present_dialogue`，
完整链路一步不少：

```text
玩家触发 GameEvent
    ↓
Runtime 发出 ObserveRequest
    ↓
Adapter 在主线程实时读取这个 Pawn
    traits / 背景 / 技能 / needs / mood / health / location
    ↓
Context ＝ archetype + 当前 Observation + 该 Pawn 自己的 Memory / History + 玩家输入
    ↓
模型决策
    ↓
自行选择 present_dialogue
    ↓
RimWorld 主线程展示对话窗口
    ↓
玩家回复
    ↓
下一次 AgentTurn
```

Runtime 侧的执行顺序已核实：`runtime/internal/gateway/stream_environment.go` 以请求/应答方式发送
`ObserveRequest{entity_id, world_id}` 并等待该轮 Observation，gateway 保证 ACCEPTED 先于
ObserveRequest 发出；Context 构建与模型调用发生在其后。

### 1.2 本阶段如实不验证什么

选 RimWorld 而非其它候选的原始理由是"世界自己会动"。收缩为玩家触发的纯对话后，这一点**没有被验证**：

```text
未验证   稀疏 Event → bounded Observation 在自主推进的世界里是否成立
未验证   长时异步 Action 与 Job ownership（本阶段没有异步 capability）
未验证   世界状态边沿（精神崩溃、受伤、商队、地图切换）的准入策略
```

今后不得以"已接入 RimWorld"声称上述任何一条已经成立。

### 1.3 这个切片能证明什么

它能产出 Stardew **产不出**的发现：

> **Stardew 的 Agent 身份是作者手写的静态数据；RimWorld 的 Pawn 是存档内程序生成的实例数据。**

Stardew 的角色定义（`npc-abigail.json` 等）是仓库里的静态文件。RimWorld 没有这种东西——
traits、童年/成年背景、技能全部随存档生成。这是"definition 属于 Runtime 配置树"这一 Stardew
隐含假设的第一次真实检验。这是分层与协议问题，不是功能问题。

```text
1  动态实例 Agent
   程序生成的 Pawn → 运行时 entity → 共享 archetype → Observation 提供个体差异

2  definition 与 identity 真正解耦
   Pawn A / B / C 共享 archetype:colonist，但 Memory A ≠ Memory B ≠ Memory C
   这是对"definition_id 不是 agent identity"的实测，而不是架构文档的声明

3  感知真的决定对话
   人格事实来自真实游戏实例状态，而不是两份手写 persona JSON

4  第二个 Adapter 复用同一 Runtime
   Runtime Core diff = 0

5  Capability 与 Event 语义跨游戏复用
   present_dialogue / player_interacted_with_npc / player_said_to_npc 原样复用，
   而 Runtime 完全不解释这些名字
```

## 2. 前提：传输已实机验证

已在 RimWorld 1.6.4871 rev591 / Unity 2022.3.35f1 / Mono 6.13.0 上确认：

```text
SocketsHttpHandler   不存在（Mono 6.13 的 BCL 没有 HTTP/2 实现）
Grpc.Core 2.46.6     在进程内建立双向流成功，对真实 Runtime 收到 EnvironmentReady
原生库               经 LoadLibrary(绝对路径) 预加载后，DllImport("grpc_csharp_ext") 按名解析命中同一模块
```

技术栈固定，不再评估替代方案：

```text
net472 + Grpc.Core 2.46.6（C-core 原生）+ 进程内直连
不采用：grpc-dotnet（Mono 无 HTTP/2）、给 Runtime 加 TLS（会绑到 Windows 11+）、手写 h2c、进程外 shim
```

## 3. 命名与接入位置（已定）

```text
仓库目录            adapters/rimworld
将来独立仓库        wia-adapter-rimworld
Mod 安装目录        <Game>\Mods\WiaRimWorld
AssemblyName        WiaRimWorld.dll
RootNamespace       Wia.RimWorld
About.xml <name>    World Is Agent (RimWorld)
packageId           wia.rimworld
```

`packageId`、Mod 安装目录与 `AssemblyName` 自首次发布起即为兼容契约，改动需单独授权。

## 4. definition 与 entity：模板与实例

### 4.1 既有分层

```text
game.json                    世界层：设定、世界规则、叙事约束
archetype:<name>.json        模板层：可复用的角色模板，共享给多个实体
<npc|agent>:<name>.json      人格层：个别角色的完整定义
```

模板层已存在，其行为准则就是为这种用法写的（`archetype-town-villager.json`）：

```text
Do not invent a unique backstory beyond the current observation.
Treat this definition as a shared template, not as a persistent entity identity.
```

### 4.2 绑定方式

**所有 eligible colonist 绑定同一个 archetype**，不生成 per-Pawn 定义文件：

```text
game_id       rimworld
definition_id archetype:colonist        ← 共享模板
entity_id     pawn:<Pawn.GetUniqueLoadID()>
```

Runtime 侧 catalog 以 `(game_id, definition_id)` 为键，允许多个实体指向同一 definition，
因此"多实体共享一个模板"不需要任何 Runtime 改动。

### 4.3 个体差异来自哪里

共享模板意味着定义本身不提供个体差异，差异必须来自实例：

```text
性格         Pawn.story.traits + 童年/成年背景标题  →  Observation（有界，见 §7）
技能         Pawn.skills 的高值项                   →  Observation
历史         我们与该 Pawn 的历史 turn               →  既有 Memory
最近         最近 turn + 当前 Observation            →  既有 Context
```

Memory 以 `AgentSessionKey = (game_id, world_id, entity_id)` 为键，因此共享同一 archetype 的多个 Pawn
各自累积互不干扰的历史，"按不同经历长成不同的人"不需要新机制。

### 4.4 archetype 必须写成模板而不是人格

若模板写成具体人格，所有殖民者会说出同一个人。`behavior_guidelines` 必须指示模型从当前 Observation
读取该实体的 traits、背景与状态，不假定固定人格，不把定义当作持久身份。

### 4.5 将来的扩展轴（本阶段不做）

RimWorld 自带可用于分类的维度（背景故事的 `spawnCategories`，如部落／都市／边缘世界等）。
将来若需要模板集合，应沿这个游戏自带维度扩展，而不是自造分类法。**第一版只做一个 archetype。**

## 5. 范围限定：eligible colonist

不是"所有 Pawn"。第一版明确限定：

```text
eligible = 原版 Human ThingDef + 玩家阵营 + 存活
排除       敌人 / 囚犯 / 访客 / 动物 / 机械族 / 死亡 Pawn / mod 种族
```

**这不是可选的收紧。** 玩家入口通过 XML patch 给 `Human` def 挂 `ThingComp`，而 `Human` 是所有
人形 def 的基类，敌人类与囚犯同样会拿到这个 Comp。因此 `CompGetGizmosExtra()` **必须自带
eligibility 判断**，否则敌方单位身上会出现 WIA 入口。

## 6. Identity 与时间

### 6.1 三个标识

```text
game_id    = "rimworld"
world_id   = Adapter 生成并随存档持久化的 GUID
entity_id  = "pawn:" + Pawn.GetUniqueLoadID()
```

`entity_id` 的前缀只加一次。所有地方引用它时都用同一个值，不得再拼一次前缀：

```text
target_entity_id  = entity_id
EntityRef
  entity_id       = entity_id
  entity_type     = "pawn"          ← 冻结；Runtime 不按这个字符串做游戏分支
  display_name    = Pawn 名称
  definition_id   = "archetype:colonist"
```

`Pawn.GetUniqueLoadID()` 的确切返回形式由游戏 API 决定，**10.3-2 必须在实机记录真实取值并冻结**，
不得凭推测写死字符串拼接结果。本阶段冻结的是"格式"（单一 `pawn:` 前缀 + `entity_type = "pawn"`），
不是"猜测的后缀"。

### 6.2 world_id 必须由 Adapter 自己持久化

```text
World.info.name        玩家可以改名
World.info.seedString  无法区分同一 seed 的不同存档
存档文件名              Save As 会改变
```

做法：生成 GUID，通过 `WorldComponent.ExposeData` + `Scribe_Values.Look` 写入存档。
移除 mod 后存档仍可读取。

已知语义，本阶段接受并记录：**Save As 或复制存档会继承同一个 world GUID，因此它表达的是
world lineage，不是磁盘文件 identity。**

### 6.3 entity_id 的稳定范围与唯一性

这是两个不同的性质，必须分别验证：

```text
唯一性
  同一 world_id 内      entity_id 必须唯一
  不同 world_id 之间    entity_id 不要求全局唯一

稳定性
  同一 Pawn 的 entity_id 在 save/load 后不变
  同一 Pawn 的 entity_id 在 Map ↔ Caravan 切换后不变
```

跨 world_id 不唯一不构成问题，因为 `AgentSessionKey` 包含 world_id。
但"同一 world 内两个不同 eligible colonist 拿到同一个 entity_id"是身份碰撞，必须能测出来。

**Map 不得进入 Agent identity**：商队与部分世界状态下 `Pawn.Map` 就是 `null`。

### 6.4 GameTime：tick-only

协议 `GameTime` 的全部字段都是 `optional`，Runtime 已正式支持 calendar、calendar+tick 与 tick-only
三种时间基准——基准定义在 `runtime/internal/memory/game_time.go`
（`GameTimeCalendar` / `GameTimeCalendarWithTick` / `GameTimeTick` 与 `SharedGameTimeBasis`），
Context 与 Memory 投影消费它；tick-only 路径有专项测试覆盖。

第一版直接冻结为 tick-only，**不为 RimWorld 发明日历映射**：

```text
GameTime.tick = Find.TickManager.TicksGame
```

`TicksGame` 是绝对、单调、随存档持久化的计数器。要求：

```text
GameEvent.game_time.tick      与
Observation.game_time.tick     使用同一个时钟
```

不映射 year / quadrum / day / hour / minute，避免引入无谓语义。

**单调性的范围必须限定。** RimWorld 允许显式载入更早的存档，而 `world_id` 是 lineage GUID、不会改变：

```text
载入存档 A（tick 150000）→ 玩到 200000 → 重新载入存档 A → tick 回到 150000
```

因此正确的表述是：

```text
同一个 loaded run 内，Event 与 Observation 使用同一 tick-only 时钟并单调递增
正常 save → 重新载入同一保存点，时间保持一致
显式载入更早的存档属于 world timeline rewind
```

**rewind 沿用 Runtime 既有的 game-time filtering 语义，不新增回滚机制。**
已核实该机制存在且行为正确：`selectTimelineMemories` 先丢弃 `isFutureMemory` 的记录，
再按 `SharedGameTimeBasis` 比较排序——回档后游戏时间"在未来"的 Memory 会自然退出时间线投影。

### 6.5 必须实机验证的场景

```text
1  保存 → 退出 → 重新载入 → 同一 Pawn 的 entity_id 不变，world_id 不变
2  Map → 商队 → Map（其间 Pawn.Map == null）→ 两者都不变
3  同一 Pawn 重载后仍是同一个 AgentSession，且历史可被读回
4  同一 loaded run 内 Event 与 Observation 的 tick 单调递增；save → reload 同一保存点时间一致
   注：10.3-2 只验证 tick 本身（单一来源、单调、save → reload 一致）；"Event 与 Observation"
   的配对要等两者都存在，见 §15 条件 8 与 §16 的 10.3-4 证据链
```

## 7. Bounded Observation

个体差异由 Observation 承载，因此它是**承重结构**，不是可选补充。上限在方案阶段即冻结，
不留 `N` 这类占位符。

### 7.1 字段与固定上限

```text
state.rimworld
  schema_version   "0.1"                    与 Stardew 既有约定一致（字符串）
  pawn
    id             entity_id
    name           ≤ 80 字符
    colonist       bool
    drafted        bool
    traits         最多 4 项，每项 { def_name, degree, label }；label ≤ 80 字符
    childhood      1 条背景标题，≤ 120 字符
    adulthood      1 条背景标题，≤ 120 字符
    skills         最多 5 项，每项 { def_name, level }
  location
    map_id         string | null
    position       { x, z } | null
    in_caravan     bool
  needs            food / rest / recreation，各为 0..1 数值
  mood             current level
  health           normal / injured / downed
```

`traits` 用结构而不是纯 label：`def_name` 是稳定机器身份兼排序键，`degree` 是 trait 强度，
`label` 供模型阅读。若只保留 label，§7.2 的 `defName ASC` 排序在 Observation 里就没有可见的排序键。

`location` 的两个字段同时为空是合法状态：

```text
Map != null   →  map_id 与 position 都有值
Map == null   →  map_id = null 且 position = null      （商队 / 世界状态）
```

`Pawn.Map == null` 时 `Pawn.Position` 不是可靠的当前位置事实，因此不得输出。
第一版不引入 world tile 或 caravan id——当前只有对话，没有世界移动需求。

### 7.2 Projection 确定性

要验证的是 **projection builder 的确定性**，不是整个 `Observation` 对象的一致性：
`Observation.revision` 与 `Observation.game_time.tick` 在两次 Observe 之间本来就可能不同。

因此判据是：

```text
对同一份冻结的 Pawn snapshot 输入，连续构建两次 state.rimworld，
其规范化内容（结构化比较）必须完全一致
```

排序规则：

```text
skills       level DESC，defName ASC 作为稳定 tie-break
traits       defName ASC
```

比较基准是结构化内容（规范化 JSON / 字段级比较），**不是 Protobuf 序列化字节**：
`google.protobuf.Struct` 是 map 语义，wire 字节顺序不构成兼容契约，把它钉成产品要求是错的。

### 7.3 明确不收录

```text
全部 Hediff / 全部 Thought / 全部 Skill / 全部 Relation / 全部 Inventory
全部 WorkPriority / Pawn.story 全文 / 整个 Map / 附近所有 Pawn
```

要验证的命题是 **Adapter 能否把丰富的游戏对象压成有界 Observation**，不是 Protobuf 能装多大的 JSON。
`Observation.nearby_entities` 保持为空。任何路径、存档内容、文件系统信息不得进入 Observation。

### 7.4 schema_version

`state.rimworld.schema_version` 随字段集变化递增。Stardew 已采用同一约定
（`StardewObservation.CurrentSchemaVersion`），未来 RimWorld projection 扩字段时用于判定兼容。

## 8. 事件契约（冻结）

RimWorld **没有玩家↔Pawn 的原生对话系统**（`InteractionDef` 只管 Pawn 之间），因此这里不是"替换对话"，
而是新增一个入口与一个窗口。

两个事件名沿用 Stardew Adapter 对相同语义使用的名字。`event_type` 由 Adapter 决定，
Runtime 不做 game-specific allowlist——两个游戏共用同一组事件名本身就是"Runtime 不按名字分支"的证据。

### 8.1 `player_interacted_with_npc`

```text
event_type        player_interacted_with_npc
world_id          persisted GUID
target_entity_id  entity_id
game_time.tick    TicksGame
entities
  { player:local,  entity_type = "player" }
  { entity_id,     entity_type = "pawn", display_name, definition_id = "archetype:colonist" }
payload
  conversation_id     非空，由 Adapter 在玩家点击 gizmo 时生成
  trigger             本阶段固定为 "wia_gizmo"
  source              "rimworld-mod"
```

### 8.2 `player_said_to_npc`

```text
event_type        player_said_to_npc
world_id          persisted GUID
target_entity_id  entity_id
game_time.tick    TicksGame
entities          同 §8.1
payload
  conversation_id        与 §8.1 同一个 id
  input_kind             "option" | "free_text"
  text                   玩家输入
  selected_option_index  仅当 input_kind = option 时存在
  trigger                "wia_gizmo"
  source                 "rimworld-mod"
context facts
  kind        utterance
  actor       player:local
  target      entity_id
  scope_id    conversation_id
  attributes  { input_kind, trigger, selected_option_index? }
```

**复用的是完整的 conversation contract，不是两个同名字符串。** 字段名、payload 结构、
`ContextFact` 形状都与 Stardew 一致，因此 Runtime 侧的会话语义无需为 RimWorld 做任何适配；
两个 Adapter 的差别只有 `source` 取值与实体类型。

### 8.3 不携带 InteractionSource

`InteractionSource` 服务于 Task authority。已核实 Stardew Adapter 也只在 task ready 时才挂载它
（`RuntimeClient` 中 `IsTaskReady ? worldContext.Current : null`）。本阶段 `task.enabled=false`，
因此**不为了对齐 Stardew 而强行携带**。将来接 Durable Task 时再补对应 scope。

### 8.4 不产生 AgentTurn 的变化

```text
tick / 位置变化 / 需求衰减 / 心情浮点 / Job 进度
精神崩溃 / 受伤 / 倒地 / 被征召 / 商队出发 / 地图切换
Pawn 之间的社交互动
```

全部只更新 projection。MVP0 的边界是不做自主目标生成与无界后台自主行为，
不因"RimWorld 事件很多"而打开。

## 9. Capability：`present_dialogue`

**沿用 Stardew 的同名 capability，不新造名字，并保持完全相同的声明与生命周期。**

### 9.1 声明（与 Stardew 一致）

```text
name               present_dialogue
execution_mode     SYNC
concurrency_mode   SEQUENTIAL
extensions.gameagent.tool_policy
  exclusive_per_step     true
  settle_after_success   true
```

```text
输入    text（Pawn 台词）/ reply_options / allow_free_text
文本边界 text ≤ 240 字符，单条选项 ≤ 80 字符

输入形态（与 Stardew 完全一致，不是"至多 3 条"）
  继续对话   reply_options 恰好 3 条，allow_free_text = true 或省略
  结束对话   reply_options = []，allow_free_text = false

成功时 ActionResult.output
  conversation_id
  displayed_text
  reply_options_count
  allow_free_text
```

### 9.2 生命周期

```text
模型调用 present_dialogue
      ↓
主线程成功创建对话窗口
      ↓
ActionResult = SUCCEEDED          ← 职责只是"展示对话"，不等待玩家
      ↓
settle_after_success → 当前 Turn 结束

玩家之后选择回复或自由输入
      ↓
player_said_to_npc
      ↓
下一次 AgentTurn
```

终态规则：

```text
窗口创建失败 / 参数非法   → FAILED / REJECTED
展示之前的取消            → CANCELLED
窗口被玩家直接关闭        → 本地 conversation state 清理，不产生 player_said_to_npc，
                            不产生新的 ActionResult，也不存在悬挂 Action
```

**"等待玩家回复"不是这个 Action 的职责**，因此玩家关闭窗口不构成一次 Action 失败。
这消除了早先版本里"关闭窗口导致 Action CANCELLED"的错误语义，也就不存在悬挂 Action 的问题。

### 9.2.1 conversation_id 是 Adapter 私有关联状态

`present_dialogue` 的模型输入里**没有** `conversation_id`，这是有意的——模型不应持有这个关联 ID。
Adapter 通过 `ActionRequest.source_event_id` 自己查回它：

```text
发送 GameEvent 时
  event_id → conversation_id 存入 Adapter 本地 pending conversation state

收到 ActionRequest
  source_event_id
      ↓ 查表
  conversation_id
      ↓
  打开窗口，并把 conversation_id 写进 ActionResult.output
```

因此 **`conversation_id` 不得进入 model-visible tool schema**。
它是 Adapter 自己维护的关联状态，与 §8 事件 payload 里的同名字段是同一个值。

### 9.3 tool_policy 复用与 §4.5 结论

本阶段 RimWorld 侧**复用** `exclusive_per_step` 与 `settle_after_success`，与 Stardew 取值相同。
这给总方案 §4.5 的判断提供了比"没有压力"更强的证据：

```text
证据    同一组 tool policy 在两个完全不同的 Adapter 中表达了同一种工具语义
        → 这两个 key 不是 Stardew-only

保留意见 只有两个真实消费者
        尚未出现字符串 key 导致的真实维护事故
        policy 集合也还不能证明已经定型

结论    继续保留 Capability.extensions.gameagent.tool_policy，暂不提升为一等 proto 字段
```

## 10. 主线程模型

```text
进程级（无存档也在）
  gRPC 完成线程  →  enqueue  →  主线程队列泵  →  连接管理 / Hello / 能力声明 / 心跳

存档级（随世界生灭）
  GameComponent / WorldComponent  →  world_id 持久化、projection、对话窗口状态
```

**网络泵不挂在 `GameComponent` 上。** 主菜单没有 `Current.Game`，商队状态下 `Map == null`，
而传输连接是进程级的，不应随存档生灭。

gRPC 回调线程不得直接触碰：

```text
Pawn    Map    JobTracker    Find.*    Messages    任何 Def 数据库
```

第一版不使用 Harmony：玩家入口用 XML patch 挂 `ThingComp`，在 `CompGetGizmosExtra()` 里提供 gizmo
（附带 §5 的 eligibility gate），不需要 patch `Pawn.GetGizmos()`。

## 11. Ownership 边界

本阶段完全不触碰 RimWorld 的 AI：不排 Job、不征召、不改变任何行为，只挂一个对话入口。
这是 ownership 边界最保守的一档，应记录为"第二个 Adapter 在最小范围内无需触碰游戏 AI"。

## 12. 接入与部署

```text
<Game>\Mods\WiaRimWorld\
  About\About.xml
  Assemblies\   WiaRimWorld.dll
                Google.Protobuf.dll  Grpc.Core.dll  Grpc.Core.Api.dll
                System.Memory.dll  System.Buffers.dll  System.Numerics.Vectors.dll
                System.Runtime.CompilerServices.Unsafe.dll
  Native\       grpc_csharp_ext.dll
  Patches\      WiaEntryPoint.xml（§10 的 XML patch：给 Human 的 comps 挂 WiaDialogueCompProperties）
```

`Patches/` 与 `Assemblies/`、`Native/` 一样属于白名单 stage 的范围：安装脚本会先清空再写入。
少了这个目录，适配器能加载、能握手，但 gizmo 永远不会出现——表现像"mod 坏了"而不是"少了一个文件"。

两条布局规则已在实机确认，必须写进安装脚本：

```text
1  原生库不能放在 Assemblies/
   RimWorld 的 ModAssemblyHandler.ReloadAll() 把 Assemblies/ 下每个 *.dll 交给 Assembly.LoadFrom，
   原生映像抛 BadImageFormatException；托管程序集按文件名顺序加载，这个失败会让排在它后面的
   主程序集根本不加载 —— 表现为"游戏正常、mod 静默不工作"。

2  DllImport 的基名必须与预加载的模块名一致
   Grpc.Core 的 DllImport 名是 grpc_csharp_ext，包内文件名是 grpc_csharp_ext.x64.dll，
   Windows 的 LoadLibrary 不会用基名匹配名字不同的已加载模块。故安装时重命名为
   grpc_csharp_ext.dll 并在启动时以绝对路径预加载。
```

RimWorld 的安装脚本不能像 Stardew 那样整目录拷贝（构建输出含 x86 / macOS / Linux 三套原生库与 pdb），
必须按白名单 stage。游戏路径通过 `-GamePath` / `WIA_RIMWORLD_GAME_PATH` 显式提供。

`scripts/check-architecture.ps1` 需要两处扩展：

```text
1  它当前把 Stardew 适配器路径写死（第 39 行）。总方案 §4.8 条件 2 要求断言覆盖新 Adapter，
   应改为遍历 adapters/*。

2  $runtimeForbiddenTerms 目前不含 RimWorld。应补上，否则"Runtime 不含游戏特定词"这条断言
   对新游戏是空转。（该脚本第 31 行已把 \runtime\config\games\ 排除在扫描外，
   因此 §13.1 的 definitions 不会被误报。）
```

## 13. 开发期配置：不启用 Task，不重构 game profile

### 13.1 配置资产（位置冻结）

```text
runtime/config/games/rimworld/
└── definitions/
    ├── game.json                   game_id = rimworld
    └── archetype-colonist.json     game_id = rimworld，definition_id = archetype:colonist

adapters/rimworld/profile/
└── agent.json                      RimWorld agent profile（开发用）
```

**`agent.json` 绝对不能放在 `runtime/config/games/rimworld/` 下。**
`runtime/config/defaults.go` 的 `shippedProfile()` 要求发布树中恰好存在一份 `games/*/agent.json`，
多一份即报错，而 `Seed()` 失败会让 `bootstrap.Open()` 置 `StateBlocked`：
**所有全新 data root 的首次启动会直接 blocked。** 这条约束是刻意设计的（多份 profile 而没有
选择规则属于打包错误），因此 RimWorld 的 profile 放在 Adapter 目录内，由开发期写入 dev data root：

```text
<dev root>/config/agent.json                   ← 复制 adapters/rimworld/profile/agent.json
<dev root>/config/games/rimworld/definitions/  ← 由 Runtime 正常 seed
```

流程：先让 Runtime 正常 seed 一次 dev data root（此时**不设置** `GAMEAGENT_AGENT_CONFIG`），
再把 RimWorld profile 覆盖到 `<dev root>/config/agent.json`。之后 anchor 已存在，不会被再次覆盖。

**不使用 `GAMEAGENT_AGENT_CONFIG`**：一旦设置它，`seedDefaults` 会以 `skip=true` 跳过整棵树的播种，
dev data root 将拿不到任何 definitions，archetype 也就无法被 catalog 找到。

`definition_catalog_root` 保持 `config/games`（相对 data root）。

已知副作用：把 definitions 放在 Runtime 发布树里意味着它们会被 seed 进**之后新建的** data root。
`Seed()` 不是升级机制——`agent.json` 一旦存在它就直接返回，不会补任何 definition，
因此既有的 data root 不会自动得到 RimWorld definitions。本阶段不受影响（10.3-3 使用全新 dev data root），
但这是发布前必须解决的问题，见 §17。

### 13.2 不重构 game profile

Runtime 目前只读一份 `agent.json`，本阶段不解决：`GAMEAGENT_AGENT_CONFIG` 与
`definition_catalog_root` 都已存在，开发期用独立 `--data-root` 即可。

**RimWorld profile 第一版明确关闭 task：**

```json
"task": { "enabled": false }
```

理由是已核实的两条 gateway 行为：

```text
能力扩展 gameagent.tasks.v1 只有在 Runtime 侧启用 task 时才被接受
未协商该扩展时，WorldBinding 被明确拒绝（不是静默忽略）
```

本阶段不做长期任务、不做异步世界行为，因此**不协商该扩展**，也就不用
`WorldBinding` / `WorldClock` / `CheckpointPrepare` / `CheckpointFinish`。
普通 AgentTurn 不依赖它们：`AdapterHello.game_id` + `GameEvent.world_id` + 目标 `EntityRef` 已足够。

注意区分两个概念：`GameEvent.world_id`（普通回合用的持久化 GUID）与 Task 的 `WorldBinding` 无关。

## 14. 阶段拆分与分段验收

```text
10.3-1  Skeleton
  Mod 加载 → 进程级主线程泵 → gRPC 双向流 → AdapterHello → EnvironmentReady → 能力声明
  到此为止；不做 WorldBinding / WorldClock / Checkpoint / task extension
  验收：Runtime Core diff = 0；退出条件 1–4

10.3-2  Identity + Time
  world_id 持久化 GUID；pawn entity_id 并在实机记录其确切形式后冻结；tick-only GameTime
  save/load、商队、Memory session 连续性
  验收：退出条件 5–7，以及条件 8 中本阶段可取得证据的部分
    （单一 tick 来源、同一 loaded run 内单调、save → reload 同一保存点 tick 一致）
  条件 8 的 Event ↔ Observation 配对需要 GameEvent 与 ObserveRequest，两者都到 10.3-4 才存在，
  因此该配对并入 §16 的 10.3-4 证据链，不在本阶段造一条没有触发源的 Observation 路径

10.3-3  Instance Projection
  建立 dev data root 的 RimWorld profile（§13.1）；archetype:colonist 绑定
  有界 state.rimworld；两个 Pawn 的上下文与记忆隔离证据
  验收：退出条件 9–13

10.3-4  Dialogue
  gizmo → player_interacted_with_npc → present_dialogue → 展示即 SUCCEEDED → settle
  → 玩家回复 → player_said_to_npc
  验收：退出条件 14–18 与 §16 证据链
```

每一步只证明一个明确命题，完成即 commit，不与其他阶段揉在一起。

## 15. 退出条件

```text
1    Mod 无游戏安装也可构建
2    进程内 gRPC 双向流稳定
3    Runtime Core 零 game-specific 改动
4    AdapterHello.supported_extensions 不声明 gameagent.tasks.v1；
     未发送 WorldBinding / WorldClock / Checkpoint

5    world_id 跨 save/load 稳定
6    Pawn entity_id 跨 save/load 与商队稳定，且其确切形式已在实机记录并冻结
7    同一 world_id 内两个不同 eligible colonist 的 entity_id 必须不同
8    Event 与 Observation 使用同一个 tick-only GameTime；同一 loaded run 内单调递增，
     save → reload 同一保存点时间一致
     10.3-2 收口：tick 在适配器里只有一个来源；同一 loaded run 内单调递增；
       save → reload 同一保存点 tick 一致
     配对部分（同一次交互的 Event.game_time.tick 与 Observation.game_time.tick 相等）需要
       GameEvent 与 ObserveRequest 同时存在，二者都在 10.3-4 才出现，故并入 §16 的 10.3-4 证据链

9    eligible colonist 绑定 archetype:colonist；敌人/囚犯/动物/机械/死亡 Pawn 没有 WIA 入口
10   两个 Pawn 的 Context 中实例 traits/背景互不串扰（以 Context trace 为准）
11   两个 Pawn 的 Memory 互不串扰（以存储/投影为准）
12   state.rimworld 有 schema_version，且字段数量与长度上限固定生效
13   同一冻结 snapshot 构建两次 state.rimworld，结构化内容一致
     （skills/traits 排序稳定；比较以结构化内容为准，不以 protobuf wire 字节为准）

14   一次 gizmo 点击恰好产生一次 AgentTurn
15   tick 与普通世界变化不产生 AgentTurn

16   present_dialogue 与 Stardew 同声明：SYNC + SEQUENTIAL + exclusive_per_step + settle_after_success
17   展示成功即 Action SUCCEEDED；关闭窗口不产生回复事件，也不存在悬挂 Action
18   tool_policy 复用结论已记录（非 Stardew-only，暂不提升 proto）
```

第 10、11 条的隔离证据**不以 LLM 输出为准**；模型回复风格差异只作为补充的定性观察。
达到即停下来 review。

## 16. 验收证据链

最终验收不接受"RimWorld 对话成功"这类结论句。必须留下同一存档上的一组结构化证据：

```text
同一个 RimWorld save

  Pawn A   entity_id = pawn:...   definition_id = archetype:colonist   traits = [...]
  Pawn B   entity_id = pawn:...   definition_id = archetype:colonist   traits = [...]
        ↓
  两者共享同一 definition
        ↓
  两次玩家事件（各自 gizmo 触发）
        ↓
  Runtime 分别发出 ObserveRequest 并各自取得 Observation
        ↓
  同一次交互内 Event.game_time.tick == Observation.game_time.tick
    （条件 8 的配对部分；两边的 tick 都取自适配器唯一的时钟来源）
        ↓
  Context trace
    A 只出现 A 的 instance facts 与 A 的 history
    B 只出现 B 的 instance facts 与 B 的 history
        ↓
  两个 Agent 各自选择 present_dialogue
        ↓
  两个 Turn completed
```

这组证据直接证明：

> **WIA Agent 的持久身份来自 world + entity，而不是来自一份静态 persona 文件。**

证据以 trace 与存储为准；模型回复的风格差异只作为补充的定性观察，不作为身份隔离的证明。

## 17. 已知债务与风险

```text
Grpc.Core 已进入 maintenance mode，官方建议迁移到 gRPC for .NET（后者在 Mono 下不可用）。
    这是 RimWorld Adapter 的平台债务，不是 WIA Runtime 的债务；不改变协议与 Runtime。

程序集冲突面：Adapter 会向 RimWorld 的共享 AppDomain 放入 7 个散装程序集，而所有 mod 同域加载。
    已实机验证的范围只有"原版 + 5 个 DLC，无其它 mod"。
    缓解方向是发布前合并为单程序集、只留原生库；不是本阶段的任务。

共享 archetype 的退化风险：模板写得越具体，殖民者越像同一个人。以退出条件 10/11 的确定性证据为准。

对话窗口：RimWorld 侧需要自写窗口，是本阶段唯一的新 UI 工作量。

world lineage：Save As 继承同一 GUID，语义是 world lineage 而非文件 identity，本阶段接受。

definitions distribution：
    本阶段沿用 Stardew 的做法把 definitions 放进 Runtime 发布树；10.3-3 使用全新 dev data root，
    因此当前 seed 机制足够。

    但 Seed() 不是升级机制：agent.json 一旦存在，新版本新增的 game definitions
    不会补进既有 data root。该规则是刻意的——runtime/config/defaults.go 的包注释写明，
    逐文件补齐会"silently become an upgrade mechanism"。

    因此在 10.5 / RimWorld 正式发布前必须重新决定：
      - definitions 随 Adapter 发布；
      - 或 Runtime 引入明确的 definition upgrade / install 机制；
      - 或其他显式安装路径。

    本阶段不解决。它与"definitions 到底属于 Runtime 配置还是 Adapter 仓库"是同一个问题，
    Stardew 已经存在该问题，不是 RimWorld 引入的。

multi-game profile selection：
    10.3 开发期仍使用独立 dev data root。
    第二 Adapter 闭环、公开 RimWorld 支持前，需要由 Local UI 选择 active game / profile
    （届时有两个真实消费者：Stardew profile 与 RimWorld profile）。
    本阶段不实现，也不预先设计其 API。
```

## 18. 明确不做

```text
move_to 及任何异步 / 世界副作用能力
gameagent.tasks.v1 扩展、WorldBinding、WorldClock、Checkpoint
Pawn 之间的社交互动文本替换
自主事件触发（tick、需求阈值、精神崩溃、袭击）
长期任务 / 全部 Needs / 全部 Job / 多 Pawn autonomy
Memory 查看界面
功能对齐 Stardew
拆仓（10.5）与接入检查表（10.6）
game profile 重构
```
