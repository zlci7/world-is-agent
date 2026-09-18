# World Is Agent (WIA) Runtime 整体架构设计规范

> **Public Documentation Note (2026-09-01):** 根目录 [ARCHITECTURE.md](../../ARCHITECTURE.md) 和 [docs/STATUS.md](../STATUS.md) 是 GitHub 首次读者的公开事实源。本文保留为详细架构基线和设计约束资料。
>
> **Version:** v0.7
> **Status:** Architecture Baseline
> **Baseline Evidence:** Phase1 Accepted + Phase2 Accepted + Phase3 Accepted + Phase4 Accepted + Phase5 Accepted + Phase5.5 Accepted + Phase5.6 Accepted + Phase6 Accepted + Phase6.5 Accepted
> **Revision Source:** [GameAgent 多游戏兼容性与 Agent Binding 决策](./GameAgent 多游戏兼容性与 Agent Binding 决策.md)（2026-08-22）；[GameAgent MVP0 Phase6 Async Action Protocol Strategy ADR](../phase06/GameAgent MVP0 Phase6 Async Action Protocol Strategy ADR.md)（2026-08-31）；[GameAgent MVP0 Phase6.5 技术开发与验收方案](../phase06/GameAgent MVP0 Phase6.5 技术开发与验收方案.md)（2026-09-02 Accepted）
> **Purpose:** 定义 WIA 的长期架构边界、核心运行模型、模块职责、依赖方向和演进约束。
> 本文中的 `MUST / MUST NOT / SHOULD / MAY` 为规范性关键词。
>
> **Current Positioning Note (2026-09-18):** WIA MVP0 已定型，当前工作方向是固定该版本并完成开源产品化，见仓库根目录 `AGENTS.md`。本文的架构边界继续有效；文中以 Phase 划分承载的工作组织方式属于当时的开发模型，不代表当前交付节奏。

------

# 1. 文档定位

本文档是 WIA 后续所有 Runtime / Protocol / Adapter 开发的上位架构约束。

它回答：

> **WIA 长期应该保持什么结构，以及后续加入 Memory、Multi-step、异步 Action、Scheduler、Reconnect 或更多 Game Adapter 时，哪些核心边界不能被破坏。**

本文档不负责规定：

```text
某个 Phase 的具体开发范围
具体开发顺序
具体测试命令
某个 Stardew 功能的实现方案
具体 protobuf 字段定义
具体 Prompt 内容
数据库或存储选型
```

这些内容分别由：

```text
Protocol 设计规范
阶段规划（历史阶段划分与验收范围）
Phase N 技术开发与验收方案
具体模块设计文档
```

负责；当前版本的迭代与交付流程见根目录 `AGENTS.md`。

架构层级关系：

```text
Architecture Baseline
        ↓
Protocol / Runtime Contracts
        ↓
Iteration Scope
        ↓
Implementation
        ↓
Test / Real-game Acceptance
```

Phase 技术方案原则上 MUST 遵守本文。

如果真实开发证明本文架构约束本身不成立，应先形成明确 Architecture Decision，并更新 Architecture Baseline，而不是由某个 Phase 静默绕过。

------

# 2. Baseline 来源

Architecture v0.7 不是纯理论设计。

它建立在已经完成并验收的多个真实阶段之上。

## Phase1 已验证

```text
真实 Stardew GameEvent
    ↓
Go Runtime
    ↓
Observation
    ↓
真实 LLM Provider
    ↓
ToolCall
    ↓
ActionRequest
    ↓
真实 Stardew Action
    ↓
ActionResult
```

Phase1 已证明：

```text
Runtime / Protocol / Adapter 边界可以真实落地。

Runtime 不需要依赖 Stardew / SMAPI 类型。

Provider-neutral Model abstraction 可以工作。

Agent Core 可以通过 Environment abstraction 操作游戏。

ToolCall 可以转换成 Protocol ActionRequest。

真实游戏、真实 Runtime、真实 LLM 可以形成完整 vertical slice。
```

## Phase2 已验证

Phase2 在 Phase1 基础上进一步证明：

```text
AgentTurn 可以成为明确执行边界。

Turn 可以具有 turn_id / trace_id 和唯一终态。

Capability schema 可以由 Adapter 驱动动态注册成 Tool。

speak → emote 不要求 Runtime 增加 tool-specific 主链路。

Trace 可以作为非阻塞 best-effort Observer。

Observe / Model / Action 都可以设置 bounded timeout。

Action timeout 后可以向 Adapter 发送 best-effort cancel。
```

## Phase3 已验证

Phase3 在 Phase2 基础上进一步证明：

```text
AgentSessionKey = game_id + world_id + entity_id 可以稳定路由多个 NPC。

EnvironmentSession 与 AgentSession 可以分离。

同一 AgentSession 可以串行执行，跨 AgentSession 可以并行。

Protocol v1alpha2 的 world_id / target_entity_id 能支撑显式实体路由。
```

## Phase4 已验证

Phase4 在 Phase3 基础上进一步证明：

```text
MemoryStore 可以按 AgentSessionKey 隔离短期 Memory。

ContextBuilder / Renderer 可以把 Event、Observation、Recent Memory 和 Tools 组合成模型上下文。

Memory load failure 可以 fail-open，Memory update failure 不改写已成功 Turn。

确定性 gateway 测试可以覆盖同实体排队、跨 session reconnect、不同实体隔离和断线 drain。
```

因此 v0.7 冻结的是：

> **已经有真实证据支持的核心边界，以及为了后续演进必须提前保持的架构不变量。**

------

# 3. 项目定义

GameAgent 是一个：

> **面向实时游戏环境的、事件驱动、Capability 驱动、与具体游戏解耦的 Agent Runtime / Harness。**

GameAgent 的核心运行模型是：

```text
Environment Event
    ↓
Observe
    ↓
Build Agent Context
    ↓
Model Decision
    ↓
Tool Call
    ↓
Environment Action
    ↓
Action Result
```

GameAgent 当前最重要的使用场景是：

```text
Game NPC Agent
```

但 Runtime 架构 MUST NOT 永久限制为 NPC 专用。

未来只要一个游戏实体能够通过 Environment 抽象表达：

```text
Event
Observation
Capability
Action
```

就可以成为 Agent Runtime 的控制对象。

Stardew Valley 是：

> **第一套真实 Adapter 和真实验证环境。**

而不是 GameAgent Runtime 本身。

------

# 4. 最高级别架构原则

