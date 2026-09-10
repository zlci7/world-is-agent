# GameAgent MVP0 Phase9.1 Runtime Task 内核技术开发方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-10
> **执行方式:** 使用 executing-plans 按任务单元连续实现；自动测试通过后进入下一单元，独立 review 集中在 Phase9.5。
> **Goal:** 建立支持跨天、可靠唤醒、权威结果和完整快照恢复的 Runtime 任务内核。
> **Architecture:** TaskService 是进程内模块，SQLite 是状态权威；模型工具、连接和游戏 API 均在模块之外接入。
> **Tech Stack:** Go 1.25、modernc.org/sqlite；复用仓库现有依赖，不引入调度框架。
> **Spec:** [Phase9 总方案](./GameAgent%20MVP0%20Phase9%20技术开发与验收方案.md)
> **后续:** [Phase9.2 Runtime Tools 与调度接入](./GameAgent%20MVP0%20Phase9.2%20Runtime%20Tools%20与调度接入技术开发方案.md)

## 1. 执行边界

- 从工作区实际状态开始，核对 `daf4f98` 基线及现有改动；保留用户修改，不创建提交或推送。
- 阅读总方案第 1–6、10 节和仓库 AGENTS.md。Phase9.0 的游戏可行性探针位于总方案第 13 节。
- 本阶段不调用 LLM、gRPC 或 Stardew API；不实现生产预约之外的新任务场景。
- 测试时直接提交规范化 TaskSpec，不能为了测试内核伪造玩家点击或 Adapter Proposal。
- 不设置每个任务睡眠 goroutine，不在数据库事务内等待 lane、网络、模型或游戏线程。
- 默认所有开发用例使用临时数据库；不得删除或重置用户现有 Task / Memory 数据。

## 2. 文件与依赖

| 文件，均位于 `runtime/internal/task/` | 职责 |
| --- | --- |
| `model.go`、`errors.go` | 下述值对象、状态及稳定错误码 |
| `store.go`、`sqlite_store.go` | 事务边界、DDL、读写、JSON 与快照完整性 |
| `store_lock_windows.go`、`store_lock_unix.go` | 持有到 Close 的进程级文件锁；复用 x/sys 的平台锁 |
| `service.go` | 创建、等待/取消、版本校验、业务状态推进 |
| `evidence.go`、`operation.go` | 事实接纳、operation 登记、结果与 cleanup |
| `wake.go` | claim/admission/消费；不包含网络或 lane 实现 |
| `checkpoint.go` | working head、保存屏障、不可变快照与恢复 |
| `admission.go` | 可配置的准入限制；预约等价性和单任务约束 |
| `testkit_test.go` | 固定时钟、故障注入、临时 SQLite fixture |
| `service_test.go`、`sqlite_store_test.go`、`checkpoint_test.go`、`evidence_test.go`、`wake_test.go`、`admission_test.go` | 各单元确定性测试 |

所有文件为新增。TaskService 不导入 agent / gateway / Stardew 类型；owner 复用 `session.AgentSessionKey`，ID 复用 `idgen.New`。公开接口中的 JSON 为 `json.RawMessage`，严格限制大小并保留数值精度。

## 3. 跨阶段数据合同

以下为 Go 类型的固定字段合同，字段使用导出的 PascalCase，持久 JSON 使用对应 snake_case。Runtime / Adapter 映射依照 Phase9.2，不向模型暴露执行字段。

