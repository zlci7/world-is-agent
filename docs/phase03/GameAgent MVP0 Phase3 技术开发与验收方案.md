# GameAgent MVP0 Phase3 技术开发与验收方案

> Status: Implementation Baseline — Ready for Development
> Date: 2026-08-20
> Scope: Agent Identity Contract + 多 NPC Adapter 泛化 + 事件路由与每 Session 串行化
> Architecture Baseline: GameAgent Runtime Architecture v0.2
> Roadmap Baseline: GameAgent 阶段规划 v0.3（Status: Roadmap Baseline）
> Protocol Baseline: [GameAgent Protocol v1alpha2 设计规范](./GameAgent%20Protocol%20v1alpha2%20设计规范.md)

------

# 1. 阶段目标

Phase1 证明了单 NPC one-turn 链路可以跑通，Phase2 把这条链路升级成了可观察、可配置、失败可收敛的最小 AgentTurn Runtime。

Phase3 的目标是把 Runtime 从"服务一个固定 NPC"升级为：

> **Runtime 不理解具体 NPC，也能稳定控制同一 Environment 中的多个实体。**

本阶段主要证明三个能力：

```text
1. AgentSession 身份稳定
   同一存档、同一实体，无论连接 session 怎么变，解析结果不变。

2. 事件可以路由到正确的 AgentSession
   多 NPC 环境下，一个 GameEvent 能解析出目标实体并路由到对应 AgentSession。

3. 同一 AgentSession 不会同时运行多个 active Turn
   同一 NPC 的连续事件被串行执行，不同 NPC 之间互不阻塞。
```

最终验收链路：

```text
玩家点击 Abigail
    ↓
Stardew Adapter 捕获交互（任意目标 NPC，不再写死 Linus）
    ↓
Adapter 发送 GameEvent（typed field: target_entity_id）
    ↓
Runtime 解析 AgentSession identity（game + world + entity）
    ↓
Runtime 路由到对应 AgentSession 并串行调度
    ↓
Runtime AgentTurn（Observe → Model → Tool → Action，与 Phase2 相同）
    ↓
游戏内 Abigail 显示模型生成台词
```

本阶段不增加 Memory、Multi-step、异步 Action 或 reconnect（见 Roadmap 阶段依赖门，这些分别属于 Phase4/5/6/7）。

------

# 2. 当前基线（代码事实）

## 2.1 Runtime 侧

Phase2 Accepted 后，Runtime 的实际形态：

```text
runtime/cmd/server
    启动入口，加载 Provider 与配置。

runtime/internal/gateway
    Server.Connect 管理一条 Adapter stream：
    EnvironmentReady → CapabilityRequest → CapabilityList → recvLoop。
    单 eventCh(16) + 单 agent goroutine 串行消费事件。
    EventAck ACCEPTED / REJECTED(event_queue_full)。

runtime/internal/agent
    Loop.HandleEvent 执行 one-turn：
    event type 过滤 → 选择目标 entity → Observe → Model → Tool → Action。
    AgentConfig 控制 timeout 与 prompt。

runtime/internal/model + llm/{fake,openai,deepseek}
    Provider 抽象与三家实现。

runtime/internal/tool
    Registry 从 Adapter Capability.input_schema_json 动态注册 ToolDefinition。
    ValidateToolCall 只校验 tool 已注册 + arguments 非 nil。

runtime/internal/trace
    TurnTracer + 非阻塞 JSONL Recorder。
    TurnContext 当前携带 game_id / save_id / session_id / event_id / event_type / entity_id；
    Phase3 将 save_id 语义收敛为 world_id。
```

与 Phase3 直接相关的三个现状：

```text
1. loop.go:56-58
   HandleEvent 硬编码 event.EventType != "player_interacted_with_npc"。

2. loop.go:61-69
   从 event.Entities 里挑选第一个 entity_type == "npc" 的实体，
   后续 Observe / ValidateToolCall / BuildActionRequest 全部使用 entityIDs[0]。

3. gateway.go:94-104
   单 eventCh + 单 goroutine：所有 NPC 的事件全局串行。
   一个 NPC 的慢 LLM 调用会阻塞另一个 NPC 的事件处理。
```

当前 Runtime 没有任何 AgentSession 概念，`ConnectionContext` 只有 `GameID / SessionID`。

## 2.2 Stardew Adapter 侧

```text
RuntimeClient
    gRPC 双向流；RequireNpc / HandleActionOnMainThread 已经按 entity_id 通用处理任意 NPC。
    currentSaveId 当前由 RefreshSaveContext 维护；Phase3 将收敛为 currentWorldId。
    WorldID 唯一允许来源是 Constants.SaveFolderName，不再使用 UniqueMultiplayerID 兜底。

ProtocolMapper
    npc:Name ↔ NPC 映射集中管理；player:local 表示玩家。
    BuildPlayerInteractedWithNpcEvent 构造事件，payload 目前只有 trigger / source；
    Phase3 将把目标实体提升到 GameEvent.target_entity_id typed 字段。

PlayerInteractProbe
    单目标：只响应 targetAgentName（AdapterConfig.TargetAgentName，默认 "Linus"）的点击。
    这是当前"单 NPC"限制的真正来源。

ObservationBuilder + ProbeObservation
    构造 agent/player 的当前事实；NearbyEntities 目前只有玩家。
```

Adapter 侧"多 NPC"只差一件事：`PlayerInteractProbe` 的点击命中从单名字改为多目标 / 任意 villager。Action 与 Observation 层已经通用。

## 2.3 测试现状

```text
runtime 单测
    agent / gateway / tool / trace / llm / idgen 均有覆盖。

runtime 集成
    gateway_integration_test.go 使用 fake adapter + fake provider + bufconn，
    已经验证单 NPC 完整 turn。

adapter
    tests/ActionCancellationRegistry.Tests 是独立小测试工程（无 SMAPI 依赖）。
    ProtocolMapper / ObservationBuilder 目前没有单测。
```

------

# 3. 为什么 Phase3 不做 Memory / Multi-step / Reconnect

## 3.1 Memory 依赖身份，身份必须先行

Phase4 Memory 有一个不可回避的前置问题：

```text
Memory 到底属于谁？
```

必须在 Phase4 之前稳定回答：

```text
同一个存档、同一个实体 → 同一个 AgentSession identity
不同存档、相同 NPC       → 不同 AgentSession identity
不同实体                 → 不同 AgentSession identity
session_id 变化          → AgentSession identity 不应变化
```

如果 Phase3 不先冻结 Agent Identity Contract，Phase4 的 MemoryStore 即使写通，也可能从一开始就绑定到错误的 key 上。因此 Roadmap 依赖门规定：

```text
进入 Phase4 前
    Agent Identity Contract 必须 Accepted。
```

## 3.2 Multi-step / Async Action 依赖 AgentTurn 的稳定性

Phase5（Multi-step）和 Phase6（异步 Action）都深改 AgentTurn Core。在进入这些改造前，AgentTurn 必须证明自己能稳定处理"多个实体、多事件、按身份隔离"的输入空间。否则 Phase5/6 的调试会同时面对两个变量：核心执行模型 + 多实体路由。

## 3.3 Reconnect 保持 Phase7

Phase1 已验证 stream 断开时 pending Observe / Action 能被解除，Phase2 保证 Turn 可以失败收敛，因此"不自动重连"不会导致现阶段 Runtime 永久卡死。

Reconnect 一旦进入范围，就会连带 EnvironmentSession replacement、Capability refresh、pending operation 处理、Event retry / idempotency 等，与 Phase7 的持久化放在一起设计更完整。

