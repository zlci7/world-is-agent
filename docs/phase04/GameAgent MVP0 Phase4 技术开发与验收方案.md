# GameAgent MVP0 Phase4 技术开发与验收方案

> **Status:** Accepted — Post-Phase4 compatibility revision
> **Date:** 2026-08-22
> **Scope:** Context Boundary + Short-term Memory + Deterministic TestEnvironment
> **Architecture Baseline:** GameAgent Runtime Architecture v0.3
> **Roadmap Baseline:** GameAgent Phase3–Phase8 阶段规划 v0.4
> **Protocol Baseline:** gameagent.protocol.v1alpha2（以 Phase3 Accepted 实现为准）

------

# 1. 阶段目标

Phase3 建立稳定的：

```text
AgentSessionKey
=
GameID
+
WorldID
+
EntityID
```

并使多个实体可以通过同一套 Runtime AgentTurn 链路独立路由和串行执行。

Phase4 在此基础上解决：

> **同一个 AgentSession 第二次被唤醒时，能否使用此前 Turn 留下的有限相关上下文，同时保证不同 AgentSession 之间完全隔离？**

本阶段主要证明三个能力：

```text
1. Context 有明确 Scope
   Runtime 能明确区分：
   静态定义、当前世界事实、Agent 状态、历史 Memory、
   当前 Observation 和当前 Event。

2. AgentSession 具有跨 Turn 的短期 Memory
   同一个 AgentSession 的后续 Turn 能读取前一 Turn 留下的信息；
   不同 world / entity 的 Memory 不串线。

3. AgentTurn 可以在确定性环境中被重复验证
   不依赖真实 Stardew、真实 LLM 或 sleep，
   即可脚本化多 Entity、多 Turn、Observation、ActionResult 和失败路径。
```

Phase4 最小 vertical slice：

```text
Turn #1
玩家触发 Abigail
    ↓
Resolve AgentSessionKey
    ↓
Observe
    ↓
ContextBuilder
    ├── 当前 Event
    ├── 当前 Observation
    ├── Recent Memory = []
    └── Tools
    ↓
Model
    ↓
Tool / Action
    ↓
Action succeeded
    ↓
MemoryProjector
    ↓
MemoryStore.Append(Abigail, Turn #1)
    ↓
Trace context_updated / context_update_failed
    ↓
Turn completed

Turn #2
玩家再次触发 Abigail
    ↓
Resolve 同一个 AgentSessionKey
    ↓
Observe
    ↓
ContextBuilder
    ├── 当前 Event
    ├── 当前 Observation
    ├── Recent Memory = [Turn #1]
    └── Tools
    ↓
Model Request 可以看到 Turn #1 的历史信息
```

同时：

```text
Linus Turn
    ↓
Recent Memory 不包含 Abigail 的记录
```

以及：

```text
World_B / Abigail
    ↓
Recent Memory 不包含 World_A / Abigail 的记录
```

------

# 2. Phase4 开发前置

## 2.1 Mandatory Dependency Gate

Phase4 不应在以下条件未满足时进入正式实现：

```text
Phase3 Accepted

且：

Agent Identity Contract Accepted

且：

AgentSessionKey =
GameID + WorldID + EntityID

已经有自动测试证明 identity invariant。
```

Phase4 不重新定义 Agent identity。

MemoryStore、ContextBuilder、TestEnvironment 均直接使用 Phase3 已解析完成的 `AgentSessionKey`。

------

## 2.2 预期 Phase3 基线

本方案假设 Phase3 最终实现至少具有：

```text
gameagent.protocol.v1alpha2

GameEvent.world_id
GameEvent.target_entity_id

ObserveRequest.world_id
Observation.world_id

ActionRequest.world_id

AgentSessionKey

AgentSessionResolver

per-AgentSession ExecutionLane

同一 AgentSession Turn 串行
不同 AgentSession 可以并行

world_mismatch 防护
```

如果 Phase3 最终实现事实与此不同，Phase4 开工前必须先更新本方案。

------

## 2.3 当前代码事实边界

本方案已按 Phase3 当前实现进行一次 code-aware 修订。

Phase4 可以依赖的事实：

```text
gameagent.protocol.v1alpha2 已接入 Runtime / Stardew Adapter

AgentSessionKey 已作为 Runtime identity scope 使用

Gateway 已完成 target_entity_id admission、session routing、
per-session lane 串行与 world_mismatch 防护

Trace 已记录 turn_started / turn_completed / turn_failed 等 turn 生命周期事件
```

仍需在实现前由代码确认并以最小改动收敛的边界：

```text
AgentLoop 当前 Request 构造位置

TurnTracer 暴露 turn_id 的方式

测试夹具应从哪些现有 gateway fake/helper 中抽取

Runtime Config 的最终字段归属
```

因此本文中的 Go 类型仍是 implementation sketch；
最终代码签名以 Phase3 代码附近的既有风格为准。

------

# 3. Phase4 核心设计原则

## 3.1 Identity 只来自 AgentSessionKey

任何 Memory / Context 的 Agent 级数据都必须使用：

```text
GameID
WorldID
EntityID
```

作为 Scope。

禁止：

```text
session_id
display_name
localized name
connection pointer
ExecutionLane pointer
```

参与 Memory identity。

------

## 3.2 静态定义与动态实例分离

Context 的长期模型采用：

```text
Game Definition
        +
World Instance

Agent Definition
        +
World-scoped Agent State
        +
World-scoped Memory

        ↓
Current Observation
        ↓
Current Agent Context
```

但 Phase4 不要求一次实现所有 Store。

------

## 3.3 Game 是当前世界事实的 Source of Truth

Runtime Memory 不是游戏状态数据库。

例如：

```text
Memory:
昨天 Abigail 在湖边。

Current Observation:
Abigail 当前在家。
```

模型必须以：

```text
Current Observation
```

作为当前事实。

Memory 只回答：

> 过去发生过什么。

不得把历史 Observation 当成当前世界状态重新执行。

------

## 3.4 Memory != Agent State != Trace

三者职责不同：

```text
Agent State
    当前 Agent 实例是什么状态。

Memory
    Agent 过去经历过什么。

Trace
    Runtime 当时具体如何执行。
```

Phase4 不允许：

```text
直接把 Trace JSONL 当 Memory 使用。
```

也不允许：

```text
通过查询 Trace Store 拼当前 Prompt。
```

Trace 是执行观测系统。

Memory 是会影响未来 Agent 行为的 functional state。

------

## 3.5 Memory 是 Harness Peripheral

Memory 不成为：

```text
MemoryAgent
ReflectionAgent
MemoryPlanner
```

第一版保持：

```text
AgentLoop
    ↓
ContextBuilder
    ↓
MemoryStore
```

MemoryStore 是可替换 peripheral。

------

## 3.6 In-Memory First