| 类型 | 字段 |
| --- | --- |
| `WorldKey` | GameID string、WorldID string |
| `Binding` | World WorldKey、RunID string、Generation uint64 |
| `Clock` | ID string、Tick int64、Sequence uint64 |
| `SourceRef` | Kind string、EventID / TurnID / CallID string、GameTime / Facts JSON |
| `ExecutionContext` | Owner session.AgentSessionKey、Binding Binding、Clock Clock、Source SourceRef、TaskID / WakeID string、ExpectedRevision uint64 |
| `TaskSpec` | Instruction string、ClockID string、WakeAt / DeadlineAt int64、ResultContract string、Contract JSON、EquivalenceKey string、Source SourceRef |
| `Record` | ID string、Owner、Spec TaskSpec、State State、Revision uint64、CreatedAtGameTick / CreatedAtUnixMS int64、NextWakeAt *int64、Progress JSON、NeedsReconcile bool、PauseReason string、NoProgressAttempts / ReconcileAttempts int、Operations []Operation、Evidence []Evidence、Result *Result、Cleanup []Cleanup |
| `Operation` | ID / ActionID / CommandFingerprint string、StartRevision uint64、Binding Binding、Status string、Receipt JSON |
| `Evidence` | FactID / TaskID / OperationID string、Binding Binding、StartRevision uint64、OccurredAt int64、Kind string、WaitUntil *int64、Details JSON、Source SourceRef、Applied bool、RevalidatedIn *Binding |
| `Result` | ID / TaskID string、Revision uint64、State State、Reason string、OccurredAt int64、EvidenceRefs []string、Source SourceRef |
| `Cleanup` | OperationID string、Status string、Reason string |
| `Wake` | ID / TaskID string、Owner、ExpectedRevision uint64、DueTick int64、Reason / Status string、ClaimID / ClaimedBy string、Generation uint64、Attempt int、RetryAfterUnixMS int64 |
| `Intent` | Kind string、NextWakeAt *int64、ProgressNote / Reason string |
| `Admission` | MaxActivePerOwner int；0 表示内核不施加此限制，生产预约设置 1 |
| `CreateResult` | Task Record、Created bool |
| `ReconcileResult` | Task Record、Next string：`decide / observe / settled` |
| `CheckpointRef` | Status / ID / Checksum string、SchemaVersion int、World WorldKey、Reason string |
| `Head` | Binding Binding、Clock Clock、CheckpointID string、Status / Reason string |
| `Prepared` | Head Head、Reference CheckpointRef、SaveRequestID string |
| `AttemptOutcome` | Kind string：`progress / no_progress / reconcile_failed`、Reason string |

State 使用 `waiting / running / paused / succeeded / failed / cancelled`。ResultContract 首版接受 `authoritative_evidence`；Contract 可以为空，预约时保存完整不可变世界约定。内部 Source.Kind 为 `internal`，外部入口分别使用 `interaction / task_wake / environment / lifecycle`；这些值由受信执行器产生。

State 定义为 `type State string`。Revision/generation 的持久范围为 1..MaxInt64，递增检查溢出；Clock.Tick 为非负 int64。CreatedAtGameTick 用于审计，不作为恢复选择依据。

Record 的世界身份始终与 ExecutionContext.Owner、Binding.World 一致。不要重复保存一个可以与 owner 冲突的 game/world 字段。Source.GameTime 是原始通用游戏时间，不由内核按游戏名称换算。

### 3.1 公开服务接口

以下方法必须保持名称和参数语义一致；私有 DAO 可以按实现需要拆分。

```go
func OpenSQLiteStore(ctx context.Context, options StoreOptions) (*SQLiteStore, error)
func (s *SQLiteStore) Close() error
func NewService(store *SQLiteStore) *Service

func (s *Service) ActivateWorld(ctx context.Context, world WorldKey, runID string, clock Clock, ref CheckpointRef) (Head, error)
func (s *Service) UpdateClock(ctx context.Context, binding Binding, clock Clock) (Head, error)
func (s *Service) DeactivateWorld(ctx context.Context, binding Binding, reason string) error
func (s *Service) Create(ctx context.Context, exec ExecutionContext, spec TaskSpec, admission Admission) (CreateResult, error)
func (s *Service) Read(ctx context.Context, owner session.AgentSessionKey, taskID string) (Record, error)
func (s *Service) List(ctx context.Context, owner session.AgentSessionKey, limit int) ([]Record, error)
func (s *Service) ApplyIntent(ctx context.Context, exec ExecutionContext, intent Intent) (Record, error)
func (s *Service) RegisterOperation(ctx context.Context, exec ExecutionContext, operation Operation) (Operation, error)
func (s *Service) AdmitEvidence(ctx context.Context, binding Binding, evidence Evidence) (bool, error)
func (s *Service) Reconcile(ctx context.Context, exec ExecutionContext) (ReconcileResult, error)
func (s *Service) FinishAttempt(ctx context.Context, exec ExecutionContext, outcome AttemptOutcome) (Record, error)
func (s *Service) RecordCleanup(ctx context.Context, binding Binding, taskID string, cleanup Cleanup) error

func (s *Service) ClaimDue(ctx context.Context, binding Binding, clock Clock, limit int) ([]Wake, error)
func (s *Service) MarkEnqueued(ctx context.Context, binding Binding, wakeID, claimID string) error
func (s *Service) ReleaseClaim(ctx context.Context, binding Binding, wakeID, claimID string, retryAt time.Time) error
func (s *Service) BeginWake(ctx context.Context, binding Binding, wakeID, claimID string) (ExecutionContext, Record, error)
func (s *Service) PrepareCheckpoint(ctx context.Context, binding Binding, clock Clock, saveRequestID string, evidence []Evidence) (Prepared, error)
func (s *Service) FinishCheckpoint(ctx context.Context, binding Binding, saveRequestID string, saved bool) error
```

