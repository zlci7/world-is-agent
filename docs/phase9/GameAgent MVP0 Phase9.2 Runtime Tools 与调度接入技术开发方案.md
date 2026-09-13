# GameAgent MVP0 Phase9.2 Runtime Tools 与调度接入技术开发方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-13
> **执行方式:** 每个独立模块完成相关测试和 `git diff --check` 后创建本地提交，暂停并交付聚焦 CR；CR 修正使用独立 `fix:` 本地提交，复验通过并经用户确认后继续下一模块。9.2 收口执行阶段整体回归；Phase9.5 执行最终整分支/系统审查。
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

本阶段闭环为：创建持久 Task → 创建 Turn 结束 → 游戏时间到期 → 当前 NPC lane 启动新 Turn → Environment Action → Evidence → 等待或终态及清理。普通玩家交互支持取消当前任务。

9.2 完成上述 Runtime 闭环、TaskControl 的 Runtime 侧及 Checkpoint 协议定义与映射；9.3 实现真实游戏行动、等待与控制权释放；9.4 实现完整保存桥接、恢复及结果记忆。TaskControl 使用 fake Adapter 验证，Checkpoint 的协议就绪不表示真实保存链路已通过验收。

实现优先复用 9.1 TaskService、现有 Tool View、FIFO lane 和 bounded Loop。Registry 只合并条目并复用准入逻辑；技术重试在 Dispatcher/当前投递入口内完成。扫描间隔与每次投递批量使用第 5 节配置。完整结果 History、近期结果读取、恢复信息投影及独立 Task Context 分项预算由 9.4 接入；性能压测和存储优化以实测瓶颈为触发条件，不作为本阶段开工或验收前置条件。9.1 保持 Accepted，接入所需的最小通用接口扩展及其测试归入 9.2。

## 2. 文件边界

| 文件 | 修改 |
| --- | --- |
| `protocol/proto/gameagent.proto`、`protocol/gen/go/` | 增量字段、生成代码 |
| `protocol/tests/check-protocol-static.ps1`、`check-go-generation.ps1` | 兼容与生成校验 |
| `runtime/internal/tool/registry.go`、`runtime_tool.go`、`registry_test.go` | 合并条目，复用现有准入与 Tool View；Runtime 执行上下文与执行器合同 |
| `runtime/internal/tool/types.go`、`environment_catalog.go`、`environment_tool.go` | KindRuntime、合并 Tool View、Task 来源注入 |
| `runtime/internal/tool/task_tools.go`、`proposal.go`、`task_tools_test.go` | 两个本地工具的 schema、参数解析、TaskService 调用与 Turn 内 proposal_ref |
| `runtime/internal/task/service.go`、`sqlite_store.go`、`service_test.go`、`sqlite_store_test.go` | B2 增加 ListActive，先筛选非终态任务再限制数量；保留 List 语义 |
| `runtime/internal/task/model.go`、`wake.go`、`wake_test.go` | C2 增加 WakeInspection / InspectWake，按同一只读快照确认投递状态 |
| `runtime/internal/task/dispatcher.go`、`dispatcher_test.go` | 扫描、认领和有界重试 |
| `runtime/internal/gateway/world_registry.go`、`task_dispatch.go`、`task_protocol.go`、`task_control.go` | 新增世界绑定、lane 入口、协议映射与 release 请求 |
| `runtime/internal/gateway/gateway.go`、`stream_environment.go` | 控制消息、证据接纳与 stream 清理 |
| `runtime/internal/agent/task_turn.go`、`task_loop_test.go` | 内部 Task trigger 与同一有界认知循环接入 |
| `runtime/internal/agent/task_context.go`、`task_context_test.go` | 普通交互与 Task Turn 共用的当前任务只读快照 |
| `runtime/internal/agent/scheduler.go`、`loop.go`、`config.go` | 按 kind 执行、状态收敛后停止、配置加载 |
| `runtime/internal/context/context.go`、`renderer.go`、`task_projection.go`、`request_sizing.go` | 最小 Task 投影计入现有预算；通用措辞允许调用当前可用工具 |
| `runtime/cmd/server/main.go`、`runtime/config/agent.json` | 进程级实例、服务启停、默认任务配置 |
| `runtime/internal/gateway/task_dispatch_test.go`、`world_binding_test.go`、`task_protocol_test.go` | 新增连接与并发集成测试 |
| `runtime/internal/gateway/task_e2e_test.go` | 真实 gRPC/临时 SQLite 与 fake environment/model 的阶段闭环；9.5 扩展系统场景 |