Phase4 的目标是验证：

```text
Memory Contract
Context Contract
AgentSession Isolation
```

不是验证数据库。

P0 Backend：

```text
InMemoryMemoryStore
```

Phase4 不冻结：

```text
SQLite schema
PostgreSQL schema
Vector schema
Embedding model
```

简单本地持久化可以作为 P2 实验，但不是验收门。

------

# 4. Phase4 范围

## 做

```text
1. P0 Context Scope Contract
   明确不同 Context 数据的 Scope、生命周期和 Source of Truth。

2. 最小 ContextBuilder
   将当前 Event / Observation / Recent Memory / Static Context / Tools
   组合成稳定、可测试的 Model Context。

3. Recent Turn Memory
   每个成功 AgentTurn 生成一个有限、结构化的 short-term MemoryRecord。

4. MemoryStore
   AgentSessionKey-scoped、线程安全、可替换。

5. Recent Memory Retrieval
   第一版仅按 AgentSessionKey + 最近 N 条读取。
   不做 semantic retrieval。

6. Memory read-after-write guarantee
   同一 AgentSession 的下一 Turn 开始前，
   前一 completed Turn 的 Memory update 已经结束。

7. Memory failure fail-open
   Memory 子系统故障不能让原有 One-Turn 游戏链路失效。

8. Context / Memory Trace
   能看出本 Turn 加载了多少 Memory，以及是否成功更新。

9. Deterministic TestEnvironment
   从现有 fake adapter / fake Environment 演进，
   支持多 Entity、多 Turn、Observation、ActionResult、失败注入。

10. 保持 Memory 可关闭
    MemoryEnabled=false 时继续跑 Phase3 One-Turn 链路。
```

------

## 不做

```text
向量数据库
Embedding
Semantic Search
Hybrid Search
长期 Memory
Core Memory 自动提炼
Memory Reflection
Memory Consolidation
Knowledge Graph
复杂人格演进
完整 World State Store
持久 AgentState Store
Runtime 维护游戏真实状态副本
LLM 自动总结 Memory
复杂 Context Compression
精确 Tokenizer Budget
Prompt Cache
Multi-step AgentTurn
Runtime Memory Tool
memory_search Tool
memory_write Tool
Goal
Scheduled Goal
Reconnect
跨 Runtime restart 的 Memory 恢复保证
完整 MiniWorld
Scenario Evaluation Framework
```

------

# 5. P0 Mandatory Deliverable：Context Scope Contract

Phase4 必须正式冻结下面的逻辑 Scope。

| Context 类型           | Scope                            | Phase4 数据来源       | Phase4 状态    |
| ---------------------- | -------------------------------- | --------------------- | -------------- |
| Runtime Policy         | Runtime                          | Runtime config        | P0             |
| Game Definition        | `game_id`                        | 静态配置              | P1 可选        |
| World Instance Context | `game_id + world_id`             | 当前 Observation 为主 | 只冻结概念     |
| Agent Definition / Archetype | `game_id + definition_id`  | 静态配置              | P1 可选        |
| Agent Instance Descriptor | `game_id + world_id + entity_id` | Adapter / 静态映射 / 当前 Observation | 只冻结概念 |
| Agent State            | `game_id + world_id + entity_id` | 当前 Observation 为主 | 不建独立 Store |
| Agent Memory           | `game_id + world_id + entity_id` | Runtime MemoryStore   | P0             |
| Observation            | 当前 AgentTurn                   | Adapter               | P0             |
| Event                  | 当前 AgentTurn                   | Adapter               | P0             |
| Tools                  | 当前 AgentTurn                   | Capability Registry   | P0             |

`Runtime Policy` 在本方案中表示 Runtime 全局行为边界：

```text
允许使用哪些工具 / capability
工具选择约束
默认语言
默认输出长度
全局安全与格式要求
```

它不是每个 NPC 的人格、背景或说话风格。

每个 NPC 不同的说话方式、人物设定、背景知识应属于：

```text
Agent Definition / Archetype
scope = game_id + definition_id

Agent Instance Descriptor
scope = game_id + world_id + entity_id
```

Phase4 可以先用 Runtime Policy 提供默认 prompt 行为；
未来若 `PromptConfig.NPCStyle` 需要按 NPC 覆盖，应迁移或映射到 `AgentDefinition`，
而不是扩大 Runtime-scope policy 的语义。

该迁移不属于 Phase4 P0。

Phase4 保持现有 `PromptConfig` 配置路径可用，
只在 Context Scope Contract 中冻结语义边界；
per-agent prompt / persona 覆盖留到 Phase5/6 或后续 Static Definition 阶段。

Post-Phase4 compatibility review 已进一步明确：

```text
Entity != Agent Definition
Agent Definition / Archetype = game_id + definition_id
Agent Instance Descriptor = game_id + world_id + entity_id
```

Stardew 的固定 NPC 可以让 `definition_id == entity_id`；
动态实体游戏不应依赖这个等式。

核心关系：

```text
Static Context
    Runtime Policy
    Game Definition
    Agent Definition

Semi-Stable Context
    Recent Memory

Volatile Context
    Current Event
    Current Observation
    Current Tools
```

Phase4 不因为定义了：

```text
World Instance Context
Agent State
```

就立即创建：

```text
WorldStore
AgentStateStore
```

第一版优先使用 Adapter Observation 中的当前事实。

------

# 6. ContextBuilder

## 6.1 职责

Phase4 将现有 Prompt 拼接逻辑收敛为一个最小 Context 构造边界。

逻辑链路：

```text
AgentTurn
    ↓
Observe
    ↓
ContextBuilder.Build(...)
    │
    ├── Runtime Policy
    ├── Game Definition（若配置）
    ├── Agent Definition（若配置）
    ├── Recent Memory
    ├── Current Event
    ├── Current Observation
    └── Tools
    ↓
AgentContext
    ↓
Prompt / ModelRequest Rendering
    ↓
ModelProvider
```

ContextBuilder 不负责：

```text
调用 LLM
执行 Tool
写 Memory
Resolve Agent identity
操作 gRPC
读取 Stardew API
```

------

## 6.2 最小 AgentContext

示意：

```go
type AgentContext struct {
    SessionKey AgentSessionKey

    RuntimePolicy string

    GameDefinition  *GameDefinition
    AgentDefinition *AgentDefinition

    RecentMemories []MemoryRecord

    Event       EventContext
    Observation ObservationContext

    Tools []ToolDefinition
}
```

P0 倾向直接使用现有 Protocol 类型作为输入字段：

```go
Event       *protocolv1alpha2.GameEvent
Observation *protocolv1alpha2.Observation
```

不要为了 Phase4 提前创建中间 DTO。
只有当 Context Renderer 需要稳定、裁剪后的展示模型时，
再在 `runtime/internal/context` 内部创建小型 view struct。

