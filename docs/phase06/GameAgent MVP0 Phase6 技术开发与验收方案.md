# GameAgent MVP0 Phase6 技术开发与验收方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-08-31
> **Scope:** Tool Policy Generalization, Turn Completion, Action Source Correlation, Interaction Guard, Async Action Lifecycle and AgentTurn Resume
> **Architecture Baseline:** GameAgent Runtime Architecture v0.6
> **Roadmap Baseline:** GameAgent 阶段规划 v0.8
> **Entry ADR:** [Async Action Protocol Strategy ADR](GameAgent MVP0 Phase6 Async Action Protocol Strategy ADR.md)
> **Protocol Baseline:** gameagent.protocol.v1alpha2 after Phase5.6
> **Previous Phase Gate:** Phase5.6 Stardew Dialogue Interaction Surface Accepted

---

# 1. 阶段目标

Phase5 已经证明一个 AgentTurn 可以包含多个有界 AgentStep，同一 Step 可以执行 ordered ToolCall batch，并把 ToolResult 回灌给模型。

Phase5.5 已经证明 Stardew Adapter 可以通过 `Observation.state.stardew` 提供成熟的当前事实，Runtime 不需要理解 Stardew 字段。

Phase5.6 已经证明 Stardew Adapter 可以维护跨 Turn conversation，并通过 `present_dialogue` / `player_said_to_npc` / `ContextFact` 打通 NPC 台词、玩家回复、Runtime AgentTurn 和 Recent Memory。

Phase6 要证明：

> **长时间运行的 Environment Action 不等于同步函数；Runtime 可以启动异步 Action，等待 terminal result，刷新 Observation，并恢复同一个 AgentTurn 继续推进。**

Phase6 还要收敛 Phase5.6 暴露出的 Runtime tool-name hardcode：

> **Runtime Core 不按具体 capability 名称执行特殊规则；Adapter 通过 Capability.description 给模型说明工具用途，通过 Capability.extensions.gameagent.tool_policy 给 Runtime 声明执行策略。**

该改进作为 Phase6 的前置里程碑处理，不回写 Phase5.6，也不新增 Phase5.7。

Phase6 还要补齐 Phase5.6 暴露出的交互生命周期缺口：

> **Adapter 可以知道一个已接受 GameEvent 对应的 Turn 已经终结，并释放等待态交互上下文。**

目标链路：

```text
GameEvent(player_interacted_with_npc / player_said_to_npc)
  -> EventAck(ACCEPTED)
  -> Adapter records interaction context snapshot
  -> Observe
  -> AgentStep #1
  -> ModelDecision(policy-driven tool call or settle)
  -> ActionRequest(capability, source_event_id, source_turn_id)
  -> ActionStatusUpdate(ACCEPTED / RUNNING)
  -> AgentTurn suspended
  -> ActionResult(SUCCEEDED / FAILED / REJECTED / CANCELLED / INTERRUPTED)
  -> re-observe target entity
  -> AgentTurn resumed
  -> AgentStep #2
  -> ToolResult transcript visible to model
  -> settle
  -> TurnCompletion(COMPLETED / FAILED)
  -> Adapter releases interaction context
```

---

# 2. 阶段结论

Phase6 做这些工作：

```text
1. 接受 Async Action Protocol Strategy ADR。
2. Runtime Tool Policy Generalization：Runtime Core 不再硬编码 Stardew capability name 执行特殊规则。
3. Stardew Adapter 通过 Capability.extensions.gameagent.tool_policy 声明 `present_dialogue` 的执行策略。
4. Protocol additive 增加 `ActionRequest.source_event_id` / `source_turn_id`，让 Adapter 可以把 Action 绑定回触发它的 accepted GameEvent。
5. Protocol additive 增加 Runtime -> Adapter 的 TurnCompletion 终态信号。
6. Runtime Gateway 在每个 accepted GameEvent 对应 Turn 终结时发送 TurnCompletion。
7. Stardew Adapter 使用 Action source correlation 执行 Interaction Context Guard，并使用 TurnCompletion 释放 pending interaction context。
8. Runtime Gateway 分发 ActionStatusUpdate。
9. Runtime Environment Port 支持 async action start / wait / cancel。
10. Tool Registry 暴露 ASYNC capability，并保存 execution mode metadata。
11. Tool Scheduler 支持单 async ToolCall 的 start -> wait -> terminal result。
12. AgentLoop 支持 waiting / suspended / resumed trace，并在 async terminal result 后 re-observe。
13. Context transcript 继续只接收 terminal ToolResult，不把 ActionStatusUpdate 当作 ToolResult。
14. Memory 沿用 Phase5.6 的 SourceContextFacts + visible outcome 投影；本阶段只新增 terminal SUCCEEDED async action outcome。
15. Stardew Adapter 增加一个真实异步 Environment Tool：move_to。
16. 确定性测试夹具覆盖 tool policy、Action source correlation、TurnCompletion、status update、延迟 terminal result、timeout cancel、late result、resume。
```

Phase6 不做这些工作：

```text
ActionBatchRequest / ActionBatchResult
多个并发长 Action
一个 Step 内混合同步和异步 ToolCall
Runtime 崩溃后的 continuation 恢复
跨 Environment reconnect 恢复 pending async action
Workflow Engine
复杂行为树
路径规划进入 Runtime
事务回滚
同一 Turn 内等待玩家输入
长期 conversation persistence
AgentDefinition store
canonical dialogue retrieval
long-term event memory persistence
玩家输入 ContextFact / Recent Memory projection 重新设计
除 ActionRequest source correlation 与 TurnCompletion 之外的 Protocol 字段变更
CapabilityPolicy 正式 proto 字段
Runtime 解析 game-specific capability name 执行特殊规则
Runtime 配置文件放入 Adapter 目录
```

等待 LLM 或等待异步 Action 期间，游戏世界继续运行。Phase6 不把“冻结玩家或 NPC”作为 Runtime 能力；Stardew Adapter 通过 interaction context snapshot 和执行前 guard 保证 UI 与 Action 不落到过期上下文。

---

# 3. 架构边界

## 3.1 TurnCompletion 是 Turn 终态，不是 Action 结果

`EventAck(ACCEPTED)` 只表示 Runtime 接受了 GameEvent，不表示 Turn 已经完成。

`ActionResult` 只表示某个 Action 的终态，不表示整个 AgentTurn 已经完成。

Phase6 新增 Runtime -> Adapter 的 `TurnCompletion`：

```text
TurnCompletion
  turn_id
  event_id
  world_id
  entity_id
  status = COMPLETED / FAILED / CANCELLED
  error
```

语义：