当前 eventHandler 接口把 `*tool.EnvironmentToolCatalog` 直接传给 Loop；改为合并 Runtime 条目后向每个 Turn 提供准入后的 View。Environment bootstrap 的校验、诊断和旧 fake tests 保留；schema、计数、预算、裁剪及 policy 处理共用现有实现。

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

任务扩展握手顺序固定为：Adapter 先发送带 supported_extensions 的 AdapterHello；Runtime 依次返回带 accepted_extensions 的 EnvironmentReady 和 CapabilityRequest；Adapter 返回 CapabilityList 后才能发送 WorldBinding；Runtime 完成激活后返回 WorldBindingReady。Gateway 不为乱序任务消息增加缓冲状态机：CapabilityList 前的 WorldBinding、WorldBindingReady 前的 Clock/Task 消息均明确拒绝。未协商扩展的旧 Adapter 在 CapabilityList 后继续现有普通事件链路；重新连接必须重新执行完整握手。

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
| TaskEvidence | fact_id:string=1；task_id:string=2；operation_id:string=3；scope:TaskScope=4；start_revision:uint64=5；occurred_at:int64=6；outcome:string=7；wait_until:optional int64=8；details:google.protobuf.Struct=9；game_time:GameTime=10；context_facts:repeated ContextFact=11 |
| TaskActionSource | task_id:string=1；start_revision:uint64=2；wake_id:string=3；operation_id:string=4；scope:TaskScope=5；task_contract:TaskProposal=6 |
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
- Evidence.StartRevision ↔ TaskEvidence.start_revision；Evidence.Kind ↔ outcome；WaitUntil 保留 presence。
- TaskActionSource.start_revision 取自已登记的 Operation.StartRevision。Adapter 为对应 operation 保存该值，关联该 operation 的 ActionResult / GameEvent / Observation 证据均沿用它；映射不读取 Task 当前 revision 替换启动版本。
- Task.Revision、ExecutionContext.ExpectedRevision、Result.Revision 保持现有语义。无 operation 的 Evidence 继续按 9.1 合同校验 StartRevision 与当前 Task.Revision，映射层不改写版本。
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
    ObservedTask *task.Record
}

type RuntimeExecutor interface {
    Execute(context.Context, RuntimeCallContext, model.ToolCall) (model.ToolResult, error)
}

