# GameAgent Context Architecture v0.3

> Status: Architecture Draft — Context Model Baseline
> Date: 2026-08-27
> Scope: Context Sources + Context Engine + Model Context
> Identity Baseline: `AgentSessionKey = game_id + world_id + entity_id`
> Compatibility Baseline: [GameAgent 多游戏兼容性与 Agent Binding 决策](../GameAgent 多游戏兼容性与 Agent Binding 决策.md)
> Phase7 Normative Subset: Candidate — [Phase7.0 Context Contract Entry Gate](../../phase07/GameAgent%20MVP0%20Phase7.0%20技术开发与验收方案.md)
> Design Goal: 为长期运行、多 World、多 Agent 的游戏 Agent Runtime 提供清晰、可扩展、可验证的上下文架构。

---

# 0. Phase7 Normative Subset

Phase7 Core 使用本文的规范子集：

```text
Context Source Scope
Definition / Instance separation
Current Event ContextFacts
Current Observation authority
Current Turn Transcript
Phase7 minimum budget contract
```

以下内容保留为 Future / Non-normative：

```text
Cognitive State
Experience
Episodic Memory
Semantic Memory
Vector Retrieval
World State Projection
完整 Context Source 插件框架
```

Phase7.0 只冻结 Runtime 当前主链路需要的 Context 合同，不冻结上述长期能力的完整实现方案。

---

# 1. 设计目标

GameAgent 的 Context 不是简单的 Prompt 拼接，也不是一个不断增长的 Conversation History。

它需要同时处理：

```text
固定游戏规则
角色基础设定
当前世界真实状态
角色当前主观认知
真实发生过的历史
长期与短期 Memory
当前这一次 AgentTurn 的 Event / Observation / Turn Transcript / Tools
```

并解决三个核心问题：

```text
1. 这些信息属于谁？
   → Scope

2. 信息冲突时谁说了算？
   → Authority / Freshness

3. 当前这一轮到底哪些信息值得进入模型？
   → Selection / Retrieval / Budget
```

因此 GameAgent Context Architecture 采用三层主模型：

```text
Context Sources
      ↓
Context Engine
      ↓
Model Context
      ↓
LLM
```

其中：

```text
Context Sources
    定义系统中有哪些可供 Agent 使用的信息，
    以及它们的 Scope、Authority、Lifecycle。

Context Engine
    针对当前 AgentTurn，从 Sources 中定位、验证、检索、筛选并压缩信息。

Model Context
    Context Engine 最终为本次模型调用生成的有限、结构化上下文。
```

---

# 2. 核心原则

## 2.1 Context Source 的第一属性是 Scope

任何新的 Context Source 在进入系统之前，必须先回答：

```text
1. 它属于谁？
2. 生命周期是什么？
3. 用什么 Scope Key 定位？
```

即：

> **Every Context Source MUST declare its Scope before it can participate in Context Engine.**

Scope 决定 Context Engine 应该读取哪一份数据，也决定不同 World / Agent 之间是否会发生串线。

---

## 2.2 当前游戏事实由 Game 决定

GameAgent Runtime 不能成为第二套游戏状态系统。

```text
Game World State
= Ground Truth
```

Runtime 可以保存：

```text
State Projection
Experience History
Memory
Cognitive State
```

但其中由 Game 派生的状态只能表示：

> “Runtime 最近从 Game 确认过什么。”

而不能表示：

> “Runtime 认为世界一定是什么。”

---

## 2.3 LLM 可以解释世界，但不能宣布世界已经改变

例如游戏当前事实：

```text
Abigail.friendship_hearts = 2
```

LLM 可以形成主观判断：

```text
trust_player = high
belief_player_is_kind = true
```

但 LLM 不能直接把：

```text
friendship_hearts = 8
```

写入 Environment State。

正确的世界变化路径必须是：

```text
LLM
 ↓
ActionRequest
 ↓
Game / Adapter
 ↓
Game State Changed
 ↓
Observation / Event
 ↓
Authoritative Environment State 更新
```

原则：

> **LLM 可以提出世界改变，但不能宣布世界已经改变。**

---

## 2.4 Static Definition 与 Dynamic Instance 分离

Game Definition 和 Agent Definition / Archetype 是基础定义；World、Agent Instance Descriptor、Agent State、Experience、Memory 是具体实例态。

```text
Game Definition
        +
World Instance

Agent Definition / Archetype
        +
Agent Instance Descriptor
        +
World-scoped Agent State / Memory
```

同一个 Definition 可以用于多个世界实例中的具体实体。

Stardew 中经常是 1:1：

```text
                  Abigail Definition
                        │
          ┌─────────────┴─────────────┐
          ↓                           ↓

   World A Abigail              World B Abigail

   state A                      state B
   cognition A                  cognition B
   experience A                 experience B
   memory A                     memory B
```

但动态实体游戏中通常是 N:1：

```text
                  villager/farmer Definition
                              │
              ┌───────────────┴───────────────┐
              ▼                               ▼
world_001 / villager:uuid-1       world_001 / villager:uuid-2
```

因此：

```text
entity_id != definition_id
```

Stardew 只是允许 `definition_id == entity_id` 的特例。

---

## 2.5 History、Memory、Current Context 是三个不同对象

