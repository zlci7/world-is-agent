# GameAgent MVP0 Phase6.5 技术开发与验收方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-01
> **Scope:** Stardew Dialogue Interaction Convergence
> **Architecture Baseline:** GameAgent Runtime Architecture v0.6
> **Roadmap Baseline:** GameAgent 阶段规划 v0.9
> **Previous Phase:** Phase6 Async Action Lifecycle and AgentTurn Resume
> **Reference:** ValleyTalk, SMAPI 4.5.2

---

# 1. 阶段目标

Phase5.6 已经建立 Stardew 对话 UI、`present_dialogue`、玩家回复事件、`ContextFact` 和跨 Turn conversation。

Phase6 已经建立 Runtime async action lifecycle、`TurnCompletion`、Action source correlation、Interaction Context Guard 和 `move_to` vertical slice。

Phase6.5 要收敛 Stardew 玩家点击 NPC 后的对话体验：

> **玩家点击 NPC 默认进入稳定的对话域；Adapter 通过 Tool View、UI 默认值、waiting menu、点击重入门禁和 effect-time guard 保证对话由玩家自然推进和结束。**

本阶段解决的问题不是 Runtime async 主干，而是 Stardew Adapter 的交互语义：

```text
Player clicks NPC
  -> Adapter accepts one active interaction for that NPC
  -> Runtime chooses an available capability
  -> Adapter presents one coherent dialogue surface
  -> Player chooses option, types free text, or closes dialogue
  -> Adapter emits the next GameEvent or ends the conversation
```

---

# 2. 阶段结论

Phase6.5 做这些工作：

```text
1. Stardew 玩家点击 NPC 的主输出能力收敛为 present_dialogue。
2. Stardew 当前生产 Tool View 不再暴露 speak。
3. 单句 NPC 台词也通过 present_dialogue 表达；无 reply_options 且 allow_free_text=false 表示说完即结束。
4. present_dialogue 缺省 allow_free_text=true。
5. reply_options 与自由输入 UI 稳定共存；自由输入不依赖模型显式打开。
6. PlayerInteractProbe 在 source time 使用与 Interaction Context Guard 一致的距离规则。
7. 同一 NPC 在 pending 或 committed interaction 完成前拒绝重复点击进入新 interaction。
8. Runtime 返回前使用 Stardew activeClickableMenu waiting surface 锁住玩家输入。
9. Adapter ActionResult 日志记录 rejected / failed / cancelled 的 code 与 message。
10. 保持 Runtime Core game-agnostic，不新增 Runtime 对 Stardew capability name 的执行分支。
```

Phase6.5 不做这些工作：

```text
Protocol 字段变更
Runtime async lifecycle 重写
ActionBatchRequest / ActionBatchResult
Runtime 路径规划
自然语言地点解析
Stardew vanilla dialogue Harmony patch
Adapter 内部 LLM
ValleyTalk prompt builder 迁移
长期 conversation persistence
Runtime 崩溃后的 continuation 恢复
AgentDefinition store
按游戏事件动态生成 Tool View
```

---

# 3. ValleyTalk 参考结论

ValleyTalk 的关键做法：

```text
1. 先判断是否接管 Stardew 原生对话。
2. 接管后统一进入对话生成路径。
3. LLM 输出第一行作为 NPC 台词。
4. LLM 输出的后续行作为玩家回复选项。
5. typed response 由配置和 UI 层决定，不依赖模型额外声明。
6. 生成期间维护 awaiting generation 状态，避免重复交互重入。
```

Phase6.5 借鉴以下设计：

```text
统一 dialogue surface
选项与自由输入由 Adapter UI 稳定展示
对话生成等待期间有明确 in-flight 状态
生成等待期间使用原生菜单锁住玩家输入
普通点击 NPC 默认进入对话，而不是随机选择单句工具
```

Phase6.5 不迁移以下实现：

```text
Harmony patch 替换 Stardew 原生对话
Adapter 内部调用 LLM
概率式 PatchNpc 接管策略
完整 ValleyTalk prompt builder
送礼、婚姻、节日等专用对话生成链路
```

---

# 4. 架构边界

## 4.1 Runtime 与 Adapter 分工

Runtime Core 继续只理解通用契约：

