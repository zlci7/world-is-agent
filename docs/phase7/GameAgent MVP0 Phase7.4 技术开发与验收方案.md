# GameAgent MVP0 Phase7.4 技术开发与验收方案

> **Status:** Implementation Plan Accepted
> **Date:** 2026-09-04
> **Phase:** Phase7.4 Selection, Budget 与 Observability
> **Roadmap Baseline:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md) v1.9
> **Previous Gate:** Phase7.3 Accepted
> **Review Required Before Coding:** Yes
> **Code Baseline:** `main` @ `aff1826`
> **Plan Amendment:** Token Budget Amendment accepted for implementation; Code Accepted remains pending user review.

---

# 1. 阶段目标

Phase7.4 让 Context 在内容变长时仍然稳定、可解释、可测试。

本阶段主要证明：

```text
Turn setup:
EnvironmentToolCatalog
    ↓
Tool size admission
    ↓
Final TurnToolView + ToolAdmissionReport
    ↓
AgentTurn

Each AgentStep:
BuildInput + Final TurnToolView
    ↓
Engine.Build
    ↓
deterministic selection / budget inside Engine
    ↓
BuildResult
    ↓
ContextProjection + ContextBuildReport
    ↓
Renderer.Render(BuildResult.Projection)
    ↓
model.Request
```

Phase7.4 不重新设计 Definition lookup、Tool View 生命周期或 Context Projection 主链路。它只在 Phase7.3 已稳定的投影基础上，加上确定性的选择、裁剪、工具准入和报告。

本阶段的边界是：

```text
Tool admission
    Turn-scoped，只在 AgentTurn setup 执行一次。

Context budget
    Step-scoped，每次 Provider.Generate 前随当前 transcript 重建。
```

---

# 2. 非目标

Phase7.4 不做：

```text
Provider-specific 精确 tokenizer
模型窗口自动探测
语义压缩
向量检索
持久 Memory backend
Stardew 实机最终验收
Definition lookup 重设计
Tool View 生命周期重设计
Context Source 插件框架
字段级事实冲突报告
adapters/ 代码改造
proto 字段或生成代码改造
```

真实 Stardew 对话体验验收属于 Phase7.5。Phase7.4 可以使用 Stardew-shaped fixture，但不把实机 smoke 当成 hard gate。

---

# 3. 当前代码事实

## 3.1 Phase7.3 已完成 Context Engine Core

当前 Runtime 已有：

```text
agentcontext.Engine.Build(input)
    -> agentcontext.ContextProjection

Renderer.Render(projection)
    -> model.Request
```

`Engine.Build` 已经负责：

```text
scope validation
definition binding validation
EventProjection
ContextFactProjection
ObservationProjection
RecentMemory projection
CurrentTurnTranscript projection
ContextProjection.Tools
Instruction
```

`Renderer` 只消费 `ContextProjection`，不直接接收原始 `GameEvent`、`[]memory.Record` 或 `TurnToolView`。

## 3.2 现有预算只是局部保护

当前配置已有：

```text
MaxRecentMemoryTokens
MaxToolResultOutputTokens
MaxToolResultOutputDepth
MaxToolResultOutputFields
MaxToolResultOutputArrayItems
```

它们只保护 Recent Memory 和 ToolResult / ToolArguments 的局部输出，不是完整 Context Budget。

当前还没有：

```text
总请求规模上限
分段预算
裁剪报告
Tool schema size admission
final request estimated token summary
```

## 3.3 Phase7.2 已完成 Tool View 生命周期

当前 Tool View 链路是：

```text
EnvironmentToolCatalog
    ↓
TurnToolView snapshot
    ├── ContextProjection.Tools
    └── Scheduler lookup
```

Phase7.4 不能把模型可见工具和 Scheduler 可执行工具拆成两份各自裁剪的列表。

Tool size admission 发生在捕获最终 `TurnToolView` 前；一旦 AgentTurn 开始，每次 `Provider.Generate` 前重建 Context Projection 时不能再改变本 turn 的工具集合。

---

# 4. 设计范围

## 4.1 Build 结果

Phase7.4 将 `Engine.Build` 输出升级为携带 Projection 和 Report 的结果对象。

目标方向：

```text
Engine.Build(input)
    -> BuildResult, error

BuildResult
    Projection ContextProjection
    Report     ContextBuildReport
```

`ContextBuildReport` 不进入模型输入。它只用于：

```text
unit tests
agent / gateway integration tests
trace diagnostics
debug logs
```

