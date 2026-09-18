# GameAgent MVP0 Phase6 Async Action Protocol Strategy ADR

> **Status:** Accepted
> **Date:** 2026-08-31
> **Scope:** Tool Policy Generalization, Async Action Lifecycle, Action Source Correlation and Turn Completion Protocol Strategy
> **Architecture Baseline:** GameAgent Runtime Architecture v0.6
> **Roadmap Baseline:** GameAgent 阶段规划 v0.8
> **Protocol Baseline:** gameagent.protocol.v1alpha2 after Phase5.6

---

# 1. Decision

Phase6 继续复用现有 `ActionRequest / ActionStatusUpdate / ActionResult / CancelActionRequest` 表达异步 Action lifecycle。

Phase6 additive 增加 `ActionRequest.source_event_id` / `source_turn_id`，用于把 Runtime 发出的 Action 绑定回触发它的 accepted GameEvent 和 Runtime Turn：

```text
ActionRequest
  source_event_id
  source_turn_id
```

Phase6 additive 增加 Runtime -> Adapter 的 `TurnCompletion` 终态信号，用于表达已接受 GameEvent 对应的 AgentTurn 已经结束：

```text
RuntimeMessage.turn_completion
  turn_id
  event_id
  world_id
  entity_id
  status = COMPLETED / FAILED / CANCELLED
  error
```

Phase6 不新增 Action batch protocol，不把 AgentStep 暴露给 Adapter，不把 model transcript 暴露给 Protocol。

Phase6 不新增 `CapabilityPolicy` 正式 proto 字段。Runtime 可读的 tool policy 使用现有 `Capability.extensions.gameagent.tool_policy` 承载：

```text
exclusive_per_step
settle_after_success
```

---

# 2. Rationale

现有 v1alpha2 已经表达 Phase6 需要的 Action 生命周期：

```text
- Capability 能声明 sync / async execution mode；
- Action 有独立 action_id；
- Adapter 能发送非终态 ActionStatusUpdate；
- Adapter 能发送终态 ActionResult；
- Runtime 能发送 best-effort CancelActionRequest。
```

Phase5.6 引入了跨 Turn conversation 和同步 Dialogue UI。Adapter 在 `EventAck(ACCEPTED)` 后会进入等待态，但现有协议没有 Runtime -> Adapter 的 Turn 终态信号：

```text
EventAck
    只表示 Runtime 接受事件，不表示 Turn 已完成。

ActionResult
    只表示某个 Environment Action 完成，不覆盖 settle-only / no-action Turn。

Heartbeat / Error
    不承载 Turn lifecycle 语义。
```

因此 Phase6 需要一个小的 terminal Turn signal，让 Adapter 可以释放 interaction context、清理 pending lock，并让真实 UI 行为与 Runtime Turn 终态对齐。

Phase5.6 还暴露了一个 Action 执行时校验所需的 source correlation 缺口。Adapter 在 EventAck(ACCEPTED) 后以 GameEvent.event_id 记录 interaction context snapshot；如果 ActionRequest 不携带源 event_id，Adapter 只能按 NPC 或最近事件猜测上下文。Phase6 通过 ActionRequest additive 字段让 Runtime 显式传递 source_event_id / source_turn_id。

Phase5.6 还让 `present_dialogue` 成为第一个具有特殊 Turn 收敛语义的 Environment Tool。该语义不属于工具名本身，而属于 capability 对 Runtime 做出的执行策略承诺。Phase6 通过 `Capability.extensions` 收敛该边界，避免 Runtime Core 按 Stardew capability name 写特殊分支。

---

# 3. Protocol Additive Change

ActionRequest additive：

```protobuf
message ActionRequest {
  string action_id = 1;
  string entity_id = 2;
  string capability = 3;
  google.protobuf.Struct arguments = 4;
  google.protobuf.Struct extensions = 5;
  string world_id = 6;
  string source_event_id = 7;
  string source_turn_id = 8;
}
```

TurnCompletion additive：

```protobuf
enum TurnCompletionStatus {
  TURN_COMPLETION_STATUS_UNSPECIFIED = 0;
  TURN_COMPLETION_STATUS_COMPLETED = 1;
  TURN_COMPLETION_STATUS_FAILED = 2;
  TURN_COMPLETION_STATUS_CANCELLED = 3;
}

message TurnCompletion {
  string turn_id = 1;
  string event_id = 2;
  string world_id = 3;
  string entity_id = 4;
  TurnCompletionStatus status = 5;
  Error error = 6;
}
```

`RuntimeMessage.oneof payload` 增加：

```protobuf
TurnCompletion turn_completion = 17;
```

字段策略：