`AdmitEvidence` 返回本次是否新增事实；返回 false 仍可能是成功去重。发生同 fact_id / 调用凭据但内容不同时必须返回冲突，不覆盖原记录。`ActivateWorld` 在新 run 中恢复引用的全量快照，同 run 中恢复 working head；不得由调用者选择“忽略引用、加载最新”。

StoreOptions 字段为 Path string、BusyTimeout time.Duration、MaxTaskBytes / MaxSnapshotBytes / MaxTasksPerWorld int。零值使用默认值：BusyTimeout=5 秒，MaxTaskBytes=256 KiB，MaxSnapshotBytes=32 MiB，MaxTasksPerWorld=1024；Path 必填。上限应用于新写入，不能静默截断恢复数据或自动删除快照。

### 3.2 错误码

`invalid_task_spec`、`task_not_found`、`task_conflict`、`task_changed`、`idempotency_conflict`、`evidence_conflict`、`source_invalid`、`world_mismatch`、`generation_stale`、`clock_mismatch`、`clock_rewound`、`world_not_ready`、`save_in_progress`、`task_terminal`、`task_capacity_exceeded`、`checkpoint_unconfirmed`、`checkpoint_missing`、`checkpoint_invalid`、`store_in_use`。

实现带 Code 的 task.Error，并支持 errors.Is / errors.As。错误输出不含数据库绝对路径、原始 SQL 或完整游戏 payload。

## 4. 存储与事务

SQLite 使用外键校验、WAL、`synchronous=FULL`、有限 busy timeout。数据库旁文件锁必须先取得，再打开可写连接；第二进程返回 store_in_use。崩溃由 OS 释放锁，不通过“锁文件存在”判定存活。Unix 与 Windows 使用各自实现，测试锁争用和进程退出。

| 表 | 主键 / 唯一约束 | 必备数据 |
| --- | --- | --- |
| `task_world_heads` | game_id + world_id | run_id、generation、clock、status、checkpoint_id、save_request_id、屏障状态 |
| `tasks` | game_id + world_id + entity_id + task_id | state、revision、clock_id、next_wakeup_at、创建凭据及指纹、equivalence_key、完整 record_json |
| `task_wakeups` | wake_id；owner + task_id + expected_revision + reason 去重 | owner、due_tick、status、claim、generation、retry/attempt |
| `task_checkpoints` | checkpoint_id；game/world/save_request_id 唯一 | schema_version、clock、checksum、不可变 snapshot_json |

为 owner/state 和 game/world/clock_id/due_tick/status 建索引。涉及 Task、Evidence、Operation 的 JSON 变更与索引列更新在同一事务；采用单 writer 及 `BEGIN IMMEDIATE`，任何错误/取消均回滚。

