# GameAgent MVP0 Phase10.3-3 与 10.3-4 实现与证据记录

> 状态：**已实现并实机验收**。三个殖民者的完整对话在实机跑通，证据见 §3。
> 方案见 [Phase10.3 技术方案](GameAgent%20MVP0%20Phase10.3%20RimWorld%20对话接入与世界实例实体技术方案.md)。
> 10.3-1 与 10.3-2 的证据见 [10.3-1 实机验收记录](GameAgent%20MVP0%20Phase10.3-1%20实机验收记录.md)。

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
     ✅ 绑定：EntityRef.definition_id 固定为 archetype:colonist（ProtocolMapper.ColonistDefinitionId）

     ✅ 正向实机：gizmo 出现在玩家自己的殖民者身上，且 Runtime 连接时可用、断开时置灰并给出原因
     ⬜ 负向未验证，**经用户决定不再要求**：验收过程中始终没有一局带囚犯、访客或袭击者的存档，
        因此"他们身上没有 gizmo"没有被观察到。这一条以代码保证为准——`WiaDialogueComp.
        CompGetGizmosExtra()` 自己判 eligibility，不依赖 comp 挂在哪个 def 上——但它**不是**
        实机已验证项，本文不把它记成已验证。若将来引入一局带囚犯的存档，补一次即可闭合。

10   两个 Pawn 的 Context 中实例 traits/背景互不串扰（以 Context trace 为准）
     ✅ 三个实体各自产生独立 turn，trace 中每条记录都带自己的 entity_id
     ✅ 每次对话的台词都取自本人投影：Mighty（背景"实验品"）说"别往我身上塞什么机械玩意"，
        第二轮准确报出自己"枪练到七级、盖东西只有三级"，即 observation 里的 skills 被模型读到
     ⚠️ 未做逐字段的 Context 内容 dump：trace 记录的是 token 计数与来源 id，不是上下文全文。
        更强的证据需要打开 trace 的正文记录，见 §5 限制 1。

11   两个 Pawn 的 Memory 互不串扰
     ✅ 两个实体在同一 world 下引用**不同的 history source id**（§3.3），
        且 Runtime 的 history maintenance 分别以两个 AgentSessionKey 记账
     ✅ 检索在 SQL 层就按实体收窄：`runtime/internal/memory/sqlite_search.go:343` 的查询带
        `WHERE h.game_id=? AND h.world_id=? AND h.entity_id=?`，schema 上也有
        `(game_id, world_id, entity_id, sequence)` 索引（`sqlite_migrations.go:31`）。
        属于另一个实体的记忆记录不可能被这条上下文选中——这比抽一次样本更强。
     ⚠️ 仍未做 memory.db 的逐行 dump：本机没有 sqlite3 客户端。曾尝试用
        `BuildProjectionBatchKey` 复算 history source id 来反证归属，复算值与 trace 中的取值
        不一致（16 种 kind/version/turn 组合都不匹配），因此那条捷径不作为证据提出

12   state.rimworld 有 schema_version，且字段数量与长度上限固定生效
     ✅ 离线测试：SchemaVersionIsReported、ConfiguredTextFieldsAndCollectionsAreBounded、
        TruncationDoesNotLeaveHalfOfASurrogatePair

13   同一冻结 snapshot 构建两次，结构化内容一致
     ✅ 离线测试：SameSnapshotProjectsToTheSameContent（Struct.Equals 与规范化 JSON 各一次）、
        PermutingTheInputDoesNotChangeTheContent（输入顺序置换后内容不变，强于条件本身）

14   一次 gizmo 点击恰好产生一次 AgentTurn
     ✅ 实机：6 次玩家动作（3 次 gizmo 点击 + 3 次回复）产生恰好 6 个 turn，
        每个 turn steps=1、tools=[present_dialogue]

15   tick 与普通世界变化不产生 AgentTurn
     ✅ 实机：同一时段 clock 从 tick=1 走到 14001 以上，期间没有任何由 tick 或世界变化产生的 turn；
        全部 turn 都能对应到一次玩家动作