```text
- 使用 RuntimeMessage 当前 oneof 的下一个可用字段号；
- 不修改 AdapterMessage；
- ActionRequest 只 additive 增加 source correlation 字段；
- 不修改 ActionStatusUpdate / ActionResult；
- 不新增 target_definition_id、Observation.definition_id 或 AgentDefinition store 字段；
- 不新增 CapabilityPolicy 正式字段，tool policy 使用 Capability.extensions；
- 不新增 ActionBatchRequest / ActionBatchResult。
```

---

# 4. Protocol Semantics

## 4.1 Capability.execution_mode

`Capability.execution_mode` 表示 Adapter 对该能力执行生命周期的承诺。

```text
EXECUTION_MODE_SYNC
    Adapter 会在短时间内返回 terminal ActionResult。

EXECUTION_MODE_ASYNC
    Adapter 接收 ActionRequest 后，可以先返回 ActionStatusUpdate，
    并在未来某个时间返回 terminal ActionResult。

EXECUTION_MODE_UNSPECIFIED
    MVP0 按 sync 处理，保留旧 fixture 和未显式声明能力的兼容行为。
```

## 4.2 Capability.extensions.gameagent.tool_policy

`Capability.description` 是 model-facing 工具用途说明。Runtime 不从 description 解析执行策略。

`Capability.extensions.gameagent.tool_policy` 是 Runtime-facing 执行策略。Phase6 只定义：

```text
exclusive_per_step
    true 时，该 ToolCall 必须单独占据当前 AgentStep。

settle_after_success
    true 时，该 ToolCall terminal SUCCEEDED 且当前 step 无 model-visible failure 后，
    Runtime 不再发起下一次 model request，直接完成当前 Turn。
    Phase6 仅用于 sync tool。async tool terminal 后必须 re-observe 当前 entity，
    再进入下一 AgentStep；async tool 不使用 settle_after_success 直接收敛 Turn。
```

缺失 policy 时按零值处理。字段存在但类型非法时，Runtime 注册 capability 时跳过该 capability。

Runtime Core MUST NOT 根据 `present_dialogue`、`ask_player`、`show_choices` 等具体 capability name 推断上述策略。

后续玩家输入或环境进展由新的 GameEvent 驱动这一事实，属于 capability description 与 event contract，不作为 Phase6 Runtime-facing policy。

Stardew `present_dialogue` 在 Phase6 必须声明 `exclusive_per_step` 与 `settle_after_success` 为 true。

## 4.3 ActionRequest Source Correlation

`ActionRequest.source_event_id` 表示触发当前 Runtime Action 的原始 GameEvent。

写入规则：

```text
- 对 accepted GameEvent 触发的 AgentTurn，Runtime 构造的每个 ActionRequest 必须写入 source_event_id；
- source_event_id 取原 GameEvent.event_id；
- source_turn_id 取 Runtime 当前 Turn ID；
- Adapter 使用 source_event_id 查找 EventAck(ACCEPTED) 后记录的 interaction context snapshot；
- source_turn_id 只作为诊断字段，不作为 Adapter context matching 主键；
- 模型不生成 source_event_id、source_turn_id 或 conversation_id；
- 非事件触发的未来 Runtime maintenance action 可以不带 source_event_id，Phase6 不实现该路径。
```

Interaction Context Guard 必须基于 `source_event_id` 查找 snapshot。Adapter 不得从 tool arguments 推断 source event。

## 4.4 ActionStatusUpdate

`ActionStatusUpdate` 表示异步 Action 的非终态进展。

Phase6 只把以下状态视为有效 start progress：

```text
ACTION_STATUS_ACCEPTED
ACTION_STATUS_RUNNING
```

`ACTION_STATUS_PENDING` 可以记录 trace，但不得被 Runtime 当作 Adapter 已接管 action 的承诺。

终态必须通过 `ActionResult` 表达：

```text
ACTION_STATUS_SUCCEEDED
ACTION_STATUS_FAILED
ACTION_STATUS_REJECTED
ACTION_STATUS_CANCELLED
ACTION_STATUS_INTERRUPTED
```

## 4.5 ActionResult

`ActionResult` 是 Action terminal truth。

Runtime 只在收到 terminal `ActionResult` 后生成 model-visible `ToolResult`。

`ActionStatusUpdate` 不进入 model transcript；它属于 lifecycle trace，不属于模型下一步决策需要直接消费的工具结果。

## 4.6 CancelActionRequest

Cancel 保持 best-effort 语义：

```text
如果 Action 尚未执行或仍可中断，Adapter 尽量停止它；
如果 Action 已经产生游戏副作用，Runtime 不要求回滚。
```

Runtime 对超时发出 `CancelActionRequest` 后，当前等待失败；之后到达的 late terminal result 不恢复已失败的 Turn。

如果 late terminal result 对应的游戏副作用已经发生，Runtime 不回滚游戏状态。后续 GameEvent 触发的新 Turn 通过 Observe 收敛到真实 Environment state。

## 4.7 TurnCompletion

`TurnCompletion` 表示 Runtime 已经结束某个 accepted GameEvent 对应的 AgentTurn。

