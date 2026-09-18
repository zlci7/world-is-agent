# GameAgent 阶段规划

> **Public Documentation Note (2026-09-01):** [docs/STATUS.md](../STATUS.md) 是当前公开能力状态入口。本文保留为阶段规划、阶段验收和内部开发节奏资料。
>
> **Positioning Note (2026-09-18):** WIA MVP0 已定型，当前工作方向是固定该版本并完成开源产品化，见根目录 `AGENTS.md`。本文作为阶段基线与内部排期记录保留；Phase9 是最后一个已完成阶段，后续阶段划分不构成继续开发的授权，当前迭代与交付流程以 `AGENTS.md` 为准。
>
> **Version:** v1.15
> **Status:** Roadmap Baseline
> **Date:** 2026-09-10
> **Architecture Baseline:** GameAgent Runtime Architecture v0.7
> **Current Baseline:** Phase1 Accepted + Phase2 Accepted + Phase3 Accepted + Phase4 Accepted + Phase5 Accepted + Phase5.5 Accepted + Phase5.6 Accepted + Phase6 Accepted + Phase6.5 Accepted + Phase7.0 Accepted + Phase7.1 Accepted + Phase7.2 Accepted + Phase7.3 Accepted + Phase7.4 Accepted + Phase8.1 Accepted + Phase8.2 Accepted + Phase8.3 Accepted；代码基线 `main` @ `daf4f98`，保留各阶段验收限制
> **Revision Source:** 评审意见（Roadmap Review，2026-08-18）；Phase3 评估（Protocol v1alpha2 Decision，2026-08-20）；[多游戏兼容性与 Agent Binding 决策](./GameAgent 多游戏兼容性与 Agent Binding 决策.md)（2026-08-22）；[Stardew Adapter 方案对比](../adapter/Stardew Adapter 方案对比.md)（2026-08-27）；[Phase6 Async Action Protocol Strategy ADR](../phase06/GameAgent MVP0 Phase6 Async Action Protocol Strategy ADR.md)（2026-08-31）；[Phase6.5 Stardew Dialogue Interaction Convergence](../phase06/GameAgent MVP0 Phase6.5 技术开发与验收方案.md)（2026-09-02 Accepted）；GameAgent 阶段规划 v1.1 评审意见（2026-09-02）；Phase7 Context Subsystem Replan（2026-09-02）；Phase7 Contract Review（2026-09-02）；Phase7 Baseline Candidate Review（2026-09-02）；Phase7 Roadmap Baseline Freeze（2026-09-02）；Phase7.0 Contract Revision（2026-09-02）；Phase7.0 Gate Scope Correction（2026-09-02）；Phase7.0 Minor Review Correction（2026-09-02）；Phase7.0 Over-scope Guard Correction（2026-09-02）；Phase7.3 Implementation Acceptance（2026-09-04）；Phase7.4 Code Acceptance（2026-09-06，`main` @ `e50794c`）；**Phase10 范围重排（2026-09-18）：Phase10 改为 Ecosystem & Productization，原 Phase10 Environment Reconnect 顺延为 Phase11，原 Phase11 Evaluation/DX/产品化 顺延为 Phase12**
>
> **Phase Numbering Change (2026-09-18):** 原 Phase10（Environment Reconnect and Capability Recovery）与 Phase11（Evaluation、Developer Experience 与产品化）均未开工，因此顺延为 Phase11 与 Phase12，范围与完成条件不变。新的 Phase10 承载 Ecosystem & Productization（第三方 mod 能力接入、桌面客户端产品化、第二个真实 Adapter），技术方案见 [Phase10 技术开发与验收总方案](../phase10/GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md)。本文件第 17、18 节即顺延后的原 Phase10、Phase11。

---

# 1. 文档定位

本文档用于初步划分 Phase3 及后续阶段的核心目标和边界。

它只回答：

> **后续每个阶段主要验证哪一项新的 GameAgent 架构能力？**

本文档不提前规定具体文件、接口、协议字段、测试命令、技术选型或阶段内部开发顺序。这些内容应在进入对应阶段前，由独立的《PhaseN 技术开发与验收方案》重新设计和确认。

对于 Phase7 这类 umbrella phase，本文可以承载跨 Phase7.x 共用的语义边界和阶段归属；具体接口、对象命名、报告字段和算法步骤仍由对应 Phase7.x 技术方案决定。

每个阶段完成并验收后，必须重新 Review：

- 当前实现事实是否仍符合 Architecture v0.7；
- 下一阶段是否仍是最高优先级；
- 后续阶段是否需要合并、拆分或调整顺序；
- 是否需要形成新的 Architecture Decision。

因此，本 Roadmap 是阶段方向，不是不可修改的长期排期承诺。

---

# 2. 当前基线

## Phase1：真实 One-Turn Vertical Slice

状态：`Accepted`

已经跑通：

```text
真实 Stardew GameEvent
→ Runtime Observe
→ 真实 LLM ToolCall
→ ActionRequest
→ 真实游戏 speak
→ ActionResult
```

Phase1 确立了 Runtime、Protocol、Adapter、Provider 和 Tool 的基础边界。

## Phase2：最小 AgentTurn Runtime 工程化

状态：`Accepted`

已经完成：

```text
Turn observability
Prompt / timeout config
Failure convergence
Dynamic Capability → Tool
speak + emote
Action timeout + best-effort cancel
```

Phase2 将系统从“能跑通一轮”升级为一个可观察、可配置、失败可收敛、可扩展简单 Tool 的最小 AgentTurn Runtime。

## Baseline Evidence

Accepted 状态的依据不在本文重复展开，以下文档作为当前 Roadmap 的证据入口：

- [GameAgent MVP0 Phase1 技术开发与验收方案](../phase01/GameAgent MVP0 Phase1 技术开发与验收方案.md)
- [GameAgent MVP0 Phase1 工程设计规范](../phase01/GameAgent MVP0 Phase1 工程设计规范.md)
- [GameAgent MVP0 Phase2 技术开发与验收方案](../phase02/GameAgent MVP0 Phase2 技术开发与验收方案.md)
- [GameAgent MVP0 Phase2 Trace 链路观测设计](../phase02/GameAgent MVP0 Phase2 Trace 链路观测设计.md)
- [GameAgent MVP0 Phase5 技术开发与验收方案](../phase05/GameAgent MVP0 Phase5 技术开发与验收方案.md)
- [GameAgent MVP0 Phase8.2–8.3 开发与验收记录](../phase08/GameAgent MVP0 Phase8.2-8.3 开发与验收记录.md)
- [GameAgent Runtime 整体架构设计规范](./GameAgent Runtime 整体架构设计规范.md)

---

# 3. 后续阶段划分原则

## 3.1 每个可验收单元只证明一个主要架构跃迁

后续阶段以可独立验收的 Phase / Phase.x 控制复杂度。Phase7 可以作为 Context 主题阶段，但必须拆成多个 Phase7.x；每个 Phase7.x 只完成一条清楚、可测试、可回退的主链路。

## 3.2 新 Capability 只作为架构验证手段

功能数量不是阶段目标。每阶段只增加验证该阶段架构所需的最小 Capability，例如：

```text
Phase3：用简单 Action 验证 Adapter 泛化。
Phase5.6：用对话 UI 与 ContextFact 验证玩家输入到 AgentTurn / Recent Memory 的闭环。
Phase6：用 Tool Policy、ActionRequest source correlation、TurnCompletion、Interaction Guard 和 move_to 验证异步 Action lifecycle 与 Turn resume。
Phase6.5：用 Stardew Tool View 收敛、present_dialogue 默认输入、source-time gate、in-flight gate 和 waiting menu ActionRequest 边界验证玩家点击 NPC 的稳定对话体验。
Phase7.1：用少量 Stardew NPC Definition 验证角色定义真正进入模型输入。
Phase7.2：用当前 EnvironmentSession 的 Tool View snapshot 验证模型看到的工具与实际执行的工具一致。
Phase7.5：用真实 Stardew 对话验收 Context 主链路。
Phase8.1：用 SQLite Recent Memory 验证持久身份、幂等与近期记忆读取。
Phase8.2：用终态 History 与有界 Summary 验证跨 Turn、跨 Runtime 的历史连续性。
Phase8.3：用历史原文检索与可选留存清理验证细节召回和删除后的能力边界。
Phase9：用 Runtime TaskService、Runtime Tools、跨天唤醒与最小检查点，完成预约、赴约、见面或超时离开及结果记忆的闭环。
Phase10：用第三方 mod 能力接入、桌面客户端与第二个真实 Adapter 验证生态扩展与产品化。
Phase11：用 reconnect、capability replacement 和 pending operation 收敛验证 Environment Recovery。
```

## 3.3 每阶段结束后重新规划

Phase7 及后续阶段属于当前可调整范围。上一阶段结束后，可以根据实际代码和验收结果调整后续阶段，但不得静默破坏 Architecture v0.7 的核心边界。

---

# 4. 阶段总览

| 阶段 | 核心主题 | 主要验证目标 |
| --- | --- | --- |
| Phase3 | Agent Identity 与 Adapter 泛化 | 同一实体具有稳定身份；Stardew Adapter 不再局限于单 NPC |
| Phase4 | Context 与短期 Memory | Agent 可以在多个 Turn 之间保留隔离的上下文，并形成可复用确定性测试底座 |
| Phase5 Entry Gate | Multi-game Compatibility / Agent Binding | Phase5 开工前先冻结 Entity、Agent Definition、Agent Instance 的长期语义 |
| Phase5 | 有界 Multi-step AgentTurn | 一个 Turn 可以包含多个有界 AgentStep；单 Step 可包含 ordered ToolCall batch |
| Phase5.5 | Stardew Adapter Context Enrichment | Stardew Adapter 通过 Observation narrow waist 提供成熟的游戏当前事实 |
| Phase5.6 | Stardew Dialogue Interaction Surface | 对话会话可以跨 Turn 延续；玩家回复事件、ContextFact、同步 UI 和 Recent Memory 能进入 Runtime 闭环 |
| Phase6 | Tool Policy、Action Source、TurnCompletion、异步 Action 与 Turn Resume | Runtime 不按具体工具名执行特殊规则；Adapter 能把 Action 绑定回触发事件并释放 Turn 等待态；长时间 Action 不被建模为同步函数；Turn 可以等待并恢复 |
| Phase6.5 | Stardew Dialogue Interaction Convergence | Stardew 玩家点击 NPC 默认进入稳定对话域；生产 Tool View 收敛到 present_dialogue / emote / face_player / move_to；自由输入、重复点击、waiting menu 和对话结束语义稳定 |
| Phase7 | Context Subsystem Completion | 以 Phase7.0–Phase7.5 拆分，让角色定义、游戏定义、Context 投影、Tool View snapshot 和预算渲染进入 Runtime 主链路 |
| Phase7.0 | Context Contract Entry Gate | 确认 Phase7 总体边界、文档权责、阶段拆分和进入 Phase7.1 的验收口径 |
| Phase7.1 | Definition Sources 与 Instance Descriptor | Runtime 完成 canonical Target EntityRef，按 `game_id` 加载 Game Definition，按 `game_id + definition_id` 加载 Agent Definition，实例信息复用 `EntityRef` 与 `AgentSessionKey` |
| Phase7.2 | Environment-scoped Tool View | 每条 EnvironmentSession 形成 Tool Catalog，每个 AgentTurn 捕获最终 Tool View snapshot，模型看到的工具与实际执行的工具一致，并记录最小工具诊断 |
| Phase7.3 | Context Engine Core | 将 Event、ContextFacts、Observation、Memory、Transcript、Definition 和 Tool View 组合成结构化投影 |
| Phase7.4 | Selection、Budget 与 Observability | 按优先级选择上下文，稳定裁剪，完成 Tool size admission、BuildReport 和综合诊断 |
| Phase7.5 | Stardew Context Integration 与验收 | 用 Stardew Definition 内容校对、Fake / Recording Provider 和实机对话验收 Context 主链路 |
| Phase8 | Persistent Memory, History & Context Compaction | 持久来源与模型输入分离，分 8.1/8.2/8.3 独立验收 |
| Phase8.1 | SQLite Recent Memory | 有限 Recent 持久化、身份隔离、幂等与重启读取；Accepted |
| Phase8.2 | Persistent Session History & Context Compaction | 完整约定终态历史、历史摘要与近期原文，恢复提交的检查点；Accepted，保留验收限制 |
| Phase8.3 | History Retrieval & Retention | 中文字面检索、来源片段与默认关闭的原文清理；Accepted，保留验收限制 |
| Phase9 | Runtime Durable Task & Appointment Vertical Slice | Runtime Tools 暴露持久任务意图，TaskService 驱动跨天唤醒与结果收敛；真实游戏效果、结果记忆和检查点构成最小闭环 |
| Phase10 | Ecosystem & Productization | 第三方 mod 能力可被 agent 自主调用；桌面客户端让外部用户能装起来用并看见过程；第二个真实 Adapter 验证跨游戏泛化 |
| Phase11 | Environment Reconnect and Capability Recovery | Adapter reconnect、EnvironmentSession 重建、capability replacement 和 pending operation 收敛 |
| Phase12 | Evaluation、Developer Experience 与产品化 | 系统可重复评估、定位、交付，并支持新 Adapter 接入 |