Context 的关键不是这个 struct 的具体字段，而是：

> 每个字段必须有明确 Scope 和 Source。

------

## 6.3 构造顺序

推荐固定顺序：

```text
1. Runtime Policy

2. Game Definition
   “这个游戏通常如何运作”

3. Agent Definition
   “这个角色是谁”

4. Recent Memory
   “这个 Agent 近期经历过什么”

5. Current Event
   “为什么这次被唤醒”

6. Current Observation
   “现在世界真实是什么样”

7. Tools
   “现在可以做什么”
```

其中：

```text
Current Observation
```

对当前事实具有最高优先级。

Context 中应明确表达：

```text
Recent Memory 是历史信息，可能已经过时。
若 Memory 与 Current Observation 冲突，以 Current Observation 为准。
若 Recent Memory 来自今天，且当前游戏时间没有明显推进，
应将其视为附近的对话上下文，而不是玩家离开后再次回来。
```

这段语义属于 Renderer hardcoded invariant。

它不是 Runtime Policy 可配置项，也不应被 `PromptConfig` 覆盖。
原因是它表达的是架构边界：

```text
Historical Memory < Current Observation
```

而不是某个 NPC 的说话风格或某个游戏的可调偏好。

这条规则用于避免同一天短时间内连续点击 NPC 时，
模型仅因为看到了上一轮 Memory 就生成“又来了 / 又见面了”之类的错误时间暗示。

------

## 6.4 ContextBuilder 不做复杂 Selector

成熟 Context Engine 未来可以：

```text
Persistent World State
        ↓
Context Selector
        ↓
Relevant World Context
```

但 Phase4 不实现完整 Relevant Context Selection。

第一版的“选择”只有：

```text
AgentSessionKey scope
+
Recent N Memory
+
有限 Context size
```

------

## 6.5 Context Budget

Phase4 不引入 provider-specific tokenizer。

第一版只需要确定性限制：

```text
RecentMemoryLimit
MemoryContextSizeLimit
```

`RecentMemoryLimit` 的单位是 records 数。

`MemoryContextSizeLimit` 的单位是 Recent Memory section 渲染后的 UTF-8 字节数，
不是 token 数，也不是 records 数。

如果超过 `MemoryContextSizeLimit`：

```text
按最新优先保留 MemoryRecord 的渲染摘要，
丢弃更旧记录，
直到渲染后字节数不超过限制。
```

`MemoryContextSizeLimit` 是 soft limit。

若单条最新 MemoryRecord 渲染后已经超过该限制：

```text
至少保留最新 1 条 MemoryRecord，
允许 Recent Memory section 临时超过 MemoryContextSizeLimit。
```

Phase4 不做 LLM summarization，也不把完整 `MemoryRecord` 原样塞进 prompt。

Renderer 只输出模型决策需要的短摘要，例如：

```text
- today 06:20: said "..."
- previous day Y1 S1 D2 18:20: said "..."
```

完整结构化记录属于 Runtime Memory / 未来 Experience 层，
Model Context 只使用有限 projection。

具体默认数字在读取 Phase3 最终 prompt / config 代码后冻结。

必须保证：

```text
Context 不会因为无限 recent turns 线性增长。
```

超过限制时：

```text
优先保留最新 Memory；
旧 Memory 从本轮 Context 中排除，
但不等于从 MemoryStore 删除。
```

------

# 7. Short-term Memory Contract

## 7.1 Phase4 Memory 定位

第一版 Memory 不是：

```text
“Abigail 的完整长期人格记忆系统”
```

而是：

> **Recent Turn Memory。**

它解决：

```text
刚才发生过什么？

上一轮我做了什么？

玩家刚刚触发过什么？

上一轮我说过 / 做过什么？
```

不尝试解决：

```text
几个月前最相关的事件是什么？

哪些 Memory 最重要？

哪些 Memory 应被遗忘？

如何形成长期人格变化？
```

------

## 7.2 MemoryRecord

P0 使用结构化记录，而不是直接保存 LLM 总结文本。

示意：

```go
type MemoryRecord struct {
    MemoryID string

    SessionKey AgentSessionKey

    SourceTurnID        string
    SourceEventID       string
    SourceEventSequence uint64

    EventType string
    GameTime  *GameTimeSnapshot

    Outcome TurnOutcome

    CreatedAt time.Time
}
```

其中：

```go
type TurnOutcome struct {
    ToolName      string
    ToolArguments map[string]any

    ActionStatus string
}
```

`GameTimeSnapshot` 是 Runtime 内部的最小游戏时间快照：

```go
type GameTimeSnapshot struct {
    Year   int32
    Season int32
    Day    int32
    Hour   int32
    Minute int32
    Tick   int64
}
```

它来自 Protocol `GameTime`，但不是新的 Protocol message。
如果某个游戏没有完整时间系统，可以只填可用字段；
如果完全没有游戏时间，则保持 `nil`，Renderer 退化为按顺序表达 recent interaction。

P0 实现要求稳定写入并测试：

```text
MemoryID
SessionKey
SourceTurnID
SourceEventID
SourceEventSequence
EventType
GameTime
Outcome.ToolName
Outcome.ToolArguments
Outcome.ActionStatus
CreatedAt
```

Phase4 当前仍保持：

```text
一个成功 AgentTurn -> 一条 Recent MemoryRecord
```

因为当前 AgentTurn 仍是 one-model-call / one-tool-call。
未来若支持一个 Turn 内多个 tool call，
可以把 `Outcome` 自然升级为 `[]TurnOutcome`，
或引入独立 `TurnExperience`；
Phase4 不提前实现该结构。

------

## 7.3 为什么不保存完整 Observation

Phase4 默认不把完整历史 Observation 写成 Memory。

原因：

```text
Observation 是过去某个时间点的世界快照。

长期重复保存：
    数据量大；
    很多字段马上过时；
    容易让模型把历史状态误认为当前状态。
```

因此 RecentTurnMemory 优先记录：

```text
trigger
+
interaction / tool outcome
+
必要 metadata
```

当前世界状态仍通过：

```text
新的 Observation
```

获得。

------

## 7.4 Memory 不由 LLM 自动总结

P0 MemoryRecord 必须能够：

```text
确定性地产生。
```

不增加一次额外模型调用：

```text
Turn completed
↓
LLM summarize memory
↓
Save
```

这种模式不属于 Phase4。

原因：

```text
增加时延
增加成本
增加 nondeterminism
引入新的失败路径
使测试复杂化
```

第一版可以由确定性的：

```text
TurnMemoryProjector
```

从：

```text
Event
ToolCall
ActionResult
GameTime / EventSequence
```

投影出 MemoryRecord。