16   present_dialogue 与 Stardew 同声明：SYNC + SEQUENTIAL + exclusive_per_step + settle_after_success
     ✅ 声明文本与 Stardew 的 CapabilityCatalog 逐字一致（description、input schema、两个 policy key）
     ✅ Runtime 侧 capability bootstrap accepted=1 catalog=1，invalid_schema / invalid_policy /
        duplicates 全空，零告警
     ✅ 每个 turn 的 available_tools 都恰好是 [present_dialogue]，settled_by=present_dialogue，
        即 settle_after_success 真的被 Runtime 消费了

17   展示成功即 Action SUCCEEDED；关闭窗口不产生回复事件，也不存在悬挂 Action
     ✅ 实机：每次 present_dialogue 都返回 status=Succeeded，并且是在窗口出现时返回的
     ✅ 实机：按 Esc 关闭窗口后，新增 turn 数为 0，没有 player_said_to_npc、没有新的 ActionResult
     ✅ 离线测试覆盖两种合法形态与全部非法形态（PresentDialogueParserTests，22 个用例），
        其中 `3 options + allow_free_text=false` 是本轮 CR 补上的回归用例

18   tool_policy 复用结论已记录
     ✅ 方案 §9.3。本阶段给该结论增加了第二个真实消费者
```

## 3. 实机证据

原始记录见 [a3-a4-dialogue-in-game.txt](../../adapters/rimworld/tests/evidence/a3-a4-dialogue-in-game.txt)，
截图见同目录的 `a3-a4-01-gizmo.jpg` 与 `a3-a4-02-dialogue.jpg`。

### 3.1 一次完整交换

```text
适配器侧（Player.log）
  [WIA] event accepted event=event-3-8c08f2b6178f4f679b3e03395f4f97ca
  [WIA] Observation sent entity=pawn:Thing_Human272 revision=1 tick=6293 world=4a8e66a23b0e4e91966cf322dcc7e2a7
  [WIA] ActionResult sent action=act_1789835488237874100_8 capability=present_dialogue status=Succeeded
  [WIA] turn completed turn=turn_1789835486537571200_5 status=Completed
        ← 玩家点了回复
  [WIA] event accepted event=event-7-477081f67c8e4888be9527ed80370901
  [WIA] Observation sent entity=pawn:Thing_Human272 revision=2 tick=8734 world=4a8e66a23b0e4e91966cf322dcc7e2a7
  [WIA] ActionResult sent action=act_1789835528992555700_15 capability=present_dialogue status=Succeeded
  [WIA] turn completed turn=turn_1789835527226880200_12 status=Completed
```

### 3.2 Runtime 侧的 turn 列表

```text
6 个 turn，全部 game_id=rimworld，全部 steps=1，tools=[present_dialogue]，
available_tools=[present_dialogue]，settled_by=present_dialogue，status=completed

  player_said_to_npc          entity=pawn:Thing_Human272   ← World 4a8e66a2…
  player_interacted_with_npc  entity=pawn:Thing_Human272
  player_said_to_npc          entity=pawn:Thing_Human508
  player_interacted_with_npc  entity=pawn:Thing_Human508
  player_said_to_npc          entity=pawn:Thing_Human505
  player_interacted_with_npc  entity=pawn:Thing_Human505
```

三个殖民者（Mighty / Hays / Brash）跨两个 world、两次游戏进程，各自一问一答。

### 3.3 Trace 与记忆归属

```text
trace 事件类型 19 种，每种恰好 ×6（6 个 turn 各一份），结构完全对称。

context_request_built 中每次 turn 引用的 history source：
  entity=pawn:Thing_Human505  turn=…18  history_sources_retained=1  history_4d31240c…
  entity=pawn:Thing_Human508  turn=…32  history_sources_retained=1  history_9efb6c9e…
  （首轮 turn 的 history_sources_retained=0，diagnostics=search_no_valid_query，符合"还没有历史"）

Runtime 的 history maintenance 以两个不同的 AgentSessionKey 分别记账：
  owner=game="rimworld" world="0ccc4d0f…" entity="pawn:Thing_Human505"
  owner=game="rimworld" world="0ccc4d0f…" entity="pawn:Thing_Human508"
