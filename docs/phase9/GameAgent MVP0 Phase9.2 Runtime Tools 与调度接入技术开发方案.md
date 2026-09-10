# GameAgent MVP0 Phase9.2 Runtime Tools 与调度接入技术开发方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-10
> **执行方式:** 每个独立审查单元先完成测试、聚焦验证、阶段回归和 `git diff --check`，再以实现及其测试创建一个本地提交并交由独立任务 CR；CR 修正使用独立 `fix:` 本地提交，通过后自动继续。Phase9.5 保留最终整分支/系统 review。
> **Goal:** 把持久 Task 接入真实 Runtime 的工具、协议、游戏时钟和 NPC lane。
> **Architecture:** 进程级 TaskService + 当前连接绑定；Runtime 工具本地执行，Environment 工具通过 ActionRequest 执行。
> **Tech Stack:** Go 1.25、gRPC / Protobuf v1alpha2、现有 Tool View / Scheduler。
> **Spec:** [Phase9 总方案](./GameAgent%20MVP0%20Phase9%20技术开发与验收方案.md)
> **前置接口:** [Phase9.1 Task 内核](./GameAgent%20MVP0%20Phase9.1%20Runtime%20Task%20内核技术开发方案.md)
> **后续:** [Phase9.3 游戏行动与交互](./GameAgent%20MVP0%20Phase9.3%20Stardew%20行动与交互技术开发方案.md)

## 1. 全局约束

- TaskSpec、ExecutionContext、Record、Evidence、Wake、CheckpointRef 使用 Phase9.1 的合同。
- 从 `e482923` 实现基点在 `codex/phase9-durable-task` 开发，保留 `daf4f98` 作为已检查代码基线；每个提交保持可构建、可测试，且包含实现及其测试。未获用户明确授权不得 `git push`。
- 仓库默认 `task.enabled=false` 保持不变；专用演示/本地配置显式启用任务功能，不提交凭据或私有本地配置。
- Runtime 不按能力名称、事件名称、Stardew state 或自然语言 description 分支。
- wake 协调与玩家事件共用现有 FIFO lane；`EnqueueMaintenance` 不承载持久任务。
- Runtime 重启可显式重新连接同一游戏 run；自动重连、backoff loop、跨连接事件补发和旧 Action 续跑归 Phase10。
- 本阶段使用 fake environment / fake model 验证协议和执行；不能把这些测试计为真实游戏验收。
- 不修改已 Accepted 的模型、Context 和 Memory token 基线；新增 Task Context 计入现有预算。

## 2. 文件边界

| 文件 | 修改 |
| --- | --- |
| `protocol/proto/gameagent.proto`、`protocol/gen/go/` | 增量字段、生成代码 |
| `protocol/tests/check-protocol-static.ps1`、`check-go-generation.ps1` | 兼容与生成校验 |
| `runtime/internal/tool/registry.go`、`runtime_tool.go`、`registry_test.go` | 新增统一 Registry、Runtime 执行上下文与执行器合同 |
| `runtime/internal/tool/types.go`、`environment_catalog.go`、`environment_tool.go` | KindRuntime、合并 Tool View、Task 来源注入 |
| `runtime/internal/task/tools.go`、`proposal.go`、`tools_test.go` | 两个本地工具、短生命周期 proposal_ref |
| `runtime/internal/task/dispatcher.go`、`dispatcher_test.go` | 扫描、认领和有界重试 |
| `runtime/internal/gateway/world_registry.go`、`task_dispatch.go`、`task_protocol.go`、`task_control.go` | 新增世界绑定、lane 入口、协议映射与 release 请求 |
| `runtime/internal/gateway/gateway.go`、`stream_environment.go` | 控制消息、证据接纳与 stream 清理 |
| `runtime/internal/agent/task_turn.go`、`task_loop_test.go` | 内部 Task trigger 与同一有界认知循环接入 |
| `runtime/internal/agent/scheduler.go`、`loop.go`、`config.go` | 按 kind 执行、状态收敛后停止、配置加载 |
| `runtime/internal/context/context.go`、`renderer.go`、`task_projection.go` | 新增最小 Task 投影；修正仅允许 Environment action 的旧通用措辞 |
| `runtime/cmd/server/main.go`、`runtime/config/agent.json` | 进程级实例、服务启停、默认任务配置 |
| `runtime/internal/gateway/task_dispatch_test.go`、`world_binding_test.go`、`task_protocol_test.go` | 新增连接与并发集成测试 |

