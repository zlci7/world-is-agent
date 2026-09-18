# GameAgent MVP0 Phase2 Trace 链路观测设计

> Scope：Runtime 内部 Agent Turn trace，不改变 GameAgent Protocol v1alpha1。

---

# 1. 目标

Phase2 trace 的目标不是做故障排查平台，而是给 GameAgent Runtime 一个轻量的 turn timeline。

它需要回答：

```text
这一轮 Agent Turn 从哪个 GameEvent 开始？
中间经历了哪些关键步骤？
如果失败，失败发生在哪个阶段？
```

同时必须遵守游戏 LLM 的性能约束：

```text
Trace 可以丢弃。
Trace 可以降级。
Trace 不能阻塞 AgentLoop。
Trace 不能拖慢游戏交互响应。
```

因此 Phase2 的结论是：

```text
实现轻量、非阻塞、可丢弃的 JSONL turn timeline。
不做完整 tracing 平台。
不做 Web viewer。
不做 OpenTelemetry。
```

---

# 2. 默认记录形态

## 2.1 JSONL recorder

Phase2 默认将 trace event 写入 JSONL 文件。

默认路径：

```text
runtime/.local/traces.jsonl
```

每条 event 是一行完整 JSON：

```json
{"schema_version":1,"trace_id":"turn_123","turn_id":"turn_123","seq":5,"event":"tool_call_selected","time":"2026-08-17T10:01:02.123456789+08:00","elapsed_ms":42,"entity_id":"npc:Linus","tool":"emote"}
```

选择 JSONL 的原因：

```text
可持久化。
可 grep。
可用脚本分析。
未来 viewer 可以直接消费。
一行一事件，写入模型简单。
```

## 2.2 stdout recorder

stdout recorder 只作为本地 debug 选项，不作为 Phase2 默认实现。

原因：

```text
终端输出会污染 Runtime 普通运行日志。
终端输出不方便查询和回放。
用户已经可以通过 SMAPI 控制台看 adapter send/recv。
Runtime trace 更适合落文件。
```

## 2.3 viewer

viewer 是展示层，不是记录层。

Phase2 不做 viewer，只保证 JSONL 数据结构未来可以被 viewer 消费。

---

# 3. ID 语义

Trace 设计中各类 ID 的作用域必须分清：

```text
game_id      游戏类型，例如 stardew-valley。
save_id      当前存档 ID；未加载存档时允许为空。
session_id   当前 Adapter 运行/连接会话 ID。
event_id     Adapter 上报的游戏事件 ID。
event_type   Adapter 上报的游戏事件类型。
turn_id      Runtime 为一次有效 Agent Turn 生成的 ID。
trace_id     trace 关联 ID。Phase2 令 trace_id == turn_id。
message_id   gRPC 单条消息 ID。
action_id    ActionRequest / ActionResult 匹配 ID。
entity_id    本轮 Agent 操作目标，例如 npc:Linus。
```

边界：

```text
game_id / save_id / session_id 是环境上下文。
turn_id / trace_id 是本轮 Agent Turn 上下文。
message_id 是协议消息上下文。
action_id 是动作执行上下文。
```

不要用 `message_id` 或 `action_id` 表示完整 turn。

来源：

```text
game_id
    来自 AdapterHello.GameId，由 gateway.Connect 在握手阶段保存。

session_id
    来自 AdapterHello.SessionId，由 gateway.Connect 在握手阶段保存。

save_id
    来自进入 AgentLoop 的有效 GameEvent.SaveId。

event_id / event_type / entity_id
    来自进入 AgentLoop 的有效 GameEvent。

turn_id / trace_id
    由 Runtime 在创建 TurnTracer 时生成。
```

AgentLoop 当前不能只靠 `GameEvent` 推导完整环境上下文：`game_id / session_id` 属于连接级上下文，`save_id` 属于事件发生时的存档上下文。实现时应由 gateway 保存 connection context，并在调用 AgentLoop / 创建 TurnTracer 时注入 `game_id / session_id`，由 GameEvent 注入 `save_id`。

协议兼容性说明：

```text
AdapterHello.instance_id -> session_id 是 v1alpha breaking rename。
Runtime 与 Adapter 必须同步升级。
后续开放第三方 Adapter 时，应使用新 tag + deprecated 旧字段，而不是复用 tag。
```

Phase2 的简化规则：

```text
一个有效 GameEvent -> 一个 turn_id
trace_id == turn_id
```

GameAgent Turn 的语义：