发送规则：

```text
- 每个 accepted GameEvent 最多发送一次；
- Duplicate / rejected GameEvent 不发送；
- settle-only / no-action Turn 也发送；
- TurnCompletion.event_id 与原 GameEvent.event_id 一致；
- TurnCompletion.turn_id 是 Runtime 内部 Turn ID 的 protocol projection；
- TurnCompletion.world_id / entity_id 来自 Turn target；
- failed / cancelled status 携带 Error；
- Adapter 使用 event_id 释放 EventAck(ACCEPTED) 后记录的 interaction context；
- TurnCompletion.turn_id 只作为诊断字段，不作为 Adapter context matching 主键；
- TurnCompletion 发送失败只记录非终态 trace，不改变已经判定的 Turn terminal status；
- Adapter 收到未知 event_id 或已释放 context 的 TurnCompletion 时安全忽略。
```

映射规则：

```text
turn_completed  -> TURN_COMPLETION_STATUS_COMPLETED
turn_failed     -> TURN_COMPLETION_STATUS_FAILED
explicit cancelled turn -> TURN_COMPLETION_STATUS_CANCELLED
```

TurnCompletion 不进入 model transcript，不写 Memory，不替代 ActionResult。

`TURN_COMPLETION_STATUS_CANCELLED` 是协议枚举值，用于未来显式 Turn cancellation。Phase6 的 timeout、async wait failed、Action terminal failure 均收敛为 failed Turn。

---

# 5. Phase6 Limits

Phase6 只支持：

```text
- 单个 AgentTurn 内最多一个 async ToolCall；
- async ToolCall 必须单独占据一个 AgentStep；
- async ToolCall 与其它 ToolCall 不在同一个 batch 中执行；
- async terminal result 到达后，Runtime re-observe 当前目标实体，再进入下一 AgentStep；
- async terminal SUCCEEDED 后 re-observe 失败时，Runtime fail Turn，不使用 stale observation 继续；
- Adapter 用 TurnCompletion 释放本地 interaction context；
- Adapter 在 effect time 基于 ActionRequest.source_event_id 执行 Interaction Context Guard。
```

Phase6 不支持：

```text
Runtime Core hardcoded game-specific capability name policy
CapabilityPolicy proto field
多个并发长 Action
一个 Step 内混合同步和异步 ToolCall
Runtime 崩溃后的 continuation 恢复
跨 Environment reconnect 恢复 pending async action
ActionBatchRequest / ActionBatchResult
路径规划进入 Runtime
事务回滚
Workflow Engine
同一 Turn 内等待玩家输入
```

---

# 6. Acceptance

ADR 通过标准：

```text
1. protocol/proto/gameagent.proto additive 增加 ActionRequest.source_event_id / source_turn_id。
2. protocol/proto/gameagent.proto additive 增加 TurnCompletion。
3. RuntimeMessage.oneof 增加 turn_completion = 17。
4. protocol static check 断言 ActionRequest.source_event_id = 7、source_turn_id = 8、RuntimeMessage.turn_completion = 17。
5. CapabilityPolicy 不新增 proto 字段，Runtime 从 Capability.extensions.gameagent.tool_policy 读取 policy。
6. Runtime tests 证明 exclusive_per_step / settle_after_success 不依赖具体 capability name。
7. Stardew adapter tests 证明 present_dialogue 声明所需 tool_policy。
8. Runtime tests 证明 exclusive_per_step 违规的 ToolResult code 不包含具体 capability name。
9. Go / C# generated code 与 proto 一致。
10. Runtime tests 证明 ActionRequest 会带原 GameEvent.event_id 和当前 turn_id。
11. Runtime tests 证明 TurnCompletion 会在 accepted GameEvent 的 terminal outcome 确定后发送。
12. Runtime tests 证明 ActionStatusUpdate 会被接收和 trace。
13. Runtime tests 证明 ASYNC capability 可以进入 tool view。
14. Runtime tests 证明 async action terminal result 能恢复原 AgentTurn。
15. Runtime tests 证明 async ToolCall terminal 后必须 re-observe 并进入下一 AgentStep，不通过 settle_after_success 直接完成 Turn。
16. Runtime tests 证明 re-observe 失败不会使用 stale observation 继续 Turn。
17. Runtime tests 证明 async timeout 或 TurnTimeout 会发送 CancelActionRequest，并且 late result 不恢复已失败 Turn。
18. Stardew adapter tests / build 证明 move_to 使用 EXECUTION_MODE_ASYNC。
19. Stardew adapter tests 证明 TurnCompletion 可以释放 interaction context。
20. Stardew adapter tests 证明 interaction-bound Action 使用 source_event_id 执行 guard。
```

验收命令：

```powershell
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
go test ./runtime/...
go test ./protocol/gen/go/...
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet build adapters/stardew/GameAgent.Stardew.csproj
git diff --check
```
