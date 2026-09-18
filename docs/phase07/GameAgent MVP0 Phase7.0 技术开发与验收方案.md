# GameAgent MVP0 Phase7.0 技术开发与验收方案

> **Status:** Accepted
> **Date:** 2026-09-02
> **Phase:** Phase7.0 Context Contract Entry Gate
> **Roadmap Baseline:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md) v1.9
> **Architecture Baseline:** [GameAgent Runtime 整体架构设计规范](../summary/GameAgent%20Runtime%20整体架构设计规范.md) v0.7
> **Context Design Input:** [Context架构设计](../summary/Context/Context架构设计.md) v0.3 Architecture Draft
> **Code Baseline:** `main` @ `e4f3490`
> **Review Result:** Accepted
> **Reviewer:** zlc7
> **Review Date:** 2026-09-02
> **Known Limitations:** Phase7.0 只验收文档、边界和进入 Phase7.1 的条件，不代表 Runtime 代码能力已实现。

---

# 1. 阶段定位

Phase7.0 是 Phase7 的入口闸门，不是 Phase7 Context 子系统的完整开发方案。

本阶段只确认四件事：

1. Phase7 的总体方向已经回到 Context 主链路建设。
2. Runtime / Adapter 的职责边界没有被破坏。
3. 后续 Phase7.1 到 Phase7.5 的拆分清楚，能够分别写方案、分别开发、分别验收。
4. 当前代码事实、阶段规划和 Context 架构草案对同一套边界没有明显冲突。

Phase7.0 的 `Accepted` 只表示可以进入 Phase7.1 技术方案，不表示 Definition、Tool View、Context Engine、Budget 或 Stardew 集成已经完成。

---

# 2. 阶段结论

Phase7 的主线保持为：

```text
Gateway / Session Resolver
        ↓
Resolved AgentSessionKey
Validated Target EntityRef
Current Event
Current Observation
Recent Memory
Current Turn Transcript
Turn Tool View snapshot
        ↓
Context Engine
        ↓
Renderer
        ↓
Model Request
        ↓
Tool Scheduler 使用同一份 Turn Tool View snapshot
```

Phase7.0 只确认这条主线和阶段拆分。后续细节由各自阶段负责：

| 内容 | 负责阶段 |
| --- | --- |
| Canonical / Validated Target EntityRef | Phase7.1 |
| Game Definition / Agent Definition / Instance Descriptor | Phase7.1 |
| Environment-scoped Tool View / Turn Tool View snapshot / 最小工具诊断 | Phase7.2 |
| Context Engine / Renderer 主链路 | Phase7.3 |
| Selection / Budget / BuildReport / 综合诊断 | Phase7.4 |
| Stardew Definition 内容校对、Fake / Recording Provider 和实机验收 | Phase7.5 |

---

# 3. 文档权责

Phase7.0 按以下文档分工执行：

| 文档 | 职责 |
| --- | --- |
| `docs/summary/GameAgent 阶段规划.md` | 承载 Phase7 阶段拆分、依赖、主流程交接和阶段范围。 |
| `docs/phase7/GameAgent MVP0 Phase7.0 技术开发与验收方案.md` | 承载 Phase7.0 单阶段方案，只做入口检查、文档对齐、代码事实核对和进入 Phase7.1 的放行条件。 |
| `docs/summary/Context/Context架构设计.md` | 继续作为长期 Context 语义、Scope、Authority 和演进边界草案，只标注 Phase7 当前采用的规范子集。 |
| `docs/STATUS.md` / `docs/README.md` | 对外说明当前开发重心、已支持能力、未支持能力和阶段文档入口。 |

Phase7.0 不新增阶段文件夹，不新增单独的 Phase7 Context 总纲文件。Phase7.0 到 Phase7.5 的阶段方案继续放在 `docs/phase7/`。

---

# 4. 当前代码事实

以下事实是 Phase7.0 判断后续开发入口的依据。

## 4.1 Context Builder

当前文件：

```text
runtime/internal/context/context.go
runtime/internal/context/renderer.go
```

当前 `Builder.Build` 已经接收：

```text
AgentSessionKey
RuntimePolicy
RecentMemories
GameEvent
Observation
Tools
Transcript
```

当前 `AgentDescriptor` 只有：

```text
EntityID
DefinitionID
```

`DefinitionID` 从 `GameEvent.target_entity_id` 对应的 `EntityRef.definition_id` 提取。现有逻辑按列表顺序返回第一个匹配目标，尚未验证同一个 `target_entity_id` 是否存在冲突的 `EntityRef`。