```text
GameAgent Turn 是一个有效 GameEvent 触发的一次完整 AgentLoop。

未来 ReAct 的多次 model -> tool/action 循环属于同一个 GameAgent Turn 下的多个 step。

届时通过 step_index / tool_call_id / attempt 等字段扩展，不重新定义 turn_id。
```

---

# 4. 有效事件与 Turn 创建

所谓有效 GameEvent 是指：

```text
通过事件类型过滤。
能找到目标 entity。
真正进入 AgentLoop 的事件。
```

被忽略的事件不创建 `turn_id`，也不进入 turn trace。

如果后续需要观测 ignored event，可以用普通 debug log 或独立 event log，不混入 turn timeline。

---

# 5. Trace Event Schema

Phase2 第一版必须固定 JSON schema，避免后续 viewer、脚本和测试依赖不稳定字段。

建议类型：

```go
type EventName string

type Fields map[string]any

type EventData struct {
    ActionID string
    Tool     string
    Fields   Fields
}

const (
    EventTurnStarted           EventName = "turn_started"
    EventObservationRequested  EventName = "observation_requested"
    EventObservationReceived   EventName = "observation_received"
    EventModelRequestStarted   EventName = "model_request_started"
    EventModelResponseReceived EventName = "model_response_received"
    EventToolCallSelected      EventName = "tool_call_selected"
    EventActionSubmitStarted   EventName = "action_submit_started"
    EventActionResultReceived  EventName = "action_result_received"
    EventTurnCompleted         EventName = "turn_completed"
    EventTurnFailed            EventName = "turn_failed"
)

type Event struct {
    SchemaVersion int       `json:"schema_version"`
    TraceID       string    `json:"trace_id"`
    TurnID        string    `json:"turn_id"`
    Seq           uint32    `json:"seq"`
    Event         EventName `json:"event"`
    Time          time.Time `json:"time"`
    ElapsedMS     int64     `json:"elapsed_ms"`

    GameID    string `json:"game_id,omitempty"`
    SaveID    string `json:"save_id,omitempty"`
    SessionID string `json:"session_id,omitempty"`
    EventID   string `json:"event_id,omitempty"`
    EventType string `json:"event_type,omitempty"`
    EntityID  string `json:"entity_id,omitempty"`

    ActionID string `json:"action_id,omitempty"`
    Tool     string `json:"tool,omitempty"`

    Stage        string `json:"stage,omitempty"`
    Reason       string `json:"reason,omitempty"`
    ErrorMessage string `json:"error,omitempty"`

    Fields Fields `json:"fields,omitempty"`
}
```

字段原则：

```text
固定关联字段放顶层。
阶段特有的小字段放 Fields。
不记录完整 prompt。
不记录完整 completion。
不记录完整 observation。
不记录 request header。
不记录 API key。
error 文本要截断，建议上限 2 KB。
```

`EventData` 用于 `TurnTracer.Emit / Complete / Fail` 的入参：

```text
ActionID / Tool 是常见动态顶层字段。
Fields 只放阶段特有的小型扩展字段。
```

`Fields` 所有权规则：

```text
调用 Emit / Complete / Fail / Record 后，Fields 所有权转移给 trace 系统。
调用方不得继续修改或复用同一个 Fields map。
Fields 只放 string / bool / number / nil 等小型 JSON 值。
MVP0 不放嵌套 map、可变 slice、指针、大对象、自定义结构。
Record 不对 Fields 做深拷贝，避免热路径增加复制开销。
```

---

# 6. 顺序与耗时

Trace 需要区分两层语义：

```text
TurnTracer 发射语义。
JSONL recorder 落盘语义。
```

TurnTracer 发射语义：

```text
seq 从 1 开始，单 turn 内连续递增。
Complete / Fail 只允许成功一次。
正常路径必须发射一个终态。
```

JSONL recorder 落盘语义：

```text
JSONL 是 best effort。
已落盘的同一 turn 事件按 seq 严格递增。
因为队列满、写入失败或进程崩溃，落盘结果允许缺事件、缺 seq、缺终态。
没有 drop / 写入失败 / 崩溃时，JSONL 应包含完整 timeline 和唯一终态。
```

每个 turn event 内必须有：

```text
seq
elapsed_ms
```

含义：

```text
seq
    TurnTracer 发射顺序，从 1 开始。

elapsed_ms
    从 turn_started 到当前事件的耗时，使用单调时钟计算。

time
    用于跨 turn / 跨系统观察，使用 wall clock。
```

