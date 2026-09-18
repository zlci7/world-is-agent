# GameAgent MVP0 Phase5.6 技术开发与验收方案

> **Status:** Implementation Baseline Draft
> **Date:** 2026-08-28
> **Scope:** Stardew Adapter Interaction Surface + ContextFact Memory Projection
> **Architecture Baseline:** GameAgent Runtime Architecture v0.3
> **Roadmap Baseline:** GameAgent 阶段规划 v0.5
> **Protocol Baseline:** gameagent.protocol.v1alpha2 after Phase5 + Phase5.6 ContextFact additive update
> **Previous Phase:** Phase5.5 Stardew Adapter Context Enrichment Accepted
> **Next Phase:** Phase6 Async Action Lifecycle and AgentTurn Resume
> **Reference:** ValleyTalk, Stardojo, SMAPI 4.5.2

---

# 1. 阶段目标

Phase5 已经完成有界 multi-step AgentTurn、ordered ToolCall batch、ToolResult transcript 和 settle control。

Phase5.5 已经完成 Stardew 当前事实模型，并通过 `Observation.state.stardew` 把时间、天气、人物、关系、场景和日程交给 Runtime。

Phase5.6 要建立 Stardew Adapter 的交互面：

```text
Player input / click
  -> Adapter GameEvent
  -> Runtime AgentTurn
  -> Environment ToolCall
  -> Adapter UI / NPC action
  -> ActionResult
  -> Turn settle
```

本阶段主要证明：

> **对话会话可以跨多个 AgentTurn 进行；Runtime 仍是通用认知中枢，Adapter 负责 Stardew 输入、UI 和 NPC 可见动作。**

---

# 2. 阶段结论

Phase5.6 做这些工作：

```text
1. 定义 Stardew 对话会话为多 Turn session。
2. 新增玩家对话事件 player_said_to_npc。
3. 为 GameEvent 增加通用 ContextFact，承载 Adapter 显式提供的 model-visible event context。
4. 新增 Adapter 内存 conversation state，并把最近对话行注入 Observation.state.stardew.conversation。
5. 新增 present_dialogue capability，用于通过 Stardew 原生对话节奏展示 NPC 台词、最多 4 个可见玩家回复行和内联自定义输入行。
6. 新增 face_player capability，用于让 NPC 面向玩家。
7. 更新 speak / emote / present_dialogue 的工具描述，使模型能稳定选择对话展示或简单动作。
8. 新增 Stardew 对话 UI，NPC 台词先走原生 dialogue box，玩家推进后再出现底部回复菜单；自由输入作为最后一行直接接收输入。
9. 更新 Runtime 配置提示词，使模型理解玩家输入事件、回复选项和当前可用能力。
10. 更新 Runtime Memory，使玩家输入 ContextFact 与 NPC 可见动作共同进入 Recent Memory。
11. Context Engine 过滤游戏时间晚于当前时间的 Recent Memory。
12. 明确交互发起到 UI 展示之间的上下文校验边界，作为 Phase6 Turn lifecycle 的接入点。
```

Phase5.6 不做这些工作：

```text
除 GameEvent.context_facts / ContextFact 外的 Protocol 字段变更
Runtime async action lifecycle
同一 Turn 内等待玩家输入
ask_player / human-in-loop async tool
movement capability
ActionStatusUpdate / CancelActionRequest 接线
AgentDefinition store
canonical dialogue retrieval
long-term event memory persistence
ExperienceLog / EventDefinition registry
ValleyTalk prompt builder 迁移
Adapter 内部 LLM 调用
Harmony patch 改写 Stardew 原生 Dialogue 流程
Stardojo player-centric inventory / menu / shop / craft actions
等待 LLM 期间冻结玩家或 NPC
Interaction Context Guard 执行态校验
```

---

# 3. Turn 与 Conversation 边界

## 3.1 Conversation 是跨 Turn 会话

`conversation_id` 是 Adapter 侧对话会话 ID。

`turn_id` 是 Runtime 侧单次 AgentTurn ID。

两者关系：

```text
conversation_id: conv_12
  turn_A: player_interacted_with_npc -> present_dialogue
  turn_B: player_said_to_npc        -> present_dialogue + emote
  turn_C: player_said_to_npc        -> face_player + present_dialogue
```

规则：

- 一个 `GameEvent` 启动一个新的 AgentTurn；
- 一个 `conversation_id` 可以关联多个 AgentTurn；
- 一个 AgentTurn 不等待玩家选择或输入；
- `present_dialogue` 的 ActionResult 表示 NPC 台词已进入 Stardew 原生 UI；回复入口如果存在，会在玩家推进台词后由 Adapter 状态机继续展示；
- 玩家选择或输入后，Adapter 发送新的 `player_said_to_npc` 事件；
- 同一 NPC 的事件继续由 Runtime `ExecutionLane` 串行处理。

## 3.2 Phase5.6 对话链路

点击 NPC：

```text
Player clicks NPC
  -> Adapter reserves or resumes conversation_id
  -> GameEvent(player_interacted_with_npc)
  -> EventAck(ACCEPTED)
  -> Adapter commits conversation open state
  -> Runtime Observe
  -> Model calls present_dialogue
  -> Adapter displays NPC line in Stardew's native dialogue box
  -> ActionResult(SUCCEEDED)
  -> Runtime settle
  -> Player advances the NPC line
  -> Adapter displays reply options / inline free-text row
```