---

# 5. Phase3：Agent Identity 与 Stardew Adapter 泛化

## 阶段目标

将当前面向单一 NPC 的 Adapter 验证，升级为具有稳定身份边界、支持多个 NPC 和多个简单能力的 Stardew Environment Adapter。

本阶段主要证明：

> **Runtime 不理解具体 NPC，也能稳定控制同一 Environment 中的多个实体。**

## 主要范围

- 明确 EnvironmentSession 与 AgentSession identity 的区别；
- 建立稳定 entity identity / AgentSession resolution contract，并形成 P0 Agent Identity Contract；
- GameEvent 携带目标 entity 信息，Runtime 通过 identity contract 解析并路由到对应 AgentSession；
- Protocol 层接受 Phase3 升级到 `gameagent.protocol.v1alpha2`，一次性引入 typed `target_entity_id` 与消息级 `world_id` 贯穿，避免 WorldScope 双来源和后续二次协议迁移；
- 同一 AgentSession 同时只允许一个 active Turn，冲突处理策略留待 Phase 技术方案确定；
- 将 NPC 交互从单一目标泛化到多个 NPC；
- 扩展少量必要且稳定的 Observation 当前事实；
- 补强 ProtocolMapper、ObservationBuilder、RuntimeClient 等 Adapter 边界测试；
- 按需增加少量简单、短时、可观察 Capability。

## P0 Mandatory Deliverable：Agent Identity Contract

Phase3 结束前必须形成 Agent Identity Contract。该 contract 冻结逻辑身份组成和不变量，不冻结具体字符串编码、数据库主键或 UUID 方案。

推荐逻辑模型：

```text
AgentSessionIdentity
=
GameScope
+
WorldScope
+
StableEntityIdentity
```

解释：

```text
GameScope
    当前游戏命名空间，例如 game_id。

WorldScope
    当前存档或世界身份。Phase3 技术方案已将 Protocol 术语收敛为 world_id。

StableEntityIdentity
    Adapter 在该世界内提供的稳定、opaque entity_id。
```

必须保持：

```text
session_id
    MUST NOT 参与 AgentSession identity。

display_name
    MUST NOT 参与 AgentSession identity。

本地化名称
    MUST NOT 参与 AgentSession identity。
```

`entity_type` 是否纳入最终 identity 编码不在 Roadmap 层冻结；若 Adapter 无法保证 `entity_id` 在 WorldScope 内跨类型唯一，Phase3 技术方案应明确是否把 `entity_type` 纳入解析规则。

P0 还必须包含 AgentSessionResolver 的最小实现或等价可测试解析逻辑。否则 identity contract 只能停留在文档层，无法证明事件能够稳定路由到同一个 AgentSession。

最低验收不变量：

| 输入变化 | 预期 |
| --- | --- |
| 相同 game、world/save、entity，多次解析 | 同一 identity |
| entity 不同 | identity 不同 |
| world/save 不同 | identity 不同 |
| EnvironmentSession / session_id 不同 | identity 不变 |
| display_name 或语言变化 | identity 不变 |
| 相同 display_name、不同 entity_id | identity 不同 |

## 非目标

```text
长期 Memory
Multi-step ReAct
复杂异步 movement
自动 reconnect（保持 Phase11 的 Environment Recovery 范围）
Event replay
复杂 Permission
大量 Stardew 功能覆盖
```

## 完成条件

- 多个 NPC 可以进入同一条 Runtime AgentTurn 链路；
- Runtime 不需要为具体 NPC 增加分支；
- Agent Identity Contract 已验收，并覆盖最低身份不变量；
- GameEvent 目标实体可以解析并路由到对应 AgentSession；
- 同一 AgentSession 不会同时运行多个 active Turn；
- 稳定 entity identity 足以成为未来 AgentSession 的身份基础；
- 新增简单 Capability 时，Runtime 继续通过动态 Tool Registry 感知；
- Adapter 的关键映射和结果 contract 有自动测试。

## 阶段结束 Review

重点确认身份模型是否足以进入 Memory 阶段，以及 Runtime 是否出现任何 Stardew-specific 泄漏。

---

# 6. Phase4：Context 与短期 Memory

## 阶段目标

让同一个 AgentSession 在多个 AgentTurn 之间保留轻量上下文，并证明不同实体之间的状态不会串线。

本阶段主要回答：

> **Agent 第二次被唤醒时，能否使用第一次 Turn 留下的相关信息？**

## 主要范围

- 建立最小 AgentSession state boundary；
- 实现轻量、可替换的 MemoryStore；
- 在 Turn 进入终态（completed / failed）后，将有限的 recent turns 或 episodic facts 写入 MemoryStore，与 Trace Recorder 解耦；
- 在 Model Request 中组合 Trigger Event、Observation、Recent Context 和 Tools；
- 为 context loaded / context updated 增加必要观测；
- 将现有 fake adapter / fake Environment 收敛为可复用的确定性测试夹具，用于验证多 Entity、多 Turn、Memory 隔离和失败路径。

第一版默认使用 In-Memory Store；如为开发调试使用简单本地文件实现，不承诺跨进程恢复、版本兼容或 Environment Recovery；正式 Persistent Recent Memory 属于 Phase8，Environment Recovery 属于 Phase11。

## 非目标

```text
向量数据库
复杂长期人格系统
Memory Reflection Agent
Knowledge Graph
复杂摘要与压缩
Multi-step AgentTurn
完整 MiniWorld
Scenario Evaluation Framework
```

## 完成条件

- 同一 NPC 的后续 Turn 可以引用前一次相关信息；
- 不同 NPC 的 Memory 不会串线；
- Memory 绑定 AgentSession，而不是 EnvironmentSession；
- 关闭 Memory 后，现有 One-Turn 链路仍能正常运行；
- Trace 能说明本轮是否加载和更新了 Context；
- 确定性测试夹具可以脚本化多 Entity、多 Turn、Observation、ActionResult 和基础失败路径。

## 阶段结束 Review

重点确认 AgentSession identity 是否稳定、MemoryStore 是否足够小、Context 构造是否已经需要独立模块，确定性测试夹具是否足以支撑 Phase5，以及 Phase5 前是否需要先冻结多游戏兼容性语义。

---

# 7. Phase5 Entry Gate：Multi-game Compatibility / Agent Binding

## Gate 目标

Phase5 会扩展 AgentTurn 内部结构，引入多个 AgentStep。

在进入这个阶段前，必须避免继续放大 Stardew-only 假设：

```text
Agent Definition = game_id + entity_id
```

因此 Phase5 开工前必须接受并引用：

```text
docs/summary/GameAgent 多游戏兼容性与 Agent Binding 决策.md
```

## Gate 冻结语义

```text
Entity != Agent Definition

Entity -> Agent Binding

Agent Definition / Archetype = game_id + definition_id

Agent Instance Descriptor = game_id + world_id + entity_id

Agent State / Memory = game_id + world_id + entity_id

Observation = small common envelope + game-specific state/extensions

Available Tools = current AgentTurn dynamic capability view

Trigger admission is not hardcoded to one game-specific event_type
```

## 非目标

本 Gate 不要求立即实现：

```text
完整 AgentBinding runtime package
AgentDescriptor protocol message
第二个真实 Adapter
Agent Definition storage
ActionBatchRequest / ActionBatchResult
```

Phase5 技术方案已接受最小 additive Protocol 更新：`EntityRef.definition_id` 与 `Capability.concurrency_mode`。`definition_id` 的协议来源是 `EntityRef.definition_id`，不新增 `Observation.definition_id` 或 `target_definition_id`。

Phase5 Entry Gate 必须产出最小 FakeGame / non-Stardew fixture contract test。该测试不要求真实第二 Adapter，也不要求新增 game-specific protocol 字段；它只需要证明非 Stardew 语义的 trigger 可以通过 Runtime core，并且不需要为了该 fixture 修改 `runtime/internal/{agent,context,tool,model,session}` 的 game-specific 分支。

## 完成条件

- Architecture v0.7 已纳入 Agent Binding / Definition / Instance 分离；
- Context Architecture v0.2 已更新 Scope Contract；
- Phase5 技术方案不再默认 `definition_id == entity_id`；
- Phase5 技术方案在 AgentContext / AgentDefinition / Context Source 的签名、字段或数据源说明中显式区分 `definition_id` 与 `entity_id`；若 Phase5 仍不实现 AgentDefinition source，也必须写明当前降级策略不依赖 `definition_id == entity_id`；
- Phase5 技术方案不要求 Runtime Core 理解具体游戏 Observation 字段；
- Phase5 技术方案把 Tools 视为本次 AgentTurn 的动态 capability view；
- Phase5 技术方案必须明确 Trigger Admission / Trigger Router 的最小策略，AgentLoop / Gateway core 不得继续把单一 game-specific `event_type` 作为长期准入条件；
- Phase5 Entry Gate 必须包含最小 non-Stardew fixture / contract test，覆盖至少一个非 `player_interacted_with_npc` trigger。

---

# 8. Phase5：有界 Multi-step AgentTurn

## 阶段目标

将当前：

```text
1 Turn = 1 Model Call + 1 Tool / Action
```

扩展为：

