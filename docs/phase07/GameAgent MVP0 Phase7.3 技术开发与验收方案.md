# GameAgent MVP0 Phase7.3 技术开发与验收方案

> **Status:** Accepted
> **Date:** 2026-09-04
> **Phase:** Phase7.3 Context Engine Core
> **Roadmap Baseline:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md) v1.9
> **Previous Gate:** Phase7.1 + Phase7.2 Accepted
> **Review Required Before Coding:** Completed
> **Code Baseline:** `main` @ `5cb4512`
> **Accepted Commit:** `main` @ `d565273`
> **Review Result:** Accepted
> **Reviewer:** zlc7
> **Review Date:** 2026-09-04

---

# 1. 阶段目标

Phase7.3 把 Context 从“临时拼装 prompt”推进成 Runtime 的正式 Context Engine 主链路。

本阶段主要证明：

```text
Definition / Descriptor
Current Event / ContextFacts
Current Observation
Recent Memory
Current Turn Transcript
TurnToolView snapshot
    ↓
Engine.Build
    ↓
Context Projection
    ↓
Renderer
    ↓
Model Request
```

7.3 的重点是把“本轮模型到底应该看到什么”变成清楚、结构化、可校验、可测试的 Runtime 行为。

7.3 不追求完整预算系统，也不做真实 Stardew 实机总验收。预算、裁剪报告和最终 Stardew 体验验收分别属于 Phase7.4 和 Phase7.5。

---

# 2. 非目标

Phase7.3 不做：

```text
Context Budget Manager
BuildReport
Tool size / token admission
Persistent Memory backend
Vector retrieval
Context Source 插件框架
字段级 Stardew Observation 解析
按 GameEvent.payload key 做游戏语义判断
Adapter 协议字段改造
Stardew 实机最终验收
```

本阶段可以保留短期 Recent Memory soft limit，因为它已经存在并服务于当前 Recent Memory 渲染；但不把它扩展成完整 Context Budget。Phase7.4 后该运行时字段由 `MaxRecentMemoryTokens` 承接。

---

# 3. 当前代码事实

## 3.1 Phase7.1 已完成 Definition / Descriptor 链路

当前 Runtime 已有：

```text
runtime/internal/definition
    GameDefinition
    AgentDefinition
    AgentInstanceDescriptor
    Catalog
    LoadCatalogFromDir

runtime/config/games/stardew-valley/definitions
    game.json
    npc-*.json
    archetype-town-villager.json
```

`AgentLoop` 当前按 `game_id + definition_id` 查 `AgentDefinition`，按 `game_id` 查 `GameDefinition`。找不到 Definition 时使用 fallback，不伪造定义内容。

## 3.2 Phase7.2 已完成 Tool View 链路

当前 Runtime 已有：

```text
runtime/internal/tool
    EnvironmentToolCatalog
    TurnToolView
    BootstrapDiagnostics
    BuildEnvironmentToolCatalog
```

Gateway 在 `Connect` 中完成 capability bootstrap，并把 `EnvironmentToolCatalog` 绑定到当前 stream。`AgentLoop.HandleEvent` 收到非 nil catalog 后，在 turn 开始时捕获 `TurnToolView`。

模型请求里的 `Tools` 和 Scheduler 执行工具查找来自同一份 `TurnToolView`。

## 3.3 Context 目前还是 Builder + Renderer

当前 `runtime/internal/context` 里有：

```text
AgentContext
BuildInput
Builder.Build
Renderer.Render
```

`Builder.Build` 主要做结构化装配和少量 nil / 空 key 校验。

`Renderer.Render` 当前同时承担：

```text
Recent Memory 选择
Recent Memory 文本摘要
Current Event JSON 渲染
Current Observation JSON 渲染
Transcript 渲染
最终 model.Request 组装
```

这让“Context 选择”和“模型请求渲染”混在一起。Phase7.3 需要把这两件事分开。

## 3.4 Current Event ContextFacts 尚未成为独立 Context 段落