为什么需要 `seq`：

```text
多个事件可能有相同时间精度。
异步 recorder 写入顺序可能与事件产生顺序不同。
以后如果加队列，不能只靠时间戳排序。
```

---

# 7. Turn 终态语义

TurnTracer 发射层每个 turn 必须且只能有一个终态，并且终态必须是最后一个发射事件：

```text
turn_completed XOR turn_failed
terminal event is final event
```

建议通过 Turn 级 tracer 保证：

```go
type TurnContext struct {
    GameID    string
    SaveID    string
    SessionID string
    EventID   string
    EventType string
    EntityID  string
}

type TurnTracer interface {
    Emit(name EventName, data EventData)
    Complete(data EventData)
    Fail(stage string, reason string, err error, data EventData)
}
```

`Complete` 和 `Fail` 内部使用轻量状态位，保证终态只写一次，并且关闭后续发射。

```text
Complete / Fail 成功后：
    后续 Emit / Complete / Fail 全部 no-op。

因此不会出现：
    seq=8 action_result_received
    seq=9 turn_completed
    seq=10 tool_call_selected
```

实现上可以用 mutex + closed 状态，而不是只依赖 `sync.Once`。`sync.Once` 只能阻止第二个终态，不能阻止终态后的普通 `Emit`。

TurnTracer 创建时绑定固定上下文：

```text
game_id
save_id
session_id
turn_id
trace_id
event_id
event_type
entity_id
start_time
seq
```

`Emit / Complete / Fail` 只传阶段动态字段。`EventData.ActionID`、`EventData.Tool` 会进入 Event 顶层；`EventData.Fields` 保持为扩展字段。`stage`、`reason` 由 `Fail` 参数进入 Event 顶层。`Fail` 的 `err` 允许为 nil。

ActionResult 语义：

```text
收到 ActionResult：
    始终记录 action_result_received

ActionResult.status == SUCCEEDED：
    turn_completed

ActionResult.status != SUCCEEDED：
    turn_failed
        stage=action
        reason=action_result_failed
        fields.action_status=FAILED / REJECTED / CANCELED
```

---

# 8. 标准成功链路

Phase2 成功 turn 的标准 timeline：

```text
turn_started
observation_requested
observation_received
model_request_started
model_response_received
tool_call_selected
action_submit_started
action_result_received
turn_completed
```

说明：

```text
turn_started 携带 event_id / event_type / entity_id。
model_request_started 在 model.Request 构造完成后、调用 Provider.Generate 前记录。
不再单独记录 event_received。
action_submit_started 在调用 env.SubmitAction 前记录。
```

`model_request_started` 表示真正的 Provider 调用边界：

```text
model_response_received.elapsed_ms - model_request_started.elapsed_ms
```

可以近似作为本次模型调用耗时。

`action_submit_started` 放在调用前，是为了在 SubmitAction 卡住或超时时，也能看出 Runtime 已进入动作提交阶段。

---

# 9. 失败链路

任意阶段失败都应记录 `turn_failed`。

示例：

```json
{"event":"turn_failed","stage":"observation","reason":"observation_failed","error":"npc not found: Linus"}
```

```json
{"event":"turn_failed","stage":"model","reason":"provider_timeout","error":"context deadline exceeded"}
```

```json
{"event":"turn_failed","stage":"tool","reason":"tool_call_invalid","tool":"unknown","error":"tool not registered: unknown"}
```

```json
{"event":"turn_failed","stage":"action","reason":"action_result_failed","fields":{"action_status":"REJECTED"}}
```

失败要求：

```text
必须带 turn_id / trace_id。
尽量带 entity_id / tool / action_id。
必须带 stage。
必须带 reason。
error 只在存在底层技术错误时记录。
不要为了合法业务失败伪造 error。
Adapter 返回的业务失败细节放入 Fields，例如 action_status / action_reason。
不能 panic。
不能阻塞后续 GameEvent。
```

---

# 10. Recorder 性能契约

这是本设计最重要的约束。

Trace 是观测能力，不是游戏主链路能力。

因此：

```text
Record 必须是非阻塞或近似非阻塞。
Record 不做 JSON encode。
Record 不做 file.Write。
Record 不等待磁盘 IO。
Record 不使用 turn ctx。
队列满时丢弃 trace event。
丢弃 trace 不影响 AgentLoop。
```

推荐实现：