```text
1 Turn = N AgentSteps

1 AgentStep = 1 ModelDecision
            + 0..N ToolCalls
            + 0..N ToolResults
            + optional AgentTurn Control
```

同时保持明确的最大步数、总 timeout、失败语义和 Trace 边界。

## 主要范围

- 正式引入 AgentStep 概念；
- Runtime model contract 从单 `Response.ToolCall` 迁移到 `ModelDecision`；
- 一个 Step 可以包含 ordered ToolCall batch；
- ToolResult transcript 可以进入下一次 Model Request；
- 设置 `max_steps` 和 Turn 全局上限；
- 每个 Step 具有顺序、ToolCalls 和结果观测；
- Tool Scheduler 支持 `Sequential / ParallelSafe`；
- Tool 失败可以在有限范围内由模型修正；
- 明确 AgentTurn 的正常结束语义（settle）：`settle` 是 AgentTurn control，不是 Environment Tool；
- 一个 Turn 仍然只有一个最终 terminal result。

本阶段优先只使用短时、可快速返回结果的 Tool，不同时引入长时间异步 Action。

## 非目标

```text
无限 ReAct
复杂 Planner
Sub-agent / Supervisor
长时间 move_to suspend / resume
跨进程 continuation recovery
ActionBatchRequest / ActionBatchResult
```

## 完成条件

- 一个 AgentTurn 可以稳定执行至少两个 AgentSteps；
- 每个 Step 都能在同一 `turn_id` 下被追踪；
- AgentTurn 可以在未达到 `max_steps` 时通过正常结束语义收敛为唯一终态，而不是只能依赖 `max_steps` / timeout 被动终止；
- 单 Step 可以执行多个 ToolCall，并按原始 ToolCall order 返回 ToolResult；
- 超过最大步数时 Turn 明确收敛；
- ToolResult 能以 provider-neutral 方式进入下一次模型请求；
- 单步模式仍保持兼容。

## 阶段结束 Review

重点确认 AgentTurn Core 是否仍清晰、Step retry 是否有明确上限，以及异步 Action 是否有自然插入位置。

---

# 9. Phase5.5：Stardew Adapter Context Enrichment

## 阶段目标

定义 Stardew Adapter 的正式当前事实模型，使 Runtime 在不理解 Stardew 字段含义的前提下获得更完整、结构化、可测试的游戏上下文。

本阶段主要回答：

> **Adapter 能否通过通用 Observation narrow waist 提供成熟的游戏当前事实，而不把 Stardew-specific 语义泄漏进 Runtime Core？**

## 主要范围

- 在 Stardew Adapter 内建立稳定的 `StardewObservation` 生产模型；
- `Observation.state` 使用 `stardew` 命名空间承载 game-specific 当前事实；
- 补充 ValleyTalk 已验证对对话质量关键的当前事实：时间、季节、日期、天气、地点、玩家/NPC 位置、关系、附近 NPC、当前触发、可获得的日程摘要；
- 更新 Stardew capability metadata，使 `speak` 与 `emote` 的环境效果描述明确；
- 增加 Adapter mapper / observation builder 测试，证明结构化字段稳定输出；
- 增加 Runtime context renderer 的 game-specific nested state 回归测试，证明 Runtime 只做通用渲染，不读取 Stardew 字段。

本阶段的核心产物是 Adapter Context Source，不是新的 Runtime cognition 能力。

## 非目标

```text
Protocol 字段变更
Runtime Stardew-specific parser
AgentDefinition store
Agent biography / traits / relationships 加载
原版台词 sample retrieval
长期事件记忆持久化
ValleyTalk prompt builder 迁移
gift / typed response 等新触发入口的完整接入
move_to / async Action lifecycle
```

## 完成条件

- Stardew Adapter 输出的 `Observation.state.stardew` 至少包含 time、weather、agent、player、relationship、scene、schedule 七类结构化事实；
- Runtime Core 不新增任何 Stardew-specific 类型、字段判断或分支；
- 生产路径的 canonical observation schema 为 `StardewObservation` 与 `Observation.state.stardew`；
- `EntityRef.definition_id` 继续只表示 Agent Definition 绑定 key，Observation 不承载 Agent Definition 模板内容；
- Adapter 测试覆盖 namespaced state、relationship hearts、weather flags、nearby NPCs、schedule summary 和 capability descriptions；
- Runtime context renderer 测试覆盖 nested game-specific state 的稳定渲染；
- Phase5 multi-step / tool transcript / memory 行为不因 observation 结构增强而退化。

## 阶段结束 Review

重点确认 Adapter Context Source 是否已经足够支撑 Phase6 的真实长 Action 场景，以及是否具备进入 Phase7 Definition-backed Context 的 Adapter 事实基础。

---

# 10. Phase5.6：Stardew Dialogue Interaction Surface

## 阶段目标

建立 Stardew Adapter 的正式对话交互面，让玩家输入、NPC UI 回复和 Runtime AgentTurn 形成闭环。

本阶段主要回答：

> **对话是否可以作为跨 Turn session 存在，并在不把 Stardew UI 逻辑放入 Runtime 的前提下支撑真实玩家回复？**

## 主要范围

- Adapter 维护内存态 `conversation_id`，一个 conversation 可以跨多个 AgentTurn；
- 新增 `player_said_to_npc` 事件，把玩家 option / free text 回复送入 Runtime；
- Protocol additive 增加 `ContextFact` 与 `GameEvent.context_facts`，把玩家输入等 model-visible event context 作为通用事实交给 Runtime；
- `player_interacted_with_npc` 携带 `conversation_id`，同一 active conversation 复用会话 ID；
- 新增 `present_dialogue` 同步 capability，展示 NPC 台词、回复选项和 free text 入口；
- 新增 `face_player` 同步 capability，支持 NPC 面向玩家；
- Dialogue UI 区分玩家提交、玩家放弃和 Adapter 抢占关闭；
- Runtime 更新通用 prompt、nested observation 渲染回归、ContextFact memory projection 和 Recent Memory 可见摘要；
- Context Engine 过滤 `GameTime > CurrentGameTime` 的 Memory，不把未来时间记忆注入当前 Model Context；
- 明确 Interaction Context Guard 的边界，作为 Phase6 ActionRequest source correlation 与 TurnCompletion 的 Adapter 侧接入点。

## 非目标

```text
除 ContextFact / GameEvent.context_facts 之外的 Protocol 字段变更
Runtime Stardew-specific parser
同一 Turn 内等待玩家输入
等待 LLM 期间冻结玩家或 NPC
Interaction Context Guard 执行态校验
move_to / async Action lifecycle
ActionStatusUpdate / Turn resume
AgentDefinition store
长期 conversation persistence
ValleyTalk prompt builder 迁移
```

## 完成条件

- Adapter 可以发送 `player_interacted_with_npc` 与 `player_said_to_npc`；
- `player_said_to_npc` 携带 `ContextFact(kind=utterance)`；
- `Observation.state.stardew.conversation` 可以提供 active conversation 的最近对话行；
- `present_dialogue` 成功显示 UI 后写入 NPC conversation line；
- 玩家提交 option / free text 后发送新的 GameEvent，conversation 继续；
- 玩家 Close / Escape 放弃菜单时关闭匹配的 active conversation；
- Adapter 抢占关闭旧菜单时不关闭 active conversation；
- `face_player` 成功输出 `facing`，不同 location 返回 `REJECTED / different_location`；
- 成功完成的对话 Turn 可以在 Recent Memory 中同时包含玩家输入 ContextFact 和 NPC 可见动作 outcome；
- `GameTime > CurrentGameTime` 的 Memory 不进入 Model Context，同一可比时间片内按 `GameEvent.sequence` 稳定化；
- Runtime 不新增 Stardew-specific parser；
- Phase6 可以在对话事件和同步 UI 基础上接入 ActionRequest source correlation、TurnCompletion、Interaction Guard 和长 Action。

## 阶段结束 Review

重点确认玩家交互到模型响应之间的世界演化如何收敛，以及 Phase6 是否需要先实现 Tool Policy Generalization、ActionRequest source correlation、TurnCompletion 和 Interaction Context Guard，再进入 `move_to` vertical slice。

---

# 11. Phase6：Tool Policy、Action Source、TurnCompletion、异步 Action Lifecycle 与 AgentTurn Resume

## 阶段目标

证明 GameAgent 可以原生处理长时间运行的游戏动作，并能管理交互等待期间的生命周期，而不是把所有 Environment Tool 都当作同步 RPC。

本阶段主要回答：

> **Action 或交互等待期间游戏继续运行时，Runtime 能否明确等待、恢复、释放，并继续推进原 AgentTurn？**

## 主要范围

- 开发前完成 Async Action Protocol Strategy ADR，冻结现有 Action lifecycle 字段、`ActionRequest` source correlation 与新增 `TurnCompletion` 的职责边界；
- Runtime Tool Policy Generalization：Runtime 不按 Stardew capability name 写执行特例，通用策略由 `Capability.extensions.gameagent.tool_policy` 声明；
- `Capability.description` 保持 model-facing 工具用途说明，Runtime 不从自然语言说明解析执行策略；
- Protocol additive 增加 `ActionRequest.source_event_id` / `source_turn_id`，让 Adapter 可以把 Action 绑定回触发它的 accepted GameEvent；
- Protocol additive 增加 Runtime -> Adapter 的 `TurnCompletion` 终态信号，使 Adapter 可以释放 pending interaction context；
- 接入 Adapter 侧 Interaction Context Guard，防止 LLM 响应前玩家或 NPC 已经离开后仍显示 UI 或启动过期 movement；
- 等待 LLM 或等待异步 Action 期间游戏世界继续运行，Adapter 在 effect time 校验当前上下文；
- 支持 Action 非终态状态，例如 accepted / running；
- AgentTurn 可以进入 waiting / suspended 状态；
- Action terminal result 到达后可以恢复 Turn；
- 明确 timeout、cancel、interrupt 和迟到结果语义；
- 扩展确定性测试夹具，支持 ActionStatusUpdate（ACCEPTED / RUNNING）、延迟 terminal result、cancel 竞争与 late result 注入；
- 使用一个真实长 Action vertical slice 验证，例如 `move_to`。

## 非目标

```text
复杂行为树
多个并发长 Action
事务回滚
Workflow Engine
Runtime 崩溃后的 continuation 恢复
路径规划进入 Runtime
未通过 ADR 证明必要的 Protocol breaking change
长期 conversation persistence
```

## 完成条件

- Protocol additive `ActionRequest.source_event_id` / `source_turn_id` 与 `TurnCompletion` 已生成到 Runtime / Adapter 使用面；
- Runtime 执行路径不再硬编码 `present_dialogue` 等 game-specific capability name；
- `present_dialogue` 的独占 step、成功后 settle 由 capability policy 声明；等待后续玩家事件由 capability description 与 `player_said_to_npc` / `ContextFact` event contract 承载；
- Runtime 构造的 `ActionRequest` 携带原 `GameEvent.event_id` 与当前 `turn_id`；
- Adapter 可以返回完整的 Action 非终态与终态生命周期；
- Adapter 可以基于 `TurnCompletion` 释放等待态 interaction context；
- `present_dialogue` 与 `move_to` 可以在执行前拒绝已失效的 interaction context；
- Runtime 不阻塞 Environment 消息接收循环等待长 Action；
- AgentTurn 可以等待 Action，并在 terminal result 后恢复；
- `move_to` 等具体执行仍完全位于 Adapter / Game；
- Trace 可以复盘 suspend、Action lifecycle 和 resume；
- Phase6 实现符合已 Accepted ADR 确定的 Protocol 与 continuation 策略。