```text
CapabilityList
Capability.description
Capability.input_schema
Capability.execution_mode
Capability.concurrency_mode
Capability.extensions.gameagent.tool_policy
GameEvent
ContextFact
ActionRequest
ActionResult
TurnCompletion
```

Stardew Adapter 负责：

```text
Stardew-specific Tool View
Capability description
对话 UI
玩家点击与输入事件
conversation state
主线程调度
source-time interaction gate
effect-time interaction guard
NPC movement/pathfinding
ActionResult code/message
```

Runtime 不根据 `present_dialogue`、`move_to`、`emote` 等具体 capability name 写执行策略。`present_dialogue` 的独占执行和成功后 settle 继续来自 `Capability.extensions.gameagent.tool_policy`。

## 4.2 Stardew Tool View 收敛

Phase6.5 的 Stardew 生产 Tool View：

```text
present_dialogue
emote
face_player
move_to
```

`speak` 不进入 Stardew 当前生产 Tool View。

原因：

```text
present_dialogue 与 speak 都能让 NPC 显示台词。
玩家点击 NPC 后通常期望可以回复。
两个相似工具同时暴露会让模型在对话主路径里随机分流。
```

单句 NPC 台词使用：

```json
{
  "text": "See you around.",
  "reply_options": [],
  "allow_free_text": false
}
```

该调用显示 NPC 台词并在展示完成后关闭 active conversation。

Phase6.5 不引入 session-scoped capability registry。Runtime 当前 registry 是进程内累积状态；实机验收必须在重新编译 Adapter 后冷启动 Runtime，使 Stardew CapabilityList 从 fresh registry 注册。长期 session-scoped Tool View 属于 Phase7+。

## 4.3 Capability Name

Stardew 对话主能力名称保持：

```text
present_dialogue
```

命名语义：

```text
present_dialogue = Adapter 在游戏 UI 中呈现一段 NPC dialogue surface。
```

该能力覆盖两种 Stardew 对话输出：

```text
可回复对话：text + reply_options 或 allow_free_text=true
单句结束：text + reply_options=[] + allow_free_text=false
```

`presented dialogue` 是 Runtime Recent Memory 的可见摘要措辞，不作为 capability name。

## 4.4 present_dialogue 默认语义

`present_dialogue` 是 Stardew 玩家对话的主能力。

输入：

```text
text: string, required, max 240 chars
reply_options: string[], optional, max 3 items, max 80 chars each
allow_free_text: boolean, optional, default true
```

规则：

```text
- 缺省 allow_free_text=true；
- 最多显示 3 个 reply_options；
- allow_free_text=true 时显示自由输入入口；
- reply_options 为空且 allow_free_text=true 时仍显示自由输入入口；
- reply_options 为空且 allow_free_text=false 时显示单句 NPC 台词，并在展示完成后关闭 active conversation；
- 玩家选择 option 或提交 free text 后发送 player_said_to_npc；
- 玩家关闭或取消对话时关闭 active conversation，不发送 player_said_to_npc。
```

`present_dialogue` 的 `ActionResult.output`：

```json
{
  "conversation_id": "conv_12",
  "displayed_text": "Want to walk over here?",
  "reply_options_count": 3,
  "allow_free_text": true
}
```

## 4.5 UI 行为

Phase6.5 的 Stardew 对话 UI 必须稳定支持：

```text
NPC 台词
0-3 个回复选项与自由输入框
0-3 个回复选项且无自由输入框
提交按钮
关闭按钮
Escape 取消
```

玩家能主动结束对话：

```text
Close button
Escape
single-line present_dialogue with allow_free_text=false after NPC line display
```

Stardew Adapter 固定最多显示 3 个生成选项。`allow_free_text=true` 时额外显示自由输入入口；`allow_free_text=false` 时不显示自由输入入口。

## 4.6 Interaction In-flight Gate

Adapter 维护每个 NPC 的 interaction in-flight 状态。

Phase6.5 复用 `InteractionContextStore` 作为 interaction 生命周期的唯一状态入口。该 store 内部同时维护：

```text
eventId -> InteractionContextSnapshot / pending|committed
InteractionKey(world_id,npc_entity_id,player_entity_id) -> eventId
```