```text
Experience / History
    实际发生过什么。

Memory
    从历史中保留了什么、如何理解过去。

Model Context
    当前这一次推理应该看到什么。
```

因此：

```text
Experience != Memory
Memory != Model Context
Trace != Memory
```

Memory 应该可以从 Experience 中形成；Model Context 应由 Context Engine 从 Sources 中投影。

---

# 3. 三层 Context Architecture

```text
┌────────────────────────────────────────────┐
│ 1. Context Sources                         │
│                                            │
│ Definition                                 │
│ Environment State                          │
│ Cognitive State                            │
│ Experience                                 │
│ Memory                                     │
│ Current Run                                │
└────────────────────────────────────────────┘
                     ↓
┌────────────────────────────────────────────┐
│ 2. Context Engine                          │
│                                            │
│ Scope Resolution                           │
│ Source Loading                             │
│ Authority                                  │
│ Freshness                                  │
│ Retrieval                                  │
│ Selection                                  │
│ Budget                                     │
│ Projection / Rendering                     │
└────────────────────────────────────────────┘
                     ↓
┌────────────────────────────────────────────┐
│ 3. Model Context                           │
│                                            │
│ System / Definitions                       │
│ Authoritative Facts                        │
│ Cognitive State                            │
│ Recent / Relevant Experience               │
│ Relevant Memory                            │
│ Current Event / Observation                │
│ Current Turn Transcript                    │
│ Tools                                      │
└────────────────────────────────────────────┘
                     ↓
                    LLM
```

---

# 4. Context Sources

## 4.1 Context Scope Contract

Context Scope Contract 是 Context Architecture 的核心 Contract。

| Context Source | Scope |
| --- | --- |
| Runtime Policy | Runtime |
| Game Definition / Lore | `game_id` |
| World Environment State | `game_id + world_id` |
| Agent Definition / Archetype | `game_id + definition_id` |
| Agent Instance Descriptor | `game_id + world_id + entity_id` |
| Agent Environment State | `game_id + world_id + entity_id` |
| Agent Cognitive State | `game_id + world_id + entity_id` |
| World Experience | `game_id + world_id` |
| Agent Experience | `game_id + world_id + entity_id` |
| Agent Memory | `game_id + world_id + entity_id` |
| Current Event | 当前 AgentTurn |
| Current Event Context Facts | 当前 AgentTurn |
| Current Observation | 当前 AgentTurn |
| Current Turn Transcript | 当前 AgentTurn |
| Available Tools | 当前 AgentTurn |

这张表回答：

> **这份 Context 属于谁？**

它不回答存储方式，也不回答冲突时谁优先。

---

## 4.2 Scope 层级关系

```text
Runtime
│
├── Runtime Policy
│
└── Game
    │
    ├── Game Definition
    │   scope = game_id
    │
    ├── Agent Definition / Archetype
    │   scope = game_id + definition_id
    │
    └── World Instance
        │
        ├── World Environment State
        ├── World Experience
        │
        └── Agent Instance
            │
            ├── Agent Instance Descriptor
            ├── Agent Environment State
            ├── Agent Cognitive State
            ├── Agent Experience
            └── Agent Memory
```

旁边还有当前执行态：

```text
Current AgentTurn
│
├── AgentSessionKey
├── Current Event
├── Current Observation
├── Current Turn Transcript
└── Available Tools
```

---

# 5. Definition Sources

Definition 回答：

> **“这个游戏 / 角色本来是什么？”**

## 5.1 Runtime Policy

Scope：

```text
Runtime
```

例如：

```text
行为边界
安全规则
Agent Runtime 通用约束
模型使用规范
```

---

## 5.2 Game Definition / Lore

Scope：

```text
game_id
```

例如 Stardew：

```text
一年四季
每季 28 天
Pelican Town 世界设定
节日、关系、商店等基础规则
```

它回答：

> “这个游戏通常如何运作？”

不同 world_id 通常共享同一个 Game Definition。

`game_version` 可以作为 Definition 的 metadata、兼容性说明或发布记录，
但不参与 Context Scope。

当游戏版本升级导致基础规则变化时，应更新对应 `game_id` 下的 Definition，
而不是让 Runtime 在每次 Context 构建时用 `game_version` 参与 identity / scope 分裂。

---

## 5.3 Agent Definition / Archetype

Scope：

```text
game_id + definition_id
```

例如：

```text
Stardew:
    npc:Linus
    npc:Abigail

Minecraft:
    villager/farmer
    villager/librarian

RimWorld:
    human_pawn
    trader
```

它回答：

> “这一类 Agent 应该如何说话、思考和行动？”

Agent Definition 是可复用模板，不一定对应某个具体 `entity_id`。

Stardew 的固定 NPC 可以使用：

```text
definition_id = entity_id
```

但动态实体游戏不应依赖这个等式。

---

## 5.4 Agent Instance Descriptor

Scope：

```text
game_id + world_id + entity_id
```

例如：

```text
display_name
definition_id
entity_type
profession
traits
faction
relationship hints
adapter-provided metadata
```

它回答：

> “当前 world 中这个具体实体是谁？”

Agent Instance Descriptor 是事实输入，不是完整 prompt。

Adapter 可以提供 Descriptor facts；Runtime / Context Sources 根据 `definition_id` 加载 Agent Definition，并与 Descriptor、State、Memory、Observation 一起组合 Model Context。