预算和选择在 `Engine.Build` 内完成。`BuildResult.Projection` 是已经完成预算处理的 Projection；`BuildResult.Report` 是旁路诊断，不参与 section 优先级、预算裁剪或模型渲染。

`Engine.Build` 是 Context 构建的唯一主入口。测试可以在测试文件内保留 fixture helper，但 Runtime 主链路不保留第二套兼容 builder。AgentLoop 主链路必须使用 `BuildResult.Projection` 渲染模型请求，并把 `BuildResult.Report` 交给 trace。

当必保内容超过 hard limit 时：

```text
BuildResult.Projection
    可以为空或只包含已安全生成的部分

BuildResult.Report
    尽量包含失败前已知的尺寸、保留项和失败 reason

error
    表示本次不能调用 Provider
```

## 4.2 Budget 配置

Phase7.4 使用确定性的 provider-neutral estimated token，不做 provider-specific tokenizer。

建议最小配置：

```text
MaxRequestTokens
MaxSystemTokens
MaxUserMessageTokens
MaxDefinitionTokens
MaxObservationTokens
MaxEventTokens
MaxContextFactsTokens
MaxRecentMemoryTokens
MaxTranscriptTokens
MaxToolCount
MaxToolDescriptionTokens
MaxToolSchemaTokens
MaxTotalToolSchemaTokens
MaxToolResultOutputTokens
```

配置值为 0 或负数时使用 Runtime 默认值，不表示无限制。

预算计算以 estimated token count 为准。Phase7.4 不保证和具体模型 tokenizer 完全一致。

估算规则固定为：

```text
CJK、全角字符、ASCII 标点、JSON 结构字符、emoji 和其他 Unicode
    1 rune = 1 estimated token

ASCII 字母、数字、普通空白连续 run
    ceil(run length / 4)

结构化对象
    stable compact JSON
    再执行同一套 text estimate
```

预算尺寸分为两个层次：

```text
Projection estimated tokens
    Engine 内部用于确定性 selection 和 section 预算，是裁剪启发量。
    它基于 stable compact JSON 和固定 section 顺序。
    它不复刻 Renderer 文本格式，也不对外承诺等于最终 request estimated token size。

Rendered request estimated tokens
    Renderer.Render 后，由 buildModelRequest 对最终 model.Request 计算。
    它是 MaxSystemTokens、MaxUserMessageTokens、MaxRequestTokens 的权威检查。
```

Section budget 使用 projection estimated tokens 做确定性预预算；全局 `MaxRequestTokens / MaxSystemTokens / MaxUserMessageTokens` 裁剪循环使用 `Renderer.Render` 后的 rendered request estimated tokens 做 fit 判定。Engine 复用真实 Renderer 计算 request 尺寸，不维护第二套文本格式估算。`buildModelRequest` 保留同一口径的最终 hard gate，作为 request 发出前的最后防线。

`EstimateRequestTokens` 对最终 `model.Request` 使用 provider-neutral estimated-token sizing 约定，不等同于 Provider tokenizer。最终 `model.Request` 超过任一 hard limit 时，`buildModelRequest` 返回 `ContextBuildReport + error`，`runBoundedSteps` 不调用 Provider、不提交 Action。

`ContextBuildReport` 记录稳定的 projection estimated token summary。最终 `model.Request` token summary 在 `Renderer.Render` 后由 `buildModelRequest` 使用 `EstimateRequestTokens` 补齐。`EstimateRequestTokens` 对已渲染的 `Messages[].Content` 按同一套 text estimate 计数，对 `Tools` / `Controls` 等结构化字段使用 stable compact JSON 后估算。Renderer 输出变长时 request estimate 应随之变大，并触发 hard gate，而不是静默漂移。

`MaxSystemTokens` 和 `MaxUserMessageTokens` 约束对应 request section；`MaxRequestTokens` 是 provider-neutral request estimated-token hard limit。若必保内容和工具在最终 request 中超过 `MaxRequestTokens`，Runtime 不裁剪必保内容，也不静默发送超预算请求；本次 build 明确失败，并在 `ContextBuildReport` 中记录 `required_context_over_budget`。若超限来自必保 section 本身，Report 同时记录 `required_section_over_budget` 作为细分原因。

