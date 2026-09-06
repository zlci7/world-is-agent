# GameAgent MVP0 Phase5 技术开发与验收方案

> **Status:** Accepted with Known Limitations / Archived Implementation Baseline
> **Date:** 2026-08-24
> **Acceptance Date:** 2026-08-27
> **Scope:** Bounded Multi-step AgentTurn with Tool Batch
> **Architecture Baseline:** GameAgent Runtime Architecture v0.3
> **Roadmap Baseline:** GameAgent 阶段规划 v0.4
> **Entry Gate:** GameAgent 多游戏兼容性与 Agent Binding 决策 Accepted
> **Protocol Target:** gameagent.protocol.v1alpha2 additive Phase5 revision
> **Final Commit:** 372d2b6 fix: address phase5 review findings
> **Active Follow-up:** Phase5.5 Stardew Adapter Context Enrichment

---

# 0. 验收快照

Phase5 已于 2026-08-27 验收为 `Accepted with Known Limitations`。本文保留为 Phase5 的开发与验收基线；当前开发已进入 `docs/phase5.5/GameAgent MVP0 Phase5.5 技术开发与验收方案.md`。

最终落地口径：

```text
- AgentTurn 从“一次模型决策 + 一次动作”升级为有界 multi-step。
- 默认预算：MaxSteps=3 / MaxToolCallsPerStep=4 / MaxToolCallsPerTurn=6 / MaxParallelToolCalls=4。
- Model contract 统一为 ModelDecision{ToolCalls, Control}。
- 真实 provider 使用 no-tool final response 表达 settle；__gameagent_settle 只保留 parser 兼容路径。
- Tool Scheduler 支持 Sequential / ParallelSafe，并保持 ToolResults 按原始 ToolCall 顺序回灌。
- Tool preflight 覆盖 tool lookup、arguments 非空、ToolCall.ID 去重和 InputSchema 常用子集校验。
- Memory 使用 Turn-level Outcomes；技术错误前已确认成功的同组 action 仍会写入 Memory。
```

落地后代码评审修复：

```text
- 补齐 parallel group model-visible failure 覆盖。
- 保留 parallel / sequential technical error 前已成功 action，避免 Runtime Memory 与游戏状态静默偏离。
- 拦截同 Turn 跨 step 复用 ToolCall.ID。
- Provider 非 2xx 错误不再暴露原始 HTTP body，Gateway stdout 日志做单行截断。
- 删除 loop.go 孤儿 updateMemory、registry 测试专用 ValidateToolCall API、memory.Record.Outcome 单数字段。
```

验收验证：

```text
- go test ./... PASS
- protocol/tests/check-protocol-static.ps1 PASS
- dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj --no-restore --verbosity minimal PASS
- git diff --check PASS
- go test -race ./runtime/... 受当前 Windows C toolchain 限制未作为本机硬门，需在 CI/Linux 或可用 64-bit gcc 环境补跑。
```

已知限制：

```text
- definition_id 当前只来自 EntityRef.definition_id；static binding source、fallback 和 mismatch gate 延后实现。
- Tool Registry 当前仍按单 adapter / 单 active stream 假设运行。
- Runtime InputSchema preflight 是浅层子集校验，不替代 Adapter 业务校验。
- Memory renderer 仍保留 Phase4 speak / emote 兼容摘要语义，后续进入 compat roadmap。
```

---

# 1. 阶段目标

Phase4 已经证明：

```text
1 Trigger
  -> 1 AgentTurn
  -> Observe
  -> Build Context
  -> 1 Model Generate
  -> 1 Tool / Action
  -> Memory update
  -> 1 terminal trace event
```

Phase5 将内部执行扩展为：

```text
1 AgentTurn = N bounded AgentSteps

AgentStep
  = 1 Model Decision
  + 0..N ToolCalls
  + 0..N ToolResults
  + optional AgentTurn Control
```

Phase5 的核心问题是：

> **同一个 Turn 内，模型能否一次提出多个工具调用，Runtime 能否按安全调度策略执行这些调用，再把规范化 ToolResult 放回下一次模型请求，并在有限 step budget 内正常 settle？**

`max_steps` 表示模型连续决策次数上限，不表示工具调用总数上限。工具调用 fan-out 由独立预算控制。

---

# 2. 范围边界

## 2.1 P0 范围

```text
1. Model Response 支持一个 AgentStep 内返回多个 ToolCall。
2. Model Decision 支持 optional settle control。
3. Runtime 执行 ToolCall batch，并按原始 ToolCall 顺序回灌 ToolResult。
4. Tool Scheduler 支持 Sequential / ParallelSafe 两类 batch 内执行策略。
5. Environment Tool 默认 Sequential。
6. Capability 可通过 Protocol 声明 concurrency_mode。
7. EntityRef 可通过 Protocol 携带 definition_id。
8. MemoryRecord 支持同一 completed Turn 内多个 successful outcomes。
9. Gateway 集成测试证明一个 EventAck 后可以出现多个 ActionRequest。
10. non-Stardew fixture 必须覆盖 entity_id != definition_id。
11. Batch Preflight 必须在任何 ActionRequest 发出前完成。
12. Parallel group 技术错误必须 drain 后再发 Turn terminal event。
13. Provider wrapper 必须定义 multi-tool 与 settle control 的归一化规则。
```

## 2.2 非目标

```text
ActionBatchRequest
ActionBatchResult
Async Action / ActionStatusUpdate resume
每 step 重新 Observe
Planner / Supervisor / Sub-agent
Workflow engine
move_to / pathfinding
Runtime Tool memory_search / memory_write
Long-term semantic memory
Vector retrieval
Persistent continuation
真实第二游戏 Adapter
把 definition_id 纳入 AgentSessionKey / Memory key
global Environment lock
per-entity lock scope
resource group / lock key
Capability compatibility matrix
continue_on_failure policy
真实 LLM 稳定生成 multi-tool 的硬验收
```

## 2.3 影响面

| 领域 | 影响 | 边界 |
| --- | --- | --- |
| Protocol | 增加 `EntityRef.definition_id` 与 `Capability.concurrency_mode` 字段 | additive，不引入 batch action message |
| Adapter | Stardew mapper 填充 `definition_id=entity_id`，capability 默认 Sequential | Adapter 不接管 prompt / memory |
| Runtime model | `Response` 从单 ToolCall 扩展为 `ModelDecision` | Provider-specific encoding 在 wrapper 内归一化 |
| Tool Registry | 保存 Environment Tool metadata 与并发策略 | AgentTurn Control 不进入 Tool Registry |
| AgentLoop | 执行 bounded step loop 与 tool batch scheduler | 不实现 async resume |
| Context | 回合内 transcript 包含 tool calls/results | 不用 LLM 二次摘要 ToolResult |
| Memory | completed Turn 写入多个 successful outcomes；action 技术错误前已确认成功的 outcomes 可写入 | unknown / failed / skipped outcomes 不写入 Memory |
| Trace | 增加 step / batch / scheduler 可观测字段 | terminal event 仍唯一且最后 |
| Gateway | 单 EventAck 后可发送多个 ActionRequest | 不修改 event admission 的 game-neutral 规则 |

---

# 3. Protocol 变更

Phase5 修改 `protocol/proto/gameagent.proto`，并重新生成 Go / C# 代码。

## 3.1 definition_id

背景语义：

```text
entity_id
    当前 world 内稳定的具体游戏实体身份。
    用于 AgentSessionKey / Agent State / Agent Memory / routing / trace。

definition_id
    可复用 Agent Definition / Archetype 身份。
    scope = game_id + definition_id。
    多个动态实体可以共享同一个 definition_id。
```

示例：

```text
Stardew:
    entity_id     = npc:Linus
    definition_id = npc:Linus

Dynamic game:
    entity_id     = villager:uuid-123
    definition_id = villager/farmer
```

Protocol 字段：

