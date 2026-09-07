# GameAgent Memory Architecture v0.1

> Status: Architecture Draft — Memory Model Baseline
> Date: 2026-09-07
> Scope: game-native Memory Core + Game Memory Policy + Memory Store + Memory Retrieval
> Identity Baseline: `AgentSessionKey = game_id + world_id + entity_id`

---

# 0. Phase8 Normative Subset

本文是 GameAgent Memory 的长期架构设计，不等同于 Phase8 的完整开发方案。

Phase8 可采用本文的最小子集：

```text
Phase8.1 Persistent Recent Memory
    使用 SQLiteMemoryStore 将现有进程内 Recent Memory 持久化，
    支撑 Runtime 重启后的短期对话连续性。
    数据库按 game_id + world_id 物理分库，
    库内按 entity_id 隔离个人记忆。

Phase8.2 Lightweight Long-term Memory
    在同一世界数据库中引入规则优先的 Game Memory Policy，
    保存少量重要经历，
    支撑窗口外召回、更正和 Scope 隔离。
```

如果阶段规划采用该方向，需要后续同步更新 `GameAgent 阶段规划.md`。

Phase8 不要求完成：

```text
完整 Experience Store
完整 Agent Instance State
任务 / 目标 / 承诺执行系统
自主行为调度
向量数据库硬依赖
跨机器共享 Memory
完整 Memory reflection pipeline
JSON -> SQLite 迁移器
```

---

# 1. 设计目标

GameAgent Memory 是 Runtime 管理的游戏原生 Agent 记忆系统。

它围绕以下对象工作：

```text
game_id
world_id
entity_id
AgentTurn
ContextFact
Observation
ActionResult
```

Memory 的目标不是做一个脱离游戏环境的记忆库，而是让游戏中的 Agent 在具体世界里持续积累经历，并在后续 AgentTurn 中被 Runtime 正确召回、选择和投影。

Memory 主要回答：

```text
这个世界发生过什么？
这个具体 Agent 经历过什么？
哪些过去内容应该影响当前这一次 AgentTurn？
```

---

# 2. 核心原则

## 2.1 Game-native Memory 特征

GameAgent Memory 不是 Chat Memory。

它保存的不是一段普通 AI 会话历史，而是游戏世界中 Agent 实体的经历。

WIA 的差异化体现在：

```text
Scope 绑定 Game / World / Entity，而不是普通 chat session。
Memory 来源优先来自 ContextFact、已确认 ActionResult、Observation reference 和 AgentTurn 终态。
ActionResult 必须由 Adapter / Game 确认，不能只凭模型说法当成已经发生。
Memory 可见性需要考虑游戏时间、存档、世界时间线和实体知情范围。
Definition 是模板，Memory 是某个 world 中具体 entity 的经历。
```

因此 Memory 设计必须保持：

```text
Game-native, not chat-only.
Multi-game, not Stardew-only.
Runtime-owned, not Adapter-owned.
Source-backed, not free-form hallucination.
Context-projected, not prompt-appended.
```

## 2.2 Memory 属于 Runtime

```text
Runtime owns cognition.
Adapter owns translation.
Game owns execution.
```

Memory 属于 Runtime cognition / Agent State。

Adapter 可以提供事件、当前观察、ContextFact、ActionResult 和领域事实，但 Adapter 不直接保存 Agent Memory，不执行 Memory Retrieval，也不根据 Memory 做 Agent Decision。

## 2.3 Memory 绑定 AgentSession

Memory 绑定长期 AgentSession identity：

```text
game_id + world_id + entity_id
```

Memory 不绑定：

```text
EnvironmentSession
gRPC session_id
definition_id
display_name
```

EnvironmentSession 断开和重连不应该改变同一 Agent 的 Memory scope。

## 2.4 Memory 不修改 Definition

Definition 是静态或半静态模板。

Memory 是具体 world / entity 在运行中积累的经历。

```text
Agent Definition / Archetype = game_id + definition_id
Agent Instance / State / Memory = game_id + world_id + entity_id
```

多个实体可以共享同一个 `definition_id`，但它们的 Memory 必须按 `entity_id` 隔离。

## 2.5 Memory 不是 Prompt Builder

Memory Store 不直接进入 model.Request。

Memory Retrieval 只返回候选。

Context Engine 决定哪些 MemoryProjection 最终进入模型输入，并负责预算、裁剪和报告。

## 2.6 Memory 必须有来源

长期持久记忆必须能追溯来源。

模型可以在当前对话里做角色表演，但无来源的即兴内容默认不能进入持久 Memory。

---

# 3. Memory Scope

GameAgent 使用 `game_id / world_id / entity_id` 作为 Memory Scope 的正式架构字段。

## 3.1 WorldMemoryScope

```text
WorldMemoryScope = game_id + world_id
```

WorldMemoryScope 表示当前游戏、当前世界、当前存档或当前环境实例的整体记忆。

典型内容：

```text
world-level event
shared environmental experience
public fact remembered by Runtime
```

