# GameAgent Protocol v1alpha2 设计规范

> Status: Phase3 Protocol Baseline
> Date: 2026-08-20
> Scope: Environment Protocol contract for Phase3 Runtime + Stardew Adapter
> Related Plan: [GameAgent MVP0 Phase3 技术开发与验收方案](./GameAgent%20MVP0%20Phase3%20技术开发与验收方案.md)

------

# 1. 文档定位

本文档冻结 Phase3 使用的 `gameagent.protocol.v1alpha2` 语义。

Phase3 技术方案回答：

```text
为什么要从 v1alpha1 升级到 v1alpha2，以及开发顺序如何安排。
```

本文档回答：

```text
v1alpha2 协议本身是什么，Runtime 与 Adapter 必须共同遵守哪些 contract。
```

本文档不定义 Runtime 内部实现细节，例如 `AgentSessionKey`、`ExecutionLane`、Turn trace 存储结构或模型请求格式。

------

# 2. Version 与生成目标

```proto
package gameagent.protocol.v1alpha2;

option go_package = "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2;protocolv1alpha2";
option csharp_namespace = "GameAgent.Protocol.V1Alpha2";
```

Phase3 不保留 v1alpha1 兼容 shim。

```text
原因：
    Phase3 只有 Runtime + Stardew Adapter 两端需要同步升级。
    v1alpha1 的 save_id / AdapterHello scope 语义会与 v1alpha2 的 world_id message scope 冲突。
```

C# 侧不提交 `protocol/gen/csharp/*`。Stardew Adapter 通过 `Grpc.Tools` 在构建时从 `protocol/proto/gameagent.proto` 生成 C# 类型。

------

# 3. 核心 Scope

## 3.1 EnvironmentSession

```text
EnvironmentSession = 一条 live Connect stream。
```

Phase3 支持的拓扑：

```text
一个 Runtime process 同一时刻只支持一个 live Connect stream。
```

EnvironmentSession 不是 AgentSession，也不参与长期身份。

```text
EnvironmentSession 可以经历：
    title screen
    Save_A
    title screen
    Save_B

但 Phase3 不做 reconnect / stream replacement / durable queue。
这些能力留到 Phase7。
```

## 3.2 GameScope

```text
GameScope = AdapterHello.game_id
```

示例：

```text
game_id = "stardew-valley"
```

`game_id` 是连接级稳定信息。

## 3.3 WorldScope

```text
WorldScope = world_id
```

`world_id` 不属于 `AdapterHello`，必须随 world-scoped message 传递：

```text
GameEvent.world_id
ObserveRequest.world_id
Observation.world_id
ActionRequest.world_id
```

Stardew Phase3 的唯一允许来源：

```text
Constants.SaveFolderName
```

禁止使用 `UniqueMultiplayerID`、`session_id` 或其他临时值兜底。

## 3.4 StableEntityIdentity

```text
StableEntityIdentity = Adapter 提供的 opaque entity_id。
```

Runtime 不解析 `entity_id` 的游戏语义。Stardew 当前使用：

```text
npc:Abigail
player:local
```

------

# 4. Message Contract

## 4.0 Outer Message Envelope

v1alpha2 的 gRPC 入口仍是双向流：

```proto
service GameAgentGateway {
  rpc Connect(stream AdapterMessage) returns (stream RuntimeMessage);
}
```

Adapter → Runtime：

```proto
message AdapterMessage {
  string message_id = 1;
  string correlation_id = 2;

  oneof payload {
    AdapterHello hello = 10;
    GameEvent event = 11;
    Observation observation = 12;
    CapabilityList capabilities = 13;
    ActionStatusUpdate action_status = 14;
    ActionResult action_result = 15;
    Heartbeat heartbeat = 16;
    Error error = 17;
  }
}
```

Runtime → Adapter：

```proto
message RuntimeMessage {
  string message_id = 1;
  string correlation_id = 2;

  oneof payload {
    EnvironmentReady environment_ready = 10;
    ObserveRequest observe = 11;
    CapabilityRequest capability_request = 12;
    ActionRequest action = 13;
    CancelActionRequest cancel_action = 14;
    EventAck event_ack = 15;
    Error error = 16;
  }
}
```

Envelope 约束：

```text
message_id 标识本条协议消息。
correlation_id 标识对哪条对端消息的响应。

ObserveRequest：
    RuntimeMessage.message_id 是 request id。
    AdapterMessage.correlation_id 必须等于该 request id。

CapabilityRequest：
    RuntimeMessage.message_id 是 request id。
    AdapterMessage.correlation_id 必须等于该 request id。

ActionRequest：
    RuntimeMessage.message_id 标识协议消息；
    ActionResult / ActionStatusUpdate 通过 action_id 关联业务动作。

EventAck：
    RuntimeMessage.correlation_id SHOULD 等于对应 AdapterMessage.message_id。
    EventAck.event_id MUST 等于 GameEvent.event_id。
```