整个项目 MUST 长期遵循：

```text
Agent owns intent.
Runtime owns cognition.
Protocol owns contracts.
Adapter owns translation.
Game owns execution.
```

中文：

```text
Agent
    负责意图。

Runtime
    负责认知、状态与执行编排。

Protocol
    负责 Runtime 与 Environment 的通信契约。

Adapter
    负责具体游戏与通用 Protocol 的翻译。

Game
    负责行为真正如何执行。
```

另一个核心原则：

```text
Agent / Runtime decides WHAT.
Adapter / Game decides HOW.
```

例如：

```text
move_to("lake")
```

Runtime MAY 决定：

```text
去湖边
```

Runtime MUST NOT 决定：

```text
Stardew 使用哪条路径
如何检测地图碰撞
NPC 每一帧如何移动
播放哪一个底层动画
调用哪个 SMAPI / Game1 API
```

这些属于 Adapter / Game。

------

# 5. 总体架构

```text
                         Model Provider
                              ↑
                              │
                              ↓

┌───────────────────────────────────────────────────────────┐
│                    GameAgent Runtime                      │
│                                                           │
│  Environment Gateway                                      │
│  Trigger / Turn Control                                   │
│  Agent Turn Core                                          │
│  Model Runtime                                            │
│  Tool Runtime                                             │
│  Agent State                                              │
│  Trace / Observability                                    │
│                                                           │
└────────────────────────────┬──────────────────────────────┘
                             │
                      Environment Port
                             │
══════════════════════ GameAgent Protocol ══════════════════
                             │
                      Current: gRPC
                             │
                             ↓
┌───────────────────────────────────────────────────────────┐
│                       Game Adapter                        │
│                                                           │
│  Event Translation                                        │
│  Observation Translation                                  │
│  Capability Declaration                                   │
│  Action Execution                                         │
│  Protocol Mapping                                         │
│                                                           │
└────────────────────────────┬──────────────────────────────┘
                             │
                       Game / Mod API
                             ↓
                           Game
```

必须明确：

```text
Runtime
!=
gRPC Server

Runtime
!=
LLM Wrapper

Runtime
!=
NPC Dialogue Backend
```

Gateway、Provider、Tool、Trace 都只是 Runtime 的子系统。

------

# 6. 依赖方向

合法依赖：

```text
             protocol
             ↑      ↑
            /        \
       runtime      adapter
                      ↓
                    game
```

即：

```text
runtime
    → protocol

adapter
    → protocol

adapter
    → concrete game API
```

禁止：

```text
runtime
    → adapters/stardew

adapter
    → runtime/internal
```

Protocol MUST NOT：

```text
依赖 Runtime Agent 逻辑
依赖 LLM Provider
依赖具体游戏 API
```

Runtime 核心逻辑 MUST NOT 依赖：

```text
SMAPI
Game1
Farmer
Stardew NPC
Abigail
Linus
PelicanTown
或任何其他具体游戏对象
```

Runtime 可以处理：

```text
entity_id
event_type
Observation
Capability
ActionRequest
ActionResult
```

因为这些属于 Environment contract。

------

# 7. 核心生命周期模型

GameAgent 正式区分：

```text
EnvironmentSession
AgentSession
AgentTurn
AgentStep
Action
```

关系：

```text
EnvironmentSession
      │
      ├──────────── AgentSession A
      │                  │
      │                  ├── AgentTurn #1
      │                  │      │
      │                  │      ├── AgentStep #1
      │                  │      │      └── Action #1
      │                  │      │
      │                  │      └── AgentStep #2  [future]
      │                  │             └── Action #2
      │                  │
      │                  └── AgentTurn #2
      │
      └──────────── AgentSession B
```

这些概念 MUST NOT 共用同一个生命周期语义。

------

# 8. EnvironmentSession

EnvironmentSession 表示：

> **一个当前在线的游戏 Environment 连接实例。**

当前 gRPC transport 下：

```text
one Connect stream
≈
one EnvironmentSession
```

当前生命周期：

```text
Connect
    ↓
AdapterHello
    ↓
EnvironmentReady
    ↓
Capability Bootstrap
    ↓
Online
    ↓
Disconnect
```

EnvironmentSession 负责承载：

```text
connection identity
game identity
stream lifecycle
capability bootstrap
message correlation
pending Observation
pending Action
disconnect cleanup
```

Phase2 当前 connection context 已经包含：

```text
game_id
session_id
```

以及与当前 Turn 关联的其他 Environment metadata。

EnvironmentSession 属于：

```text
Environment / Gateway lifecycle
```

而不是长期 Agent identity。

重新连接后 EnvironmentSession MAY 被替换。

------

# 9. AgentSession

AgentSession 表示：

> **某个游戏实体对应的长期 Agent 身份与状态边界。**

Phase1 / Phase2 尚未正式实现 AgentSession persistence。

但从 v0.3 起，语义固定。

AgentSession MUST：

```text
跨多个 AgentTurn 存在。

原则上能够跨 EnvironmentSession 重连继续识别同一个 Agent。

绑定稳定的游戏世界实体身份，而不是网络连接身份。
```

未来 AgentSession 的稳定 identity SHOULD 能由类似：

```text
game scope
+
world scope
+
stable entity identity
```

的信息解析。

具体：

```text
key 格式
编码方式
数据库主键
UUID 方案
```

不在 v0.3 冻结。

未来 AgentSession 可以拥有：

```text
Profile
Recent Turns
Working Memory
Episodic Memory
Goals
Persistent Agent State
```

必须长期保持：

```text
EnvironmentSession
!=
AgentSession
```

## 9.1 Agent Binding 与 Definition / Instance 分离

AgentSession identity 绑定的是具体游戏实体，不绑定 Agent Definition。

长期必须区分：

```text
Game Entity
    ↓
Agent Binding
    ↓
Agent Instance
    ├── Agent Definition / Archetype
    ├── Agent Instance Descriptor
    ├── Agent State
    └── Agent Memory
```

`entity_id` 表示当前 `world_id` 命名空间内稳定的游戏实体身份。

它用于：

```text
AgentSessionKey
Agent State scope
Agent Memory scope
Event routing
Trace diagnostics
```

它 MUST NOT 被长期解释为 Agent Definition 的天然 key。

Agent Definition / Archetype 的 scope 是：

```text
game_id + definition_id
```

Agent Instance Descriptor / State / Memory 的 scope 是：

```text
game_id + world_id + entity_id
```