WorldMemoryScope 不是所有 Agent 的默认个人记忆池。Entity 读取 WorldMemoryScope 内容必须经过 Game Memory Policy 或显式共享规则。

## 3.2 EntityMemoryScope

```text
EntityMemoryScope = game_id + world_id + entity_id
```

EntityMemoryScope 表示当前世界中特定 Agent 个体的记忆。

典型内容：

```text
该 Agent 亲历的交互
该 Agent 对玩家或其他实体形成的记忆
该 Agent 在当前 world 中积累的经历摘要
```

EntityMemoryScope 是 Agent Memory 的最小个人粒度。

## 3.3 归属对象与涉及对象

Memory 必须区分：

```text
owner_scope
    这条记忆属于谁？

participants
    这条记忆涉及谁或什么？
```

示例：

```text
owner_scope:
    game_id + world_id + entity_id = stardew-valley + farm-001 + npc:Abigail

participants:
    player:local
    npc:Abigail
    topic:fishing
```

这条记忆归属于 Abigail，不代表其他 NPC 默认知道它。

---

# 4. Definition / Instance / Memory 关系

GameAgent 必须长期区分：

```text
Game Definition
    game_id 级别的游戏定义、世界观边界、默认策略入口。

Agent Definition / Archetype
    game_id + definition_id 级别的角色模板。

Agent Instance Descriptor
    game_id + world_id + entity_id 级别的当前实例描述。

Agent Memory
    game_id + world_id + entity_id 级别的实例经历。
```

关系规则：

- `definition_id` 是模板绑定 key，不是 Memory key。
- `entity_id` 是 world 内稳定实体身份，是 Memory key 的一部分。
- Memory 不反向改写 Agent Definition。
- Agent Instance 可以同时拥有 Definition、Descriptor、State、Memory 和 Current Observation。

---

# 5. Memory Sources

Memory 来源必须是 Runtime 已接收或已确认的游戏原生材料。

主要来源：

```text
GameEvent
ContextFact
Observation reference
ToolCall
ActionResult
TurnCompletion
system correction
```

## 5.1 ContextFact

ContextFact 是 Adapter 显式声明的 model-visible event context。

它适合成为 Recent Memory 和长期候选的主要来源之一。

ContextFact 可以表达：

```text
utterance
choice
command
interaction
```

Memory Core 可以保存 ContextFact 引用，但不得解析 game-specific payload 来猜测事件语义。

## 5.2 Observation

Observation 表达当前环境事实。

Observation 不是 Memory，也不是 Memory Store。

Game Memory Policy 可以使用 Observation 生成召回 hint 或来源引用，例如当前地点、当前对象、当前关系状态或当前事件上下文。但这些字段的具体含义属于该 `game_id` 下的策略，不进入 Memory Core。

## 5.3 ActionResult

ActionResult 表达 Runtime 请求 Adapter 执行动作后的确认结果。

成功的、对外可见的 ActionResult 可以成为 Memory 写入来源。失败、取消、超时或 rejected result 可以作为执行经历记录，但不能被写成已经成功发生的游戏事实。

## 5.4 Evidence Capsule

长期 Memory 和保留历史版本引用的必要证据必须可持久读取。

Recent 窗口淘汰只影响默认短期读取窗口，不能删除仍被长期 Memory 引用的唯一证据。

最小 evidence capsule 包含：

```text
source_id
owner_scope
source_time
participants
ContextFact summary / original text when needed
ActionResult summary when needed
Observation reference when needed
```

Evidence capsule 不是完整 Experience Store。它只保存长期记忆核验所需的最小证据。

## 5.5 ToolCall 与 TurnCompletion

ToolCall 可以证明 Runtime 请求过某个工具。

TurnCompletion 可以证明某个 AgentTurn 进入了执行终态。

它们不能单独证明游戏效果已经发生。游戏效果必须由 Adapter / Game 确认后的 ActionResult 或等价结果来源表达。

---

# 6. Game Memory Policy

Game Memory Policy 是 `game_id` 下的记忆策略层。

它负责解释某个游戏里的来源如何形成记忆，以及当前 AgentTurn 应该如何查找相关记忆。

## 6.1 Default Game Memory Policy

Runtime 提供跨游戏可复用的 Default Game Memory Policy。

默认策略是基础记忆能力的一部分。缺少 game-specific policy 时，Runtime 仍应能基于通用 ContextFact、confirmed ActionResult、Observation reference 和 AgentTurn 终态形成 Recent Memory，并执行最小长期候选与召回。

Default Game Memory Policy 可以使用：

```text
ContextFact.kind
ContextFact.text
ContextFact.actor_entity_id
ContextFact.target_entity_id
ActionResult status / code / message / visible output
AgentSessionKey
GameTime / sequence when available
Definition-declared topic hints
```

Default Game Memory Policy 不解析 game-specific `Observation.state` 私有字段，不按具体 capability name 推断游戏效果，也不补造 Adapter 应提供的 ContextFact。

## 6.2 Game-specific Policy Extension

Game-specific Policy 是可选扩展，不是基础记忆能力的前提。

