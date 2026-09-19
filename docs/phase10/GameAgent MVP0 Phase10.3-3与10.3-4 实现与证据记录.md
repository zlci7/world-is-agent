# GameAgent MVP0 Phase10.3-3 与 10.3-4 实现与证据记录

> 状态：**已实现，实机验收未执行**。本文如实区分"已有证据"与"待实机确认"，
> 不把只跑了离线测试的写成实机通过。
> 方案见 [Phase10.3 技术方案](GameAgent%20MVP0%20Phase10.3%20RimWorld%20对话接入与世界实例实体技术方案.md)。
> 10.3-1 与 10.3-2 的实机证据见 [10.3-1 实机验收记录](GameAgent%20MVP0%20Phase10.3-1%20实机验收记录.md)。

## 1. 交付物

```text
runtime/config/games/rimworld/definitions/game.json         game_id = rimworld
runtime/config/games/rimworld/definitions/archetype-colonist.json
adapters/rimworld/profile/agent.json                        task.enabled = false

adapters/rimworld/src/Projection/RimWorldSnapshot.cs        无游戏类型的 DTO
adapters/rimworld/src/Projection/ColonistProjection.cs      纯投影（可离线测）
adapters/rimworld/src/Projection/ColonistSnapshotReader.cs  读游戏状态（主线程）

adapters/rimworld/src/Runtime/ProtocolMapper.cs             全部出站消息
adapters/rimworld/src/Runtime/ColonistObservationService.cs 应答 ObserveRequest
adapters/rimworld/src/Dialogue/PresentDialogueRequest.cs    两种合法形态与全部拒绝路径
adapters/rimworld/src/Dialogue/ConversationStore.cs         event_id → conversation_id
adapters/rimworld/src/Dialogue/WiaDialogueWindow.cs         对话窗口
adapters/rimworld/src/Dialogue/DialogueService.cs           gizmo 入口与 present_dialogue 执行
adapters/rimworld/src/Dialogue/WiaDialogueComp.cs           ThingComp + gizmo（含 eligibility gate）
adapters/rimworld/Patches/WiaEntryPoint.xml                 给 Human 的 comps 挂 CompProperties

adapters/rimworld/tests/Projection.Tests/                   33 个离线测试
scripts/install-rimworld-adapter.ps1                        白名单增加 Patches/
```

## 2. 退出条件逐条状态

```text
9    eligible colonist 绑定 archetype:colonist；敌人/囚犯/动物/机械/死亡 Pawn 没有 WIA 入口
     ✅ 绑定：EntityRef.definition_id 固定为 archetype:colonist（ProtocolMapper.ColonistDefinitionId），
        对应 definition 已随 seed 进入 dev data root，Runtime 启动时加载 catalog 未报错
     ⬜ WIA 入口只出现在 eligible colonist 上——需实机确认（negative 侧：敌人/囚犯身上无 gizmo）

10   两个 Pawn 的 Context 中实例 traits/背景互不串扰
     ⬜ 需实机：Context 与 Memory 的隔离只有真实回合才会产生 trace

11   两个 Pawn 的 Memory 互不串扰
     ⬜ 同上

12   state.rimworld 有 schema_version，字段数量与长度上限固定生效
     ✅ 离线测试：SchemaVersionIsReported、EveryFieldIsBoundedNoMatterHowLargeTheInputIs、
        TruncationDoesNotLeaveHalfOfASurrogatePair

13   同一冻结 snapshot 构建两次，结构化内容一致
     ✅ 离线测试：SameSnapshotProjectsToTheSameContent（Struct.Equals 与规范化 JSON 各一次）、
        PermutingTheInputDoesNotChangeTheContent（输入顺序置换后内容不变，强于条件本身）

14   一次 gizmo 点击恰好产生一次 AgentTurn
     ⬜ 需实机

15   tick 与普通世界变化不产生 AgentTurn
     ⬜ 需实机。代码上发事件的路径只有两条——gizmo 点击与窗口回复——没有任何 tick 驱动的事件源，
        但这是代码性质，不是观测结果

16   present_dialogue 与 Stardew 同声明：SYNC + SEQUENTIAL + exclusive_per_step + settle_after_success
     ✅ 声明文本与 Stardew 的 CapabilityCatalog 逐字一致（description、input schema、两个 policy key）
     ✅ Runtime 侧 capability bootstrap accepted=1 catalog=1，invalid_schema / invalid_policy /
        duplicates 全空，零告警

17   展示成功即 Action SUCCEEDED；关闭窗口不产生回复事件，也不存在悬挂 Action
     ✅ 离线测试覆盖两种合法形态与全部非法形态（PresentDialogueParserTests，21 个用例）
     ⬜ "开窗即 SUCCEEDED""关窗不发事件"需实机

18   tool_policy 复用结论已记录
     ✅ 方案 §9.3。本阶段给该结论增加了第二个真实消费者
```

## 3. 已有证据

### 3.1 离线测试

```text
dotnet test adapters/rimworld/tests/Projection.Tests/WiaRimWorld.Projection.Tests.csproj
已通过 - 失败: 0，通过: 33，已跳过: 0，总计: 33
```

测试项目**链接源文件**而不是引用适配器程序集。这不是图省事：只要被链接的文件里出现 RimWorld
类型，测试项目就编译不过，而那正是"这部分必须保持无游戏依赖"这条性质的强制方式。读 Pawn 与画窗口
在边界之外，因此只能实机验证。

### 3.2 实机冒烟（已执行）