业务 revision 在状态/进展/结果提交时递增；claim、operation 登记、原始 Evidence 接纳、Clock 和 cleanup 单独更新运行凭据，不冒充业务推进。更新 JSON 时始终在事务中读取当前值并合并，不能用较早的完整 Record 覆盖新接纳的 Evidence。自身已提交的业务变更返回最新版本供下一 Step 使用，已经发出的模型请求仍携带它原先观察到的版本。

创建幂等键为 owner + Source.EventID + Source.TurnID + Source.CallID；内部调用也必须提供非空、稳定的调用身份。同键同规范化输入返回同一 CreateResult；同键不同输入冲突。不同调用但同一有效约定按等价键返回已有非终态任务，不创建第二个。

保留终态记录和结果；任务数达到上限拒绝新建。结果写入预留固定结构空间，过大 opaque 字段在接纳前拒绝；不能出现模型说明写满记录后连 cancelled 都无法提交的状态。

## 5. 任务单元与测试

### 9.1-A：模型、隔离与 SQLite

修改范围：model、errors、store、sqlite_store、平台锁及相应 tests。

- [ ] 定义第 3 节类型与 StoreOptions；为状态、时间范围、空身份、JSON 数字精度编写失败测试。
- [ ] 创建四张表和索引，保存完整 owner；所有读写包含 game/world/entity 条件。
- [ ] 实现文件锁、事务取消和 Close 幂等；不在事务中做外部 I/O。
- [ ] 使用故障注入验证插入 Task 后、插入 wake 前失败时两者都不存在。
- [ ] 运行该单元测试，失败修复后继续 9.1-B。

```powershell
go test ./runtime/internal/task -run 'TestStore|TestTaskValidation|TestTaskIsolation' -count=1
```

必须包含：两个 world 使用相同 entity/task ID 不串读；错误 owner 不泄露存在性；第二 writer 拒绝；关闭后重开能读取数据；进程崩溃后锁可重新取得。

### 9.1-B：创建与模型意图

修改范围：service、admission、service_test、admission_test。

Create 要求同一 clock、`now < WakeAt <= DeadlineAt`、有效 owner/run/generation；生成 task_id，revision=1，state=waiting，首次 wake 与 Task 同事务提交。预约准入传 MaxActivePerOwner=1；内核测试可以传 0。

ApplyIntent 只接受：

- wait：当前上下文版本有效；`now < NextWakeAt <= DeadlineAt`；保留权威证据，ProgressNote 标记为模型说明。
- cancel：非空 Reason；提交 cancelled 及稳定结果，消费所有可执行 wake，保留待 cleanup 的 operation。

最终成功/失败不通过 ApplyIntent 写入。对终态任务的重复同调用重试返回原响应，新的修改请求返回 task_terminal。Exact retry 凭据及响应随 Task 快照保存。

- [ ] 写创建重复、不同约定冲突、旧 revision、空原因、越过 deadline 的失败测试。
- [ ] 实现 Create / ApplyIntent；在事务内检查已接纳但未处理的 Evidence。
- [ ] 若新事实需要协调，先阻止过期认知覆盖它；返回 task_changed 或由当前 lane 先 Reconcile。
- [ ] 运行测试后进入 9.1-C。

完整最小内核测试，不依赖任何 Adapter：

```go
func TestCreateWithoutProposal(t *testing.T) {
    ctx := context.Background()
    store, err := OpenSQLiteStore(ctx, StoreOptions{
        Path: filepath.Join(t.TempDir(), "tasks.sqlite"),
    })
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = store.Close() })
    svc := NewService(store)
    world := WorldKey{GameID: "fake-game", WorldID: "world-a"}
    clock := Clock{ID: "fake.minute.v1", Tick: 100, Sequence: 1}
    head, err := svc.ActivateWorld(ctx, world, "run-a", clock,
        CheckpointRef{Status: "absent", World: world})
    if err != nil { t.Fatal(err) }
    source := SourceRef{Kind: "internal", EventID: "e", TurnID: "t", CallID: "c"}
    exec := ExecutionContext{
        Owner: session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "actor"},
        Binding: head.Binding, Clock: clock, Source: source,
    }
    spec := TaskSpec{Instruction: "inspect later", ClockID: clock.ID,
        WakeAt: 200, DeadlineAt: 300, ResultContract: "authoritative_evidence", Source: source}
    got, err := svc.Create(ctx, exec, spec, Admission{})
    if err != nil { t.Fatal(err) }
    if !got.Created || got.Task.State != State("waiting") || got.Task.Revision != 1 {
        t.Fatalf("unexpected create: %+v", got)
    }
    again, err := svc.Create(ctx, exec, spec, Admission{})
    if err != nil || again.Task.ID != got.Task.ID { t.Fatalf("retry: %+v %v", again, err) }
}
```