`eventId` 维度服务 effect-time guard，`InteractionKey` 维度服务 source-time in-flight gate。

状态：

```text
none
pending
committed
```

进入与转移：

```text
- queued / reserved player_interacted_with_npc -> pending in-flight
- queued / reserved player_said_to_npc -> pending in-flight
- player_said_to_npc 可在同一 conversation 内从已 committed 的 player_interacted_with_npc 原子 handoff 到新的 pending in-flight
- EventAck(ACCEPTED) -> committed in-flight
- EventAck(REJECTED) -> release matching pending in-flight
- EventAck(DUPLICATE) -> release matching pending in-flight
- TurnCompletion(COMPLETED / FAILED) -> release matching committed in-flight
- player abandon close dialogue UI -> release matching in-flight
- world change / day started / disconnect / reconnect -> release all in-flight
```

同一 NPC 已有 in-flight interaction 时，新的普通点击不发送 `player_interacted_with_npc`。

该规则只限制 Stardew 玩家点击交互，不阻止 Runtime 已经启动的 ActionStatusUpdate、ActionResult 或 TurnCompletion 被处理。

`abandon close` 只表示玩家点击 Close 或按 Escape 放弃对话。玩家提交 option 或 free text 时也会关闭当前菜单，但这属于 submit close；submit close 不释放 in-flight，后续 `player_said_to_npc` Turn 必须等 Runtime 发送 TurnCompletion 后释放。

`player_said_to_npc` 的 `EventAck` 收敛规则：

```text
- ACCEPTED：提交 pending player line，保持 active conversation，等待 TurnCompletion 释放 in-flight；
- REJECTED：丢弃 pending player line，释放匹配 pending in-flight，关闭 active conversation，并记录 rejected reason；
- DUPLICATE：不追加 pending player line，不释放其它已 committed context；若当前 event 仍有 pending mutation，则丢弃 pending mutation、释放匹配 pending in-flight，并记录 duplicate reason。
```

提交 option / free text 早于原 `player_interacted_with_npc` TurnCompletion 到达时，Adapter 不丢弃提交事件；`InteractionContextStore` 将同一 conversation 的 in-flight 从原事件交接到新的 `player_said_to_npc` 事件，原事件的 late TurnCompletion 不释放新事件。

## 4.7 Source-time 与 Effect-time Guard

Source-time gate 由 `PlayerInteractProbe` 调用 `RuntimeClient` 的同步 gate / queue 方法完成。gate 的 query 与 pending reserve 在主线程同步发生，成功 reserve 后再 suppress Stardew 原生输入。

```text
- Runtime 未 ready 时不发送事件；
- world context 不可用时不发送事件；
- active menu 或 dialogueUp 时不发送事件；
- 玩家与 NPC 距离超过 MaxInteractionDistance 时不发送事件；
- 同一 NPC interaction in-flight 时不发送事件。
```

source-time gate 成功后，Adapter 展示本地 waiting menu。waiting menu 使用 `Game1.activeClickableMenu` 锁住玩家输入，直到 Runtime 返回对应 interaction 的 `ActionRequest`，或该 interaction 被 reject / duplicate、发送失败、Turn failed / cancelled、断线或 world reset。

waiting menu 是 Stardew Adapter 的 UX surface，不改变 Runtime / Protocol 契约，也不承诺暂停整个游戏世界或冻结 NPC 对象。Adapter 收到任何 `ActionRequest` 后先关闭 waiting，再执行 capability；`present_dialogue` 会接着打开 Stardew 对话 UI，`move_to` 会让游戏世界继续 tick 以推进 NPC pathfinding。

Interaction context snapshot 只保存 source-time facts，例如 world、entity、conversation、location、tile 和距离阈值。是否执行 proximity guard 是 effect-time action policy，由 action handler 显式传入。

Effect-time guard 位于 capability 执行前：

```text
present_dialogue
move_to
```

校验：

```text
world_id
entity_id
player_entity_id
conversation_id
location
player/NPC distance, only for actions that require proximity
```

`present_dialogue` 只校验 world、entity、conversation 和 location。它允许 Runtime / LLM 响应等待期间的同场景内距离漂移。

`move_to` 等真实物理动作保留 proximity guard。启动交互的 source-time click 仍然需要距离校验，确保对话从有效交互开始。