当前 eventHandler 接口把 `*tool.EnvironmentToolCatalog` 直接传给 Loop；改为先构建通用 Registry，再向每个 Turn 提供准入后的 View。Environment bootstrap 的校验、诊断和旧 fake tests 保留。

## 3. 固定增量协议

所有字段在现有 v1alpha2 上 additive 添加。下列编号已按 `daf4f98` 核对为空闲；开发时若分支已有其他协议变更，先核对冲突，不能覆盖已有编号。

### 3.1 现有消息新增字段

| 消息 | 新字段 |
| --- | --- |
| AdapterHello | `repeated string supported_extensions = 7` |
| EnvironmentReady | `repeated string accepted_extensions = 3` |
| GameEvent | `repeated TaskEvidence task_evidence = 10`；`InteractionSource interaction_source = 11` |
| Observation | `repeated TaskEvidence task_evidence = 8` |
| ActionRequest | `TaskActionSource task_source = 9` |
| ActionResult | `TaskProposal task_proposal = 5`；`repeated TaskEvidence task_evidence = 6` |
| AdapterMessage.payload | `WorldBinding world_binding = 18`；`WorldClockUpdate world_clock = 19`；`CheckpointPrepare checkpoint_prepare = 20`；`CheckpointFinish checkpoint_finish = 21`；`TaskControlResult task_control_result = 22` |
| RuntimeMessage.payload | `WorldBindingReady world_binding_ready = 18`；`CheckpointPrepared checkpoint_prepared = 19`；`TaskControlRequest task_control = 20` |

扩展名称固定为 `gameagent.tasks.v1`。只有两端协商成功并完成 WorldBindingReady 才暴露 Task 工具；旧 Adapter / 旧 Runtime 没有扩展时继续普通链路，不发送对端未协商的任务控制消息。

### 3.2 新消息字段

以下每行的 `字段:type=编号` 是实现合同。string 状态在边界做 allowlist 校验；未知值明确拒绝。

| 消息 | 字段 |
| --- | --- |
| TaskScope | game_id:string=1；world_id:string=2；world_run_id:string=3；execution_generation:uint64=4 |
| WorldClock | clock_id:string=1；now_tick:int64=2；sequence:uint64=3 |
| TaskCheckpointRef | schema_version:uint32=1；game_id:string=2；world_id:string=3；status:string=4；checkpoint_id:string=5；checksum:string=6；reason:string=7 |
| WorldBinding | scope:TaskScope=1；clock:WorldClock=2；entities:repeated EntityRef=3；checkpoint:TaskCheckpointRef=4 |
| WorldBindingReady | scope:TaskScope=1；status:string=2；error:Error=3 |
| WorldClockUpdate | scope:TaskScope=1；clock:WorldClock=2 |
| TaskProposal | clock:WorldClock=1；wake_at:int64=2；deadline_at:int64=3；participant_entity_ids:repeated string=4；equivalence_key:string=5；payload:google.protobuf.Struct=6 |
| TaskEvidence | fact_id:string=1；task_id:string=2；operation_id:string=3；scope:TaskScope=4；task_revision:uint64=5；occurred_at:int64=6；outcome:string=7；wait_until:optional int64=8；details:google.protobuf.Struct=9；game_time:GameTime=10；context_facts:repeated ContextFact=11 |
| TaskActionSource | task_id:string=1；task_revision:uint64=2；wake_id:string=3；operation_id:string=4；scope:TaskScope=5；task_contract:TaskProposal=6 |
| InteractionSource | source_id:string=1；scope:TaskScope=2；player_entity_id:string=3；task_id:string=4；operation_id:string=5；kind:string=6 |
| TaskControlRequest | scope:TaskScope=1；task_id:string=2；operation_id:string=3；request_id:string=4；reason:string=5 |
| TaskControlResult | scope:TaskScope=1；task_id:string=2；operation_id:string=3；request_id:string=4；status:string=5；error:Error=6 |
| CheckpointPrepare | scope:TaskScope=1；clock:WorldClock=2；save_request_id:string=3；final_evidence:repeated TaskEvidence=4 |
| CheckpointPrepared | scope:TaskScope=1；save_request_id:string=2；checkpoint:TaskCheckpointRef=3；error:Error=4 |
| CheckpointFinish | scope:TaskScope=1；save_request_id:string=2；saved:bool=3 |