GameDefinition / AgentDefinition 可以声明：

```text
memory topic hints
retention preference
importance hints
visibility hints
game-specific policy id
```

这些配置影响形成和召回倾向，但不改变 Memory Core 的 Scope、来源、幂等、状态和持久化合同。

Game Memory Policy 负责：

```text
从 ContextFact / ActionResult / Observation reference 形成 MemoryCandidate
从当前 AgentTurn 形成 MemoryQuery
判断 candidate 是否替代、失效或补充已有记忆
声明时间可见性规则
声明 WorldMemoryScope 何时可以被 Entity 读取
```

Memory Core 负责：

```text
校验 Scope
校验来源
执行幂等写入
执行状态更新
保存 MemoryRecord
读取和过滤候选
```

Phase8 首版采用规则优先的 Game Memory Policy，不要求 LLM 提取长期记忆。

LLM 未来可以生成 MemoryCandidate，但必须带来源、confidence 和 claim type，并经过 Memory Core 校验后才能持久化。

## 6.3 首版规则能力

Phase8 首版规则至少覆盖：

```text
从 utterance / choice / command / interaction ContextFact 形成 stated candidate
从 successful visible ActionResult 形成 observed candidate
从当前 turn context 生成 topic / participant / event kind / location hint
为同一来源内的不同输出生成 stable_candidate_key
用 semantic key 定位可能被更新的同一认知
依据来源证据判断 append / supersede / invalidate
```

`stable_candidate_key` 用于幂等区分，不负责决定替代关系。

`semantic key` 用于找到可能相关的旧记录；是否替代或失效必须由 Game Memory Policy 根据来源证据明确给出。无法确定更新关系时，默认追加经历，不自动使旧认知失效。

更正通常更新 Semantic 当前认知，同时保留 Episodic 中“当时发生过什么 / 当时说过什么”的历史证据。

具体游戏可以扩展这些规则，但基础流程必须复用 Default Game Memory Policy。

---

# 7. Memory Lifecycle

Memory 机制发生在 AgentTurn 的两个位置：

```text
AgentTurn 开始后、模型请求构建前：
    Memory Recall

AgentTurn 进入终态后：
    Memory Write
```

完整链路：

```text
Adapter / Game
    -> GameEvent
    -> ContextFacts
    -> Observation
    -> ActionResult

Gateway
    -> canonical target entity
    -> AgentSessionKey

AgentTurn starts

Memory Recall
    -> Game Memory Policy builds MemoryQuery
    -> Memory Core applies Scope filter
    -> Memory Core applies TimeVisibilityPolicy
    -> Memory Store returns candidates
    -> Memory Retriever produces MemoryProjection candidates

Context Build
    -> Context Engine merges Event / Observation / Definition / Memory / Transcript / Tools
    -> Context Engine selects and budgets final MemoryProjection
    -> Renderer builds model.Request

Model / Tool / Action loop

AgentTurn terminal state

Memory Write
    -> collect confirmed turn sources
    -> Game Memory Policy forms MemoryCandidate
    -> Memory Core validates source and scope
    -> Memory Core applies idempotency
    -> Memory Core applies supersede / invalidate relations
    -> Memory Store persists records
    -> indexes refresh from stored records
```

关键约束：

- Recall 发生在 Provider 调用前。
- Phase8 首版 Recall 是 turn-scoped：每个 AgentTurn 开始后执行一次。
- 同一 AgentTurn 内的多 step 复用同一批 Memory 召回候选。
- 同一 AgentTurn 内的新 ToolResult / ActionResult 通过 Transcript 进入下一次模型请求，不触发 Memory Store 重新召回。
- Write 发生在 Turn 终态后。
- Write 只基于已确认来源。
- 同一 Entity 的 Memory 写入尝试必须在释放该 Entity 的 ExecutionLane 前结束。
- 存储事务提交成功后，下一 Turn 可以读取该结果。
- Memory 写入失败不回滚已经完成的 Action。
- Memory 读取失败不阻塞基础 AgentTurn。

本文中的 `ExecutionLane` 指 Runtime 现有按 `AgentSessionKey` 管理的 per-entity turn 串行队列。同一 `game_id + world_id + entity_id` 的 AgentTurn 在同一 lane 内 FIFO 执行；不同 Entity 可以并行。Memory 不新增独立锁模型，只要求同一 Entity 的 Memory 写入尝试在该 turn 的 lane task 结束前完成。

## 7.1 Turn Transcript / Trace / Memory Store

Phase8 需要区分三类数据：

| 数据 | 位置 | 职责 |
| --- | --- | --- |
| Current Turn Transcript | Runtime 内存 | 保存当前 AgentTurn 内的模型消息、ToolCall 和 ToolResult，供后续 step 构建请求 |
| JSONL Trace | 可选诊断文件 | 记录调用顺序、参数摘要、结果和失败原因，帮助排查问题 |
| SQLite Memory Store | Runtime 本地 SQLite | 保存已确认的 Recent Memory、后续长期 Memory 和必要 evidence |

边界：