玩家回复：

```text
Player selects option or enters text
  -> Adapter creates pending player-line mutation
  -> GameEvent(player_said_to_npc)
  -> EventAck(ACCEPTED)
  -> Adapter commits player line to conversation state
  -> Runtime Observe, including conversation recent_lines
  -> Model calls present_dialogue / emote / face_player
  -> Adapter displays NPC line and commits NPC line
  -> ActionResult(SUCCEEDED)
  -> Runtime settle
```

## 3.3 Active Conversation

`active conversation` 表示 `ConversationStateStore` 中存在、未 close、未 reset 的当前 NPC 会话。

生命周期规则：

- `player_interacted_with_npc` 在没有 active conversation 时 reserve 新的 `conversation_id`；
- `player_interacted_with_npc` 在存在 active conversation 时复用当前 `conversation_id`；
- reserved conversation 只有在 `EventAck.ACCEPTED` 后进入 active 状态；
- `EventAck.REJECTED` 时丢弃 reserved conversation；
- `EventAck.DUPLICATE` 不创建、不重复写入 conversation state；
- `present_dialogue` 准备显示时预留 `conversation_id`，UI 真正显示后才追加 NPC line；
- 玩家通过 Close / Escape 放弃菜单时按 `conversation_id` 精确关闭 active conversation；
- 玩家提交 option / free text 时不关闭 active conversation；
- Adapter 抢占同一 NPC 的旧菜单时不关闭 active conversation；
- `DayStarted` 到达时执行 `ConversationStateStore.Clear()`，所有 `conversation_id` 失效；
- 新一天对同一 NPC 的首次交互总是创建新的 `conversation_id`；
- world change、returned to title、Runtime reconnect 时执行 `ConversationStateStore.Clear()`；
- MVP0 不恢复跨 stream 的 pending mutation，disconnect 后未确认的 conversation mutation 失效。

---

# 4. Adapter Event Contract

## 4.1 ContextFact

`ContextFact` 是 Adapter 显式提供的 model-visible event context。

它不是完整事件日志，不替代 `GameEvent.payload`，不替代 `Observation`，也不参与 Agent Definition binding。

Protocol shape：

```proto
message ContextFact {
  string kind = 1;
  string actor_entity_id = 2;
  string target_entity_id = 3;
  string scope_id = 4;
  string text = 5;
  string label = 6;
  google.protobuf.Struct attributes = 7;
}

message GameEvent {
  ...
  repeated ContextFact context_facts = 9;
}
```

字段语义：

- `kind` 表示事实类型，Runtime 只解释通用词表；
- `actor_entity_id` 表示事实发起者，例如 `player:local`；
- `target_entity_id` 表示事实指向的 Agent entity；
- `scope_id` 表示该事实所属交互作用域，本阶段使用 `conversation_id`；
- `text` 表示模型可见自然语言内容；
- `label` 表示短标签或选择标题，本阶段可为空；
- `attributes` 只放通用事实的附加属性，不放完整游戏状态。

通用 `kind` 词表：

```text
utterance
choice
command
interaction
```

Phase5.6 只实现 `utterance`。后续新增 kind 必须仍然保持多游戏通用语义，不得使用 `player_said_to_npc` 这类 game-specific event type 作为 Runtime 分支条件。

规则：

- `ContextFact` 引用 `entity_id`，不承载 `definition_id`；
- Runtime Memory 可以投影 `ContextFact`，但不得解析 game-specific `Observation.state` 来倒推事实；
- Adapter 只把确实应进入模型上下文的事件语义写入 `context_facts`；
- 没有 model-visible event context 的事件可以不填 `context_facts`。
- `GameEvent.payload` 保留 Adapter / event-specific 字段；`ContextFact` 是 Runtime memory projection 的通用事实入口。
- 当前 Turn 的 `[Current Event]` 可以同时包含 payload text 和 context_facts text；MVP0 接受这点冗余，不做 renderer 去重。
- 跨 Turn 的 Recent Memory 只消费 `context_facts`，不从 payload text 生成记忆。

## 4.2 player_said_to_npc

`player_said_to_npc` 是 Environment -> Agent 事件。

它不是 capability，也不由模型调用。

Payload：

```json
{
  "conversation_id": "conv_12",
  "input_kind": "option",
  "text": "I can help you get there.",
  "selected_option_index": 1,
  "trigger": "dialogue_option"
}
```

ContextFact：

```json
[
  {
    "kind": "utterance",
    "actor_entity_id": "player:local",
    "target_entity_id": "npc:Abigail",
    "scope_id": "conv_12",
    "text": "I can help you get there.",
    "attributes": {
      "input_kind": "option",
      "selected_option_index": 1,
      "trigger": "dialogue_option"
    }
  }
]
```

字段规则：

- `conversation_id` 必须非空；
- `input_kind` 使用 `option / free_text`；
- `text` 必须非空，最大 240 chars，超长输入必须拒绝并返回明确错误；
- `selected_option_index` 只在 `input_kind=option` 时出现，使用 0-based index；
- `trigger` 使用 `dialogue_option / dialogue_free_text`；
- `target_entity_id` 是被对话 NPC 的 `entity_id`；
- `entities` 必须包含目标 NPC 和 `player:local`；
- `EntityRef.definition_id` 继续沿用 `entity_id` alias。
- `context_facts` 必须包含一条 `kind=utterance` 的玩家输入事实；
- `context_facts[0].text` 与 payload `text` 一致；
- `context_facts[0].scope_id` 与 payload `conversation_id` 一致。