```text
AgentLoop / TurnTracer
    ↓ O(1) 构造 Event
JSONLRecorder.Record
    ↓ non-blocking enqueue
bounded channel
    ↓
single writer goroutine
    ↓
json.Encoder.Encode(event)
    ↓
buffered writer / file
```

队列满时：

```text
drop event
atomic increase dropped_event_count
Record 热路径不做日志、不做格式化、不做时间检查
never block AgentLoop
```

writer goroutine 或低频后台逻辑可以聚合输出 dropped_event_count。`Close` 可以返回或记录最终 dropped count，但不能在 `Record` 热路径里打印每次 drop。

正常退出时：

```go
Close(ctx context.Context) error
```

`Close` 用于 drain queue 和 flush writer。进程异常退出时允许丢失尾部少量 trace。JSONL writer 可以每写一条 event 后 `Flush` 到 buffered writer / file，但 MVP0 不做 `fsync`。

---

# 11. Recorder 接口

建议接口：

```go
type Recorder interface {
    Record(event Event)
    Close(ctx context.Context) error
}
```

为什么 `Record` 不接收 `ctx`：

```text
turn ctx 取消时，最需要记录的往往是 turn_failed。
如果 recorder 服从已取消的 turn ctx，最终失败事件可能写不进去。
```

为什么 `Record` 不返回 error：

```text
写 trace 失败不能让 Agent Turn 失败。
构造 recorder 时的错误应该由 NewJSONLRecorder 返回。
运行期写入错误由 recorder 内部降级处理。
```

构造函数：

```go
func NewJSONLRecorder(path string, options Options) (*JSONLRecorder, error)
```

构造时应先执行：

```go
os.MkdirAll(filepath.Dir(path), 0o755)
```

MVP0 默认降级策略：

```text
NewJSONLRecorder 失败时，Runtime 写一次普通日志，然后使用 NoopRecorder。
不因为 trace 文件不可写导致 Runtime 启动失败。
未来如果加入 trace.required=true，再允许启动失败。
```

`Close` 生命周期：

```text
Runtime 退出时先停止接收新事件 / 等待 AgentLoop 收敛，再 Close recorder。
Close 尽量 drain queue，然后 Flush。
Close 必须幂等。
Close 后调用 Record 直接丢弃事件。
实现时避免向已关闭 channel send。
```

---

# 12. 文件与进程约束

MVP0 简化约束：

```text
一个 traces.jsonl 只允许一个 Runtime 进程写入。
traces.jsonl 是派生的 best-effort 观测数据，不是 Runtime 或 Agent 的 source of truth。
不得依赖 traces.jsonl 恢复 Turn、恢复 Agent 状态或恢复游戏状态。
不做文件轮转。
不做跨进程文件锁。
不做 fsync。
不保证进程崩溃前最后几条 trace 一定落盘。
```

`schema_version` 演进规则：

```text
新增可选字段，不 bump schema_version。
新增 EventName，不 bump schema_version。
删除字段、重命名字段、改变字段语义，需要 bump schema_version。
```

后续可扩展：

```text
trace.path
trace.max_file_size
trace.max_files
traces-{game_id}-{save_id}-{pid}.jsonl
```

---

# 13. AgentLoop 接入点

连接级上下文来源：

```text
gateway.Connect 收到 AdapterHello 后保存：
    game_id    = AdapterHello.GameId
    session_id = AdapterHello.SessionId

之后每次调用 AgentLoop.HandleEvent 时，把该 connection context 一起传入，或通过 EnvironmentSession 注入。

AgentLoop 只负责 turn 内字段，不负责解析 AdapterHello；save_id 来自触发本轮 turn 的 GameEvent。
```

AgentLoop 打点位置：

```text
事件过滤、目标 entity 确认后：
    turn_started

调用 env.Observe 前：
    observation_requested

env.Observe 成功后：
    observation_received

构造 model.Request 后、调用 Provider 前：
    model_request_started

Provider.Generate 成功后：
    model_response_received

ValidateToolCall 成功后：
    tool_call_selected

调用 env.SubmitAction 前：
    action_submit_started

收到 ActionResult 后：
    action_result_received
    turn_completed 或 turn_failed
```

如果任一步返回 error：

```text
turn_failed
return error
```

接入 trace 后，应删除现有 loop 中的 `fmt.Printf("%s once loop end")` 占位日志，由 `action_result_received` 和 `turn_completed` 取代。

---

# 14. MVP0 Invariants

Phase2 P0 明确采用单步 turn：