Phase3 只保证一件事：

```text
identity model supports reconnect
```

即身份模型允许未来重连后解析到同一个 AgentSession，但不在本阶段实现自动重连。

------

# 4. Phase3 范围

## 做

```text
1. P0 Mandatory Deliverable：Agent Identity Contract
   冻结 AgentSession 身份的逻辑组成与不变量，并落地 AgentSessionResolver。

2. 多 NPC Adapter 泛化
   把 Stardew Adapter 从单目标 NPC 升级为多目标 / 任意 NPC。

3. GameEvent 目标实体路由
   Protocol 增加 typed target_entity_id，Runtime 解析并路由到对应 AgentSession。

4. 每 AgentSession 串行化
   同一 AgentSession 同时只允许一个 active Turn。

5. 少量稳定的 Observation 当前事实扩展
   P1 可选；不阻塞 P0 Identity + Routing 验收。

6. Adapter 边界测试补强
   ProtocolMapper / ObservationBuilder / 命中判定 的自动测试。

7. 按需的简单、短时、可观察 Capability
   用于再次证明新增 Capability 时 Runtime 无需改主链路。
```

## 不做

```text
长期 Memory / 短期 Context        → Phase4
Multi-step AgentTurn              → Phase5
异步 Action / Turn Resume         → Phase6
自动 reconnect                    → Phase7
Event replay
复杂 Permission / Policy 子系统
Entity 级 Capability（不同实体不同能力集）
大量 Stardew 功能覆盖
DROP / COALESCE 等 Trigger 策略    → 留待未来 Trigger Control
持续 NPC LLM 推理 / 全地图 NPC 轮询
    Phase3 仍只由 player_interacted_with_npc 触发；
    "多 Agent"表示 Runtime 能按身份隔离和调度多个 Agent，
    不表示所有 NPC 持续消耗推理资源。
```

------

# 5. P0 Mandatory Deliverable：Agent Identity Contract

## 5.1 逻辑身份模型

AgentSession identity 由三个逻辑组成部分构成：

```text
AgentSessionIdentity
=
GameScope
+
WorldScope
+
StableEntityIdentity
```

对应当前系统的来源：

```text
GameScope
    当前游戏命名空间。
    来源：AdapterHello.game_id，例如 "stardew-valley"。

WorldScope
    当前存档或世界身份。
    来源：GameEvent.world_id。
    Stardew Adapter 必须使用 Constants.SaveFolderName 作为 WorldID，
    例如存档文件夹名 "Abigail_123456789"。
    不允许使用 UniqueMultiplayerID、session_id 或其他临时值兜底。

StableEntityIdentity
    Adapter 在该世界内提供的稳定、opaque entity_id。
    来源：GameEvent.Entities 中由 GameEvent.target_entity_id 指明的实体，
    例如 "npc:Abigail"。
```

## 5.2 必须保持的排除项

```text
session_id
    MUST NOT 参与 AgentSession identity。
    session_id 是"这一次网络连接"的身份，连接可以换，Agent 不能变。

display_name
    MUST NOT 参与 AgentSession identity。
    Abigail 改叫 "Abby" 不产生新的 Agent。

本地化名称
    MUST NOT 参与 AgentSession identity。
```

## 5.3 entity_type 的处理

`entity_type` 不纳入 Phase3 的 identity 编码。

理由：

```text
Protocol 对 entity_id 的理想约束是 stable / opaque / adapter-defined / 世界内唯一。

Stardew Adapter 的 entity_id（npc:X / player:local）已经带有类型前缀，
在世界内跨类型唯一。

额外拼接 entity_type 是重复信息。
```

如果未来某个 Adapter 无法保证 entity_id 在世界内跨类型唯一，应回到本 Contract 重新决策，而不是静默改变编码。

## 5.4 内部 key 表示（实现细节，不冻结）

Contract 冻结的是逻辑组成与不变量，不冻结字符串编码。

Runtime 内部查找 AgentSession 时，不使用 `agent:{game}:{world}:{entity}` 这类 delimiter 字符串拼接。原因是 `entity_id` 本身允许包含 `:`，未来 `game_id / world_id` 也不应被隐含限制。

Phase3 采用 Go 原生 comparable struct 作为 map key：

```go
type AgentSessionKey struct {
    GameID   string
    WorldID  string
    EntityID string
}
```

示例：

```go
AgentSessionKey{
    GameID:   "stardew-valley",
    WorldID:  "Abigail_123456789",
    EntityID: "npc:Abigail",
}
```

注意：

```text
AgentSessionKey 只是 Runtime 内部 lookup key。
AgentSessionKey 不进入 Protocol。
trace / log 应优先输出结构化字段：game_id / world_id / entity_id。
如果额外输出 agent_session_id，只能作为 diagnostic string；
Phase3 采用人读格式 `game_id/world_id/entity_id`，仅用于日志阅读与测试断言。
Runtime 行为不能反向解析该字符串，不能把它当作持久化主键。
未来 AgentSession 持久化（Phase7）时，由 Phase7 技术方案重新决定存储主键格式。
```

## 5.5 AgentSessionResolver

Contract 必须落地一个最小可测试解析逻辑，否则不变量只能停留在文档层。

```go
// runtime/internal/session

// Resolve 把连接与事件信息解析为稳定的 AgentSessionKey。
// 返回 error 表示输入不足以构成合法身份（例如 world_id 为空）。
func Resolve(gameID, worldID, entityID string) (AgentSessionKey, error)
```

解析规则：

```text
1. gameID / worldID / entityID 任一为空 → error，调用方按 pre-turn rejected 处理。
2. 三个输入组成 AgentSessionKey（见 5.4）。
3. 解析是纯函数，不持有状态，不访问网络。
```

为什么 world_id 为空必须拒绝而不是降级：

```text
如果 world_id 缺失时用 session_id、UniqueMultiplayerID 或其他临时值兜底，会静默破坏
"EnvironmentSession 变化不影响 identity"这条不变量。
宁可拒绝事件（记录日志），也不让身份建立在错误信息上。
```

## 5.6 最低验收不变量矩阵

| 输入变化 | 预期 |
| --- | --- |
| 相同 game、world、entity，多次解析 | 同一 identity |
| entity 不同 | identity 不同 |
| world 不同 | identity 不同 |
| EnvironmentSession / session_id 不同 | identity 不变 |
| display_name 或语言变化 | identity 不变 |
| 相同 display_name、不同 entity_id | identity 不同 |
| gameID / worldID / entityID 任一为空 | 解析失败（error） |

------

# 6. AgentSession 与事件路由设计

## 6.1 AgentSessionKey 与 ExecutionLane 最小状态

Phase3 必须区分两件事：

```text
AgentSessionKey
    长期逻辑身份：game + save/world + entity。
    它回答"这个事件属于哪个 Agent"。

ExecutionLane
    当前 Connect stream 内的临时执行队列。
    它回答"这个 Agent 当前怎么串行执行 turn"。
```

Phase3 不把 worker / channel / lock 当作未来 AgentSession 持久化状态的一部分。

```go
// runtime/internal/session

type AgentSessionKey struct {
    GameID   string
    WorldID  string
    EntityID string
}

type ExecutionLane struct {
    Key   AgentSessionKey
    Queue chan *turnTask
}

type LaneStore struct {
    mu    sync.Mutex
    lanes map[AgentSessionKey]*ExecutionLane
}
```

LaneStore 能力：

```text
GetOrCreate(key, meta)
    不存在则创建当前 Connect stream 内的 ExecutionLane，并启动该 lane 的 worker goroutine。

Drain(reason)
    连接关闭时清空尚未开始执行的 queued events，记录结构化日志，不创建 turn trace。

Close()
    取消当前 Connect stream 内所有 lane worker。
```