```proto
message EntityRef {
  string entity_id = 1;
  string entity_type = 2;
  string display_name = 3;
  string definition_id = 4;
}

message Observation {
  string entity_id = 1;
  uint64 revision = 2;
  GameTime game_time = 3;
  google.protobuf.Struct state = 4;
  repeated EntityRef nearby_entities = 5;
  google.protobuf.Struct extensions = 6;
  string world_id = 7;
}
```

`GameEvent.target_entity_id` 仍然是 turn routing 的唯一目标字段。不新增 `target_definition_id`。Runtime 通过 `GameEvent.entities` 中与 `target_entity_id` 匹配的 `EntityRef.definition_id` 解析目标实体的 Agent Definition 绑定。

Runtime 对空 `definition_id` 的处理：

```text
1. Stardew adapter 必须显式填充 definition_id=entity_id。
2. fake non-Stardew fixture 必须显式填充 definition_id!=entity_id。
3. Runtime 从 target EntityRef.definition_id 读取当前 Turn 的 definition binding。
4. Runtime 不得把缺失 definition_id 自动解释为 entity_id。
5. static binding fallback 与 EntityRef/static binding mismatch gate 延后实现。
```

`definition_id` resolution 当前为 EntityRef-only。Phase5 不新增独立 Binding store，也不定义新的长期优先级体系。

`Observation` 不携带目标实体的 `definition_id`。Observation 表达当前实体状态与局部环境事实；Agent Definition 绑定由 `EntityRef.definition_id` 和 Runtime Agent Binding policy 提供。

Phase5 引入最小 Runtime descriptor：

```go
type AgentDescriptor struct {
	EntityID     string
	DefinitionID string
}
```

`definition_id` 只是模板引用 key，不是模板内容。

## 3.2 capability concurrency_mode

Protocol 字段：

```proto
enum CapabilityConcurrencyMode {
  CAPABILITY_CONCURRENCY_MODE_UNSPECIFIED = 0;
  CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL = 1;
  CAPABILITY_CONCURRENCY_MODE_PARALLEL_SAFE = 2;
}

message Capability {
  string name = 1;
  string version = 2;
  string description = 3;
  string input_schema_json = 4;
  ExecutionMode execution_mode = 5;
  google.protobuf.Struct extensions = 6;
  CapabilityConcurrencyMode concurrency_mode = 7;
}
```

Runtime 解释：

```text
UNSPECIFIED -> SEQUENTIAL
SEQUENTIAL -> 同一 ToolCall batch 内的 ordering barrier
PARALLEL_SAFE -> 可与同一连续 parallel-safe group 中的其他调用有界并发执行
```

`concurrency_mode` 只描述同一个 Runtime ToolCall batch 内的调度关系，不表达跨 AgentTurn、跨 AgentSession 或整个 Environment 的全局互斥。Environment Tool 的并发语义由 Adapter / Capability 声明提供。Runtime 不硬编码具体 capability 的业务语义。

`PARALLEL_SAFE` 是 Adapter 的强承诺：该 Capability 的调用可以与同一 batch 中任意其他 `PARALLEL_SAFE` Capability 调用并发，而不会因为并发本身破坏执行正确性。无法做出该承诺的 Capability 必须声明为 `SEQUENTIAL` 或保持 `UNSPECIFIED`。

## 3.3 Protocol 不变项

```text
ActionRequest 仍表示单个 Environment Action。
ActionResult 仍对应单个 action_id。
ObserveRequest 仍以 entity_id + world_id 请求观察。
CapabilityRequest 仍按可选 entity_id 请求能力列表。
AgentSessionKey 仍为 game_id + world_id + entity_id。
Memory key 仍为 game_id + world_id + entity_id。
```

Protocol wire compatibility 与 Runtime semantic compatibility 分开处理：

```text
新增字段保持 wire additive。
concurrency_mode 缺失可安全降级为 SEQUENTIAL。
definition_id 缺失按 Agent Binding policy 处理，不因字段新增而默认旧 Adapter 已支持 Definition Binding。
```

---

# 4. Runtime 设计

## 4.1 ModelDecision

Phase5 的 provider-neutral response：

```go
type Response struct {
    Decision ModelDecision
}

type ModelDecision struct {
    ToolCalls []ToolCall
    Control   ControlDirective
}

type ControlDirective struct {
    Kind   ControlKind
    Reason string
}

type ControlKind string

const (
    ControlUnspecified ControlKind = "unspecified"
    ControlContinue ControlKind = "continue"
    ControlSettle   ControlKind = "settle"
)
```

规则：

```text
1. 一个 AgentStep 对应一次 provider.Generate。
2. 一个 AgentStep 可以包含 0..N ToolCalls。
3. `settle` 是 AgentTurn Control，不是 Tool。
4. `settle` 可以单独出现，也可以和 ToolCalls 同时出现。
5. 无 ToolCalls 且无 settle 是 invalid_model_response。
6. Provider technical error / timeout 直接 turn_failed(stage=model)。
7. Provider wrapper 必须在进入 AgentLoop 前把缺省 control 归一化为 Continue / Settle。
8. AgentLoop 不接受 ControlUnspecified。
```

Provider wrapper 负责把 provider-native 输出归一化为 `ModelDecision`：

```text
provider native multiple tool calls
    -> ordered ToolCalls + ControlContinue

provider final / no-tool response
    -> ToolCalls=[] + ControlSettle

provider-facing internal settle sentinel
    -> ControlSettle
    -> sentinel stripped from ToolCalls

tool calls + internal settle sentinel
    -> ordered ToolCalls + ControlSettle
```

internal settle sentinel 不进入 Runtime Tool Registry，不占 ToolCall budget，不进入 Scheduler，不生成 ToolResult，不发送给 Adapter。

## 4.2 Bounded Step Loop

```text
turn ctx
  ↓
observe once
  ↓
load recent memory once
  ↓
create base AgentContext
  ↓
for step_index in 1..max_steps:
    build ModelRequest(base context + intra-turn transcript + tools + controls)
    provider.Generate
    normalize Response -> ModelDecision
    validate step budgets by proposed ToolCall count

    if ToolCalls empty and ControlSettle:
        update memory from successful environment outcomes
        turn_completed
        return

    prepare batch preflight before executing any ToolCall
    if batch preflight failed:
        append one ToolResult for each proposed ToolCall
        continue

    schedule and execute preflighted ToolCalls
    normalize ActionResults -> ToolResults
    append assistant tool calls + tool results to transcript in original call order

    if ControlSettle and all ToolResults succeeded:
        update memory from successful environment outcomes
        turn_completed
        return

    continue
  ↓
max_steps exhausted
  ↓
turn_failed(stage=step, reason=max_steps_exceeded)
```

Phase5 P0 只 Observe once。ActionResult / ToolResult 是下一步模型决策的事实输入。

## 4.3 settle control

`settle` 表达当前 Turn 可以正常结束。

语义：

```text
0 ToolCalls + settle
    直接 complete turn。

N ToolCalls + settle
    先执行 ToolCalls。
    所有 ToolResults succeeded 后 complete turn。
    任一 ToolResult rejected / failed / cancelled / interrupted / invalid 时继续下一 step。
```

`settle.reason` 只进入 trace/debug，不进入游戏 Action，不写入 Memory。

## 4.4 Tool Registry 与 ExecutionPolicy

建议扩展 `runtime/internal/tool`：

```go
type Kind string

const (
    KindEnvironment Kind = "environment"
)

type ConcurrencyMode string

const (
    ConcurrencySequential   ConcurrencyMode = "sequential"
    ConcurrencyParallelSafe ConcurrencyMode = "parallel_safe"
)

type Entry struct {
    Definition  model.ToolDefinition
    Kind        Kind
    Concurrency ConcurrencyMode
}
```

规则：

```text
1. Phase5 P0 的 Tool Registry 只保存 Environment Tool。
2. AgentTurn Control 不进入 Tool Registry。
3. Adapter capability `UNSPECIFIED` concurrency 映射为 `SEQUENTIAL`。
4. `Available()` 按 tool name 升序稳定输出。
5. `Lookup(name)` 返回 Tool Entry 与执行策略。
6. `execution_mode=ASYNC` 的 Capability 不进入 Phase5 当前 AgentTurn Tool View。
```