Stardew 可以采用：

```text
entity_id     = npc:Linus
definition_id = npc:Linus
```

但这只是固定角色游戏的特例。

动态实体游戏可以采用：

```text
entity_id     = villager:uuid-123
definition_id = villager/farmer

entity_id     = pawn:723491
definition_id = human_pawn
```

Agent Binding 负责回答：

```text
这个 Game Entity 是否应该成为 Agent？
如果是，它使用哪个 definition_id？
它有哪些 instance descriptor facts？
```

Runtime MAY 未来以独立 Resolver / Store 实现 Agent Binding。Phase5 Implementation Baseline 只创建最小 AgentBinding / AgentDefinition 概念，并使用 `EntityRef.definition_id` 作为 `definition_id` 的唯一协议承载。

Adapter SHOULD 提供事实，例如 `entity_id`、`EntityRef.definition_id`、`display_name`、traits、attributes 和当前状态。

Adapter MUST NOT 通过 Agent Binding 直接接管完整 Agent prompt；Runtime / Context Sources 负责根据 Definition、Descriptor、State、Memory、Observation 和 Tools 组合模型上下文。

------

# 10. AgentTurn

AgentTurn 是 Runtime 的核心执行单位。

定义：

> **一个有效 Trigger 唤醒 Agent 后，从开始执行到进入唯一明确终态的一次 Agent 执行。**

Phase2 已经实际采用：

```text
turn_id
trace_id
turn_started
turn_completed
turn_failed
```

因此本文档统一使用：

```text
AgentTurn
```

作为长期术语。

当前 Phase2：

```text
1 AgentTurn
=
1 Observe
+
1 Model Generate
+
1 Tool / Action
```

未来 Multi-step：

```text
1 AgentTurn
=
N AgentSteps
```

无论内部执行多少次：

```text
Model
→ Tool
→ Result
```

只要属于同一次 Trigger 后的连续执行，就仍然属于同一个 AgentTurn。

------

# 11. AgentStep

AgentStep 表示：

> **一个 AgentTurn 内的一次 ModelDecision 推进。**

Phase5 的 AgentStep 形态是：

```text
AgentStep = 1 ModelDecision
          + 0..N ToolCalls
          + 0..N ToolResults
          + optional AgentTurn Control
```

AgentStep 是 Runtime 内部概念，不进入 Environment Protocol。Phase5 不要求创建：

```text
StepStore
AgentStep protocol message
独立 step state machine package
```

Phase5 AgentLoop MUST 维护：

```text
step_index
tool_call_id per ToolCall
intra-turn ToolResult transcript
bounded step budget
```

必须保持：

```text
AgentStep belongs to AgentTurn.
```

------

# 12. Action

Action 表示：

> **Runtime 请求 Environment 执行的一次具有独立业务身份和生命周期的副作用操作。**

Action MUST 使用：

```text
action_id
```

作为业务关联身份。

它与：

```text
message_id
turn_id
```

不是同一个概念。

架构必须允许 Action 具有：

```text
submitted
accepted / running
terminal result
```

等生命周期状态。

具体状态枚举由 Protocol 定义。

当前已验证的同步 / 短时 Action：

```text
emote
present_dialogue
face_player
```

属于短时 Action。

当前已验证的异步 Action：

```text
move_to
```

Runtime 当前支持短时 Action 的 bounded wait：

```text
SubmitAction
    ↓
bounded wait
    ↓
ActionResult
```

Runtime 当前支持 `move_to` 的异步 suspend / resume：

```text
ActionRequest
    ↓
accepted / running
    ↓
AgentTurn suspended
    ↓
terminal ActionResult
    ↓
re-observe
    ↓
resume AgentTurn
```

但 Architecture MUST NOT 假设：

```text
Action = synchronous function
```

未来更多长 Action，例如：

```text
follow
wait
long interaction
```

## 12.1 ActionRequest Source、ActionResult 与 TurnCompletion

`ActionRequest` 是 Runtime 请求 Adapter 执行 Action 的协议消息。

当 Action 来源于 accepted GameEvent 对应的 AgentTurn 时，Runtime MUST 写入：

```text
source_event_id
source_turn_id
```

`source_event_id` 来自原 GameEvent.event_id，是 Adapter 查找 interaction context snapshot 的协议主键。`source_turn_id` 来自 Runtime turn_id，只作为诊断与 trace 对齐字段。

Adapter MAY 使用 `ActionRequest.source_event_id` 在 effect time 校验玩家、NPC、world、location、conversation 或距离是否仍符合该 Action 的原始交互上下文。

模型 MUST NOT 生成或修改：

```text
source_event_id
source_turn_id
conversation_id
```

`ActionResult` 是 Action 的终态事实：

```text
action_id
status
output
error
```

它不表示整个 AgentTurn 已经结束。

需要让 Adapter 感知 Turn 终态时，Runtime MUST 使用独立的 Turn-level signal，例如：

```text
TurnCompletion
    turn_id
    event_id
    world_id
    entity_id
    completion status
```

TurnCompletion 表示：

```text
accepted GameEvent 对应的 AgentTurn 已经进入终态。
```

Adapter 使用 TurnCompletion.event_id 关联并释放已接受 GameEvent 的本地上下文；TurnCompletion.turn_id 只作为 Runtime Turn 的诊断投影。

TurnCompletion MUST NOT 承载：

```text
AgentStep
model transcript
ToolResult transcript
Memory payload
具体游戏 UI 规则
```

Adapter MAY 使用 TurnCompletion 释放本地 interaction context、pending lock 或 UI 等待态。

v0.7 冻结这些能力要求。

不冻结：

```text
continuation 如何持久化
是否阻塞 goroutine
是否使用 workflow engine
resume 如何调度
```

这些属于未来阶段实现设计。

------

# 13. GameEvent 与 Observation

必须长期保持：

```text
GameEvent
=
发生了什么
```

而：

```text
Observation
=
现在是什么状态
```

例如：

```text
player_interacted_with_npc
```

属于 Event。

```text
当前地点
天气
游戏时间
附近 Entity
当前关系状态
```

属于 Observation。

以下内容不应该因为模型需要就不断塞入 Observation：

```text
历史事件
过去对话
Recent Turns
长期 Memory
Goal history
```

这些未来属于：

```text
Agent Context
Recent Events
Working Memory
Persistent Memory
```

推荐长期模型输入：

```text
Triggering Event
+
Current Observation
+
Agent Context
+
Available Tools
```