## 4.3 player_interacted_with_npc

现有 `player_interacted_with_npc` 保留，payload 增加 `conversation_id`。

Payload：

```json
{
  "conversation_id": "conv_12",
  "trigger": "action_button",
  "source": "stardew-smapi"
}
```

字段规则：

- 如果目标 NPC 没有 active conversation，Adapter 创建新的 `conversation_id`；
- 如果目标 NPC 有 active conversation，Adapter 复用当前 `conversation_id`；
- `source` 固定表示事件来源系统，本阶段使用 `stardew-smapi`；
- `trigger` 表示 Adapter 捕获的交互类型，可用值为 `action_button / mouse_left / mouse_right / console_probe`。
- 本阶段 `player_interacted_with_npc` 可以不填 `context_facts`；当前点击事实仍通过 `Current Event` payload 进入本 Turn context。

## 4.4 EventAck 与 conversation mutation

Adapter 对 conversation state 的写入必须与 Runtime `EventAck` 对齐。

规则：

- 发送 `player_interacted_with_npc` 前，Adapter 只 reserve `conversation_id`；
- 发送 `player_said_to_npc` 前，Adapter 只创建 pending player-line mutation；
- 每个 conversation 最多保留一个 pending mutation；
- pending mutation 保存 `event_id`，收到 ACK 时必须按 `event_id` 匹配；
- 收到匹配的 `EventAck.ACCEPTED` 后 commit pending mutation；
- 收到匹配的 `EventAck.REJECTED` 后丢弃 pending mutation；
- 收到匹配的 `EventAck.DUPLICATE` 后不重复 commit；
- gRPC server-to-adapter 消息顺序要求 Adapter 在处理同一 stream 后续 `ObservationRequest` 前先处理已收到的 `EventAck`；
- pending mutation 不写入 `Observation.state.stardew.conversation`。

## 4.5 Interaction Context Guard 边界

`Interaction Context Guard` 是 Phase6 前需要接入的 Adapter 执行前校验边界。

目标：

```text
防止玩家点击 NPC 后，在 LLM 响应前玩家或 NPC 已经离开，随后 dialogue UI 又在错误位置弹出。
```

设计规则：

- Phase5.6 不实现执行态校验，只明确边界；
- Phase6 在 Adapter 发送 `player_interacted_with_npc` 和 `player_said_to_npc` 时记录当前 interaction context；
- interaction context 至少包含 `world_id`、`conversation_id`、`npc_entity_id`、`player_entity_id`、location、NPC tile、player tile 和最大交互距离；
- `present_dialogue` 显示 UI 前校验当前世界、location 和距离仍满足该 interaction context；
- 校验通过后显示 UI，并在 UI 显示成功后追加 NPC conversation line；
- 校验失败时返回 `ActionResult(REJECTED)`，错误码为 `interaction_context_changed`，并关闭匹配的 active conversation；
- 该 guard 不冻结玩家或 NPC，不等待同一 Turn 内玩家输入，不引入 Runtime Stardew-specific parser；
- Phase6 在该 guard 基础上增加 `turn_id / action_id` 绑定、等待锁和 Turn terminal 释放信号。

---

# 5. Conversation State

## 5.1 Adapter 内存状态

新增 Adapter 侧状态组件：

```text
adapters/stardew/src/Dialogue/ConversationStateStore.cs
adapters/stardew/src/Dialogue/ConversationState.cs
adapters/stardew/src/Dialogue/ConversationLine.cs
adapters/stardew/src/Dialogue/ConversationIdGenerator.cs
```

状态 key：

```text
world_id + npc_entity_id + player_entity_id
```

状态内容：

```json
{
  "conversation_id": "conv_12",
  "npc_entity_id": "npc:Linus",
  "player_entity_id": "player:local",
  "recent_lines": [
    {
      "role": "npc",
      "speaker_entity_id": "npc:Linus",
      "speaker_name": "Linus",
      "text": "The mountain is quiet tonight.",
      "time_of_day": 1820
    },
    {
      "role": "player",
      "speaker_entity_id": "player:local",
      "speaker_name": "ZLC",
      "text": "Do you want company?",
      "time_of_day": 1820
    }
  ]
}
```

规则：

- MVP0 只使用内存状态；
- 状态在 mod reload、Runtime reconnect、world change、returned to title 或 day started 时重置；
- `DayStarted` handler 必须调用 `ConversationStateStore.Clear()`；
- `ConversationStateStore` 必须是 thread-safe，所有 public mutation/read API 通过同一把锁保护；
- `ConversationStateStore` 通过构造注入 conversation id generator，生产实现生成唯一 ID，测试实现返回确定性 ID；
- 每个 conversation 最多保留 12 行；
- 单行 text 最大 240 chars；
- 单行 text 超出 240 chars 时拒绝写入，并返回明确错误；
- line 的 `time_of_day` 取追加该 line 时的 `Game1.timeOfDay`；
- 超出行数上限时保留最近行，并记录 `recent_lines_omitted_count`；
- Adapter 只记录已被 Runtime 接纳的玩家输入和 `present_dialogue` 展示的 NPC 台词；
- `speak` 不写入 conversation state；
- Adapter 不把 conversation state 持久化到 Stardew save data。