---

# 6. Environment State

Environment State 回答：

> **“游戏现在实际上是什么样？”**

Source of Truth：

```text
Game / Adapter
```

Runtime 中的 Environment State 是 Game-derived representation。

---

## 6.1 World Environment State

Scope：

```text
game_id + world_id
```

例如：

```text
Year 2 Fall 12
Community Center completed
当前节日状态
某关键剧情已完成
沙漠已解锁
```

它描述一个具体 World Instance 已经发展成什么样。

---

## 6.2 Agent Environment State

Scope：

```text
game_id + world_id + entity_id
```

例如：

```text
friendship_hearts = 2
location = Town
married = false
health = ...
inventory = ...
```

这些信息由 Game 决定，LLM 不能直接覆盖。

---

## 6.3 Current Observation

Scope：

```text
当前 AgentTurn
```

它表示当前时刻 Agent 被允许看到的最新世界事实，例如：

```text
current_time
weather
location
nearby_entities
player state
当前关系数据
```

Current Observation 通常比 Runtime 中缓存的 Environment State 更新，因此具有更高 freshness。

---

## 6.4 Volatile Facts 与 Durable Facts

不是所有游戏事实都值得持久化。

### Volatile Facts

例如：

```text
当前坐标
附近 NPC
当前动画
实时朝向
当前时间
```

主要依赖 Current Observation，不应为了 Context 把整个游戏内存镜像进 Runtime Storage。

### Slow-changing / Durable Facts

例如：

```text
Community Center completed
marriage status
friendship level
关键剧情 milestone
世界解锁状态
```

可以形成 Runtime State Projection，并根据需要持久化。

---

# 7. Cognitive State

Cognitive State 回答：

> **“Agent 当前怎么想？”**

Scope：

```text
game_id + world_id + entity_id
```

Source of Truth：

```text
Runtime Agent
```

例如：

```text
mood = happy
trust_player = high
belief_player_is_kind = true
current_goal = talk_to_player
attention = player
```

Environment State 与 Cognitive State 可以不完全一致。

例如：

```text
Environment Fact:
friendship_hearts = 2

Cognitive State:
trust_player = high
```

这是允许的，因为：

```text
friendship_hearts
    = 游戏规则事实

trust_player
    = Agent 主观认知
```

但下面是不允许的：

```text
Game:
friendship_hearts = 2

Runtime Environment State:
friendship_hearts = 8
```

如果 Runtime 与 Game 对同一权威事实产生冲突，应以 Game / Current Observation 为准。

---

# 8. Experience / History

Experience 回答：

> **“实际上发生过什么？”**

它更接近 append-only history，而不是 Agent 的主观 Memory。

---

## 8.1 World Experience

Scope：

```text
game_id + world_id
```

例如：

```text
Community Center completed
某个 Festival 已发生
玩家修复温室
世界进入某剧情阶段
```

这些历史不一定属于某一个 Agent。

---

## 8.2 Agent Experience

Scope：

```text
game_id + world_id + entity_id
```

例如：

```text
Day 15:
player gave Abigail an amethyst

Day 17:
player talked to Abigail near the lake

Day 18:
Abigail called speak(...)
ActionResult = SUCCEEDED
```

Experience 可以包含：

```text
GameEvent
GameTime / event sequence
Observation snapshot / reference
Turn
ToolCall(s)
ActionResult(s)
Turn terminal state
```

Experience 是后续 Memory Formation 的事实基础。

---

# 9. Memory

Memory 回答：

> **“过去哪些信息值得继续影响未来行为？”**

Scope：

```text
game_id + world_id + entity_id
```

Memory 不等于完整 History。

关系：

```text
Experience
    ↓
selection / consolidation / interpretation
    ↓
Memory
```

---

## 9.1 Recent Memory

用于回答：

> “刚才发生了什么？”

例如：

```text
上一轮玩家问了什么
上一轮 Agent 做了什么
最近几次交互结果
```

Recent Memory 不应等同于“最近几次 speak 文本”。

它应从成功 AgentTurn 中形成，记录对后续行为有语义影响的可见结果：

```text
utterance: 玩家刚才说过什么
speak: NPC 刚才说过什么
emote: NPC 刚才表达了什么情绪
move: NPC 刚才移动到哪里
item / quest action: NPC 刚才给予、接受或触发了什么
```

内部可以保留较完整的结构化事实；
进入 Model Context 时必须经过 projection / compaction。

例如完整记录中可以包含 `GameTime`、`SourceEventSequence`、`SourceContextFacts`、`ToolCall` 和 `ActionResult`，
但 prompt 中只需要类似：

```text
- today 06:20: player said "..."; said "..."
- previous day Y1 S1 D2 18:20: moved to the river
```

同一条 MemoryRecord 的模型可见摘要必须先渲染 `SourceContextFacts`，再渲染 visible action outcomes。

这表示：

```text
触发本 Turn 的玩家输入 / 指令
    ->
本 Turn 内 Agent 已确认发生的可见动作结果
```

成功完成的 AgentTurn 可以把 `SourceContextFacts` 与 successful visible outcomes 写入同一条 Recent Memory。失败、超时或预算耗尽的 Turn 不得只凭 `SourceContextFacts` 写入 Memory；已确认成功的 Environment outcome 仍按对应阶段的 action outcome 规则处理。若 technical terminal failure 按阶段规则记录 prior successful outcome，该 MemoryRecord 不携带本 Turn 的 `SourceContextFacts`。