注意：

```text
同一个 AgentSessionKey 在不同 Connect stream 中解析结果必须相同；
但 Phase3 的 ExecutionLane 生命周期跟当前 Connect stream 绑定。
自动 reconnect / lane rebind / durable queue 仍属于 Phase7。
```

## 6.2 GameEvent 目标实体解析

当前 `loop.go:61-69` 用 `entity_type == "npc"` 挑选目标，这是 Stardew 特定字符串泄漏进 Runtime，Phase3 必须移除。

新机制：

Phase3 正式引入 `gameagent.protocol.v1alpha2`。WorldScope 不再从连接级 `AdapterHello` 获取，而是随具体 world-scoped message 传递：

```proto
message AdapterHello {
  string adapter_id = 1;
  string adapter_version = 2;
  string protocol_version = 3; // "v1alpha2"
  string game_id = 4;
  string game_version = 5;
  string session_id = 6;
}

message GameEvent {
  string event_id = 1;
  string event_type = 2;
  repeated EntityRef entities = 3;
  GameTime game_time = 4;
  google.protobuf.Struct payload = 5;
  uint64 sequence = 6;
  string world_id = 7;
  string target_entity_id = 8;
}

message ObserveRequest {
  string entity_id = 1;
  string world_id = 2;
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

message ActionRequest {
  string action_id = 1;
  string entity_id = 2;
  string capability = 3;
  google.protobuf.Struct arguments = 4;
  google.protobuf.Struct extensions = 5;
  string world_id = 6;
}
```

Adapter 发送事件时设置：

    event.world_id = "Abigail_123456789"
    event.target_entity_id = "npc:Abigail"

Runtime 的解析规则：

1. event.target_entity_id 为空
   → pre-turn rejected，EventAck.Error.Code = target_entity_missing。

2. event.target_entity_id 不在 event.Entities 中
   → pre-turn rejected，EventAck.Error.Code = target_entity_not_in_event。

3. 目标实体解析成功但 Resolve 失败（例如 world_id 为空）
   → pre-turn rejected，EventAck.Error.Code = identity_scope_missing。

pre-turn reject 的可观测性（这些不创建 turn，不进 turn trace，必须走 EventAck error code）：

```text
固定 EventAck error code，测试断言：

    target_entity_missing          event.target_entity_id 为空
    target_entity_not_in_event     event.target_entity_id 不在 event.Entities
    identity_scope_missing         Resolve 失败（game_id / world_id 任一为空；entity_id 缺失由 target_entity_missing 覆盖）
    event_id_missing               event.event_id 为空，无法做 EnvironmentSession 内去重
    unsupported_event_type         event_type 不属于 Phase3 支持的 trigger
    session_queue_full             目标 lane 队列满，无法接纳
    environment_closed             Connect stream 正在关闭，已停止 admission
```

Protocol 决策：

```text
Protocol 版本升级决策（Phase3 范围扩展）：
    本方案接受将 Protocol 从 v1alpha1 升级到 v1alpha2，
    作为 Phase3 Agent Identity Contract 的必要前提，
    而不是推迟到独立的 Protocol 演进阶段。

原因：
    1. WorldScope 双来源会直接破坏 identity invariant：
       同一 game / world / entity 必须稳定解析到同一个 AgentSession。
    2. Phase3 只有 Runtime + Stardew Adapter 两端需要同步升级，
       此时一次性迁移成本最低；Phase4+ 接入更多 Adapter 后成本会显著上升。
    3. 一次性把 Event → Observe → Action 的 WorldScope 贯穿，
       避免 Phase3 只改 target_entity_id、后续再改 world_id 的二次协议迁移。
    4. v1alpha2 不把 Runtime-internal AgentSession 暴露到 Protocol，
       只收紧 Environment/World boundary。

v1alpha2 将 EnvironmentSession 定义为：
    Game / Adapter 的 live connection。

WorldScope 不属于 AdapterHello。
同一条 Connect stream 可能经历 title screen → Save_A → title screen → Save_B，
因此 AdapterHello 不声明 world_id，避免出现 AdapterHello.world_id 与 GameEvent.world_id 两个权威来源。

target_entity_id 是 Runtime 路由 GameEvent 的通用 contract，
不再放进 payload，也不从 payload fallback。

Roadmap v0.2 曾提醒：Phase3 重点是泛化解析与路由，不默认新增协议字段。
本方案选择新增 typed target_entity_id，是因为现有 GameEvent.Entities 只表达"事件相关实体集合"，
不表达"触发目标实体"。一旦事件携带 player + 多个 NPC 或 Observation 扩展附近 NPC，
仅靠列表顺序或 entity_type 选择目标会产生歧义。
因此 target_entity_id 是一个显式 trigger-target pointer：
    它必须引用 event.entities 中已有 EntityRef；
    它不引入 Runtime-internal AgentSession 概念；
    它不依赖 payload key 或实体列表位置约定。

payload 继续用于 trigger / source 等 Adapter 扩展信息；
如果 payload 中也出现 target_entity_id，Runtime 必须忽略它，避免双来源冲突。

v1alpha2 是 Phase3 接受的一次性 Protocol breaking revision：
    target_entity_id / ObserveRequest.world_id / ActionRequest.world_id 是字段增加；
    package v1alpha1 → v1alpha2、save_id → world_id、AdapterHello 移除 save_id
    是 alpha 阶段允许的破坏性修订。
Phase3 必须更新 proto 源文件、Go 生成代码、C# Grpc.Tools 生成入口、
protocol static check 与 Adapter mapper 测试。

world_id 贯穿 WorldScope 链路：
    GameEvent.world_id       事件属于哪个世界。
    ObserveRequest.world_id  Runtime 要观察哪个世界里的实体。
    Observation.world_id     Adapter 回传的是哪个世界的观察结果。
    ActionRequest.world_id   Runtime 要在哪个世界执行副作用。

ActionResult / ActionStatusUpdate 不新增 world_id：
    通过 action_id 关联原始 ActionRequest。
    如 Adapter 发现 request.world_id != currentWorldId，
    返回 ActionResult(status=REJECTED, error.code=world_mismatch)。

CapabilityRequest / CapabilityList 暂不新增 world_id：
    Phase3 capability 仍是 environment-level，不做 world/entity-specific capability。

Phase3 由 Runtime 与 Stardew Adapter 同步升级到 v1alpha2，
不保留 save_id / world_id 双字段兜底。
```

## 6.3 每 AgentSession 串行化

设计决策：**每 session 一个 bounded 队列 + 一个 worker goroutine 串行消费**。

```text
Gateway recvLoop 收到 GameEvent
    ↓
Trigger Admission（event_type 必须是 player_interacted_with_npc）
    ↓
Resolve(gameID, worldID, targetEntityID)
    ↓
LaneStore.GetOrCreate → 拿到当前 stream 内的 ExecutionLane
    ↓
创建 turnTask{event, admitted}
    ↓
lane.enqueue(task)       // 非阻塞；队列满 → REJECTED(session_queue_full)
    ↓
EventAck(ACCEPTED)
    ↓
close(task.admitted)     // admission barrier：允许 worker 开始 turn
    ↓
worker goroutine 串行执行：
    <-task.admitted
    ↓
    Loop.HandleEvent(ctx, env, conn, sessionKey, event)
    ↓
队列空 → worker 等待下一个事件；Connect stream 关闭时退出
```

为什么选这个方案而不是全局 worker pool：