## 阶段结束 Review

重点确认 continuation 是否需要持久化、Action 是否需要独立子系统，`TurnCompletion` 是否足以支撑 Adapter interaction lifecycle，以及系统是否具备进入 reconnect / recovery 的条件。

---

# 12. Phase6.5：Stardew Dialogue Interaction Convergence

## 阶段目标

收敛 Stardew 玩家点击 NPC 后的真实对话体验，让对话入口、回复入口、重复点击和结束语义稳定落在 Adapter 侧。

本阶段主要回答：

> **玩家点击 NPC 时，Adapter 能否提供一个稳定、可回复、可主动结束且不会重入错乱的 Stardew 对话面？**

## 主要范围

- Stardew 生产 Tool View 不再暴露 `speak`；
- `present_dialogue` 成为 Stardew 玩家点击 NPC 后的主对话能力；
- 单句结束型 NPC 台词通过 `present_dialogue(text, reply_options=[], allow_free_text=false)` 表达；
- `present_dialogue` 缺省 `allow_free_text=true`；
- 回复选项和自由输入入口稳定共存；
- source-time interaction gate 使用与 effect-time guard 一致的距离规则；
- 同一 NPC pending 或 committed interaction 完成前 suppress 重复点击；
- waiting menu 只覆盖 Runtime 返回 ActionRequest 前的等待期，ActionRequest 到达后、执行 capability 前关闭；
- submit close 与 abandon close 的 in-flight 释放语义明确；
- Adapter 日志可以复盘 ActionRequest source correlation、ActionResult code/message 和 TurnCompletion 释放；
- Runtime Core 保持 game-agnostic，不新增 Stardew capability name 执行分支。

## 非目标

```text
Protocol 字段变更
Runtime async lifecycle 重写
Runtime 路径规划
自然语言地点解析
Stardew vanilla dialogue Harmony patch
Adapter 内部 LLM
长期 conversation persistence
Runtime 崩溃后的 continuation 恢复
session-scoped capability registry
capability-driven visible summary metadata
```

## 完成条件

- Stardew CapabilityList 不暴露 `speak`；
- 普通点击 NPC 不再只触发单句 `speak`；
- `present_dialogue` 缺省提供自由输入入口；
- 玩家可以选择 option、输入 free text 或 Close / Escape 主动结束；
- 连点 NPC 不产生重复 GameEvent、乱序回复或叠 UI，ACK 前重复点击也会被 pending in-flight gate 拦截；
- source-time gate 与 effect-time guard 使用同一距离语义；
- `move_to` 执行前关闭 waiting menu，不阻塞 Stardew world tick；
- `present_dialogue` rejected / failed / cancelled 时 Adapter 日志包含 code/message；
- 单句结束型 `present_dialogue` 不遗留 active conversation；
- Runtime 默认 prompt 和执行路径不写死 Stardew tool name。

## 阶段结束 Review

重点确认 Stardew 对话入口是否已经稳定，`speak` 是否需要作为 future ambient capability 重新进入动态 Tool View，以及 Phase7 是否应进入 Context Subsystem Completion。

---

# 13. Phase7：Context Subsystem Completion

## 阶段目标

把 Context 从“临时拼 prompt 的辅助代码”升级为 Runtime 中正式的一等主链路。

本阶段主要回答：

> **模型每一轮到底看到什么、为什么看到这些、这些内容来自哪里、哪些内容更可信、哪些内容在预算不足时可以裁剪？**

Phase7 的 `Completion` 只表示当前真实输入的 Context 主链路完成，不表示所有未来记忆、检索和世界状态能力都已经实现。

## Phase7 运行流程

```text
Gateway / Session Resolver
        解析 AgentSessionKey
        校验 target_entity_id 与唯一、无冲突的目标 EntityRef
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
        校验本轮输入的 scope 一致性
        加载 Game Definition / Agent Definition
        生成 Agent Instance Descriptor
        分离 Current Event 与 ContextFacts
        按优先级选择和裁剪内容
        生成 Context Projection / BuildReport
        ↓
Renderer
        ↓
Model Request
        ↓
Tool Scheduler 使用同一份 Tool View snapshot
```

本阶段坚持两个边界：

- Context 由 Runtime 组合，Adapter 只提供游戏事实和 capability；
- Runtime 不解析 `Observation.state.stardew` 的具体游戏字段，不按 Stardew 工具名决定执行逻辑。

## Phase7 生命周期边界

Phase7 必须把五个生命周期分清：

```text
AgentSession identity
    由 Gateway / Session Resolver 在进入 AgentTurn 前解析。

Definition Catalog
    在 Runtime 进程生命周期内保持稳定，第一版不做热更新。

Environment Tool Catalog
    绑定当前 EnvironmentSession。

Turn Tool View
    在 AgentTurn 开始时捕获一次，整个 AgentTurn 内不可变。

Context Projection
    在每次 Provider.Generate 前重新构建，反映当前 AgentStep 的 Observation、Transcript 和 ContextFacts。
```

也就是说，Gateway / Session 层决定“这个事件属于哪个 Agent”；Tool Runtime 决定“当前连接提供哪些工具”；Context Engine 决定“这个 Agent 这一轮模型调用该看到什么”。

## Phase7.0：Context Contract Entry Gate

Phase7.0 是入口闸门。它负责确认 Phase7 的总体规划、共享边界和后续阶段拆分已经清楚，不负责在单阶段方案里展开 Phase7.1 到 Phase7.5 的完整实现细节。

主要范围：

- 确认 Phase7 的总体主线、生命周期边界和 Runtime / Adapter 分工；
- 确认 `GameAgent 阶段规划.md` 承载 Phase7 共享规则；
- 确认 `GameAgent MVP0 Phase7.0 技术开发与验收方案.md` 只作为入口检查文档；
- 确认 `Context架构设计.md` 保持长期 Draft，只标注 Phase7 当前采用的规范子集；
- 确认 Phase7.1 到 Phase7.5 能分别写方案、分别开发、分别验收。

以下共享合同属于 Phase7 总体规划，由对应 Phase7.x 在自己的技术开发方案中细化和实现。这里冻结语义边界和阶段归属，不冻结具体接口、对象命名、报告字段或算法步骤。

主流程交付物归属：

| 主流程交付物 | Owner |
| --- | --- |
| Resolved AgentSessionKey | 现有 Gateway / Session 机制，Phase7 复用 |
| Canonical / Validated Target EntityRef | Phase7.1 |
| Current Event | 现有 Protocol / Gateway 机制，Phase7 复用 |
| Current Observation | 现有 Gateway / AgentLoop 机制，Phase7 复用 |
| Recent Memory | 现有 Phase4 短期 Memory，Phase8 再做持久化 |
| Current Turn Transcript | 现有 Phase5 multi-step transcript，Phase7.4 再做裁剪 |
| Turn Tool View snapshot | Phase7.2 |
| Context Projection / Renderer 主链路 | Phase7.3 |
| Selection / Budget / BuildReport / 综合诊断 | Phase7.4 |
| Stardew 端到端验收 | Phase7.5 |

Definition 最小模型：

```text
GameDefinition
    Required identity:
        schema_version
        game_id
    Model-visible content:
        summary
        world_rules[]
        lore[]
        narrative_constraints[]
    Optional metadata:
        title
        source_version

AgentDefinition
    Required identity:
        schema_version
        game_id
        definition_id
    Model-visible content:
        identity
        personality[]
        speech_style[]
        preferences[]
        behavior_guidelines[]
    Explicit exclusions:
        world_id
        entity_id
        current location
        current relationship state
        current inventory
        other world-instance facts

AgentInstanceDescriptor
    Runtime domain object:
        game_id
        world_id
        entity_id
        entity_type
        display_name
        definition_id
    Default model projection:
        display_name
        entity_type
        必要的可读身份信息
```

Definition fallback：

```text
Game Definition 不存在
    使用确定性的空 / 通用 Game Definition fallback，AgentTurn 继续执行。

definition_id 为空
    使用 Agent fallback，不推导 definition_id = entity_id，AgentTurn 继续执行。

Agent Definition 未找到
    使用 validated descriptor + Runtime 全局 npc_style fallback，AgentTurn 继续执行。

已配置静态 Definition 文件不可读、语法错误、schema_version 不支持、
重复 key 或文件声明 scope 冲突
    Runtime 启动 fail-fast。

Definition Source 返回的数据与查询 key 不一致
    Context build failed，不调用 Provider，不提交 Action。
```

Target EntityRef 解析规则：

```text
target_entity_id
    必须解析为唯一、无冲突的目标 EntityRef。

同一个 target_entity_id 在 GameEvent.entities 中出现多次
    如果字段完全一致，可以规范化为同一对象。

同一个 target_entity_id 的 definition_id / entity_type / display_name 存在冲突
    必须在 pre-turn validation 阶段 EventAck REJECTED。
    不创建 lane，不创建 AgentTurn。
    Runtime 不得按列表顺序取第一条。
```

Scope 规则：

| Context 内容 | Scope |
| --- | --- |
| Runtime Policy | Runtime |
| Game Definition | `game_id` |
| Agent Definition | `game_id + definition_id` |
| Agent Instance Descriptor | `game_id + world_id + entity_id` |
| Recent Memory | `game_id + world_id + entity_id` |
| Current Event / ContextFacts / Observation / Transcript / Tools | 当前 AgentTurn |

可信关系和新旧关系：

```text
Runtime Policy
    对 Runtime 执行规则、Tool 使用规则和全局输出约束具有最高权威。
    Game Definition、Agent Definition、Event、ContextFacts、Observation、Memory 和 Transcript 都不得覆盖 Runtime Policy。

Game Definition
    只对稳定游戏规则、世界观和全局叙事边界具有权威。

Agent Definition
    只对稳定角色身份、人格、说话风格和行为倾向具有权威。
    Agent Definition 不覆盖 Runtime Policy，也不声明当前游戏状态。

Current Observation
    对当前游戏事实高于历史 Memory。

Current Event ContextFacts
    是本 Turn 中 Adapter 显式声明的输入事实。

Current Turn Transcript
    记录本 Turn 内的模型决策、ToolCall 请求和执行反馈。

ToolCall
    只表示模型计划或 Runtime 请求执行的动作，不证明 Environment effect 已经发生。

ToolResult / ActionResult / 更新后的 Current Observation
    用来确认动作结果或当前世界事实。

Current Event、ContextFacts、Observation、Memory 和 Transcript
    都是 Context 数据，不是 Runtime Policy。
```

Scope 不一致失败语义：

```text
任一必要 scope 不一致
    → Context build 失败
    → 不调用 Provider
    → 不提交 Action
    → AgentTurn 通过现有 context failure boundary 收敛。
```

预算保护规则：