Observation MUST 保持 narrow waist：

```text
small common envelope
+
game-specific structured state / extensions
```

Runtime Core 不应该因为某个游戏需要就持续把以下字段变成跨游戏 protocol 字段：

```text
season
weather
friendship
biome
hunger
mood
job
block
mana
```

这些游戏事实应由 Adapter 通过 `state` / `extensions` 表达，并由 Context projection 在需要时选择性使用。

------

# 14. Event Ingress 与 Trigger Control

当前 Phase2 可以继续采用：

```text
GameEvent
    ↓
AgentLoop.HandleEvent
    ↓
event filtering
```

但长期 Architecture MUST 分离以下责任：

```text
GameEvent
    ↓
Environment Gateway
    ↓
Trigger Admission / Routing
    ↓
AgentSession Resolution
    ↓
Turn Scheduling
    ↓
AgentTurn
```

## Environment Gateway

回答：

> Environment 发来了什么消息？

## Trigger Control

回答：

> 这个已经发生的事实是否应该唤醒 Agent？

未来 MAY 形成：

```text
START
QUEUE
COALESCE
DROP
```

等 Runtime 内部决策。

## Turn Scheduling

回答：

> 当前 AgentSession 是否已经存在 Active Turn，以及新 Turn 什么时候运行？

这些都属于 Runtime 内部控制语义。

MUST NOT 因此污染 Environment Protocol。

------

# 15. EventAck 与 TriggerDecision 分离

Protocol EventAck 与 Runtime TriggerDecision MUST 保持概念分离。

架构层只规定：

```text
EventAck
=
Environment Protocol 对 GameEvent 接收 / 接纳状态的表达。
```

其具体：

```text
durability
retry
idempotency
replay
```

语义由当前 Protocol Specification 负责定义。

Runtime 的：

```text
START
QUEUE
COALESCE
DROP
```

属于：

```text
TriggerDecision
```

不得直接等价为 EventAck 状态。

因此：

```text
事件已被 Runtime 接纳
```

与：

```text
该事件最终是否启动 AgentTurn
```

是两件不同的事情。

------

# 16. Environment Gateway

当前模块：

```text
runtime/internal/gateway
```

是 Runtime 与在线 Environment 之间的边界。

Gateway 负责：

```text
gRPC Connect
EnvironmentSession lifecycle
AdapterHello / EnvironmentReady
Capability bootstrap
stream receive / send
message dispatch
correlation
pending Observation
pending ActionResult
disconnect cleanup
event ingress
action egress
```

Gateway MUST NOT 负责：

```text
Prompt
Memory Retrieval
Model Decision
Persona
Tool Selection
Agent Planning
```

Agent Core MUST NOT：

```text
直接操作 gRPC stream
```

------

# 17. Environment Port

AgentTurn 操作 Environment 必须通过抽象 Port。

Phase1 / Phase2 已经采用类似：

```go
type Environment interface {
    Observe(...)
    SubmitAction(...)
}
```

Gateway 的 connection-scoped environment 实现该 Port。

依赖方向：

```text
Agent Core
    ↓
Environment Port

Gateway implementation
    ↑
implements
```

禁止：

```text
Agent Core
    ↓
gateway.Server
```

v0.3 不要求为了架构形式提前创建：

```text
runtime/internal/environment
```

独立 package。

当未来出现多个真实 Environment 实现，例如：

```text
gRPC Environment
MiniWorld In-Process Environment
Replay Environment
```

且独立 package 能真实降低耦合时，再 SHOULD 抽出。

原则：

> **需求产生抽象，而不是架构图产生抽象。**

------

# 18. Capability、Policy 与 Tool

必须长期区分：

```text
Capability
Policy / Permission
Tool
```

## Capability

表示：

> Environment 技术上能够做什么。

由 Adapter 声明。

## Policy / Permission

表示：

> Runtime 当前允许 Agent 使用什么。

由 Runtime 决定。

## Tool

表示：

> 本次 Model 最终能看到和调用什么。

由 Runtime 构造。

Available Tools 是当前 AgentTurn 的动态 capability view。

它可能由以下因素共同决定：

```text
Environment capabilities
Entity capabilities
Current Observation / State
Runtime Policy / Permission
Turn Policy
```

它不等同于某个 Environment 或 entity type 的永久固定工具列表。

长期链路：

```text
Adapter Capability
        ↓
Capability Registry
        ↓
Runtime Policy
        ↓
Tool Registry
        ↓
Model ToolDefinition
```

当前 MVP0 已实现 capability metadata 驱动的最小 Tool Policy；尚未实现独立 Policy subsystem。

当前 MVP 合法策略：

```text
受信任的第一方 Adapter
+
基础解析通过
    ↓
Capability 1:1 暴露为 Tool
```

这只是当前最小 policy。

不代表未来 Runtime 必须永久无条件暴露所有 Capability。

------

# 19. Capability 语义权威

对于 Environment Capability：

> **谁执行，谁定义 capability schema 和游戏侧语义。**

Adapter 是以下信息的业务事实来源：

```text
Capability name
description
input schema
game-side parameter semantics
execution implementation
game-side failure
```

`Capability.description` 是 model-facing 自然语言说明。它可以包含具体游戏工具的用途、参数解释和游戏侧效果。

Runtime-facing 执行策略必须使用结构化 metadata，不从 description 解析：

```text
Capability.extensions.gameagent.tool_policy
```

当前已确定的最小 policy 词表：

```text
exclusive_per_step
    该 tool call 必须单独占据当前 AgentStep。

settle_after_success
    该 tool call 成功后，当前 Turn 应收敛到 settle。
```

后续玩家输入或环境进展由新的 GameEvent 驱动这一事实，属于 capability description 与 event contract，不作为 Runtime-facing policy。

Runtime MUST NOT 写死：

```text
Stardew emote enum
Stardew speak 参数规则
NPC.doEmote mapping
Stardew movement semantics
present_dialogue 特殊执行语义
任意 game-specific capability name 对应的 Runtime policy
```

不同游戏可以用不同 capability name 表达同类能力。Runtime 只能通过 `execution_mode`、`concurrency_mode` 和 `extensions.gameagent.tool_policy` 执行通用调度规则。

Environment Tool 的通用链路：

```text
Adapter Capability
    ↓
ToolRegistry
    ↓
model.ToolDefinition
    ↓
Model
    ↓
ModelDecision
    ↓
ordered ToolCall batch
    ↓
Tool Scheduler
    ↓
ActionRequest(s)
    ↓
Adapter
    ↓
ToolResult transcript
```