- Transcript 是当前 turn 的执行数据，不是持久 Memory。
- Trace 是 best-effort observer，不是 Memory 真源。
- SQLite Memory Store 是 Phase8 的持久记忆真源。
- Trace 写入失败不影响 Transcript、Memory 提交或 Turn 结果。
- Memory 不从 Trace 反向重建，不从 Trace 自动重放工具。
- 未完成 Turn 的恢复、工具自动重放和完整 Experience Log 属于后续阶段。

---

# 8. Memory Content Types 与 Reading Strategies

Memory content type 描述内容语义。

Reading strategy 描述读取方式。

两者必须分开。

## 8.1 Content Type: Episodic Memory

Episodic Memory 保存具体经历。

它回答：

```text
过去某个时间，这个世界或这个 Agent 发生过什么？
```

Episodic Memory 必须有 Scope、source、time 和 participants。

## 8.2 Content Type: Semantic Memory

Semantic Memory 保存从经历中提炼出的稳定认知。

它回答：

```text
从过去经历中，当前 Agent 可以稳定记住什么？
```

Semantic Memory 必须能追溯到一个或多个来源。推断类 Semantic Memory 必须带 confidence。

## 8.3 Reading Strategy: Recent

Recent 读取最近交互形成的短期记录。

用途：

```text
维持对话连续性
支撑 Runtime restart 后短期恢复
为长期记忆形成提供来源
```

Recent 读取可以消费 Episodic 或 Semantic MemoryRecord，但 Phase8.1 主要消费现有 Recent Memory 投影。

## 8.4 Reading Strategy: Relevant

Relevant 根据当前 AgentTurn 的 query hints 召回相关记忆。

它不等于全文搜索所有历史，也不要求每次都使用向量检索。

## 8.5 Reading Strategy: Core

Core 表示少量高优先级长期记忆的常驻读取策略。

Core 不是新的事实来源。

Phase8 不要求实现完整 Core Memory 常驻机制。

---

# 9. MemoryCandidate / MemoryRecord / MemoryProjection

## 9.1 MemoryCandidate

MemoryCandidate 是 Game Memory Policy 输出的写入候选。

概念字段：

```text
candidate_id
stable_candidate_key
projection_kind
projection_version
owner_scope
memory_type
claim_type
content
summary
participants
topics
importance
confidence
source_refs
time_ref
update_intent
metadata
```

`stable_candidate_key` 用于区分同一来源内的多条候选记忆。它由 Game Memory Policy 根据候选内容的稳定语义生成，不能依赖随机数、当前系统时间或列表顺序。

`claim_type` 表达内容性质：

```text
observed
stated
inferred
system_asserted
```

`update_intent` 表达写入关系：

```text
append
supersede
invalidate
expire
```

## 9.2 MemoryRecord

MemoryRecord 是持久化后的记忆条目。

概念字段：

```text
memory_id
schema_version
projection_batch_key
memory_item_key
owner_scope
memory_type
claim_type
status
content
summary
participants
topics
importance
confidence
time_ref
created_at
updated_at
source_refs
evidence_refs
supersedes_memory_id
invalidates_memory_id
metadata
```

`source_refs` 指向形成记忆的来源；`evidence_refs` 指向可持久读取的必要证据。长期 Memory 的 `evidence_refs` 不能只指向会被 Recent 窗口淘汰的临时内容。

`status` 至少包含：

```text
active
superseded
invalidated
expired
```

## 9.3 MemoryProjection

MemoryProjection 是 Memory 交给 Context Engine 的候选投影。

概念字段：

```text
memory_id
owner_scope
memory_type
claim_type
summary
evidence_refs
confidence
importance
time_ref
reason_for_recall
estimated_tokens
```

MemoryProjection 不等于数据库原始记录。

---

# 10. Recall Flow

Memory Recall 发生在 AgentTurn 开始后、Context Build 之前。

Phase8 首版采用 turn-scoped recall。

```text
一个 AgentTurn:
    Memory Recall 一次
    Context Build 可执行多次
    Transcript 在 step 间增长
    Memory candidates 不在 step 间重新查询
```

流程：

```text
AgentSessionKey
    -> current turn context
    -> Game Memory Policy builds MemoryQuery
    -> Memory Core resolves readable scopes
    -> Scope hard filter
    -> TimeVisibilityPolicy hard filter
    -> status hard filter
    -> Store retrieves candidates
    -> Memory Retriever ranks candidates
    -> MemoryProjection candidates
    -> Context Engine selection
```

MemoryQuery 可以包含：

```text
owner_scope
readable_scopes
query_text
topics
participants
event_kind
location_hint
time_context
memory_types
claim_types
limit
metadata
```

Topic 是检索标签或 query hint，不是独立存储位置。

硬过滤条件：

```text
scope
time visibility
status
```

首版排序因素：

```text
importance
relevance
recency
confidence
memory_id
```

规则版 `relevance` 来自确定性 hint 匹配，不依赖向量数据库：

```text
topic match
participant match
event_kind match
location_hint match
claim_type match
```