```

### 3.4 离线测试

```text
dotnet test adapters/rimworld/tests/Projection.Tests/WiaRimWorld.Projection.Tests.csproj
已通过 - 失败: 0，通过: 33，已跳过: 0，总计: 33
```

测试项目**链接源文件**而不是引用适配器程序集。这不是图省事：只要被链接的文件里出现 RimWorld
类型，测试项目就编译不过，而那正是"这部分必须保持无游戏依赖"这条性质的强制方式。读 Pawn 与画窗口
在边界之外，因此只能实机验证。

### 3.5 仓库级检查

```text
compile                0 警告 0 错误（net472）
check-standalone-build 三项全过（仓库外构建、产物不含 Ludeon/Unity 程序集、WIA_PROTOCOL_DIR 反证）
check-architecture     通过（Runtime 与 protocol 零 game-specific 改动）
```

## 4. 复现步骤

```text
1  按方案 §13.1 准备 dev data root：先让 Runtime 无 GAMEAGENT_AGENT_CONFIG 正常 seed，
    再把 adapters/rimworld/profile/agent.json 覆盖到 <dev root>/config/agent.json，
    并放入可用的 config/model.json（agent core 必须 ready，否则不会有任何对话）
2  go run ./runtime/cmd/server --data-root <dev root> --no-open
3  scripts/install-rimworld-adapter.ps1 -GamePath <RimWorld install>
4  启动 RimWorld。用 -quicktest 可以直接得到一局带若干殖民者的测试殖民地；
    它同时也会打印版本行，因此不需要手动开新档
5  选中一名殖民者（键盘 . / , 可以循环切换，比点精灵稳定）
   → 命令栏出现「WIA 对话」
6  点击 → 窗口出现台词与 3 个回复；点回复 → 第二次 turn
7  按 Esc 关闭窗口 → 不应有新的 turn
```

## 5. 已知限制

```text
1  条件 10、11 的证据到"每个实体有自己的 turn、自己的 history source、自己的历史记账"为止。
    更强的逐字段 Context dump 需要 trace 记录上下文正文，而当前 trace 只记录 token 计数与
    来源 id。二者都指向隔离成立，但严格说这是间接证据。

2  条件 9 的负向（敌人/囚犯/访客身上没有 gizmo）本次未观测，见 §2 的说明。

3  敌人/囚犯身上不会出现 WIA 入口这一点，靠的是 WiaDialogueComp 自己判断 eligibility，
    而不是靠 def 归属——因为 Human 是所有 humanlike def 的父节点，comp 一定会挂到他们身上。

4  窗口关闭时清理的是 Adapter 本地 conversation state。不产生 player_said_to_npc，
    也不产生新的 ActionResult；已完成的那个 Action 早已是 SUCCEEDED 终态，因此没有悬挂 action。

5  ConversationStore 的 event_id → conversation_id 映射上限 64 条，超出后按最旧先淘汰。
    若某个 turn 在到达 present_dialogue 之前就失败，它的 event id 永不被回引，靠这个上限兜住。

6  Observation.revision 是每个 entity 的递增计数，不是游戏侧事实。Runtime 不比较它，
    它的作用是让一次陈旧的读取在 trace 里看得出来。

7  state.rimworld 的字段与上限按方案 §7.1 冻结。health 的三值是 normal / injured / downed，
   injured 定义为存在 Hediff_Injury；永久性缺失部位或成瘾不算 injured。