`GameTime` 优先来自 `GameEvent.game_time`。
如果某个 Adapter 未来无法在 Event 上提供时间，
可以用 `Observation.game_time` 作为 fallback；
Phase4 Stardew 路径中 Event 已携带 `game_time`。

------

## 7.5 SourceTurnID 来源

`MemoryRecord.SourceTurnID` 必须来自当前正在执行的 AgentTurn 上下文。

禁止从：

```text
traces.jsonl
TurnTracer 已落盘事件
日志字符串
```

反向解析 `turn_id`。

Phase4 开工时必须先补齐一个稳定的 turn id 来源。

本阶段采用方案 B：

> AgentLoop 先生成 `turnID`，再把同一个 `turnID` 同时交给 Trace 和 Memory 投影链路。

```go
turnID := idgen.New("turn")
tracer := trace.NewTurnTracerWithID(recorder, traceCtx, turnID)
```

不采用“给 `TurnTracer` 接口增加 `TurnID()` 再由 Memory 读取”的方案。
原因是 Memory 不应把 Trace 对象当作 source；
Trace 和 Memory 应共享上游 `turnID`，而不是让 Memory 反向依赖 Trace。

验收要求：

```text
TraceEvent.turn_id
==
MemoryRecord.SourceTurnID
```

这个顺序是 Phase4 的 P0：

```text
先拿稳定 turn_id
↓
执行 AgentTurn
↓
成功 action 后投影 MemoryRecord
↓
写入 MemoryStore
↓
记录 context_updated / context_update_failed
↓
记录 terminal turn_completed / turn_failed
```

------

# 8. MemoryStore

## 8.1 最小接口

示意：

```go
type MemoryStore interface {
    Append(
        ctx context.Context,
        record MemoryRecord,
    ) error

    Recent(
        ctx context.Context,
        key AgentSessionKey,
        limit int,
    ) ([]MemoryRecord, error)
}
```

要求：

```text
Append:
    写入一条 AgentSession-scoped Memory。

Recent:
    只返回指定 AgentSessionKey 的最近记录。
    最多返回 limit 条。
    按 Append / 插入顺序返回，最新记录在末尾。
    若记录数超过 limit，保留最新的 limit 条，
    并保持这些记录原本的 Append 顺序。
```

`Recent` 的返回顺序是接口 contract。
Renderer 可以据此按时间从旧到新渲染 Recent Memory，
不应在调用侧重新猜测排序语义。

`CreatedAt` 是 metadata，不是 P0 排序真源。
原因是 Phase4 测试不依赖 wall clock；
固定时钟下多条记录可能拥有相同 `CreatedAt`。

如果未来 backend 使用显式排序，必须保证：

```text
Append / 插入顺序是稳定 tiebreak。
```

P0 不需要：

```text
Search(query string)
VectorSearch(...)
DeleteBySimilarity(...)
```

------

## 8.2 InMemoryMemoryStore

P0 唯一 mandatory backend：

```text
InMemoryMemoryStore
```

逻辑结构可以近似：

```text
map[AgentSessionKey][]MemoryRecord
```

但实现必须线程安全。

Phase4 的默认 InMemory backend 必须有 per-AgentSession 保留上限：

```text
effective_max_records_per_session = max(20, RecentMemoryLimit)
```

超过该上限时，淘汰最旧 MemoryRecord，保留最新记录。
这样既避免默认开启 Memory 后进程内历史无限增长，
也保证 `RecentMemoryLimit` 不会被 store 层固定上限隐式截断。

并发要求：

```text
不同 AgentSession 可以并发读写。

同一 AgentSession 由于 ExecutionLane 已串行，
正常 AgentTurn 写入不存在两个 active writer，
但 MemoryStore 本身仍不能依赖该隐含条件保证 data race safety。
```

------

## 8.3 Store 生命周期

MemoryStore 是：

```text
Runtime-scoped
```

不是：

```text
EnvironmentSession-scoped
ExecutionLane-scoped
```

因此逻辑上：

```text
EnvironmentSession A
↓
AgentSessionKey = Farm001 / Abigail
↓
Memory

连接结束

EnvironmentSession B
↓
同一 AgentSessionKey
↓
仍应该解析到同一 Memory namespace
```

Phase4 使用 InMemory backend 时：

```text
Runtime process 不重启
→ Memory 可以继续存在

Runtime process 重启
→ Memory 丢失
```

这是 Phase4 Accepted Known Limitation。

跨进程持久恢复属于 Phase7。

------

# 9. Memory 写入时机与一致性

## 9.1 只记录成功 Action 对应的 Turn

P0：

```text
Action succeeded
    → MemoryProjector
    → MemoryStore.Append
    → context_updated / context_update_failed
    → turn_completed

turn_failed
    → 不写 Agent Memory
    → 失败信息只进入 Trace

Action failed / rejected
    → 不写 P0 Agent Memory
    → 失败信息只进入 Trace
```

未来若发现 Agent 应记住失败经历，再单独扩展 Memory kind。

------

## 9.2 Read-after-write Guarantee

这是 Phase4 的核心 invariant。

同一个 AgentSession：

```text
Turn A
    ↓
Action succeeded
    ↓
MemoryStore.Append(A)
    ↓
Append 完成
    ↓
Turn A 返回 / ExecutionLane 释放
    ↓
Turn B 开始
    ↓
MemoryStore.Recent(...)
    ↓
必须可以看到 Turn A
```

禁止：

```text
Turn A memory 异步后台写入

同时：

Turn B 已开始 BuildContext
```

否则会出现：

```text
同一 AgentSession 串行 Turn，
但 Context 仍然看不到刚结束的上一 Turn。
```

因此 Phase4 的 Memory update：

> 必须在同一 AgentSession lane 释放下一任务之前完成。

实现上只要求同步发生在当前 `HandleEvent` 返回之前；
不需要为了 Memory 再新增一层 lane barrier。

------

## 9.3 为什么仍然不让 Memory 写失败 Turn

Memory 是 peripheral。

如果：

```text
Action 已经在游戏中成功

但 MemoryStore.Append 失败
```

不应该把一个已经成功发生的游戏副作用改写成：

```text
turn_failed
```

P0 语义：

```text
Memory Append 成功
    → context_updated

Memory Append 失败
    → context_update_failed
    → Turn 仍保持原有 completed 状态
```

`context_updated` / `context_update_failed` 是 terminal 之前的非终态 trace 事件。
它们不能取代 `turn_completed`。

因此 Append 失败时的 trace 顺序必须是：

```text
action_result_received
↓
context_update_failed
↓
turn_completed
```

不能是：

```text
action_result_received
↓
turn_completed
↓
context_update_failed
```

因为 `turn_completed` 是 terminal event，之后 `TurnTracer` 应拒绝继续写入同一 Turn 的普通事件。

------

# 10. Memory / Context 失败语义

## 10.1 Memory Load Failure

```text
MemoryStore.Recent 失败
```