Runtime Tool 仍是未来扩展位，本阶段不实现。

## 4.5 Tool Batch Preflight 与 Scheduler

Scheduler 输入为同一 AgentStep 内的 ordered ToolCalls。

Batch Preflight 必须在任何 ActionRequest 发出前完成：

```text
1. ToolCall budget check。
2. ToolCall.ID duplicate check。
3. Tool lookup。
4. ToolCall envelope / arguments validation。
5. concurrency metadata resolution。
6. Convert ToolCall.Arguments to protocol Struct for every Environment ToolCall。
7. BuildActionRequest for every Environment ToolCall。
8. build ExecutionPlan。
```

Preflight 规则：

```text
1. Preflight 全部成功后才允许 Scheduler 执行任何 ToolCall。
2. 任一 ToolCall preflight 失败时，不发送任何 ActionRequest。
3. preflight 失败的 call 生成 `status=invalid`。
4. preflight 成功但因 batch preflight 原子失败未执行的 call 生成 `status=skipped, code=batch_validation_failed`。
5. 同一 AgentStep 内 ToolCall.ID duplicate 在 Phase7.4 后由 Loop 于模型响应边界拒绝为 `invalid_model_response`；Scheduler preflight 保留为执行层内部保护。
6. 同一 Turn 内后续 AgentStep 复用已出现过的 ToolCall.ID 时，由 Loop 生成 model-visible preflight failure，不进入 Scheduler。
7. Runtime 仅校验 InputSchema 的 top-level object / properties.type / required / enum / additionalProperties=false 子集；Adapter 仍负责业务级与完整 schema 校验。
```

调度规则：

```text
1. 连续 ParallelSafe calls 组成一个 parallel group。
2. Sequential call 是 batch 内 ordering barrier。
3. parallel group 内最多同时执行 MaxParallelToolCalls 个调用。
4. 当前已经启动的 parallel group 必须等待全部完成。
5. 任一 group 出现 non-success result 后，后续尚未启动的 group 不执行。
6. 后续未启动 calls 生成 `status=skipped, code=prior_group_failed`。
7. ActionResult 可以乱序返回。
8. Transcript 中 ToolResults 必须按原始 ToolCall 顺序排列。
```

Parallel group 技术错误处理：

```text
1. 记录 first fatal technical error。
2. cancel group context。
3. stop launching later groups。
4. 对已经提交的 Environment Action 使用现有 best-effort cancellation 语义。
5. wait started workers drain / join。
6. 不再开始任何后续 group。
7. 所有 Runtime-owned work drain 后再 emit turn_failed。
```

AgentTurn terminal event 必须在当前 ToolBatch 已启动工作全部达到 Runtime-owned terminal / drained state 后发出。

ToolResult 生成不变量：

```text
对于已经进入 ModelDecision.ToolCalls 的每个 ToolCall，
如果 Step 继续进入下一次模型调用，
Runtime 必须产生且仅产生一个对应 ToolResult。
ToolResult.ToolCallID 必须原样回显 ToolCall.ID。
```

ToolCall ID 不变量：

```text
ToolCall.ID 在一个 AgentTurn 内必须唯一。
ToolCall.ID 在 intra-turn transcript 生命周期内必须稳定。
Provider 原生提供 ID 时保留 provider ID。
Provider 不提供 ID 时，由 provider wrapper / model runtime 生成稳定 runtime call ID。
```

示例：

```text
Input order:
    A parallel_safe
    B parallel_safe
    C sequential
    D parallel_safe
    E parallel_safe

Schedule:
    [A B] -> [C] -> [D E]

Model-visible result order:
    A, B, C, D, E
```

`speak` / `emote` 可以出现在同一个 ModelDecision 中，但 Stardew adapter 应声明为 Sequential，Runtime 按顺序执行。

## 4.6 Intra-turn Tool Transcript

Transcript 记录当前 Turn 内已经发生的模型工具调用和 Runtime 归一化后的工具结果：

```text
assistant tool_calls:
    call_1 speak {"text":"早上好。"}
    call_2 emote {"emote":"happy"}

tool results:
    call_1 speak succeeded
    call_2 emote succeeded
```

建议在 `runtime/internal/model` 增加 provider-neutral message：

```go
type MessageRole string

const (
    RoleUser      MessageRole = "user"
    RoleAssistant MessageRole = "assistant"
    RoleTool      MessageRole = "tool"
)

type Message struct {
    Role        MessageRole
    Content     string
    ToolCalls   []ToolCall
    ToolResults []ToolResult
}

type ToolCall struct {
    ID        string
    Name      string
    Arguments map[string]any
}

type ToolResult struct {
    ToolCallID string
    ToolName   string
    Status     string
    Code       string
    Message    string
    Output     map[string]any
}
```

`runtime/internal/model` 是 provider-neutral contract，不直接暴露 protocol protobuf carrier。`ToolCall.Arguments` 和 `ToolResult.Output` 使用 JSON-like `map[string]any`；Runtime 在 protocol 边界执行转换：

```text
ToolCall.Arguments
    -> BuildActionRequest
    -> google.protobuf.Struct arguments

ActionResult.output
    -> bounded projection
    -> ToolResult.Output
```

Transcript 规则：

```text
1. assistant message 保存同一 step 的 ordered ToolCalls。
2. tool result message 保存同一 step 的 ordered ToolResults。
3. ToolResult 不经过 LLM 二次摘要。
4. Transcript 不内置 speak / emote 等具体 capability 的语义摘要规则。
5. Recent Memory 只表示 previous turns；Transcript 只表示 current turn earlier steps。
```

允许进入 Transcript：

```text
tool_call_id
tool name
schema-bounded tool arguments
normalized status
short error code
short diagnostic message
bounded structured output
```

不允许进入 Transcript：

```text
action_id
raw ActionRequest / ActionResult protobuf
raw JSON dump
stack trace
provider-specific payload
未裁剪的大段 adapter diagnostic
```

ToolResult normalization：

| 来源 | 是否进入下一步 ModelRequest | ToolResult 规则 |
| --- | --- | --- |
| Environment Action `SUCCEEDED` | 是 | `status=succeeded, code=action_succeeded, output=<bounded ActionResult.output>` |
| ToolCall validation error | 是 | `status=invalid, code=<reason_code>` |
| BuildActionRequest error | 是 | `status=invalid, code=<reason_code>` |
| Batch validation skipped call | 是 | `status=skipped, code=batch_validation_failed` |
| ActionResult `REJECTED` | 是 | `status=rejected, code=<adapter_or_runtime_code>` |
| ActionResult `FAILED` | 是 | `status=failed, code=<adapter_or_runtime_code>` |
| ActionResult `CANCELLED` | 是 | `status=cancelled, code=<adapter_or_runtime_code>` |
| ActionResult `INTERRUPTED` | 是 | `status=interrupted, code=<adapter_or_runtime_code>` |
| Prior group failure skipped call | 是 | `status=skipped, code=prior_group_failed` |
| ActionResult `PENDING` / `ACCEPTED` / `RUNNING` / `UNSPECIFIED` | 否 | 直接 `turn_failed(stage=action, reason=non_terminal_action_status)` |

`message` 最多 120 字符，不包含 stack trace / raw protobuf / raw JSON。

`output` 来源于 `ActionResult.output` 的通用 bounded projection：

```text
1. Runtime 不解释 capability-specific 语义。
2. Runtime 只做 JSON-safe / Struct-safe 转换。
3. Runtime 限制最大字节数、最大深度、最大字段数和最大数组长度。
4. ActionResult.output 应被视为 Adapter 声明的 model-visible structured result。
5. Runtime 不按字段名猜测业务语义，不做 capability-specific 过滤。
6. projection 后为空时可以省略 output。
```

默认 bound 建议：

```text
MaxToolResultOutputBytes = 8192
MaxToolResultOutputDepth = 4
MaxToolResultOutputFields = 64
MaxToolResultOutputArrayItems = 32
```

## 4.7 Failure Feedback