```text
compile               0 警告 0 错误（net472）
check-standalone-build 三项全过（仓库外构建、产物不含 Ludeon/Unity 程序集、WIA_PROTOCOL_DIR 反证）
check-architecture    通过（Runtime 与 protocol 零 game-specific 改动）
git diff --check      干净

游戏侧 Player.log
  [WIA] adapter 0.1.0 started on thread 1; game rimworld/1.6.4871 rev591; runtime 127.0.0.1:50051
  [WIA] AdapterHello sent ... session=4efcce52c3844bedaf2d0805f29706cf extensions=(none)
  [WIA] EnvironmentReady received; accepted extensions=(none)
  [WIA] CapabilityList sent: present_dialogue revision=1
  [WIA] main-thread pump drained on thread 1, startup thread was 1, same=True
  XML patch 错误、异常、组件实例化失败：均无

Runtime 侧
  capability bootstrap session_id=4efcce52c3844bedaf2d0805f29706cf revision=1 accepted=1 catalog=1
    skipped_nil=0 invalid_names=[] invalid_schema=[] invalid_policy=[] duplicates=[]
    unsupported_entity_id="" invalid_entity_id=""
  agent core ready: model config <dev root>\config\model.json
```

第 16 条的证据到这里为止：Runtime 接受并编目了 `present_dialogue`，且没有任何 schema 或 policy 告警。
这不等于"模型会选它"，也不等于"窗口会出现"。

### 3.3 开发期 data root

按方案 §13.1 的流程：先让 Runtime 无 `GAMEAGENT_AGENT_CONFIG` 正常 seed，再把
`adapters/rimworld/profile/agent.json` 覆盖到 `<dev root>/config/agent.json`。

```text
<dev root>/config/agent.json                 task.enabled = false
<dev root>/config/model.json                 api_key = env:DEEPSEEK_API_KEY
<dev root>/config/games/rimworld/definitions/  随 seed 写入
<dev root>/config/games/stardew-valley/        同样随 seed 写入（未使用）
```

`agent.json` **不能**放进 `runtime/config/games/rimworld/`：`shippedProfile()` 要求发布树中恰好一份
`games/*/agent.json`，第二份会让 `Seed()` 报错、所有全新 data root 首次启动直接 blocked。

## 4. 待实机验收的步骤

前四条是已记录过的 10.3-2 证据，可直接复用同一个存档。

```text
1  启动 Runtime（dev data root 已按 §3.3 配好）与 RimWorld，载入任意有 ≥2 名殖民者的存档
2  选中一名殖民者 → 命令栏出现「WIA Runtime 已连接」状态的「WIA 对话」gizmo
3  点击 gizmo
   预期适配器侧：[WIA] Observation sent entity=pawn:Thing_Human.. revision=.. tick=..
                 [WIA] ActionResult sent action=.. capability=present_dialogue status=SUCCEEDED
        Runtime 侧：一个 turn，且该 turn 只由这一次点击产生
4  对话窗口出现，显示台词与 3 个回复选项
5  点一个回复
   预期：[WIA] ActionResult sent ... 之后再来一次 turn；
        Runtime trace 中 player_said_to_npc 的 conversation_id 与第 3 步 player_interacted_with_npc 相同
6  再点开另一名殖民者，重复 3–5
   这是条件 10、11 的证据来源：两个 entity_id 各自独立的 Context 与 Memory
7  开一个窗口后直接按 Esc 关闭
   预期：不产生 player_said_to_npc，不产生新的 ActionResult，Runtime 侧无悬挂 action
8  对敌人或囚犯悬停/选中，确认没有 WIA gizmo（条件 9 的 negative 侧）
```

条件 14、15 的判据是 Runtime 侧 trace：**只有** gizmo 点击与窗口回复各产生一次 turn，
tick 前进与世界变化不产生 turn。

## 5. 已知限制

```text
1  对话全程未在实机跑过。README 与本记录都不声称窗口会出现或模型会选中该能力。

2  敌人/囚犯身上不会出现 WIA 入口这一点，靠的是 WiaDialogueComp 自己判断 eligibility，
   而不是靠 def 归属——因为 Human 是所有 humanlike def 的父节点，comp 一定会挂到他们身上。
   代码路径如此，但仍需实机确认没有漏网。

3  窗口关闭时清理的是 Adapter 本地 conversation state。不产生 player_said_to_npc，
   也不产生新的 ActionResult；已完成的那个 Action 早已是 SUCCEEDED 终态，因此没有悬挂 action。

4  ConversationStore 的 event_id → conversation_id 映射上限 64 条，超出后按最旧先淘汰。
   若某个 turn 在到达 present_dialogue 之前就失败，它的 event id 永不被回引，靠这个上限兜住。

5  Observation.revision 是每个 entity 的递增计数，不是游戏侧事实。Runtime 不比较它，
   它的作用是让一次陈旧的读取在 trace 里看得出来。

6  state.rimworld 的字段与上限按方案 §7.1 冻结。health 的三值是 normal / injured / downed，
   injured 定义为存在 Hediff_Injury；永久性缺失部位或成瘾不算 injured。
```

## 6. 本阶段不做的事

```text
不移动 Pawn、不排 Job、不征召、不改变任何游戏 AI —— ownership 边界最保守的一档
不产生任何非玩家触发的事件；tick 与世界变化只更新 projection
不协商 gameagent.tasks.v1，因此不发送 WorldBinding / WorldClock / Checkpoint
不收录：全部 Hediff / Thought / Skill / Relation / Inventory、附近所有 Pawn、整个世界地图
```