Phase7.4 的配置入口只接受 token 预算字段。项目当前未上线，不保留旧计量单位预算兼容入口。Runtime 内部只能形成一份 effective token budget。默认 `MaxRequestTokens = 65536` 表示 64K provider-neutral estimated tokens，是 Phase7.4 token-only 后有意变宽的新语义，不等价于旧 64KB 预算。

## 4.3 Budget 执行顺序与 Section 优先级

Phase7.4 固定一条预算流水线，不新增可插拔预算策略。

执行顺序：

```text
1. 归一化 BudgetConfig。
2. AgentTurn setup 执行 Tool size admission，得到 Final TurnToolView + ToolAdmissionReport。
3. Engine 使用 Final TurnToolView 生成完整结构化 Projection。
4. 对 ToolCall arguments 和 ToolResult output 先应用逐项局部 bound。
5. 构造 required minimum 和 fixed cost。
6. 按共享 MaxDefinitionTokens 保留 Definition。
7. Transcript 非空时保护最近一个完整因果组。
8. 按固定顺序裁剪 optional 内容。
9. 使用 Projection estimated-token sizing helper 完成 section 预预算。
10. 返回 BuildResult，或返回带 ContextBuildReport 的 budget failure。
11. Renderer.Render 后，buildModelRequest 使用 EstimateRequestTokens 执行最终 request hard gate。
```

Phase7.4 必须区分必须保留、固定成本和可以裁剪的内容。

必须保留：

```text
RuntimePolicy
Instruction
AgentDescriptor
CurrentEvent shell
Current Event ContextFacts minimum
CurrentObservation identity / revision / game_time
```

固定成本：

```text
Final TurnToolView projection estimated tokens
```

`ContextBuildReport` 必须生成并可 trace，但它不是 Context section，不进入模型输入，也不参与预算保留列表。

高优先级保留：

```text
AgentDefinition
GameDefinition
```

主要裁剪对象：

```text
RecentMemory
CurrentTurnTranscript
Event payload
Observation nested state
Observation extensions / nearby_entities
ContextFact attributes
ToolResult output
ToolCall arguments
```

Agent Definition 的优先级高于旧 Memory。预算不足时，旧 Memory 不能把当前事件、当前观察、角色定义和基础指令挤掉。

`MaxDefinitionTokens` 是 Game Definition 与 Agent Definition 的共享预算。分配顺序固定为：

```text
1. Agent Definition
2. Game Definition
```

Definition 内部字段按固定字段顺序处理；list item 保持 definition 文件中的原始顺序，只按完整 item 裁剪。

Current Event ContextFacts minimum 是有界事实投影，至少保留：

```text
kind
actor
target
scope
label
```

`kind / actor / target / scope / label` 是恒保留 identity 字段。`text` 可以截断为明确的 `_truncated` marker；`attributes` 是可裁剪内容。若 `text` 本身超过字段预算，保留 fact identity 和 marker，不用旧 Memory 替代当前事件事实。

Optional 内容保留顺序固定为：

```text
1. Agent Definition optional fields
2. Game Definition optional fields
3. Current Observation optional fields
4. Current Event payload
5. ContextFact attributes
6. Recent Memory，按 timeline 结果从新到旧保留
7. Current Turn Transcript 的更旧完整因果组，从新到旧保留
```

预算不足时按保留顺序的反向裁剪 optional 内容；因此 Current Turn Transcript 的更旧完整因果组先于 Recent Memory 被裁剪。

结构化对象的遍历顺序固定为：

```text
map
    按 key 字典序

array
    保持原始顺序

Definition list
    保持文件中的原始顺序

Memory
    按 timeline 结果，从新到旧保留

Transcript
    按完整因果组，从新到旧保留
```

## 4.4 结构化裁剪规则

Phase7.4 只能按结构边界裁剪，不能把 JSON 或工具 schema 截成非法字符串。

结构化裁剪复用现有 projection bounds 原语，不新增第二套裁剪引擎：

```text
projectOutputValue
projectBoundedMap
orderedMap
stableCompactJSON
```

必须保持合法结构的内容：

```text
GameEvent payload
Observation state
Observation extensions
ContextFact attributes
ToolCall arguments
ToolResult output
Tool input schema
```

结构化裁剪可以使用：

```text
已知字段按显式优先级裁剪
未知 map 字段按 key 字典序保留前缀
限制数组项数量
限制 map 字段数量
替换为明确的 _truncated marker
整条 memory 丢弃
整对 transcript tool call/result 丢弃
整项 tool 丢弃
```

不能使用：