```text
- 每个 accepted GameEvent 最多对应一个 TurnCompletion；
- TurnCompletion 必须在 Runtime terminal outcome 已确定后、唯一 terminal trace 之前发送；
- TurnCompletion 是 Adapter 释放 interaction context / pending lock 的正式信号；
- TurnCompletion 不进入 model transcript；
- TurnCompletion 不替代 ActionResult。
```

`TURN_COMPLETION_STATUS_CANCELLED` 是协议枚举值，用于未来显式 Turn cancellation。Phase6 的 timeout、async wait failed、Action terminal failure 均收敛为 failed Turn；不新增独立的 Turn cancellation 入口。

## 3.2 Action 不是同步函数

整体架构已定义：

```text
Action = Runtime 请求 Environment 执行的一次具有独立业务身份和生命周期的副作用操作。
```

Phase6 将当前同步假设拆开：

```text
Phase5:
    SubmitAction(ctx, req) -> terminal ActionResult

Phase6:
    StartAction(ctx, req) -> accepted / running / fast terminal
    WaitActionResult(ctx, action_id) -> terminal ActionResult
    CancelAction(action_id, reason) -> best-effort cancellation
```

`action_id` 是 Runtime 与 Adapter 之间关联异步生命周期的唯一业务 ID。

## 3.3 ActionRequest 必须携带来源事件关联

Phase6 中，Adapter 的 Interaction Context Guard 需要知道某个 Action 是由哪个 accepted GameEvent 对应的 AgentTurn 触发。Runtime 必须在构造 ActionRequest 时写入：

```text
source_event_id = GameEvent.event_id
source_turn_id  = Runtime turn_id
```

`source_event_id` 是 Adapter 查找 interaction context snapshot 的主键。`source_turn_id` 只用于诊断和 trace 对齐，不作为 Adapter context matching 主键。

模型不负责生成 `source_event_id`、`source_turn_id` 或 `conversation_id`。这些字段由 Runtime 从 Turn context 写入协议消息。

## 3.4 Capability description 与 tool_policy 分工

`Capability.description` 是模型可读的自然语言说明，用于解释工具用途、参数语义和游戏侧效果。

`Capability.extensions.gameagent.tool_policy` 是 Runtime 可读的结构化执行策略，用于表达少量通用调度约束。

Phase6 只定义以下 policy：

```text
exclusive_per_step
    该 tool call 必须单独占据当前 AgentStep。

settle_after_success
    该 tool call terminal SUCCEEDED 且当前 step 无 model-visible failure 时，
    Runtime 不再发起下一次 model request，直接进入 Turn completed / settle path。
    Phase6 仅用于 sync tool。async tool terminal 后必须 re-observe 当前 entity，
    再进入下一 AgentStep；async tool 不使用 settle_after_success 直接收敛 Turn。
```

Runtime Core MUST NOT 从 capability name 推断上述策略。Stardew 的 `present_dialogue`、其它游戏的 `ask_player` 或 `show_choices` 都必须通过同一套 policy 表达相同执行语义。

后续玩家输入或环境进展由新的 GameEvent 驱动这一事实，属于 capability description 与 event contract，不作为 Phase6 Runtime-facing policy。

Phase6 不新增 `CapabilityPolicy` proto 字段。上述策略先使用既有 `Capability.extensions` 承载，等 Phase6 / Phase7 验证稳定后再决定是否升格为正式协议字段。

## 3.5 AgentStep 仍然不进入 Protocol

`AgentStep` 是 Runtime 内部推理推进单位。Adapter 不需要知道：

```text
step_index
ToolResult transcript
settle control
multi-step budget
resume 后是不是下一次模型调用
```

Adapter 只处理：

```text
ActionRequest
ActionStatusUpdate
ActionResult
CancelActionRequest
TurnCompletion
```

## 3.6 Runtime 不接管路径规划

`move_to` 的目标解析、可达性判断、寻路、主线程执行、中断和失败原因都属于 Stardew Adapter。

Runtime 只负责：

```text
ToolCall envelope validation
ActionRequest routing
action lifecycle correlation
timeout / cancel
ToolResult transcript
AgentTurn continuation
TurnCompletion
trace
```

## 3.7 Current Observation 在 async resume 后刷新

长 Action 可能改变游戏状态。Phase6 规定：

```text
收到 async terminal ActionResult 后，
Runtime 必须重新 Observe 当前 target entity，
再构建下一步 model request。
```

这样模型看到的是 action 后的当前事实，而不是 action 启动前的旧位置、旧场景或旧 schedule。

## 3.8 Status Update 是 trace，不是 ToolResult

`ActionStatusUpdate(ACCEPTED / RUNNING)` 表示 Adapter 已接管异步 Action 或正在执行。

它进入 trace：

```text
action_status_update_received
turn_suspended
turn_resumed
```

它不进入 model transcript。模型只看到 terminal `ToolResult`。

---

# 4. Protocol 设计

## 4.1 Additive Proto

修改范围：

```text
protocol/proto/gameagent.proto
protocol/tests/check-protocol-static.ps1
protocol/gen/go/...
```

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
- 不修改 ActionResult；
- 不新增 ActionBatchRequest / ActionBatchResult；
- 不引入 TurnStep / transcript / Runtime internal 字段。
```

## 4.2 Capability Extensions Tool Policy

Phase6 使用 `Capability.extensions` 承载 Runtime 可读 tool policy，不新增正式 proto 字段。

规范结构：

```json
{
  "gameagent": {
    "tool_policy": {
      "exclusive_per_step": true,
      "settle_after_success": true
    }
  }
}
```

字段语义：

```text
exclusive_per_step
    true 时，该 ToolCall 必须是当前 ModelDecision 中唯一的 ToolCall。

settle_after_success
    true 时，该 ToolCall terminal SUCCEEDED 且当前 step 无 model-visible failure 后，
    Runtime 不再发起下一次 model request，直接完成当前 Turn。