Effect-time guard 失败时返回：

```text
ActionResult.status = REJECTED
ActionResult.message = concrete reason
```

错误码：

```text
interaction_context_missing
interaction_context_world_changed
interaction_context_entity_changed
interaction_context_player_changed
interaction_context_conversation_changed
interaction_context_npc_location_changed
interaction_context_player_location_changed
interaction_context_distance_changed
```

`interaction_context_distance_changed` 只用于需要 proximity guard 的 effect-time action。

## 4.8 Observability

Adapter 日志必须能看清以下事实：

```text
CapabilityList sent: present_dialogue, emote, face_player, move_to
player_interacted_with_npc ignored: reason=runtime_not_ready | world_unavailable | too_far | interaction_in_flight | active_menu/dialogue
ActionRequest received: capability, action_id, source_event_id, source_turn_id
ActionResult sent: capability, status, code, message
TurnCompletion received: status, event_id, turn_id
interaction context released: event_id, entity_id
```

Rejected / failed / cancelled action 必须打印 code 与 message。

## 4.9 Recent Memory 摘要边界

Phase6.5 不改 Runtime `visibleActionSummary` 的摘要模型。

当前 `present_dialogue` 的 Recent Memory 摘要使用：

```text
presented dialogue "<text>"
```

该摘要表示 NPC 在游戏对话 UI 中呈现了台词。更严格的多游戏摘要泛化由后续 `Capability.extensions.gameagent.visible_summary` 或等价 metadata 处理。

---

# 5. 开发里程碑

## M1：Stardew Tool View 收敛

目标：

```text
Stardew 生产 CapabilityList 不再暴露 speak，玩家交互默认由 present_dialogue 承担。
```

修改范围：

```text
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/Capabilities/SpeakCapability.cs
adapters/stardew/src/ModEntry.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
adapters/stardew/tests/check-context-static.ps1
```

实现要求：

```text
- CapabilityCatalog.BuildEnvironmentCapabilities() 不返回 speak；
- RuntimeClient 不处理生产 speak ActionRequest；
- SpeakCapability 从生产注册、注入和 dispatch wiring 移除；
- present_dialogue description 明确它是 Stardew NPC dialogue surface；
- present_dialogue description 明确结束对话必须显式传 allow_free_text=false 且 reply_options=[]；
- emote / face_player / move_to 保持可见；
- Runtime 测试中使用的 fake speak 不受影响。
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

```text
- CapabilityList 只包含 present_dialogue / emote / face_player / move_to；
- present_dialogue 注册 ExecutionMode=SYNC；
- present_dialogue 注册 Sequential；
- present_dialogue 注册 exclusive_per_step=true；
- present_dialogue 注册 settle_after_success=true；
- speak 不出现在 Stardew CapabilityList；
- cold-start Runtime 后 registry 中没有 Stardew speak；
- Runtime Core 没有新增 game-specific execution branch。
```

## M2：present_dialogue 输入默认值与 UI

目标：

```text
玩家默认拥有回复入口，选项与自由输入稳定显示。
```

修改范围：

```text
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Dialogue/DialogueInteractionController.cs
adapters/stardew/src/Dialogue/DialogueInteractionMenu.cs
adapters/stardew/src/Dialogue/DialogueReplyChoice.cs
adapters/stardew/src/Dialogue/DialogueResponseMenuLayout.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
adapters/stardew/tests/check-context-static.ps1
```

实现要求：

```text
- RequirePresentDialogueArgument 缺省 AllowFreeText=true；
- present_dialogue input_schema 为 allow_free_text 标注 default=true；
- allow_free_text 显式 false 时关闭自由输入入口；
- reply_options 参数最多接收 3 个；
- allow_free_text=true 时 UI 最多展示 3 个 option 与自由输入入口；
- allow_free_text=false 时 UI 最多展示 3 个 option；
- Submit 空字符串不发送 player_said_to_npc；
- 超过 240 chars 的 free text 被拒绝并留在输入态；
- 玩家 Close / Escape 取消时关闭 active conversation；
- 玩家提交 option / free text 的 submit close 不关闭 active conversation。
- 玩家提交 option / free text 后的 NPC 回复不因同场景内距离漂移被拒绝。
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