状态集合：WorldBindingReady 为 ready/paused；checkpoint 为 absent/confirmed/unconfirmed；Evidence.outcome 为 progress/satisfied/unsatisfied/interrupted；InteractionSource.kind 为 player/task_arrival；TaskControlResult.status 为 released/handed_off/unconfirmed。TaskControl 仅 release，没有重新发起世界动作的权限。

TaskEvidence 内的发生时间和 ContextFact 是该事实来源权威。外层事件可以携带对它的展示引用，但不提供第二套可推进 Task 的结果。复制到 Observation 的旧事实必须保留原 fact_id / 发生时间，不能变成“此刻刚发生”。

InteractionSource 由 Adapter 真实交互创建。Runtime 只用结构化来源判定工具准入；Adapter 在每次动作前再次校验其本地来源记录、world/run/player、距离及控制权，字段本身不能绕过游戏检查。

### 3.3 映射规则

- Phase9.1 Binding ↔ TaskScope；Clock ↔ WorldClock；CheckpointRef ↔ TaskCheckpointRef。
- Evidence.StartRevision ↔ task_revision；Evidence.Kind ↔ outcome；WaitUntil 保留 presence。
- SourceRef.GameTime / Facts 保存 Evidence.game_time / context_facts 的原始通用内容。模型输入中的 JSON 不得替代它们。
- 预约 TaskSpec.Contract 保存 TaskProposal 的规范化 JSON，执行时原样恢复 TaskProposal 放入 task_contract。
- TaskScope.execution_generation 的当前值只来自 Runtime 的 WorldBindingReady / CheckpointPrepared；普通 WorldBinding 请求不得自行提高代次。
- 首次绑定的 generation=0；同 run 重新连接可以报告上次值，Runtime 仍分配新的执行代次。

## 4. Runtime Tools 合同

### 4.1 Registry 与执行器

```go
type RuntimeCallContext struct {
    Execution task.ExecutionContext
    InteractionSourceID string
}

type RuntimeExecutor interface {
    Execute(context.Context, RuntimeCallContext, model.ToolCall) (model.ToolResult, error)
}

func NewRegistry(environment *EnvironmentToolCatalog, runtimeEntries []Entry) (*Registry, error)
func (r *Registry) Lookup(name string) (Entry, bool)
func (r *Registry) BuildTurnToolView(config ToolAdmissionConfig) ToolAdmissionResult
```

以上定义放在 tool 包；task 包的 tools.go 实现工具处理方法，不导入 tool 包，避免循环依赖。其固定签名为 `func (s *Service) ExecuteTool(ctx context.Context, exec ExecutionContext, sourceID string, call model.ToolCall) (model.ToolResult, error)`；tool/runtime_tool.go 的薄包装实现 RuntimeExecutor。task 的核心 model/service 文件不导入 model.Provider。

Entry.Kind 新增 runtime；Registry 拒绝任意跨来源同名条目。每次模型调用前创建只读 Tool View，执行时从该 View 取 Entry，不按模型参数选执行器。两类工具共享 schema / 计数 / exclusive_per_step / settle_after_success / 输出裁剪和 History。

背景 Task 的 lease/状态变化优先于继续 Step：等待或终态已经提交则停止背景认知；真实玩家交互仍可生成确认台词。

### 4.2 create_task

```json
{
  "type": "object",
  "properties": {
    "proposal_ref": {"type": "string", "minLength": 1},
    "instruction": {"type": "string", "minLength": 1, "maxLength": 2048}
  },
  "required": ["proposal_ref", "instruction"],
  "additionalProperties": false
}
```