## 5.2 Observation 注入

扩展 `Observation.state.stardew`：

```json
{
  "stardew": {
    "conversation": {
      "conversation_id": "conv_12",
      "active": true,
      "recent_lines_omitted_count": 0,
      "recent_lines": [
        {
          "role": "npc",
          "speaker_entity_id": "npc:Linus",
          "speaker_name": "Linus",
          "text": "The mountain is quiet tonight.",
          "time_of_day": 1820
        }
      ]
    }
  }
}
```

字段规则：

- `conversation` 只在目标 NPC 有 active conversation 时出现；
- `role` 使用 `npc / player`；
- `speaker_entity_id` 必须是当前 Observation 可解析的实体 ID；
- `speaker_name` 使用追加 line 时的游戏展示名；
- `recent_lines` 按发生顺序输出；
- `recent_lines` 不进入 `Observation.nearby_entities`；
- Runtime 不读取 `state.stardew.conversation.*` 的具体语义，只通过通用 renderer 输出。

---

# 6. Capability Contract

## 6.1 present_dialogue

`present_dialogue` 是 Agent -> Environment 的同步 capability。

Capability：

```text
name: present_dialogue
version: 0.1.0
execution_mode: SYNC
concurrency_mode: Sequential
```

Description：

```text
Displays one NPC dialogue line and optional player reply choices. The action completes when the dialogue UI is shown; player replies arrive later as player_said_to_npc events.
```

Input schema：

```json
{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "minLength": 1,
      "maxLength": 240
    },
    "reply_options": {
      "type": "array",
      "items": {
        "type": "string",
        "minLength": 1,
        "maxLength": 80
      },
      "maxItems": 4
    },
    "allow_free_text": {
      "type": "boolean"
    }
  },
  "required": ["text"],
  "additionalProperties": false
}
```

Execution rules：

- `text` 显示为 NPC 台词；
- `reply_options` 最多 4 个；
- `allow_free_text` 缺省为 `false`；
- Adapter handler 必须校验 `text` 非空且不超过 240 chars；
- Adapter handler 必须校验 `reply_options` 为字符串数组，最多 4 个，每个 option 非空且不超过 80 chars；
- 超出长度或数量上限时返回 `ActionResult(REJECTED)`，错误信息必须包含具体 limit；
- Action handler 使用 `world_id + ActionRequest.entity_id + player:local` 查找 active conversation；
- 如果不存在 active conversation，Action handler 创建新的 active conversation；
- 当 `reply_options` 为空且 `allow_free_text=false` 时，只展示 NPC 台词；
- 当 `allow_free_text=true` 时，可见回复菜单最后一个槽位保留给内联自定义输入行；Adapter 最多显示前 3 个 `reply_options` 加 1 个输入行；
- 当 `allow_free_text=false` 时，Adapter 最多显示 4 个 `reply_options`；
- NPC 台词、回复选项、自由输入框不得同时出现在同一个居中自绘 modal 中；
- `DialogueInteractionController` 持有 pending action；
- Adapter 在 NPC 原生对话进入 Stardew UI 流程后返回 terminal `ActionResult(SUCCEEDED)`；
- 当存在回复选项或内联自定义输入行时，后续回复菜单由 Adapter 状态机在玩家推进 NPC 台词后继续展示，但不延迟 sync `ActionResult`；
- Adapter 在 UI 显示成功后把 `text` 追加为 NPC conversation line；
- 当 `reply_options` 为空且 `allow_free_text=false` 时，UI 展示完成后关闭 active conversation；
- 玩家后续选择或输入由 UI 发送 `player_said_to_npc` 事件。

ActionResult output：

```json
{
  "conversation_id": "conv_12",
  "displayed_text": "The mountain is quiet tonight.",
  "reply_options_count": 2,
  "allow_free_text": true
}
```

## 6.2 face_player

`face_player` 是 Agent -> Environment 的同步 capability。

Capability：

```text
name: face_player
version: 0.1.0
execution_mode: SYNC
concurrency_mode: Sequential
```

Description：

```text
Turns the NPC to face the local player when both are in the same location.
```

Input schema：

```json
{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}
```

Execution rules：

- NPC 与玩家不在同一 location 时返回 `ActionResult(REJECTED)`；
- 同 location 时按 tile delta 设置 NPC facing direction；
- tile delta 相同或方向无法确定时保持当前方向并返回 `SUCCEEDED`；
- 不移动 NPC。

ActionResult output：

```json
{
  "facing": "down"
}
```

## 6.3 speak 与 emote

`speak` 和 `emote` 保留。

规则：

- `speak` 用于普通单句 NPC 台词，不展示玩家回复选项；
- `present_dialogue` 用于可交互对话；
- `emote` 继续用于 NPC 头顶表情；
- 三个 capability 均为 `Sequential`；
- `speak` 不写入 conversation state。

---

# 7. Dialogue UI

新增 UI 组件：

```text
adapters/stardew/src/Dialogue/DialogueInteractionMenu.cs
adapters/stardew/src/Dialogue/DialogueInteractionController.cs
```

UI 行为：