多个候选得分相同时，继续使用 `importance`、`recency`、`confidence`、`memory_id` 等稳定字段排序。

---

# 11. Write Flow

Memory Write 发生在 AgentTurn 进入终态后。

流程：

```text
Turn terminal state
    -> collect confirmed sources
    -> Game Memory Policy forms candidates
    -> Memory Core validates candidates
    -> Memory Core applies idempotency
    -> Memory Core resolves update relations
    -> Memory Store commits records
    -> derived indexes update
```

写入规则：

- 成功 Turn 可以写入 Recent Memory。
- 已确认成功的 visible ActionResult 可以写入经历。
- failed / canceled / timeout Turn 不能凭未确认来源写入成功事实。
- 同一 source 重复处理必须幂等。
- 一份 source 可以形成多条 MemoryRecord。
- TurnCompletion 表示执行终态，不表示 Memory 已持久保存。
- Memory 持久保存以 Store 事务提交成功为准。

同一 Entity 的写入顺序：

```text
Turn terminal state
    -> bounded Memory Write attempt
    -> Store commit success / failure diagnostic
    -> release Entity ExecutionLane
    -> next Turn may start
```

写入尝试必须有界，不能无限阻塞 ExecutionLane。

ProjectionBatchKey 标识一次来源投影批次：

```text
ProjectionBatchKey =
owner_scope
    + source_id
    + projection_kind
    + projection_version
```

MemoryItemKey 标识批次内一条稳定输出：

```text
MemoryItemKey =
ProjectionBatchKey
    + stable_candidate_key
```

`projection_version` 升级时，技术方案必须明确旧投影是被 supersede、invalidate，还是与新投影并存。架构层只冻结：版本升级必须产生明确版本关系，不能悄悄制造一组重复 active 记忆。

Phase8.1 可以将现有 `turnID` 或 `turnID + source_event_id` 映射为 `source_id`。`MemoryID` 是持久记录 ID，不承担幂等键职责。Phase8 技术方案必须明确现有 `turnID / MemoryID` 与 `ProjectionBatchKey / MemoryItemKey` 的映射，避免两套幂等语义并存。

---

# 12. Time Visibility

Memory Core 保存时间信息，但具体时间线语义由 Game Memory Policy 或游戏配置声明。

通用时间引用：

```text
created_at
updated_at
source_time
observed_time
timeline_ref
source_event_sequence
effective_time
valid_from
valid_to
```

读取时执行：

```text
MemoryRecord + CurrentTimeContext -> visible / hidden / unknown
```

规则：

- `visible` 可以进入召回候选。
- `hidden` 不进入当前 Context。
- `unknown` 默认不执行未来时间过滤，只应用 Scope、status、普通 Recent / Relevant 和 budget 规则。
- 时间过滤不删除 MemoryRecord。

当前游戏场景可以采用：

```text
observed_game_time > current_game_time
    -> hidden

observed_game_time <= current_game_time
    -> visible
```

默认 `GameTime` 合同：

- 使用严格 `>` 过滤未来记忆。
- 与当前 `GameTime` 相等的 MemoryRecord 继续可见。
- 缺少可比较 `GameTime` 时，不执行未来时间过滤。
- 同一非空 `GameTime` 且 `source_event_sequence` 非 0 的记录，按 `source_event_sequence` 稳定排序。

Phase8 只承诺未来时间记录不进入当前 Context，不承诺完整回档分支恢复。

长期架构预留 `effective_time / valid_from / valid_to`，用于后续解决回档后旧认知版本恢复问题。

---

# 13. Correction / Supersede / Invalidation

Memory 更正不直接抹掉历史。

状态关系：

```text
new record supersedes old record
new record invalidates old record
record expires by policy
```

要求：

- Game Memory Policy 判断什么内容构成更正或失效。
- Memory Core 原子更新新旧记录状态。
- 被 superseded / invalidated 的记录默认不作为当前事实召回。
- evidence chain 可以保留，用于解释和诊断。
- Phase8 按持久状态筛选有效记录，并过滤可比较 GameTime 下的未来记录。
- 回档后，被替代的旧认知可能无法自动恢复；按历史时间恢复有效版本由后续 timeline / branch 设计负责。
- Phase8 不承诺完整历史版本恢复；涉及回档后旧认知恢复的规则由后续 timeline / branch 设计补齐。

---

# 14. Storage Architecture

MVP0 Memory Store 使用 Runtime 本地结构化持久存储。Phase8 推荐采用 SQLite 作为正式持久化后端，但架构边界仍是 `memory.Store`，不要求 Adapter 或 Context Engine 直接理解数据库实现。

物理数据库边界：

```text
game_id + world_id -> one local SQLite database
```

默认数据库路径由技术方案定义，例如：

```text
runtime/.local/memory/<game_id_hash>/<world_id_hash>/memory.db
```

规则：