既没有 `SourceContextFacts`，也没有 successful visible outcomes 的 Turn 不写 Recent Memory。

Recent Memory 的时间语境应使用游戏内时间，而不是 Runtime wall-clock。
Runtime wall-clock 更适合 debug、TTL 和存储维护。

## 9.1.1 Memory Timeline Consistency

Recent Memory 进入 Model Context 前必须经过当前游戏时间线选择。该规则处理玩家读取更早存档、回档或世界时间回退后的上下文一致性，避免 Agent 在当前时间线中看到未来发生过的记忆。

当 Current Event 或 Current Observation 能提供当前 `GameTime` 时：

```text
MemoryRecord.GameTime > CurrentGameTime
    不进入本次 Model Context

MemoryRecord.GameTime <= CurrentGameTime
    可以按 Relevance / Budget 继续参与选择
```

过滤条件是严格 `>`，不是 `>=`。与当前 `GameTime` 相等的 MemoryRecord 继续参与选择。

比较顺序使用游戏时间字段：

```text
year, season, day, hour, minute, tick
```

如果 MemoryRecord 或当前上下文缺少可比较的 `GameTime`，Context Engine 不做未来时间过滤，只应用普通 Recent / Relevance / Budget 规则。

未来时间过滤后，Context Engine 默认保持 MemoryStore 返回顺序。

连续、同一非空 `GameTime` 且 `SourceEventSequence` 非 0 的 MemoryRecord 片段应按 `SourceEventSequence` 升序稳定排序。`SourceEventSequence` 来源于 `GameEvent.sequence`。缺少可比较 `GameTime` 或 `SourceEventSequence` 时，该 MemoryRecord 保持 MemoryStore 返回顺序。

未来时间过滤必须发生在 memory context estimated-token budget trim 之前，避免未来时间记录占用预算并挤掉当前时间线中的有效记忆。

MVP0 首先在 Context selection 阶段过滤未来时间 Memory，不要求从 MemoryStore 中删除记录。持久化 Memory 的 prune / invalidation 属于 Environment Recovery 与长期状态阶段。

---

## 9.2 Episodic Memory

用于保存具体经历：

```text
Spring 15:
玩家第一次送 Abigail 紫水晶。
```

它通常来自 Agent Experience 的长期保留。

---

## 9.3 Core / Semantic Memory

用于保存从多个 Experience 中形成的长期重要认知：

```text
The player knows that I love amethyst.
The player has repeatedly treated me kindly.
```

它不是每次都需要 semantic retrieval；其中极少量高价值内容未来甚至可以常驻 Context。

---

## 9.4 Memory 不是 World Fact

例如：

```text
Memory:
玩家最近经常送我喜欢的礼物。
```

不能直接推出：

```text
friendship_hearts = 8
```

如果 Current Observation 显示：

```text
friendship_hearts = 2
```

则当前事实仍然是 2。

---

# 10. Current Run

Current Run 是 Context Engine 的查询 Anchor。

术语说明：

```text
在 MVP0 / Phase4 中：
    Current AgentTurn
    ==
    一次 AgentTurn
```

本架构文档统一使用 `AgentTurn` 作为当前执行边界。
后续 Phase5/6 若需要引入更高层的 `AgentRun` 概念，必须单独形成 Architecture Decision。

例如：

```text
GameID      = stardew-valley
WorldID     = Farm001
EntityID    = npc:Abigail
DefinitionID = npc/abigail

Event
Observation
Turn Transcript
Tools
```

Current Run 不只是本身要进入 Context，更重要的是它决定 Context Engine 去哪些 Scope 中加载数据。

---

## 10.1 Scope Resolution 示例

当前 AgentTurn：

```text
game_id      = stardew-valley
world_id     = Farm001
entity_id    = npc:Abigail
definition_id = npc/abigail
```

Context Engine 可以推导：

```text
Runtime Policy
    scope = Runtime

Game Definition
    scope = stardew-valley

World Environment State
    scope = stardew-valley + Farm001

Agent Definition / Archetype
    scope = stardew-valley + npc/abigail

Agent Instance Descriptor
    scope = stardew-valley + Farm001 + npc:Abigail

Agent Environment State
    scope = stardew-valley + Farm001 + npc:Abigail

Agent Cognitive State
    scope = stardew-valley + Farm001 + npc:Abigail

Agent Experience
    scope = stardew-valley + Farm001 + npc:Abigail

Agent Memory
    scope = stardew-valley + Farm001 + npc:Abigail
```

因此：

> Context Engine 不需要猜“应该读谁的数据”，Scope Contract 已经回答这个问题。

---

## 10.1.1 Target EntityRef Canonical Resolution

`target_entity_id` 必须在 Gateway / Trigger admission 阶段解析为唯一、无冲突的目标 `EntityRef`。

```text
target_entity_id 不存在于 GameEvent.entities
    EventAck REJECTED。

target_entity_id 出现一次
    使用该 EntityRef。

target_entity_id 出现多次且字段完全一致
    规范化为同一 EntityRef。

target_entity_id 出现多次且 definition_id / entity_type / display_name 冲突
    EventAck REJECTED。
```