```text
截断 JSON 字符串后继续当 JSON 使用
截断 tool schema 字符串
只保留 ToolCall 不保留对应 ToolResult
只保留 ToolResult 不保留对应 ToolCall
```

逐项局部 bound 和 section budget 的顺序固定为：

```text
1. 先对每条 ToolCall arguments 和 ToolResult output 应用现有局部 bound。
2. 再对 Current Turn Transcript 应用 section budget。
3. Transcript section budget 基于局部 bound 后的产物。
4. ContextBuildReport 只记录 section 级裁剪事件；局部 bound 保持现有 _truncated marker。
```

## 4.5 Recent Memory 选择

Phase7.4 保持 Phase7.3 已有 memory 时间线语义：

```text
过滤未来 GameTime memory
同一 GameTime + 非零 SourceEventSequence 稳定排序
缺少 GameTime 或 sequence 时保持 MemoryStore 返回顺序
SourceContextFacts 先于 Tool outcomes
```

在此基础上增加预算裁剪：

```text
先完成时间线选择和通用 projection
再按 MaxRecentMemoryTokens 做确定性保留
优先保留更新的 memory
被丢弃数量写入 ContextBuildReport
```

`MaxRecentMemoryTokens` 使用 Recent Memory projection 的 compact JSON projection estimated tokens 计量，与 `ContextBuildReport.Sections["recent_memory"].ProjectionEstimatedTokens` 口径一致。Recent Memory 是 optional context；若最新一条 memory projection 单独超过预算，本次 build 可以丢弃全部 Recent Memory，而不是发送超预算上下文。

本阶段不引入语义检索或向量相似度排序。

## 4.6 Current Turn Transcript 裁剪

Transcript 是当前 turn 内模型消息和工具反馈的顺序记录。裁剪时必须保持 step 语义。

规则：

```text
按消息顺序保持可读性
优先保留最近 step
ToolCall 与对应 ToolResult 成对保留
rejected ToolResult 与对应 ToolCall 保持同组语义
被裁剪消息数量写入 ContextBuildReport
```

Transcript 非空时，至少保护最近一个完整因果组：

```text
Assistant ToolCall
    +
对应 ToolResult / rejected 状态
```

配对键固定为：

```text
ToolCall.ID
ToolResult.ToolCallID
```

一个因果组由一条 assistant tool_call message 和对应 tool result / rejected 状态组成。ToolCall 和 ToolResult 可以位于不同 `model.Message`，裁剪时按因果组整体保留或整体丢弃。

Phase7.4 正常输入只接受完整 ToolCall-ToolResult / rejected 因果组。缺少对应结果的 ToolCall 视为 transcript input invalid，或尚未形成完整组，不进入下一次 Context build。未来若支持 pending 状态下再次调用模型，再引入显式 pending transcript state。

更旧完整因果组从旧到新裁剪，从新到旧保留。若最近完整因果组经过结构化裁剪后仍无法放入 hard limit，本次 build 返回明确 budget failure，不拆散 ToolCall / ToolResult。

Phase7.4 可以继续使用 `model.Message` 作为 transcript projection 类型，但裁剪后不能产生孤立 ToolCall 或孤立 ToolResult。

## 4.7 Tool Size Admission

Phase7.4 对工具做独立准入，不截断 tool schema。

Tool admission 是 Turn-scoped，不是 Step-scoped。目标形态：

```text
type ToolAdmissionResult struct {
    View   TurnToolView
    Report ToolAdmissionReport
}
```

概念入口可以是：

```text
EnvironmentToolCatalog
    -> BuildTurnToolView(admissionConfig)
    -> Final TurnToolView + ToolAdmissionReport
```

具体 Go 名字由实现决定，但生命周期必须固定：AgentLoop 在 AgentTurn setup 执行一次 admission，所有 AgentStep 使用同一份 Final TurnToolView。

冻结语义：

```text
admission 返回 filtered copy，不 mutate 原始 EnvironmentToolCatalog。
BuildInput.TurnToolView 接收 final admitted view。
Engine 只读 final admitted view。
Scheduler 只读同一个 final admitted view。
ContextProjection.Tools 来自 final admitted view。
```

最小规则：

```text
1. 先检查单 Tool description 上限。
2. 再检查单 Tool schema 上限。
3. admission 内一律按 tool name 稳定排序。
4. 单次遍历，同时应用 MaxToolCount 和 MaxTotalToolSchemaTokens。
5. 构造 Final TurnToolView 和 ToolAdmissionReport。
```