测试文件使用 package task，导入 context、path/filepath、testing、session。其余 fixture 从此流程抽取，不绕过世界绑定和持久层。

```powershell
go test ./runtime/internal/task -run 'TestCreate|TestIntent|TestAdmission' -count=1
```

### 9.1-C：operation、证据与确定性收敛

修改范围：operation、evidence、service 及 tests。

RegisterOperation 必须在下发命令之前提交 operation_id、ActionID、启动 revision、绑定和命令指纹。同 ID 同指纹返回已有操作；同 ID 不同内容拒绝。Action 超时只说明结果不确定，不能自动生成“玩家爽约”。

AdmitEvidence 只持久接纳/去重并登记待协调资格，不在接收线程直接调用模型。校验的是已登记 operation 的启动版本，不能要求 Evidence.StartRevision 等于当前 Task revision。读取其他 Task 的证据、回档前旧代次消息均拒绝。

RevalidatedIn 仅由 Gateway 为当前连接、当前待响应 Observation 的回执设置，不在 wire 或模型输入中提供。服务只在原事实与当前 Binding 属于同一 run、operation 匹配、RevalidatedIn 等于当前绑定时接纳旧代次的主动复核回执；保留原 Binding，不伪造发生代次。普通旧消息没有此凭据；新 run 不能使用它接纳回档外结果。

Reconcile 在 NPC lane 内执行：

| 事实 / 条件 | 处理 |
| --- | --- |
| satisfied | succeeded；生成 result_id，保存事实来源，消费 wake |
| unsatisfied | failed；保存真实原因，消费 wake |
| 明确的 interrupted | failed，原因是游戏执行中断，不归责玩家 |
| progress + WaitUntil | waiting + NextWakeAt 原子提交；晚到回执已经过截止时立即协调 |
| 仅普通 progress | 保存进展；根据是否仍在动作中/仍需决策返回 observe 或 decide |
| 截止时没有充分证据 | 返回 observe；有界技术协调后仍未知则 paused/evidence_unconfirmed |
| 未到期且无新事实 | 返回 settled，保留 waiting 和原 wake |

一个 Task 每次进入终态只产生一个不可变 Result。RecordCleanup 单独更新租约收尾状态，不改变 Result 指纹。控制权交接成功记录 handed_off；无法确认记录 unconfirmed。

- [ ] 写证据重复/冲突、错误 operation、旧代次、启动 revision 与当前 revision 不同的测试。
- [ ] 写“satisfied 已接纳后 deadline 不得覆盖”的竞争测试。
- [ ] 实现确定性映射和 BeginWake / Reconcile 的 CAS，不根据 opaque 中的 met/expired 字符串分支。
- [ ] 验证上述路径没有 model/provider 依赖。

```powershell
go test ./runtime/internal/task -run 'TestEvidence|TestOperation|TestReconcile|TestCleanup' -count=1
```

### 9.1-D：可靠 wake 与不确定执行

修改范围：wake、service、wake_test、sqlite_store_test。

状态为 `pending → claimed → enqueued → running → consumed`。认领只针对 ready 世界、相同 clock 且 due_tick 已到的项；retry_after 是现实技术重试时间，不推进游戏时间。

同 owner/task 的未消费 wake 使用 partial unique 约束，保证最多一份可执行资格。新 Evidence 提前已有 pending wake；若 wake 正在执行则只登记 inbox/needs_reconcile，由当前执行收敛后产生下次 wake。Wake.ExpectedRevision 用于 admission，当前执行自己的状态推进不使自己的消费资格失效；消费仍校验 wake_id/claim_id/绑定。