```text
- 未传 allow_free_text 的 present_dialogue 解析为 AllowFreeText=true；
- 传 allow_free_text=false 时解析为 false；
- 3 个 reply_options + free text 可以同时进入 UI layout；
- 4 个 reply_options 被参数校验拒绝；
- 3 个 reply_options 在 allow_free_text=false 时可以同时进入 UI layout；
- 只有无 reply_options 且 AllowFreeText=false 时展示后关闭 active conversation；
- submit close 保持 active conversation，等待 player_said_to_npc TurnCompletion；
- 玩家提交 option / free text 后生成 player_said_to_npc；
- 玩家主动关闭不生成 player_said_to_npc。
```

## M3：Source-time Interaction Gate

目标：

```text
玩家点击 NPC 时就过滤明显无效或重复的交互，不让 Runtime 处理注定过期的对话。
```

修改范围：

```text
adapters/stardew/src/Events/PlayerInteractProbe.cs
adapters/stardew/src/Capabilities/PresentDialogueCapability.cs
adapters/stardew/src/Dialogue/DialogueInteractionController.cs
adapters/stardew/src/Dialogue/DialogueWaitingMenu.cs
adapters/stardew/src/Runtime/InteractionContextStore.cs
adapters/stardew/src/Runtime/InteractionPolicy.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/tests/PlayerInteractProbe.Tests/Program.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
adapters/stardew/tests/check-context-static.ps1
```

实现要求：

```text
- PlayerInteractProbe 使用与 RuntimeClient MaxInteractionDistance 相同的距离阈值；
- MaxInteractionDistance 来自 Adapter 内部单一常量或策略类型；
- PlayerInteractProbe 在 suppress 前调用 RuntimeClient 同步 gate / queue；
- 鼠标命中 NPC sprite 但玩家距离过远时不发送事件；
- queued / reserved event 立即进入 pending in-flight，阻止 ACK 前快速连点重复发送；
- source-time gate 成功后请求 waiting menu；
- player_said_to_npc handoff reserve 成功后请求 waiting menu；
- 收到 ActionRequest 后、执行 capability 前关闭对应 waiting menu；
- present_dialogue 关闭 waiting 后打开对话 UI；
- move_to 关闭 waiting 后执行移动；
- ACK rejected / duplicate、发送失败、failed / cancelled TurnCompletion、断线和 world reset 会关闭对应 waiting menu；
- player_said_to_npc 在同一 conversation 内使用 handoff reserve，不被原 player_interacted_with_npc in-flight 误吞；
- 同一 NPC pending 或 committed in-flight 未释放时 suppress 输入，但不发送重复 player_interacted_with_npc；
- Duplicate EventAck 不删除已 committed interaction context；
- TurnCompletion 是释放 committed context 的正式信号；
- Close / Escape abandon 关闭 active conversation 并释放匹配 in-flight；
- option / free text submit close 不释放 in-flight，等待 player_said_to_npc TurnCompletion；
- world change / day started / reconnect reset 会清空 in-flight 与 conversation state。
- ignored player interaction 打印 reason。
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

```text
- 远距离鼠标点击 NPC 不产生 player_interacted_with_npc；
- 有 active menu / dialogueUp 时不产生 player_interacted_with_npc；
- ACK 到达前同一 NPC 重复点击被 pending in-flight suppress；
- ACK(ACCEPTED) 后同一 NPC 重复点击被 committed in-flight suppress；
- 初次点击 gate 成功后出现 waiting menu；
- 玩家提交回复 handoff 成功后出现 waiting menu；
- 收到 ActionRequest 后、执行 present_dialogue 或 move_to 前关闭对应 waiting menu；
- 玩家提交回复早于原 TurnCompletion 时，player_said_to_npc 通过 handoff 发送；
- player_said_to_npc 后续 NPC 回复允许同场景内距离漂移；
- move_to 继续拒绝 effect-time 距离漂移；
- TestInteractionInFlightReleasedOnAbandonClose 覆盖 Close / Escape 释放 in-flight；
- TestInteractionInFlightKeptOnSubmitClose 覆盖 option / free text submit close 不释放 in-flight；
- EventAck(REJECTED) 只丢 pending context；
- EventAck(DUPLICATE) 不释放 committed context；
- player_said_to_npc EventAck(REJECTED) 关闭 active conversation 并释放 in-flight；
- player_said_to_npc EventAck(DUPLICATE) 不追加 pending player line，且不释放其它 committed context；
- TurnCompletion 释放 committed context。
```

## M4：ActionResult 日志与实机可观测性

目标：

```text
实机测试时可以从 Adapter 日志判断为什么 UI 没出现、为什么 Action 被拒绝、为什么 movement 没启动。
```

修改范围：

```text
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/tests/check-context-static.ps1
```

实现要求：

```text
- 所有 ActionResult 发送日志包含 capability/action_id/status/code/message；
- REJECTED / FAILED / CANCELLED 至少 warn/debug 级别打印 code 和 message；
- ActionRequest 接收日志包含 source_event_id/source_turn_id；
- interaction context guard 失败时打印具体 reason；
```

验收命令：

```powershell
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