当前 `GameEvent.context_facts` 会随着完整 `GameEvent` JSON 一起进入 `[Current Event]`。

Recent Memory 已能保存 `SourceContextFacts`，但当前 turn 的 `ContextFacts` 还没有独立投影段落。Phase7.3 需要让它单独进入模型上下文，并避免在 Event Projection 中重复渲染同一份 facts。

## 3.5 Recent Memory 选择逻辑目前在 Renderer 中

当前 Renderer 已有：

```text
selectTimelineMemories
trimMemories
renderMemory
```

它会过滤 `MemoryRecord.GameTime > CurrentGameTime` 的未来记忆，并按 `MaxRecentMemoryTokens` 做短期 soft limit。

Phase7.3 应把这类“选哪些 memory 进入本轮上下文”的逻辑归到 Context Engine，Renderer 只渲染已经选好的 Projection。

## 3.6 Tool outcome memory 渲染仍有 Stardew-specific 分支

当前 `visibleActionSummary` 会按具体工具名处理：

```text
speak
emote
present_dialogue
face_player
```

这违反 Phase7.3 的目标：Runtime Core 不应该按 Stardew capability name 写分支。Phase7.3 需要改成通用 Tool outcome projection。

## 3.7 Scope 校验仍不完整

当前 Gateway 已完成 pre-turn target admission：

```text
event_type 非空
target_entity_id 非空
target_entity_id 能在 entities 中找到唯一、不冲突的 EntityRef
AgentSessionKey 使用 connection game_id + event world_id + target entity_id
```

但 Context 层还没有统一校验：

```text
GameEvent.world_id 是否等于 AgentSessionKey.world_id
Observation.world_id 是否等于 AgentSessionKey.world_id
Observation.entity_id 是否等于 AgentSessionKey.entity_id
GameDefinition.game_id 是否等于 AgentSessionKey.game_id
AgentDefinition.game_id / definition_id 是否匹配当前 Agent
```

Phase7.3 需要把这些校验放到 Context Engine。

---

# 4. 设计范围

## 4.1 新主入口

Phase7.3 引入正式 Context Engine：

```text
agentcontext.NewEngine(config)
agentcontext.Engine.Build(input)
    -> agentcontext.ContextProjection
```

概念名叫 Context Engine，Go 类型名叫 `Engine`，主入口是 `Engine.Build`。

`Engine.Build` 负责：

```text
结构化输入校验
Scope 一致性校验
当前事件投影
当前事件 ContextFacts 投影
Observation 投影
Recent Memory 时间线选择
Recent Memory 可见摘要投影
Transcript 投影
TurnToolView 投影
```

`Renderer.Render` 负责：

```text
ContextProjection -> model.Request
```

Renderer 不再决定 memory 选择，不再做 scope 校验，不再从原始 `GameEvent` 里拆 ContextFacts。

Renderer 只消费 `ContextProjection`。它不直接接收原始 `GameEvent`、`[]memory.Record` 或 `TurnToolView`，也不调用 memory selection、tool result normalization、ContextFact extraction、scope validation 等构建逻辑。

## 4.2 最小对象模型

Phase7.3 使用最小结构，不建设插件框架。

```text
Engine
    无全局状态
    可长期复用

BuildInput
    SessionKey
    CanonicalTarget
    AgentDescriptor
    GameDefinition
    AgentDefinition
    RuntimePolicy
    Event
    Observation
    RecentMemories
    TurnToolView
    Transcript

ContextProjection
    RuntimePolicy
    GameDefinition
    AgentDefinition
    AgentDescriptor
    CurrentEvent
    CurrentEventContextFacts
    CurrentObservation
    RecentMemory
    CurrentTurnTranscript
    Tools
    Instruction
```

Phase7.3 使用 `ContextProjection` 作为正式投影类型。现有 `AgentContext` 不再作为 AgentLoop 和 Renderer 之间的主传递对象；如果为了兼容少量现有测试短暂保留类型名，它也只能是 `ContextProjection` 的别名，不能形成第二套 Context 对象。