```text
Provider technical error / timeout
    -> turn_failed(stage=model)

Observe technical error / timeout
    -> turn_failed(stage=observation)

SubmitAction technical error / timeout
    -> record first fatal technical error
    -> cancel group context
    -> drain / join started workers
    -> turn_failed(stage=action)

ToolCall validation error
    -> append invalid ToolResult for invalid call
    -> append skipped ToolResult for the rest of the batch
    -> continue if step budget remains

BuildActionRequest error
    -> append invalid ToolResult for build-failed call
    -> append skipped ToolResult for not-started calls
    -> continue if step budget remains

ActionResult REJECTED / FAILED / CANCELLED / INTERRUPTED
    -> append ToolResult feedback
    -> finish current running group
    -> append skipped ToolResult for later groups
    -> continue if step budget remains

ActionResult PENDING / ACCEPTED / RUNNING / UNSPECIFIED
    -> record first fatal semantic error
    -> cancel group context
    -> drain / join started workers
    -> turn_failed(stage=action, reason=non_terminal_action_status)

MaxToolCallsPerStep exceeded
    -> turn_failed(stage=model, reason=max_tool_calls_per_step_exceeded)

MaxToolCallsPerTurn exceeded
    -> turn_failed(stage=step, reason=max_tool_calls_per_turn_exceeded)
```

ToolCall budget 按 `ModelDecision.ToolCalls` 中提出的数量计数，不按实际执行成功数计数。`MaxToolCallsPerStep` 必须在 batch validation 前检查；`MaxToolCallsPerTurn` 必须累计 invalid / skipped / executed calls。

## 4.8 Memory Update

Phase5 一个成功 Turn 可能包含多个 Environment Tool outcomes：

```text
一个 completed AgentTurn -> 一条 MemoryRecord -> 多个 visible outcomes
```

建议最小修改：

```go
type Record struct {
    ...
    Outcomes []TurnOutcome
}
```

Memory 记录已由 Runtime 确认为 `SUCCEEDED` 的 Environment outcomes。

如果 Turn 因 action 技术错误失败，错误发生前已确认成功的 Action 可以在 terminal failure 前写入 Memory。

如果 Turn 因 `max_steps_exceeded`、budget exceeded、model 或 context 失败，Phase5 不补写未 settle 的历史 actions。

completed Turn 中 rejected / failed / invalid ToolResult 不进入 Recent Memory，只保留在 current turn transcript 和 trace 中。

## 4.9 Trace

新增 trace event：

```text
agent_step_started
agent_step_completed
agent_step_failed
tool_batch_started
tool_batch_completed
tool_batch_failed
turn_settled
```

既有事件 `Fields` 增加：

```text
step_index
tool_call_count
tool_call_index
tool_name
concurrency_mode
batch_group_index
control_kind
```

不变量：

```text
同一个 AgentTurn 内所有 AgentStep 使用同一个 turn_id。
turn_completed / turn_failed 仍然是唯一 terminal event。
terminal event 仍然最后发出。
turn_settled 是非 terminal event。
```

## 4.10 Config

新增 Agent Runtime Config：

```go
type Config struct {
    MaxSteps            int
    MaxToolCallsPerStep int
    MaxToolCallsPerTurn int
    MaxParallelToolCalls int
    MaxToolResultOutputBytes int
    MaxToolResultOutputDepth int
    MaxToolResultOutputFields int
    MaxToolResultOutputArrayItems int
    TurnTimeout         time.Duration
}
```

默认值建议：

```json
{
  "max_steps": 3,
  "max_tool_calls_per_step": 4,
  "max_tool_calls_per_turn": 6,
  "max_parallel_tool_calls": 4,
  "max_tool_result_output_bytes": 8192,
  "max_tool_result_output_depth": 4,
  "max_tool_result_output_fields": 64,
  "max_tool_result_output_array_items": 32,
  "turn_timeout_ms": 60000
}
```

预算语义：

```text
MaxSteps
    限制模型连续决策次数。

MaxToolCallsPerStep
    限制单次模型响应提出的 tool fan-out。

MaxToolCallsPerTurn
    限制单个 Turn 内模型提出的工具调用总数。

MaxParallelToolCalls
    限制同一 parallel group 内的并发执行数量。

TurnTimeout
    whole-turn hard bound，优先级高于全部 step / tool budgets。
```

`turn_timeout_ms = 60000` 覆盖默认最坏同步路径：

```text
ObserveTimeout 3s
+ MaxSteps 3 * LLMTimeout 8s
+ MaxToolCallsPerTurn 6 * ActionTimeout 3s
+ runtime / stream / trace margin
```

---

# 5. 文件边界

## 5.1 预计修改

```text
protocol/proto/gameagent.proto
    EntityRef.definition_id。
    CapabilityConcurrencyMode。
    Capability.concurrency_mode。

protocol/gen/go/*
protocol/gen/csharp/*
    重新生成。

protocol/tests/*
    更新 static / generation checks。

adapters/stardew/src/*
    ProtocolMapper 填充 definition_id=entity_id。
    speak / emote capability 填充 SEQUENTIAL。

adapters/stardew/tests/*
    更新 mapper / generated contract tests。

runtime/internal/model/provider.go
    Model contract migration。
    Response 从单 ToolCall 替换为 ModelDecision。
    ToolCall 增加 ID，Arguments 改为 map[string]any。
    Message 支持 ToolCalls / ToolResults。
    增加 RoleTool / ToolResult / ControlDirective。

runtime/internal/llm/deepseek/provider.go
runtime/internal/llm/deepseek/*_test.go
    解析多个 provider-native tool calls。
    保留或生成 ToolCall.ID。
    归一化 no-tool / internal settle sentinel。

runtime/internal/llm/openai/provider.go
runtime/internal/llm/openai/*_test.go
    解析多个 provider-native tool calls。
    保留或生成 ToolCall.ID。
    归一化 no-tool / internal settle sentinel。

runtime/internal/llm/fake/provider.go
runtime/internal/llm/fake/*_test.go
    支持 scripted ModelDecision。
    为测试生成稳定 ToolCall.ID。

runtime/internal/llm/factory.go
runtime/internal/llm/factory_test.go
    保持 provider 创建 contract，更新返回类型相关测试。

runtime/internal/tool/registry.go
    Entry 增加 Kind / Concurrency。
    Available() 稳定排序。
    Lookup() 返回执行 metadata。
    支持 batch preflight 校验。

runtime/internal/tool/environment_tool.go
    BuildActionRequest 签名迁移。
    在 protocol 边界把 ToolCall.Arguments map[string]any 转成 google.protobuf.Struct。

runtime/internal/agent/config.go
    增加 MaxSteps / MaxToolCallsPerStep / MaxToolCallsPerTurn / MaxParallelToolCalls。
    增加 ToolResult output bound 配置或常量。

runtime/internal/agent/loop.go
    将 one-step 主流程改为 bounded step loop。
    维护 intra-turn transcript。
    调用 Tool Scheduler。

runtime/internal/agent/scheduler.go
    新增 ToolCall batch preflight 与调度。
    Scheduler 属于 Tool Runtime logical responsibility；Phase5 先薄实现，逻辑增长后再迁入 runtime/internal/tool。

runtime/internal/context/context.go
runtime/internal/context/renderer.go
    AgentContext 增加 IntraTurnTranscript / Controls。
    Renderer 将 transcript 作为 model messages 传入 Provider request。
    Renderer 支持 RoleTool / ToolCalls / ToolResults。

runtime/internal/memory/record.go
runtime/internal/memory/projector.go
    MemoryRecord 支持多个 visible outcomes。
    ProjectInput 从单 ToolCall / ActionResult 迁移为 successful outcome list。

runtime/internal/trace/trace.go
runtime/internal/trace/turn.go
    增加 step / batch trace event 与 fields。

runtime/internal/gateway/gateway.go
    保持 event admission game-neutral。
    支持同一 EventAck 后多个 ActionRequest。

runtime/internal/agent/*_test.go
runtime/internal/tool/*_test.go
runtime/internal/context/*_test.go
runtime/internal/memory/*_test.go
runtime/internal/gateway/*_test.go
    更新单 ToolCall 断言为 ModelDecision / batch / transcript 断言。
```