- 使用 SMAPI/Stardew 主线程显示；
- 如果 `Game1.activeClickableMenu` 非空，延迟到下一帧再显示；
- Adapter 准备发送同一 NPC 新 GameEvent 前，先关闭该 NPC 未决 dialogue UI；
- 先使用 Stardew 原生 NPC dialogue box 显示 NPC 台词；
- 玩家点击推进 NPC 台词后，再显示底部回复菜单；
- 可见回复行最多 4 个；
- 当 `allow_free_text=true` 时，第 4 个可见槽位保留给内联自定义输入行，Adapter 最多显示 3 个生成选项；
- 内联自定义输入行默认获得键盘焦点；
- 启用内联自定义输入时，底部回复菜单保留 `Close` 和 `Send`；
- 玩家选择选项时关闭菜单并发送 `player_said_to_npc`；
- 玩家提交自定义输入时关闭菜单并发送 `player_said_to_npc`；
- 玩家取消或关闭菜单时不发送 `player_said_to_npc`；
- 玩家取消或关闭菜单时关闭 active conversation；
- Adapter 抢占关闭旧菜单时不关闭 active conversation；
- 文本输入为空时不发送事件。

实现边界：

- 参考 ValleyTalk 的 `IClickableMenu`、`Game1.activeClickableMenu` 和 `Game1.keyboardDispatcher.Subscriber` 用法；
- 不 patch `Dialogue.chooseResponse`；
- 不覆盖 Stardew 原生 `Dialogue` 内部状态；
- 不在 UI 组件内调用 Runtime 或 LLM，由 controller 接收 UI 结果后调用 `RuntimeClient`。

---

# 8. Runtime Scope

Runtime 本阶段只做通用接线。

修改范围：

```text
runtime/config/agent.json
runtime/internal/agent/loop.go
runtime/internal/agent/loop_test.go
runtime/internal/context/renderer.go
runtime/internal/context/builder_test.go
runtime/internal/memory/record.go
runtime/internal/memory/projector.go
runtime/internal/memory/projector_test.go
```

允许改动：

- 为 `GameEvent` 增加 `context_facts`，为 Protocol 增加 `ContextFact`；
- 更新 Protocol static check 和 Go / C# 生成代码；
- `player_said_to_npc` mapper 填充 `ContextFact(kind=utterance)`；
- 更新 `tool_instruction`，说明玩家文本通过 `player_said_to_npc` 事件到达；
- 更新 `tool_instruction`，说明 `present_dialogue` 可展示回复选项，玩家回复会在后续事件中到达；
- 更新 `tool_instruction`，说明有回复选项或允许玩家输入时使用 `present_dialogue`，普通单句使用 `speak`；
- `MemoryRecord` 保存 `SourceContextFacts` 和 `Outcomes`；
- `MemoryProjector` 从 `GameEvent.context_facts` 投影玩家输入等事件事实；
- 成功完成的 AgentTurn 在存在 model-visible `ContextFact` 或 successful visible outcome 时写入 Recent Memory；
- 为 `visibleActionSummary` 增加 `present_dialogue` 与 `face_player` 摘要；
- Recent Memory 渲染同时展示 SourceContextFacts 和 action outcomes；
- Context Engine 在渲染 Recent Memory 前过滤游戏时间晚于当前 GameTime 的记录；
- 增加一个通用 context renderer 测试，验证 `GameEvent.context_facts` 和 nested `Observation.state.stardew.conversation.recent_lines` 可以进入模型上下文；
- 增加 Recent Memory 回归测试，验证 `present_dialogue` 的 `text` 可以进入可见摘要。

禁止改动：

- 不新增 Stardew-specific Runtime parser；
- 不改变 `agent.Loop` 的 multi-step / scheduler / settle 执行语义；
- 不改 `gateway`；
- 不改 `model.Provider`；
- 不新增 `ContextFact` 之外的 Protocol 字段；
- 不实现 async resume。

Memory 投影规则：

- `SourceContextFacts` 使用当前 `GameEvent.context_facts`，表示本 Turn 的输入语义；
- `MemoryRecord.SourceEventSequence` 取 `GameEvent.sequence`，用于相等 `GameTime` 下的稳定排序；
- `Outcomes` 使用本 Turn 中 Runtime 已确认 `SUCCEEDED` 的可见 Environment outcomes；
- 成功完成的 Turn 才能把 `SourceContextFacts` 写入 Memory；
- `player_said_to_npc` + settle-only 的成功 Turn 可以只凭 `SourceContextFacts` 写入 Memory；
- 失败、超时、`max_steps_exceeded` 或 invalid model response 的 Turn 不得只凭 `SourceContextFacts` 写入 Memory；
- Phase5 已确认的 technical terminal failure 例外保持不变：错误发生前已确认 `SUCCEEDED` 的 action outcome 可以写入 Memory；
- technical terminal failure 写入的 prior successful outcome record 不附带 `SourceContextFacts`；
- 纯 `SourceContextFacts` record 允许 `Outcomes` 为空；
- `MemoryProjector` 不得为了兼容旧单 action 路径而合成空 ToolCall / ActionResult；
- rejected / failed / invalid / skipped / cancelled / interrupted outcomes 不进入 Memory；
- 既没有 `SourceContextFacts`，也没有 successful visible outcomes 的 Turn 不写 Memory；
- 同一 MemoryRecord 渲染时必须先输出 `SourceContextFacts`，再输出 `Outcomes`，保持“玩家输入 -> NPC 行为”的因果顺序。

Memory 时间线选择规则：

