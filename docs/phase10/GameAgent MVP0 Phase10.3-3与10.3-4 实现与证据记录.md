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
     ⚠️ 负向未直接观测：验收用的殖民地地图上没有敌人、囚犯或访客，因此"他们身上没有 gizmo"
        这件事本次没有被观察到，它目前只有代码层面的保证（CompGetGizmosExtra 自己判 eligibility，
        而不是依赖 comp 挂在哪个 def 上）。补测需要一局带囚犯或袭击的存档。

10   两个 Pawn 的 Context 中实例 traits/背景互不串扰（以 Context trace 为准）
     ✅ 三个实体各自产生独立 turn，trace 中每条记录都带自己的 entity_id
     ✅ 每次对话的台词都取自本人投影：Mighty（背景"实验品"）说"别往我身上塞什么机械玩意"，
        第二轮准确报出自己"枪练到七级、盖东西只有三级"，即 observation 里的 skills 被模型读到
     ⚠️ 未做逐字段的 Context 内容 dump：trace 记录的是 token 计数与来源 id，不是上下文全文。
        更强的证据需要打开 trace 的正文记录，见 §5 限制 1。

11   两个 Pawn 的 Memory 互不串扰
     ✅ 两个实体在同一 world 下引用**不同的 history source id**（§3.3），
        且 Runtime 的 history maintenance 分别以两个 AgentSessionKey 记账
     ⚠️ 同条件 10 的保留意见：证据到"来源不同"为止，没有做 memory.db 的逐行比对

12   state.rimworld 有 schema_version，且字段数量与长度上限固定生效
     ✅ 离线测试：SchemaVersionIsReported、EveryFieldIsBoundedNoMatterHowLargeTheInputIs、
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
     ✅ 离线测试覆盖两种合法形态与全部非法形态（PresentDialogueParserTests，21 个用例）

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