func NewRegistry(environment *EnvironmentToolCatalog, runtimeEntries []Entry) (*Registry, error)
func (r *Registry) Lookup(name string) (Entry, bool)
func (r *Registry) BuildTurnToolView(config ToolAdmissionConfig, runtimeContext *RuntimeCallContext) ToolAdmissionResult
```

以上定义和 RuntimeExecutor 实现放在 tool 包。task_tools.go 负责 schema、参数与条件字段校验、TaskSpec 构造及模型结果封装，调用现有 Service.Create / ApplyIntent 等领域接口；proposal.go 只管理当前 Turn 的校验结果。task 包保留通用服务合同，不承担模型工具解析。

Entry.Kind 新增 runtime；Registry 拒绝任意跨来源同名条目。每次模型调用前创建只读 Tool View，执行时从该 View 取 Entry，不按模型参数选执行器。两类工具共享 schema / 计数 / exclusive_per_step / settle_after_success / 输出裁剪和 History。

runtimeContext 由 Runtime 构造，ObservedTask 是本 Step 已展示给模型的只读快照。功能未启用、绑定未 ready 或来源无效时传 nil，仅保留 Environment 工具；有效玩家交互准入创建/取消，Task wake 仅准入自身更新。执行入口再次校验来源和当前绑定，Tool View 的可见性不替代执行校验。

Scheduler.preflight 在 schema/policy 校验后按 Entry.Kind 分流：仅 Environment 条目调用 BuildActionRequest；Runtime 条目保留本地调用参数。同步与异步 Environment 发送路径共用可返回 error 的 beforeEnvironmentAction 检查，每个动作在实际发送前恰好调用一次；检查成功后才触发观察性 onActionSubmit，检查失败时 SubmitAction / StartAction 调用次数均为零。普通玩家及非 Task 动作使用 no-op 检查。Task 闭包由 Loop/Gateway 注入，Scheduler 不依赖 task 包。Runtime 工具不生成 ActionID，不要求 Environment.Submit/Start 可用。

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

来源必须是有效 InteractionSource。proposal_ref 由 Runtime 为本 Turn 的成功 TaskProposal 生成，绑定 owner/run/generation/event/turn；在 Turn 结束、保存屏障或连接失效时删除。创建时使用当前绑定的权威 Clock 重新校验 clock_id、wake_at 和 deadline_at；同一时钟正常推进不使引用自动失效，已错过创建窗口则拒绝。模型不能改写已校验的约定。

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

Task wake 仅操作自身 Task；真实玩家交互仅取消相同 owner 的 Task。模型不传 expected_revision/state/generation/claim；task_id 必须匹配本 Step 的 ObservedTask，ExpectedRevision 从该快照取得。版本过期返回 task_changed，后续决策重新读取上下文，不能自动覆盖最新事实。执行时使用当前权威 Clock 重新校验等待时间；时间推进与任务版本变化分别处理。

`create_task / update_task` 都是 Sync + Sequential，policy 为 exclusive_per_step=true、settle_after_success=false；它们不会产生 ActionRequest / ActionStatus。返回 Runtime ToolResult，模型工具调用预算仍计数。

两项工具共用一个错误转换入口。由工具参数解码器明确识别的 schema/条件字段错误返回模型可见的 invalid ToolResult；task_not_found、task_conflict、task_changed、idempotency_conflict、task_terminal、task_capacity_exceeded 是固定业务白名单，返回 rejected ToolResult。Service 返回的 invalid_task_spec 不因错误码相同自动视为模型输入错误；source_invalid、world_mismatch、generation_stale、clock_mismatch、clock_rewound、world_not_ready、save_in_progress、checkpoint/store 错误、context cancellation/deadline 和未知错误均作为技术失败结束当前执行资格。转换结果、History 和诊断只保留稳定分类，不包含原始参数、数据库错误或 prompt。

task_changed 只表示本 Step 观察的版本已过期。Loop 在仍有 Step 预算时重新构建任务快照并交给模型决策，不自动重放原工具调用；技术失败结束本次执行，但不直接把持久 Task 写成业务失败。

### 4.4 最小 Task Context

#### 4.4.1 当前任务查询

```go
func (s *Service) ListActive(ctx context.Context, owner session.AgentSessionKey, limit int) ([]Record, error)
```

ListActive 属于 task 包的内部服务接口，由 B2 实现。查询按完整 owner 隔离，在数据库中先筛选 state 为 waiting/running/paused，再按 task_id 升序排序并应用 LIMIT。保留现有身份与记录完整性校验；合法 owner 下无匹配项或 limit <= 0 返回空列表，非法 owner、损坏记录及存储错误按现有错误合同返回。返回记录与持久对象分离，调用方修改快照不改变数据库状态。

Service.List 保持按 owner 列出全部状态并限制数量的语义；ListActive 不通过 List(owner, limit) 后过滤实现。此接口不新增模型工具、协议消息、数据库表或状态。

#### 4.4.2 上下文投影

agent/task_context.go 在每次模型请求前构建只读快照：普通玩家交互调用 ListActive(owner, 1)，Task Turn 使用 Service.Read 按自身 task_id 读取并核对执行上下文。两类入口共用投影，历史终态不能遮蔽当前任务。

模型字段为 task_id、目标、state、revision、next_wakeup_at、deadline_at；后台入口同时带 wake_reason，暂停任务带原因。快照传给纯 Context Engine，Renderer 不访问数据库；同一快照用于本 Step 的版本校验。任务身份、状态、版本、期限计入既有请求预算，必需字段放不下时返回 budget 错误，不执行缺少任务约束的请求。近期结果和恢复明细由 9.4 扩展此入口。

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

Service.UpdateClock 和 DeactivateWorld 使用 Phase9.1 接口。断连先关闭 WorldRegistry 中该绑定的新任务/动作准入，再取消 lane 并调用 DeactivateWorld；保存屏障或存储错误使持久解绑失败时，内存准入仍保持关闭并记录错误。future Task 和持久 wake 保留，Adapter 自己收尾当前世界效果。显式重绑必须先完成旧绑定失效及持久状态恢复，才允许 ready。

WorldRegistry 按 world 串行处理 Clock 更新与任务提交前的短临界区：确认当前绑定仍与调用来源一致，读取当前 Head.Clock，重新校验时间窗口并调用任务服务。ExpectedRevision 始终使用本 Step 观察值；Binding 变化时拒绝旧调用。临界区不包含模型思考、网络等待或整个 Turn。

调度初始配置写入 agent.json 的 `task` 对象：enabled=false、db_path=runtime/.local/tasks/tasks.sqlite、scan_interval_ms=1000、dispatch_batch=32、retry_min_ms=1000、retry_max_ms=30000；StoreOptions 的大小/超时使用 Phase9.1 默认值并允许同对象配置覆盖。演示配置显式启用 task；缺省配置保持旧 Adapter 行为，启用但绑定未就绪时也不暴露 Task 工具。

Task 模块配置从 agent.Config 装配到 Server，不按 NPC/游戏定义覆盖数据库地址。启动顺序为打开 Store → Service → Dispatcher → Gateway；关闭顺序为停止扫描和新准入 → 解绑/取消并等待 lane 清理 → 关闭 Store。Ctrl+C 不能先 GracefulStop 无限等待未关闭的长 stream。

Dispatcher 通过注入的 `ReadyWorlds() []Head` 和 `EnqueueWake(context.Context, Binding, Wake) error` 回调访问活动世界与 lane，task 包不导入 gateway。Gateway 实现投递/admission，Dispatcher 负责时钟扫描和有限重试。服务退出先停止这些入口，GracefulStop 最多等待 5 秒，仍有未结束 stream 时调用 Stop；测试覆盖客户端保持连接的关停情形。

### 5.1 lane 投递

#### 5.1.1 持久状态查询

```go
type WakeInspection struct {
    Head Head
    Wake Wake
    Task Record
}