```text
预算单位：
    provider-neutral estimated tokens

整体预算至少覆盖：
    Request.System
    Request.Messages
    Request.Tools
    Request.Controls
    必要的 section framing / labels

必须保护最小 Anchor：
    Runtime Policy minimum
    Agent identity / Definition fallback
    Current Event
    Current Event ContextFacts
    Current Observation minimum
    Final Turn Tool View

可以优先裁剪：
    旧 Recent Memory
    次要 Game Definition 内容
    Observation 扩展细节
    较旧 Transcript output

预算分三层：
    Model Request 整体近似预算
    每个 Source 的独立硬上限
    Source 内部的结构化投影上限
```

这里的“保护”只表示保留最小投影，不表示完整 Observation 或完整 Tool schema 永远不能受预算限制。Provider SDK 外层 JSON envelope 不要求精确计入，但 Tools 不能被当作免费输入。

Event payload、Observation state、ContextFact attributes 和 ToolResult output 必须按字段、数组项或完整 Section 做结构化裁剪，不得输出被截断成非法 JSON 的结构化内容。

Current Turn Transcript 必须按完整 ToolCall / ToolResult 原子组裁剪。当前数据模型下，原子组是一条包含 `ToolCalls[]` 的 assistant message 加上紧随其后的、ToolResult IDs 与其对应的 tool message。裁剪后必须保持原始消息顺序、`tool_call_id` 关联、ToolCall / ToolResult 配对和当前 Turn 的最小因果链；不得保留孤立 ToolCall 或孤立 ToolResult。较新的完整 Transcript 原子组优先于较旧 Transcript。

如果所有最小 Anchor 加起来仍超过硬上限，必须产生明确的 context build failure，不能静默丢掉当前事件、当前观察或生成结构无效的 Model Request。

诊断边界：

```text
Capability bootstrap diagnostics
    Phase7.2 负责，EnvironmentSession scope。
    记录 capability 接受、拒绝、重复 name、schema / policy 问题和 Environment Tool Catalog 结果。

Turn Tool View consistency diagnostics
    Phase7.2 负责，AgentTurn scope。
    记录最终 Turn Tool View snapshot，以及模型可见工具和 Scheduler 可执行工具是否一致。

Context build diagnostics
    Phase7.4 负责，AgentStep / Model Call scope。
    汇总 Definition 加载结果、fallback、Context 选择、裁剪、整体规模和 Tool View 诊断摘要。
```

Phase7.0 完成条件：

- Phase7 技术方案能够按 Phase7.1–Phase7.5 拆分；
- Phase7 共享合同已经放在总阶段规划中，而不是塞进 Phase7.0 单阶段方案；
- AgentSession identity、Definition Catalog、Environment Tool Catalog、Turn Tool View、Context Projection 五个生命周期已经有明确归属；
- Scope、可信关系、新旧关系、Definition、fallback、target entity、Tool diagnostics、budget、transcript 和 BuildReport 都能找到后续负责阶段；
- 每个 Phase7.x 都有独立完成条件；
- 非目标清单已经写清，不把未来能力混进 Phase7.0；
- `Context架构设计.md` 的状态更新策略已经明确：保持长期 Architecture Draft，并标注 Phase7 Normative Subset。

## Phase7.1：Definition Sources 与 Instance Descriptor

让角色定义和游戏定义真正进入 Runtime。

主要范围：

- 明确 Runtime Policy、Game Definition、Agent Definition 和 Agent Instance Descriptor 的职责；
- 在 Gateway / pre-turn validation 边界形成 canonical Target EntityRef；
- 定义最小 `GameDefinition`，描述当前游戏的基础规则、世界观、全局叙事边界和模型应遵守的常识；
- 定义最小 `AgentDefinition`，描述角色身份、性格、说话风格和行为边界；
- 提供小型 `GameDefinitionSource` 和 `AgentDefinitionSource`，生产路径可用静态文件或启动时 Catalog，测试路径可用内存实现；
- Runtime 根据 `EntityRef.definition_id` 加载 Agent Definition；
- Runtime 根据 `game_id` 加载 Game Definition；
- Agent Instance Descriptor 复用 `game_id + world_id + entity_id + entity_type + display_name + definition_id`，不新增协议；
- 增加最小 Stardew Definition fixtures，用来证明 Source loading、scope 校验和不同 definition_id 解析结果有效；
- 保持现有 `prompt.npc_style` 作为全局 fallback，而不是覆盖每个 Agent Definition 的说话风格。

职责边界：

```text
Runtime Policy
    硬执行约束、工具使用规则、语言、输出长度、全局默认行为。

Game Definition
    世界规则、世界观、全局叙事边界和游戏常识。

Agent Definition
    身份、人格、偏好、说话风格和角色行为倾向。

Agent Instance Descriptor
    当前实例的身份事实，例如 display_name、entity_type、definition_id。
```

Definition fallback：

```text
game_id 对应的 Game Definition 不存在
    使用确定性的空 / 通用 Game Definition fallback，AgentTurn 继续执行，Definition 加载结果记录 missing / fallback。

definition_id 为空
    使用 Agent fallback，不推导 definition_id = entity_id，AgentTurn 继续执行，Definition 加载结果记录 missing / fallback。

Catalog 中不存在对应 Agent Definition
    使用 validated descriptor + Runtime 全局 npc_style fallback，AgentTurn 继续执行，Definition 加载结果记录 missing / fallback。

已配置静态 Definition 文件不可读、语法错误、schema_version 不支持、重复 (game_id, definition_id) 或重复 game_id
    Runtime 启动加载阶段 fail-fast，不启动已知配置损坏的 Runtime。

Definition Source 返回的数据与查询 key 不一致
    Context build failed，不调用 Provider，不提交 Action。
```

不做：

```text
Definition 数据库
Definition hot reload
Definition 多后端 Resolver
AgentDescriptor protobuf
Descriptor registration RPC
Descriptor persistent store
```

完成条件：

- 至少两个 Stardew NPC 能加载不同 Agent Definition；
- `definition_id != entity_id` 时仍能正确加载 Definition；
- `target_entity_id` 必须先解析成 canonical Target EntityRef，再用于 Agent Definition binding 和 Instance Descriptor 构造；
- `game-a + definition-x` 不得读取 `game-b + definition-x`；
- 同一个 `definition_id` 可以被不同实体复用，实体身份和 Memory 仍按 AgentSession 隔离；
- Definition 缺失时使用 Runtime fallback，不阻断现有 AgentTurn，也不让 Adapter 拼 prompt；
- 配置损坏的静态 Definition 在启动时失败，不被静默 fallback 掩盖；
- Definition 与 Agent Instance Descriptor 可以作为结构化输入交给后续 Context Engine；
- 本阶段不以最终 Model Request 中的具体段落作为硬验收。

## Phase7.2：Environment-scoped Tool View

让模型看到的工具，和 Runtime 实际执行的工具保持一致。

主要范围：

- Gateway 接收 CapabilityList，并将其与当前 EnvironmentSession 绑定；
- Tool Runtime 负责校验 Capability，并为该 EnvironmentSession 构造完整、合法的 Environment Tool Catalog；
- Tool Runtime 记录最小 bootstrap / consistency diagnostics，包括 capability 接受、拒绝、重复 name、schema / policy 问题和最终暴露工具；
- Phase7.2 技术方案必须明确 `CapabilityList.entity_id` 的 MVP0 语义，不得把 entity-scoped capability 静默提升为 EnvironmentSession 全局能力；
- AgentTurn 创建时，Runtime 从当前 Environment Tool Catalog 捕获不可变的 Turn Tool View snapshot；
- AgentLoop 构建 Model Request 时使用最终 Turn Tool View；
- Tool Scheduler 执行 ToolCall 时使用同一份最终 Turn Tool View；
- 不同 EnvironmentSession 的 capability 不串线；
- 现有单 Stardew Adapter 行为不退化。

Turn Tool View 必须包含模型展示和 Runtime 执行所需的完整 Tool Entry：

```text
model-facing ToolDefinition
Tool Kind
Concurrency Mode
Execution Mode
Tool Policy
Lookup Entry
```

推荐流程：

```text
CapabilityList
    ↓
结构校验
    ↓
Environment-scoped Tool Catalog
    ↓
AgentTurn snapshot capture
    ↓
Turn Tool View snapshot
    ├── Model Request.Tools
    └── Tool Scheduler.Lookup
```

不做：

```text
Adapter reconnect
Capability hot refresh
Capability subscription
Tool View persistent registry
跨连接 replay
```

完成条件：

- 两条测试 EnvironmentSession 上报不同 capabilities 时，新 Turn 只看到当前连接的 tools；
- 两条测试 EnvironmentSession 上报同名 capability 但 schema / policy 不同时，不会互相覆盖；
- 同一个 AgentTurn 内，Model Request 中的 tools 与 Scheduler 可执行 tools 一致；
- 旧的进程级 Tool Registry 不再决定当前 Turn 的完整工具视图；
- 如果某个 Tool 没有进入最终 Turn Tool View，模型不会看到它，Scheduler 也不会执行它；
- 同一 AgentTurn 内，Tool exposure、ToolCall lookup、Tool validation、concurrency mode、execution mode、Tool Policy 和 Scheduler execution 都不得重新读取进程级 Environment Tool Registry；
- 同一个 EnvironmentSession 的 CapabilityList 内出现重复 name 时，不得静默 last-write-wins；
- 非法 schema / tool_policy 必须产生明确 bootstrap diagnostic；
- `CapabilityList.entity_id` 非空时必须有明确处理语义，不能静默暴露给整个 EnvironmentSession；
- 进程级 Runtime Tool 未来可以继续存在，但 Environment Tool 必须先绑定到当前 EnvironmentSession，再进入 Turn Tool View。

## Phase7.3：Context Engine Core

把现有 ContextBuilder / Renderer 演进成清楚的 Context 组装流程。

主要范围：

- 建立 Context Engine 主入口，Go 类型使用 `Engine.Build`；
- 将直接输入和加载结果统一投影为结构化 Context；
- Current Event Projection 保留通用事件外壳，例如 event_type、game_time、target、sequence；
- GameEvent payload 可以作为有界、通用 JSON 投影进入 Context，但 Runtime 不按 payload key 做 game-specific selection；
- Current Event ContextFacts 单独进入 Context，例如玩家发言、选择、命令和交互事实；
- 需要稳定进入模型或 Memory 的事件语义，由 Adapter 通过 ContextFacts 显式提供；
- 独立渲染 ContextFacts 后，Event Projection 不再重复渲染同一份 context_facts；
- Recent Memory / Tool outcome 的模型可见投影不得按 Stardew capability name 写 switch；
- 第一版使用通用、结构化、有界的 Tool outcome rendering，例如 tool name、稳定排序后的 arguments、action status 和有界 output；
- Renderer 只负责把结构化 Context 输出成模型请求；
- 保持 AgentLoop、Model Provider 和 Adapter 的核心职责不变。

不做：

```text
通用 Context Source 插件框架
所有 Source 都实现 Load 接口
字段级游戏语义解析
复杂冲突合并引擎
capability-driven visible summary metadata
```

完成条件：