准入结果：

```text
通过单项 description / schema 检查后的工具 i，
仅当以下条件同时满足时保留：
    当前保留数量 < MaxToolCount
    当前累计 schema estimated tokens + schema_i estimated tokens <= MaxTotalToolSchemaTokens

超过 MaxToolCount
    -> 工具整项剔除

description 超过 MaxToolDescriptionTokens
    -> 工具整项剔除

input_schema 超过 MaxToolSchemaTokens
    -> 工具整项剔除

所有 input_schema 总大小超过 MaxTotalToolSchemaTokens
    -> 工具整项剔除直到总大小满足限制
```

Admission 排序不依赖 `TurnToolView.Available()`。`Available()` 可以继续提供稳定输出，但 admission 自己必须按 tool name 排序；不得依赖 map iteration。

被剔除工具不能进入：

```text
ContextProjection.Tools
model.Request.Tools
Scheduler lookup
```

Tool size admission 发生在最终 `TurnToolView` 捕获前。捕获后只能读取最终视图并记录诊断，不能继续裁剪工具。最终效果必须是模型可见工具与 Scheduler 可执行工具来自同一份最终视图。

`Engine.Build` 不再剔除工具。它把 Final TurnToolView 当作固定成本，并用 rendered request estimated tokens 驱动全局 optional context 裁剪：

```text
Renderer.Render(ContextProjection)
    ->
EstimateRequestTokens
    ->
按固定保留顺序裁剪 optional projection
```

如果 Final TurnToolView 加上所有 required minimum 已超过 `MaxRequestTokens`，本次 build 失败并记录 `required_context_over_budget`，不能在某个 AgentStep 临时修改工具视图。只有具体 required section 本身超限时，才额外记录 `required_section_over_budget`。

Phase7.4 不改变 capability bootstrap、EnvironmentToolCatalog ownership 或 hot refresh 语义。

## 4.8 ContextBuildReport

`ContextBuildReport` 是最小诊断，不是完整 observability 平台。

Report 来源分三段：

```text
Tool Runtime
    产生 ToolAdmissionReport。

Engine
    产生 Context selection / budget report。

AgentLoop
    合并 bounded ToolAdmissionReport summary。
    在 Renderer.Render 后补齐 final request estimated token summary。
    将最终 ContextBuildReport summary 写入 trace。
```

Engine 不重新计算工具剔除原因；`runtime/internal/tool` 也不依赖 `runtime/internal/context`。

最小内容：

```text
effective budget config
included section summary
cropped / dropped section summary
Definition fallback 状态
Memory retained / dropped count
Transcript retained / dropped count
Tool accepted / dropped names and reasons
ToolAdmissionReport summary
final request estimated token summary
stable warning codes
```

Report 必须可测试、可 trace，但不进入模型 prompt。

Report 本身必须确定且有界。不要包含：

```text
wall-clock timestamp
random id
完整原始 Context
完整工具 schema
无界 dropped name list
```

长列表使用：

```text
count
前 N 个稳定排序名称
truncated_count
stable reason code
```

ToolAdmissionReport 中的工具名称是诊断展示名，不是 lookup key。展示名必须同时满足列表数量有界和单项长度有界；实际 Final TurnToolView / Scheduler lookup 继续使用完整 tool name。

建议稳定 reason code：

```text
definition_fallback
memory_budget_exceeded
transcript_budget_exceeded
tool_count_exceeded
tool_description_too_large
tool_schema_too_large
tool_total_schema_budget_exceeded
required_section_over_budget
required_context_over_budget
```

## 4.9 AgentLoop / Trace 接线

`AgentLoop.buildModelRequest` 继续不调用 `Provider.Generate`，但需要把 ContextBuildReport 显式交回主循环。

Phase7.4 在 AgentLoop 中新增 report 交接：

```text
Engine.Build
    -> BuildResult, error
Renderer.Render(BuildResult.Projection)
    -> model.Request, error
buildModelRequest merges ToolAdmissionReport summary
buildModelRequest adds final request estimated token summary
buildModelRequest checks request hard limit
return model.Request + ContextBuildReport + error
```

推荐接口方向：

```text
buildModelRequest(...)
    -> model.Request, agentcontext.ContextBuildReport, error
```

`runBoundedSteps` 负责：

```text
成功：
    consume request + ContextBuildReport
    emit context_request_built / report summary
    emit model_request_started
    调用 Provider.Generate

失败：
    consume ContextBuildReport + error
    emit context_request_build_failed / report summary
    fail turn with stable failure reason
    不调用 Provider
```