Context Engine 使用 canonical Target EntityRef，不按 `GameEvent.entities` 列表顺序选择第一个匹配项。

---

## 10.2 Current Event Context Facts

Current Event 可以携带 `ContextFact`。

`ContextFact` 是 Adapter 显式提供的 model-visible event context，用于把玩家输入、选择、指令或其他对后续行为有语义影响的事件事实交给 Runtime。

它解决的问题是：

```text
GameEvent.payload
    Adapter / game-specific event data

ContextFact
    Runtime 可以通用投影进 Model Context / Memory 的事件事实
```

推荐最小结构：

```text
kind
actor_entity_id
target_entity_id
scope_id
text
label
attributes
```

Runtime 可以解释的 `kind` 必须来自通用词表。

MVP0 通用词表：

```text
utterance
choice
command
interaction
```

`event_type` 是 Adapter / game-specific 触发名，不应成为 Runtime memory projection 的业务分支条件。

规则：

- `ContextFact` 属于当前 AgentTurn；
- `ContextFact` 引用 `entity_id`，不承载 `definition_id`；
- `ContextFact` 不参与 Agent Binding，不替代 Agent Definition；
- `ContextFact` 不替代 Observation；当前世界事实仍由 Current Observation 表达；
- `ContextFact` 不替代 ExperienceLog；它只是本次事件中可进入上下文投影的事实；
- Runtime 可以把 `ContextFact` 投影为 Recent Memory，但不得解析 game-specific `Observation.state` 来倒推事件事实。
- `GameEvent.payload` 可以保留同一事实的 Adapter / event-specific 表达；当前 Turn 的 Event JSON 可以包含这点冗余。
- 跨 Turn Recent Memory 只消费 `ContextFact`，不从 payload 字段推导 Memory。

例如 Stardew 中：

```text
GameEvent(player_said_to_npc)
ContextFact(kind=utterance, actor_entity_id=player:local, target_entity_id=npc:Abigail, scope_id=conv_12, text="Can you come here?")
```

第二个游戏可以用同一结构表达：

```text
GameEvent(squad_command)
ContextFact(kind=utterance, actor_entity_id=player:commander, target_entity_id=unit:medic-2, scope_id=radio:alpha, text="Hold position.")
```

---

## 10.3 Current Turn Transcript

Current Turn Transcript 表示当前 AgentTurn 内已经发生的 step-local execution facts。

Scope：

```text
当前 AgentTurn
```

例如 Phase5 中：

```text
Step 1
    ToolCall speak(...)
    ToolResult succeeded

Step 2
    ToolCall emote(...)
    ToolResult succeeded
```

Current Turn Transcript 的来源是 Runtime 已执行的 ToolCall 和 Environment 返回的 terminal ToolResult。它用于让后续 step 看到本 Turn 前面已经发生的动作和结果。

Current Turn Transcript 不等于 Memory，也不等于 Experience：

```text
Current Turn Transcript
    当前 Turn 内早先 step 的执行事实。

Experience
    Turn 完成后可记录的实际历史。

Memory
    从历史中选择、压缩或解释后保留的长期/短期认知材料。
```

进入 Model Context 时，ToolResult 应以 provider-neutral 形式回灌，并受当前 Turn 的 step、tool result output 和 context budget 限制。

Phase7 的 Transcript 裁剪必须保持 ToolCall / ToolResult 关联完整。当前数据模型下，一个 Transcript 原子组是：

```text
一条包含 ToolCalls[] 的 assistant message
    +
紧随其后的、ToolResult IDs 与其对应的 tool message
```

裁剪时只能整组保留或整组删除。Model Context 不得保留孤立 ToolCall 或孤立 ToolResult，也不得破坏原始消息顺序和 `tool_call_id` 关联。

---

# 11. Context Authority Contract

Scope 回答“属于谁”。

Authority 回答：

> **“谁说了算？”**

| Context Source | Authority |
| --- | --- |
| Runtime Policy | Runtime Configuration |
| Game Definition | Static Definition |
| Agent Definition | Static Definition |
| World Environment State | Game-derived |
| Agent Environment State | Game-derived |
| Current Observation | Game / Adapter |
| Current Event Context Facts | Adapter-declared |
| Current Turn Transcript | Runtime Execution + Environment ToolResult |
| Cognitive State | Runtime Agent |
| Experience | Recorded Event / Turn |
| Memory | Runtime Memory System |
| LLM inference | 非 authoritative |

---

## 11.1 Environment Fact Authority

针对游戏当前事实，推荐优先级：

```text
Current Observation
        >
Fresh Runtime Environment Projection
        >
Historical Experience
        >
Memory
        >
LLM inference
```

例如：

```text
Observation:
friendship = 2

Memory:
relationship feels very close
```

最终 Context 中：

```text
Authoritative Fact:
friendship = 2

Subjective / Memory:
relationship feels close
```

两者可以同时存在，但不能把 subjective inference 当事实。

---

## 11.2 Definition Authority

固定角色设定：

```text
Agent Definition
    >
Memory inference
```

例如 Agent Definition 明确：

```text
Abigail 喜欢 amethyst
```

Memory 不应该因为一条异常历史记录永久重写基础 Definition。

---

## 11.3 Experience Authority

针对“过去是否真的发生过某件事”：