新增一个简单 Environment Tool SHOULD NOT 要求修改：

```text
AgentLoop
Gateway main flow
Provider-neutral model contract
```

------

# 20. Tool Validation Boundary

Runtime 通用层 SHOULD 负责：

```text
Capability envelope 可解析
Capability name 合法
Tool 已注册
ToolCall ID 在当前 AgentStep / Turn 内唯一
ToolCall arguments 非 nil
arguments 可被 Runtime / Protocol 动态结构承载
ToolCall arguments 在 Protocol 边界转换为 google.protobuf.Struct
```

同一 AgentStep 内重复 ToolCall.ID 属于模型响应边界错误，由 AgentLoop 在写入 Transcript 和调用 Scheduler 前拒绝；Scheduler 的 batch duplicate preflight 是执行层内部保护。

Runtime 通用 Environment Tool 层 SHOULD NOT 负责：

```text
具体游戏 enum 业务校验
具体动作当前是否能执行
具体游戏字段业务语义
```

这些由 Adapter 在执行时判断。

游戏侧业务失败通过：

```text
ActionResult
```

表达。

Provider 为适配具体模型 API 所做的：

```text
schema copy normalization
provider-specific mapping
strict schema transformation
```

属于：

```text
Provider compatibility
```

不意味着 Runtime 取得 Capability 游戏语义权威。

------

# 21. Tool Runtime

Tool Runtime 负责：

```text
Capability registration
Tool Registry
Tool exposure
ToolCall lookup
ToolCall envelope validation
Tool execution routing
Environment Tool → ActionRequest mapping
```

长期 Tool 分为：

```text
Tool
├── Environment Tool
└── Runtime Tool
```

当前 Stardew 生产 Tool View 已实际暴露：

```text
Environment Tool
    present_dialogue
    emote
    face_player
    move_to
```

`speak` 可作为 Adapter 侧 legacy / special capability code 保留，但不属于当前 Stardew 生产 Tool View。

其中当前已实际验证的异步 Environment Tool：

```text
move_to
```

未来 Runtime Tool MAY 包括：

```text
memory_search
memory_write
goal_create
goal_update
```

Runtime Tool MUST NOT 发送给 Adapter。

Environment Tool MUST NOT 直接修改 Runtime Memory。

------

# 22. Model Runtime

Agent Core MUST 通过 provider-neutral contract 使用模型。

当前核心接口已经验证：

```go
type Provider interface {
    Generate(
        ctx context.Context,
        req Request,
    ) (Response, error)
}
```

模块关系：

```text
agent
    ↓
model.Provider
    ↑
llm/fake
llm/openai
llm/deepseek
```

`runtime/internal/model` 负责：

```text
Request
Message
ToolDefinition
ToolCall
Response
Provider interface
```

`model` MUST NOT：

```text
依赖厂商 SDK
依赖 Provider-specific response shape
依赖 Stardew
构造游戏 ActionRequest
```

`runtime/internal/llm/<provider>` 负责：

```text
Provider request mapping
Provider tool schema adaptation
HTTP / API invocation
Response parsing
Provider-specific error handling
```

Provider MUST NOT：

```text
选择游戏 Entity
访问 Adapter
提交 Action
修改 Tool Registry
保存 Agent Memory
```

------

# 23. Agent Turn Core

当前：

```text
runtime/internal/agent
```

负责 AgentTurn 的核心编排。

Phase2 当前流程：

```text
Trigger accepted
    ↓
create turn_id / trace_id
    ↓
Observe
    ↓
Build Model Request
    ↓
Provider.Generate
    ↓
Validate ToolCall
    ↓
Build ActionRequest
    ↓
SubmitAction
    ↓
ActionResult
    ↓
turn_completed / turn_failed
```

Agent Core MUST NOT：

```text
操作 gRPC stream
构造 Provider-specific JSON
读取具体游戏对象
执行 Stardew API
理解具体 Capability 游戏实现
```

未来加入 Memory / Multi-step 时，应扩展 AgentTurn，而不是破坏：

```text
Environment
Model
Tool
Adapter
```

之间的边界。

------

# 24. Context 与 Prompt

Phase4 已经引入最小 `runtime/internal/context`，用于组装 Runtime Policy、Recent Memory、Current Event、Current Observation 和 Tools。

长期 Context 如果继续包含：

```text
Game Definition
Agent Definition / Archetype
Agent Instance Descriptor
Observation
Trigger Event
Recent Turns
Memory
Goals
Tool Results
Context Budget
Compression
```

Context 组合职责 SHOULD 继续保持在 Context boundary，而不是回流到 AgentTurn 主控制流。

长期 Context MAY 分为：

```text
Stable Context
Semi-Stable Context
Volatile Context
```

并至少遵守：

```text
Agent Definition / Archetype scope = game_id + definition_id
Agent Instance Descriptor scope = game_id + world_id + entity_id
Agent Memory scope = game_id + world_id + entity_id
Current Observation > historical Memory
```

------

# 25. Agent State 与 Memory

Memory MUST 属于 Runtime。

Adapter MUST NOT：

```text
保存 Agent Memory
执行 Memory Retrieval
根据 Memory 做 Agent Decision
```

未来 Memory 基本关系：

```text
AgentSession
    ↓
MemoryStore
    ↓
Relevant Memory
    ↓
Agent Context
```

v0.3 只冻结：

```text
Memory belongs to Agent State.

Memory should bind to stable AgentSession identity.

Memory does not belong to EnvironmentSession.

Memory does not belong to Agent Definition / Archetype.
```

因此多个动态实体可以共享同一个 `definition_id`，但它们的 Memory MUST 仍按 `game_id + world_id + entity_id` 隔离。

不冻结：

```text
数据库
Vector DB
Embedding Provider
Memory ranking algorithm
Reflection
Summary strategy
```

------

# 26. Trace / Observability

Phase2 已验证的 Trace 原则从 v0.3 起成为架构约束。

## TurnTracer 与 Recorder 分离

```text
TurnTracer
=
AgentTurn lifecycle observability semantics

Recorder
=
Observability output implementation
```

## Trace 是 Observer

Trace MAY：

```text
观察
记录
输出
统计
```

Trace MUST NOT：

```text
改变 ToolCall
阻止 Action
决定 Turn 是否成功
参与 Agent 状态一致性
```