Trace 只记录 report summary 和稳定 reason，不写入大段原始 context。

不要复用已有 `context_loaded` / `context_load_failed` 事件。它们表示 MemoryStore 读取路径。Phase7.4 使用独立语义：

```text
context_request_built
context_request_build_failed
```

`required_section_over_budget` 是 ContextBuildReport 内部细分 code；Turn failure reason 使用 `required_context_over_budget`。

`required_context_over_budget` 是同一个稳定信号，可以同时出现在 ContextBuildReport reason code、trace 字段和 turn failure reason，不新增三套不同常量。

`context_build_failed`、`context_render_failed`、`required_context_over_budget` 保持为 turn failure reason / Error.Code 命名，不作为 Phase7.4 trace event 名；trace event 使用 `context_request_built` / `context_request_build_failed`。

预算失败时 AgentTurn 中止，错误进入 trace，Runtime 等待下一次事件；本次 turn 不调用 Provider，也不提交 Action。

---

# 5. 预计代码改动

## 5.1 Context Engine

在 `runtime/internal/context` 中补齐：

```text
BuildResult
ContextBuildReport
budget failure error
BudgetConfig / effective budget
deterministic section sizing
fixed budget execution order
structured projection cropping
```

`Engine.Build` 仍是 Context 主入口，不新增第二套 Context Builder。

## 5.2 Tool Runtime

在 `runtime/internal/tool` 中增加 Tool size admission 的最小能力。

目标结果：

```text
ToolAdmissionResult
ToolAdmissionReport
同一份最终 TurnToolView
    ├── ContextProjection.Tools
    └── Scheduler lookup
```

工具被预算剔除时，模型和 Scheduler 必须同时看不到该工具。

`runtime/internal/tool` 不依赖 `runtime/internal/context`。Tool 单元测试直接验证 `ToolAdmissionReport`；Agent 或 Context 集成测试再验证 ToolAdmissionReport summary 进入 ContextBuildReport / Trace。

## 5.3 AgentLoop / Gateway

AgentLoop 负责：

```text
在 Turn setup 完成 Tool admission
把 Final TurnToolView 传给每个 AgentStep
buildModelRequest 合并 report、补齐 final request estimated token size、执行 hard gate
在写入 Transcript / 调用 Scheduler 前拒绝同一 AgentStep 内重复 ToolCall.ID
runBoundedSteps 消费 request / report / error，统一写 trace 和控制 Provider 调用
```

ToolCall.ID 唯一性在 Phase7.4 后分为两层：同一步重复 ID 属于模型响应边界错误，由 Loop 以 `invalid_model_response` 结束当前 Turn；后续步骤复用此前 ID 继续由 Loop 生成 model-visible tool failure，不进入 Scheduler。Scheduler 保留 batch duplicate preflight 作为执行层内部保护。

Phase7.4 引入 `BudgetConfig` 时，同步清理现有 `RendererConfig` / `NewRenderer(config)` 死配置。Renderer 保持无状态入口，避免形成 `EngineConfig`、`RendererConfig`、`BudgetConfig` 三份重复配置。

Gateway 不重新实现 Context Budget，也不在 Adapter 协议上新增字段。

Phase7.4 不修改 `adapters/`、不修改 proto，也不运行 C# Adapter 测试作为 hard gate。

## 5.4 文档与状态

Phase7.4 实现完成后再同步：

```text
docs/STATUS.md
docs/summary/GameAgent 阶段规划.md
本 Phase7.4 文档状态
```

Draft 阶段不修改公开 Roadmap。

---

# 6. 测试计划

## 6.1 Context Engine 单元测试

新增或调整 `runtime/internal/context` 测试，覆盖：

```text
同输入同预算生成相同 Projection 和 Report
同 Projection 生成相同 section estimated token summary
预算不足时必保段落仍存在
Agent Definition 不被旧 Memory 挤掉
Recent Memory 按稳定顺序裁剪
裁剪后的 Event payload 可合法 marshal
裁剪后的 Observation state 可合法 marshal
裁剪后的 ContextFact attributes 可合法 marshal
裁剪后的 ToolResult output 可合法 marshal
ContextBuildReport 记录 section included / cropped / dropped
ContextBuildReport 记录 Definition fallback
ContextBuildReport 记录 final request estimated token summary
预算配置为 0 或负数时使用 Runtime 默认值
配置入口只读取 token 预算字段，不产生双预算
必保内容超过 MaxRequestTokens 时返回明确失败和 required_context_over_budget
Current Event ContextFacts core 不被旧 Memory 挤掉
map 插入顺序不同但语义相同的输入生成相同 Projection、Request 和 Report
固定 model.Request 下 EstimateRequestTokens 输出确定
Renderer 后 final request 超过 hard limit 时返回 report + error
ToolResult 局部 bound 先于 Transcript section budget 生效
```