来源必须是有效 InteractionSource。proposal_ref 由 Runtime 为本 Turn 的成功 TaskProposal 生成，绑定 owner/run/generation/event/turn；在 Turn 结束、保存屏障或连接失效时删除。创建前重新比较 clock 和 wake_at；过期校验结果拒绝。

转换为 TaskSpec：Instruction 来自模型；ClockID、WakeAt、DeadlineAt、EquivalenceKey、Contract 来自校验结果；ResultContract=authoritative_evidence；Source 来自执行上下文。调用 Service.Create，Admission.MaxActivePerOwner=1。

成功输出包含 task_id、revision、state、next_wakeup_at、deadline_at、created；精确重试复用原响应。模型只能在成功响应后确认约定。

### 4.3 update_task

```json
{
  "type": "object",
  "properties": {
    "task_id": {"type": "string", "minLength": 1},
    "intent": {"type": "string", "enum": ["wait", "cancel"]},
    "next_wakeup_at": {"type": "integer"},
    "progress_note": {"type": "string", "maxLength": 2048},
    "reason": {"type": "string", "maxLength": 512}
  },
  "required": ["task_id", "intent"],
  "additionalProperties": false
}
```

解码器进一步校验条件字段：wait 必须有 next_wakeup_at，允许 progress_note，不接受 reason；cancel 必须有非空 reason，不接受 next_wakeup_at/progress_note。保持现有 schema 验证器与服务端条件校验各自独立，不仅依赖模型遵守说明。

Task wake 仅操作自身 Task；真实玩家交互仅取消相同 owner 的 Task。模型不传 expected_revision/state/generation/claim；ExpectedRevision 从本 Step 的 Task Context 取得，版本过期返回 task_changed，不能自动覆盖最新事实。

`create_task / update_task` 都是 Sync + Sequential，policy 为 exclusive_per_step=true、settle_after_success=false；它们不会产生 ActionRequest / ActionStatus。返回 Runtime ToolResult，模型工具调用预算仍计数。

## 5. 调度与连接接口

新增 WorldRegistry，内部条目包含 Head、当前 streamEnvironment、完整 EntityRef 目录、Environment catalog、LaneStore 和 ready 状态。LaneStore 仍属于当前有效连接；TaskService 和 Dispatcher 属于进程。不要为解决长期 Task 把失效连接的 lane 留在进程里继续运行。

```text
WorldBinding
  校验协商扩展、Hello.game_id、唯一活动绑定与 EntityRef
  → 暂停旧绑定，取消旧 lane 执行；等待旧清理结束
  → Service.ActivateWorld，产生当前 generation
  → 注册当前连接 / lanes / clock
  → WorldBindingReady；只有 ready 可以扫描

Clock update
  校验 scope、sequence 和非倒退
  → Service.UpdateClock 持久同步 head
  → 通知扫描；暂停时现实 ticker 不推进游戏 Tick
```

Service.UpdateClock 和 DeactivateWorld 使用 Phase9.1 接口。断连立即 DeactivateWorld；future Task 保留，持久 wake 不消费，Adapter 自己收尾当前世界效果。

调度初始配置写入 agent.json 的 `task` 对象：enabled=false、db_path=runtime/.local/tasks/tasks.sqlite、scan_interval_ms=1000、dispatch_batch=32、retry_min_ms=1000、retry_max_ms=30000；StoreOptions 的大小/超时使用 Phase9.1 默认值并允许同对象配置覆盖。演示配置显式启用 task；缺省配置保持旧 Adapter 行为，启用但绑定未就绪时也不暴露 Task 工具。

Task 模块配置从 agent.Config 装配到 Server，不按 NPC/游戏定义覆盖数据库地址。启动顺序为打开 Store → Service → Dispatcher → Gateway；关闭顺序为停止扫描和新准入 → 解绑/取消并等待 lane 清理 → 关闭 Store。Ctrl+C 不能先 GracefulStop 无限等待未关闭的长 stream。

Dispatcher 通过注入的 `ReadyWorlds() []Head` 和 `EnqueueWake(context.Context, Binding, Wake) error` 回调访问活动世界与 lane，task 包不导入 gateway。Gateway 实现投递/admission，Dispatcher 负责时钟扫描和有限重试。服务退出先停止这些入口，GracefulStop 最多等待 5 秒，仍有未结束 stream 时调用 Stop；测试覆盖客户端保持连接的关停情形。