`BuildInput` 沿用现有类型名并扩展字段，避免再引入一套 `ContextBuildInput`。

`BuildInput` 是原始输入集合，`ContextProjection` 是模型可见投影。两者不能只是同一个对象换名。

```text
BuildInput.Event
    -> ContextProjection.CurrentEvent
       使用 EventProjection，只保留事件外壳，不包含 context_facts

BuildInput.Event.context_facts
    -> ContextProjection.CurrentEventContextFacts
       使用 ContextFactProjection 列表

BuildInput.RecentMemories
    -> ContextProjection.RecentMemory
       已完成过滤、排序、soft limit 和模型可见摘要

BuildInput.Transcript
    -> ContextProjection.CurrentTurnTranscript
       已复制并整理，ToolResult 输出按本阶段本地上限处理

BuildInput.TurnToolView
    -> ContextProjection.Tools
       来自 TurnToolView.Available() 的 []model.ToolDefinition
       不把 Scheduler 执行元数据暴露给 Renderer

Instruction / Authority Rules
    -> ContextProjection.Instruction
       使用固定规则，Renderer 只负责格式化
```

## 4.3 Context Projection 段落

最终 Projection 至少包含以下逻辑段落：

```text
Runtime Policy
Game Definition
Agent Definition
Agent Instance Descriptor
Current Event
Current Event Context Facts
Current Observation
Recent Memory
Current Turn Transcript
Turn Tool View
Instruction / Authority Rules
```

`Runtime Policy` 继续进入 `model.Request.System`。

`Turn Tool View` 不需要把完整 JSON schema 重复写进 user message。Context Engine 只把模型可见的 `[]model.ToolDefinition` 放入 `ContextProjection.Tools`，并由 Renderer 输出到 `model.Request.Tools`。

## 4.4 Current Event Projection

Current Event Projection 保留通用事件外壳：

```text
event_id
event_type
world_id
target_entity_id
sequence
game_time
canonical target EntityRef
payload
```

`payload` 可以作为通用 JSON 进入 Projection，但 Runtime 不按 payload key 写 Stardew-specific 判断。

Event Projection 不渲染 `context_facts` 字段。`ContextFacts` 由独立段落表达，避免同一事实在模型输入里重复出现。

## 4.5 Current Event ContextFacts Projection

`GameEvent.context_facts` 是 Adapter 显式声明的模型可见事件事实。

Context Engine 按输入顺序投影当前事件的 facts。每条 fact 至少保留：

```text
kind
actor_entity_id
target_entity_id
scope_id
text
label
attributes
```

投影规则：

```text
空 facts -> 渲染为 (none)
text / label 保留原始语义，但做首尾空白裁剪
attributes 使用通用 JSON 渲染
Runtime 不从 payload 或 Observation.state 反推 ContextFact
```

## 4.6 Current Observation Projection

Current Observation 继续作为当前世界事实进入 Projection。

Phase7.3 不解析 Stardew-specific 字段，例如：

```text
friendship
weather
conversation
schedule
nearby_npcs
```

这些内容可以以 Adapter 已提供的通用 protobuf / JSON 结构进入模型输入，但 Runtime Core 不按字段名写游戏逻辑。

Current Observation 的事实优先级高于 Recent Memory。ContextProjection 应包含固定 instruction，告诉模型当前 Observation 优先于历史 Memory；Renderer 只负责把这条 instruction 渲染出来。

## 4.7 Recent Memory Projection

Context Engine 消费 `MemoryStore.Recent` 返回的短期 memory，并在 Projection 阶段完成：

```text
未来时间过滤
同一 GameTime + 非零 SourceEventSequence 的稳定排序
按短期 Recent Memory soft limit 做限制
SourceContextFacts 先于 Tool outcomes 渲染
```

本阶段不改变 MemoryStore，不引入持久化 backend，不引入向量检索。

Tool outcome projection 使用通用表达：

```text
tool_name
action_status
arguments
```

不再按 `speak`、`emote`、`present_dialogue`、`face_player` 等具体工具名生成特殊摘要。