## 6.2 Transcript 测试

测试要求：

```text
Transcript 超预算时优先保留最近 step
ToolCall / ToolResult 成对保留
Transcript 非空时至少保留最近完整因果组
rejected ToolResult 与对应 ToolCall 保持同组语义
缺少对应结果的 ToolCall 不作为合法 pending 进入下一次 build
不会生成孤立 ToolCall
不会生成孤立 ToolResult
裁剪数量写入 ContextBuildReport
```

## 6.3 Tool Admission 测试

新增或调整 `runtime/internal/tool` 测试，覆盖：

```text
MaxToolCount 超限按稳定顺序整项剔除
admission 一律按 tool name 排序
count limit 与 total schema limit 使用单遍贪心合取规则
description 超限整项剔除
单 schema 超限整项剔除
总 schema 超限按稳定顺序保留可放入工具
schema 不被字符串截断
最终模型可见工具与 Scheduler lookup 一致
ToolAdmissionReport 能看到剔除原因
同一输入生成稳定的 Final TurnToolView 和 ToolAdmissionReport
```

## 6.4 AgentLoop / Gateway 集成测试

测试要求：

```text
Provider request 使用 budgeted projection
ContextBuildReport summary 进入 trace
ToolAdmissionReport summary 进入 ContextBuildReport / trace
buildModelRequest 在 Renderer 后执行 request hard gate
Tool admission 只在 Turn 开始时发生一次
step 1 / step 2 的 Final TurnToolView 完全一致
被剔除工具不进入 model.Request.Tools
被剔除工具不能被 Scheduler 执行
required minimum + Final TurnToolView 超过 MaxRequestTokens 时不调用 Provider
required minimum + Final TurnToolView 超过 MaxRequestTokens 时不提交 Action
Phase5 multi-step 行为不退化
Phase6 async resume 行为不退化
Phase7.1 Definition 行为不退化
Phase7.2 Tool View 行为不退化
Phase7.3 ContextProjection 行为不退化
```

## 6.5 回归测试

推荐命令：

```powershell
go test ./runtime/internal/context
go test ./runtime/internal/tool
go test ./runtime/internal/agent
go test ./runtime/internal/gateway
go test ./...
```

Phase7.4 不触碰 `adapters/` 或 proto 生成，不需要运行 Stardew Adapter C# 测试作为 hard gate。

---

# 7. 验收条件

Phase7.4 代码开发完成后必须满足：

```text
1. Engine.Build 输出 ContextProjection、ContextBuildReport 和明确 error。
2. ContextBuildReport 不进入模型输入。
3. 同输入同预算生成相同 Model Request 和 Report。
4. 当前事件、当前观察、Agent Descriptor、Instruction、Runtime Policy 不被预算裁掉。
5. Current Event ContextFacts identity minimum 不被旧 Memory 挤掉，text 可截断为 marker。
6. Agent Definition 优先级高于旧 Memory，MaxDefinitionTokens 先分配给 Agent Definition。
7. Recent Memory 按稳定顺序裁剪。
8. Transcript 非空时保留最近完整因果组，缺少结果的 ToolCall 不作为合法 pending 进入下一次 build。
9. JSON payload / state / attributes / ToolResult output 裁剪后仍是合法结构。
10. Tool schema 不被字符串截断。
11. Tool admission 只在 AgentTurn setup 执行一次。
12. Tool admission 一律按 name 排序，并按 count + total schema 单遍贪心合取规则保留。
13. 模型可见工具与 Scheduler 可执行工具来自同一份最终视图。
14. Renderer 后最终 model.Request 超过 hard limit 时返回 ContextBuildReport + error，不调用 Provider，不提交 Action。
15. ToolAdmissionReport bounded summary 由 AgentLoop 合并进 ContextBuildReport / trace。
16. ContextBuildReport 记录 fallback、裁剪、剔除和 request estimated token summary。
17. Trace 只记录 report summary，不写入大段原始 context。
18. Phase7.1 Definition、Phase7.2 Tool View、Phase7.3 ContextProjection 主链路不退化。
```