- [ ] ClaimDue 原子认领、递增 attempt，生成独立 claim_id；不把 attempt 当业务 revision。
- [ ] MarkEnqueued 在 lane admission 打开前持久提交；失败通过 ReleaseClaim 退回 pending。
- [ ] BeginWake 再校验绑定、claim、revision、deadline；过期队列项失去执行资格。
- [ ] Task 进展事务消费当前 wake 并产生后续 wake；同 Task 同 revision 同原因不重复创建。
- [ ] 重启把 claimed/enqueued 变回可投递项；running 变 needs_reconcile，不恢复旧函数栈。
- [ ] FinishAttempt 区分认知无进展与技术观察失败，分别计数，连续三次进入明确暂停。

```text
TestWakeAdmission
  创建 T(wake=200)，now=199：ClaimDue 为空
  now=200：第一次认领 W，第二次扫描无第二个 claim
  模拟 lane full：ReleaseClaim，W 仍存在
  再次认领并 MarkEnqueued、BeginWake：只得到一个有效执行资格
  提交 wait(300)：旧 W consumed，新 W2 pending
  重复 BeginWake(W)：不再执行；新游戏时间 300 才能认领 W2
```

```powershell
go test ./runtime/internal/task -run 'TestWake|TestNoProgress|TestRestart' -count=1
```

### 9.1-E：完整快照与 working head

修改范围：checkpoint、sqlite_store、checkpoint_test。

快照包含完整 Record、Result、幂等凭据和 wake；序列化前稳定排序，checksum 为规范化快照字节的 SHA-256。保留 Source.GameTime 的 presence 和原始 JSON 数值，不把它转为浮点数。运行态只保留 needs_reconcile 与关联，不保存 queue、goroutine 或 controller。

- [ ] PrepareCheckpoint 先校验/接纳本次保存附带的最终 Evidence，再 fence 旧 generation，并在同一数据库事务保存完整快照和屏障状态。
- [ ] 同 save_request_id 重试返回同一 Prepared；不同内容使用同 ID 时冲突。
- [ ] ActivateWorld 在同 run 中使用 working head；新 run 中严格按 ref 恢复 tasks 与 wakeups 集合，快照本身不可修改。
- [ ] 新 run 的 absent 引用仅在无既有世界任务状态时初始化空集合；unconfirmed、缺失、损坏、错误 world 引用暂停，不选择其他快照。
- [ ] 重新分配 generation，恢复旧版本 Task 的时候只保留快照事实；旧回调不能提交。
- [ ] FinishCheckpoint 及屏障有界解除保持幂等，游戏保存成功与否不决定下次加载选择哪个引用。

```text
TestCheckpointRestoresPriorState
  now=100 创建 T(wake=200, deadline=300)，保存 C1
  T 进入 waiting(progress=arrived, wake=300)，保存 C2
  satisfied 使 T 成功，另创建 T2
  新 run 加载 C1：只有初始 T；加载 C2：T 为 arrived/wake=300
  同分钟创建和更新也按 C1/C2 精确区分
  同 run Runtime 重开：使用最新 working head，不回到 C1/C2
  错误 world 引用、checksum、schema 和 unconfirmed：旧数据不变，任务暂停
```

```powershell
go test ./runtime/internal/task -run 'TestCheckpoint|TestWorldHead|TestSaveBarrier' -count=1
go test ./runtime/internal/task ./runtime/internal/session -count=1
```

## 6. 交接条件

- [ ] 第 3.1 节公开接口存在，全部上述测试通过；缺少实现不能通过空返回、跳过或只测 mock 代替。
- [ ] task 包不依赖 LLM/gateway/game；内部 TaskSpec 用例没有 Proposal 和玩家来源。
- [ ] 创建、结果、后续 wake 与快照有故障窗口测试；任务库重开后能够复现。
- [ ] 在验收记录填写命令、输出摘要、阻断和未执行项，再自动进入 Phase9.2，不等待中途人工 review。

仅本阶段通过表示 Task 内核可用，不表示游戏预约、实机存档或 Phase9 整体已验收。