### 5.1 lane 投递

1. ClaimDue 返回有限批量 Wake；按 owner 取得当前连接的 lane。
2. 创建 session.Task，Admitted 使用独立 channel，Abort 回退 claim。
3. Enqueue 失败：ReleaseClaim，保留 wake 并指数退避到 30 秒上限。
4. Enqueue 成功：MarkEnqueued 持久成功后才 close(Admitted)。
5. MarkEnqueued 失败时标记该队列项失效并释放屏障，让 Run 直接返回；不能留下永久等待 channel。
6. Run 先 BeginWake，再 Reconcile；settled 路径直接完成，observe 路径取得当前 Observation 并接纳 Evidence，decide 路径进入新 Task Turn。
7. 当前 lane 执行中的 ActionResult 在当前执行上下文直接处理，不再次入同一 lane 后等待自己。

每个非终态 Task 最多一份待协调资格。技术重试 attempt 与业务 revision 分离；同一个 Wake 再次入队不重复安装世界 controller。

### 5.2 Task Turn 与事件

Loop 新增 `HandleTaskWake`，输入当前 Environment、ConnectionContext、canonical EntityRef、Registry、ExecutionContext 与 Record；复用已有 Observe → Context → bounded Steps → TurnCompletion，不复制第二套 Agent Loop。

内部 trigger 用 wake_id 作为来源关联，Context 明确 kind=task_wake。这个 trigger 不是发给 Adapter 的虚假 GameEvent，不发送虚假的 EventAck，也不授权玩家 UI。

普通 GameEvent 携带 Evidence 时先落库和去重，再 EventAck。后台结果由 Task coordinator 处理，不将每条 progress 都派发一个模型 Turn。存在有效 InteractionSource 时另走一次交互入口，Task 已终态不使该来源失效；同 fact/source 不重复派发交互。

到达交互的 task_id 是结果关联，不是让已完成 Task 再次拥有出发/等待权限。TaskActionSource 仅注入背景任务动作；到达交互动作使用其实际 source_event_id/source_turn_id，经 Adapter 保存的到达来源校验。

### 5.3 操作登记、观察与 release

背景 Task 发 Environment Action 前调用 RegisterOperation；提交失败则不发命令。ActionRequest.task_source 写入 Task、启动 revision、operation、wake、当前绑定及保存的 TaskProposal。

同 run 重绑协调时，Observation 可以重新提交带原 operation/原发生时间的旧回执；Gateway 在确认回执来自当前绑定的主动查询后设置内部 Evidence.RevalidatedIn。原连接直接迟到消息仍拒绝；新 run 一律不能引用回档外旧回执。该字段不接受客户端 JSON/模型参数注入。

终态后对仍属于 Task 的 operation 发送 TaskControlRequest；有界等待 3 秒，结果写 RecordCleanup。释放不依赖模型，也不复用已完成 Sync Action 的 CancelAction。超时记录 unconfirmed；Adapter 的截止、断线和 UI 生命周期仍负责本地安全。

## 6. 开发任务

### 9.2-A：Protocol 与映射

协议定义、生成文件和其测试必须处于同一个提交；映射实现及其测试可作为后续独立提交。

- [ ] 按第 3 节增补消息，保持所有旧字段编号与 presence。
- [ ] 生成 Go；C# 测试项目通过 Grpc.Tools 构建生成，不手改生成文件。
- [ ] 实现 task_protocol.go 的 scope/clock/evidence/proposal 双向映射。
- [ ] 覆盖 absent 与零 Tick、wait_until 缺省、EntityRef.definition_id、错误类型、未知状态和旧消息兼容。

```powershell
powershell -ExecutionPolicy Bypass -File protocol/scripts/gen-go.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
go test ./runtime/internal/gateway -run 'TestTaskProtocol|TestLegacyProtocol' -count=1
```

### 9.2-B：Registry 与 Runtime Tools

提交边界：Registry 与 Runtime Tools 分别形成独立可审查提交。