`arguments` 使用稳定 key 顺序的 JSON 表达，并按 `MaxToolResultOutputTokens` 等本阶段本地上限做有界投影。这个上限只保护单段输出可读性，不升级为 Phase7.4 的整体 Context Budget。

## 4.8 Current Turn Transcript Projection

Transcript 表示当前 AgentTurn 内模型消息和工具反馈的顺序记录，不等同于世界事实。

```text
ToolCall
    表示模型提出的工具调用意图

ToolResult / ActionResult
    表示 Runtime 或环境返回的工具反馈

Current Observation
    表示重新 observe 后的当前世界状态
    事实优先级高于 Transcript
```

Phase7.3 继续使用现有 `model.Message` 作为 transcript 输入，但 Projection / Renderer 必须保持：

```text
ToolCall 与 ToolResult 关联完整
同一 turn 的后续 step 能看到前面 step 的模型输出和工具结果
ToolResult 输出有本地上限，不把超长结果原样展开
Transcript 不写入 Memory
Transcript 不替代 Experience
```

Transcript 的完整预算裁剪属于 Phase7.4。Phase7.3 只保持当前行为稳定。

## 4.9 Tool View Projection

Context Engine 只消费 Phase7.2 已捕获的 `TurnToolView snapshot`。

Phase7.3 不直接消费 `EnvironmentToolCatalog`，不重新设计 capability bootstrap，也不改变 Scheduler lookup。

Projection 中的 `Tools` 必须与最终 `model.Request.Tools` 同源。模型看到的工具和 Scheduler 可执行工具继续保持一致。

---

# 5. Scope 与失败语义

## 5.1 必要输入

`Engine.Build` 必须收到：

```text
SessionKey
CanonicalTarget
AgentDescriptor
RuntimePolicy
Event
Observation
TurnToolView
```

`TurnToolView` 是值对象。显式 empty `TurnToolView` 合法；Phase7.3 不使用 nil 表达“没有工具”。

`GameDefinition` 和 `AgentDefinition` 可以为 nil，表示 Definition fallback。

`RecentMemories` 和 `Transcript` 可以为空。

## 5.2 Scope 校验

Context Engine 必须校验：

```text
SessionKey.game_id / world_id / entity_id 非空
CanonicalTarget.entity_id == SessionKey.entity_id
AgentDescriptor.SessionKey == SessionKey
AgentDescriptor.definition_id == CanonicalTarget.definition_id
Event.world_id == SessionKey.world_id
Event.target_entity_id == SessionKey.entity_id
Observation.world_id == SessionKey.world_id
Observation.entity_id == SessionKey.entity_id
```

Definition 非 nil 时必须校验：

```text
GameDefinition.game_id == SessionKey.game_id
AgentDefinition.game_id == SessionKey.game_id
AgentDefinition.definition_id == AgentDescriptor.definition_id
AgentDefinition.definition_id == CanonicalTarget.definition_id
```

如果 `CanonicalTarget.definition_id` 为空，则 `AgentDescriptor.definition_id` 必须为空，且 `AgentDefinition` 必须为 nil。

`AgentDefinition == nil` 表示 Definition missing fallback，不跳过 `CanonicalTarget` 与 `AgentDescriptor` 的 definition_id 一致性校验。

`Engine.Build` 不修正、不覆盖 `AgentDescriptor.SessionKey` 或 `AgentDescriptor.definition_id`。输入不一致时直接失败。

## 5.3 失败处理

Scope 或必要输入不一致时：

```text
Engine.Build 返回错误
AgentLoop 不调用 Provider.Generate
AgentLoop 不提交 ActionRequest
TurnCompletion 使用 FAILED
错误信息包含稳定错误原因
```

Definition missing 不属于错误：

```text
GameDefinition nil -> Game Definition 段落渲染 (none)
AgentDefinition nil -> Agent Definition 段落渲染 (none)
```

---

# 6. 预计代码改动

## 6.1 Context Engine

在 `runtime/internal/context` 中建立 Context Engine 主入口。