Phase4 默认：

```text
fail-open
```

即：

```text
trace context_load_failed
↓
RecentMemory = []
↓
继续当前 One-Turn
```

Memory 暂时不可用不能导致：

```text
NPC 完全不能响应玩家。
```

------

## 10.2 Memory Update Failure

```text
MemoryProjector 失败
或
MemoryStore.Append 失败
```

默认：

```text
记录 context_update_failed
↓
当前 Turn terminal result 不改变
```

也就是说：

```text
Action 已成功
↓
MemoryProjector 返回 error
↓
context_update_failed
↓
turn_completed

Action 已成功
↓
MemoryStore.Append 返回 error
↓
context_update_failed
↓
turn_completed
```

MemoryProjector 应实现为确定性纯函数，并返回 error 表达可预期失败。
panic 属于实现 bug，不作为正常业务语义。

------

## 10.3 ContextBuilder Structural Failure

以下属于真正的 Turn failure：

```text
Current Event 缺失
Current Observation 无法使用
AgentSessionKey 非法
Tool Context 无法构造
```

这些不是可选 Memory 功能，而是当前 AgentTurn 的核心输入。

------

# 11. Memory 开关

Runtime 必须支持：

```text
MemoryEnabled = true / false
```

关闭时：

```text
MemoryStore.Recent
    → skip

MemoryStore.Append
    → skip
```

其余链路保持：

```text
GameEvent
→ Observe
→ ContextBuilder
→ Model
→ Tool
→ Action
```

这用于证明：

> Memory 是可插拔 Agent capability，而不是破坏 Phase1–3 AgentTurn Core 的强耦合组件。

------

# 12. Static Definition（P1 可选）

Phase4 可以开始预留：

```text
Game Definition
Agent Definition
```

但不作为 P0 Memory 验收门。

建议边界：

```text
P0:
    AgentContext / Renderer 中预留 GameDefinition、AgentDefinition 字段。
    可以用代码常量或最小 Config 注入少量静态文本。

P1/P2:
    再实现独立 DefinitionStore、YAML schema、文件加载、热更新或校验。
```

也就是说，Phase4 可以直接实现一个非常薄的静态定义能力，
例如：

```go
type GameDefinition struct {
    GameID      string
    DisplayName string
    Summary     string
}

type AgentDefinition struct {
    EntityID    string
    DisplayName string
    Persona     string
}
```

成本较低的是：

```text
字段预留
Renderer section 预留
测试中注入固定字符串
```

成本较高、暂不作为 P0 的是：

```text
games/ 目录布局
YAML schema
DefinitionStore
per-NPC 文件管理
配置热加载
版本迁移
```

未来推荐逻辑布局可以是：

```text
games/
└── stardew-valley/
    ├── game.yaml
    ├── lore/
    │   └── world.md
    └── agents/
        ├── linus.yaml
        └── abigail.yaml
```

职责保持为：

```text
Game Definition
    game_id
    游戏规则 / 基础世界设定

Agent Definition
    game_id + definition_id
    固定人物人格 / 背景

Agent Instance Descriptor
    game_id + world_id + entity_id
    当前 world 中这个具体实体的描述性事实
```

禁止在这里保存：

```text
当前 friendship
当前 location
当前 mood
玩家当前配偶
当前剧情 flag
```

这些属于：

```text
World / Agent Instance State
```

Phase4 当前优先从 Observation 获得。

若本阶段实现静态定义，必须满足：

```text
Static Definition 只进入 Context 输入，
不保存动态状态，
不参与 AgentSession identity，
不改变 MemoryStore scope。
```

------

# 13. World Instance Context / Agent State 在 Phase4 的处理

本阶段只冻结概念，不建立完整持久层。

例如：

```text
friendship_with_player
current_location
weather
current_time
```

若 Adapter Observation 已经提供：

```text
直接使用当前 Observation。
```

不要：

```text
Observation
↓
Runtime 保存 current_location
↓
下次不 Observe，直接相信 Runtime 副本
```

Game 继续是 Ground Truth。

未来 Runtime 若保存 WorldContext：

```text
它是面向 Context 的 representation / cache / summary，
不是游戏状态权威副本。
```

------

# 14. Context Rendering

ContextBuilder 应尽量生成 provider-neutral 数据。

推荐逻辑：

```text
ContextBuilder
    ↓
AgentContext
    ↓
PromptRenderer / ModelRequestBuilder
    ↓
ModelRequest
```

而不是：

```text
ContextBuilder
直接输出 OpenAI-specific message shape
```

P0 Render 的文本顺序必须稳定，以方便 deterministic test。

示例：

```text
[Runtime Policy]
...

[Agent Definition]
...

[Recent Memory]
- today 06:20: said "..."
- previous day Y1 S1 D2 18:20: used emote "happy"

[Current Event]
...

[Current Observation]
...

[Instruction]
Use current observation as current truth.
Recent memory is historical context.
```

`[Instruction]` 中关于 Observation 优先于 Memory 的语义由 Renderer 固定注入。
Runtime Policy 可以提供额外全局行为约束，
但不能关闭或覆盖这条优先级规则。

Renderer 不应把完整 `MemoryRecord` 作为 JSON 原样渲染给模型。
它应将结构化记录投影为简短 recent interaction summary：

```text
when relation + visible action summary
```

例如：

```text
- today 06:20: said "早上好……这么早就来山里，你也很喜欢安静吧。"
```

这样既保留连续性，又避免 20 条结构化记录导致 prompt 快速膨胀。

Tools 继续走现有：

```text
ToolDefinition
```

而不是复制为自然语言 Tool 列表。

------

# 15. Context Observability

Phase4 增加最小 Trace 事件。

建议：

```text
context_loaded
context_load_failed

context_updated
context_update_failed
```

`context_loaded` 至少包含：

```text
turn_id
game_id
world_id
entity_id

memory_enabled
memory_count
memory_ids

has_game_definition
has_agent_definition
```

可选：

```text
context_build_duration
```

默认不需要把完整 Memory 文本重复写入 Trace。

Memory 已经是 functional state；

Trace 只记录：

> 本 Turn 使用了哪些 Context source。

`context_updated` / `context_update_failed` 必须发生在 terminal turn event 之前：

```text
Trace(action_result_received)
↓
MemoryStore.Append result
↓
Trace(context_updated / context_update_failed)
↓
Trace(turn_completed)
```

`turn_failed` 的 Turn 不写 P0 Memory，因此不需要 `context_updated`。

------

# 16. Trace 与 Memory 的边界

必须保持：

```text
Trace Recorder
=
Observer
```

而：

```text
Memory Update
=
Functional Hook
```

禁止实现为：

```text
TurnTracer.Record(...)
    ↓
顺便写 Memory
```

否则关闭 Trace 可能改变 Agent 行为。