```text
- 静态检查覆盖 ActionResult code/message 日志；
- 静态检查覆盖 source_event_id/source_turn_id 日志；
- build 通过；
- 实机日志可定位 present_dialogue rejected 的具体原因。
```

## M5：Runtime 边界回归

目标：

```text
确认 Phase6.5 没有把 Stardew 对话策略塞回 Runtime Core。
```

测试与静态检查范围：

```text
runtime/internal/agent/config_test.go
runtime/internal/agent/prompt_test.go
runtime/internal/tool
runtime/internal/agent
runtime/internal/context
```

实现要求：

```text
- Runtime 默认 ToolInstruction 保持 game-agnostic；
- Runtime 默认 agent config 保持 game-agnostic；
- Stardew 专用 prompt profile 放在 runtime/config/games/stardew-valley/agent.json，并通过 GAMEAGENT_AGENT_CONFIG 显式选择；
- Runtime 不新增 present_dialogue / speak / Stardew event_type 执行分支；
- Tool policy 仍只来自 Capability.extensions.gameagent.tool_policy；
- visibleActionSummary 的已有 Stardew 工具名摘要保持渲染层兼容，不扩展为执行策略；
- Runtime renderer 现存 tool-name 摘要维持现状，本阶段不新增 Runtime renderer 分支；
- 不新增 Runtime 对 Observation.state.stardew 的解析。
```

验收命令：

```powershell
go test ./runtime/internal/agent ./runtime/internal/tool ./runtime/internal/context
go test ./runtime/...
```

通过标准：

```text
- Runtime 默认 prompt 不包含 Stardew capability name；
- Runtime 执行路径不按 capability name 分支；
- 现有 async lifecycle / move_to / TurnCompletion 测试保持通过。
```

## M6：实机验收

目标：

```text
在 Stardew 中验证玩家点击、选择、输入、取消、移动请求的真实体验。
```

验收步骤：

```text
1. 从 world-is-agent repo root 运行 scripts/install-stardew-adapter.ps1，重新 build 并安装 Stardew adapter。
2. 冷启动 Runtime，确保 registry 从 fresh Stardew CapabilityList 注册。
3. 进入同一存档，靠近 NPC。
4. 点击 NPC。
5. 等待 NPC 台词出现。
6. 推进 NPC 台词。
7. 确认回复选项与自由输入入口同时出现。
8. 选择一个 option。
9. 确认 Adapter 发送 player_said_to_npc，Runtime 返回下一轮 NPC 响应。
10. 再次点击 NPC 时，确认对话不乱序、不叠 UI。
11. 输入 free text 并提交。
12. 确认 payload.text 与 ContextFact.text 一致。
13. 按 Escape 或 Close。
14. 确认 active conversation 关闭，后续点击创建新对话。
15. 对 NPC 说“走到我身边”一类意图。
16. 确认 Runtime trace 出现 move_to，或模型用 present_dialogue 明确回应无法移动；若出现 move_to，确认 Thinking waiting menu 在动作执行前关闭，NPC 能移动或返回明确失败。
17. 触发单句结束型 NPC 台词时，确认 ActionRequest 显式包含 allow_free_text=false 且 reply_options 为空。
```

未冷启动 Runtime 导致旧 capability 残留时，视为实机测试环境问题。