```

读取规则：

```text
- 缺失 gameagent.tool_policy 时全部按 false 处理；
- 非 boolean 字段视为无效 capability metadata，注册时跳过该 capability；
- Runtime 不从 Capability.description 解析 policy；
- Runtime 不从 capability name 推断 policy；
- Capability.description 继续作为 model-facing 工具用途说明。
```

Stardew Adapter 在 Phase6 中必须给 `present_dialogue` 声明：

```text
exclusive_per_step = true
settle_after_success = true
```

## 4.3 ActionRequest Source Correlation

写入规则：

```text
- 对 accepted GameEvent 触发的 AgentTurn，Runtime 构造的每个 ActionRequest 必须带 source_event_id 与 source_turn_id；
- source_event_id 取原 GameEvent.event_id；
- source_turn_id 取 Runtime 当前 Turn ID；
- Adapter 使用 source_event_id 查找 EventAck(ACCEPTED) 后记录的 interaction context snapshot；
- Adapter 不从 model arguments 读取 conversation_id；
- 非事件触发的未来 Runtime maintenance action 可以不带 source_event_id，但 Phase6 不实现该路径。
```

`tool.BuildActionRequest` 的调用面必须能接收 Turn source context，避免 scheduler 或 Adapter 侧猜测来源事件。

## 4.4 TurnCompletion Semantics

映射规则：

```text
Runtime turn_completed      -> TURN_COMPLETION_STATUS_COMPLETED
Runtime turn_failed         -> TURN_COMPLETION_STATUS_FAILED
Runtime explicit cancelled  -> TURN_COMPLETION_STATUS_CANCELLED
```

发送规则：

```text
- 仅对 EventAck(ACCEPTED) 后真实创建的 AgentTurn 发送；
- Duplicate / rejected GameEvent 不发送；
- TurnCompletion 与原 GameEvent 使用相同 event_id；
- TurnCompletion.world_id / entity_id 来自 Turn target；
- TurnCompletion.error 仅在 failed / cancelled 时携带；
- Adapter 使用 event_id 释放 EventAck(ACCEPTED) 后记录的 interaction context；
- TurnCompletion.turn_id 只作为诊断字段，不作为 Adapter context matching 主键；
- TurnCompletion 发送失败只记录非终态 trace，不回滚 Turn 终态，不把 completed Turn 改判为 failed；
- Adapter 必须能接受未知 event_id 或已释放 context 的 TurnCompletion，并安全忽略。
```

---

# 5. Runtime 设计

## 5.1 Environment Port

修改范围：

```text
runtime/internal/agent/loop.go
runtime/internal/agent/scheduler.go
runtime/internal/tool/environment_tool.go
runtime/internal/gateway/stream_environment.go
runtime/internal/gateway/gateway.go
runtime/internal/gateway/stream_environment_test.go
runtime/internal/gateway/gateway_integration_test.go
```

目标接口形态：

```go
type ActionStart struct {
    ActionID string
    Status   protocolv1alpha2.ActionStatus
    Update   *protocolv1alpha2.ActionStatusUpdate
    Result   *protocolv1alpha2.ActionResult
}

type Environment interface {
    Observe(ctx context.Context, worldID string, entityID string) (*protocolv1alpha2.Observation, error)
    SubmitAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (*protocolv1alpha2.ActionResult, error)
    StartAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (ActionStart, error)
    WaitActionResult(ctx context.Context, actionID string) (*protocolv1alpha2.ActionResult, error)
    CancelAction(actionID string, reason string)
    SendTurnCompletion(ctx context.Context, completion *protocolv1alpha2.TurnCompletion) error
}
```

语义：

```text
SubmitAction
    保留给 SYNC capability，等待 terminal ActionResult。

StartAction
    注册 pending action，发送 ActionRequest，等待第一条 ACCEPTED / RUNNING status update。
    如果 terminal ActionResult 比 status update 更早到达，返回 fast terminal Result。

WaitActionResult
    等待同一 action_id 的 terminal ActionResult。

CancelAction
    发送 best-effort CancelActionRequest。

SendTurnCompletion
    在 AgentTurn 进入唯一终态后发送 Runtime -> Adapter terminal Turn signal。
```

Scheduler 选择 `SubmitAction` 或 `StartAction` 的唯一依据是 `Entry.Execution`。Runtime 不得用 capability name 判断某个 action 是 sync 还是 async。

ActionRequest 构造：

```text
Loop holds GameEvent.event_id and turn_id
  -> scheduler receives Action source context
  -> BuildActionRequest writes source_event_id / source_turn_id
  -> Adapter uses source_event_id for guard and source_turn_id for diagnostics
```

`streamEnvironment` 内部 pending action 统一为 lifecycle waiter：

```text
pendingActions[action_id]
  updates chan *ActionStatusUpdate
  results chan actionResult
```

`recvLoop` 必须分发：

```text
AdapterMessage_ActionStatus -> resolveActionStatusUpdate(action_id, update)
AdapterMessage_ActionResult -> resolveActionResult(action_id, result)
```

## 5.2 Tool Registry Execution And Policy Metadata

修改范围：

```text
runtime/internal/tool/registry.go
runtime/internal/tool/registry_test.go
```

`Entry` 增加 execution mode 与 tool policy：

```go
type ExecutionMode string

const (
    ExecutionSync  ExecutionMode = "sync"
    ExecutionAsync ExecutionMode = "async"
)

type ToolPolicy struct {
    ExclusivePerStep   bool
    SettleAfterSuccess bool
}

type Entry struct {
    Definition  model.ToolDefinition
    Kind        Kind
    Concurrency ConcurrencyMode
    Execution   ExecutionMode
    Policy      ToolPolicy
}
```

映射规则：

```text
Capability.execution_mode = SYNC         -> ExecutionSync
Capability.execution_mode = ASYNC        -> ExecutionAsync
Capability.execution_mode = UNSPECIFIED  -> ExecutionSync
```

Phase6 开始，`RegisterEnvironmentCapabilities` 不再排除 `EXECUTION_MODE_ASYNC`。

`RegisterEnvironmentCapabilities` 同时从 `Capability.extensions.gameagent.tool_policy` 读取 Runtime 可读策略。缺失 policy 时使用零值；字段存在但类型非法时跳过该 capability。

`Available()` 仍只返回 `[]model.ToolDefinition`，不把 execution metadata 暴露到 provider-specific contract。

Runtime Core 的执行路径不得硬编码 `present_dialogue`、`ask_player`、`show_choices` 等具体工具名。`exclusive_per_step` / `settle_after_success` 只能来自 `Entry.Policy`。

## 5.3 Scheduler Async Path

修改范围：

```text
runtime/internal/agent/scheduler.go
runtime/internal/agent/scheduler_test.go
runtime/internal/tool/environment_tool.go
runtime/internal/tool/environment_tool_test.go
```

Phase6 支持的 async 调度规则：

```text
- exclusive_per_step ToolCall 必须单独占据当前 AgentStep；
- exclusive_per_step preflight 在 sync / async execution 分叉之前执行，M1 落地后 M6 复用，不在 async 路径重写；
- sync settle_after_success ToolCall terminal SUCCEEDED 且当前 step 无 model-visible failure 后，Runtime 不再发起下一次 model request，直接完成 Turn；
- async ToolCall 必须单独占据当前 AgentStep；
- async ToolCall 不与其它 ToolCall 组成 batch；
- async ToolCall terminal 后必须 re-observe 当前 entity 并进入下一 AgentStep，不通过 settle_after_success 直接完成 Turn；
- async action 使用 ActionStartTimeout 等待 ACCEPTED / RUNNING；
- async action 使用 AsyncActionTimeout 等待 terminal ActionResult；
- timeout 时 Runtime 发送 CancelActionRequest；
- late ActionResult 不恢复已经 failed 的 AgentTurn；
- terminal ActionResult 转成普通 model.ToolResult，按既有 transcript 规则回灌。
```

新增 ToolResult code：

```text
exclusive_tool_must_be_only_tool_call
async_batch_unsupported
async_action_limit_exceeded
action_start_timeout
async_action_timeout
action_start_rejected
```

终态映射沿用 Phase5：

```text
SUCCEEDED     -> ToolResult.status = succeeded
REJECTED      -> ToolResult.status = rejected
FAILED        -> ToolResult.status = failed
CANCELLED     -> ToolResult.status = cancelled
INTERRUPTED   -> ToolResult.status = interrupted
```

## 5.4 AgentLoop Resume

修改范围：

```text
runtime/internal/agent/loop.go
runtime/internal/agent/loop_test.go
runtime/internal/context/builder_test.go
runtime/internal/memory/projector_test.go
runtime/internal/trace/trace.go
runtime/internal/trace/turn_test.go
```

Loop 行为：

```text
1. Step 返回 sync ToolCalls：
   走 Phase5 scheduler 逻辑。