- 未来时间过滤只处理 `MemoryRecord.GameTime > CurrentGameTime` 的记录；
- 相等时间不被过滤；
- `GameTime` 比较顺序为 `year, season, day, hour, minute, tick`；
- 未来时间过滤后默认保持 MemoryStore 返回顺序；
- 连续、同一非空 `GameTime` 且 `SourceEventSequence` 非 0 的 Memory 片段按 `SourceEventSequence` 升序稳定排序；
- 缺少可比较 `GameTime` 或 `SourceEventSequence` 时，该 MemoryRecord 保持 MemoryStore 返回顺序；
- 未来时间过滤必须发生在 memory context byte budget trim 之前；
- 该规则用于处理读取更早存档、回档或世界时间回退后的上下文一致性；
- 同一天且未处于未来时间的 Recent Memory 仍按现有 prompt 语义作为 nearby conversation context。

实现命名约束：

- `updateMemoryForSuccessfulActions` 需要改名并调整签名，表达它负责 Turn 级 `SourceContextFacts + successful outcomes` 投影；
- 新名称应描述最终职责，例如 `updateMemoryForCompletedTurn` 或 `updateMemoryForTurnMemory`；
- 调用点必须显式传入本 Turn 的 completion 语义，避免失败 Turn 因携带 `ContextFact` 而写入 Memory。

---

# 9. 开发里程碑

## M0 Protocol ContextFact

目标：

```text
为 GameEvent 增加通用 model-visible event context 窄腰。
```

修改范围：

```text
protocol/proto/gameagent.proto
protocol/gen/go/gameagent/protocol/v1alpha2
protocol/gen/csharp/GameAgent.Protocol/V1Alpha2
protocol/tests/check-protocol-static.ps1
```

验收命令：

```powershell
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
go test ./protocol/gen/go/...
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

- `ContextFact` message 存在；
- `GameEvent.context_facts = 9` 存在；
- `ContextFact` 不包含 `definition_id`；
- 文档内固定通用 kind 词表为 `utterance / choice / command / interaction`；
- `Observation` 不新增 context fact 字段；
- `ContextFact` 只作为 model-visible event context，不替代 payload、Observation、Memory 或 Experience。

## M1 Conversation State Model

目标：

```text
建立 Adapter 内存 conversation state，并能被 ObservationBuilder 注入 StardewObservation。
```

修改范围：

```text
adapters/stardew/src/Dialogue/ConversationLine.cs
adapters/stardew/src/Dialogue/ConversationState.cs
adapters/stardew/src/Dialogue/ConversationStateStore.cs
adapters/stardew/src/Dialogue/ConversationIdGenerator.cs
adapters/stardew/src/State/StardewObservation.cs
adapters/stardew/src/State/StardewObservationFactory.cs
adapters/stardew/src/State/ObservationBuilder.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/ModEntry.cs
adapters/stardew/tests/ProtocolMapper.Tests
adapters/stardew/tests/check-context-static.ps1
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
```

通过标准：

- `ConversationStateStore` 可创建、复用、重置 conversation；
- `ConversationStateStore` public read/mutation API 是 thread-safe；
- conversation id generator 通过构造注入，测试使用确定性实现；
- `DayStarted` reset 规则进入 static check；
- 新一天首次交互同一 NPC 创建新的 `conversation_id`；
- recent lines 按发生顺序输出；
- recent lines 超出 12 行时只保留最近 12 行；
- line 的 `time_of_day` 取追加时的游戏时间；
- 单行 text 超出 240 chars 时被拒绝，错误信息说明 240 chars limit；
- `Observation.state.stardew.conversation` 在有 active conversation 时输出；
- 无 active conversation 时不输出 `conversation`。

## M2 Player Dialogue Event

目标：

```text
Adapter 能把玩家选择或输入转换为 player_said_to_npc GameEvent。
```

修改范围：

```text
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/Events/PlayerInteractProbe.cs
adapters/stardew/tests/ProtocolMapper.Tests
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
```

通过标准：

- `BuildPlayerSaidToNpcEvent(...)` 输出 `event_type=player_said_to_npc`；
- payload 包含 `conversation_id`、`input_kind`、`text` 和 `trigger`；
- `context_facts` 包含一条 `kind=utterance` 的玩家输入事实；
- `context_facts[0].actor_entity_id` 指向 `player:local`；
- `context_facts[0].target_entity_id` 指向目标 NPC；
- `context_facts[0].scope_id` 等于 `conversation_id`；
- `context_facts[0].text` 等于 payload `text`；
- option 输入包含 `selected_option_index`；
- free text 输入不包含 `selected_option_index`；
- 事件 `entities` 包含目标 NPC 和 `player:local`；
- 事件 `target_entity_id` 指向目标 NPC；
- 空 text 被拒绝；
- 超长 text 被拒绝，错误信息说明 240 chars limit；
- `player_interacted_with_npc.source` 保持 `stardew-smapi`；
- `player_interacted_with_npc.trigger` 区分 `action_button / mouse_left / mouse_right / console_probe`；
- `PlayerInteractProbe` 负责把输入按钮映射为 `action_button / mouse_left / mouse_right`；
- console probe 负责产生 `console_probe`；
- player line 只在 `EventAck.ACCEPTED` 后写入 conversation state；
- `EventAck.REJECTED` 不写入 conversation state；
- `EventAck.DUPLICATE` 不重复写入 conversation state。

## M3 present_dialogue Capability

目标：

```text
模型可以请求 Adapter 展示 NPC 台词和玩家回复入口。
```

修改范围：

```text
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/Capabilities/PresentDialogueCapability.cs
adapters/stardew/src/Dialogue/ConversationStateStore.cs
adapters/stardew/tests/ProtocolMapper.Tests
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