```text
方案 A：单 eventCh + 单 goroutine（现状）
    NPC A 的慢 LLM 调用阻塞 NPC B 的事件。多 NPC 环境下不可接受。

方案 B：全局 worker pool + session mutex
    pool 大小、队列深度、背压三套参数，调度语义不直观。

方案 C：每 AgentSessionKey 一条 ExecutionLane + 一个 worker（选定）
    同一 session 天然 FIFO 串行，不同 session 天然并行，
    队列满直接复用 Phase2 的 EventAck REJECTED 语义。
    worker 生命周期绑定当前 Connect stream，不提前设计 Phase7 的 reconnect/rebind。
```

冲突策略（Roadmap 要求本方案确定）：

```text
同一 session 的新事件到达时，上一个 turn 尚未结束：
    进入 session 队列等待（FIFO）。
    队列上限（默认 1）满 → EventAck REJECTED(code=session_queue_full)。
    不 DROP、不 COALESCE —— 留待未来 Trigger Control。
```

v1alpha2 EventAck 语义：

```text
EventAck(ACCEPTED)
    = Runtime 已完成运行时 admission，确认该事件已有 queue capacity，
      且会在当前 EnvironmentSession 生命周期内进入 Turn 或记录 event_aborted_before_turn。
    不等于 turn 已完成。
    不等于 durable persistence / crash recovery。

EventAck(REJECTED)
    = Runtime 无法接纳（队列满 / 身份解析失败）。

EventAck(DUPLICATE)
    = 当前 EnvironmentSession 内已经接纳过相同 event_id。
      Runtime 不重新创建 Turn，不重新执行副作用。
      该去重不跨 reconnect / Runtime restart。
```

v1alpha2 Phase3 Event Delivery 语义：

```text
Runtime 在当前 EnvironmentSession 内维护 seenEventIDs。

event_id 为空：
    → EventAck(REJECTED, code=event_id_missing)
    → 不解析 identity，不创建 lane，不创建 turn trace。

第一次接纳某 event_id：
    → admission 成功后记录 seenEventID
    → EventAck(ACCEPTED)

同一 EnvironmentSession 再收到相同 event_id：
    → EventAck(DUPLICATE)
    → 不进入 Trigger Admission / Resolve / LaneStore
    → 不创建 turn trace。

Phase3 不保证：
    跨 reconnect 去重
    跨 Runtime restart 去重
    durable event replay

GameEvent.sequence：
    EnvironmentSession-scoped monotonic sequence。
    同一 Connect stream 内切换 WorldScope 不重置 sequence。
```

Event type admission 必须发生在 Resolve / GetOrCreate 之前：

```text
unsupported event_type
    → EventAck(REJECTED, code=unsupported_event_type)
    → 不解析 identity，不创建 lane，不创建 turn trace。
```

`EventAck(ACCEPTED)` 仍是运行时 admission ack，不是 durable event acceptance：

```text
Phase3 不承诺事件持久化、replay、reconnect recovery。
这些语义在 Phase7 与持久化 AgentSession 一起重新设计。
```

Event Admission Invariant：

```text
只要 Runtime 已返回 EventAck(ACCEPTED)，该 event 在当前 EnvironmentSession 生命周期内最终必须二选一：

1. start AgentTurn
   → completed / failed

2. 在 Turn 创建前被连接关闭取消
   → structured log: event_aborted_before_turn, reason=connection_closed

禁止 ACCEPTED 之后无 Turn、无 aborted 记录而静默丢失。

EventAck(DUPLICATE) 是该 event_id 在当前 EnvironmentSession 内的终态 ack；
它不创建 Turn，也不要求 event_aborted_before_turn。
```

ACK 顺序不变量：

```text
Runtime 必须保证：
    GameEvent
    → queue capacity reserved
    → EventAck(ACCEPTED)
    → worker starts AgentTurn
    → ObserveRequest / ActionRequest

worker 即使先从 queue 取到 task，也必须等待 admitted barrier。
禁止 ObserveRequest 早于 EventAck(ACCEPTED) 到达 Adapter。

如果 EventAck 发送失败或 stream context 关闭：
    task 不启动 Turn。
    queued but not started event 记录 event_aborted_before_turn。
```

并发契约（本阶段从单 goroutine 串行 → 多 session 并发，必须显式成立）：

```text
同一 Connect stream 支持多个 session 并发 Observe / SubmitAction：
    pendingObservations 按 correlation_id 隔离。
    pendingActions      按 action_id 隔离。
    send                由 sendMu 串行化（同一条 gRPC stream 只有一个 writer）。

Adapter 必须正确回传 correlation_id（Observation / Error）与 action_id（ActionResult），
否则 Runtime 侧即使 race 干净，也会串线。

pending Observe 可以被两类 AdapterMessage 解除：
    1. Observation：correlation_id 命中 pendingObservations。
    2. Error：correlation_id 命中 pendingObservations，例如 Error(code=world_mismatch)。

Runtime 接收成功 Observation 后必须反向校验 scope：
    Observation.world_id == requested world_id
    Observation.entity_id == requested entity_id
若不匹配：
    Environment.Observe 返回 error；
    AgentTurn 进入 turn_failed(stage=observe, reason=observation_scope_mismatch)。

pending Observe 被 Observation 或 Error 解除后，Runtime 必须删除 waiter；
迟到的 Observation / Error 命中不到 waiter 时忽略，不二次 resolve。

Runtime shared services 必须支持多 AgentSession 并发：
    model.Provider.Generate      支持不同 AgentSession 并发调用。
    Environment.Observe          支持同一 stream 多个 pending request。
    Environment.SubmitAction     支持同一 stream 多个 pending action。
    ToolRegistry read path       bootstrap 后只读，ValidateToolCall 可并发调用。
    Trace Recorder / TurnTracer  支持多 turn 并发 Record，保持非阻塞 best-effort。
    stream.Send                  仍由 connection-level sendMu 保证 single writer。

验收手段：gateway/session 集成测试必须覆盖乱序返回，并用 go test -race 跑过。
```

## 6.4 ExecutionLane 生命周期与断线语义

```text
Phase3 的 ExecutionLane 是 EnvironmentSession-scoped runtime state。

Phase3 supported topology：
    Phase3 Runtime process 同一时刻只支持一个 live Connect stream。
    duplicate live stream / reconnect overlap / lane rebind 留到 Phase7。

worker 生命周期：
    LaneStore.GetOrCreate 创建 lane 时启动。
    lane queue 为空时阻塞等待下一次 enqueue。
    Connect stream 关闭时通过 context cancel 退出。

Close / Drain 并发契约：
    Close 开始后停止 admission。
    enqueue-after-close 返回 ErrLaneClosed；gateway 在尚未发送 ACCEPTED 时返回 EventAck(REJECTED, code=environment_closed)。
    已 ACCEPTED 但尚未被 worker 取出的 event 必须由 Drain 记录 event_aborted_before_turn。
    已被 worker 取出但尚未 emit turn_started 的 event，仍属于 pre-turn 阶段；
        连接关闭时由 worker 记录 event_aborted_before_turn。
    已 emit turn_started 的 event 才进入 AgentTurn lifecycle；
        之后发生连接失败时按 active turn 失败路径收敛。
    turn_started 是 Event lifecycle 和 Turn lifecycle 的唯一分界点。

stream disconnect：
    active turn
        → 复用 Phase2 stream failure 路径，Observe / SubmitAction 被唤醒，
          已创建 turn 进入 turn_failed。

    queued but not started event
        → drain queue。
        → 记录 structured log: event_aborted_before_turn, reason=connection_closed。
        → 不创建 turn trace。

Phase3 不做 idle eviction / worker restart / maxSessions。
LaneStore 在一个 EnvironmentSession 内不回收已创建 lane。
因此跨多个 WorldScope 切换后，lane 数量等于本连接历史上访问过的
AgentSessionKey 数量，而不是仅当前 world 的 NPC 数量。
Phase3 预期规模仍有界；
如未来需要全局 session capacity，由 Trigger Admission / Phase7 持久化方案统一处理。
```