## 5.2 不应修改

```text
runtime/internal/session
    AgentSessionKey 不加入 definition_id。

protocol/proto/gameagent.proto
    不加入 ActionBatchRequest / ActionBatchResult。
    不加入 target_definition_id。

adapters/stardew/src/*
    不把 Stardew 事件类型写成 Runtime core admission 规则。
```

---

# 6. 开发步骤

## 6.0 开发里程碑与提交节奏

Phase5 按可独立验收的开发块推进。每个开发块只引入当前块需要的架构变化，验收通过后再进入下一块。

| 里程碑 | 覆盖步骤 | 目标 | 验收标准 |
| --- | --- | --- | --- |
| M1：Protocol + Adapter | Step 1 | Protocol 显式携带 definition binding 与 capability concurrency metadata，Stardew adapter 填充对应字段 | protocol static / generation check 通过；`go test ./protocol/gen/go/...` 通过；Stardew ProtocolMapper 测试与 adapter build 通过 |
| M2：Model Contract Migration | Step 2 | Runtime model contract 从单 ToolCall 迁移到 ModelDecision，ToolCall arguments 改为 provider-neutral map | provider.go、deepseek/openai/fake provider、factory 与旧调用点迁移完成；model / llm / tool / context / agent 相关测试通过 |
| M3：Tool Registry Metadata | Step 3 | Registry 保存 Tool kind 与 concurrency metadata，并保持 Available() 稳定排序 | `go test ./runtime/internal/tool` 通过 |
| M4：Tool Scheduler | Step 4 | 同一 Step 内 ordered ToolCall batch 可按 Sequential / ParallelSafe 执行，并稳定返回 ToolResult | `go test ./runtime/internal/agent` 通过；preflight、ordering、parallel bound、failure drain 均被测试覆盖 |
| M5：Context Transcript | Step 5 | ToolCall / ToolResult transcript 以 provider-neutral message 进入下一次 Model Request | `go test ./runtime/internal/context ./runtime/internal/model` 通过 |
| M6：AgentLoop Multi-step | Step 6 | AgentTurn 进入 bounded step loop，支持 settle control、budget 与唯一终态 | `go test ./runtime/internal/agent` 通过 |
| M7：Integration Hardening | Step 7-11 | 失败修正、Memory、Trace、Gateway integration 与全量回归收口 | `go test ./runtime/...`、protocol checks、Stardew adapter tests/build 全部通过 |

提交边界：

```text
1. M1 单独提交，只包含 protocol 与 adapter 第一块开发。
2. M2 单独提交，先完成 model contract migration，再进入 scheduler。
3. M3-M6 可按风险拆成独立提交。
4. M7 作为集成收口提交。
```

M1 首块开发范围：

```text
protocol/proto/gameagent.proto
protocol/gen/go/*
protocol/tests/*
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/tests/ProtocolMapper.Tests/Program.cs
```

M1 不修改：

```text
runtime/internal/model
runtime/internal/llm
runtime/internal/agent
runtime/internal/context
runtime/internal/tool
```

## Step 1：Protocol additive fields

目标：

```text
Protocol 通过 EntityRef 显式携带 definition_id，并通过 Capability 携带 concurrency metadata。
```

测试：

```text
TestProtocolStaticCheckPasses
TestGoGenerationIsCurrent
TestCSharpGenerationIsCurrent
TestProtocolMapperFillsEntityRefDefinitionID
TestObservationDoesNotCarryDefinitionID
TestProtocolMapperMarksStardewCapabilitiesSequential
```

完成信号：

```powershell
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
```

## Step 2：Model Contract Migration + budgets

目标：

```text
Runtime model contract 从 single ToolCall 迁移到 ModelDecision，支持 ordered tool_calls、tool results、control，并能加载所有 Phase5 budgets。
```

执行顺序约束：

```text
Model Contract Migration 必须作为独立步骤完成。
Step 2 完成前不进入 Scheduler / AgentLoop batch execution。

Step 2 完成信号是：
    provider.go、deepseek/openai/fake provider、factory、environment_tool.go、renderer.go、loop.go 中
    旧 Response.ToolCall 与 ToolCall.Arguments *structpb.Struct 调用依赖均完成迁移；
    provider / factory / model contract 相关测试通过。
```

测试：

```text
TestConfigLoadsPhase5Budgets
TestConfigDefaultsPhase5BudgetsWhenMissingZeroOrNegative
TestConfigPhase5DefaultTurnTimeoutCoversWorstCaseBudget
TestModelResponseSupportsMultipleToolCalls
TestModelResponseSupportsSettleControl
TestModelMessageSupportsToolCallsAndToolResults
TestToolResultSupportsNormalizedStatusCodeAndMessage
TestToolCallArgumentsUseProviderNeutralMap
TestBuildRequestAddsAdditionalPropertiesForStrictToolSchema
TestBuildRequestUsesDeepSeekChatCompletionsShape
TestBuildRequestAllowsSettleOnlyWithoutEnvironmentTools
TestGenerateHTTPErrorDoesNotExposeRawBody
TestBuildRequestMapsToolTranscriptToProviderSafeInput
TestBuildRequestMapsToolTranscriptToProviderSafeMessage
TestParseResponseReturnsModelDecisionToolCalls
TestParseResponseNoToolCallSettles
TestParseResponseStripsSettleSentinel
```

完成信号：

```text
go test ./runtime/internal/model ./runtime/internal/llm/... ./runtime/internal/tool ./runtime/internal/context ./runtime/internal/agent
```

## Step 3：Tool Registry concurrency metadata

目标：

```text
Runtime 从 Capability 读取 concurrency_mode，并为 scheduler 提供稳定 metadata。
```

测试：

```text
TestRegistryLookupFindsEnvironmentTool
TestRegistryMapsUnspecifiedConcurrencyToSequential
TestRegistryMapsParallelSafeCapability
TestRegistryTreatsParallelSafeAsStrongAdapterCommitment
TestRegistryExcludesAsyncCapabilitiesFromPhase5ToolView
TestRegistryAvailableReturnsDeterministicOrder
TestSchedulerDoesNotExecuteWhenBatchValidationFails
TestSchedulerProducesOneToolResultPerToolCallWhenBatchValidationFails
TestSchedulerPreflightsBuildActionRequestBeforeAnyExecution
TestSchedulerRejectsDuplicateToolCallIDsDuringPreflight
```

完成信号：

```text
go test ./runtime/internal/tool
```

## Step 4：Tool Scheduler

目标：

```text
Runtime 能执行同一 step 内的 ToolCall batch，并保持 model-visible result order。
```

测试：

```text
TestSchedulerRunsSequentialCallsSerially
TestSchedulerRunsParallelSafeGroupWithBoundedConcurrency
TestSchedulerUsesSequentialCallAsOrderingBarrier
TestSchedulerReturnsResultsInOriginalToolCallOrder
TestSchedulerDoesNotExecuteWhenBatchValidationFails
TestSchedulerProducesOneToolResultPerToolCallWhenBatchValidationFails
TestSchedulerPreflightsBuildActionRequestBeforeAnyExecution
TestBuildActionRequestConvertsToolCallArgumentMapToProtocolStruct
TestBuildActionRequestRejectsNonStructSafeArgumentsBeforeExecution
TestValidateArgumentsAppliesCommonInputSchemaConstraints
TestSchedulerRejectsDuplicateToolCallIDsDuringPreflight
TestSchedulerValidatesArgumentsAgainstInputSchemaBeforeExecution
TestSchedulerSkipsLaterGroupsAfterPriorGroupFailure
TestSchedulerDrainsParallelGroupBeforeSkippingLaterGroupsOnModelVisibleFailure
TestSchedulerReturnsCompletedSiblingActionsBeforeParallelTechnicalError
TestSchedulerReturnsCompletedActionsBeforeSequentialTechnicalError
TestSchedulerDrainsStartedWorkersBeforeTechnicalFailureTerminal
TestPreferTechnicalErrorKeepsRealErrorOverCancellation
TestTerminalActionToolResultUsesStatusCodeWhenAdapterCodeIsEmpty
TestSchedulerFailsOnNonTerminalActionStatus
TestSchedulerHonorsMaxParallelToolCalls
```