本阶段不以真实 Stardew 实机表现、持久 Memory、向量检索或 provider-specific tokenizer 作为验收条件。

---

# 8. Review Checklist

Review Phase7.4 时重点看：

```text
1. 是否只在 Phase7.3 Projection 基础上增加选择、预算和报告。
2. 是否没有重新设计 Definition lookup。
3. 是否没有重新设计 Tool View 生命周期。
4. Tool size admission 是否是 Turn-scoped，Context budget 是否是 Step-scoped。
5. Tool size admission 是否保证模型可见工具和 Scheduler 可执行工具一致。
6. Budget 是否使用确定性 estimated token，而不是 provider-specific tokenizer。
7. Renderer 后 request hard gate 是否由 buildModelRequest 执行，并作为 MaxRequestTokens 的权威检查。
8. Budget 执行顺序、map/list/memory/transcript 顺序是否唯一。
9. JSON / tool schema 是否不会被截成非法结构。
10. Transcript 是否保持 ToolCall / ToolResult 成对语义。
11. ContextBuildReport 是否是最小、有界、确定的诊断，而不是新的 observability 平台。
12. 是否没有提前要求 Phase7.5 的 Stardew 实机验收。
13. 是否没有进入 Phase8 的持久 Memory。
```

---

# 9. Implementation Handoff

## M1：Turn-scoped Tool Admission

交付：

```text
ToolAdmissionResult
ToolAdmissionReport
BuildTurnToolView / SnapshotWithAdmission 等价入口
Final TurnToolView
tool name sort
count + total schema greedy admission
```

验收点：

```text
Tool admission 只在 AgentTurn setup 执行一次
超限 tool 整项剔除
schema 不被截断
ToolAdmissionReport 有稳定 reason
Final TurnToolView 对所有 AgentStep 不变
```

## M2：BuildResult / ContextBuildReport 骨架

交付：

```text
BuildResult
ContextBuildReport
budget failure error
effective budget config
section estimated token summary
RendererConfig / NewRenderer(config) dead config cleanup
```

验收点：

```text
Engine.Build 返回 Projection + Report + error
Renderer 只消费 Projection
Report 不进入 model.Request
预算配置 0 或负数时使用 Runtime 默认值
BudgetConfig 不与 RendererConfig 形成重复配置
```

## M3：Deterministic Context Budget

交付：

```text
fixed budget execution order
required minimum / fixed cost
Definition shared budget
Current Event ContextFacts minimum
Recent Memory budget
Transcript causal group budget
Observation / Event structured cropping
Projection estimated-token sizing helper
EstimateRequestTokens
token-only config loading
```

验收点：

```text
必保段落仍存在
Current Event ContextFacts core 不被旧 Memory 挤掉
Agent Definition 不被旧 Memory 挤掉
JSON 投影仍合法
Transcript 保留最近完整因果组
map/list/memory/transcript 顺序确定
裁剪原因进入 ContextBuildReport
```

## M4：AgentLoop Trace 与失败控制流

交付：

```text
buildModelRequest 返回 request + ContextBuildReport + error
runBoundedSteps 写 context_request_built / context_request_build_failed trace
required_context_over_budget failure reason
ToolAdmissionReport summary merge
buildModelRequest 内 request hard gate
Provider / Action failure gate
```

验收点：

```text
ContextBuildReport summary 进入 trace
Trace 不写入大段原始 context
预算失败不调用 Provider
预算失败不提交 Action
Request.Tools 与 Scheduler lookup 一致
trace event 与 failure reason 命名空间分开
```

## M5：测试与回归

交付：

```text
Runtime Go tests
Phase7.4 文档根据实际实现偏差更新
```

验收点：

```text
Phase7.4 验收条件逐条可证明
go test ./... 通过
公开状态不把已完成 Budget / ContextBuildReport 写成未来工作
```

---

# 10. 下一阶段衔接

Phase7.4 完成后，Phase7.5 可以在稳定 Context Projection、Budget 和 ContextBuildReport 基础上做 Stardew 端到端验收。

Phase7.5 主要看：

```text
Stardew Definition 内容是否足够
真实 NPC 对话是否能稳定看到正确 Context
玩家输入、Recent Memory、Tool View 和动作结果是否形成完整体验闭环
```

Phase7.5 不应重新设计 Phase7.4 的预算和报告主链路。