- `CapabilityCatalog` 注册 `present_dialogue`；
- `present_dialogue` 为 `ExecutionMode.Sync`；
- `present_dialogue` 为 `CapabilityConcurrencyMode.Sequential`；
- input schema 限制 `text`、`reply_options`、`allow_free_text` 和 `additionalProperties=false`；
- Adapter handler 校验 `text`、`reply_options`、`allow_free_text` 的类型、长度和数量；
- `RuntimeClient` 能处理 `present_dialogue` ActionRequest；
- `reply_options` 最多 4 个；
- 超长 `text` 或 option 返回 `ActionResult(REJECTED)`，错误信息说明对应 limit；
- `ActionResult.output` 包含 `conversation_id`、`displayed_text`、`reply_options_count` 和 `allow_free_text`；
- `present_dialogue` 成功显示 UI 后追加 NPC conversation line；
- 无 active conversation 时 `present_dialogue` 创建新的 active conversation；
- 无 `reply_options` 且 `allow_free_text=false` 时，展示完成后关闭 active conversation。

## M4 Dialogue UI

目标：

```text
Adapter 展示可交互 Stardew 对话 UI，并把玩家选择或输入送回 Runtime。
```

修改范围：

```text
adapters/stardew/src/Dialogue/DialogueInteractionMenu.cs
adapters/stardew/src/Dialogue/DialogueInteractionController.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/ModEntry.cs
adapters/stardew/tests/ProtocolMapper.Tests
adapters/stardew/tests/check-context-static.ps1
```

验收命令：

```powershell
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

- UI 在主线程显示；
- active menu 存在时延迟显示；
- NPC 原生对话进入 Stardew UI 流程后才发送 `ActionResult(SUCCEEDED)`；
- 回复选项或内联自定义输入行不得阻塞 sync `ActionResult`；它们由 Adapter 状态机在玩家推进 NPC 台词后继续展示；
- Adapter 准备发送同一 NPC 新 GameEvent 前关闭该 NPC 未决 dialogue UI；
- NPC 台词先出现在 Stardew 原生 dialogue box；
- 玩家推进 NPC 台词后才出现回复入口；
- NPC 台词、回复菜单和自由输入框不会同时显示在居中自绘 modal 里；
- 最多展示 4 个可见回复入口；
- `allow_free_text=true` 时，最后一个可见行是内联自定义文本输入；当生成选项足够多时，该输入行占用第 4 个可见槽位；
- 玩家选择 option 后发送 `player_said_to_npc`；
- 玩家提交 free text 后发送 `player_said_to_npc`；
- 玩家关闭菜单不发送事件，并关闭 active conversation；
- Adapter 抢占同一 NPC 旧菜单不关闭 active conversation；
- UI 组件不直接调用 LLM；
- 本阶段不新增 Harmony patch；
- 手工加载 mod 到 Stardew 实测：点 NPC、选择 option、输入 free text、关闭菜单，确认事件发送与 conversation 状态符合预期。

## M5 face_player Capability

目标：

```text
模型可以让 NPC 面向玩家，为对话与后续移动能力建立低风险动作基础。
```

修改范围：

```text
adapters/stardew/src/Capabilities/FacePlayerCapability.cs
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/tests/ProtocolMapper.Tests
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

- `CapabilityCatalog` 注册 `face_player`；
- `face_player` 为 `ExecutionMode.Sync`；
- `face_player` 为 `CapabilityConcurrencyMode.Sequential`；
- NPC 与玩家同 location 时返回 `SUCCEEDED`；
- NPC 与玩家不同 location 时返回 `REJECTED`；
- tile delta 到 facing direction 的纯函数有测试覆盖；
- 成功 output 包含 `facing`。

## M6 Runtime ContextFact Memory Projection

目标：

```text
Runtime 可以把通用 ContextFact 和可见 Action outcome 投影成同一条 Turn 级 Recent Memory。
```

修改范围：

```text
runtime/config/agent.json
runtime/internal/agent/loop.go
runtime/internal/agent/loop_test.go
runtime/internal/context/renderer.go
runtime/internal/context/builder_test.go
runtime/internal/memory/record.go
runtime/internal/memory/projector.go
runtime/internal/memory/projector_test.go
```

验收命令：

```powershell
go test ./runtime/internal/memory
go test ./runtime/internal/context
go test ./runtime/internal/agent
go test ./runtime/...
```

通过标准：