完成信号：

```text
go test ./runtime/internal/agent
```

## Step 5：Context 支持 batch transcript

目标：

```text
第二次模型调用能看到当前 Turn 内前一步 ordered tool calls 与 ordered tool results。
```

测试：

```text
TestRendererIncludesBatchToolCallTranscriptMessages
TestRendererSeparatesRecentMemoryFromIntraTurnTranscript
TestRendererDoesNotLeakRawToolResultInternals
TestRendererExposesSettleControlInstruction
TestToolResultNormalizationIsDeterministic
TestToolResultNormalizationIsProviderNeutral
TestToolResultIncludesBoundedStructuredOutput
TestToolResultOutputProjectionAppliesBounds
```

完成信号：

```text
go test ./runtime/internal/context ./runtime/internal/model
```

## Step 6：AgentLoop multi-step + batch

目标：

```text
单个 Turn 可以执行：
    step 1: speak + emote + settle
```

测试：

```text
TestHandleEventRunsBatchToolCallsThenSettle
TestHandleEventRunsMultipleStepsUntilSettle
TestHandleEventCompletesOnSettleOnlyDecision
TestHandleEventRejectsToolCallIDReusedAcrossSteps
TestHandleEventFailsWhenMaxStepsExceeded
TestHandleEventFailsWhenMaxToolCallsPerStepExceeded
TestHandleEventFailsWhenMaxToolCallsPerTurnExceeded
TestHandleEventTurnTimeoutCanPreemptBudgetsWithDelayedProvider
TestHandleEventRunsOneTurnNPCInteraction
```

完成信号：

```text
go test ./runtime/internal/agent
```

## Step 7：失败修正路径

目标：

```text
模型可以在有限 step budget 内根据 batch tool/action failure 修正。
```

测试：

```text
TestHandleEventRetriesAfterInvalidToolCallBatchWithinStepBudget
TestHandleEventRetriesAfterActionResultTerminalFailure
TestHandleEventDoesNotSettleAfterFailedBatchEvenWhenControlSettleRequested
TestHandleEventFailsWhenFailureLoopExhaustsMaxSteps
```

完成信号：

```text
go test ./runtime/internal/agent
```

## Step 8：Memory 支持多 outcome

目标：

```text
completed Turn 的多个 Environment outcomes 能进入短期 Memory；action 技术错误前已确认成功的 outcomes 也会进入短期 Memory。
```

测试：

```text
TestProjectorBuildsRecordWithMultipleOutcomes
TestRendererSummarizesMultiOutcomeMemory
TestFailedMultiStepTurnDoesNotAppendMemory
TestActionTechnicalFailureRecordsCompletedParallelSiblingMemory
TestActionTechnicalFailureRecordsCompletedSequentialMemory
TestCompletedTurnAfterRejectedActionWritesOnlySuccessfulOutcomes
```

完成信号：

```text
go test ./runtime/internal/memory ./runtime/internal/context ./runtime/internal/agent
```

## Step 9：Trace step / batch observability

目标：

```text
可以复盘一个 Turn 内每个 Step、ToolCall batch、scheduler group、结果顺序和最终 settle / failure。
```

测试：

```text
TestMultiStepTraceEventsShareTurnIDAndIncreaseStepIndex
TestToolBatchTraceFieldsIncludeCallCountAndConcurrency
TestSchedulerReturnsResultsInOriginalToolCallOrder
TestSchedulerDrainsStartedWorkersBeforeTechnicalFailureTerminal
TestMultiStepTerminalEventIsUniqueAndLast
TestMaxStepsTraceFailureReason
```

完成信号：

```text
go test ./runtime/internal/trace ./runtime/internal/agent
```

## Step 10：Gateway integration tests

目标：

```text
通过 gRPC bufconn 证明 Adapter stream 可以在一个 EventAck 后收到同一 step 的多个 ActionRequest。
```

测试：

```text
TestConnectRunsSingleStepBatchWithTwoActionsAndSettle
TestConnectRunsParallelSafeBatchAndOrdersTranscriptByToolCallOrder
TestConnectRunsMultiStepForNonStardewTriggerWithDefinitionID
TestConnectMaxStepsExceededProducesSingleTerminalTrace
TestConnectRetriesAfterRejectedActionResult
```

实现要点：

```text
1. 使用 scripted provider：speak + emote + settle。
2. fake adapter 分别回 ActionResult SUCCEEDED。
3. 断言只有一个 EventAck。
4. 断言同一个 turn_id / step_index 下出现多个 action trace。
5. parallel-safe fake capabilities 返回顺序与 ToolCall 顺序相反，Transcript 顺序仍稳定。
6. non-Stardew trigger 使用 damage_received / creature:alpha。
7. non-Stardew fixture 必须使用 entity_id != definition_id：
   - entity_id = creature:alpha
   - definition_id = creature/generic
8. 断言 Memory key 仍使用 game_id + world_id + entity_id。
9. 断言 Gateway core 不含 player_interacted_with_npc 等 game-specific event_type allowlist。
```

完成信号：

```text
go test ./runtime/internal/gateway
```

## Step 11：回归与验收

验证命令：

```powershell
go test ./runtime/...
go test ./protocol/gen/go/...
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet run --project adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet run --project adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
dotnet build adapters/stardew/GameAgent.Stardew.csproj
git diff --check
```

`go test -race ./runtime/...` 仍受当前 Windows C toolchain 限制，不作为本机硬门，但验收记录必须继续标注 CI/Linux 待补。

---

# 7. 测试矩阵

| 场景 | 类型 | 预期 |
| --- | --- | --- |
| settle 第一轮返回 | agent unit | 无 ActionRequest，Turn completed，不写 Memory |
| one Environment Tool + settle | agent unit | Turn completed，Memory 单 outcome |
| speak + emote + settle | agent unit / gateway integration | 同一 step 两个 ActionRequest，唯一 terminal |
| speak + emote 不带 settle | agent unit | ToolResults 进入 transcript，进入下一 step |
| Sequential tools in batch | scheduler unit | 按 ToolCall 顺序串行执行 |
| ParallelSafe tools in batch | scheduler unit | 有界并发执行，结果按 ToolCall 顺序回灌 |
| ParallelSafe + Sequential + ParallelSafe | scheduler unit | Sequential 作为 batch 内 ordering barrier |
| completion order != call order | scheduler/context unit | Transcript 按 call order 输出 |
| provider native multi tool calls | provider wrapper unit | 保持原始顺序归一化为 ordered ToolCalls |
| provider no-tool final response | provider wrapper unit | 归一化为 ControlSettle |
| provider internal settle sentinel | provider wrapper unit | sentinel 被剥离，不进入 ToolCalls / budget / Scheduler |
| batch preflight failure | agent/scheduler unit | 不发送任何 ActionRequest，每个 proposed ToolCall 都有 ToolResult |
| BuildActionRequest failure in preflight | scheduler unit | 不执行任何 ActionRequest |
| duplicate ToolCall.ID in one step | agent unit | Phase7.4 后由 Loop 在写入 Transcript / Scheduler preflight 前拒绝为 invalid_model_response |
| duplicate ToolCall.ID across steps | agent unit | model-visible preflight failure，不发送任何 ActionRequest |
| valid calls skipped by batch validation | agent unit | skipped(batch_validation_failed) 进入 transcript |
| prior group failure | scheduler/agent unit | 当前 group 完成，后续 group skipped(prior_group_failed) |
| ToolResult structured output | context/model unit | bounded ActionResult.output 进入 ToolResult.output |
| ToolResult output bounds | context/model unit | output 超限后按固定 bound 投影 |
| technical error in parallel group | scheduler/trace unit | started workers drain 后才发 terminal trace |
| ASYNC capability in Phase5 | tool unit | 不进入当前 AgentTurn Tool View |
| ToolCall arguments conversion | tool/scheduler unit | map[string]any 在 BuildActionRequest 边界转为 protocol Struct |
| ToolCall InputSchema validation | tool/scheduler unit | required / enum / additionalProperties / type 不满足时 preflight failure |
| max_tool_calls_per_step exceeded | agent unit | turn_failed stage=model |
| max_tool_calls_per_turn exceeded | agent unit | turn_failed stage=step |
| max_steps exceeded | agent unit | turn_failed stage=step reason=max_steps_exceeded |
| short TurnTimeout with delayed provider | agent unit | TurnTimeout 抢占 budgets，trace 记录真实 timeout stage |
| unknown tool -> corrected tool + settle | agent unit | 第一步错误进入 transcript，Turn completed |
| ActionResult REJECTED -> corrected tool + settle | agent unit / gateway integration | 失败 action 不直接终止，模型可修正 |
| ActionResult FAILED / CANCELLED / INTERRUPTED -> corrected tool + settle | agent unit | terminal failure feedback 进入 transcript，模型可修正 |
| settle requested with failed batch | agent unit | 不 completed，继续下一 step |
| non-terminal ActionStatus | agent unit | PENDING / ACCEPTED / RUNNING / UNSPECIFIED 直接 turn_failed |
| completed turn with rejected step | memory/context unit | rejected step 不进入 Recent Memory |
| definition_id in EntityRef | protocol/runtime unit | Agent Descriptor 可区分 entity_id / definition_id |
| Observation has no definition_id | protocol unit | 避免 target definition 双来源 |
| missing definition_id | runtime unit | 按 binding policy 处理，不隐式污染 Memory key |
| Stardew definition_id | adapter mapper unit | 固定 NPC 填充 definition_id=entity_id |
| non-Stardew trigger multi-step | gateway integration | damage_received 可进入 multi-step，entity_id != definition_id |
| Gateway event admission baseline | gateway unit/integration | core 不含 game-specific event_type allowlist |
| different NPC memory isolation | gateway integration | Phase4 不变量保持 |
| reconnect memory sharing | gateway integration | Phase4 不变量保持 |