正确关系：

```text
AgentTurn
    ↓
MemoryStore.Append
    ↓
结果
    ↓
Trace(context_updated / failed)
    ↓
Trace(turn_completed)
```

不是：

```text
Trace
    ↓
Memory
```

------

# 17. Deterministic TestEnvironment

## 17.1 阶段定位

Roadmap 要求进入 Phase5 前：

```text
可复用 Deterministic TestEnvironment 必须可用。
```

因此它是 Phase4 P0 Deliverable，而不是普通测试辅助代码。

目标：

> 不启动 Stardew、不调用真实 LLM、不依赖 wall clock，也可以完整驱动 AgentTurn。

Phase4 的 P0 不要求新建一个完整生产级 `TestEnvironment` framework。

推荐落地方式：

```text
先从现有 gateway integration tests 抽取 reusable test helper：
    bufconn Runtime server
    scripted Adapter stream
    scripted Provider
    ModelRequest capture
    trace / ack assertions
```

这些 helper 足够覆盖 Phase4 Memory / Context 行为后，
再判断是否需要提升为独立 `runtime/internal/testkit`。

抽取标准：

```text
如果同一套 bufconn + scripted adapter + scripted provider + request capture
只被一个测试文件使用：
    保持 test-local helper。

如果被两个或两个以上测试文件复用，
例如 Phase4 memory integration test 和 Phase5 multi-step test 都需要：
    再抽到 runtime/internal/testkit。
```

`runtime/internal/testkit` 不应在没有真实调用方时提前创建。

------

## 17.2 最小能力

Phase4 P0 测试底座必须支持脚本化：

```text
多个 AgentSessionKey

多次 AgentTurn

Observation

ActionResult

ModelResponse

MemoryStore success / failure
```

至少能够配置：

```text
当 Observe(FarmA, Abigail)
→ 返回 Observation A1

下一次 Observe(FarmA, Abigail)
→ 返回 Observation A2

当 Observe(FarmA, Linus)
→ 返回 Observation L1
```

以下能力可以作为 P1/P2 扩展，不阻塞 Phase4 Memory P0：

```text
world_mismatch 注入
timeout 注入
ActionStatusUpdate 多步状态
late result
cancel race
```

其中 world_mismatch 已由 Phase3 覆盖基础链路；
Phase4 只需要确保 Memory / Context 不削弱该防护。

------

## 17.3 Scripted Model Provider

测试底座应搭配一个确定性的 Provider：

```text
ScriptedProvider
```

支持：

```text
预置第 1 次 ModelResponse
预置第 2 次 ModelResponse
记录实际收到的 ModelRequest
```

核心价值是直接断言：

```text
Turn #2 的 ModelRequest
是否真的包含 Turn #1 Memory。
```

而不是通过：

```text
模型生成出来的话像不像“记住了”
```

判断 Memory 是否工作。

------

## 17.4 不使用 sleep 判断并发或顺序

确定性测试：

```text
channel
barrier
explicit signal
scripted response
```

而不是：

```text
time.Sleep(500 * time.Millisecond)
```

关键状态必须由明确同步信号推进。

------

## 17.5 TestEnvironment 不等于 MiniWorld

Phase4 不构建完整游戏模拟。

TestEnvironment 不需要：

```text
地图
NPC AI
Game Time 自动推进
天气模拟
Quest
Pathfinding
```

它只需要实现 Agent Core 依赖的最小 Environment contract。

------

# 18. Runtime 落地范围（建议，待代码 Review）

Phase4 可以新增：

```text
runtime/internal/context/
    context.go
    builder.go
    renderer.go
    builder_test.go

runtime/internal/memory/
    record.go
    store.go
    in_memory.go
    projector.go
    *_test.go

runtime/internal/testkit/
    environment.go
    model_provider.go
    fixtures.go
```

`runtime/internal/context/` 是本阶段明确允许的新包，
用于放置 AgentContext builder / renderer 边界。
由于 Go 标准库也叫 `context`，业务代码 import 时应使用清晰 alias，
例如：

```go
agentcontext "gameagent/runtime/internal/context"
```

`runtime/internal/testkit/` 只有在现有 gateway 测试 helper 抽取后仍然值得复用时再创建；
不要为了目录形式提前创建空 package。

判断标准同 §17.1：

```text
同一套测试装配代码被 >= 2 个测试文件复用
```

才提升为 `runtime/internal/testkit`。

------

## 18.1 AgentLoop

预计修改：

```text
原：

Observe
→ buildPrompt
→ Model
→ Tool
→ Action

Phase4：

Observe
→ MemoryStore.Recent
→ ContextBuilder.Build
    └── Context Sources
→ Model
→ Tool
→ Action
→ MemoryProjector
→ MemoryStore.Append
→ Trace(context_updated / failed)
→ Trace(turn_completed)
```

AgentLoop 不自行：

```text
读取 YAML
拼接 Memory 字符串
操作数据库
做 vector search
```

------

## 18.2 Config

建议新增最小配置：

```text
MemoryEnabled

RecentMemoryLimit

MemoryContextSizeLimit
```

具体配置归属和默认值在 Phase3 代码 review 后冻结。

------

# 19. Protocol / Adapter 范围

## 19.1 Protocol

Phase4 默认：

```text
不修改 gameagent.protocol.v1alpha2。
```

Memory 是 Runtime-internal Agent capability。

以下概念不得进入 Protocol：

```text
MemoryRecord
MemoryID
RecentMemory
ContextBuilder
AgentDefinition
```

除非 Phase4 实现时发现真实 Environment contract 缺口，并先进行独立 Protocol Review。

------

## 19.2 Stardew Adapter

P0 原则上不需要为 Memory 修改 Adapter。

Adapter 继续负责：

```text
Event
Observation
Capability
Action
```

Runtime 负责：

```text
Memory
Context
Cognition
```

如果为了 Context 验证补充少量 Observation 当前事实：

```text
relationship
season
weather
nearby entities
```

必须遵守：

> 这些字段表达 Game 当前事实，而不是 Runtime Memory。

------

# 20. Memory Backend 决策

Phase4 P0：

```text
InMemoryMemoryStore
```

允许未来：

```text
MemoryStore
    ├── InMemory
    ├── SQLite
    └── Other Local Store
```

但 Memory Domain Model 不依赖具体 Backend。

长期：

```text
MemoryRecord
    ↓
Authoritative Structured Store
    │
    ├── Recent / Structured Query
    ├── FTS Index
    └── Vector Index
```

其中：

```text
Vector Index
```

必须是：

> 可删除、可重建的 secondary index。

不得成为 Memory 唯一真源。

Phase4 不实现该层。

------

# 21. Go 单元测试

## 21.1 MemoryStore