## 4.2 Renderer

当前 `Renderer.Render` 输出：

```text
System = RuntimePolicy
Messages = user context message + current turn transcript
Tools = ToolDefinition list
Controls = settle
```

当前 user context message 已经包含：

```text
Recent Memory
Agent Descriptor
Current Event
Current Observation
Instruction
```

当前 `ToolResult.output` 已经按结构化方式做字段、深度、数组数量和字节上限控制。Phase7 后续实现需要继续保持结构有效。

当前 `visibleActionSummary()` 仍按 `speak`、`emote`、`present_dialogue`、`face_player` 等具体 Stardew 工具名写分支。该问题归入 Phase7.3。

## 4.3 AgentLoop

当前文件：

```text
runtime/internal/agent/loop.go
```

当前 `AgentLoop` 在每个 AgentStep 调用 `buildModelRequest`。这符合 Phase7 的方向：Context Projection 应该在每次 `Provider.Generate` 前重建。

当前 `buildModelRequest` 从进程级 `tool.Registry` 读取 `Available()`，而 `newToolBatchScheduler` 也继续持有同一个 registry。该问题归入 Phase7.2。

## 4.4 Tool Registry

当前文件：

```text
runtime/internal/tool/registry.go
```

当前 `Registry` 是进程级 map：

```text
map[name]Entry
```

`Entry` 已经包含：

```text
ToolDefinition
Kind
ConcurrencyMode
ExecutionMode
ToolPolicy
```

这说明后续不是缺工具元数据，而是缺 EnvironmentSession 作用域和 AgentTurn 内不可变快照。

当前重复 capability name 会后写覆盖。当前注册阶段只确认 `input_schema_json` 是可解析 JSON，没有完整校验 JSON Schema 支持子集；非法 `tool_policy` 会打印日志并跳过。该问题归入 Phase7.2。

## 4.5 Gateway

当前文件：

```text
runtime/internal/gateway/gateway.go
```

当前 Gateway 在 `Connect` 中完成：

```text
AdapterHello
EnvironmentReady
CapabilityRequest
CapabilityList
GameEvent dispatch
Observation / ActionResult / ActionStatusUpdate routing
```

当前 CapabilityList 直接注册进进程级 `tool.Registry`。Phase7.2 需要将 capability 绑定当前 EnvironmentSession，再形成 Turn Tool View snapshot。

当前 `resolveAgentSessionKey` 已经校验：

```text
event_type 非空
target_entity_id 非空
target_entity_id 存在于 GameEvent.entities
game_id / world_id / entity_id 三要素非空
```

Phase7 后续需要补上目标 `EntityRef` 唯一、无冲突解析，Runtime 不能按列表顺序选择第一个目标实体。

## 4.6 文档现状

当前文档关系：

```text
ROADMAP.md
    公开 Roadmap 入口

docs/STATUS.md
    当前能力和未支持能力入口

docs/summary/GameAgent 阶段规划.md
    Phase7 总体规划和跨阶段共享边界

docs/summary/Context/Context架构设计.md
    长期 Context 架构草案
```

Phase7.0 需要确认这些文档表达同一个 Phase7 方向。

---

# 5. Phase7.0 开发合同

Phase7.0 只冻结“边界和归属”，不冻结后续阶段的实现级细节。

## 5.1 Runtime 与 Adapter 分工

Adapter 负责提供游戏事实和能力：

```text
AdapterHello
CapabilityList
GameEvent
ContextFacts
Observation
ActionResult
ActionStatusUpdate
```

Runtime 负责组合 Context、调用模型和执行工具：

```text
AgentSessionKey 解析
Target EntityRef 校验
Definition 加载
Memory 读取
Tool View snapshot
Context Projection
Renderer
Provider.Generate
Tool Scheduler
Trace / diagnostics
```

Adapter 不拼最终 Prompt，不决定模型应该看到哪些长期记忆，不决定工具预算。Adapter 可以通过 `Observation.state.<game>`、event payload 或 extensions 提供 game-specific structured facts；Runtime Core 不得按具体 game key 或 Stardew 字段写业务分支。

## 5.2 Context 输入清单

Phase7 后续阶段围绕以下输入建设主链路：

```text
Runtime Policy
Game Definition
Agent Definition
Agent Instance Descriptor
Current Event
Current Event ContextFacts
Current Observation
Recent Memory
Current Turn Transcript
Turn Tool View
```