---

# 8. 验收标准

Phase5 已按 `Accepted with Known Limitations` 验收。最低条件为：

```text
1. 一个 AgentStep 可以承载多个 ToolCalls。
2. runtime/internal/model 完成 ModelDecision contract migration。
3. ToolCall.Arguments 使用 provider-neutral map[string]any，并在 protocol boundary 转换为 Struct。
4. Tool Scheduler 支持 Sequential / ParallelSafe，并保持 result order 稳定。
5. Environment Tool 默认 Sequential。
6. settle 可以单独结束 Turn，也可以在 successful batch 后结束 Turn。
7. failed / rejected / invalid ToolResult 不会被 settle 吞掉。
8. max_steps / max_tool_calls_per_step / max_tool_calls_per_turn / max_parallel_tool_calls 均有测试保护。
9. Tool / Action failure 可以作为 ToolResult feedback 进入下一次 ModelRequest。
10. 每个 proposed ToolCall 在继续下一次模型调用时都有且只有一个 ToolResult。
11. ToolResult 支持 bounded structured output。
12. batch validation failure 与 prior group failure 都产生 skipped ToolResult。
13. Batch Preflight 在任何 ActionRequest 发出前完成。
14. parallel group 技术错误在 started workers drain 后才发 terminal event。
15. Provider wrapper 明确支持 multi tool calls / no-tool settle，并且不提示模型调用未声明的 settle sentinel。
16. Phase5 Tool View 不暴露 ASYNC capability。
17. 已确认成功的多个 Environment outcomes 可以进入短期 Memory。
18. Protocol 只通过 EntityRef.definition_id 携带 definition binding。
19. Protocol 显式携带 capability concurrency_mode。
20. Stardew mapper 填充 definition_id=entity_id。
21. non-Stardew fixture 明确验证 entity_id != definition_id。
22. Memory scope 仍为 game_id + world_id + entity_id。
23. Runtime 仍不依赖 Stardew / Adapter 代码。
24. Gateway core 不含任何 game-specific event_type allowlist。
25. ActionRequest / ActionResult 保持单 action 语义。
26. AgentTurn Control 不进入 Tool Registry / ActionRequest / Adapter。
```

已知限制：

```text
1. AgentBinding static source 与 EntityRef/static binding mismatch gate 延后实现。
2. Tool Registry 当前按单 adapter / 单 active stream 假设运行。
3. Windows 本机不跑 go test -race；CI/Linux 继续补 race 验收。
4. Memory renderer 仍保留 Phase4 speak / emote 兼容摘要语义，需进入 compat roadmap。
```

---

# 9. Architecture Boundary Check

验收时已核对：

```text
[x] runtime/ 不 import adapters/
[x] runtime/ 不 import Stardew / SMAPI
[x] adapter 不 import runtime/internal
[x] protocol/proto/gameagent.proto 只做 additive 字段变更
[x] protocol/proto/gameagent.proto 不包含 ActionBatchRequest / ActionBatchResult
[x] protocol/proto/gameagent.proto 不包含 target_definition_id
[x] protocol/proto/gameagent.proto 不包含 Observation.definition_id
[x] definition_id 只通过 target EntityRef + Agent Binding policy 进入 Agent Descriptor
[x] runtime/internal/model 不使用 protocol protobuf carrier 作为 ToolCall.Arguments 类型
[x] map[string]any 到 google.protobuf.Struct 的转换只发生在 protocol boundary
[x] AgentTurn Control 不进入 ActionRequest
[x] Tool Registry 只表达 Environment Tool
[x] Provider-facing settle sentinel 不进入 Tool Registry / Scheduler / Adapter
[x] Environment Tool 仍由 Adapter capability 声明
[x] Capability UNSPECIFIED concurrency 按 SEQUENTIAL 处理
[x] Capability SEQUENTIAL 只表达同一 ToolCall batch 内的 ordering barrier
[x] ASYNC capability 不进入 Phase5 AgentTurn Tool View
[x] Batch Preflight 成功前不发送任何 ActionRequest
[x] ToolResult 对每个 proposed ToolCall 一一对应
[x] ToolResult.output 只包含 bounded structured output
[x] Transcript 不包含 raw ActionRequest / ActionResult protobuf
[x] parallel group 技术错误 drain 后再发 terminal event
[x] Trace events 不改变 Agent 主流程
[x] AgentTurn terminal event 唯一且最后
[x] Memory 仍绑定 AgentSessionKey，而不是 definition_id
[x] non-Stardew fixture 覆盖 entity_id != definition_id
[x] AgentLoop / Gateway core 不含任何 game-specific event_type allowlist（含现存逻辑）
```

---

# 10. 风险与裁决

## R1：为什么 Phase5 支持一个 Step 多个 ToolCall？

裁决：支持。

原因：

```text
Step 表示一次模型决策，不表示单个动作。
模型可以在一次决策中同时提出多个已确定的工具调用。
Runtime 通过 scheduler 保证副作用顺序与并发边界。
```

## R2：多 ToolCall 是否等于并行执行？

裁决：不是。

```text
Multi ToolCall Step 是模型输出形态。
ParallelSafe / Sequential 是 Runtime batch 内执行策略。
Environment Tool 默认 Sequential。
Sequential 不是全局 Environment lock。
```

## R3：为什么不引入 ActionBatchRequest？

裁决：不引入。

原因：

```text
现有 stream 已能在同一 EventAck 后发送多个 ActionRequest。
ActionBatchRequest 会提前引入 partial success、batch cancellation、async batch resume 等复杂语义。
Phase5 的 batch 边界属于 Runtime scheduler，不属于 Protocol Action message。
```

## R4：definition_id 为什么进入 EntityRef？

裁决：进入。

原因：

```text
definition_id 是 Agent Definition / Archetype 的引用 key。
EntityRef 表达游戏实体身份与绑定事实。
Observation 表达当前实体状态，不承载目标实体的 definition binding。
AgentDefinition 模板本体后续由 Runtime Definition source 提供，不进入 Adapter Observation。
```