2. Step 返回一个 async ToolCall：
   Loop 检查 MaxAsyncActionsPerTurn；
   scheduler start action；
   async action start count +1；
   Loop emit turn_suspended；
   scheduler wait terminal result；
   Loop emit turn_resumed；
   Loop re-observe target entity；
   terminal ToolResult 进入 transcript；
   继续下一 AgentStep。

3. Step 返回 async ToolCall + 任意其它 ToolCall：
   不发送 ActionRequest；
   生成 model-visible invalid/skipped ToolResult；
   继续下一 AgentStep。

4. 当前 Turn 已经达到 MaxAsyncActionsPerTurn 后再次返回 async ToolCall：
   不发送 ActionRequest；
   生成 model-visible async_action_limit_exceeded ToolResult；
   继续下一 AgentStep。

5. async terminal SUCCEEDED：
   completed Turn 写入 Memory outcome。

6. async terminal rejected / failed / cancelled / interrupted：
   ToolResult 对模型可见；
   模型可在剩余 step budget 内修正或 settle；
   settle 只有在当前 step 没有 model-visible failure 时才完成 Turn。

7. 任意 accepted GameEvent 对应的 Turn 进入 completed / failed：
   Runtime 发送 TurnCompletion。
```

计数规则：

```text
MaxAsyncActionsPerTurn 统计本 Turn 内已经 start 的 async action 数。
preflight 失败且未发送 ActionRequest 的 async ToolCall 不计数。
已经 start 的 async action 无论最终 succeeded / rejected / failed / cancelled / interrupted / timeout，均计入上限。
```

Re-observe 失败：

```text
async terminal SUCCEEDED 后 re-observe 失败时，Runtime 不使用 stale observation 继续下一 step。
该 Turn 进入 FAILED，并发送 TurnCompletion(FAILED)。
已经真实成功的 async action 可以按 technical failure 规则作为 prior successful visible outcome 写入 Memory。
该技术失败路径不附带本 Turn 的 SourceContextFacts。
```

## 5.5 Config Budgets

修改范围：

```text
runtime/internal/agent/config.go
runtime/internal/agent/config_test.go
runtime/config/agent.json
```

新增配置：

```json
{
  "action_start_timeout_ms": 3000,
  "async_action_timeout_ms": 45000,
  "max_async_actions_per_turn": 1,
  "turn_timeout_ms": 90000
}
```

默认值：

```text
ActionStartTimeout = 3s
AsyncActionTimeout = 45s
MaxAsyncActionsPerTurn = 1
TurnTimeout = 90s
```

`ActionTimeout = 3s` 继续表示同步 Action terminal wait 上限。

Phase6 的 TurnTimeout 仍是 global hard bound。异步等待不能绕过 TurnTimeout。

当 TurnTimeout 触发且当前存在 pending async action 时，Runtime 先发送 best-effort `CancelActionRequest`，再将 Turn 收敛为 failed。

Prompt 配置边界：

```text
- runtime/config/agent.json 可以继续作为 MVP0 单一配置文件；
- Runtime 默认 ToolInstruction 必须只表达通用工具使用规则，不写死 Stardew capability name；
- Stardew-specific prompt profile 后续可以放在 runtime/config/profiles/stardew.json；
- 游戏 profile 属于 Runtime 配置树，不放入 adapters/stardew 目录；
- 模型选择具体工具时，主要依赖当前 available tools 的 Capability.description 与 input schema。
```

## 5.6 Trace

修改范围：

```text
runtime/internal/trace/trace.go
runtime/internal/trace/turn_test.go
runtime/internal/agent/loop_test.go
runtime/internal/gateway/gateway_integration_test.go
```

新增事件：

```text
action_status_update_received
turn_suspended
turn_resumed
turn_completion_sent
turn_completion_send_failed
```

字段：

```text
step_index
tool_call_id
action_id
tool
action_status
wait_ms
turn_completion_status
reason
```

不变量：

```text
- turn_suspended / turn_resumed / turn_completion_sent / turn_completion_send_failed 是非终态事件；
- turn_completed / turn_failed 仍然唯一且最后；
- turn_cancelled 仅在未来显式 Turn cancellation 入口实现后成为 terminal event；
- ActionStatusUpdate 不改变 Memory；
- TurnCompletion 不改变 Memory；
- trace 不成为 action lifecycle source of truth。
```

---

# 6. Adapter 设计

## 6.1 Interaction Context Guard

修改范围：

```text
adapters/stardew/src/Dialogue/InteractionContextStore.cs
adapters/stardew/src/Dialogue/ConversationStateStore.cs
adapters/stardew/src/Capabilities/PresentDialogueCapability.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
adapters/stardew/tests/check-context-static.ps1
```

Adapter 在 `player_interacted_with_npc` 与 `player_said_to_npc` EventAck(ACCEPTED) 后记录 snapshot：

```text
event_id
conversation_id
world_id
npc entity_id
player entity_id
location
npc tile
player tile
max interaction distance
```

Interaction-bound Action 执行前 guard：

```text
- ActionRequest.source_event_id 为空或未知 -> REJECTED / interaction_context_missing
- world_id 不一致 -> REJECTED / interaction_context_changed
- conversation_id 不匹配 -> REJECTED / interaction_context_changed
- NPC 或 player 不在原 location -> REJECTED / interaction_context_changed
- 当前距离超过 max interaction distance -> REJECTED / interaction_context_changed
- guard 失败时关闭匹配 conversation；
- present_dialogue guard 成功后按 Phase5.6 语义显示 UI；
- move_to guard 成功后进入 async action start。
```

Phase6 guard-required capabilities:

```text
present_dialogue
move_to
```

`speak` / `emote` / `face_player` 保持 Phase5.6 的同步可见动作语义，不在 Phase6 强制接入 Interaction Context Guard。

`present_dialogue` 的 capability 注册必须同时包含：

```text
description
    说明它用于交互式对话，玩家回复会通过后续 GameEvent 到达。