`ActionStatusUpdate`、`Heartbeat`、`CancelActionRequest` 在 v1alpha2 保留。Phase3 不扩展它们的字段语义，只确保 v1alpha1 已验证的链路不会被 v1alpha2 删除。

## 4.1 AdapterHello

```proto
message AdapterHello {
  string adapter_id = 1;
  string adapter_version = 2;
  string protocol_version = 3; // "v1alpha2"
  string game_id = 4;
  string game_version = 5;
  string session_id = 6;
}
```

约束：

```text
MUST NOT 携带 save_id。
MUST NOT 携带 world_id。
session_id 只描述本次 Adapter 运行 / 连接会话，不参与 Agent identity。
```

## 4.2 GameEvent

```proto
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
```

约束：

```text
event_id MUST be non-empty.
world_id MUST be non-empty for world-scoped gameplay events.
target_entity_id MUST be non-empty for routed trigger events.
target_entity_id MUST reference one EntityRef.entity_id in entities.
payload MUST NOT be used as Runtime routing source.
```

`target_entity_id` 是 trigger target pointer，不是 AgentSession id。

```text
Runtime 使用 target_entity_id 选择本次事件归属实体。
Runtime 不使用 entities 列表顺序或 entity_type 字符串猜测目标。
```

`sequence` 语义：

```text
EnvironmentSession-scoped monotonic sequence.
同一 Connect stream 内切换 WorldScope 不重置。
```

## 4.3 EventAck

```proto
enum EventAckStatus {
  EVENT_ACK_STATUS_UNSPECIFIED = 0;
  EVENT_ACK_STATUS_ACCEPTED = 1;
  EVENT_ACK_STATUS_DUPLICATE = 2;
  EVENT_ACK_STATUS_REJECTED = 3;
}

message EventAck {
  string event_id = 1;
  EventAckStatus status = 2;
  Error error = 3;
}
```

`ACCEPTED`：

```text
Runtime 已完成 runtime admission。
事件已有 queue capacity。
事件会在当前 EnvironmentSession 生命周期内 start AgentTurn
或记录 event_aborted_before_turn。

ACCEPTED 不代表：
    Turn 已完成
    Action 已执行
    event 已持久化
    crash / reconnect 后可恢复
```

`DUPLICATE`：

```text
当前 EnvironmentSession 内已经接纳过相同 event_id。
Runtime 不创建新 Turn。
Runtime 不重新执行副作用。
DUPLICATE 不跨 reconnect / Runtime restart。
```

`REJECTED`：

```text
Runtime 未接纳事件。
不会创建 Turn。
不会记录 seenEventID。
Adapter 可按自身策略记录、忽略或稍后重新发送新事件。
```

Phase3 EventAck error code：

| Code | Meaning |
| --- | --- |
| `event_id_missing` | `GameEvent.event_id` 为空 |
| `unsupported_event_type` | `event_type` 不属于 Phase3 支持的 trigger |
| `target_entity_missing` | `target_entity_id` 为空 |
| `target_entity_not_in_event` | `target_entity_id` 不在 `entities` 内 |
| `identity_scope_missing` | `game_id` / `world_id` / `entity_id` 无法组成 identity |
| `session_queue_full` | 目标 AgentSession lane 没有剩余队列容量 |
| `environment_closed` | Connect stream 正在关闭，Runtime 已停止 admission |

## 4.4 ObserveRequest

```proto
message ObserveRequest {
  string entity_id = 1;
  string world_id = 2;
}
```

Runtime 发送 ObserveRequest 时：

```text
RuntimeMessage.message_id = observe request id
ObserveRequest.entity_id = AgentSessionKey.EntityID
ObserveRequest.world_id = AgentSessionKey.WorldID
```

Adapter 回复成功 Observation 时：

```text
AdapterMessage.correlation_id = RuntimeMessage.message_id
AdapterMessage.observation = Observation(...)
```

Adapter 回复失败 Error 时：

```text
AdapterMessage.correlation_id = RuntimeMessage.message_id
AdapterMessage.error.code = world_mismatch 或其他 observe error
```

Runtime 必须允许 correlated `Error` 解除 pending Observe。

## 4.5 Observation

```proto
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

Runtime 收到 Observation 后必须校验：

```text
Observation.world_id == ObserveRequest.world_id
Observation.entity_id == ObserveRequest.entity_id
```

不匹配时：

```text
Environment.Observe 返回 error。
Turn 收敛为 turn_failed(stage=observe, reason=observation_scope_mismatch)。
```

pending Observe 被 Observation 或 Error 解除后，Runtime 必须删除 waiter。迟到的 Observation / Error 命中不到 waiter 时忽略，不二次 resolve。

## 4.6 ActionRequest

```proto
message ActionRequest {
  string action_id = 1;
  string entity_id = 2;
  string capability = 3;
  google.protobuf.Struct arguments = 4;
  google.protobuf.Struct extensions = 5;
  string world_id = 6;
}
```

Runtime 发送 ActionRequest 时：

```text
ActionRequest.action_id = globally unique action id
ActionRequest.entity_id = AgentSessionKey.EntityID
ActionRequest.world_id = AgentSessionKey.WorldID
```

Adapter 执行前必须校验：

```text
ActionRequest.world_id == currentWorldId
```

不匹配时：

```text
ActionResult.status = REJECTED
ActionResult.error.code = world_mismatch
Adapter MUST NOT 执行动作副作用。
```

## 4.7 ActionResult / ActionStatusUpdate

Phase3 不给 `ActionResult` / `ActionStatusUpdate` 增加 `world_id`。

```text
原因：
    ActionResult / ActionStatusUpdate 通过 action_id 关联原始 ActionRequest。
    world mismatch 必须在 Adapter 执行前通过 ActionRequest.world_id 检查。