通过标准：

```text
- 普通点击 NPC 不再只触发单句 speak；
- 回复菜单稳定出现；
- 自由输入入口稳定出现；
- 玩家选择 option 后对话是否继续由后续 Runtime 决策决定；
- 玩家可以通过 Close / Escape 主动结束对话；
- 连点 NPC 不产生多个并发 dialogue UI；
- in-flight 期间再次点击 NPC 不发送新 GameEvent，也不触发 Stardew 原生对话；
- present_dialogue rejected 时日志有具体 code/message；
- 单句结束型 present_dialogue 不遗留 active conversation；
- move_to 的调用、目标 tile、拒绝或失败原因可从 trace 与 Adapter 日志复盘。
```

---

# 6. 测试计划

Adapter 自动验收：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet run --project adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet run --project adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

Runtime 回归：

```powershell
go test ./runtime/internal/agent ./runtime/internal/tool ./runtime/internal/context
go test ./runtime/...
```

全量检查：

```powershell
go test ./runtime/... ./protocol/gen/go/...
go test ./...
git diff --check
```

实机检查：

```text
Stardew 近距离点击 NPC
Stardew 连续点击同一 NPC
Stardew option reply
Stardew free text reply
Stardew Close / Escape
Stardew move_to request
Adapter log
runtime/.local/traces.jsonl
```

---

# 7. 提交边界

推荐提交：

```text
docs: add phase6.5 dialogue convergence plan
feat: converge stardew dialogue tool surface
feat: harden stardew dialogue interaction lifecycle
test: cover phase6.5 dialogue interaction behavior
```

提交边界：

```text
- 文档提交只包含 docs/phase6.5 与必要 roadmap 引用；
- Tool View / present_dialogue 默认值 / UI layout 可以同提交；
- source-time gate / in-flight gate / observability 可以同提交；
- Runtime 只允许边界回归测试或删除过期 Stardew-specific prompt；
- 不夹带 Phase7 persistence / recovery 工作。
```

---

# 8. Phase6.5 完成条件

Phase6.5 完成时应满足：

```text
1. Stardew CapabilityList 不暴露 speak。
2. present_dialogue 是 Stardew 玩家点击 NPC 的默认对话输出能力。
3. present_dialogue 缺省提供自由输入入口。
4. 单句结束型 NPC 台词通过 present_dialogue 显式表达。
5. 选项与自由输入入口可以稳定同时显示。
6. 玩家可以主动关闭 conversation。
7. 连点 NPC 不产生重复 GameEvent、乱序回复或叠 UI。
8. Runtime 返回 ActionRequest 前 waiting menu 锁住玩家输入；收到 ActionRequest 后、执行 capability 前关闭。
9. source-time gate 校验启动距离；present_dialogue effect-time 允许同场景内距离漂移；move_to 保留 proximity guard。
10. ActionResult 日志足以解释 rejected / failed / cancelled。
11. Runtime Core 没有新增 game-specific 执行策略。
12. 自动测试通过。
13. 实机 smoke 通过。
```

验收记录：

```text
2026-09-02 Phase6.5 验收通过。

自动化验收：
- ProtocolMapper.Tests 通过。
- PlayerInteractProbe.Tests 通过。
- adapters/stardew/tests/check-context-static.ps1 通过。
- adapters/stardew/GameAgent.Stardew.csproj Debug build 通过。
- go test ./runtime/... ./protocol/gen/go/... 通过。
- git diff --check 通过。

实机 smoke：
- 普通点击 NPC 能进入 present_dialogue。
- 玩家回复后能继续收到 NPC 响应。
- move_to 能进入 Accepted / Running / Succeeded，并在完成后继续 present_dialogue。
- Thinking waiting menu 不再阻塞 move_to 的世界 tick。
- in-flight 期间点击 NPC 不产生重复 GameEvent 或 Stardew 原生对话。
```

---

# 9. 下一阶段衔接

Phase6.5 完成后，Phase7 继续处理：

```text
Environment reconnect
Runtime restart recovery
persistent continuation
long-term memory persistence
session-scoped capability registry
dynamic game profile / tool view
capability-driven visible summary metadata
```

Phase6.5 不把这些能力提前实现。