func (s *Service) InspectWake(ctx context.Context, binding Binding, wakeID string) (WakeInspection, error)
```

WakeInspection 定义在 task/model.go，InspectWake 在 task/wake.go 实现，归入 C2。Head、Wake 与对应 Task 在同一个数据库只读事务快照中读取，校验输入 Binding 与当前 Head、Wake 的 world/run/generation 及 Wake/Task 的 owner、task_id、时钟和状态关联。当前 world 不存在返回 ErrWorldNotReady，绑定过期返回 ErrGenerationStale，wake 不存在返回 ErrTaskNotFound，记录关联损坏返回 ErrInvalidTaskSpec；存储错误沿用现有分类。

返回值是分离的只读快照。查询不修改状态、revision、claim、attempt 或保存屏障，也不生成 operation 或执行资格。调用方核对返回的 owner/task_id；对 claimed/enqueued/running 还须核对原 claim_id/claimed_by。暂停的 Head 仅用于诊断，所有后续修改和发送仍经过当前 ready、保存屏障、绑定、领取身份及版本校验。

BeginWake 保持 enqueued → running 的状态转换合同。不确定结果统一先调用 InspectWake；查询后状态再次变化时，后续操作的校验/CAS 必须拒绝过期执行，重新检查仍受本节重试上限约束。

#### 5.1.2 投递与恢复

1. ClaimDue 返回有限批量 Wake；按 owner 取得当前连接的 lane。
2. 创建 session.Task，Admitted 使用独立 channel；投递入口保留 wake_id/claim_id 与当前投递状态，Run 与 Abort 共用按状态收尾。
3. Enqueue 成功后调用 MarkEnqueued；只有持久提交成功的队列项才取得执行资格并释放屏障。
4. 提交失败时撤销队列项执行资格并释放屏障，由投递入口继续收尾；Run 返回不会自动触发 Abort。
5. Run 先 BeginWake，再 Reconcile；settled 路径直接完成，observe 路径取得当前 Observation 并接纳 Evidence，decide 路径进入新 Task Turn。
6. 当前 lane 执行中的 ActionResult 在当前执行上下文直接处理，不再次入同一 lane 后等待自己。

| 失败位置 / 持久状态 | 处理责任与恢复路径 |
| --- | --- |
| Enqueue 失败，仍为 claimed | 投递入口调用 ReleaseClaim，保留 pending wake 与退避时间 |
| MarkEnqueued 明确未提交，仍为 claimed | 撤销本队列项后调用 ReleaseClaim；释放失败仍由该投递入口保留重试责任，不能只等待下一次 ClaimDue |
| MarkEnqueued、ReleaseClaim 或 BeginWake 提交结果不确定 | 调用 InspectWake 确认同一 wake 的持久状态，再进入对应恢复路径；无法确认时关闭该 world 任务准入 |
| 查询为 pending | 已回到正常调度路径，结束旧投递项，由 ClaimDue 按到期/退避条件重新认领 |
| 查询为 claimed | 核对原领取身份，按队列项是否仍有效继续 MarkEnqueued 或 ReleaseClaim；领取身份已变化则结束旧项 |
| 查询为 enqueued | 原领取身份和 lane 仍有效时有界重试 BeginWake；lane 已关闭则进入绑定恢复，不调用仅适用于 claimed 的 ReleaseClaim |
| 查询为 running | 原投递入口核对身份后进入 Reconcile / FinishAttempt；不再次 BeginWake，已登记动作先核对证据，不重跑整段工具调用 |
| 查询为 consumed | 结束旧投递项，不生成新的执行资格 |
| 旧 generation、断线或保存屏障 | 立即停止旧执行资格；由当前有效绑定或保存屏障解除后的恢复入口处理持久 wake |

running 的恢复协调上下文使用快照 Task.Owner/ID/Revision、Wake.ID 及受信 task_wake 来源，不能把启动前的 Wake.ExpectedRevision 当作当前任务版本。进入服务前按第 5 节重新核对 Binding 并取得当前 Clock；已有模型意图仍使用该模型实际观察的 revision。仅在协调确认需要新决策时启动新 Turn。

技术重试由 Dispatcher/当前投递入口负责，复用 retry_min_ms/retry_max_ms 退避，每个未完成投递的连续技术失败最多三次，技术计数与业务 revision、模型无进展计数分开。耗尽后关闭该 world 的任务准入并报告 wake/claim/错误；存储可用时持久记录暂停原因，不可用时保留原记录及连接诊断，等待显式恢复。相同身份只有一份重试责任；重试不调用模型、不安装世界 controller。恢复测试同时覆盖同进程临时失败恢复和暂停后的显式重绑。

### 5.2 Task Turn 与事件

Loop 新增 `HandleTaskWake`，输入当前 Environment、ConnectionContext、canonical EntityRef、Registry、ExecutionContext 与 Record；复用已有 Observe → Context → bounded Steps → TurnCompletion，不复制第二套 Agent Loop。

内部 trigger 用 wake_id 作为来源关联，Context 明确 kind=task_wake。这个 trigger 不是发给 Adapter 的虚假 GameEvent，不发送虚假的 EventAck，也不授权玩家 UI。

普通 GameEvent 携带 Evidence 时先落库和去重，再 EventAck。后台结果由 Task coordinator 处理，不将每条 progress 都派发一个模型 Turn。存在有效 InteractionSource 时另走一次交互入口，Task 已终态不使该来源失效；同 fact/source 不重复派发交互。

到达交互的 task_id 是结果关联，不是让已完成 Task 再次拥有出发/等待权限。TaskActionSource 仅注入背景任务动作；到达交互动作使用其实际 source_event_id/source_turn_id，经 Adapter 保存的到达来源校验。

模型错误、预算耗尽、无进展和取消都必须结束本次执行资格。绑定仍有效时使用有限清理上下文调用 FinishAttempt；绑定已失效时交给绑定恢复。已确认的等待/终态保持不变，清理失败按第 5.1 节报告并停止新准入。

### 5.3 操作登记、观察与 release

背景 Task 发 Environment Action 前，通过可返回 error 的发送前检查调用 RegisterOperation；登记失败时同步 Submit 与异步 Start 的调用次数均为零。ActionRequest.task_source 写入 Task、operation、wake、当前绑定及保存的 TaskProposal，start_revision 使用已登记的 Operation.StartRevision。登记成功但发送结果不明确时保留原 operation，先观察和协调证据，不重新生成 operation 盲目重发。

同 run 重绑协调时，Observation 可以重新提交带原 operation/原发生时间的旧回执；Gateway 在确认回执来自当前绑定的主动查询后设置内部 Evidence.RevalidatedIn。原连接直接迟到消息仍拒绝；新 run 一律不能引用回档外旧回执。该字段不接受客户端 JSON/模型参数注入。

终态后对仍属于 Task 的 operation 发送 TaskControlRequest；有界等待 3 秒，结果写 RecordCleanup。释放不依赖模型，也不复用已完成 Sync Action 的 CancelAction。超时记录 unconfirmed；Adapter 的截止、断线和 UI 生命周期仍负责本地安全。

## 6. 开发任务

按 A1 → A2 → B1 → C1 → B2 → C2 → D 执行。每个模块是一个包含实现和测试的本地提交；模块完成后暂停 CR，交付提交号、相关测试结果及限制，用户确认后继续。模块只执行对应测试及直接受影响的回归；D 收口执行阶段整体回归，Phase9.5 执行最终系统审查。

各模块先写下列断言并确认失败，再实现最小逻辑、运行验证及 `git diff --check`。命令从仓库根目录执行；筛选测试必须实际匹配用例，零用例不算通过。

### 9.2-A1：Protocol 定义与生成

文件：protocol/proto/gameagent.proto、protocol/gen/go/、protocol/tests/、adapters/stardew/tests/ProtocolMapper.Tests/。输入为第 3 节合同，输出为兼容旧消息的 Go/C# 协议类型。

- [ ] 按第 3 节增补消息，保持所有旧字段编号与 presence。
- [ ] 生成 Go，C# 通过现有 ProtocolMapper.Tests 的 Grpc.Tools 构建生成；新增字段 round-trip 与旧消息兼容测试。
- [ ] 协议、生成文件和相关测试处于同一个提交；Checkpoint 消息合同在此定义，完整保存桥接由 9.4 实现。

```powershell
powershell -ExecutionPolicy Bypass -File protocol/scripts/gen-go.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
```

通过标准：旧字段编号与消息语义保持兼容，新增字段往返保真，Go 生成校验和 C# 构建测试通过。

### 9.2-A2：协议与 Task 模型映射

文件：runtime/internal/gateway/task_protocol.go、task_protocol_test.go。输入为 A1 协议类型和 9.1 值对象，输出为第 3.3 节双向映射。

- [ ] 实现 scope/clock/checkpoint/evidence/proposal 与 task 模型的边界映射。
- [ ] 覆盖 absent 与零 Tick、wait_until 缺省、EntityRef.definition_id、错误类型、未知状态拒绝和旧消息兼容。
- [ ] Evidence 的原始时间、来源、start_revision 和 Contract 内容映射后保持一致；RevalidatedIn 仅由 Runtime 受信入口设置。
- [ ] TestTaskProtocolStartRevisionRoundTrip：operation 在 revision=4 启动，Task 随后推进到 revision=5；TaskActionSource 和 TaskEvidence 的 start_revision 经双向映射仍为 4，不被当前任务版本覆盖。回归现有内核测试，确认关联同一 operation 的启动版本证据可接纳、误填当前版本则拒绝。

```powershell
go test ./runtime/internal/gateway -run 'TestTaskProtocol|TestLegacyProtocol' -count=1
go test ./runtime/internal/task -run '^TestAdmitEvidenceUsesOperationStartRevisionAfterTaskBusinessRevisionAdvances$' -count=1
```

通过标准：映射不丢失 presence、身份及原始事实，非法状态不能进入任务服务。

### 9.2-B1：条目合并与执行分流

文件：runtime/internal/tool/{registry,runtime_tool,types,environment_catalog,environment_tool}.go、对应测试，runtime/internal/agent/scheduler.go 及 scheduler tests。输入为现有 catalog 和 fake RuntimeExecutor，输出为混合 Tool View 与本地执行通路。

- [ ] 合并条目并复用现有 schema/policy/预算准入；覆盖混合 kind、同名冲突、只读 View 和来源准入。
- [ ] preflight 在 BuildActionRequest 前按 kind 分流；Runtime 分支调用 fake 本地执行器，Environment 保留既有 Sync/Async 路径。
- [ ] Runtime 成功/失败进入 ToolResult 与 HistoryExecution.RuntimeResult；断言 ActionID 不生成、Submit/Start 均未调用。
- [ ] 接入返回 error 的 Environment 发送前检查；fake 检查失败时同步/异步命令都不发送，通知回调不承担准入职责。

```powershell
go test ./runtime/internal/tool -count=1
go test ./runtime/internal/agent -run 'TestRuntimeTool|TestMixedTool|TestActionPreSend|TestScheduler|TestHandleEvent.*Tool' -count=1
```

通过标准：两类工具执行位置有计数断言，既有 Environment 行为及预算不变。本模块使用 fake 本地工具，B2 接入真实任务工具。

### 9.2-C1：世界绑定、Clock 与配置

文件：runtime/internal/gateway/{world_registry,gateway,stream_environment}.go、world_binding_test.go，runtime/internal/agent/config.go 及 config tests、runtime/cmd/server/main.go、runtime/config/agent.json。输入为 A2 映射、B1 View 和 9.1 Service，输出为受信的 ready 绑定、当前 Clock 与任务提交准入。

- [ ] 接入默认关闭的 task 配置、进程级 Store/Service、能力协商和独立 task-ready 状态；旧 Adapter 继续普通链路。
- [ ] 接入 Clock 序号去重、倒退暂停，以及 Clock 更新与任务提交的短临界区；模型和网络等待不持有该临界区。
- [ ] 无绑定、旧 stream 迟到、第二 NPC/world 隔离、保存屏障均有准入断言；持久解绑失败时内存准入仍关闭。
- [ ] 覆盖同 run 显式重绑和关闭长 stream 的有限关停；C2 在此生命周期内接入 Dispatcher。

```powershell
go test ./runtime/internal/gateway -run 'TestWorldBinding|TestWorldClock|TestTaskShutdown|TestLegacyProtocol' -count=1
go test ./runtime/internal/agent -run 'Test.*Config' -count=1
```

通过标准：只有当前 ready 绑定拥有任务执行权限，Clock 单调同步，断连和关停不会保留旧执行入口。

### 9.2-B2：任务工具与最小上下文

文件：runtime/internal/tool/{task_tools,proposal}.go、task_tools_test.go，runtime/internal/agent/{task_context,loop}.go、task_context_test.go，runtime/internal/context/{task_projection,context,renderer,request_sizing}.go 及对应测试，runtime/internal/task/{service,sqlite_store}.go、service_test.go、sqlite_store_test.go。输入为 B1 执行器和 C1 权威上下文，输出为 ListActive、可创建/等待/取消的本地工具及共用任务快照。

- [ ] 实现第 4.4.1 节 ListActive；TestListActive 覆盖终态记录排在前面仍能取到当前任务、paused 可见、全部终态为空、limit 边界、稳定排序及 owner/world 隔离，并断言原 List 行为保持不变。
- [ ] 实现两项 schema、条件字段校验、Turn 内 proposal_ref 与 Service 调用；成功响应对应实际 SQLite 提交，精确重试返回原响应。
- [ ] 实现第 4.4 节快照，覆盖普通玩家交互和后台来源；共用现有请求预算，默认措辞允许调用当前 View 中的工具。
- [ ] TestTaskIntentUsesObservedRevision：模型看到 revision=4 后任务变更，提交返回 task_changed，不能替换成最新 revision；伪造 state/generation 参数被拒绝。
- [ ] TestRuntimeToolUsesCurrentClock：模型思考期间 Tick 从 100 推进到 110，窗口仍有效则允许提交；已经越过窗口则拒绝；两种情况都使用当前 Clock。
- [ ] TestTaskContextFreshDialogueCancel：History 为空，新玩家对话仍能看到当前任务 ID/revision 并取消；历史终态不遮蔽当前任务，错误 owner/task_id 被拒绝。
- [ ] TestMixedToolKinds 使用真实 create_task：SQLite 有 Task，Environment.Submit/Start 均为零；Environment 工具仍走原 Action 生命周期。

```powershell
go test ./runtime/internal/task -run 'TestListActive' -count=1
go test ./runtime/internal/tool ./runtime/internal/context -count=1
go test ./runtime/internal/agent -run 'TestRuntimeTool|TestMixedTool|TestTaskIntent|TestTaskContext' -count=1
```

通过标准：ListActive 在限制数量前筛选当前任务，普通对话可据此创建/取消；模型使用的任务快照、执行版本与来源一致，时间正常推进不会误用旧 Clock。

### 9.2-C2：持久 wake 与 lane 投递

文件：runtime/internal/task/{model,wake,sqlite_store,dispatcher}.go、wake_test.go、sqlite_store_test.go、dispatcher_test.go，runtime/internal/gateway/task_dispatch.go、task_dispatch_test.go、gateway.go、runtime/cmd/server/main.go。输入为 C1 ready 绑定和 9.1 wake 接口，输出为 InspectWake、有界扫描、投递及收尾；使用 fake wake 执行回调独立测试，D 接入真实 Task Turn。

- [ ] 实现第 5.1.1 节 InspectWake；TestInspectWake 覆盖各持久状态、绑定/记录错误、分离快照及只读约束，查询前后 revision/claim/attempt/屏障均不变。
- [ ] TestInspectWakeSnapshotConsistency：并发提交前后读取 Head/Wake/Task，返回同一事务快照中的完整版本，不能混用两个时刻的记录。
- [ ] 接入有限批次 ClaimDue、现有 FIFO lane 和 admission 屏障，遵循第 5.1 节状态表。
- [ ] TestTaskDispatch 覆盖队列满、MarkEnqueued 失败、ReleaseClaim 临时失败、BeginWake 临时失败、Abort、旧 generation 和保存屏障；每条路径都有明确恢复或暂停结果。
- [ ] TestTaskDispatchBeginWakeCommittedButUnconfirmed：先让真实 SQLite 中 BeginWake 成功提交，再在调用边界模拟成功结果未交付；InspectWake 返回 running 和提交后的 Task，恢复路径不再次 BeginWake、不重复增加启动 revision、不重复发送动作。提交前回滚不能替代此用例。
- [ ] TestTaskDispatchInspectionBecomesStale：查询后另一操作改变任务版本、领取身份或绑定，旧快照驱动的操作被校验/CAS 拒绝，世界动作不发送；再次查询按当前状态恢复或结束旧项。
- [ ] 模拟 SQLite 临时失败恢复，不重启进程也能收回或继续同一 wake；连续失败耗尽后停止该 world 新准入，显式重绑后恢复持久记录。
- [ ] 断言技术重试不调用模型、不重复投递同一资格；关停先停止 Dispatcher，再清理 lane 和 Store。

```powershell
go test ./runtime/internal/task -run 'TestDispatcher|TestInspectWake' -count=1
go test ./runtime/internal/gateway ./runtime/internal/session -run 'TestTaskDispatch|TestTaskShutdown|TestLane' -count=1
```

通过标准：InspectWake 提供一致且只读的事实；持久 wake 不丢失、不重复执行，BeginWake 已提交但结果不确定时可正确协调；失败不会留下永久 admission 等待或无人负责的 claim。

### 9.2-D：认知、证据与阶段闭环

文件：runtime/internal/agent/{task_turn,loop,scheduler}.go、task_loop_test.go，runtime/internal/gateway/{task_dispatch,task_control,stream_environment,gateway}.go、task_e2e_test.go 及对应测试。输入为前六个模块，输出为完整 Runtime 调度闭环。

- [ ] HandleTaskWake 复用 bounded Loop 和 B2 任务快照；创建 Turn 结束后，Clock 独立触发新 turn_id 和新 Observation。
- [ ] 将 RegisterOperation 接入 B1 发送前检查；登记失败不发动作，登记后发送结果不明确时保留 operation 并先协调证据。
- [ ] ActionResult 的 wait_until 自动提交等待，停止背景 Task Turn，不再额外调用 update_task。
- [ ] 确证结果自动提交终态和 cleanup；重复 Evidence 及 ActionResult/Event 竞争只产生一个结果，有效交互来源独立派发。
- [ ] 模型错误、预算耗尽、取消均结束本次执行资格；模型无进展与观察失败使用各自三次上限，技术重试不调用模型。

```text
TestTaskRuntimeEndToEnd
  使用真实 gRPC、临时 SQLite、fake Adapter/model 和非 Stardew 工具名
  Turn A 收到 TaskProposal → create_task 本地提交 → A 完成
  Clock 到期 → ClaimDue/lane → 新 Turn B → 新 Observation
  登记 operation → Environment Action → progress/wait_until → B 完成
  satisfied 或 unsatisfied → 唯一业务终态与 cleanup；重复回执不重复动作
  游戏端与模型均为 fake，此用例不证明真实跨地图或存档行为