- `game_id_hash` 使用 `sha256(game_id)` 的 lowercase hex。
- `world_id_hash` 使用 `sha256(world_id)` 的 lowercase hex。
- hash 只用于稳定、安全的路径生成，避免平台保留名、尾随点和特殊字符问题。
- SQLite 数据库内必须保存原始 `game_id` 和 `world_id`。
- 打开数据库时必须核对库内绑定身份与请求的 `game_id / world_id` 一致。
- 物理分库是存储部署方式，不等同于 `WorldMemoryScope`。
- `WorldMemoryScope = game_id + world_id` 表示世界级记忆归属。
- `EntityMemoryScope = game_id + world_id + entity_id` 表示个人记忆归属。
- 同一个世界数据库可以同时保存世界级记忆和多个 Entity 的个人记忆。
- 个人记忆读取仍必须按 `entity_id` 隔离。
- `definition_id` 不进入数据库路径，也不进入 Memory Scope。

概念集合：

```text
memory_records
memory_sources
memory_relations
memory_indexes
```

规则：

- `memory_records` 是权威持久记录。
- `memory_sources` 保存来源引用和必要 evidence capsule。
- `memory_relations` 保存 supersede / invalidate / evidence 关系。
- `memory_indexes` 是派生索引，可以重建。
- FTS 和 Vector 都是 retrieval index，不是 Memory 真源。
- Adapter 不直接写 Memory Store。
- 长期 Memory 引用的唯一证据不能因 Recent 窗口淘汰而丢失。

提交后可读分为两层：

```text
主记录可读
Relevant 检索可召回
```

Phase8 技术方案必须固定索引可见性策略：

```text
索引与记录处于同一原子提交边界
或索引不可用时有界回查主记录
或明确声明 Relevant 延迟可见并记录诊断
```

主记录可读和 Relevant 检索可召回应分别验收。

Phase8 直接使用 SQLite 作为落地后端，不实现 JSON -> SQLite 迁移器。

---

# 15. Context Handoff

Memory 与 Context 的交界是 MemoryProjection。

```text
Memory Store
    -> Memory Retrieval
    -> MemoryProjection candidates
    -> Context Engine
    -> selected Memory section
    -> Renderer
    -> model.Request
```

Context Engine 负责：

- 合并 Recent / Relevant / Core 候选。
- 去重。
- 排序。
- 预算裁剪。
- 区分 retrieval hit 与 final context inclusion。

Memory Core 负责保证候选：

- Scope 正确。
- 时间可见。
- 状态可用。
- 来源可追溯。

---

# 16. Failure Behavior

读取失败：

```text
AgentTurn 可以继续。
ContextBuildReport / trace 记录 memory_read_failed。
本轮模型输入不包含持久 Memory。
```

写入失败：

```text
已完成 Action 不回滚。
TurnCompletion 语义不回滚。
trace 记录 memory_write_failed。
后续 turn 可能看不到本轮记忆。
```

存储损坏：

```text
启动时发现 schema 不兼容或 store 损坏，应给出明确错误。
允许显式 reset，但 reset 的数据丢失边界必须清楚。
```

索引失败：

```text
MemoryRecord 不因索引失败失效。
索引可以重建。
```

---

# 17. Phase8 Adoption

Phase8 的 hard gate 主要验收 EntityMemoryScope 个人记忆。

WorldMemoryScope 保留为长期架构层级和数据模型能力，不作为 Phase8 真实游戏共享记忆体验的硬验收。

## 17.1 Phase8.1 Persistent Recent Memory

目标：

```text
现有 Recent Memory 使用 SQLiteMemoryStore。
Runtime 重启后，同一 AgentSessionKey 可以恢复最近记忆。
```

范围：

- 持久化现有 Recent Memory。
- 按 `game_id + world_id` 使用独立 SQLite 数据库。
- 保持 `game_id + world_id + entity_id` 隔离。
- 支持幂等写入。
- 支持 TimeVisibilityPolicy。
- 保持读取 fail-open。
- 保持写入失败不回滚已成功 Turn。

字段边界：

| 字段 / 能力 | Phase8.1 硬需求 | Phase8.2 | 长期预留 |
| --- | --- | --- | --- |
| `MemoryID` | 记录 ID | 记录 ID | 记录 ID |
| `AgentSessionKey` | 隔离键 | 隔离键 | 隔离键 |
| `SourceTurnID` / `SourceEventID` | 来源追踪 | 来源追踪 | 来源追踪 |
| `SourceEventSequence` | 稳定排序 | 稳定排序 | 稳定排序 |
| `GameTime` | 时间可见性 | 时间可见性 | 时间线语义 |
| `SourceContextFacts` | 近期事实来源 | 候选来源 | 证据来源 |
| `Outcomes` | 已确认动作结果 | 候选来源 | 证据来源 |
| `CreatedAt` | 持久记录时间 | 持久记录时间 | 版本时间 |
| `ProjectionKind` / `ProjectionVersion` | 幂等所需投影身份 | 候选形成版本 | 投影版本管理 |
| `ProjectionBatchKey` | 幂等所需逻辑键 | 来源批次键 | 批次版本关系 |
| `claim_type` / `confidence` / `topics` | 不作为硬需求 | 最小支持 | 完整模型 |
| `supersedes` / `invalidates` | 不作为硬需求 | 最小支持 | 完整版本关系 |