```

## 6. 本阶段不做的事

```text
不移动 Pawn、不排 Job、不征召、不改变任何游戏 AI —— ownership 边界最保守的一档
不产生任何非玩家触发的事件；tick 与世界变化只更新 projection
不协商 gameagent.tasks.v1，因此不发送 WorldBinding / WorldClock / Checkpoint
不收录：全部 Hediff / Thought / Skill / Relation / Inventory、附近所有 Pawn、整个世界地图
```

## 7. CR 修正轮次

首次交付后的一轮代码评审指出三个必须修的问题与两个准确性收尾，均已处理。

### 7.1 `present_dialogue` 与 Stardew 的语义分歧（必修）

原实现按 `reply_options` 的条数分形态，于是：

```text
reply_options = 3 条 + allow_free_text = false   → 本适配器接受
```

而 Stardew 的 `ProtocolMapper.Core.RequirePresentDialogueArgument` 是按 `allow_free_text` 分形态的：

```csharp
if ( allowFreeText && replyOptions.Length != MaxReplyOptions) throw;
if (!allowFreeText && replyOptions.Length != 0)             throw;
```

也就是说这个形态在 Stardew 被拒。**这正是本阶段最重要的论点被削弱的地方**：两个 Adapter 复用的是
同一个 capability contract，那么接受与拒绝的形状必须一致。修法是把判断改成按 `allow_free_text`
分支，与 Stardew 逐行同构，并补一条回归用例
`ThreeOptionsWithFreeTextRefusedIsRejected`（该用例在修复前会失败）。

### 7.2 Conversation 生命周期（必修）

三处，都属于同一件事：会话状态必须跟得上"这次事件到底成没成"。

```text
1  closed 不得被旧 source_event_id 重开
   Execute 过去只做 TryResolve 就直接开窗。event binding 有意在 Close 后保留（为了让迟到的
   ActionRequest 拿到明确答复而不是让 Runtime 一直等），但保留 binding ≠ 会话还活着。
   现在补上 IsOpen 检查，不满足返回 REJECTED conversation_closed。

2  同一 Pawn 不得并发开多个会话
   BeginConversation 现在先查该 entity 是否已有 open conversation，有则拒绝并告诉玩家。
   否则连点会连开多个会话，而 Runtime 在同一 entity lane 上一次只跑一个 turn，
   多出来的排队或被丢弃，玩家看到的却是更早那次点击的窗口。

3  event 真正失败时要收摊
   EventAck.REJECTED、TurnCompletion FAILED/CANCELLED、以及 detached write 失败，
   现在统一走 DialogueService.AbandonEvent：关掉 store 里的会话、关掉窗口、给玩家一条消息。
   EventAck.DUPLICATE 不收摊——那说明 Runtime 已经收下这个事件、turn 正在跑。
```

这一条同时消掉了原实现里最难看的一种沉默：点击被交给了一个活跃会话（于是 calller 回答"已受理"），
随后写入失败，玩家等一个永远不会来的 turn。原代码注释自己写着"a click that does nothing is the
worst possible answer"，而现在发送路径也确实按这句话做了。

### 7.3 `game.json` 的事实错误（必修）

```text
删除  "The colony exists on one map at a time..."
      RimWorld 可以同时存在多个 colony map、临时 quest map 与 caravan，这条是错的
改为  "A colonist may be present on a map or travelling off-map in a caravan;
       different colonists may be on different maps."

弱化  "Skills and traits ... are learned and acquired during play, not chosen by the player"
      traits 与背景在 Pawn 生成时就确定，skills 也可能有初始值
改为  "Traits, backgrounds and skills are properties of the generated colonist,
       and they shape how that colonist works and speaks."
```

GameDefinition 是给模型看的事实源，这里的事实错误比 README 的笔误更值得修。

### 7.4 P2 收尾

```text
测试名  EveryFieldIsBoundedNoMatterHowLargeTheInputIs
        → ConfiguredTextFieldsAndCollectionsAreBounded
        entity id / map id / def name 本来就没有设长度上限（它们只由游戏自身的标识符填充），
        旧名字比实现声称得更多