```text
- Append 后 Recent 可读取
- Recent 按 Append / 插入顺序返回，最新记录在末尾
- Recent 顺序不依赖 CreatedAt 唯一性；
  固定时钟下多条记录顺序仍确定
- limit 生效

- FarmA / Abigail
  不会读到 FarmA / Linus

- FarmA / Abigail
  不会读到 FarmB / Abigail

- GameA / entityX
  不会读到 GameB / entityX

- 并发不同 Session Append / Recent 无 data race

- MemoryStore 生命周期独立于 ExecutionLane
```

------

## 21.2 ContextBuilder

```text
- 无 Memory 时正常构造 Context

- Recent Memory 存在时进入 AgentContext

- 只加载当前 AgentSessionKey Memory

- MemoryEnabled=false 时不读取 Memory

- MemoryStore read failure 时 fail-open

- Context section 顺序确定

- Current Observation 与 Memory 冲突时，
  Rendered Context 明确当前 Observation 是当前事实

- RecentMemoryLimit 生效

- Context size limit 生效

- Tools 继续使用 ToolDefinition，不被自然语言展开代替
```

------

## 21.3 MemoryProjector

```text
- completed Turn 可以确定性生成 MemoryRecord

- event_id / turn_id / AgentSessionKey 正确

- Event trigger 被保留

- Tool / Action outcome 被保留

- 不把完整历史 Observation 默认写入 Memory

- failed Turn 不生成 P0 MemoryRecord

- projector 返回 error 时，
  不把已成功 Action 改写成 failed，
  而是触发 context_update_failed
```

------

# 22. Deterministic Integration Tests

测试落点遵循 §17.1 的抽取标准：

```text
先在最贴近被测对象的 package 内使用 test-local helper。

当 helper 被多个测试文件复用时，
再抽取为 runtime/internal/testkit。
```

Phase4 不因为列出 Deterministic Integration Tests，
就必须先建立独立 testkit package。

------

## 22.1 同 Agent 多 Turn

```text
Turn A1:
    Abigail
    Memory = 0
    → completed

Turn A2:
    Abigail
    → Context contains A1 Memory

断言：
    ScriptedProvider 第二次收到的 ModelRequest
    包含 A1 的 Memory。
```

------

## 22.2 Entity Isolation

```text
Turn:
    Abigail → 产生 Memory A

随后：
    Linus Turn

断言：
    Linus Context 中不存在 Memory A。
```

------

## 22.3 World Isolation

```text
FarmA / Abigail → Memory A

FarmB / Abigail → Turn

断言：
    FarmB Context 不包含 Memory A。
```

------

## 22.4 Read-after-write

```text
同一个 AgentSession：

Turn A completed
↓
立即排队 Turn B

断言：
    Turn B BuildContext 时一定能读取 Turn A Memory。
```

不使用 sleep。

------

## 22.5 Memory Disabled

```text
MemoryEnabled=false

连续执行两个 Turn

断言：
    不调用 MemoryStore
    两个 Turn 都正常完成。
```

------

## 22.6 Memory Load Failure

```text
MemoryStore.Recent → injected error

断言：
    context_load_failed
    RecentMemory=[]
    Model 仍被调用
    Action 仍能成功
    MemoryProjector 仍被调用
    MemoryStore.Append 正常执行
    context_updated
    turn_completed
```

------

## 22.7 Memory Save Failure

```text
Action 已成功

MemoryStore.Append → injected error

断言：
    context_update_failed
    Turn 仍为 completed
```

------

## 22.8 Memory Project Failure

```text
Action 已成功

MemoryProjector → injected error

断言：
    context_update_failed
    不调用 MemoryStore.Append
    Turn 仍为 completed
```

------

## 22.9 EnvironmentSession Reconnect Scope

```text
EnvironmentSession A:
    FarmA / Abigail → Memory A

EnvironmentSession A 断开

EnvironmentSession B:
    同一 AgentSessionKey = FarmA / Abigail
    → Turn

断言：
    EnvironmentSession B 的 Context 包含 Memory A。
```

这证明 MemoryStore 是 Runtime-scoped，
不是 EnvironmentSession-scoped。

------

## 22.10 Multi-Agent Concurrency

```text
Abigail Turn
+
Linus Turn

并行执行。

断言：
    MemoryStore 无 race
    Context 不串线
    各自 Memory update 正确归属。
```

------

# 23. Stardew 真机验收

Phase4 真机 smoke test 不以模型自然语言表现作为唯一 Pass/Fail 条件。

建议：

```text
1. 启动 Runtime + Stardew Adapter。

2. 点击 Abigail。
   → trace:
       context_loaded memory_count=0
       context_updated memory_count/result=success
       turn_completed

3. 再次点击 Abigail。
   → trace:
      context_loaded memory_count>=1
   → 若 Current Observation / Event 的 game_time 与上一轮同属今天且时间没有明显推进：
      NPC 回复应延续当前对话，
      不应默认说“又来了 / 又见面了 / 你回来了”。

4. 点击 Linus。
   → Linus context 中不存在 Abigail memory_id。

5. 若切换 world：
   → 相同 NPC 新 WorldID 下 memory_count=0。

6. MemoryEnabled=false：
   → NPC 仍正常完成原 One-Turn speak/emote 链路。
```

模型第二轮是否自然提及上一轮内容：

```text
可作为 qualitative observation，
不作为唯一自动验收条件。
```

------

# 24. 实施顺序

```text
1. Phase3 code-aware baseline check
   确认 AgentSessionKey / AgentLoop / ModelRequest / Trace / gateway test helper 实际边界。
   实现最小 GameTimeSnapshot：
       从 GameEvent.game_time 投影 year / season / day / hour / minute / tick；
       同时记录 GameEvent.sequence。

2. 冻结 Context Scope Contract。

3. 补齐稳定 turn_id 来源：
       MemoryRecord.SourceTurnID 与 TraceEvent.turn_id 必须同源。

4. 新建 MemoryRecord + MemoryStore contract。

5. 实现 InMemoryMemoryStore + isolation / race 单测。

6. 实现 RecentTurn MemoryProjector。

7. 建立 runtime/internal/context 下的最小 AgentContext + ContextBuilder。

8. 在迁移前补 ModelRequest 内容回归测试：
       锁定 System prompt 关键片段、
       User message 中 Event / Observation 的存在、
       Tools 仍通过 ToolDefinition 传入。

9. 将现有 buildPrompt / ModelRequest 构造迁移到 ContextBuilder 边界。

10. AgentLoop 接入 Memory load：
       Observe
       → Recent Memory
       → ContextBuilder
       → Model。

11. AgentLoop 接入成功 Action 后的 Memory update：
       Action succeeded
       → MemoryProjector
       → MemoryStore.Append
       → context_updated / failed
       → turn_completed。
    保证 update 完成后才释放同 Session 下一 Turn。

12. 增加 context_loaded / context_updated / failure trace。

13. 增加 MemoryEnabled + RecentMemoryLimit 等最小配置。

14. 从现有 gateway integration tests 抽取 fake Environment / fake Provider helper，
    建立可复用 Deterministic TestEnvironment 基础。

15. 完成 multi-turn / entity isolation / world isolation /
    read-after-write / failure tests。

16. Memory disabled regression。

17. 真机 Stardew short-term memory smoke test。

18. Architecture boundary check。

19. 阶段 Review：
    确认 Deterministic TestEnvironment 足以进入 Phase5。
```