extensions.gameagent.tool_policy.exclusive_per_step = true
extensions.gameagent.tool_policy.settle_after_success = true
```

这些 policy 不改变 Stardew UI 语义，只让 Runtime 用通用 metadata 执行 Phase5.6 已经形成的对话边界。

TurnCompletion 处理：

```text
COMPLETED / FAILED
  -> release interaction context matched by event_id
```

等待 LLM 期间：

```text
- 不冻结玩家；
- 不冻结 NPC schedule；
- 不阻塞游戏时间；
- Adapter 在 effect time 用 guard 决定是否展示 UI 或执行 action。
```

## 6.2 Stardew move_to Capability

修改范围：

```text
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Runtime/ActionCancellationRegistry.cs
adapters/stardew/src/Capabilities/MoveToCapability.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
adapters/stardew/tests/ActionCancellationRegistry.Tests/Program.cs
adapters/stardew/tests/check-context-static.ps1
```

Capability：

```text
name = move_to
version = 0.1.0
execution_mode = EXECUTION_MODE_ASYNC
concurrency_mode = CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL
description = Moves the NPC toward a reachable tile in the current location.
```

Input schema：

```json
{
  "type": "object",
  "properties": {
    "location": { "type": "string" },
    "tile": {
      "type": "object",
      "properties": {
        "x": { "type": "number" },
        "y": { "type": "number" }
      },
      "required": ["x", "y"],
      "additionalProperties": false
    }
  },
  "required": ["location", "tile"],
  "additionalProperties": false
}
```

Phase6 vertical slice 约束：

```text
- location 必须等于 NPC 当前 location；
- tile 必须在当前 location map bounds 内；
- tile 可达性由 Stardew Adapter 判断；
- Runtime 不生成路径，不读取地图，不判断坐标语义；
- 跨 location movement 留到后续阶段。
```

## 6.3 Async Adapter Lifecycle

Adapter 行为：

```text
收到 move_to ActionRequest
  -> source_event_id missing or unknown: ActionResult(REJECTED, interaction_context_missing)
  -> stale interaction context: ActionResult(REJECTED, interaction_context_changed)
  -> world_id mismatch: ActionResult(REJECTED, world_mismatch)
  -> cancel marker already exists: ActionResult(CANCELLED)
  -> input invalid: ActionResult(REJECTED, invalid_move_target)
  -> accepted: send ActionStatusUpdate(ACCEPTED)
  -> main thread starts movement: send ActionStatusUpdate(RUNNING)
  -> reached target: send ActionResult(SUCCEEDED, output current location/tile)
  -> path failed: send ActionResult(FAILED, move_failed)
  -> cancel received while running: stop movement, send ActionResult(CANCELLED)
```

Active async action state 属于 Stardew Adapter：

```text
action_id
entity_id
world_id
target location
target tile
started_at game time / tick
terminal sent flag
```

Runtime 不读取这些内部字段。

Timeout 后的迟到结果：

```text
Runtime 已因 timeout fail Turn 并发送 TurnCompletion(FAILED) 后，迟到 ActionResult 不恢复该 Turn。
Adapter / Game 可能已经完成真实移动，导致 Runtime 本 Turn 认知与游戏状态短暂分歧。
Phase6 不做事务回滚；后续 GameEvent 触发的新 Turn 会通过 Observe 收敛到真实位置。
```

---

# 7. Milestones And Acceptance

## M0：Protocol + ADR

目标：

```text
冻结 Phase6 协议策略，补齐 ActionRequest source correlation，并把 TurnCompletion 作为 Runtime -> Adapter 的正式终态信号。
```

修改范围：

```text
docs/phase6/GameAgent MVP0 Phase6 Async Action Protocol Strategy ADR.md
docs/phase6/GameAgent MVP0 Phase6 技术开发与验收方案.md
protocol/proto/gameagent.proto
protocol/tests/check-protocol-static.ps1
protocol/gen/go/...
```

验收命令：

```powershell
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
go test ./protocol/gen/go/...
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

```text
- ADR 明确 TurnCompletion 是 terminal Turn signal；
- protocol/proto/gameagent.proto additive 增加 ActionRequest.source_event_id / source_turn_id；
- protocol/proto/gameagent.proto additive 增加 TurnCompletion；
- RuntimeMessage.oneof 增加 turn_completion = 17；
- protocol static check 断言 ActionRequest.source_event_id = 7、source_turn_id = 8、RuntimeMessage.turn_completion = 17；
- Go / C# generated code 与 proto 一致；
- ActionStatusUpdate / ActionResult / CancelActionRequest 字段保持不变；
- C# generated code 由 adapters/stardew 的 Grpc.Tools build 输出验证，不新增 tracked Generated 目录；
- 非目标包含 CapabilityPolicy proto 字段、ActionBatchRequest、persistent continuation、多个并发长 Action、Runtime pathfinding。
```

## M1：Runtime Tool Policy Generalization

目标：

```text
Runtime 使用 capability metadata 执行通用 tool policy，不再按 Stardew capability name 写特殊规则。
```

修改范围：

```text
runtime/internal/tool/registry.go
runtime/internal/tool/registry_test.go
runtime/internal/agent/loop.go
runtime/internal/agent/loop_test.go
runtime/internal/agent/scheduler.go
runtime/internal/agent/scheduler_test.go
runtime/internal/agent/config.go
runtime/internal/agent/config_test.go
runtime/config/agent.json
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
adapters/stardew/tests/check-context-static.ps1
```

验收命令：

```powershell
go test ./runtime/internal/tool ./runtime/internal/agent
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
```

通过标准：

```text
- Tool Registry 从 Capability.extensions.gameagent.tool_policy 解析 ToolPolicy；
- 缺失 tool_policy 时使用零值；
- 非 boolean policy 字段使对应 capability 注册失败；
- Entry.Policy 能表达 exclusive_per_step / settle_after_success；
- Stardew present_dialogue 注册上述两个 policy 为 true；
- Runtime 默认 ToolInstruction 不写死 Stardew capability name；
- Runtime 执行路径不再存在 terminalDialogueToolName / present_dialogue 专用分支；
- exclusive_per_step 校验位于通用 preflight，先于 sync / async execution 分叉；
- exclusive_per_step ToolCall 与其它 ToolCall 同 step 出现时，preflight 失败且不发送 ActionRequest；
- exclusive_per_step 违规的 ToolResult code 不包含具体 capability name；
- sync settle_after_success ToolCall terminal SUCCEEDED 且当前 step 无 model-visible failure 后，Runtime 不再发起下一次 model request，直接进入 completed / settle path；
- async ToolCall 即使 carrying settle_after_success policy，也必须先 re-observe 当前 entity 并进入下一 AgentStep；
- 不新增 CapabilityPolicy proto 字段。
```