Phase7.0 只确认清单完整。字段、格式、加载器、预算和失败语义由对应 Phase7.x 技术方案落地。

## 5.3 可信关系

Phase7 采用以下基本关系：

```text
Runtime Policy
    约束 Runtime 执行规则、Tool 使用规则和全局输出规则。

Game Definition
    描述稳定游戏规则、世界观和全局叙事边界。

Agent Definition
    描述稳定角色身份、人格、说话风格和行为倾向。

Current Observation
    描述当前游戏事实，优先于历史 Memory。

Current Event / ContextFacts
    描述 Adapter 在本 Turn 中显式上报的触发事实和输入事实。

Current Turn Transcript
    记录本 Turn 内的模型决策、ToolCall 请求和执行反馈。

ToolCall
    只表示模型计划或 Runtime 请求执行的动作，不证明 Environment effect 已经发生。

ToolResult / ActionResult / 更新后的 Current Observation
    用来确认动作结果或当前世界事实。
```

更细的裁剪、冲突和报告规则由 Phase7.1 到 Phase7.4 分别细化。

## 5.4 生命周期边界

Phase7 总体规划中必须区分以下生命周期：

```text
AgentSession identity
Definition Catalog
Environment Tool Catalog
Turn Tool View
Context Projection
```

Phase7.0 只检查这些生命周期是否在总阶段规划中有明确归属。具体结构和代码接线由后续阶段负责。

## 5.5 后续阶段职责

| 阶段 | 主要职责 | Phase7.0 验收口径 |
| --- | --- | --- |
| Phase7.1 | Canonical Target EntityRef、Definition Sources 与 Instance Descriptor | 能独立写出技术方案，不依赖 Adapter 拼 prompt。 |
| Phase7.2 | Environment-scoped Tool View 与最小工具诊断 | 能独立解决模型可见工具和 Scheduler 可执行工具一致性。 |
| Phase7.3 | Context Engine Core | 能独立替换临时 prompt 拼装，形成结构化 Context Projection。 |
| Phase7.4 | Selection、Budget 与综合 Observability | 能独立定义预算、裁剪和报告，不重新设计 Tool View 生命周期和 lookup 语义，也不要求 Phase7.1 提前依赖 BuildReport。 |
| Phase7.5 | Stardew Context Integration 与验收 | 能用真实 Stardew 对话验证 Phase7 主链路，并完成 Definition 内容校对。 |

---

# 6. 非目标

Phase7.0 不做：

```text
GameDefinitionSource 代码实现
AgentDefinitionSource 代码实现
Environment-scoped Tool Catalog 代码实现
Turn Tool View 代码实现
ContextEngine.Build 代码实现
Budget Manager 代码实现
Persistent Memory backend
Vector retrieval
Semantic compression
Provider-specific tokenizer
Adapter reconnect
Definition hot reload
新增 Protocol 字段
Stardew Adapter 行为调整
```

Phase7.0 不修改：

```text
protocol/
runtime/
adapters/
scenarios/
```

---

# 7. 开发里程碑

## M0：Roadmap 与公开入口对齐

目标：

```text
确认 docs/summary/GameAgent 阶段规划.md 是 Phase7 阶段拆分、依赖、主流程交接和阶段范围的事实源。
```

通过标准：

- 总阶段规划包含 Phase7.0 到 Phase7.5 的拆分。
- 总阶段规划包含主流程交接和跨阶段共享边界。
- Phase7 主流程中的每个交付物都有唯一负责阶段。
- Canonical / Validated Target EntityRef 不成为无 Owner 的前置能力。
- 总阶段规划没有要求一次性写完所有 Phase7.x 详细方案。
- `ROADMAP.md` 的 Now 指向 Phase7 Context Subsystem。
- `docs/STATUS.md` 没有提前声称 Definition-backed context 已实现。
- `docs/README.md` 包含 Phase7 阶段文档入口。

## M1：Phase7.0 文档瘦身

目标：

```text
Phase7.0 文档回到入口闸门定位。
```

通过标准：

- 文档可以单独解释 Phase7.0 的阶段定位。
- 文档不展开 7.1 到 7.5 的完整实现级规则。
- 文档明确本阶段不实现 Phase7.1 之后的代码能力。

## M2：当前代码事实确认

目标：

```text
记录 Phase7 后续开发将触碰的真实入口。
```

通过标准：

- Context Builder / Renderer 现状清楚。
- AgentLoop 每步重建 Model Request 的现状清楚。
- Tool Registry 当前进程级作用域问题清楚。
- Gateway 当前目标实体校验和 capability 注册现状清楚。