## Observer 不得对主链路形成背压

Recorder SHOULD：

```text
non-blocking
best-effort
```

Recorder failure MUST NOT 改变：

```text
AgentTurn result
```

## Trace data 不是 Source of Truth

当前 JSONL trace 属于：

```text
derived observability data
```

不得依赖它恢复：

```text
Agent state
Memory
真实 Action 状态
Game state
```

------

# 27. Observer 与 Functional Policy 分离

未来以下能力：

```text
Permission
Safety Policy
Rate Limit
Tool Preflight
Context Mutation
```

可能改变 Agent 行为。

它们属于：

```text
Functional Policy / Hook
```

而不是 Trace Observer。

必须保持：

```text
Observer
    observes

Policy / Hook
    may affect execution
```

不得让 Trace Recorder 为了方便承担：

```text
Allow / Deny
BeforeTool mutation
Prompt mutation
```

------

# 28. Timeout 与 Failure Boundary

每个已经创建的 AgentTurn MUST 最终：

```text
complete
or
fail
```

不得因为 Environment / Provider 永久无响应而无限等待。

当前 Phase2 已采用：

```text
turn timeout
observe timeout
model timeout
action timeout
```

原则：

```text
turn timeout
=
global hard bound

stage timeout
=
local wait bound
```

阶段 timeout context SHOULD 从：

```text
turn context
```

派生。

对于已经创建 Turn 的失败：

```text
turn_failed
```

SHOULD 至少包含：

```text
stage
reason
```

技术错误与业务失败 SHOULD 保持区分。

具体 failure taxonomy MAY 随 Runtime 实现演进，但不得破坏：

```text
一个 Turn 只有唯一 terminal result
```

这一原则。

------

# 29. Action Timeout 与 Cancellation

Action 与 Observe / Model 不同，因为它具有 Environment side effect。

如果：

```text
ActionRequest 已经发送
```

之后 Runtime：

```text
action timeout
```

则 Runtime SHOULD：

```text
停止等待
+
best-effort request cancellation
```

当前 Phase2 使用：

```text
CancelActionRequest
```

Cancel 的定义：

> **如果 Action 尚未真正执行，请尽量不要再执行。**

Cancel MUST NOT 被理解为：

```text
transaction rollback
```

如果 Action 已经执行：

```text
Runtime 不保证回滚。
```

因此 Runtime 必须允许：

```text
AgentTurn = failed(action_timeout)
```

而真实游戏动作实际上已经发生。

这是 best-effort cancellation 的正常残留语义。

------

# 30. Game Adapter 架构

Game Adapter 是：

> **具体游戏 Environment 与 GameAgent Protocol 之间的 Driver / Translation Layer。**

Adapter 负责：

```text
Game lifecycle integration
Event collection
Entity / Protocol mapping
Observation building
Capability declaration
Action execution
Game thread constraints
Runtime transport
```

Adapter MUST NOT：

```text
调用 LLM
决定 Agent 应该说什么
保存 Agent Memory
执行 Agent Planning
选择 Model Tool
实现 Runtime cognition
```

------

# 31. Stardew Adapter 当前边界

Phase6.5 后 Stardew Adapter 已形成：

```text
RuntimeClient
    Runtime connection / stream / send / recv

CapabilityCatalog
    capability declaration

ProtocolMapper
    Stardew object ↔ protocol identity

ObservationBuilder
    game state → Observation

PlayerInteractProbe
    game interaction → GameEvent

PresentDialogueCapability
    dialogue UI execution

FacePlayerCapability
    turn-to-player execution

MoveToCapability
    async pathfinding execution

EmoteCapability
    emote execution

MainThreadDispatcher
    SMAPI main-thread boundary

ActionCancellationRegistry
    pre-execution cancellation state
```

ProtocolMapper SHOULD 继续作为：

```text
Entity identity mapping
Protocol conversion
```

的集中位置。

不得在多个 capability / probe 中复制：

```text
NPC ↔ entity_id
```

规则。

------

# 32. Adapter Capability 原则

新增 Environment Capability SHOULD 尽量满足：

```text
输入结构明确
执行语义明确
效果可观察
失败可表达
副作用边界清楚
```

新增简单 Capability SHOULD 主要修改：

```text
Adapter Capability declaration
Adapter Action implementation
```

而不应要求 Runtime 新增：

```text
if tool == "stardew_xxx"
```

之类专用逻辑。

复杂长 Action MAY 推动通用 Action Runtime 演进，但 Runtime 仍然 MUST NOT 理解具体游戏执行方式。

------

# 33. Configuration Boundary

配置分为三类。

## Model Provider Config

负责：

```text
如何连接模型。
```

例如：

```text
provider
model
base URL
API key reference
provider compatibility settings
```

## Agent Runtime Config

负责：

```text
一个 AgentTurn 如何运行。
```

例如：

```text
turn timeout
observe timeout
model timeout
action timeout
model behavior hints
language / style hints
tool-use hints
```

## Adapter Config

负责：

```text
具体游戏 Adapter 如何运行。
```

例如：

```text
Runtime endpoint
Adapter log level
game-specific debug probes
game-specific switches
```

必须保持：

```text
Adapter config
    不变成 Runtime cognition contract。

Provider secret
    不进入 Environment Protocol。

Game-specific config
    不成为 Runtime 通用 schema。
```

------

# 34. 当前 Runtime Minimal Core

Phase2 Accepted 后，已经有真实调用方的核心模块：

```text
runtime/

cmd/server
internal/gateway
internal/agent
internal/model
internal/llm
internal/tool
internal/trace
internal/idgen
```

职责：

```text
cmd/server
    Composition Root
    配置
    dependency wiring
    process lifecycle

gateway
    EnvironmentSession
    gRPC
    correlation
    pending operations

agent
    AgentTurn orchestration
    prompt / model request construction

model
    provider-neutral model contract

llm
    provider adapters

tool
    Capability → Tool
    ToolCall validation
    Action mapping

trace
    AgentTurn observability

idgen
    Runtime local ID generation
```

这是 v0.7 当前实际 Minimal Core。

------

# 35. Future Logical Extensions

未来明确存在以下逻辑扩展位：

```text
Agent Session
Memory
Trigger / Turn Scheduling
Policy / Permission
Context Engine
Long-running Action continuation
Evaluation
```

可能最终形成类似：

```text
session/
memory/
trigger/
policy/
contextengine/
action/
eval/
```