建议测试：

```text
TestRegistryParsesToolPolicyExtensions
TestRegistryDefaultsMissingToolPolicyToZeroValue
TestRegistryRejectsInvalidToolPolicyMetadata
TestSchedulerRejectsExclusiveToolMixedBatchBeforeExecution
TestHandleEventSettlesAfterSuccessfulPolicyTool
TestHandleEventDoesNotRequestNextStepAfterSuccessfulPolicyTool
TestDefaultToolInstructionDoesNotNameStardewTools
TestStardewPresentDialogueDeclaresToolPolicy
```

## M2：Runtime TurnCompletion Plumbing

目标：

```text
Runtime 能在 accepted GameEvent 的 AgentTurn 进入终态时，向 Adapter 发送 TurnCompletion。
```

修改范围：

```text
runtime/internal/agent/loop.go
runtime/internal/gateway/gateway.go
runtime/internal/gateway/stream_environment.go
runtime/internal/gateway/stream_environment_test.go
runtime/internal/gateway/gateway_integration_test.go
runtime/internal/trace/trace.go
```

验收命令：

```powershell
go test ./runtime/internal/gateway ./runtime/internal/agent ./runtime/internal/trace
```

通过标准：

```text
- completed Turn 发送 TURN_COMPLETION_STATUS_COMPLETED；
- failed Turn 发送 TURN_COMPLETION_STATUS_FAILED 并携带 error；
- TURN_COMPLETION_STATUS_CANCELLED 保留给未来显式 Turn cancellation；
- settle-only Turn 也发送 TurnCompletion；
- TurnCompletion 与原 GameEvent event_id 绑定；
- duplicate / rejected GameEvent 不发送 TurnCompletion；
- TurnCompletion 发送发生在唯一 terminal trace 之前；
- TurnCompletion 发送失败会进入非终态 trace，不生成第二个 Turn terminal event；
- TurnCompletion 发送失败不改变已经判定的 Turn terminal status；
- TurnCompletion 发送由单一 terminal helper 或等价集中路径统一负责，不分散在各退出点手写；
- turn_completion_sent 或 turn_completion_send_failed trace 最多出现一次；
- turn_completed / turn_failed 仍是最后一条 Turn trace。
```

建议测试：

```text
TestHandleEventSendsTurnCompletionOnSettle
TestHandleEventSendsTurnCompletionOnFailure
TestHandleEventDoesNotSendTurnCompletionForRejectedEvent
TestConnectSendsTurnCompletionBeforeTerminalTrace
TestTurnCompletionSendFailureDoesNotCreateSecondTerminalTrace
TestTurnCompletionSendFailureDoesNotChangeCompletedTurnStatus
```

## M3：Adapter Interaction Context Guard

目标：

```text
Stardew Adapter 能记录交互上下文，并在 TurnCompletion 后释放。
```

修改范围：