目标结果：

```text
type Engine struct
func NewEngine(config EngineConfig) Engine
func (e Engine) Build(input BuildInput) (ContextProjection, error)
```

Engine 使用和 Context 投影相关的配置：

```text
MaxRecentMemoryTokens
MaxToolResultOutputTokens
MaxToolResultOutputDepth
MaxToolResultOutputFields
MaxToolResultOutputArrayItems
```

这些配置只服务于现有 memory / transcript 可见投影，不升级为完整 Budget。

## 6.2 Projection Types

在 `runtime/internal/context` 中补齐 Projection 类型。

Projection 类型应能表达：

```text
Definitions
Agent Descriptor
Current Event shell projection
Current Event ContextFact projections
Current Observation
Recent Memory summaries
Current Turn Transcript projection
Tool definitions from TurnToolView snapshot
Authority instruction
```

Projection 类型应保存结构化字段，Renderer 再决定文本展示形式。

## 6.3 Renderer

Renderer 改为消费 `ContextProjection`。

Renderer 输出：

```text
model.Request.System
model.Request.Messages
model.Request.Tools
model.Request.Controls
```

Renderer 继续生成一个主要 user message，并追加 current turn transcript messages。

User message 至少包含：

```text
[Recent Memory]
[Game Definition]
[Agent Definition]
[Agent Descriptor]
[Current Event]
[Current Event Context Facts]
[Current Observation]
[Instruction]
```

`model.Request.Tools` 来自 `ContextProjection.Tools`。

## 6.4 AgentLoop 接线

`AgentLoop.buildModelRequest` 只负责构建并返回 `model.Request`：

```text
resolveDefinitions
Engine.Build
Renderer.Render
return model.Request
```

AgentStep 主循环负责调用 Provider：

```text
model.Request
Provider.Generate
```

每次 `Provider.Generate` 前都通过 `buildModelRequest` 重新 Build Projection。

对于 multi-step turn：

```text
step 1 使用空 Transcript
step 2+ 使用前面 step 累积的 Transcript
TurnToolView 始终使用同一份 snapshot
```

对于 async action resume：

```text
成功 resume 后使用重新 Observe 得到的 Observation
TurnToolView 仍使用本 turn 捕获的 snapshot
Transcript 保留前面 step 的 ToolCall / ToolResult
```

## 6.5 清理 Stardew-specific 渲染

移除 Runtime Core 中按具体 Stardew tool name 生成 memory 摘要的分支。

目标表达从：

```text
said "..."
used emote "..."
presented dialogue "..."
faced player
```

改成通用 Tool outcome 表达，例如：

```text
tool "present_dialogue" status "ACTION_STATUS_SUCCEEDED" arguments {"text":"..."}
tool "emote" arguments {"emote":"happy"}
```

该表达只依赖 tool name、arguments 和 action status，不解释具体游戏行为。

---

# 7. 测试计划

## 7.1 Context Engine 单元测试

新增或调整 `runtime/internal/context` 测试，覆盖：

```text
valid input 可以生成 ContextProjection
missing Event -> build error
missing Observation -> build error
missing CanonicalTarget -> build error
SessionKey 缺 game/world/entity -> build error
CanonicalTarget.entity_id 与 SessionKey.entity_id 不一致 -> build error
Event.world_id 与 SessionKey.world_id 不一致 -> build error
Event.target_entity_id 与 SessionKey.entity_id 不一致 -> build error
Observation.world_id 与 SessionKey.world_id 不一致 -> build error
Observation.entity_id 与 SessionKey.entity_id 不一致 -> build error
GameDefinition.game_id scope mismatch -> build error
AgentDefinition.game_id scope mismatch -> build error
AgentDefinition.definition_id 与 Descriptor.definition_id 不一致 -> build error
CanonicalTarget.definition_id 与 AgentDescriptor.definition_id 不一致 -> build error
CanonicalTarget.definition_id 与 AgentDefinition.definition_id 不一致 -> build error
```

## 7.2 ContextFacts 投影测试

测试要求：