- Fake Provider 可以稳定断言最终 Model Request 的主要段落；
- Context 中能明确看到 Game Definition、Agent Definition、Instance Descriptor、ContextFacts、Observation、Memory、Transcript 和 Tool View；
- Runtime 不新增 Stardew-specific Observation 字段判断；
- Runtime 不按 `GameEvent.payload` key 做 Stardew-specific 解析，关键事件语义必须由 ContextFacts 提供；
- `Recent Memory` 和 Tool outcome 渲染不按 `speak`、`emote`、`present_dialogue`、`face_player` 等具体工具名分支；
- Gateway 保证 `AgentSessionKey.game_id` 来自当前 `AdapterHello.game_id` 所在的 ConnectionContext；
- `Engine.Build` 校验加载出的 Game / Agent Definition scope 与 `AgentSessionKey.game_id` 一致；
- `Engine.Build` 校验 `AgentSessionKey.world_id` 与 `GameEvent.world_id`、`Observation.world_id` 一致；
- `Engine.Build` 校验 `AgentSessionKey.entity_id` 与 `target_entity_id`、canonical Target EntityRef、`Observation.entity_id` 一致；
- Gateway / pre-turn validation 确认目标 `EntityRef` 唯一且无冲突，不按列表顺序选择第一个目标实体；
- Agent Definition lookup 始终使用 `game_id + definition_id`，不能只按 `definition_id` 查询；
- 任一必要 scope 不一致时，Context build 失败，不调用 Provider，不提交 Action。

## Phase7.4：Selection、Budget 与 Observability

让 Context 在内容变长时仍然稳定、可解释、可测试。

Phase7.2 负责完成 Environment-scoped Tool Catalog、immutable Turn Tool View、全链路 lookup 迁移和最小工具正确性诊断。Phase7.4 不重新设计 Tool View 的生命周期和 lookup 语义，只在既有 Catalog → Final Turn Tool View 边界中增加确定性的 size admission policy，并把 Context 选择、预算、裁剪和报告串起来。

主要范围：

- 按 Source 优先级选择上下文；
- 区分必须保留和可以裁剪的内容；
- 对 Definition、Recent Memory、Observation 和 Transcript 使用简单确定的长度上限；
- Tool schema 计入整体请求规模治理，但不得通过字符串截断破坏结构；
- 对 tool count、description 长度、单 schema 大小和总 schema 大小设置独立上限；
- 超限 Tool 在形成最终 Turn Tool View 时按整项处理，Model Request 与 Scheduler 必须共享同一份最终视图；
- 因 schema budget 剔除 Tool 只发生在形成最终 Turn Tool View 时；每次 Provider.Generate 前重建 Context Projection 不重新改变本 Turn 的工具集合；
- 保证相同输入和相同预算产生相同 Model Request；
- 记录 `BuildReport`，说明加载、缺失、裁剪、fallback、整体规模和 Tool View 诊断摘要；
- 对 Memory 保持已有 timeline filtering 语义。

不做：

```text
Provider-specific 精确 tokenizer
模型窗口自动探测
向量检索
语义压缩
字段级事实冲突报告
```

完成条件：

- 预算不足时，当前事件、当前观察和角色定义不会被旧 Memory 挤掉；
- 被裁剪的内容和原因可以被测试或 trace 看到；
- Memory 与 Observation 冲突时，当前 Observation 在模型输入中被明确标记为当前事实；
- Event payload、Observation state、ContextFact attributes 和 ToolResult output 不会被截成非法 JSON；
- Current Turn Transcript 不会被裁剪成孤立 ToolCall 或孤立 ToolResult；
- Tool schema 不会被截成非法 JSON；
- 最终 Turn Tool View 中不存在“模型看不到但 Scheduler 可以执行”的工具。

## Phase7.5：Stardew Context Integration 与验收

用真实 Stardew 对话证明 Context 主链路已经可用。

主要范围：

- 为两个 Stardew NPC 提供真实 Agent Definition；
- 使用一份 Stardew Game Definition；
- 使用 Fake Provider / Recording Provider 精确验证不同 `definition_id` 产生不同 Agent Definition projection；
- 使用 Fake Provider / Recording Provider 精确验证 Adapter 没有提供完整 prompt；
- 使用真实 Stardew + 真实模型验证完整链路无错误，并观察角色设定能被模型利用；
- 用 Fake 测试验证多个实体共享同一个 Definition 时，Descriptor 和 Memory 不串线；
- 验证 missing Definition fallback；
- 验证两个 EnvironmentSession 的 Tool View 不串线；
- 回归 Phase5 multi-step、Phase6 async move_to、Tool Policy 和 TurnCompletion；
- 同步 Roadmap、Status 和必要架构说明。

完成条件：

- 玩家点击 NPC 后，模型输入包含正确角色定义、游戏定义、当前事件、当前观察、最近记忆、当前工具和本轮对话记录；
- 两个 NPC 的 Definition 差异可以在 Fake / Recording Provider 捕获的 Model Request 中稳定验证；
- 真实 Stardew 对话 smoke test 完成，不要求真实模型输出固定台词或固定 ToolCall；
- Phase5 multi-step、Phase6 async move_to、Tool Policy 和 TurnCompletion 行为不因 Context / Tool View 改造退化；
- Runtime / Adapter 分离边界保持稳定；
- Phase7 技术方案、验收记录和阶段小结都完成。

## Phase7 非目标

```text
Persistent Memory backend
长期 Semantic Memory
Vector retrieval / embedding index
World State Projection
Agent Cognitive State
Experience Retrieval
完整通用 Context Source 插件框架
Definition hot reload
Provider-specific 精确 token budget
Adapter reconnect / Environment Recovery
新增第二个真实 Game Adapter
```

## Phase7 阶段结束 Review

重点确认 Context 主链路是否已经稳定，角色定义是否真的影响模型输入，Tool View snapshot 是否解决当前连接内的工具一致性问题，以及是否具备进入 Persistent Recent Memory 的条件。

---

# 14. Phase8：Persistent Memory, History & Context Compaction

## 阶段目标

让同一 `game_id + world_id + entity_id` 的 Agent 持久积累交互来源，通过摘要和有界近期原文保持连续性，并按需检索仍保留的细节。

持久原文容量与 Context 容量分别管理。Memory 归 Runtime，当前事实以 Observation 为准，ActionResult 的实际结果与角色陈述保持区别。

## 主要范围

| 子阶段 | 主要范围 | 独立状态 |
| --- | --- | --- |
| 8.1 | SQLite 世界分库、entity 隔离、Recent 与凭据原子提交、时间过滤、有界保留 | Accepted，保留原验收限制 |
| 8.2 | 终态来源整批保存、8.1 迁移、固定快照、同步摘要、近期原文和检查点恢复 | Accepted，保留原验收限制 |
| 8.3 | 中文字面检索、Context 片段与去重、按显式策略清理已覆盖的超期原文 | Accepted，保留原验收限制 |

8.2 不按重要性筛选保存来源，恢复保证以成功提交的终态批次为界；中途崩溃可能缺失该轮。压缩后近期原文有可配置 token 目标，后续交互允许继续增长。8.3 默认关闭清理，删除后原文不可精确检索或重建早期摘要。

共同保持读取 fail-open、写入失败不回滚已发生动作、稳定逻辑键幂等与 GameTime 可见性。摘要必须检查累计来源时间，不能在回档后泄露可比较的未来来源。Trace 只作诊断。

详细合同见 [Phase8 总方案](../phase08/GameAgent MVP0 Phase8 技术开发与验收方案.md)、[Phase8.1](../phase08/GameAgent MVP0 Phase8.1 技术开发与验收方案.md)、[Phase8.2](../phase08/GameAgent MVP0 Phase8.2 技术开发与验收方案.md) 与 [Phase8.3](../phase08/GameAgent MVP0 Phase8.3 技术开发与验收方案.md)。

## 非目标

```text
逐步骤 Experience Log 与完整 Event Sourcing
Event replay / Action replay
Durable async continuation
Resume token
Semantic 认知提取、Evidence Capsule 专门模型与 supersede/invalidate
Vector DB / embedding retrieval
Knowledge Graph
通用 Migration Framework
跨机器共享 Memory
完整存档 timeline / branch 管理
```

## 完成条件

- 8.1 的 Accepted 状态与已有验证限制保持独立；后续规划不补记未执行的验收。
- 8.2 以 SQLite、摘要覆盖与最终模型请求证明完整约定来源、身份隔离、事务、预算和回档规则；真实模型忠实性与真实 Runtime 重启单独验收。
- 8.3 证明近期尾部以外的中文原文可查询并进入 Context，清理资格、索引一致性和删除后能力均可验证。
- 各阶段均保持读取失败继续 Turn、写入失败不改变 Action/TurnCompletion；不依赖 Adapter 保存 Memory。
- 每个子阶段独立评审、开发和验收；方案通过只表示允许编码。

## 阶段结束 Review

持久来源、摘要忠实性、原文可用性和回档限制以各阶段验收记录为准。Phase9 使用已 Accepted 的 8.1–8.3 基线，负责 Task 结果来源接入与后续引用回归；摘要、检索和留存策略保持既有合同，不增加新的 Memory 开发前置项。Task 检查点与 History / Summary 检查点分别管理。

---

# 15. Phase9：Runtime Durable Task 与预约最小闭环

状态：`Implementation Plan Draft`。本阶段定位为可向他人展示的最小产品闭环，Accepted 以实现和验收证据为准。

## 阶段目标

玩家通过自然语言与 NPC 约定未来某天在沙滩见面。Runtime 持久保存任务，按游戏时间唤醒 NPC，以多个短 Turn 完成跨地图赴约、等待、见面或超时离开；对话真正结束后 NPC 恢复原生日程，后续对话可引用实际结果。任务支持跨天，且随游戏保存的检查点恢复。

## 主要范围

- Runtime 内部 TaskService 提供单实例 SQLite Task、首次及后续唤醒、revision、幂等投递和结果关联；通过 Runtime Tools 暴露创建、等待与取消意图；
- 持久任务按通用游戏逻辑时间到期，进入对应 NPC 的串行 lane，先协调事实；需要行动选择时才启动新 Turn 并重新观察，等待不占用长期 Turn 或 Action waiter；
- 等待回执、已确认见面/爽约、截止协调和控制权收尾由代码处理；业务结果不依赖模型确认，真实到达交互可独立开启对话；
- Adapter 提供语义点位、目标日可行性校验、当下跨地图移动及有界等待；真实位置、日历、节日和可达性均由游戏侧判断；
- 提供 `approach_player` 接近玩家能力，由 Adapter 按动作开始时的玩家位置选择相邻空闲格，并以该固定格为终点执行一次异步移动；
- 普通对话及赴约对话真正结束后，Adapter 释放本次交互控制权，让 NPC 自动恢复当前时刻的原生日程和自主行为；
- 通过 Task 来源、世界结果和 GameEvent 关联 `task_id / wake_id / operation_id / turn_id`，保持真实见面与模型承诺的区别；
- 最小检查点使用 Runtime 全量任务快照和游戏 SaveData 中的精确 checkpoint_id；恢复完整任务集合、状态、进展和后续唤醒；
- 区分 Runtime 同运行实例重启与游戏重新读档；拒绝旧代次回调，未确认动作先观察再协调；
- 覆盖跨天保存、回档、非法输入、节日、寻路失败、断线安全释放和检查点异常；
- 提供成功赴约和爽约两条可复现演示路径，以及事件、ActionResult、Task、Checkpoint、History 和 Trace 证据。