```

## 4.8 CapabilityRequest / CapabilityList

Phase3 不给 CapabilityRequest / CapabilityList 增加 `world_id`。

```text
Phase3 capability 是 Environment-level。
不做 world-specific capability。
不做 entity-specific capability。
```

## 4.9 Heartbeat

```proto
message Heartbeat {
  int64 timestamp_unix_ms = 1;
  uint64 last_event_sequence = 2;
}
```

Heartbeat 在 v1alpha2 保留，但 Phase3 不强制 Adapter 发送 Heartbeat。

```text
Heartbeat.last_event_sequence 沿用 GameEvent.sequence 的 scope：
    EnvironmentSession-scoped monotonic sequence。
    同一 Connect stream 内切换 WorldScope 不重置。
```

------

# 5. Event Delivery

Runtime 在当前 EnvironmentSession 内维护：

```text
seenEventIDs map[string]struct{}
```

处理顺序：

```text
1. event_id 为空
   → EventAck(REJECTED, event_id_missing)

2. event_id 已在 seenEventIDs
   → EventAck(DUPLICATE)

3. event_type admission
   → unsupported 则 EventAck(REJECTED, unsupported_event_type)

4. target_entity_id / world_id / identity 解析
   → 失败则 EventAck(REJECTED, 对应 error code)

5. lane queue admission
   → 满则 EventAck(REJECTED, session_queue_full)

6. admission 成功
   → 记录 seenEventIDs[event_id]
   → EventAck(ACCEPTED)
   → release admitted barrier
```

Phase3 不保证：

```text
跨 reconnect 去重
跨 Runtime restart 去重
durable event replay
Adapter pending queue recovery
```

------

# 6. Turn Start Boundary

`turn_started` 是 Event lifecycle 和 Turn lifecycle 的唯一分界点。

```text
task 仍在 queue
    → stream close / drain
    → event_aborted_before_turn

task 已 dequeue，但尚未 emit turn_started
    → stream close / ctx cancel
    → event_aborted_before_turn

已 emit turn_started
    → 后续 observe / action / stream failure
    → turn_failed 或 turn_completed
```

`EventAck(ACCEPTED)` 之后禁止静默丢失：

```text
ACCEPTED event 必须最终 start Turn 或记录 event_aborted_before_turn。
```

`EventAck(DUPLICATE)` 不创建 Turn，也不要求 abort 记录。

------

# 7. Disconnect / Reconnect 当前保证

Phase3：

```text
支持 stream disconnect 后 pending Observe / Action 被唤醒。
支持 active Turn 按 Phase2 failure path 收敛。
支持 queued but not started event 记录 event_aborted_before_turn。
```

Phase3 不支持：

```text
自动 reconnect
EnvironmentSession replacement
lane rebind
durable queue
跨 stream duplicate detection
跨 process replay
```

这些属于 Phase7。

------

# 8. Non-goals

v1alpha2 Phase3 不引入：

```text
AgentSessionKey protocol field
ExecutionLane protocol field
Memory protocol
Goal protocol
Permission protocol
world-specific CapabilityList
entity-specific CapabilityList
durable event replay
multi-environment instance identity
```

------

# 9. Contract Tests

Phase3 至少覆盖：

```text
Protocol static check：
    package / go_package / csharp_namespace 为 v1alpha2。
    AdapterMessage / RuntimeMessage envelope oneof 分支与规范一致。
    AdapterHello 不包含 save_id / world_id。
    GameEvent 包含 world_id / target_entity_id。
    ObserveRequest 包含 world_id。
    Observation 包含 world_id。
    ActionRequest 包含 world_id。
    Heartbeat 保留，last_event_sequence 使用 EnvironmentSession-scoped sequence。

Gateway tests：
    missing event_id → REJECTED(event_id_missing)
    duplicate event_id in same EnvironmentSession → DUPLICATE
    duplicate event_id after reconnect / new stream 不保证去重
    ACCEPTED 后 ACK 先于 ObserveRequest
    correlated Error(world_mismatch) 解除 pending Observe
    late Observation after correlated Error 被忽略
    Observation scope mismatch → observation_scope_mismatch
    ActionRequest world mismatch → ActionResult(REJECTED, world_mismatch)

Adapter tests：
    ProtocolMapper 设置 GameEvent.world_id / target_entity_id。
    payload 不写入 target_entity_id。
    RuntimeClient Observe / Action 执行前校验 request.world_id。
```