Phase8.1 不要求一次实现完整长期 MemoryRecord 模型。

验收：

- Runtime 重启后 Recent Memory 可恢复。
- 不同 world 不串线。
- 不同 entity 不串线。
- 重复 source projection 不产生重复记录。
- 时间不可见记录不进入本次 Context。
- 不引入长期 Memory / Evidence Capsule 完整模型。

## 17.2 Phase8.2 Lightweight Long-term Memory

目标：

```text
规则优先的 Game Memory Policy 保存少量重要经历，
并在相关 AgentTurn 中完成窗口外召回。
```

范围：

- 复用 Default Game Memory Policy。
- 支持 MemoryCandidate 写入。
- 支持 Episodic / Semantic Memory 的最小区分。
- 支持 topic / participant / event kind / location hint 检索。
- 支持 supersede / invalidate。
- 支持必要 evidence capsule 持久读取。
- Trace / report 区分 retrieval hit 与 final context inclusion。
- 支持 ProjectionBatchKey 与 MemoryItemKey。
- 不要求 LLM 提取长期记忆。
- 不要求向量数据库。

验收：

- 规范化来源可以形成长期 MemoryCandidate。
- 长期记忆离开 Recent 窗口后仍可被相关 query 召回。
- 更正记录可以替代旧记录。
- Scope 隔离稳定。
- model.Request 中可看到最终选中的 MemoryProjection。
- Phase8 不承诺完整回档分支恢复。

## 17.3 Phase8 技术方案撰写边界

Phase8 技术方案应按两个增量写：

```text
Phase8.1:
    只把现有 Recent Memory 持久化。
    明确本地 Store、现有 Record 字段、幂等 source 映射、时间可见性和重启恢复。

Phase8.2:
    在 8.1 基础上加入轻量长期记忆。
    明确 Default Game Memory Policy、MemoryCandidate、Relevant recall、更新关系、索引可见性和 Context handoff。
```

Phase8 技术方案不应写成完整 Memory 平台设计：

```text
不做完整 Experience Store
不做完整 Agent Instance State
不做自主行为调度
不要求向量数据库
不做完整回档分支恢复
不要求 LLM 提取长期记忆
```

Phase8 技术方案必须分别验收：

```text
持久化记录能读回
Relevant 查询能召回
retrieval hit 能追踪
final context inclusion 能追踪
Memory 写入失败不回滚 Turn
同一 Entity 下一 Turn 的可见性符合 Store commit 结果
```

---

# 18. Stardew / FakeGame 验收场景

Phase8 验收包含一个 FakeGame 场景和一个 Stardew 场景。

## 18.1 FakeGame 场景

FakeGame 场景验证 Memory Core 和 Default Game Memory Policy 不依赖 Stardew 字段。

```text
game_id = fake-game
world_id = world-a
entity_id = agent-1

source S:
    ContextFact.kind = utterance
    ContextFact.actor_entity_id = player:main
    ContextFact.target_entity_id = agent-1
    ContextFact.text = "I prefer quiet evenings."

Default Game Memory Policy 将 S 转成 stated MemoryCandidate。
Memory Core 保存为 active MemoryRecord。
该记录离开 Recent 窗口后，相关 query 仍可召回。

source S2:
    ContextFact.kind = memory_correction
    ContextFact.actor_entity_id = player:main
    ContextFact.target_entity_id = agent-1
    ContextFact.text = "player:main no longer prefers quiet evenings."
    ContextFact.attributes.update_intent = supersede
    ContextFact.attributes.target_semantic_key = stated-preference-player-main-quiet-evenings

Default Game Memory Policy 依据来源证据声明 Semantic 当前认知被更正。
Memory Core 写入新记录，并将旧记录标记为 superseded。
agent-2 或 world-b 读取不到 agent-1 的个人记忆。
```

兼容信息不能误替代：

```text
source S3:
    ContextFact.kind = utterance
    ContextFact.actor_entity_id = player:main
    ContextFact.target_entity_id = agent-1
    ContextFact.text = "I also prefer busy festivals."

Default Game Memory Policy 将 S3 作为追加经历处理。
Memory Core 不因 S3 自动 supersede "I prefer quiet evenings."
```

自然语言否定解析不是 Phase8 首版 hard gate。普通 `utterance` 即使包含否定词，也不能绕过结构化更正来源直接修改旧记录；需要由后续 Game Memory Policy 扩展或 LLM candidate extraction 明确产出 `update_intent` 后再进入 Memory Core。

同一套 Default Game Memory Policy 也必须能处理 confirmed ActionResult 形成的 observed candidate。ActionResult 只能证明动作执行结果，例如一句话已经展示；不能单独证明台词内容描述的历史事实为真。

## 18.2 Stardew 场景

Stardew 场景验证真实 Adapter 接入。