但这些只是：

```text
logical architecture boundaries
```

不是当前必须创建的 package。

规则：

> **没有真实调用方时，不创建空 package、空 framework 或大规模抽象。**

------

# 36. Minimal Core

GameAgent MUST 保持：

```text
Small Core
+
Capabilities at edges
```

不应该因为增加能力演变成：

```text
DialogueAgent
MemoryAgent
BehaviorAgent
PlannerAgent
SupervisorAgent
...
```

大量 Agent 类型互相调用的基础框架。

长期优先：

```text
One general Agent Runtime
+
Many AgentSessions
+
Peripheral capabilities
```

Memory、Goal、Policy、Provider、Trace 都应保持明确职责，而不是通过不断增加 Agent 类型解决。

------

# 37. Protocol Boundary

GameAgent Protocol 描述：

```text
Environment
Entity
GameEvent
Observation
Capability
Action
Environment Session communication
```

以下内容 MUST NOT 进入 Environment Protocol：

```text
Prompt
LLM Provider
Agent Memory
Persona implementation
Planner
ModelDecision
Model ToolCall raw response
ToolResult transcript
AgentStep internal state
Runtime Policy internals
settle sentinel
Trace Recorder internals
```

Phase5 接受的通用 Environment Protocol additive 字段：

```text
EntityRef.definition_id
Capability.concurrency_mode
```

Phase5 不把以下内容加入 Environment Protocol：

```text
Observation.definition_id
target_definition_id
ActionBatchRequest / ActionBatchResult
```

新增 Protocol 概念必须回答：

> **它是否是 Runtime 与多个游戏 Environment 都需要理解的通用 contract？**

如果只对某一个具体游戏有意义，应优先留在：

```text
Adapter
dynamic payload
extensions
```

中。

------

# 38. Protocol 与 Transport 分离

必须区分：

```text
GameAgent Protocol
```

与：

```text
gRPC
```

Protocol 是：

```text
Environment contract
```

gRPC 是：

```text
current transport
```

未来 MAY 支持：

```text
gRPC
in-process Environment
other transport
```

但在没有第二个真实 transport 需求之前：

```text
MUST NOT
```

为了理论纯洁提前重构当前 gRPC 实现。

------

# 39. Game-Agnostic Runtime 检验

新增一个新 Game Adapter 时，理想情况下不应该修改：

```text
AgentTurn orchestration
Model Runtime
Memory core
Trace semantics
ToolCall generic flow
Policy core
```

主要实现：

```text
Event translation
Observation translation
Entity mapping
Capability declaration
Action execution
Transport binding
```

如果 Runtime 开始大量出现：

```text
if game == "stardew"
if entity is StardewNPC
```

说明：

```text
Environment abstraction
Protocol boundary
或 Runtime boundary
```

已经发生泄漏。

------

# 40. Architecture Enforcement

核心依赖规则 SHOULD 通过自动检查保护。

至少检查：

```text
runtime/ 不依赖 adapters/

runtime/ 不 import Stardew / SMAPI

adapter 不依赖 runtime/internal/

runtime/internal/ 下不得存在仅含 .gitkeep 的空 package

protocol generated code 不手工修改

proto source 与 generated code 保持一致
```

长期 CI SHOULD 包含：

```text
architecture check
```

Architecture check 失败 SHOULD 阻止合并。

架构约束不能只靠人工记忆维持。

------

# 41. Vertical Slice First

长期开发 MUST 优先：

```text
Real Event
    ↓
Runtime
    ↓
Model / Fake Model
    ↓
Tool
    ↓
Protocol Action
    ↓
Real / Equivalent Environment
```

而不是：

```text
先实现全部 Future Architecture
再尝试第一次真实运行
```

复杂能力 SHOULD 分成：

```text
minimal happy path
    ↓
failure semantics
    ↓
automated tests
    ↓
real-game / equivalent smoke test
```

------

# 42. Interface First, Framework Later

真正的跨层 boundary MAY 提前定义小接口，例如：

```text
Environment Port
Model Provider
Memory Store
Policy
```

但 MUST NOT 因“以后可能需要”提前建设：

```text
Plugin Framework
Workflow Engine
generic EventBus
large Hook Framework
Service Locator
Abstract Tool Factory
Distributed Scheduler
CQRS
Event Sourcing
```

除非已经存在明确真实需求。

------

# 43. Architecture Classification

较大的开发任务开始前 SHOULD 明确：

```text
Feature:

Layer:
Runtime / Protocol / Adapter / Game

Modules affected:

Protocol changes:
Yes / No

New Capability:
Yes / No

New Tool:
Environment Tool / Runtime Tool / None

Agent State impact:

Action lifecycle impact:

Dependency impact:
```

目的不是增加文档负担，而是开发前先回答：

> **这个能力究竟属于哪一层？**

------

# 44. Architecture Definition of Done

涉及架构边界的开发完成前 SHOULD 检查：

```text
[ ] Runtime 没有新增具体游戏 API 依赖。

[ ] Adapter 没有新增 Agent cognition。

[ ] Protocol 没有新增 Runtime-internal 概念。

[ ] 新 Capability 的游戏语义由执行它的 Adapter 定义。

[ ] 新 Tool 已明确属于 Environment Tool 或 Runtime Tool。

[ ] Agent Core 没有新增 provider-specific API shape。

[ ] Action side effect 有明确 success / failure semantics。

[ ] AgentTurn 最终能够 completed 或 failed。

[ ] Observer 不改变 Agent 主流程。

[ ] 改变 Agent 行为的能力没有伪装成 Trace Observer。

[ ] 核心 contract 有自动测试。

[ ] 有真实或等价 vertical slice 验证。

[ ] 没有为了当前功能提前构建无真实调用方的大型抽象。
```

------

# 45. 当前已验证的架构事实

截至 Phase6.5 Accepted：