------

# 7. Runtime 落地范围

## 7.1 新增

```text
runtime/internal/session/
    identity.go        AgentSessionKey / Resolve / diagnostic id（如需要）
    lane.go            LaneStore / ExecutionLane / 队列与 worker
    identity_test.go   Resolve 不变量矩阵测试
    lane_test.go       并发安全 / 队列满 / drain 测试
```

## 7.2 修改

```text
runtime/internal/agent/loop.go
    HandleEvent 签名增加 sessionKey：
        HandleEvent(ctx, env, conn, sessionKey, event)
    target entity 由 sessionKey.EntityID 派生，不再另传 targetEntityID，避免二者漂移。
    event_type admission 已前移到 gateway；Loop 不再静默忽略 unsupported event。
    删除 entity_type == "npc" 选择逻辑（目标实体由 gateway 解析后传入）。
    TurnContext 可携带 AgentSessionKey / diagnostic agent_session_id，用于 trace/log。
    调用 Environment.Observe 时传入 sessionKey，确保 ObserveRequest.world_id 与 entity_id 同源。

runtime/internal/gateway 或 runtime/internal/session 配置
    增加 SessionQueueSize int（默认 1）。
    该参数属于事件准入/背压，不属于 AgentTurn 执行配置。

runtime/internal/agent/prompt.go
    不需要结构性修改；Model Request 中无需出现 agent_session_id（内部标识）。

runtime/internal/gateway/gateway.go
    用 session dispatch 替换单 eventCh + 单 goroutine：
        recvLoop 收到 Event → event_id 去重 → event_type admission → 解析 target → Resolve → GetOrCreate →
        enqueue task（满 → EventAck REJECTED session_queue_full）→ EventAck ACCEPTED → release admitted barrier。
    当前 EnvironmentSession 内维护 seenEventIDs，用于 EventAck(DUPLICATE)；
        不做跨 reconnect / restart 持久化。
    连接断开时：failAllPending 语义保持不变；
        active turn 复用 Phase2 turn_failed；
        queued but not started event drain 并记录 event_aborted_before_turn，不创建 turn。

runtime/internal/gateway/stream_environment.go
    Observe 接口从 entityID 扩展为 sessionKey 或 (worldID, entityID)，发送 ObserveRequest.world_id。
    pendingObservations 存储 requested world_id / entity_id。
    recvLoop 必须允许 correlated Error 解除 pending Observe，
        而不只允许 Observation 解除 pending Observe。
    Observation 返回成功时，Runtime 反向校验 world_id / entity_id；
        不匹配则 Observe 返回 observation_scope_mismatch。
    SubmitAction 继续接收完整 ActionRequest，但要求 req.WorldId 非空。
    Adapter 返回 world_mismatch 时：
        Observe 阶段 → turn_failed(stage=observe, reason=world_mismatch)；
        Action 阶段 → turn_failed(stage=action, reason=world_mismatch)；
        TurnTracer 记录 world_mismatch 为独立 failure reason，便于诊断存档切换问题。

runtime/internal/tool/environment_tool.go
    BuildActionRequest 从 entityID 参数改为 sessionKey 参数：
        BuildActionRequest(sessionKey, toolCall)
    填充 ActionRequest.world_id = sessionKey.WorldID；
    填充 ActionRequest.entity_id = sessionKey.EntityID。

runtime/internal/trace/trace.go
    TurnContext 可增加 AgentSessionKey 或 diagnostic agent_session_id。
    Event JSON 使用 world_id 字段表达 WorldScope。
    Event JSON 不把 diagnostic id 当作可解析或持久化主键。
    保持"非阻塞 best-effort Observer"语义不变。

protocol/proto/gameagent.proto
    新增/切换到 gameagent.protocol.v1alpha2。
    AdapterHello 不带 save_id / world_id。
    GameEvent 增加 world_id + target_entity_id。
    ObserveRequest 增加 world_id。
    Observation 使用 world_id。
    ActionRequest 增加 world_id。
    EventAck(DUPLICATE) 定义为 EnvironmentSession 内 event_id 去重结果。
    GameEvent.sequence 定义为 EnvironmentSession-scoped monotonic sequence。
    重新生成 Go protocol 代码。
    C# 侧通过 Grpc.Tools 在 Adapter 构建时生成代码；
        更新 Adapter using / namespace 到 GameAgent.Protocol.V1Alpha2。
    更新 protocol static check。
```

## 7.3 明确不改

```text
runtime/internal/tool/registry.go
    Capability 仍是 environment 级（不是 entity 级），注册逻辑不变。
    ValidateToolCall 的 entityID 参数保留但继续不参与校验。

runtime/internal/model + llm/*
    Provider 接口 shape 不变，但必须通过并发审计：不同 AgentSession 可并发 Generate。
```

------

# 8. Stardew Adapter 落地范围

## 8.1 多 NPC 交互捕获

```text
adapters/stardew/src/Runtime/AdapterConfig.cs
    TargetAgentName: string
        ↓
    AgentTargets: List<string>
        空列表 = 允许所有 villager NPC（Phase3 默认走这个，验证泛化）。
        非空 = 只允许列表中 NPC。

adapters/stardew/src/Events/PlayerInteractProbe.cs
    FindClickedTarget 从"按名字查单个 NPC"改为"候选枚举 + 命中判定 + 目标过滤"：
        1. 从 Game1.currentLocation.characters 枚举当前地点 villager NPC。
        2. 目标过滤：AgentTargets 为空 → 接受；非空 → 名字在列表内才接受。
        3. 命中判定：光标像素落在 NPC 包围盒 / 相邻 tile（保留现有几何判定）。
        4. tie-break：包围盒命中优先于相邻 tile；同类多个候选按光标距离升序，
           再按 NPC.Name ordinal 升序，保证确定性。
    命中判定与过滤抽成纯函数，便于无 SMAPI 单测。
```

## 8.2 事件携带目标实体

```text
adapters/stardew/src/Runtime/ProtocolMapper.cs
    抽出无 SMAPI 依赖的纯函数，例如：
        BuildPlayerInteractedWithNpcEvent(
            string npcEntityId,
            string npcDisplayName,
            string playerEntityId,
            string playerDisplayName,
            string trigger,
            ulong sequence,
            string worldId)

    BuildPlayerInteractedWithNpcEvent 设置 typed 字段：
        TargetEntityId = npcEntityId

    现有 NPC/Farmer overload 只做薄封装：
        NPC/Farmer → entity_id/display_name/world_id → 调纯函数。

    payload 继续保留 trigger / source 等扩展信息，不再承载 target_entity_id。
    保持 npc:Name ↔ NPC 映射集中在本类，不在 Probe / Capability 复制。
```

## 8.3 Observation 稳定事实扩展（P1 可选）

Observation 扩展不阻塞 Phase3 P0 Identity + Routing；若本阶段实现，只加验证多 NPC 泛化所需的最小事实：