- [ ] 写混合 kind、同名冲突、预算和执行位置测试，先运行确认失败。
- [ ] 实现 RuntimeExecutor 包装、两项 schema、proposal_ref 生命周期和服务端版本注入。
- [ ] 修改 scheduler.runOne，Environment 分支保留既有 Sync/Async 逻辑；Runtime 分支调用本地执行器。
- [ ] Runtime 成功/失败写入模型 ToolResult 与 HistoryExecution.RuntimeResult，不能捏造 ActionID 或 ActionResult。

```text
TestMixedToolKinds
  Registry = local.create_task + environment.travel_x
  create_task 成功：SQLite 有 Task，Environment.Submit/Start 调用次数为 0
  travel_x 成功：Environment 调用次数为 1，正常 Action 生命周期不变
  environment 也注册 create_task：启动/合并拒绝该冲突，不覆盖本地工具

TestIntentUsesObservedRevision
  模型读到 revision=4，提交前 Task 新事实使原上下文过期
  update_task(intent=wait)：task_changed，原事实和 wake 不被覆盖
  参数加入 state=succeeded 或 generation：输入校验失败
```

```powershell
go test ./runtime/internal/tool ./runtime/internal/task -count=1
go test ./runtime/internal/agent -run 'TestRuntimeTool|TestMixedTool|TestTaskIntent' -count=1
```

### 9.2-C：世界绑定与持久投递

提交边界：绑定/Clock 与 dispatcher/lane 分别形成独立可审查提交。

- [ ] 接入 task 配置、WorldRegistry 和进程级 Service；增加能力协商与独立 task-ready 状态。
- [ ] 接入 WorldClockUpdate、序号去重、倒退暂停与 disconnect 清理。
- [ ] 按第 5.1 节实现队列 admission；测试 Enqueue 成功但数据库提交失败的窗口。
- [ ] 写无绑定、第二 NPC/world、保存屏障、旧 stream 迟到和 Runtime 重启测试。
- [ ] 完成服务关停测试，不靠终止整个测试进程绕过资源清理。

```powershell
go test ./runtime/internal/gateway ./runtime/internal/session -run 'TestWorldBinding|TestTaskDispatch|TestTaskShutdown|TestLane' -count=1
go test ./runtime/internal/task -run 'TestDispatcher' -count=1
```

### 9.2-D：认知与确定性协调

- [ ] 创建 Task Turn 入口并复用 bounded Loop；最小 Task Context 必须包含目标、当前状态、期限和本 Step revision。
- [ ] 把 Context 默认措辞从只允许 Environment action 调整为可调用可用工具；保留 world fact / Runtime task authority 的区别。
- [ ] ActionResult 的 wait_until 自动提交等待，停止背景 Task Turn，不再额外调用 update_task。
- [ ] 接纳确证结果后自动提交终态和 cleanup；交互来源独立派发。
- [ ] 模型无进展、观察失败使用各自三次上限；技术重试不调用模型。

```text
TestWakeStartsNewTurn
  创建 T 的 Turn A 已完成；Clock 更新到 WakeAt
  使用非 Stardew 名称的环境工具，Task 在 lane 启动 Turn B
  B != A，且收到新 Observation；不存在跨小时 waiter

TestEvidenceDoesNotRequireModel
  已登记 wait operation；fake model 每次调用都计数并报错
  接纳 satisfied 或 unsatisfied → 业务终态、cleanup 请求成立
  没有 InteractionSource 时模型调用次数为 0
  有有效到达来源时可以尝试对话，失败也不改变已确认 Task 结果
```

```powershell
go test ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/gateway -count=1
go test ./... -count=1
```

## 7. 交接条件

- [ ] 创建 Turn 已结束后，fake 游戏时间独立触发新的有界认知；后续等待可持续跨天。
- [ ] Runtime 与 Environment 工具执行位置有计数断言，内部 Task trigger 没有伪造玩家来源。
- [ ] 队列满、数据库提交失败、断连、保存屏障均不丢持久 wake。
- [ ] Protocol 生成及 Runtime 回归通过；完成最后一个本阶段提交及其独立任务 CR 后自动进入 Phase9.3。
- [ ] 本阶段不以 fake 测试替代跨地图、玩家 UI 或真实存档验证。