```text
Recorded Experience
    >
Memory Summary
```

Memory 应允许未来从 Experience 重新生成或修正。

---

# 12. Freshness Contract

Authority 高不代表永远有效。

Context Engine 还需要考虑 freshness。

例如：

```text
Runtime Projection:
Abigail.location = Town
observed 30 game minutes ago

Current Observation:
Abigail.location = Mountain
```

则当前 Observation 应覆盖旧 Projection。

因此持久化的 State Projection 应尽量带 provenance：

```text
game_id
world_id
subject_entity_id
fact_key
fact_value

source
source_event_id
source_turn_id
observed_at_game_time
observed_revision
updated_at
```

Context Engine 才能判断：

```text
来源是什么？
什么时候观察的？
是否已经被更新事实覆盖？
```

---

# 13. Context Engine

Context Engine 不是简单的 Prompt Builder。

它负责把不同 Scope、Authority、Freshness 和 Relevance 的 Context Sources，投影为当前模型调用应该看到的有限上下文。

完整流水线：

```text
Current AgentTurn
        ↓
Scope Resolution
        ↓
Source Loading
        ↓
Authority Resolution
        ↓
Freshness Check
        ↓
Retrieval
        ↓
Relevance Selection
        ↓
Budget
        ↓
Projection / Rendering
        ↓
Model Context
```

---

## 13.1 Scope Resolution

根据 Current Run 的：

```text
game_id
world_id
entity_id
resolved definition_id
```

计算当前调用可以访问的 Context Scope。

这是 Context Engine 的第一步。

---

## 13.2 Source Loading

按照 Scope 加载：

```text
Game Definition
World Environment State
Agent Definition / Archetype
Agent Instance Descriptor
Agent Environment State
Cognitive State
Experience
Memory
```

Current Event / Observation / Turn Transcript / Tools 则来自本次 AgentTurn。

---

## 13.3 Authority Resolution

当多个 Source 描述同一类信息时，先按 Authority Contract 判断谁具有事实优先级。

例如：

```text
Current Observation:
friendship = 2

old projection:
friendship = 1

memory:
relationship is close
```

输出：

```text
friendship = 2
```

并允许主观 Memory 作为额外语义存在。

---

## 13.4 Freshness

对 Environment State 检查：

```text
observed time
revision
current world
current entity
```

避免把旧 World / 旧实体 / 旧状态当作当前事实。

---

## 13.5 Retrieval

Retrieval 主要用于：

```text
Experience
Memory
大型 World Knowledge
```

而不是所有 Context Source 都做 Vector Search。

例如固定 Agent Definition 不需要每轮 semantic retrieval。

---

## 13.6 Selection

即使 Runtime 拥有完整 World State，也不能全部放入模型。

```text
Persistent / Available World State
        ↓
Context Selector
        ↓
Relevant World Context
```

例如 Linus 当前交互可能需要：

```text
当前年份
季节
天气
当前地点
玩家与 Linus 关系
最近关键世界事件
```

但不需要：

```text
玩家仓库第 37 格物品
Sebastian 当前坐标
沙漠商店完整库存
```

---

## 13.7 Recent 与 Relevant 分离

Recent 解决：

> “刚才发生了什么？”

Relevant Retrieval 解决：

> “过去很久以前有什么和现在有关？”

例如：

```text
Recent:
Turn 97 / 98 / 99

Relevant Memory:
30 天前玩家第一次送 Abigail 紫水晶
```

两者都可能同时进入本轮 Context。

---

## 13.8 Budget

Context Engine 必须有明确预算。

不是：

```text
有多少 Context 就全部塞多少。
```

而是：

```text
Definition
Facts
Cognitive State
Recent
Relevant Memory
Current Event
Current Observation
Current Turn Transcript
```

在有限模型窗口中进行分配和裁剪。

Phase7 第一版使用 provider-neutral approximate budget：

```text
预算单位 = provider-neutral estimated tokens
```

整体预算至少覆盖：

```text
Request.System
Request.Messages
Request.Tools
Request.Controls
必要的 section framing / labels
```

Provider SDK 外层 JSON envelope 不要求精确计入，但 ToolDefinition 不能被当作免费输入。

结构化内容必须按字段、数组项或完整 Section 裁剪。Event payload、Observation state、ContextFact attributes 和 ToolResult output 不得被截成非法 JSON。

长期 Context Engine 需要处理：

```text
Relevance
Freshness
Authority
Priority
Budget
```

---

# 14. Model Context

Model Context 是：

> Context Engine 针对本次模型调用生成的最终、有限、结构化 Context Projection。

它不等于一个超级字符串。
也不等于把 Memory / Experience 的完整存储记录原样塞给模型。

推荐逻辑结构：

```text
Model Context
│
├── System
│   ├── Runtime Policy
│   ├── Game Definition
│   └── Agent Definition / Archetype
│
├── Environment Context
│   ├── Relevant Authoritative Facts
│   └── Current Observation
│
├── Agent Context
│   ├── Agent Instance Descriptor
│   ├── Cognitive State
│   ├── Relevant Memory
│   └── Recent Experience
│
├── Current Run
│   ├── Current Event
│   ├── Current Event Context Facts
│   └── Current Turn Transcript
│
└── Tools
    └── Tool Definitions
```

最终公式：