```text
adapters/stardew/src/State/ObservationBuilder.cs
    继续读取 Stardew 当前事实；如实现 NearbyEntities 扩展，
    负责枚举并规范化 nearby NPC 候选，写入 ProbeObservation。

adapters/stardew/src/State/ProbeObservation.cs
    如实现 NearbyEntities 扩展，增加可测试的 nearby NPC 候选数据结构，
    例如 List<NpcRef> 或等价 record。
    State 层不构造 protobuf EntityRef。

adapters/stardew/src/Runtime/ProtocolMapper.cs
    BuildObservation 从 ProbeObservation 读取 nearby NPC 候选，
    转换为 Observation.NearbyEntities 中的 EntityRef：
        玩家当前地点的其他 NPC（可配置半径或同地图即可）。
        每个 NPC 作为 EntityRef(entity_type="npc", display_name)。
    已有 agent/player 事实字段保持不变。
```

可选（P2，非验收必需）：

```text
state 增加 season / weather 两个稳定字段，给模型更稳定的环境上下文。
```

## 8.4 可选简单 Capability（验证动态注册）

Phase2 已经用 speak + emote 证明了"新增 Capability 不需要改 Runtime 主链路"。Phase3 可选增加一个简单、短时、可观察的能力，例如：

```text
face_player
    让 NPC 面向交互玩家（game1 侧一次朝向设置）。
    input: {}
    output: { "facing": "..." }
```

验收点：Adapter 上报后，Runtime 通过既有动态 Tool Registry 感知，无需任何分支代码。此项为 P2 可选，不做也不影响本阶段完成条件。

## 8.5 明确不改

```text
RuntimeClient.HandleActionOnMainThread 已经按 capability 通用分发，不改。
RequireNpc 已经按 entity_id 通用解析任意 NPC，不改。
CapabilityCatalog 保持 environment 级（speak + emote [+ face_player]）。
```

------

# 9. 测试计划

## 9.1 Go 单元测试

```text
session.Resolve：
    - 相同 game/world/entity 多次解析 → 同一 AgentSessionKey
    - entity 不同 → 不同 AgentSessionKey
    - world 不同 → 不同 AgentSessionKey
    - game 不同 → 不同 AgentSessionKey
    - 任一输入为空 → error
    - AgentSessionKey 可比较，且不依赖 delimiter 字符串拼接

session.LaneStore：
    - GetOrCreate 幂等（同 key 返回同一 lane）
    - 并发 GetOrCreate / enqueue 无 data race（-race）
    - 队列满时 enqueue 返回错误（模拟 session_queue_full）
    - Close 与 enqueue 并发时，要么 enqueue 成功并被 Drain 记录 aborted，要么返回 ErrLaneClosed
    - Close / Drain 会清空 queued but not started events
    - worker 在 stream context cancel 后退出
    - queued event drain 不创建 turn trace，只记录 event_aborted_before_turn

agent.Loop：
    - sessionKey.EntityID = "companion:foo"，event.Entities 中存在对应 EntityRef 时也能创建 turn
    - Loop 能正常 Observe / Action，证明不依赖 entity_type=="npc"
    - TurnContext 可携带 AgentSessionKey / diagnostic agent_session_id

gateway dispatch：
    - event_id 缺失 → pre-turn rejected，断言 error code = event_id_missing
    - 同一 EnvironmentSession 内重复 event_id → EventAck DUPLICATE，不创建 turn trace
    - unsupported event_type → pre-turn rejected，断言 error code = unsupported_event_type
    - target_entity_id 缺失 → pre-turn rejected（无 turn trace），断言 error code = target_entity_missing
    - target_entity_id 指向事件外的实体 → pre-turn rejected，断言 error code = target_entity_not_in_event
    - world_id 缺失 → pre-turn rejected，断言 error code = identity_scope_missing
    - 队列满 → EventAck REJECTED，断言 error code = session_queue_full
    - EventAck(ACCEPTED) 必须早于 ObserveRequest / ActionRequest 发送
    - ObserveRequest.world_id / ActionRequest.world_id 均来自 AgentSessionKey.WorldID

protocol：
    - gameagent.proto 使用 package gameagent.protocol.v1alpha2
    - AdapterHello 不包含 save_id / world_id
    - GameEvent 包含 world_id / target_entity_id
    - ObserveRequest 包含 world_id
    - Observation 包含 world_id
    - ActionRequest 包含 world_id
    - EventAck(DUPLICATE) 语义为当前 EnvironmentSession 内 event_id 去重
    - GameEvent.sequence 是 EnvironmentSession-scoped monotonic sequence
    - Go / C# generated code 与 proto 源一致
    - static protocol check 覆盖新增字段
```

## 9.2 Go 集成测试（fake adapter）

在既有 `gateway_integration_test.go` 的 fake adapter 基础上扩展（不新建框架，Phase4 才收敛成确定性测试夹具）：

```text
1. 双 NPC 路由
   adapter 发送两个事件（npc:Linus / npc:Abigail）
   → Runtime 解析出两个不同 AgentSession
   → 两个 turn 各自完整执行
   → trace 中两个 turn 的 entity_id / agent_session_id 互不串线

2. 同 NPC 连发事件
   adapter 连续发送两个 npc:Linus 事件
   → 两个 turn 串行执行（第二个在第一个终态后开始）
   → trace 无并发重叠

3. 不同 NPC 互不阻塞
   fake provider 对 npc:Linus 的调用阻塞在测试可释放的 channel/barrier
   → npc:Abigail 的事件仍能在等待期间完成 turn（并发调度生效）
   → 测试用信号断言并发，不使用 sleep / 墙钟时间判断

4. 多 session 并发 Observe/Action
   两个 NPC 同时进入 Observe + Action 阶段
   → pendingObservations 按 correlation_id、pendingActions 按 action_id 正确匹配
   → correlated Error(world_mismatch) 能解除对应 pending Observe，不等待 timeout
   → pending Observe 被 Error 解除后，迟到 Observation 被忽略，不二次 resolve
   → Observation.world_id/entity_id 与请求不一致时返回 observation_scope_mismatch
   → 全程 go test -race 无告警
   → 乱序返回 Observation/ActionResult 仍各自归位
   → 每个 ObserveRequest / ActionRequest 携带对应 sessionKey.WorldID

5. 队列满
   一个 NPC 的事件批量灌入超过队列上限
   → 后续事件 EventAck REJECTED(session_queue_full)
   → 已接纳的事件仍按序完成

6. session_id 变化不影响身份
   用两个不同 session_id 的 stream 各发一次同实体事件
   → 解析出同一个 AgentSessionKey（可由 Resolve 单测 / trace 字段断言）

7. 连接断开时 drain queued events
   Event A 已开始，Event B 已 ACCEPTED 但还在 lane queue
   → stream disconnect
   → A 按 Phase2 turn_failed 收敛
   → B 记录 event_aborted_before_turn，不创建 turn trace

8. enqueue 与 Close/Drain 竞态
   并发执行 enqueue 与 Close
   → enqueue 未被 ACCEPTED 时返回 environment_closed
   → 已 ACCEPTED 的 event 必须 start turn 或 event_aborted_before_turn
   → go test -race 无告警

9. Admission barrier 顺序
   worker 在 EventAck(ACCEPTED) 前拿到 task
   → worker 阻塞在 admitted barrier
   → fake adapter 先收到 EventAck，再收到 ObserveRequest

10. world mismatch
    fake adapter 对 ObserveRequest / ActionRequest 返回 world_mismatch
    → active turn 按失败路径收敛
    → 不执行跨世界 Action

11. Event delivery / duplicate
    同一 EnvironmentSession 内重复发送相同 event_id
    → 第一次按 admission 结果 ACCEPTED 或 REJECTED
    → 已 ACCEPTED 的 event_id 再次出现时 EventAck DUPLICATE
    → 不重新创建 Turn
    → disconnect / new stream 后不保证继续去重
```