```text
adapters/stardew/src/Dialogue/InteractionContextStore.cs
adapters/stardew/src/Dialogue/ConversationStateStore.cs
adapters/stardew/src/Capabilities/PresentDialogueCapability.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
adapters/stardew/tests/check-context-static.ps1
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

```text
- EventAck(ACCEPTED) 后记录以 event_id 为主键的 interaction context snapshot；
- present_dialogue 与 move_to 使用 ActionRequest.source_event_id 查找 snapshot；
- ActionRequest.source_event_id 为空或未知时返回 REJECTED / interaction_context_missing；
- TurnCompletion 后按 event_id 释放匹配 snapshot；
- world / conversation / location / distance 变化会让 present_dialogue 返回 REJECTED / interaction_context_changed；
- world / conversation / location / distance 变化会让 move_to 在 start 前返回 REJECTED / interaction_context_changed；
- guard 失败时关闭匹配 conversation；
- present_dialogue guard 成功时沿用 Phase5.6 的 UI 展示语义；
- 等待 LLM 期间玩家和 NPC 不被 Runtime 冻结；
- Adapter 不新增 runtime/internal 依赖。
```

建议测试：

```text
TestInteractionContextCommittedAfterAcceptedAck
TestInteractionContextReleasedByTurnCompletion
TestInteractionContextRejectsMissingSourceEventID
TestPresentDialogueRejectsWhenConversationContextChanged
TestPresentDialogueRejectsWhenNpcMovedAwayBeforeDisplay
TestPresentDialogueRejectsWhenPlayerMovedAwayBeforeDisplay
TestPresentDialogueGuardFailureClosesMatchingConversation
```

## M4：Runtime Action Lifecycle Plumbing

目标：

```text
Gateway 能分发 ActionStatusUpdate，streamEnvironment 能关联同一 action_id 的 status update 和 terminal result。
```

修改范围：

```text
runtime/internal/agent/loop.go
runtime/internal/gateway/gateway.go
runtime/internal/gateway/stream_environment.go
runtime/internal/gateway/stream_environment_test.go
runtime/internal/gateway/gateway_integration_test.go
```

验收命令：

```powershell
go test ./runtime/internal/gateway
go test ./runtime/internal/agent
```

通过标准：

```text
- AdapterMessage_ActionStatus 会进入 resolveActionStatusUpdate；
- StartAction 在收到 ACCEPTED / RUNNING 后返回 ActionStart；
- terminal ActionResult 比 status update 更早到达时，StartAction 返回 fast terminal Result；
- WaitActionResult 只由 terminal ActionResult 完成；
- StartAction / WaitActionResult 超时会发送 CancelActionRequest；
- unknown action_id 的 ActionStatusUpdate / ActionResult 被忽略；
- disconnect 会唤醒 pending async action waiter；
- 现有 sync SubmitAction 测试继续通过。
```

建议测试：

```text
TestStreamEnvironmentStartActionReceivesAcceptedStatus
TestStreamEnvironmentStartActionReturnsFastTerminalResult
TestStreamEnvironmentWaitActionResultReceivesTerminalResult
TestStreamEnvironmentAsyncTimeoutSendsCancelAction
TestStreamEnvironmentLateAsyncResultAfterTimeoutIsIgnored
TestConnectRoutesActionStatusUpdateToPendingAction
```

## M5：Tool Registry Execution Mode Metadata

目标：

```text
Runtime Tool Registry 能保存并暴露 ASYNC capability 的 execution mode。
```

修改范围：

```text
runtime/internal/tool/registry.go
runtime/internal/tool/registry_test.go
```

验收命令：

```powershell
go test ./runtime/internal/tool
```

通过标准：

```text
- Entry.Execution 能区分 sync / async；
- ExecutionMode_UNSPECIFIED 映射为 sync；
- ExecutionMode_SYNC 映射为 sync；
- ExecutionMode_ASYNC 映射为 async；
- ASYNC capability 进入 Available()；
- Available() 仍只返回 provider-facing ToolDefinition；
- Available() 仍按 Name 升序稳定输出。
```

建议测试：

```text
TestRegistryMapsSyncExecutionMode
TestRegistryMapsUnspecifiedExecutionModeToSync
TestRegistryIncludesAsyncCapabilitiesInPhase6ToolView
TestRegistryLookupReturnsExecutionModeMetadata
```

## M6：Scheduler Async Single Action Path

目标：

```text
Tool Scheduler 支持单 async ToolCall 的 start / wait / terminal ToolResult。
```

修改范围：

```text
runtime/internal/agent/scheduler.go
runtime/internal/agent/scheduler_test.go
runtime/internal/agent/config.go
runtime/internal/agent/config_test.go
```

验收命令：

```powershell
go test ./runtime/internal/agent
```

通过标准：

```text
- 单 async ToolCall 会调用 StartAction；
- async ActionRequest 带 source_event_id 与 source_turn_id；
- 收到 ACCEPTED / RUNNING 后等待 terminal ActionResult；
- terminal SUCCEEDED 生成 succeeded ToolResult；
- terminal REJECTED / FAILED / CANCELLED / INTERRUPTED 生成 model-visible ToolResult；
- async ToolCall 与其它 ToolCall 同 step 出现时，preflight 失败且不发送 ActionRequest；
- action start timeout 发送 CancelActionRequest 并 fail turn；
- async action timeout 发送 CancelActionRequest 并 fail turn；
- TurnTimeout 触发时若存在 pending async action，先发送 CancelActionRequest 再 fail turn；
- late terminal result 不恢复已失败 Turn；
- sync scheduler 行为保持 Phase5 语义。
```

建议测试：

```text
TestSchedulerStartsAndWaitsForSingleAsyncAction
TestSchedulerAddsActionSourceCorrelation
TestSchedulerRejectsAsyncMixedWithSyncBatchBeforeExecution
TestSchedulerRejectsMultipleAsyncCallsBeforeExecution
TestSchedulerAsyncTerminalFailureIsModelVisible
TestSchedulerAsyncStartTimeoutCancelsAction
TestSchedulerAsyncWaitTimeoutCancelsAction
TestSchedulerTurnTimeoutCancelsPendingAsyncAction
TestConfigLoadsPhase6AsyncBudgets
TestConfigDefaultsPhase6AsyncBudgetsWhenMissingZeroOrNegative
TestConfigPhase6DefaultTurnTimeoutCoversAsyncBudget
```

## M7：AgentLoop Suspend / Resume

目标：

```text
AgentLoop 可以在 async action terminal result 后恢复同一个 Turn，并进入下一 AgentStep。
```

修改范围：

```text
runtime/internal/agent/loop.go
runtime/internal/agent/loop_test.go
runtime/internal/context/builder_test.go
runtime/internal/memory/projector_test.go
runtime/internal/trace/trace.go
runtime/internal/trace/turn_test.go
```

验收命令：

```powershell
go test ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/memory ./runtime/internal/trace
```

通过标准：

```text
- async ToolCall 后 emit turn_suspended；
- 所有 ActionRequest 都带原 GameEvent.event_id 与当前 turn_id；
- 一个 Turn 内第二个 async ToolCall 产生 async_action_limit_exceeded；
- terminal ActionResult 后 emit turn_resumed；
- resume 后重新 Observe target entity；
- async terminal SUCCEEDED 后 re-observe 失败会 fail Turn，不使用 stale observation 继续下一 step；
- 下一次 model request 包含 terminal ToolResult transcript；
- terminal SUCCEEDED 的 async action 在 completed Turn 后作为 visible outcome 写入 Memory；
- async terminal SUCCEEDED 后若因 re-observe technical failure 导致 Turn failed，该 successful async outcome 可按 prior successful visible outcome 写入 Memory；
- terminal failed / rejected / cancelled / interrupted 进入 transcript，模型可在剩余 step 内修正；
- settle 仍只能在当前 step 无 model-visible failure 时完成 Turn；
- TurnCompletion 在 terminal outcome 确定后发送，唯一 Turn terminal trace 仍保持最后。
```

建议测试：

```text
TestHandleEventSuspendsAndResumesAfterAsyncAction
TestHandleEventAddsActionSourceCorrelation
TestHandleEventReobservesAfterAsyncTerminalResultBeforeNextStep
TestHandleEventFailsTurnWhenReobserveFailsAfterAsyncSuccess
TestHandleEventPassesAsyncToolResultTranscriptToNextStep
TestHandleEventWritesAsyncSuccessfulOutcomeToMemoryOnCompletion
TestHandleEventRetriesAfterAsyncTerminalFailureWithinStepBudget
TestAsyncTurnTerminalEventIsUniqueAndLast
```

## M8：Stardew Adapter move_to Vertical Slice

目标：

```text
Stardew Adapter 提供一个真实异步 Environment Tool，用于验证长 Action lifecycle。
```

修改范围：

```text
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Runtime/ActionCancellationRegistry.cs
adapters/stardew/src/Capabilities/MoveToCapability.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
adapters/stardew/tests/ActionCancellationRegistry.Tests/Program.cs
adapters/stardew/tests/check-context-static.ps1
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet run --project adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

```text
- CapabilityCatalog 注册 move_to；
- move_to 使用 EXECUTION_MODE_ASYNC；
- move_to 使用 CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL；
- move_to schema 要求 location 和 tile；
- move_to 使用 ActionRequest.source_event_id 执行 interaction context guard；
- source_event_id 为空或未知返回 ActionResult(REJECTED, interaction_context_missing)；
- stale interaction context 返回 ActionResult(REJECTED, interaction_context_changed)；
- world mismatch 返回 ActionResult(REJECTED, world_mismatch)；
- invalid target 返回 ActionResult(REJECTED, invalid_move_target)；
- accepted 后发送 ActionStatusUpdate(ACCEPTED)；
- movement running 后发送 ActionStatusUpdate(RUNNING)；
- 到达目标后发送 ActionResult(SUCCEEDED)；
- cancel before start 返回 ActionResult(CANCELLED)；
- cancel while running 停止移动并返回 ActionResult(CANCELLED)；
- Adapter 不引入 runtime/internal 依赖。
```

## M9：Gateway Integration And Regression

目标：

```text
bufconn / fake adapter 覆盖完整 async turn lifecycle。
```

修改范围：