```text
一轮 turn 只调用一次模型。
模型只选择一个 tool。
一个 tool call 只产生一个 ActionRequest。
一轮 turn 只有一个 ActionResult。
一轮 turn 只有一个终态。
```

因此 P0 不引入：

```text
tool_call_id
step_index
attempt
multi-action timeline
retry timeline
```

这些字段等进入 ReAct、多 tool call、重试或异步动作时再补。

这里的 `turn_id` 不等同于 p-agent 中“一次 LLM response + tool calls”的 turn。GameAgent 的 `turn_id` 始终表示一次有效 GameEvent 的完整处理链路，后续多次模型调用只在同一 turn 内增加 step 维度。

---

# 15. Observer 与 Hook 边界

Trace Recorder 属于 best-effort Observer：

```text
Observer 不参与 AgentLoop 状态一致性。
Observer 不允许产生背压。
Observer 失败不改变 AgentLoop 主结果。
```

未来如果引入会改变 Agent 行为的扩展点，应使用另一类 Hook：

```text
Hook 可以 await。
Hook 可以拒绝或改写行为。
Hook 可以影响 AgentLoop 结果。
```

Phase2 只实现 Trace Observer，不实现通用 EventBus，也不实现功能性 Hook。

---

# 16. MVP0 不做什么

Phase2 trace 第一版不做：

```text
不做 Web viewer。
不默认输出 trace 到终端。
不做分布式 tracing。
不引入 OpenTelemetry。
不做完整故障排查平台。
不记录完整 prompt / completion / observation。
不改变 GameAgent Protocol。
不要求 Adapter 理解 trace_id。
不把 traces.jsonl 当作 session / state source。
不做通用 EventBus。
不做功能性 Hook。
不做文件轮转。
不做跨进程写同一 trace 文件。
```

---

# 17. 验收标准

代码级验收：

```text
Event JSON 字段使用 snake_case。
Event 包含 schema_version。
成功 turn 写入标准 timeline。
标准 timeline 使用 model_request_started，不使用 model_request_built。
TurnTracer 发射的同一 turn seq 从 1 连续递增。
JSONL 落盘的同一 turn seq 严格递增，但允许因为 drop 出现缺口。
同一 turn 内 trace_id == turn_id。
TurnTracer 每个 turn 只能发射一个终态。
TurnTracer terminal event 必须是最后一个发射事件。
Complete 后 Emit 不产生新 event。
Fail 后 Emit 不产生新 event。
Complete 后 Fail 不产生新 event。
Fail 后 Complete 不产生新 event。
JSONL 落盘中同一 turn 最多出现一个终态；没有 drop / 写入失败 / 崩溃时必须出现一个终态。
ActionResult.status != SUCCEEDED 时写 turn_failed，不写 turn_completed。
turn_failed 必须带 stage / reason，err 可以为 nil。
业务失败不伪造 error。
JSONL recorder 队列满时只做非阻塞 drop 和 atomic dropped count，不阻塞 AgentLoop。
Recorder 文件写失败不改变 AgentLoop 主结果。
traces.jsonl 不能作为 Runtime / Agent / 游戏状态恢复来源。
被忽略的 GameEvent 不创建 turn_id。
Close 会 drain / flush，且重复调用安全。
Record 在 Close 后直接丢弃事件，不 panic。
```

手动验收：

```text
启动 Runtime。
启动 Stardew Adapter。
点击 Linus。
打开 runtime/.local/traces.jsonl。
正常无 drop 时，能看到同一 turn_id 下的完整 timeline。
如果 fake provider 返回 emote，JSONL 中 tool=emote。
Runtime 终端不输出 trace timeline。
游戏响应没有明显变慢。
```

---

# 18. 建议第一步代码

先写：

```text
runtime/internal/trace/trace.go
runtime/internal/trace/turn.go
runtime/internal/trace/jsonl.go
```

第一步不要接 AgentLoop。

先完成并测试：

```text
Event JSON schema。
TurnTracer seq / elapsed_ms / terminal once。
TurnTracer terminal final no-op。
Fail nil error。
TurnTracer 与 JSONL best-effort 语义分离。
Fields 所有权规则。
JSONLRecorder non-blocking enqueue + drop count。
JSONLRecorder Close drain / flush / idempotent。
```

再接入 AgentLoop。

---

# 19. 一句话原则

> Trace 是轻量 turn timeline，不是完整诊断平台；它必须可丢弃、可降级、不可阻塞游戏链路。