## M3：文档权责确认

目标：

```text
确认阶段规划、Phase7.0 文档、Context 架构草案之间的职责边界。
```

通过标准：

- `GameAgent 阶段规划.md` 负责总体拆分和共享规则。
- Phase7.0 文档负责入口检查和进入 7.1 的放行条件。
- `Context架构设计.md` 保持长期 Draft，只标注 Phase7 当前采用的规范子集。

## M4：Review Gate

目标：

```text
用户 review 通过后，进入 Phase7.1 技术方案。
```

通过标准：

- Phase7.0 文档状态可以从 `Implementation Plan Draft` 升级为 `Accepted`。
- 后续开发顺序保持为先写单个 Phase7.x 技术方案，再开发该 Phase7.x。
- Architecture / Roadmap review 结论记录为 `Accepted`，并注明 reviewer、日期和已知限制。

---

# 8. 验收检查

文档一致性检查：

```powershell
rg -n "入口闸门|文档权责|后续阶段职责|非目标|Phase7.1|Phase7.2|Phase7.3|Phase7.4|Phase7.5" "docs/phase7/GameAgent MVP0 Phase7.0 技术开发与验收方案.md"
```

总阶段规划检查：

```powershell
rg -n "Phase7.0|Phase7.1|Phase7.2|Phase7.3|Phase7.4|Phase7.5|主流程交付物归属|诊断边界|CapabilityList.entity_id|本阶段不以最终 Model Request" "docs/summary/GameAgent 阶段规划.md"
```

非目标存在性检查：

```powershell
rg -n "GameDefinitionSource 代码实现|AgentDefinitionSource 代码实现|Turn Tool View 代码实现|ContextEngine.Build 代码实现|Budget Manager 代码实现|Stardew Adapter 行为调整" "docs/phase7/GameAgent MVP0 Phase7.0 技术开发与验收方案.md"
```

本阶段只改文档，不运行 Go 测试。

代码目录变更范围检查：

```powershell
git status --short -- runtime protocol adapters scenarios
```

提交前格式检查：

```powershell
git diff --check
```

---

# 9. Phase7.0 完成条件

Phase7.0 可以提交 review 的最低条件：

1. Phase7.0 文档保持入口闸门定位。
2. Phase7.1 到 Phase7.5 的职责拆分清楚。
3. 总阶段规划继续承载 Phase7 共享边界和总体流程。
4. 当前代码事实带有明确 code baseline，足够支撑后续阶段写技术方案。
5. 非目标清单明确，不把后续代码实现混进 Phase7.0。
6. `ROADMAP.md` 的 Now 指向 Phase7 Context Subsystem。
7. `docs/STATUS.md` 没有提前声称 Definition-backed context 已实现。
8. `docs/summary/Context/Context架构设计.md` 保持 Draft，并明确 Phase7 Normative Subset。
9. Canonical / Validated Target EntityRef、Tool diagnostics、Definition fallback 和 Transcript 语义没有责任空洞。
10. 用户 review 通过后再标记 `Accepted`。

---

# 10. 下一阶段衔接

Phase7.0 review 通过后，进入 Phase7.1。

| 阶段 | 技术方案重点 |
| --- | --- |
| Phase7.1 | Canonical Target EntityRef、Definition Sources、Game Definition、Agent Definition、Agent Instance Descriptor、最小 Stardew Definition fixtures。 |
| Phase7.2 | Environment Tool Catalog、Turn Tool View snapshot、模型可见工具与 Scheduler 可执行工具一致性、最小 bootstrap / consistency diagnostics。 |
| Phase7.3 | Context Engine、Renderer、Context Projection、Stardew-specific prompt 泄漏清理。 |
| Phase7.4 | Context 选择、预算、裁剪、BuildReport、Tool size admission 和综合 observability。 |
| Phase7.5 | Stardew Definition 内容校对、真实对话链路、Fake / Recording Provider 验收、阶段小结。 |

---

# 11. Review 关注点

Review Phase7.0 时重点看：

1. 7.0 是否只是入口闸门。
2. 7.1 到 7.5 是否各自有清楚职责。
3. 总阶段规划是否仍然承载 Phase7 的共享规则。
4. Runtime / Adapter 分离是否保持清楚。
5. 当前代码事实是否足够支撑 Phase7.1 开工。
6. 是否还有后续阶段的详细实现规则被放进 7.0。