## 9.3 Adapter 单元测试

沿用 `ActionCancellationRegistry.Tests` 的独立小测试工程模式（无 SMAPI 依赖）：

```text
adapters/stardew/tests/ProtocolMapper.Tests/
    - npc:Name → entity_id 往返
    - 纯函数 BuildPlayerInteractedWithNpcEvent(string...) 的 GameEvent.TargetEntityId 正确
    - 纯函数 BuildPlayerInteractedWithNpcEvent(string...) 的 GameEvent.WorldId 正确
    - payload 不再写入 target_entity_id
    - TryParseNpcEntityId 非法输入

adapters/stardew/tests/PlayerInteractProbe.Tests/（若命中判定抽为纯函数）
    - 命中判定：包围盒内 / 相邻 tile / 范围外
    - 目标过滤：空列表接受 / 列表内接受 / 列表外拒绝
    - tie-break：包围盒优先于相邻 tile；同类候选按距离，再按 NPC 名称排序

Adapter 并发审计：
    - RuntimeClient.SendAsync 仍保证同一 stream 单 writer。
    - Observation 通过 correlation_id 回传，ActionResult 通过 action_id 回传。
    - 两个 NPC 近似同时触发时，不共享 mutable per-turn state。
    - HandleObserveOnMainThread 校验 request.WorldId == currentWorldId，不一致返回 Error(code=world_mismatch)。
    - HandleActionOnMainThread 校验 request.WorldId == currentWorldId，不一致返回 ActionResult(REJECTED, error.code=world_mismatch)。
```

## 9.4 真机手工验收

```text
1. 配置 AgentTargets 为 ["Linus", "Abigail"]（或留空 = 所有 villager）。

2. SMAPI 启动，连接 Runtime。

3. 点击 Linus → Linus 显示模型台词。
4. 点击 Abigail → Abigail 显示模型台词。
   （确认不再只有一个 NPC 响应）

5. 快速连续点击同一 NPC 两次 → 两次台词按序出现，无并发错乱。

6. 分别点击两个 NPC（间隔极短）→ 两个 NPC 都能响应，
   一个 NPC 的慢响应不阻塞另一个。

7. SMAPI 日志确认 GameEvent.world_id 稳定等于 Constants.SaveFolderName，
   且 GameEvent.target_entity_id 字段包含目标 NPC entity_id。

8. C# Adapter 日志确认两个 NPC 并发路径下 correlation_id / action_id 不串线。

9. 切换存档或模拟 currentWorldId 不匹配时，Observe / Action 被 world_mismatch 拒绝。

10. Runtime trace 按 agent_session_id 或 AgentSessionKey 字段能区分两个 NPC 的 turn。
```

------

# 10. 实施顺序

按可独立验证的方式推进，每步完成即可验证：

```text
1. 修改 protocol/proto/gameagent.proto 并升级到 v1alpha2：
   package / Go package / C# namespace 使用 v1alpha2；
   AdapterHello 移除 save_id / world_id；
   GameEvent 使用 world_id + target_entity_id；
   ObserveRequest / Observation / ActionRequest 使用 world_id；
   EventAck(DUPLICATE) / GameEvent.sequence 语义同步到 Protocol v1alpha2 设计规范；
   重新生成 Go 代码；C# 通过 Grpc.Tools 构建时生成；
   更新 protocol static check。

1a. 新增 docs/phase3/GameAgent Protocol v1alpha2 设计规范.md：
    固定 EnvironmentSession / WorldScope / EventAck / Event delivery /
    Observe-Observation / ActionRequest / reconnect 当前保证。

2. 新建 runtime/internal/session：AgentSessionKey + Resolve + 不变量单测。

3. 新建 LaneStore + ExecutionLane 队列/worker + 并发、队列满、drain 单测。

4. 修改 trace.TurnContext：携带 AgentSessionKey 或 diagnostic agent_session_id。

5. 修改 gateway/session 配置：SessionQueueSize。

6. 修改 agent.Loop：删除 entity_type=="npc" 硬编码，
   签名增加 sessionKey，target entity 从 sessionKey.EntityID 派生。

7. 修改 gateway / streamEnvironment：
   session dispatch 替换单 eventCh + 单 goroutine；
   event_id 去重 + event_type admission + typed target_entity_id 解析规则 + EventAck error taxonomy；
   ObserveRequest.world_id / ActionRequest.world_id；
   correlated Error 解除 pending Observe；
   Observation world_id / entity_id 反向校验；
   admitted barrier + disconnect drain。

8. 扩展 gateway 集成测试：双 NPC 路由 / 同 NPC 串行 / 跨 NPC 并行 / 队列满 / session 无关 / disconnect drain / admission barrier / world_mismatch / correlated Error / duplicate event。

9. Adapter：AdapterConfig.AgentTargets + PlayerInteractProbe 多目标命中。

10. Adapter：ProtocolMapper 设置 GameEvent.WorldId / TargetEntityId + 单测。

11. Adapter：RuntimeClient 对 ObserveRequest / ActionRequest 做 world_id mismatch guard。

12. Adapter：ObservationBuilder 附近 NPC + 单测（P1 可选，不阻塞 P0）。

13. 真机双 NPC smoke test。

14. Architecture boundary check：确认 runtime 不依赖 adapters/、adapter 不依赖 runtime/internal/、runtime 无具体游戏 API 引用、proto 源与生成代码一致（对应 Roadmap §12 交付物第 6 条）。

15. 形成《Agent Identity Contract》验收文档，逐条对照不变量矩阵。

16. 阶段 Review：确认进入 Phase4 的依赖门（Identity Contract Accepted）。
```

------

# 11. 验收标准

## 11.1 对照 Roadmap 完成条件

| Roadmap 完成条件 | 本方案验收手段 |
| --- | --- |
| 多个 NPC 可以进入同一条 Runtime AgentTurn 链路 | 集成测试 1（双 NPC 路由）+ 真机 3/4 |
| Runtime 不需要为具体 NPC 增加分支 | entity_type="companion" 行为测试 + code review |
| Agent Identity Contract 已验收，并覆盖最低身份不变量 | Resolve 不变量矩阵单测 + Contract 文档 |
| GameEvent 目标实体可以解析并路由到对应 AgentSession | typed target_entity_id 协议测试 + 集成测试 1/6 + target_entity_id 规则单测 |
| 同一 AgentSession 不会同时运行多个 active Turn | 集成测试 2（同 NPC 串行）+ LaneStore 单测 |
| 稳定 entity identity 足以成为未来 AgentSession 的身份基础 | 5.6 不变量矩阵 + 与架构 §9 对齐 |
| 新增简单 Capability 时，Runtime 继续通过动态 Tool Registry 感知 | face_player（P2 可选）或复用 speak/emote 验证 |
| Adapter 的关键映射和结果 contract 有自动测试 | ProtocolMapper / Probe 单测 + 既有 ActionCancellationRegistry 测试 |

## 11.2 依赖门

```text
进入 Phase4 前
    Agent Identity Contract 必须 Accepted
    = 本方案的 5.6 不变量矩阵全部通过 + Contract 文档评审通过。
```

## 11.3 阶段结束状态

按 Roadmap §12 三档状态之一结束：

```text
Accepted
Accepted with Known Limitations
Needs Follow-up
```

不能只以"代码写完"作为依据；必须包含：本方案 + Contract + 自动测试 + 真机验收记录 + 阶段 Review 结论。

按 Roadmap §12（v0.3 Baseline），每阶段固定交付物还包含：