```text
1. Stardew Adapter 可以通过 gRPC 连接 Go Runtime。

2. GameEvent 可以进入 Runtime AgentTurn。

3. Runtime 可以通过 Environment Port 请求 Observation。

4. Agent Core 不依赖 Stardew / SMAPI 类型。

5. Fake / OpenAI / DeepSeek 可以共享统一 Provider contract。

6. 真实 DeepSeek 可以基于结构化 Observation 和 Tools 返回 ToolCall。

7. Capability schema 可以由 Adapter 动态注册成 ToolDefinition。

8. speak → emote 不要求 Runtime 增加 tool-specific 主链路。

9. ToolCall 可以通用转换为 ActionRequest。

10. ActionResult 可以解除当前 Runtime Action wait。

11. AgentTurn 可以拥有 turn_id / trace_id。

12. TurnTracer 可以保证唯一 terminal emission。

13. Trace Recorder 可以作为非阻塞 best-effort Observer。

14. Observe / Model / Action 都可以设置 bounded timeout。

15. Action timeout 后 Runtime 可以发送 best-effort CancelActionRequest。

16. Adapter 可以在游戏主线程真正执行 Runtime 返回的 Action。

17. Adapter 可以在执行前消费 cancel marker，并跳过已经取消的 Action。

18. EntityRef.definition_id 可以作为 Agent Definition 绑定 key。

19. Capability.concurrency_mode 可以表达 sequential / parallel_safe 执行承诺。

20. AgentTurn 可以包含多个有界 AgentStep。

21. ToolResult transcript 可以在同一 Turn 内回灌给模型。

22. Stardew Adapter 可以通过 Observation.state.stardew 提供 namespaced 当前事实。

23. Stardew Adapter 可以通过 conversation_id、present_dialogue、player_said_to_npc 和 face_player 提供正式对话交互面。

24. Runtime Tool Policy 可以来自 Capability.extensions.gameagent.tool_policy。

25. ActionRequest 可以携带 source_event_id / source_turn_id，将 Action 绑定回触发它的 GameEvent 和 AgentTurn。

26. ActionResult 不再等价于 AgentTurn terminal signal。

27. Runtime 可以通过 TurnCompletion 向 Adapter 投影 accepted GameEvent 的 Turn 终态。

28. AgentTurn 可以 suspend / resume 异步 Action，并在 terminal ActionResult 后重新 Observation。

29. Stardew Adapter 可以用 move_to 验证异步 ActionStatusUpdate / ActionResult 生命周期。

30. Adapter 可以用 InteractionContextStore 绑定 source-time interaction facts，并在 effect time 校验 world、entity、conversation、location 与必要距离。

31. Stardew 生产 Tool View 可以收敛为 present_dialogue / emote / face_player / move_to。

32. Stardew present_dialogue 可以承载 NPC 台词、回复选项、free text 输入和单句结束语义。

33. Stardew source-time gate 与 in-flight gate 可以防止重复点击导致的重复 GameEvent、乱序回复或叠 UI。

34. Stardew waiting menu 属于 Adapter UX；它在 Runtime 返回 ActionRequest 前锁住玩家输入，并在执行 capability 前关闭，不进入 Runtime / Protocol 契约。
```

这些构成 v0.7 的实际证据基础。

------

# 46. 已确定但仍需继续完善的 Architecture Contracts

以下能力已有边界定义，但在 Phase7+ 仍需继续产品化、持久化或泛化：

```text
EnvironmentSession != AgentSession

AgentSession 是长期 Agent identity / state boundary

Event Ingress 与 TriggerDecision 应逻辑分离

TriggerDecision 与 EventAck 应逻辑分离

Runtime 必须允许 Policy 位于 Capability 与 Tool exposure 之间

Runtime tool policy 必须继续来自结构化 capability metadata，不来自 game-specific capability name

Memory 必须绑定 AgentSession，而不是 EnvironmentSession

Entity identity 不等于 Agent Definition

Agent Definition / Archetype 使用 game_id + definition_id

definition_id 的协议来源是 EntityRef.definition_id

Agent Instance Descriptor / State / Memory 使用 game_id + world_id + entity_id

Observation 必须保持 narrow waist，不因单个游戏字段膨胀 Protocol

Available Tools 是当前 AgentTurn 的 dynamic capability view

Environment Capability 可以声明 concurrency_mode

session-scoped capability registry 属于 Phase7+ 范围

Runtime restart recovery 与 persistent continuation 属于 Phase7+ 范围
```

这些属于：

```text
Architecture Contract
```

而不是：

```text
Current Feature
```

------

# 47. v0.7 明确不冻结的内容

以下设计必须等对应 Phase 有真实需求后再确定：

```text
AgentSession 最终 key 格式

AgentSession store 类型

Memory 数据模型

Memory retrieval 算法

Trigger Router 具体实现

QUEUE / DROP / COALESCE 策略

同一 AgentSession 的并发锁实现

AgentStep 具体状态机

Multi-step 最大步数

Async Action continuation 实现

Suspend / Resume persistence

Reconnect protocol

Event Replay

Action Recovery

Resume Token

Long-running Action scheduler

Permission policy language

Context compression

Database

Vector Store

Trace backend

Evaluation framework
```

这些内容不得因为 Architecture v0.7 存在就被认为已经设计完成。

------

# 48. 架构演进原则

长期演进 SHOULD 总体遵循：

```text
Stable Environment Contract
        ↓
Stable AgentTurn Runtime
        ↓
Stable Agent Identity / AgentSession
        ↓
Context / Memory
        ↓
Multi-step AgentTurn
        ↓
Long-running Async Action
        ↓
Environment Recovery / Reconnect
        ↓
Broader Adapter Capability
        ↓
Productization / Evaluation
```

具体 Phase 编号可以调整。

Architecture Baseline 不绑定阶段编号。

------

# 49. 一句话架构定义

> **GameAgent 是一个 game-native Agent Runtime / Harness：它通过统一 Environment Protocol 接收实时游戏事件和 Observation，将 Environment 声明的 Capability 转换为模型可调用的 Tool，在独立 AgentTurn 中完成模型决策与 Action 编排，并始终把具体游戏状态读取与动作执行保留在 Game Adapter / Game 一侧。**

------

# 50. 最终记忆模型

```text
Event
=
发生了什么

Observation
=
现在是什么状态

Capability
=
Environment 能做什么

Policy
=
Runtime 允许 Agent 做什么

Tool
=
Model 当前可以调用什么

Action
=
请求 Environment 执行什么

EnvironmentSession
=
当前连接的是哪个在线游戏环境

AgentSession
=
这是谁的长期 Agent 状态

AgentTurn
=
这一次 Agent 被唤醒后完整执行了什么

AgentStep
=
一个 Turn 内的一次 Model → Tool / Result 推进
```

最终必须长期保持：

```text
Agent owns intent.

Runtime owns cognition.

Protocol owns contracts.

Adapter owns translation.

Game owns execution.
```

这组边界构成 **GameAgent Runtime Architecture v0.7 Baseline**。