```text
Model Context
=
Policy / Definitions
+
Relevant Authoritative Facts
+
Current Observation
+
Selected Cognitive State
+
Relevant Retrieved Memory
+
Recent Experience
+
Current Event
+ Current Event ContextFacts
+
Current Turn Transcript
+
Available Tools
```

---

# 15. Context 构建示例

玩家在 `Farm001` 点击 Abigail：

```text
game_id      = stardew-valley
world_id     = Farm001
entity_id    = npc:Abigail
definition_id = npc/abigail
```

Current Run：

```text
Event:
player_interacted_with_npc

Observation:
18:20
Rain
Town
friendship = 2 hearts
player nearby

Turn Transcript:
empty before Step 1
contains prior ToolCalls / ToolResults from the same Turn in Step 2+

Tools:
speak
emote
```

Context Engine：

```text
1. Scope Resolution
   → stardew-valley
   → Farm001
   → npc:Abigail
   → npc/abigail

2. Load Definition
   → Game Definition
   → Abigail Agent Definition

3. Load Environment State
   → Farm001 relevant world facts
   → Abigail environment facts

4. Load Cognitive State
   → Abigail mood / beliefs / goal

5. Retrieve
   → recent experience
   → relevant memory

6. Authority
   Observation friendship=2
   overrides stale projection / memory inference

7. Selection + Budget
   → remove irrelevant world facts

8. Render Model Context
```

最终可能包含：

```text
[Game / Character Definition]
Abigail ...

[Authoritative Current Facts]
Current world: Farm001
Time: 18:20
Weather: Rain
Friendship with player: 2 hearts
Location: Town

[Cognitive State]
You currently feel increasingly comfortable around the player.

[Recent Experience]
The player spoke with you earlier today.

[Relevant Memory]
The player previously gave you an amethyst.

[Current Event]
The player has approached and interacted with you.

[Current Turn Transcript]
Step 1 tool results from this Turn, if any.
```

这里：

```text
friendship = 2 hearts
```

是 Game-owned Fact；

```text
feel increasingly comfortable
```

是 Runtime-owned Cognitive State；

两者不会互相覆盖。

---

# 16. Storage Architecture 与 Context Architecture 分离

Context Architecture 描述：

> 数据在语义上是什么、属于谁、谁有权威。

Storage Architecture 描述：

> 数据物理上怎么保存。

两者不能混为一谈。

推荐长期映射：

| Semantic Source | Possible Storage |
| --- | --- |
| Runtime Policy | Config |
| Game Definition | YAML / Markdown |
| Agent Definition | YAML / Markdown |
| Environment State Projection | Structured Store / SQLite |
| Cognitive State | Structured Store / SQLite |
| Experience History | JSONL / SQLite append log |
| Memory | SQLite / structured store |
| Memory Text Search | FTS secondary index |
| Semantic Retrieval | Vector secondary index |
| Current Observation | Current Run only / optional trace |
| Current Event | Current Run + Experience |
| Current Turn Transcript | Current Run only / optional trace |

---

## 16.1 JSONL 更适合 History

例如：

```text
event_received
observation_received
turn_started
tool_called
action_succeeded
turn_completed
```

History 更接近 append-only log。

这里的 `History JSONL` 指未来独立的 `ExperienceLog` / `HistoryLog` 存储选择，
不是当前 Runtime 的 `Trace JSONL`。

Trace JSONL 只能回答：

```text
Runtime 当时如何执行？
```

它不能直接作为 Experience 真源，也不能被 Context Engine 反向解析成 Memory。

---

## 16.2 SQLite / Structured Store 更适合 Current Projection 与 Memory

例如：

```text
World State Projection
Cognitive State
Agent Memory
```

这些数据通常需要：

```text
Get current
Update
Filter by world / entity
Recent
Search
```

结构化数据库更自然。

---

## 16.3 Vector 不是 Memory 真源

长期如果增加 Embedding / Vector Search：

```text
MemoryRecord
    ↓
Authoritative Structured Store
    │
    ├── FTS Index
    └── Vector Index
```

Vector Index 必须可删除、可重建。

原则：

> **Vector 是 retrieval index，不是 Memory 本身。**

---

# 17. Experience、Trace、Memory 的边界

三者不能混用。

## Trace

回答：

> Runtime 当时是怎么执行的？

属于 Observer。

关闭 Trace 不应该改变 Agent 行为。

---

## Experience

回答：

> 实际发生过什么？

它可以成为 Memory Formation 的事实基础。

---

## Memory

回答：

> 哪些过去经验值得继续影响未来行为？

属于 functional state，会影响未来 Agent 推理。

关系：

```text
Runtime Execution
    │
    ├──→ Trace
    │      Observer
    │
    └──→ Experience
           │
           ↓
       Memory Formation
           ↓
         Memory
```

Phase4 实现说明：

```text
Phase4 不要求建立持久 ExperienceStore。
```

Phase4 的 `MemoryProjector` 可以直接从当前成功 AgentTurn 的：

```text
GameEvent
GameTime / event sequence
ToolCall(s)
ToolResult(s)
AgentSessionKey
turn_id
```

确定性投影出 Recent Memory。

Phase4 的最小策略是：

```text
一个成功 AgentTurn -> 一条 Recent MemoryRecord
Renderer -> 只渲染简短 recent interaction summary
```

Phase5 实现说明：