## 架构边界

Agent 决定意图和行动；Runtime 管理持久 Task、游戏时钟唤醒、短 Turn、工具执行和结果关联；Adapter/Game 提供事实并执行当前世界命令、控制权交接及原生日程恢复。Runtime Core 不解析 Stardew 地图、日历或私有 Observation。

TaskService 是 Runtime 进程内模块。模型通过统一 Tool View 中的 `KindRuntime` 工具调用其意图入口；时钟、证据和检查点生命周期直接调用内部接口。Environment Capabilities 由 Adapter 声明，映射为 `KindEnvironment` 并通过 ActionRequest 执行。两类工具共用注册、预算与策略，执行位置明确区分。

玩家交互、proposal_ref 和单 NPC 单非终态任务是 Phase9 预约准入策略，通用 Task 内核以规范化 TaskSpec、时钟及结果合同工作。模型不直接提交 succeeded/failed，也不填写 revision、claim 或运行代次。后台结果通过通用 task_result History 来源入账，任务成功不撤销仍有效的到达交互。

游戏时钟、世界加载代次、Task 来源和检查点采用通用增量协议。未来任务不保存在 Adapter 的活动列表中；Adapter 的等待监听只负责已经开始的有限世界效果与安全截止。

Runtime 断线时游戏照常运行和保存，当前临时控制权安全释放；保存中的任务检查点标记为 unconfirmed，加载后暂停任务恢复并提示。Phase9 不提供 Adapter 离线任务镜像，也不把 Runtime 数据库的最新状态冒充该存档的任务状态。

单次会面窗口位于目标日内，任务创建和唤醒可跨天、跨季、跨年。自动重连、跨连接事件恢复及完整 pending operation 协调归 Phase11；复杂工作流、循环任务和分布式调度不在本阶段范围。

## 开发子阶段

| 子阶段 | 交付主题 |
| --- | --- |
| 9.0 前置门 | 已 Accepted 基线回归；跨地图、原生日程恢复及保存交接的可行性门 |
| 9.1 | TaskService 与通用模型、预约准入隔离、确定性状态转换、SQLite 事务和全量快照 |
| 9.2 | 游戏时钟/世界绑定、Runtime Tools / Registry、能力合同、可靠 lane 唤醒及协调/认知分流 |
| 9.3 | 游戏侧移动、等待、approach_player、控制权和真实 UI 生命周期 |
| 9.4 | 检查点保存/恢复、来源和结果验证、Task Context 与后台 task_result History 闭环 |
| 9.5 | 完整自动化、跨天实机演示、异常验收、复审与交付 |

按 9.0 → 9.1–9.5 连续开发，每个单元自动测试通过后继续，不逐阶段等待人工 review。独立代码/系统 review 集中在 9.5，修复后重新验证最终产物。检查点内核在 9.1 建立，真实游戏保存接线在 9.4 完成；缺少实机条件时保留明确未验收状态。

各阶段的文件、接口、任务顺序、测试和交接条件见 Phase9 总方案第 13 节所链接的五份子方案。

## 完成条件

- 两条演示路径在真实 Stardew 与真实模型中重复通过，不能仅凭台词判断预约或见面成功；
- 创建 Turn 结束且经历跨天保存后，Runtime 仍能唤醒 NPC；赴约不依赖玩家再次点击或持续跟随，等待期间不占用 Runtime Turn；
- Task 进展与下一次唤醒原子保存；队列满不丢任务，截止和到达事件可收敛，重复投递不重复执行世界命令；
- Runtime 工具本地执行、Environment 工具经 Adapter 执行；已确认结果在模型不可用时仍能提交，任务完成后有效到达交互仍可继续；
- 玩家到达和 NPC 超时离开都有真实世界状态与来源记录，下一次对话可利用对应上下文；
- 接近玩家的选点、移动与结果反馈可验证；玩家途中移动时目标格保持不变，完成结果准确区分抵达固定终点与当前仍相邻；
- 对话结束后 NPC 无需新的模型调用或玩家点击即可继续原生行动；在日程要求移动的验收场景中，NPC 能实际离开交互位置；
- 读档按精确检查点恢复；保存后新建的任务不在恢复集合中，保存后已完成的任务可恢复到保存时的非终态；
- Runtime 同运行实例重启恢复 working head，游戏重新读档恢复快照；断线保存和异常引用明确暂停任务恢复；
- Runtime Core 保持 game-agnostic，现有对话、异步动作和 Memory 回归通过；
- 跨地图与日程恢复可行性验证、开发里程碑和验收记录齐全。

详细合同见 [Phase9 技术开发与验收方案](../phase09/GameAgent MVP0 Phase9 技术开发与验收方案.md)。

## 阶段结束 Review

确认跨天最小闭环可以稳定演示，Task、世界操作、UI、检查点与 Memory 的权威边界清楚，失败路径能够收敛，并以此作为 Phase11 连接恢复的真实场景输入。

---

# 16. Phase10：Ecosystem & Productization

## 阶段目标

把 WIA 从"单一游戏 + 自有能力 + 本地开发可跑"扩展到"能力来自外部、外部用户能用、Runtime 能承载第二个游戏"。

本阶段回答：

> **WIA 是一个能跑 demo 的项目，还是一个可以长大的 Runtime + Adapter 生态？**

## 子阶段划分

| 子阶段 | 主题 | 一句话验证目标 |
| --- | --- | --- |
| 10.1 | Mod 能力接入与自治调用验证 | 能力来自第三方 mod 时，模型能自主选择、正确执行、失败可收敛 |
| 10.2 | 桌面客户端与产品化 | 外部用户能在不读源码的前提下装起来、跑起来、看见 agent 在做什么 |
| 10.3 | 第二款真实游戏接入 | 同一 Runtime 不改核心即可承载第二个 Adapter，且接入有可复用检查表 |

## 主要范围

- 第三方 mod 能力作为可选依赖接入（首个且必做：MailFrameworkMod 邮件能力），遵守跨 Mod 集成边界。
- 模型自主调用的完整证据链：能力进入 Tool View、模型主动产生 ToolCall、真实执行、结果回灌。自主指 Turn 内 Tool Selection，不含 Background Trigger 或 NPC 自发目标。因此 10.1 **不会**出现 agent 主动起意写第一封信；自主触发已选定复用 Phase 9 durable task + wake 作为后续独立小阶段，路线与前置见 [Phase10 总方案](../phase10/GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md) §2.6。
- 写类能力（第三方 mod 动作）与"能力不适用时不应调用"的负向用例为硬验收；读类能力接入为可选 follow-up。
- Runtime Bootstrap & Data Root：控制面可先于模型就绪（未配置也能启动本地 HTTP 面），数据目录、端口、配置路径、trace、SQLite、definition root 统一从一个 App/Data Root 解析，可作为独立产物在任意目录运行。
- 本地 Web UI（Vue 3 + TypeScript + Vite，`//go:embed` 进 runtime 二进制）：启动后自动打开浏览器、免安装包分发、首次运行向导、依赖体检、Turn 时间线可视化。
- 只服务同一台电脑上的本地浏览器；不做 Mobile、LAN 或远程访问。
- 第二个真实游戏 Adapter，并完成 Stardew Adapter 的物理拆仓（Phase B，见下）。

## 本阶段触发的结构性动作

```text
Phase B（物理拆仓，本阶段授权）
    Adapter 迁往独立仓库 wia-adapter-<game>。
    原触发条件是"出现第二个真实 Adapter"，Phase10.3 即该条件。
    Phase A 已完成逻辑分离，拆仓应为低风险目录搬迁 + 建仓。
```

## 非目标

```text
完全自主 agent（自主目标生成、长时程规划、多 Agent 协作）
背景触发器 / NPC 自发任务（本阶段只验证 Turn 内 Tool Selection）
发物品 / recipe 等高风险 mod 能力（需单独授权）
玩家确认交互字段（当前没有执行机制）
与 Stardew 功能对等的第二个 Adapter
Adapter 之间的能力共享抽象框架
桌面客户端打包（Wails / Electron）与安装器
Mobile / LAN / 远程访问 / 云端托管
```

## 阶段结束 Review

重点确认：第三方能力接入是否已形成可复用边界；产品化是否真的降低了外部使用门槛；第二款游戏是否证明 Runtime Core 无需游戏专属分支；拆仓后两个仓库能否独立构建与发布。

详细技术方案见 [GameAgent MVP0 Phase10 技术开发与验收总方案](../phase10/GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md)。

---

# 17. Phase11：Environment Reconnect and Capability Recovery

## 阶段目标

让 Stardew Adapter 在连接中断或启动顺序变化后建立新的 EnvironmentSession，并确保旧在线状态收敛、新 Tool View 正确生效。

本阶段核心原则：

> **连接可以重建，但 Agent identity、Tool View、Memory 和失败语义不能混乱。**

## 主要范围

- Stardew Adapter reconnect loop 与 backoff；
- Runtime 与 Adapter 任意启动顺序下最终建立连接；
- 新 EnvironmentSession 完成 hello / ready / capability bootstrap；
- 新 EnvironmentSession 根据新的 CapabilityList 重建 Environment Tool Catalog；
- 后续新 Turn 使用新的 Turn Tool View，旧 EnvironmentSession 的 Tool View 和 pending operation 不得进入新连接；
- 当前 stream 的 pending Observe / Action 与 queued tasks 在断线时明确失败或终止；
- 旧 EnvironmentSession 的 late ActionResult / ActionStatusUpdate 不得唤醒新 EnvironmentSession 的 waiter；
- Adapter waiting UI、interaction context 和本地 action state 清理；
- reconnect 后同一实体继续解析到相同 AgentSessionKey；
- reconnect 或 Runtime restart 后继续读取 Persistent Recent Memory；
- 保持 EnvironmentSession 内 duplicate event handling；
- Adapter 不得在 reconnect 后自动 replay 已经收到 `ACCEPTED` 的旧 Event；
- 根据真实测试结果决定是否启用 heartbeat。

## 非目标

```text
Durable Event Inbox
完整 Event Replay / Action Recovery
Exactly-once 全链路
Persistent Async Continuation
Resume Token
通用 Recovery Framework
分布式 Runtime
跨机器高可用
多租户平台
大规模并发 Agent 集群
```

## 完成条件

- Runtime-first 与 Adapter-first 启动都能最终连接；
- stream 中断后 Adapter 能建立新的 EnvironmentSession；
- 旧 pending Observe / Action 不永久悬挂；
- 旧 EnvironmentSession 的 late ActionResult / ActionStatusUpdate 只能被忽略或记录诊断，不能恢复新连接中的 AgentTurn；
- capability 变化后，新 Turn 只看到新 EnvironmentSession 的 Tool View；
- Stardew waiting UI、conversation state 和 interaction context 在断线后收敛；
- reconnect 后 Agent identity 与 Persistent Recent Memory scope 不变；
- reconnect 后的跨 Session / 跨 Runtime exactly-once 不属于 Phase11 保证；
- Adapter 不自动 replay 已经 `ACCEPTED` 的旧 Event；
- disconnected / late result / retry 的处理结果有明确 trace 与 Adapter log；
- 完成一次真实 Stardew 断线重连 smoke test。