```text
Current Event ContextFacts 独立渲染
Event Projection 不重复包含 context_facts
ContextFact 的 kind / actor / target / scope / text / label / attributes 保留
Runtime 不从 payload 推导 ContextFact
Runtime 不从 Observation.state 推导 ContextFact
```

## 7.3 Memory Projection 测试

测试要求：

```text
未来 GameTime memory 不进入 Projection
同一 GameTime 且 SourceEventSequence 非 0 的 memory 按 sequence 稳定排序
缺少 GameTime 或 sequence 时保持 MemoryStore 返回顺序
SourceContextFacts 在同一条 memory 中先于 Tool outcomes
短期 Recent Memory soft limit 行为保持稳定
Tool outcome 使用通用表达，不按 Stardew tool name 分支
Tool outcome arguments 使用稳定 key 顺序 JSON，并按本阶段本地上限有界投影
```

## 7.4 Renderer 测试

测试要求：

```text
Renderer 从 ContextProjection 生成 model.Request
RuntimePolicy 进入 Request.System
ContextProjection.Tools 进入 Request.Tools
主要 user message 包含稳定 Context 段落
Transcript messages 追加在主要 user message 之后
Renderer 不再执行 scope validation
Renderer 不再执行 memory selection
Renderer 不接触原始 GameEvent / []memory.Record / TurnToolView
Renderer 不调用 TurnToolView.Available、ContextFact extraction 或 tool result normalization
```

## 7.5 AgentLoop 集成测试

测试要求：

```text
Provider request 能看到 Definition / Descriptor / ContextFacts / Observation / Memory / Transcript / Tools
Scope mismatch 时不调用 Provider
Scope mismatch 时不提交 ActionRequest
Definition binding mismatch 时不调用 Provider
Definition binding mismatch 时不提交 ActionRequest
buildModelRequest 只返回 model.Request，不调用 Provider.Generate
multi-step turn 每个 step 前重建 ContextProjection
multi-step turn 复用同一份 TurnToolView snapshot
async resume 后使用新的 Observation 重建 ContextProjection
```

## 7.6 回归测试

推荐命令：

```powershell
go test ./runtime/internal/context
go test ./runtime/internal/agent
go test ./runtime/internal/gateway
go test ./runtime/internal/tool
go test ./runtime/internal/definition
go test ./runtime/internal/memory
go test ./...
```

如果 Phase7.3 只改 Go Runtime，不触碰 Adapter 或 proto 生成，不需要运行 Stardew Adapter C# 测试作为 hard gate。

---

# 8. 验收条件

Phase7.3 代码开发完成后必须满足：

```text
1. Engine.Build 成为 Context Projection 的正式入口。
2. AgentLoop 每次 Provider.Generate 前都通过 Engine.Build 生成 Projection。
3. ContextProjection 是模型可见投影，不是原始 AgentContext 换名。
4. Renderer 只负责把 ContextProjection 渲染成 model.Request。
5. Renderer 不接触原始 GameEvent、[]memory.Record 或 TurnToolView。
6. ContextFacts 独立进入模型上下文。
7. Event Projection 不重复渲染 context_facts。
8. Current Observation 优先于 Recent Memory 和 Transcript 的指令仍稳定存在。
9. Recent Memory 的未来时间过滤仍生效。
10. Recent Memory 的同时间 sequence 排序仍生效。
11. Tool outcome memory 渲染不按 Stardew tool name 写分支。
12. Tool outcome arguments 使用稳定、有界的通用表达。
13. ContextProjection.Tools 从 TurnToolView snapshot 进入 model.Request.Tools。
14. Engine.Build 不直接消费 EnvironmentToolCatalog。
15. buildModelRequest 只返回 model.Request，不调用 Provider.Generate。
16. 任一必要 scope 不一致时，不调用 Provider，不提交 Action。
17. Definition binding 不一致时，不调用 Provider，不提交 Action。
18. Definition missing 继续 fallback，不伪造 Definition。
19. Runtime Core 不解析 Stardew-specific Observation 字段。
20. Runtime Core 不按 GameEvent.payload key 做 Stardew-specific 解析。
21. Phase5 multi-step、Phase6 async action、Phase7.1 Definition、Phase7.2 Tool View 行为不退化。
```