- `MemoryRecord` 保存 `SourceContextFacts` 和 `Outcomes`；
- `MemoryRecord.SourceEventSequence` 来自 `GameEvent.sequence`；
- `MemoryProjector` 复制 `GameEvent.context_facts`，不解析 `Observation.state.stardew`；
- `MemoryProjector` 支持 `SourceContextFacts` 非空且 `Outcomes` 为空的 record；
- `MemoryProjector` 不为 settle-only Turn 合成空 ToolCall / ActionResult；
- `agent.Loop` 的 memory 更新函数改名后能表达 Turn 级 memory projection 职责；
- 成功完成且 settle-only 的 `player_said_to_npc` Turn 在存在 `ContextFact` 时写入 Recent Memory；
- failed / timeout / max_steps_exceeded Turn 不得只凭 `ContextFact` 写入 Recent Memory；
- technical terminal failure 写入 prior successful outcome 时不附带 `SourceContextFacts`；
- 既无 `SourceContextFacts` 又无 successful visible outcomes 的 Turn 不写 Memory；
- Runtime 对 `ContextFact.kind` 只识别通用词表，不识别 game-specific `event_type`；
- Recent Memory 渲染顺序为 SourceContextFacts 在前，NPC visible action outcome 在后；
- `GameTime` 严格晚于当前 `GameTime` 的 MemoryRecord 不进入 Recent Memory context；
- 相等 `GameTime` 的多条 Memory 按 `SourceEventSequence` 升序稳定渲染；
- 未来时间过滤发生在 memory context byte budget trim 之前；
- 当前 Turn 的 `[Current Event]` 允许 payload text 与 context_facts text 同时出现，Recent Memory 只消费 context_facts；
- prompt 说明玩家回复通过后续 `player_said_to_npc` 事件到达；
- prompt 说明 `present_dialogue` 可生成最多 4 个玩家回复选项；
- prompt 说明有回复选项或允许玩家输入时使用 `present_dialogue`，普通单句使用 `speak`；
- prompt 说明省略回复选项且不允许自由输入表示当前对话结束；
- context renderer 测试覆盖 `GameEvent.context_facts`；
- context renderer 测试覆盖 nested `Observation.state.stardew.conversation.recent_lines`；
- Recent Memory 摘要显示 `present_dialogue.text`；
- Recent Memory 摘要包含 `face_player` 行为；
- Runtime 不新增 Stardew-specific parser。

## M7 Full Regression And Commit

目标：

```text
完成 Phase5.6 全量验收，并提交一个清晰开发块。
```

验收命令：

```powershell
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet run --project adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet run --project adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
go test ./runtime/internal/context
go test ./runtime/...
go test ./protocol/gen/go/...
git diff --check
```

通过标准：

- 全部命令通过；
- Stardew 手工 smoke 通过：点 NPC、确认原生 NPC 对话先出现、推进后选择 option、通过最后一个可见输入行输入 free text、关闭菜单；
- Phase5.6 commit 只包含 Stardew interaction surface、ContextFact protocol additive update 和通用 Runtime context/memory regression；
- 不包含 Phase6 async lifecycle；
- 不包含 movement capability；
- 不包含 `ContextFact` 之外的 protocol 字段。

提交信息：

```text
feat: add stardew dialogue context facts
```

---

# 10. 实现顺序

```text
M0 Protocol ContextFact
M1 Conversation State Model
M2 Player Dialogue Event
M3 present_dialogue Capability
M4 Dialogue UI
M5 face_player Capability
M6 Runtime ContextFact Memory Projection
M7 Full Regression And Commit
```

约束：

- M0 完成前不让 Adapter 事件填充 `context_facts`；
- M1 完成前不开发 UI；
- M2 完成前不让 UI 发送 Runtime event；
- M3 完成前不把 `present_dialogue` 暴露给模型；
- M4 完成前不修改 Phase6 async 文档；
- M6 完成前不验收 Phase5.6；
- Phase5.6 完成前不进入 Phase6 movement / async runtime 开发。

---

# 11. 与 Phase6 的衔接

Phase5.6 完成后，Phase6 可以使用真实对话事件作为 movement 触发来源：

```text
player_said_to_npc: "Can you come here?"
  -> Runtime AgentTurn
  -> ModelDecision(movement capability)
  -> Async Action lifecycle
  -> terminal ActionResult
  -> re-observe
  -> present_dialogue
  -> settle
```

具体 movement capability 名称与输入 schema 由 Phase6 文档定义。

Phase5.6 不新增 movement capability，也不修改 Phase6 async lifecycle。Phase6 默认可依赖 `ContextFact` 读取玩家 utterance 记忆，不重新设计玩家输入 Memory。

---

# 12. 验收记录

Phase5.6 验收完成后记录：

```text
Adapter:
  - player_interacted_with_npc carries conversation_id
  - player_said_to_npc event accepted
  - Observation.state.stardew.conversation rendered
  - present_dialogue displays NPC text and reply affordances
  - present_dialogue writes NPC dialogue lines into active conversation
  - speak remains plain one-line dialogue and does not write conversation state
  - face_player works as sync action

Runtime:
  - generic renderer carries ContextFact and nested conversation state
  - MemoryRecord carries SourceContextFacts and visible action outcomes
  - player utterance ContextFact can enter Recent Memory even on settle-only turns
  - SourceContextFacts render before visible action outcomes inside one MemoryRecord
  - failed turns do not write SourceContextFacts into Memory
  - empty turns without SourceContextFacts or successful visible outcomes do not write Memory
  - Recent Memory summary carries present_dialogue text
  - future game-time Memory is filtered with strict GameTime > CurrentGameTime before entering Model Context
  - no Stardew-specific parser
  - no async lifecycle changes

Deferred:
  - ask_player same-turn human-in-loop
  - Phase6 movement capability
  - AgentDefinition store
  - long-term conversation persistence
  - canonical dialogue retrieval
```