## R5：definition_id 是否进入 AgentSessionKey？

裁决：不进入。

原因：

```text
AgentSessionKey / Memory / State 绑定具体游戏实体。
多个动态实体可以共享同一个 definition_id，但必须拥有独立 Memory。
```

## R6：settle 应该放在哪一层？

裁决：AgentTurn Control。

原因：

```text
settle 表达当前 Turn 可以正常结束，不是游戏能力。
它由 AgentLoop 处理，不进入 Tool Registry / ActionRequest / Adapter。
```

## R7：ToolResult 是否需要 LLM 摘要？

裁决：不需要。

原因：

```text
ToolResult 是 Runtime 归一化的 model-visible 结果。
下一次 ModelRequest 直接携带 ordered assistant tool_calls 与 ordered tool_results。
ActionResult.output 通过 bounded structured output 进入 ToolResult.output。
```

## R8：batch validation 失败后如何回灌？

裁决：每个 proposed ToolCall 都有 ToolResult。

原因：

```text
Transcript 必须保持 ToolCall 与 ToolResult 一一对应。
invalid call 生成 invalid result。
未执行 call 生成 skipped(batch_validation_failed)。
```

## R9：batch 执行中出现业务失败后是否继续后续 group？

裁决：不继续。

原因：

```text
Runtime 不理解 capability 之间的业务依赖。
当前已启动 group 等待完成。
后续尚未启动 group 生成 skipped(prior_group_failed)。
下一 AgentStep 由模型根据完整 ToolResults 重新决策。
```

## R10：Observe once 会不会让第二步上下文过时？

裁决：Phase5 接受。

原因：

```text
Phase5 使用短时 sync actions。
ActionResult 作为 ToolResult 进入下一步即可证明 multi-step。
每 step re-observe 留到 Phase6 resume / Phase7 recovery 设计。
```

## R11：失败 Action 已经发生但 Turn 后续失败，Memory 不写会不会丢事实？

裁决：已确认成功的 Action 写入 Memory。

原因：

```text
ActionResult 已确认为 SUCCEEDED 时，游戏侧事实已经发生。
如果同一 batch 后续遇到技术错误，Runtime 仍记录已确认成功的 outcomes。
未知、非终态、失败、取消、中断或被跳过的 action 不进入 Memory。
```

## R12：Protocol additive 是否等于所有 Adapter 语义兼容？

裁决：不是。

原因：

```text
新增字段是 wire additive。
concurrency_mode 缺失可降级为 SEQUENTIAL。
definition_id 缺失必须按 Agent Binding policy 处理。
Runtime 不得无条件假设旧 Adapter 已支持 Definition Binding。
```

## R13：BuildActionRequest 应在什么时候执行？

裁决：Batch Preflight 阶段。

原因：

```text
整个 batch 的 ToolCall ID、lookup、arguments、concurrency metadata 与 ActionRequest 构建必须先全部成功。
Preflight 成功前不得发送任何 ActionRequest。
Preflight 失败时不产生 Environment side effect。
```

## R14：parallel group 技术错误后什么时候发 terminal？

裁决：drain 后发。

原因：

```text
技术错误先记录 first fatal error。
Runtime 取消 group context 并停止启动后续 group。
已启动 workers 必须 drain / join。
AgentTurn terminal event 必须在 Runtime-owned work 清理后发出。
```

## R15：Provider 如何表达 settle？

裁决：由 provider wrapper 归一化。

原因：

```text
Runtime core 只接收 ControlSettle。
Provider-facing 使用 no-tool final response 表达 settle。
internal settle sentinel 只作为兼容解析路径，不在 provider tools 或 prompt 中声明。
internal settle sentinel 不进入 Tool Registry、Scheduler、ToolCall budget、Transcript 或 Adapter。
```

## R16：Phase5 是否暴露 ASYNC Capability？

裁决：不暴露。

原因：

```text
Phase5 只实现短时 sync action wait。
Current AgentTurn Tool View 只包含当前 Runtime 能执行的 Capability。
ASYNC Capability 留到 Phase6 resume 语义完成后进入 Tool View。
```

## R17：PARALLEL_SAFE 是否需要 compatibility matrix？

裁决：不需要。

原因：

```text
Phase5 的 PARALLEL_SAFE 是强承诺。
不能承诺与任意同 batch PARALLEL_SAFE 调用安全并发的 Capability 使用 SEQUENTIAL。
resource group / lock key / compatibility matrix 留给后续阶段。
```

## R18：是否保留旧 Response.ToolCall 以降低改动？

裁决：不保留。

原因：

```text
Phase5 的模型输出语义是 ModelDecision，不是单 ToolCall。
multi-tool、settle control 和 ToolResult transcript 都依赖统一 model contract。
保留旧 Response.ToolCall 会把 Phase5 主逻辑拆散到 AgentLoop 和 provider 特判里。
```

## R19：ToolCall.Arguments 为什么使用 map[string]any？

裁决：model 层使用 provider-neutral JSON-like map。

原因：

```text
runtime/internal/model 不绑定 protocol protobuf carrier。
Provider wrapper 输出 JSON-like arguments。
BuildActionRequest 在 protocol boundary 转换为 google.protobuf.Struct。
转换失败属于 Batch Preflight failure。
```

---

# 11. Phase5 不变量

实现过程中不得破坏：

```text
EnvironmentSession != AgentSession
AgentStep belongs to AgentTurn
AgentStep = one model decision, not one action
Action != synchronous function（Phase5 仍只实现 sync wait，但不把模型写死）
Entity identity != Agent Definition
Agent Definition / Archetype = game_id + definition_id
EntityRef.definition_id is a binding key, not the template body
Observation does not own target definition binding
AgentDefinition belongs to Runtime Definition source when that source is introduced
Memory scope = game_id + world_id + entity_id
Observation narrow waist != universal game state schema
Available Tools == current AgentTurn capability view
Trigger admission != hardcoded game-specific event_type
Observer != Functional Hook
AgentTurn Control != Environment Tool
ToolResult transcript != Recent Memory
ToolCall order != completion order
Model-visible ToolResult order = original ToolCall order
Every proposed ToolCall maps to exactly one ToolResult when the turn continues
ToolCall budgets count proposed calls, not executed calls
Sequential concurrency is batch-scoped, not a global lock
Batch Preflight succeeds before any ActionRequest is sent
Parallel group technical failure drains Runtime-owned work before terminal event
Provider-facing settle encoding is stripped before Runtime Tool scheduling
Phase5 Tool View contains only supported sync capabilities
Model Response = ModelDecision, not single ToolCall
ToolCall.Arguments is provider-neutral JSON-like data
```

---

# 12. 子 Agent 评审要求

从 Phase5 起，阶段技术方案和重要代码变更必须经过子 agent 独立评审。

本方案完成后至少派发两个评审：

```text
1. Architecture reviewer
   检查是否违反 Runtime Architecture v0.3、Roadmap v0.4、Agent Binding ADR。

2. Test / implementation reviewer
   检查开发步骤是否可执行，测试矩阵是否覆盖 tool batch、settle、budgets、失败修正、memory、trace、protocol。
```

评审意见应保存到：

```text
docs/phase5/评审意见.md
```

只有评审里的 P0/P1 被处理或明确裁决、P2 被纳入方案或明确记录为后续项后，才进入 Phase5 代码实现。

---

# 13. 一句话结论

> **Phase5 不做 Planner、不做异步 Action、不引入 ActionBatchRequest；它把 AgentTurn 从 one-step 扩展为有界 multi-step，把模型契约迁移为 ModelDecision，把 AgentStep 定义为一次模型决策和 0..N 个 ToolCalls，用 Sequential / ParallelSafe scheduler 管理 batch 内执行顺序，用 AgentTurn Control 表达 settle，用 ordered ToolCall / ToolResult transcript 回灌模型，并通过 EntityRef.definition_id 与 Capability.concurrency_mode 显式携带绑定和调度元数据。**