```text
Current Turn Transcript
    保存当前 Turn 内早先 step 的 ToolCall / ToolResult。

Recent Memory
    在 Turn 完成后形成，一条 Turn 级记录可以包含多个 successful tool outcome。
```

Phase5.6 实现说明：

```text
Current Event ContextFacts
    保存 Adapter 显式声明的 model-visible event facts，例如玩家 utterance。

Recent Memory
    在 Turn 完成后形成，一条 Turn 级记录可以同时包含 SourceContextFacts 和 successful visible tool outcomes。

Context Engine
    渲染 Recent Memory 前过滤 GameTime > CurrentGameTime 的记录。
```

这只是 Experience 体系的最小 vertical slice；
不代表长期架构中 Memory 必须绕过 Experience。

---

# 18. 与 AgentSession Identity 的关系

Phase3 后：

```text
AgentSessionKey
=
game_id
+
world_id
+
entity_id
```

它不仅用于 Runtime 调度，也天然成为 Agent-instance-scoped Context 的地址基础。

例如：

```text
Agent Environment State
Agent Cognitive State
Agent Experience
Agent Memory
```

都使用：

```text
game_id + world_id + entity_id
```

作为 Scope。

但 Agent Definition / Archetype 使用：

```text
game_id + definition_id
```

因为它属于可复用模板，而不是具体 World Instance。

Agent Instance Descriptor 使用：

```text
game_id + world_id + entity_id
```

因为它描述的是当前 world 中这个具体实体。

---

# 19. 参考 Harness 的设计启发

## Pi

值得借鉴：

```text
History != Current Context
```

完整 Session History 可以长期保存；当前 Model Context 是对 History 的 projection / compaction，而不是无限追加完整历史。

---

## Hermes

值得借鉴：

```text
Always-on Core Context
!=
Searchable Long-term Memory
```

人格、重要固定信息、Project Context、Recent History、Searchable History 应分层，而不是全部依赖 Vector Retrieval。

---

## DeepSeek Harness

值得借鉴：

```text
Domain Semantics
!=
Storage Backend
```

History / SessionEvent 是逻辑模型；JSONL / SQLite 是可替换持久化实现。

同时：

```text
History
→ Projection
→ Model Context
```

比直接把 messages[] 当唯一真源更稳定。

---

## GameAgent 自身需要增加的维度

GameAgent 相比 Coding Agent 多了：

```text
WorldScope
Agent Instance Scope
Environment Authority
Game-derived State
Cognitive State
```

因此 GameAgent 的 Context Architecture 应是：

> **world-and-agent-centric，而不是纯 session-centric。**

---

# 20. 长期 Context Engine 可能演进的内部结构

Context Engine 不应永久等于一个 `ContextBuilder`。

未来可以自然拆成：

```text
Context Engine
│
├── Scope Resolver
│
├── Source Resolver / Loader
│
├── Authority Resolver
│
├── Freshness Policy
│
├── Fact Selector
│
├── Experience Retriever
│
├── Memory Retriever
│
├── Context Budget Manager
│
└── Renderer / ModelRequest Builder
```

但这些是职责边界，不要求一次全部实现。

第一版仍可以由一个较小的 ContextBuilder 承担其中少量职责，等真实复杂度出现后再拆分。

---

# 21. 架构不变量

```text
1. Every Context Source MUST declare its Scope.

2. Game / Adapter 是当前 Environment Facts 的 Ground Truth。

3. LLM 不能直接覆盖 Game-owned Environment State。

4. Agent Cognitive State 可以和 Environment Fact 不完全一致，
   但必须明确是 subjective runtime state。

5. Current Observation 对当前游戏事实优先于旧 Projection / Memory。

6. Current Turn Transcript 记录当前 Turn 内早先 step 的执行事实，
   不等于 Experience 或 Memory。

7. Current Event ContextFacts 是 Adapter 显式声明的模型可见事件事实，
   Runtime 不从 game-specific Observation state 倒推事件事实。

8. Experience 记录真实发生历史，Memory 是对历史的选择或解释。

9. Trace 不是 Memory，关闭 Trace 不改变 Agent 行为。

10. Static Definition 与 World-scoped Dynamic Instance 分离。

11. Model Context 是 Context Sources 的有限 projection，
   不是所有数据的完整副本。

12. Context Engine 必须同时考虑：
    Scope / Authority / Freshness / Relevance / Budget。

13. GameTime > CurrentGameTime 的 Memory 不进入本次 Model Context。

14. Storage Backend 不得反向定义 Context Domain Model。

15. Vector Index 只能是可重建的 retrieval index，不能成为 Memory 唯一真源。

16. Target EntityRef 必须唯一、无冲突；Runtime 不得按列表顺序选择第一个目标实体。

17. Current Turn Transcript 进入模型前必须保持 ToolCall / ToolResult 关联完整。
```

---

# 22. 一句话总结

> **GameAgent Context Architecture 的核心不是“怎么拼 Prompt”，而是建立一套有 Scope、有 Authority、有 Freshness、有 History 与 Memory 边界的 Context Projection 体系：Game 决定世界事实，Runtime 管理 Agent 的认知与记忆，Context Engine 针对当前 AgentTurn 从所有信息源中选择本轮真正需要的内容，最终生成有限、结构化的 Model Context。**