```text
runtime/internal/gateway/gateway_integration_test.go
runtime/internal/gateway/stream_environment_test.go
runtime/internal/agent/loop_test.go
runtime/internal/trace/turn_test.go
```

验收命令：

```powershell
go test ./runtime/internal/gateway ./runtime/internal/agent ./runtime/internal/trace
go test ./runtime/...
```

通过标准：

```text
- 一个 EventAck 后出现 ActionRequest(move_to)；
- ActionRequest(move_to) 带 source_event_id 与 source_turn_id；
- fake adapter 发送 ACCEPTED / RUNNING status update；
- Runtime trace 记录 action_status_update_received；
- Runtime trace 记录 turn_suspended / turn_resumed；
- fake adapter 延迟 terminal ActionResult 后，Runtime 发起下一 step model request；
- 下一 step 能 settle，Turn completed；
- Runtime 发送 TurnCompletion；
- Adapter 收到 TurnCompletion 后释放 interaction context；
- async timeout 会发送 CancelActionRequest；
- late ActionResult 不生成第二个 terminal trace；
- sync speak / emote / present_dialogue 多步链路保持通过；
- non-Stardew trigger fixture 保持通过。
```

建议测试：

```text
TestConnectRunsAsyncActionLifecycleAndResumesTurn
TestConnectKeepsRecvLoopAvailableWhileAsyncActionIsWaiting
TestConnectAsyncActionTimeoutSendsCancelAndKeepsSingleTerminalTrace
TestConnectIgnoresLateAsyncResultAfterTimeout
TestConnectSendsTurnCompletionForSettleOnlyDialogueTurn
```

## M10：Full Acceptance

目标：

```text
确认 Phase6 的 Protocol、Runtime、Adapter、Trace、Memory 和 Stardew vertical slice 全部满足阶段验收标准。
```

修改范围：

```text
protocol/proto/gameagent.proto
protocol/gen/go/...
runtime/...
adapters/stardew/...
docs/phase6/...
docs/summary/...
```

验收命令：

```powershell
go test ./runtime/... ./protocol/gen/go/...
go test ./...
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet run --project adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet run --project adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
dotnet build adapters/stardew/GameAgent.Stardew.csproj
git diff --check
```

通过标准：

```text
- 全部 PASS；
- Protocol additive ActionRequest source correlation 与 TurnCompletion 已生成到 Go / C#；
- Runtime 不引用 Stardew / SMAPI / Adapter 项目；
- Adapter 不依赖 runtime/internal；
- Runtime tool policy 只包含 exclusive_per_step / settle_after_success，且不依赖具体 capability name；
- Tool Registry 能暴露 sync + async capabilities；
- AgentTurn 能 suspend / resume 并保持唯一终态；
- async terminal result 后会 re-observe；
- re-observe failure、TurnTimeout cancel pending async action、late ActionResult after timeout 均有确定性边界；
- successful async action 可以作为 visible outcome 进入 Memory；
- TurnCompletion 能释放 Adapter interaction context；
- Interaction Context Guard 能拒绝过期 dialogue display 和过期 move_to start；
- move_to 的寻路与可达性判断完全位于 Stardew Adapter；
- 真实 Stardew trace 可以看到 move_to 的 status update、suspend、resume、TurnCompletion 和 completed / failed terminal。
```

---

# 8. 开发顺序

```text
1. M0：先接受 ADR，完成 ActionRequest source correlation、TurnCompletion proto additive 与生成代码。
2. M1：先完成 Runtime Tool Policy Generalization，移除 Runtime Core 的 Stardew tool-name 执行特例。
3. M2：接 Runtime TurnCompletion plumbing，并让 ActionRequest 传递 source correlation。
4. M3：接 Stardew Adapter Interaction Context Guard。
5. M4：做 Gateway / Environment Port async action lifecycle plumbing。
6. M5：让 Tool Registry 暴露 async capability。
7. M6：接 Scheduler 的单 async ToolCall 路径。
8. M7：接 AgentLoop suspend / resume / re-observe。
9. M8：实现 Stardew move_to vertical slice。
10. M9-M10：做 integration hardening 和全量验收。
```

不要把 `move_to` Adapter 实现和 Runtime lifecycle plumbing 混在同一个提交里。

建议提交：

```text
docs: add phase6 async action plan
feat: add turn completion and action source protocol signals
feat: generalize runtime tool policy metadata
feat: release stardew interaction contexts on turn completion
feat: add runtime async action lifecycle plumbing
feat: expose async tool execution metadata
feat: resume agent turns after async actions
feat: add stardew move_to async action
test: harden phase6 async action integration
```

---

# 9. 阶段验收状态

Phase6 可以验收为 `Accepted` 的最低条件：

```text
1. Async Action Protocol Strategy ADR 被接受。
2. Runtime 执行路径不再硬编码 Stardew capability name，通用 tool policy 由 Capability.extensions 驱动。
3. Protocol additive 增加 ActionRequest source correlation 与 TurnCompletion，并完成 Go / C# 生成。
4. Runtime 对每个 accepted GameEvent Turn 发送唯一 TurnCompletion。
5. Runtime 构造的 ActionRequest 能携带原 GameEvent.event_id 与当前 turn_id。
6. Adapter 能用 TurnCompletion 释放 interaction context。
7. Interaction Context Guard 能在 effect time 拒绝过期 UI 展示和过期 move_to start。
8. AgentTurn 可以等待 async action terminal result，并恢复同一 Turn。
9. resume 后会重新 Observe 当前目标实体。
10. ToolResult transcript 只使用 terminal ActionResult。
11. ActionStatusUpdate 进入 trace，不进入 Memory / model transcript。
12. re-observe failure 不使用 stale observation 继续 Turn。
13. TurnTimeout 触发时会 cancel pending async action。
14. timeout / cancel / late result 有确定性测试。
15. Stardew move_to 作为真实长 Action vertical slice 跑通。
```

---

# 10. 后续进入 Phase7 的边界

Phase7 聚焦 Environment Recovery 与持久 Agent State。

Phase6 的 in-memory continuation 不处理：

```text
Runtime crash recovery
Adapter reconnect 后恢复 pending async action
durable pending action store
event replay
long-term memory persistence
```

阶段结束 Review 重点确认：

```text
- pending async action 是否需要持久化；
- reconnect 后 ActionResult 如何关联原 Turn；
- Action lifecycle 是否需要独立子系统；
- 当前 single async action per Turn 是否足够进入 Phase7；
- TurnCompletion 是否足以支撑 Adapter interaction lifecycle；
- Memory visible action summary 是否需要由 capability metadata 驱动，替代 Phase5.6 中对 `speak` / `emote` / `present_dialogue` / `face_player` 的渲染层摘要分支；
- move_to 是否暴露出需要升级 protocol 的真实缺口。
```