TestEvidenceDoesNotRequireModel
  已登记 wait operation；fake model 每次调用都计数并报错
  接纳 satisfied 或 unsatisfied → 业务终态、cleanup 请求成立
  没有 InteractionSource 时模型调用次数为 0
  有有效到达来源时可以尝试对话，失败也不改变已确认 Task 结果
```

```powershell
go test ./runtime/internal/gateway -run 'TestTaskRuntimeEndToEnd|TestTaskOperation|TestTaskControl' -count=1
go test ./... -count=1
```

通过标准：创建、独立唤醒、真实 Runtime 工具执行与证据收口贯通；确定性终态不依赖模型，旧普通对话链路回归通过。

## 7. 交接条件

- [ ] 创建 Turn 已结束后，fake 游戏时间独立触发新的有界认知；后续等待可持续跨天。
- [ ] Runtime 与 Environment 工具执行位置有计数断言，内部 Task trigger 没有伪造玩家来源。
- [ ] 新普通对话通过 ListActive 读取并取消当前任务，历史终态不遮蔽当前项；Clock 推进、版本冲突和绑定失效分别按合同处理。
- [ ] 队列满、数据库提交失败、断连、保存屏障均不丢持久 wake；InspectWake 能确认 BeginWake 已提交但结果不确定的状态，临时失败可恢复，持续失败有明确暂停与诊断。
- [ ] Protocol 生成、C# 协议测试及 Runtime 阶段回归通过；七个模块完成本地提交和聚焦 CR 后，交付 9.2 阶段结果并暂停，用户确认后进入 Phase9.3。
- [ ] 本阶段不以 fake 测试替代跨地图、玩家 UI 或真实存档验证。