------

# 25. 验收标准

| Roadmap 完成条件                                    | Phase4 验收                      |
| --------------------------------------------------- | -------------------------------- |
| 同一 NPC 后续 Turn 可以引用前一次相关信息           | deterministic multi-turn test    |
| 不同 NPC Memory 不串线                              | entity isolation test            |
| Memory 绑定 AgentSession 而不是 EnvironmentSession  | AgentSessionKey scope test       |
| 同一 AgentSessionKey 跨 EnvironmentSession 共享 Memory | reconnect scope integration test |
| 关闭 Memory 后 One-Turn 仍正常                      | MemoryDisabled regression        |
| Trace 能说明 Context 是否加载 / 更新                | context_loaded / context_updated |
| Recent Memory 携带游戏时间语境，连续点击不误判为再次到访 | GameTime projection + Stardew smoke |
| 有可复用确定性测试夹具                              | Deterministic TestEnvironment    |
| Phase5 可在不依赖真实 Stardew/LLM 情况下测试多 Step | Phase4 end review                |

------

# 26. Phase4 Mandatory Deliverables

阶段结束至少产生：

```text
1. 《GameAgent MVP0 Phase4 技术开发与验收方案》

2. Context Scope Contract
   可独立文档，也可由本方案 §5 Accepted。

3. MemoryStore Contract
   包含：
       Scope
       Record
       Read / Write
       Failure
       Consistency

4. Deterministic TestEnvironment

5. 自动测试

6. Stardew 真机验收记录

7. Architecture Boundary Check

8. Phase4 Review / 学习回顾
```

------

# 27. Architecture DoD

```text
[ ] Memory 使用 AgentSessionKey，而不是 EnvironmentSession / session_id。

[ ] Runtime Memory 不依赖 Stardew 类型。

[ ] Adapter 不包含 Memory Retrieval / Context Builder。

[ ] MemoryRecord / Context 不进入 GameAgent Protocol。

[ ] Game 仍是当前 World State Source of Truth。

[ ] 当前 Observation 优先于历史 Memory。

[ ] Trace 与 Memory 解耦。

[ ] 关闭 Trace 不改变 Memory 行为。

[ ] 关闭 Memory 不破坏 One-Turn AgentLoop。

[ ] MemoryStore backend 可替换。

[ ] P0 没有引入 Vector / Embedding / Reflection。

[ ] Memory context 有明确上限，不无限增长。

[ ] MemoryRecord.SourceTurnID 不从 Trace JSONL 反解析，
    且与同一 Turn 的 TraceEvent.turn_id 一致。

[ ] 同一 AgentSession 前一成功 Action 对应的 Memory update
    在下一 Turn BuildContext 前可见。

[ ] Memory failure 不会把已成功发生的游戏 Action 改写成 failed。

[ ] Deterministic TestEnvironment 不依赖 Stardew / wall-clock sleep。

[ ] Runtime 没有新增具体游戏 API 依赖。

[ ] Protocol v1alpha2 无非必要修改。
```

------

# 28. 风险与边界

## 28.1 In-Memory Memory 不跨 Runtime restart

```text
Runtime restart
↓
Recent Memory 清空
```

Phase4 接受。

持久恢复：

```text
Phase7
```

统一设计。

------

## 28.2 Recent Memory 不是长期 Memory

随着 Turn 增加：

```text
MemoryStore 内记录可能持续增加，
但每轮 Context 只读取有限 Recent N。
```

Phase4 不解决：

```text
长期 retention
forgetting
compaction
importance
semantic retrieval
```

这些必须在真实需求出现后单独设计。

------

## 28.3 Recent Memory 可能包含过时信息

因此必须维持：

```text
Current Observation > Historical Memory
```

Memory 只表示过去。

------

## 28.4 Stardew 当前交互信息有限

当前：

```text
player_interacted_with_npc
```

本身未必携带丰富玩家自然语言输入。

因此 Stardew 真机 Phase4 主要证明：

```text
跨 Turn Context continuity
```

而不是完整自由对话 Memory 产品体验。

真机验收记录应明确：

```text
硬验收：
    trace / model request 证明第二轮 Context 已包含第一轮 Memory。

软观察：
    模型自然语言是否主动提及上一轮内容。
```

如果 trace / model request 已证明 Memory 进入 Context，
但模型没有自然提及上一轮内容，不应单独判定 Phase4 失败。

更丰富的：

```text
gift
quest
dialogue input
important world event
```

可以在后续 Trigger / Adapter 扩展时自然提升 Memory 价值。

------

## 28.5 MemoryStore 不应成为游戏延迟瓶颈

Phase4 使用 InMemory Store，Memory load / append 应为轻量操作。

未来增加磁盘 / DB Backend 时必须重新评估：

```text
read latency
write latency
tail latency
failure behavior
```

不得因为“Memory 持久化”让玩家交互明显等待。

------

# 29. Phase5 入口依赖门

进入 Phase5 前：

```text
Deterministic TestEnvironment 必须 Accepted。
```

至少能够：

```text
脚本化：
    Event
    Observation
    ModelResponse
    ToolCall
    ActionResult

并能够：
    捕获 ModelRequest
    注入失败
    驱动多 Entity
    驱动多 Turn
    不依赖 sleep
```

Phase5 才在此基础上增加：

```text
1 AgentTurn
=
N AgentSteps
```

Phase4 不提前实现 AgentStep。

------

# 30. 阶段结束状态

按 Roadmap 使用：

```text
Accepted

Accepted with Known Limitations

Needs Follow-up
```

Phase4 不得因为：

```text
“Memory 已经能存数据”
```

就直接 Accepted。

必须同时证明：

```text
Context Scope 正确

Memory Isolation 正确

Read-after-write 正确

Memory Disabled 不破坏旧链路

Deterministic TestEnvironment 可复用
```

------

# 31. 一句话总结

> **Phase4 的核心不是“给 NPC 上一个数据库”，而是第一次让 AgentSession 真正拥有跨 Turn 的行为连续性：用稳定 identity 隔离 Memory，用 ContextBuilder 明确静态定义、历史信息和当前世界事实的边界，并建立一个不依赖真实游戏与真实 LLM 的确定性测试底座，为 Phase5 Multi-step AgentTurn 提供可靠地基。**