```text
Default Game Memory Policy 从 Stardew Adapter 提供的玩家输入、ContextFact、Observation reference 或 ActionResult 中形成候选。
候选归属于当前 NPC 的 EntityMemoryScope。
Runtime 重启后，同一存档、同一 NPC 在相关话题中召回该记忆。
玩家后续更正相关信息后，旧记忆不再作为当前事实进入 Context。
另一个 NPC 或另一个 world_id 不读取这条个人记忆。
```

Stardew 专用 Game Memory Policy 可以作为可选扩展补充该 `game_id` 的记忆倾向，但不能成为基础记忆能力的前提，也不能改变 Memory Core 规则。

---

# 19. 参考项目取舍

## 19.1 Hermes

WIA 借鉴 Hermes 的精选记忆、长期记忆形成和后台整理方向。

WIA 不照搬模型主动 `memory` tool 作为首版默认路径。Phase8 首版由 Runtime 的 Default Game Memory Policy 自动形成和召回记忆，模型生成 MemoryCandidate 作为后续增强。

## 19.2 Pi

WIA 借鉴 Pi 的历史保存与 Context 投影分离。

Recent 窗口裁剪不等于删除来源证据。MemoryProjection 是给 Context Engine 的候选投影，不是 MemoryRecord 原始存储。

WIA 不在 Phase8 照搬完整树状历史和分支会话系统。

## 19.3 DeepSeek Harness

WIA 借鉴 DeepSeek Harness 的持久化成功点、读取一致性、查询服务和模型可见内容分离。

Phase8 采用 Store 事务提交作为 Memory 保存成功点；同一 Entity 下一 Turn 的读取语义以该提交结果为准。

WIA 不在 Phase8 建设完整事件日志平台，也不把 Trace JSONL 当成 Memory 真源。

---

# 20. 长期演进

Memory 后续可以扩展：

```text
Persistent Experience Store
Core Memory 常驻读取策略
Vector / hybrid retrieval
Memory consolidation
World / group / faction shared memory
timeline / branch 管理
Agent Instance State
任务与目标系统
```

这些能力都必须继续遵守：

```text
Scope isolation
source traceability
TimeVisibilityPolicy
Context handoff
Runtime owns Memory
Adapter owns translation
Game owns execution
```

---

# 21. 架构不变量

1. Memory 属于 Runtime cognition / Agent State。
2. Adapter 不直接保存 Agent Memory。
3. Memory 不属于 EnvironmentSession。
4. Memory 不属于 Agent Definition / Archetype。
5. `WorldMemoryScope = game_id + world_id`。
6. `EntityMemoryScope = game_id + world_id + entity_id`。
7. Phase8 推荐 SQLiteMemoryStore 作为正式持久化后端。
8. 数据库按 `game_id + world_id` 物理分库。
9. 物理数据库不等同于 `WorldMemoryScope`。
10. 同一个世界数据库可以同时保存世界级记忆和个人记忆。
11. `definition_id` 不进入 Memory Scope。
12. 记忆归属对象和涉及对象必须分开表达。
13. Memory Core 不理解具体游戏字段。
14. Runtime 必须提供 Default Game Memory Policy。
15. Game-specific Policy 是可选扩展。
16. Game Memory Policy 负责形成候选、查询 hints 和时间可见性。
17. Game Memory Policy 不能补造 Adapter 应提供的 ContextFact。
18. Observation 是当前事实来源，不是 Memory 本体。
19. Topic 是检索标签或 query hint，不是独立存储位置。
20. Current Turn Transcript 是运行时内存，不是持久 Memory。
21. Trace 是诊断观察者，不是 Memory 真源。
22. MemoryRecord 必须可追溯来源。
23. 长期 Memory 必须保留可持久读取的必要 evidence capsule。
24. Recent 窗口淘汰不能删除仍被长期 Memory 引用的唯一证据。
25. 同一来源重复处理必须幂等。
26. 一份来源可以形成多条 MemoryRecord。
27. ProjectionBatchKey 标识来源投影批次。
28. MemoryItemKey 标识批次内单条记忆。
29. MemoryID 是记录 ID，不承担幂等键职责。
30. 更正和失效通过状态关系表达。
31. 读取必须执行 Scope 隔离。
32. 读取必须执行 TimeVisibilityPolicy。
33. Scope、时间可见性和状态是硬过滤。
34. Phase8 首版 Recall 是 turn-scoped。
35. 同一 Entity 的写入尝试必须在释放 ExecutionLane 前结束。
36. Store 事务提交成功后，下一 Turn 才能按已保存记忆读取。
37. 主记录可读和 Relevant 检索可召回应分别验收。
38. Memory 返回候选，Context Engine 决定最终模型输入。
39. 系统必须区分 retrieval hit 与 final context inclusion。
40. 模型即兴表演默认不进入持久 Memory。

---

# 22. 一句话总结

> GameAgent Memory Architecture 的核心不是保存聊天记录，而是在 game-native Runtime 中建立一套按 Game / World / Entity 隔离、可追溯来源、可持久化、可召回、可更正，并能交给 Context Engine 选择进入模型输入的 Agent 记忆系统。