## 阶段结束 Review

重点确认 reconnect、Tool View replacement、pending operation 收敛和 persistent state 边界是否稳定，以及是否具备进入系统化 Evaluation 的条件。

---

# 18. Phase12：Evaluation、Developer Experience 与产品化

## 阶段目标

将已经具备核心 Harness 能力的 GameAgent，升级为可重复验证、可定位问题、可安装使用、可扩展接入的工程化系统。

本阶段不再以增加 Agent 智能为主，而是回答：

> **如何证明系统长期没有退化，并让新开发者或新 Adapter 可靠接入？**

## 主要范围

- Scenario-based Evaluation；
- MiniWorld 或等价测试 Environment；
- 核心 AgentTurn / Memory / Tool / Action 指标；
- Trace 查询 CLI 或轻量 Viewer；
- 完善 Architecture checks 并固化为 CI merge gates；
- Runtime / Adapter packaging；
- 配置、安装和故障排查文档；
- 新 Adapter 接入规范与 contract tests。

## 非目标

```text
云平台化
复杂多租户控制台
Plugin Marketplace
分布式执行
Multi-Agent 社会模拟平台
```

## 完成条件

- 核心行为可以通过重复 Scenario 自动评估；
- Runtime 回归能够被 CI 发现；
- Trace 可以按 Turn / Entity / Failure 快速查询；
- 新开发者能够按文档启动 Runtime 并安装 Adapter；
- 新 Adapter 可以通过 Protocol contract tests 验证基本兼容性；
- 架构依赖违规能够自动阻止合并。

## 阶段结束 Review

重点判断 GameAgent 是否已经具备稳定 v0.x 产品形态，以及下一轮应该优先扩展 Agent 能力、游戏 Adapter 还是部署体验。

---

# 19. 跨阶段不变量

无论处于哪个 Phase，都必须保持：

```text
Agent owns intent.
Runtime owns cognition.
Protocol owns contracts.
Adapter owns translation.
Game owns execution.
```

以及：

```text
EnvironmentSession != AgentSession
GameEvent != Observation
Capability != Policy != Tool
message_id != action_id != turn_id
Observer != Functional Hook
Action != synchronous function
AgentStep belongs to AgentTurn
Entity identity != Agent Definition
Agent Definition / Archetype != Agent Instance Descriptor
Observation narrow waist != universal game state schema
Available Tools == current AgentTurn capability view
Trigger admission != hardcoded game-specific event_type
Runtime tool policy != hardcoded game-specific capability name
```

任何 Phase 如果需要破坏这些不变量，必须先 Review Architecture v0.7，并形成正式 Architecture Decision。

---

# 20. 每阶段固定交付物

从 Phase3 开始，每阶段至少应形成：

1. PhaseN 技术开发与验收方案；
2. 必要的专题设计文档；
3. 自动测试与真实或等价 Environment 验收记录；
4. 阶段小结或学习回顾；
5. Architecture / Protocol / Roadmap Review 结论；
6. Architecture boundary check 与 protocol generated-code 一致性检查（至少：runtime 不依赖 adapters/、adapter 不依赖 runtime/internal/、runtime 不引用具体游戏 API、proto 源与生成代码一致）。

阶段结束状态应明确为：

```text
Accepted
Accepted with Known Limitations
Needs Follow-up
```

不能只以“代码已经写完”作为阶段完成依据。

Phase7.0–Phase7.5 可以共享一份 Phase7 Context Subsystem 总纲，但不要求一次性写完所有详细方案。每个 Phase7.x 开工前必须有自己的技术开发与验收方案或独立章节，并保留独立验收状态、测试证据和阶段结论；只有形成新的架构决策时，才新增独立 ADR 或专题设计文档。

## 阶段依赖门

为避免后续阶段在关键 contract 未定的情况下开工，以下依赖门必须显式确认：

```text
进入 Phase4 前
    Agent Identity Contract 必须 Accepted。

进入 Phase5 前
    可复用 Deterministic TestEnvironment 必须可用。

进入 Phase5.5 前
    Phase5 必须 Accepted。

进入 Phase6 implementation 前
    Phase5.5 必须 Accepted。
    Phase5.6 必须 Accepted 或 Accepted with Known Limitations。
    ContextFact memory projection 必须 Accepted。
    Tool Policy Generalization / ActionRequest source correlation / TurnCompletion / Interaction Guard 边界必须明确。
    Async Action Protocol Strategy ADR 必须 Accepted。

进入 Phase6.5 implementation 前
    Phase6 async lifecycle 必须 Accepted。
    Stardew 对话 UI 实机问题必须有可复现 trace 或 Adapter log。
    Phase6.5 技术开发与验收方案必须 Accepted。

进入 Phase7.0 Review / Design 前
    Phase6.5 必须 Accepted。
    Architecture v0.7、Context Architecture 和当前代码基线必须作为评审输入。
    Phase7.0 的输出必须确认 Phase7 共享边界已在总阶段规划中归属清楚，且后续 Phase7.x 可以分别写方案、分别开发、分别验收。

进入 Phase7.1 implementation 前
    Phase7.0 必须 Accepted。
    Game Definition / Agent Definition 最小模型必须稳定。
    Canonical / Validated Target EntityRef 的 owner 必须明确。

进入 Phase7.2 implementation 前
    Phase7.0 必须 Accepted。
    Tool View snapshot 与现有 Tool Scheduler 的接线方案必须明确。

进入 Phase7.3 implementation 前
    Phase7.1 和 Phase7.2 必须 Accepted。
    Definition loading 与 Tool View snapshot 必须可测试。

进入 Phase7.4 implementation 前
    Phase7.3 必须 Accepted。
    Context Projection 的段落结构必须稳定。
    Phase7.2 已完成 Tool View 生命周期、lookup 语义和最小工具正确性诊断。

进入 Phase7.5 implementation 前
    Phase7.4 必须 Accepted。
    Stardew Definition 内容与实机验收口径必须明确。

进入 Phase8.1 implementation 前
    Phase7.5 已完成必要验收，或其 known limitations 不影响 Memory 持久化开发。
    Context 主链路必须稳定。

进入 Phase8.2 implementation 前
    Phase8.1 必须 Accepted，保留其已知限制。
    History 来源、迁移、预算、摘要与时间合同完成技术评审。

进入 Phase8.3 implementation 前
    Phase8.2 必须 Accepted。
    检索、中文样例、留存与删除后能力合同完成技术评审。

进入 Phase9 implementation 前
    采用 main @ daf4f98 的 Phase8.1–8.3 Accepted 基线，保留已有验收限制。
    持久来源必须可按 AgentSession scope 读取，不增加新的 Memory 功能前置。
    TaskService 内部服务、Runtime Tools、游戏时钟、短 Turn、当下世界操作及检查点职责明确。
    9.0 验证跨地图、当前时刻日程恢复和保存交接；9.1–9.5 连续开发，9.5 集中 review。

进入 Phase10 implementation 前
    Phase9 必须 Accepted 或 Accepted with Known Limitations。
    持久来源必须可按 AgentSession scope 读取，不增加新的 Memory 功能前置。
    跨 Mod 集成边界已确立，首个第三方 mod 能力的验收口径明确。
    Runtime 运行形态（数据目录、端口、配置路径）可配置，不依赖 cwd。

进入 Phase11 implementation 前
    Phase10 必须 Accepted。
    Runtime 与 Adapter 行为、产品化入口和第二个 Adapter 均已稳定，具备真实运行场景作为恢复验证输入。

进入 Phase12 implementation 前
    Phase11 必须 Accepted。
    Evaluation / DX / 产品化目标必须基于已稳定的 Runtime、Adapter 和 Recovery 行为。
```

---

# 21. 暂不绑定固定 Phase 的候选能力

以下能力保留为未来候选，等核心 Harness 出现真实需求后再进入阶段规划：

```text
复杂 Goal Planner
复杂 Task 工作流 / DAG、循环计划与分布式调度
Advanced Permission / Safety Policy
Long-term semantic memory
Vector retrieval
Canonical dialogue retrieval
World State Projection
Agent Cognitive State
超出个人历史字面检索的 Experience Retrieval
Definition hot reload
Definition 多后端 Resolver
Skills
Multiple concurrent actions
Multi-Agent collaboration
Supervisor / Sub-agent
更多游戏 Adapter
完整 Event replay / Action recovery
Persistent continuation / resume token
Remote Runtime / Authentication
Cloud deployment
```

---

# 22. 一句话 Roadmap

```text
Phase1
跑通真实游戏 Agent vertical slice

Phase2
让 AgentTurn 可观察、可配置、失败可收敛、Tool 可动态扩展

Phase3
稳定实体身份并泛化 Stardew Adapter

Phase4
让 Agent 在多个 Turn 之间拥有隔离的上下文与短期记忆
并建立最小确定性测试底座

Phase5
让一个 Turn 可以安全执行多个有界 Step

Phase5.5
让 Stardew Adapter 提供结构化、成熟、可测试的当前游戏事实

Phase5.6
让 Stardew Adapter 提供对话 UI、玩家回复事件、ContextFact 和跨 Turn conversation

Phase6
让 Turn 能等待和恢复长时间游戏 Action
并补齐 Tool Policy、ActionRequest source correlation、TurnCompletion、Interaction Guard 和异步 Action 协议策略

Phase6.5
让 Stardew 玩家点击 NPC 默认进入稳定、可回复、可主动结束且不重入错乱的对话面

Phase7
让 Context 成为 Runtime 的正式主链路，并拆成 Phase7.0–Phase7.5 小步验收

Phase7.0
确认 Phase7 总体边界、文档权责、阶段拆分和进入 Phase7.1 的验收口径

Phase7.1
让 canonical Target EntityRef、Game Definition 和 Agent Definition 根据 game_id / definition_id 真正进入模型输入

Phase7.2
让模型看到的工具和实际执行的工具来自同一份 EnvironmentSession Tool View snapshot，并记录最小工具诊断

Phase7.3
把 Event、ContextFacts、Observation、Memory、Transcript、Definition 和 Tool View 组合成结构化 Context Projection

Phase7.4
让 Context 可以按优先级稳定选择、裁剪，并记录 BuildReport 和综合诊断

Phase7.5
用 Stardew Definition 内容校对、Fake / Recording Provider 和真实 NPC 对话验收 Context 主链路

Phase8
按 AgentSession 分阶段建立持久来源、有界历史上下文、原文检索与留存管理

Phase8.1
SQLite Recent Memory 持久化、隔离、幂等与恢复读取

Phase8.2
完整约定终态 History、同步摘要与近期原文，恢复已提交历史连续性

Phase8.3
中文历史原文检索与可选留存清理，明确删除后的能力边界

Phase9
以 Runtime 持久 Task、跨天唤醒和最小检查点完成赴约、见面或超时离开及结果记忆的闭环

Phase10
让能力可以来自第三方 mod，让外部用户能装起来用，并让第二个游戏验证跨游戏泛化

Phase11
让 Environment 可以重连、恢复，并让 pending operation 收敛到明确状态

Phase12
让系统可以被重复评估、可靠交付和持续扩展
```

> **Roadmap 的目标不是一次预测所有未来实现，而是保证每一阶段只增加一层可独立验证的复杂度。**