```

### 7.5 修正后的实机复验

在修正后的构建上重跑（全新 dev data root，真实模型调用）：

```text
点击 gizmo → 恰好 1 个 turn，event accepted，Observation revision=1，present_dialogue Succeeded
窗口显示该殖民者的台词与 3 个回复
回复        → 恰好 1 个 turn，event accepted，Observation revision=2
窗口开着时再点 gizmo → 0 个新 turn（并发保护生效）
```

台词仍取自本人投影：Feeb（22 岁、健康一瘸一拐）说"我在，身上这伤还没好，走快了也疼。

### 7.6 完整回归

```text
go test ./... -p 1 -count=1                全部包 ok，退出码 0
dotnet test adapters/rimworld/tests/...    36 通过 / 0 失败
check-standalone-build.ps1                 三项全过
check-architecture.ps1                     通过
git diff --check                           干净
```

## 8. CR 第二轮：Turn 正常 Completed 但模型没有调用 present_dialogue

第二轮评审在上一轮的清理逻辑里指出一个缺口：`HandleTurnCompletion` 对 `Failed` 与
`Cancelled` 收摊，对 `Completed` 什么都不做。而 Runtime 允许模型**不带任何 tool call 直接
settle**，那次 turn 同样以 `Completed` 结束。于是：

```text
conversation 仍然 open
没有 dialogue window
turn 已经 completed
→ 玩家再点这个殖民者：「这位殖民者已经在对话中」
→ 而这个会话没有任何正常关闭入口
```

这是一个永久卡死的殖民者，不是概率问题：只要模型有一次不按预期调用工具就会发生。

### 修法

问题在于「会话还开着」不等于「玩家看到了东西、并且在等回复」。所以 store 现在按 **turn**
记录一个 `AwaitingPresentation`：

```text
Begin(打开对话)              → open, AwaitingPresentation = true
Bind(玩家回复，开新 turn)     → AwaitingPresentation = true
MarkPresented(present 成功)   → AwaitingPresentation = false
Close(模型选择结束形态 / 玩家关窗) → 移出 open

TurnCompletion.Completed → CompleteTurn(event_id)
    会话不在 open            → 什么都不做
    open 且 AwaitingPresentation → 这一回合没产生对话 → 关闭 + 给玩家一条消息
    open 且 !AwaitingPresentation → 正常，窗口正等着玩家回复，保持 open
```

这样初始点击的 turn 与玩家回复的 turn 都安全：两者都会先经过 `present_dialogue`，
只有真正没有产出对话的那次会被收掉。

### 回归用例

```text
ATurnThatCompletesWithoutPresentingEndsTheConversation
ATurnThatPresentedKeepsTheConversationOpen
AReplyTurnThatPresentsNothingEndsTheConversation
CompletingAnUnknownOrClosedConversationChangesNothing
```

第一条同时验证了「下一个 gizmo 点击可以正常工作」——这正是这个 bug 的可见症状。

### 本轮同时收掉的两处 P2 文字

```text
本记录 §2 条件 12 仍引用改名前的测试名，已同步为 ConfiguredTextFieldsAndCollectionsAreBounded
docs/STATUS.md 的 Automatic reconnect 一行仍写 "future work"，
        而 RimWorld 适配器的断线重连已在 10.3-1 实机验证，已改为如实描述
```

### 仍未闭合

```text
退出条件 6 的远行队部分   已闭合：Map → 远行队 → Map 一次完整往返，entity_id 不变，
                          证据见 10.3-2 实机验收记录 §4
退出条件 9 的负向         未验证，经用户决定不再要求（见 §9）
```

## 9. 验收结论

用户于 2026-09-20 复核后决定：**10.3 认定验收通过**，条件 9 的负向部分不再要求。

这里如实区分两件事：**豁免不是验证**。条件 9 的负向没有实机证据，它在本文与
`docs/STATUS.md` 中都记为未覆盖，只是不再作为 10.3 的阻塞项。

```text
10.3-1  Skeleton                ✅ 实机验收
10.3-2  Identity + Time         ✅ 实机验收（条件 5–8 全部闭合）
10.3-3  Instance Projection     ✅ 实机验收（条件 12、13 离线证；10、11 结构性证据；
                                   9 负向未验证且豁免）
10.3-4  Dialogue                ✅ 实机验收（条件 14–18 全部闭合）

跨阶段不变量
        Runtime 的 Go 代码与 protocol  本轮零 game-specific 改动
        tool_policy 的第二消费者        已成立，结论继续保留在 extensions（方案 §9.3）
```

据此 **Phase10.3 Second Real Game Adapter — Accepted**，下一步进入 10.4 Game Profile Selection。