本阶段不以完整 Budget、BuildReport、Tool token admission 或 Stardew 最终实机效果作为验收条件。

---

# 9. Review Checklist

Review Phase7.3 时重点看：

```text
1. 是否真的引入 Engine.Build，而不是继续让 Renderer 选择上下文。
2. ContextProjection 是否是真投影，而不是原始 AgentContext 换名。
3. ContextFacts 是否独立渲染，并且没有在 Event Projection 中重复出现。
4. Renderer 是否只消费 ContextProjection，不接触原始 Event / Memory / TurnToolView。
5. Scope mismatch 是否会阻止 Provider.Generate 和 ActionRequest。
6. Definition binding mismatch 是否会阻止 Provider.Generate 和 ActionRequest。
7. ContextProjection.Tools 是否只来自 TurnToolView snapshot。
8. buildModelRequest 是否只返回 model.Request，不调用 Provider.Generate。
9. Transcript 是否区分 ToolCall 意图、ToolResult 反馈和当前 Observation。
10. Runtime Core 是否移除了 Stardew-specific tool outcome switch。
11. Runtime Core 是否没有新增 Stardew-specific Observation / payload 解析。
12. 是否没有提前实现 Phase7.4 的 Budget / BuildReport。
13. 是否没有提前要求 Phase7.5 的真实 Stardew 实机验收。
```

---

# 10. Implementation Handoff

## M1：Context Engine 与 Projection 骨架

交付：

```text
Engine
BuildInput
ContextProjection
基础 scope validation
Definition binding validation
```

验收点：

```text
valid input build 成功
必要输入缺失 build 失败
scope mismatch build 失败
definition_id binding mismatch build 失败
Definition nil fallback build 成功
```

## M2：ContextFacts 与 Event Projection

交付：

```text
Current Event shell projection
Current Event ContextFacts projection
Event Projection 排除 context_facts
```

验收点：

```text
ContextFacts 独立可见
payload 保持通用 JSON
Runtime 不从 payload / Observation.state 推导 facts
```

## M3：Recent Memory Projection

交付：

```text
把 Renderer 中的 memory timeline selection 移到 Context Engine
保留 MaxRecentMemoryTokens soft limit
替换 Stardew-specific visibleActionSummary
```

验收点：

```text
未来 memory 被过滤
同时间 sequence 排序稳定
tool outcome 渲染通用
tool outcome arguments 稳定且有界
旧 memory 行为核心测试通过
```

## M4：Renderer 简化与 AgentLoop 接线

交付：

```text
Renderer.Render(ContextProjection)
AgentLoop.buildModelRequest 使用 Engine.Build
buildModelRequest 只返回 model.Request
multi-step / async resume 仍按 step 重建 Projection
```

验收点：

```text
Request.System 来自 RuntimePolicy
Request.Tools 来自 ContextProjection.Tools
Transcript 仍进入后续 step
scope mismatch 不调用 Provider
definition_id binding mismatch 不调用 Provider
buildModelRequest 不调用 Provider.Generate
```

## M5：回归与文档同步

交付：

```text
Runtime Go tests 通过
Phase7.3 文档根据实际实现偏差更新
必要时同步 docs/STATUS.md 和 ROADMAP.md
```

验收点：

```text
Phase7.3 验收条件逐条可证明
公开状态不再把已完成的 Tool View snapshot 或 Context Projection 写成未来工作
```

---

# 11. 下一阶段衔接

Phase7.3 完成后，Phase7.4 可以在稳定 Context Projection 的基础上增加：

```text
Selection policy
Context Budget
确定性裁剪
BuildReport
Tool size admission
综合 diagnostics
```

Phase7.4 不重新设计 Phase7.1 的 Definition lookup，也不重新设计 Phase7.2 的 Tool View 生命周期。