```text
6. Architecture boundary check 与 protocol generated-code 一致性检查
   （至少：runtime 不依赖 adapters/、adapter 不依赖 runtime/internal/、
    runtime 不引用具体游戏 API、proto 源与生成代码一致）。
```

本方案 §12 的 DoD 自检表即该交付物的验收手段。

------

# 12. 架构 DoD 自检（对照 Architecture v0.2 §44）

```text
[ ] Runtime 没有新增具体游戏 API 依赖。
    loop 删除 entity_type=="npc" 硬编码；session 包不接触游戏类型。

[ ] Adapter 没有新增 Agent cognition。
    Adapter 只负责 target_entity_id 声明与事件捕获，不参与身份决策逻辑。

[ ] Protocol 没有新增 Runtime-internal 概念。
    target_entity_id 是 Environment → Runtime 的通用事件路由字段；
    world_id 是 Environment/WorldScope 通用字段；
    AgentSessionKey / ExecutionLane 不进入 Protocol。

[ ] 新 Capability 的游戏语义由执行它的 Adapter 定义。
    face_player（若做）schema 由 Adapter 上报。

[ ] 新 Tool 已明确属于 Environment Tool 或 Runtime Tool。
    Phase3 无新增 Runtime Tool。

[ ] Agent Core 没有新增 provider-specific API shape。

[ ] Action side effect 有明确 success / failure semantics。
    ActionRequest 携带 world_id；Adapter 发现 world mismatch 时返回
    ActionResult(REJECTED, error.code=world_mismatch)，不执行副作用。

[ ] AgentTurn 最终能够 completed 或 failed。
    pre-turn rejected 不创建 turn；queued but not started event 断线 drain 不创建 turn；
    已创建的 turn 保持 Phase2 终态保证。

[ ] Observer 不改变 Agent 主流程。
    trace 扩展只增加字段与事件，非阻塞 best-effort 语义不变。

[ ] 改变 Agent 行为的能力没有伪装成 Trace Observer。

[ ] 核心 contract 有自动测试。
    Proto v1alpha2 / world_id + target_entity_id / EventAck(DUPLICATE) / correlated Error /
    Resolve / LaneStore / 路由 / world_mismatch 均有单测与集成测试。

[ ] 有真实或等价 vertical slice 验证。
    真机双 NPC smoke test。

[ ] 没有为了当前功能提前构建无真实调用方的大型抽象。
    session 包只做身份 + 当前 stream execution lane 两件事；不建 trigger 框架、不建 EventBus、不建 reconnect runtime。
```

------

# 13. 风险与边界

## 13.1 WorldID 来源稳定性

```text
AdapterHello 建立连接时 currentWorldId 可能为空（存档未加载）。
GameEvent 级别 WorldId 由 RefreshWorldContext 维护，事件发生时通常已有值。

边界策略：
    WorldID 唯一允许来源是 Stardew Constants.SaveFolderName。
    Constants.SaveFolderName 为空 → world_id 为空 → Resolve 失败 → pre-turn rejected。
    不允许用 UniqueMultiplayerID、session_id 或其他临时值兜底，否则破坏身份不变量。

实施验证：
    真机确认 Constants.SaveFolderName 在单机 / 联机主机 / 联机客机 /
    标题页切换与存档切换路径下稳定可用。
    若某路径下为空，保持 world_id 为空并 pre-turn rejected；
    记录为 Known Limitation，不恢复 UniqueMultiplayerID 兜底。

已知边界：
    Phase3 假设 active Turn 生命周期内 WorldScope 不切换。
    若玩家在 active Turn 期间退出 Save_A 并加载 Save_B，
    旧 Turn 的严格 cancel / Environment replacement / reconnect lifecycle 留到 Phase7。
```

## 13.2 Stardew NPC 身份稳定性

```text
Stardew 原版 NPC 名称在同一存档内稳定。
mod NPC / 玩家改名等边缘情况可能改变 entity_id 语义。
Phase3 记录为 Known Limitation，不处理；
如未来出现实际需求，回到 Identity Contract 决策。
```

## 13.3 goroutine 数量

```text
每 ExecutionLane 一个 worker goroutine，数量约等于当前 EnvironmentSession 内历史访问过的 AgentSessionKey 数。
LaneStore 在一个 EnvironmentSession 内不回收已创建 lane；
跨多个 WorldScope 切换后，lane 数量不是仅当前 world 的 NPC 数量。
单游戏内历史触达 NPC 数量仍有界（几十到低百量级），Phase3 不引入全局 maxSessions / idle eviction。
如果未来 Adapter 输入不可信或需要全局容量控制，由 Trigger Admission / Phase7 持久化方案统一处理。
不做跨进程 / 跨机器扩展（Phase7 范围）。
```

## 13.4 Protocol change

```text
Phase3 明确升级到 gameagent.protocol.v1alpha2：
    package gameagent.protocol.v1alpha2
    go_package gameagent/protocol/gen/go/gameagent/protocol/v1alpha2;protocolv1alpha2
    csharp_namespace GameAgent.Protocol.V1Alpha2

v1alpha2 Protocol contract：
    AdapterHello 不携带 save_id / world_id。
    GameEvent 携带 world_id + target_entity_id。
    ObserveRequest 携带 world_id + entity_id。
    Observation 回传 world_id。
    ActionRequest 携带 world_id + entity_id。
    EventAck(ACCEPTED) 是 runtime admission ack，不是 durable persistence ack。
    EventAck(DUPLICATE) 是当前 EnvironmentSession 内 event_id 去重结果。
    GameEvent.sequence 是 EnvironmentSession-scoped monotonic sequence，切换 world 不重置。

不保留 v1alpha1 兼容 shim：
    Phase3 只有 Runtime + Stardew Adapter 两端需要同步升级。
    旧 save_id 字段不与 world_id 并存，避免双来源身份漂移。

必须同步：
    docs/phase3/GameAgent Protocol v1alpha2 设计规范.md
    protocol/proto/gameagent.proto
    protocol/gen/go/*
    protocol/tests/check-protocol-static.ps1
    adapters/stardew/**/*.csproj 的 Protobuf / Grpc.Tools 构建入口
    Adapter C# using GameAgent.Protocol.V1Alpha1 → GameAgent.Protocol.V1Alpha2
    Runtime gateway tests
    Stardew ProtocolMapper tests
    Stardew world_mismatch handling

说明：
    C# 不存在 protocol/gen/csharp/* 手工同步目录；
    Stardew Adapter 通过 Grpc.Tools 在构建时从 proto 生成 C# 代码。

Adapter 不再通过 payload 声明目标实体；payload target_entity_id 若存在，Runtime 忽略。
```

------

# 14. 下一阶段建议（Phase4 入口）

Phase3 完成后，进入 Phase4 的前置已经满足：

```text
Agent Identity Contract Accepted
    → Memory 有了稳定的 key 基础。

确定性测试夹具的雏形
    → Phase4 把 fake adapter 收敛为可复用测试夹具
       （多 Entity / 多 Turn / Memory 隔离 / 失败路径）。

无 Stardew 泄漏的 Runtime
    → Memory 验证不会与游戏耦合混淆。
```

Phase4 建议按 Roadmap v0.3 执行：

```text
Context 与短期 Memory
+ 最小确定性测试底座（从 fake adapter 演进）
```

------

# 15. 一句话总结

> Phase3 的核心不是加功能，而是把 Runtime 从"绑定一个 NPC 的 demo"升级为"绑定稳定 Agent 身份的通用多实体 Runtime"：身份先行、路由显式、串行有界，为 Phase4 的 Memory 和后续所有阶段提供稳定的身份地基。
