# GameAgent MVP0 Phase9.4 检查点与结果记忆技术开发方案

> **Status:** Implementation Plan — 开发基线；实现与验收状态以验收记录为准
> **Date:** 2026-09-13
> **执行方式:** 以 9.4 为交付和用户验收周期。每个独立模块完成相关测试和 `git diff --check` 后创建本地提交并连续推进，无需逐提交等待用户 CR。阶段代码完成后执行整体自动化回归与 agent 内部 review，修正使用独立 `fix:` 本地提交并复验，再由用户集中 CR、review 和实机验收；用户确认通过后进入 9.5。Phase9.5 执行最终整分支/系统审查。
> **Goal:** 任务随游戏检查点准确恢复，真实结果在无模型确认的情况下进入 History，并可在后续对话引用。
> **Architecture:** 游戏保存精确引用，Runtime 保存不可变全量 Task 快照；Task 是执行权威，History 是认知来源。
> **Tech Stack:** SQLite、gRPC、SMAPI Saving/Saved、现有 Phase8 History/Context。
> **Spec:** [Phase9 总方案](./GameAgent%20MVP0%20Phase9%20技术开发与验收方案.md)
> **依赖:** [9.1 内核](./GameAgent%20MVP0%20Phase9.1%20Runtime%20Task%20内核技术开发方案.md)、[9.2 协议](./GameAgent%20MVP0%20Phase9.2%20Runtime%20Tools%20与调度接入技术开发方案.md)、[9.3 游戏执行](./GameAgent%20MVP0%20Phase9.3%20Stardew%20行动与交互技术开发方案.md)
> **后续:** [Phase9.5 联调验收与交付](./GameAgent%20MVP0%20Phase9.5%20联调验收与交付技术方案.md)

## 1. 全局约束

- 保存桥接、加载恢复和其他真实游戏依赖代码以 9.0 可行性门通过为硬前提；游戏环境不可用时不得以 fake 替代相关结论。
- 从 `e482923` 实现基点在 `codex/phase9-durable-task` 开发，保留 `daf4f98` 作为已检查代码基线；每个提交保持可构建、可测试，且包含实现及其测试。未获用户明确授权不得 `git push`。
- 使用 9.1 已实现的快照内核；不另建 Adapter 任务镜像或第二份未来任务列表。
- 不支持离线完整保存 Task。Runtime 断线时游戏照常保存，任务引用标记 unconfirmed。
- 不恢复 LLM 栈、旧 Turn、goroutine、controller、UI 或 Action waiter。
- Task checkpoint 与 Phase8 Summary checkpoint 分开；不扩展全量聊天分支回滚。
- 原 Task 成功不随 History 写入失败或对话失败回滚；成功不等于 UI 已结束。
- 本阶段不引入全局 outbox、跨连接事件补发或完整 pending Action 恢复。

## 2. 文件边界

| 文件 | 责任 |
| --- | --- |
| `adapters/stardew/src/Tasks/TaskCheckpointBridge.cs`、`CheckpointMarker.cs` | 新增有界保存交接与 SaveData 标记 |
| `adapters/stardew/src/Runtime/RuntimeClient.cs`、`ProtocolMapper.Task.cs`、`src/ModEntry.cs` | Saving/Saved、控制回复、主线程保存与绑定、证据发送路径与代次校验 |
| `adapters/stardew/src/Tasks/TaskExecutionDriver.cs`、`MeetingWaitMonitor.cs`、`TaskSourceContextStore.cs` | 保存后保留在途 operation 原始 binding 与等待交接 |
| `adapters/stardew/src/Runtime/AdapterConfig.cs`、`RuntimeWorldContext.cs` | 保存交接超时配置与范围校验、绑定代次应用 |
| `adapters/stardew/tests/TaskExecution.Tests/TaskCheckpointBridgeTests.cs` | 正常、超时、迟到、错误引用、主线程死锁测试 |
| `runtime/internal/task/checkpoint.go`、`checkpoint_test.go` | 9.1 内核集成、最终 Evidence、generation 与故障窗口 |
| `runtime/internal/gateway/task_protocol.go`、`task_control.go`、`world_registry.go` | 保存屏障生命周期、当前连接映射和恢复准入 |
| `runtime/internal/gateway/checkpoint_integration_test.go` | 新增真实 gRPC/SQLite 的保存、断线和重启测试 |
| `runtime/internal/task/result_history.go`、`result_history_test.go` | 不可变 Task 结果 DTO 与结果映射 |
| `runtime/internal/gateway/result_history.go`、`result_history_test.go` | 终态提交后发布 task_result 来源 |
| `runtime/internal/task/service.go`、`sqlite_store.go`、`service_test.go`、`sqlite_store_test.go` | 9.4-D 增加 ListRecentResults，查询当前工作集最近已确认结果 |
| `runtime/internal/memory/history.go`、`history_text.go`、`sqlite_history.go`、`in_memory_history.go` | task_result 类型、兼容校验、投影与存取 |
| `runtime/internal/memory/task_result_history_test.go` | 新增混合来源、幂等与旧数据兼容 |
| `runtime/internal/agent/task_context.go`、`task_context_test.go` | 扩展 9.2 共用任务快照，加入最近结果、恢复信息与请求关联 |
| `runtime/internal/tool/types.go` | RuntimeCallContext 承载最近结果与恢复信息 |
| `runtime/internal/agent/config.go` 及 config tests | max_task_context_tokens 加载、默认值与校验 |
| `runtime/internal/context/task_projection.go`、`context.go`、`renderer.go`、`request_sizing.go` | 扩展 9.2 任务投影，加入结果/恢复明细与分项预算 |
| `runtime/internal/trace/trace.go`、`turn.go` | 仅新增 task_id、result_id、checkpoint_id 与状态码字段及对应断言，不扩展 trace 结构 |

Memory 侧的 HistoryTaskResult 是独立 DTO，只依赖通用标量和已有来源类型；不要让 memory 包导入 task 包，避免 result_history.go 反向依赖形成循环。

## 3. 游戏保存合同

### 3.1 标记

SaveData 键固定 `runtime-task-checkpoint`，字段与 9.2 TaskCheckpointRef 对齐：

```json
{
  "schema_version": 1,
  "game_id": "stardew-valley",
  "world_id": "world-example",
  "status": "confirmed",
  "checkpoint_id": "checkpoint-example",
  "checksum": "sha256-of-runtime-task-snapshot"
}
```

unconfirmed 使用同样的身份/schema 字段、status=unconfirmed 和明确 reason，不保存可执行 checkpoint_id/checksum。absent 仅表示存档没有键，不是覆盖已有标记时可写出的替代值。

从未建立 Task 绑定且存档原本无该键的普通游戏，保留 absent。已有标记或本次运行已建立 Task 工作状态时，任务功能禁用/断线/未完成协商都属于无法确认任务保存，写 unconfirmed；不能保留旧 confirmed 冒充本次保存。

SaveData 通过字段名明确的字典写入，避免依赖序列化器把 PascalCase 自动转换为 snake_case。读取严格校验 schema、身份、状态和必填字段；非法类型不自动强转为合法标记。

仅复制游戏存档不能迁移 Runtime 的任务快照。展示与备份说明应同时指明游戏保存与 Runtime Task 数据；缺少引用目标时暂停任务，不能从 History 重建。

### 3.2 桥接接口与线程

```csharp
public interface ICheckpointTransport
{
    Task<CheckpointPrepared> PrepareAsync(CheckpointPrepare request, CancellationToken token);
    Task FinishAsync(CheckpointFinish request, CancellationToken token);
}

public sealed record CheckpointMarker(int SchemaVersion, string GameId, string WorldId,
    string Status, string? CheckpointId, string? Checksum, string? Reason);

TaskCheckpointBridge:
  public CheckpointMarker PrepareForSaving(CheckpointPrepare request, TimeSpan timeout);
  public void OnSaved();
  public void OnSaveAborted(string reason);
  public void OnDisconnected();
```

TaskCheckpointBridge 的构造输入为 ICheckpointTransport 与保存/诊断回调；游戏状态快照由主线程在调用前取得。PrepareForSaving 只等待有限 transport 结果，不读取或修改 NPC。

网络接收线程直接完成按 correlation_id 匹配的 TaskCompletionSource，使用 RunContinuationsAsynchronously；CheckpointPrepared 不能排回被 Saving 阻塞的 MainThreadDispatcher 才解锁。任何 Game API 仍只在主线程执行。

保存超时初始 5 秒，允许范围 1–8 秒并在配置校验中拒绝越界值，保持严格小于 Runtime 10 秒屏障兜底；技术结果晚到后仅清理对应 pending 请求，不得改写已经完成的游戏存档。

pending 匹配以 CheckpointPrepare 消息的 correlation_id 为权威键，并校验回复的 save_request_id 与 scope 与该请求一致；键或身份不一致的回复不解除等待，只记录诊断。

SMAPI 没有保存失败事件，OnSaveAborted 的真实触发信号是 Saved 之前先到达的 Saving、SaveLoaded、ReturnedToTitle，或本次交接尚未收到 Saved 时的 DayStarted；该信号与超时同样写入 unconfirmed 标记并结束本次交接。

SMAPI 另有 `SaveCreating` / `SaveCreated`，属于从零创建存档的流程，不经过 `Saving` / `Saved`。该流程不进入任务保存交接：此时该 run 尚无任务状态，没有快照可写，也没有旧标记需要更新。若后续把新游戏建档纳入范围，需要重新评估这两条路径与标记初值。

### 3.3 保存顺序

```text
主线程 Saving
  关闭本 world 新任务动作准入
  结束 travel/approach 临时租约；等待中的到达交接与控制占用保持不动
  采样 Clock 与尚未提交的最终 Evidence
  生成 save_request_id，发送 CheckpointPrepare

Runtime 接收
  置内存保存屏障，authority epoch 递增，关闭本 world 创建/更新/wake/Action 准入
  在 world 短临界区内校验原绑定，UpdateClock 同步保存采样
  PrepareCheckpoint 在事务内接纳 final_evidence、fence 旧代次并持久化完整快照
  WorldRegistry 保存 Prepared.Head；执行准入仍关闭
  返回 CheckpointPrepared(新 generation, 精确引用)

主线程收到有限结果
  成功：写 confirmed 标记
  超时/断线/不匹配：写 unconfirmed 标记
  允许游戏完成保存

Saved / 明确 aborted
  已取得匹配 Prepared：用 Prepared.scope 和原 save_request_id 发送 CheckpointFinish
  清除内存保存屏障；内核屏障由 Finish 解除，兜底到期同样解除
  保持同一 run，必要时先用新代次 WorldClockUpdate 推进 head.Clock
  发起 WorldBinding(G+1, 与 head 等值 Clock) / WorldBindingReady 握手
  ready 后以当前代次重新观察在途 operation；证据保持原 binding，由 Runtime 写入 RevalidatedIn
  仅恢复符合当前事实的任务调度
```

等待中的 Task 在保存窗口内保持 waiting：Saving 只结束 travel/approach 的临时租约，不结束到达交接，也不得为健康等待生成 interrupted、control_lost 等终态 Evidence，否则内核会把它收口为 StateFailed。

保存后 Adapter 保留在途 operation 的原始 binding 与 start_revision，不按新代次改写来源。内核要求 evidence.binding 等于 operation.binding，跨代证据只在 Runtime 侧重新确认后被接纳：Runtime 在观察回执路径确认同一 run 内旧代次的 operation 后写入 RevalidatedIn=当前代次，事件推送路径不接受跨代证据。因此保存后等待中的证据改由 Observe 回执携带；Adapter 的证据构建把代次等值校验放宽为同一 run 内代次不大于当前，TaskControl 使用当前代次。

保存不等待模型完成。模型返回、来源提交和 Action 发送在操作前检查 generation/保存屏障；旧请求即使已经得到模型输出也不能跨屏障执行。

final_evidence 保留发生时的绑定，Runtime 在 fence 前接纳。确认回复中的新 generation 生效后，Adapter 不再发送旧代次的新执行请求。复制快照不以调用 History 成功为前提。

屏障兜底超时为 10 秒，必须长于单次保存交接超时；超时解除不表示 world 自动 ready，仍需双方当前绑定一致。Saved 确认丢失时，下次加载以磁盘游戏标记为准。

### 3.4 时钟、代次与恢复握手

- 首次 Prepare 处理在 WorldRegistry 的同一 world 短临界区中执行 UpdateClock → PrepareCheckpoint，防止其他 Clock 更新插入两者之间。UpdateClock 按单调序号校验，PrepareCheckpoint 使用已同步的精确 Clock。网络等待和游戏保存不持有该临界区。相同 save_request_id 的重试保留原 scope/Clock/final_evidence，进入内核幂等 Prepare 路径，不先以旧代次重做 UpdateClock。
- Prepared.scope 来自 Prepared.Head.Binding；CheckpointFinish 使用该新代次，不使用请求 Prepare 时的旧代次，也不自行加一。saved 表示是否收到真实 Saved，不代表是否收到 Prepared。FinishAsync 完成只表示发送完成，不能当作 Runtime 已解除屏障的确认。
- 正常 Finish 后，由同一 stream 上后续 WorldBinding 发起 ready 握手；Gateway 按顺序处理 Finish、内存屏障清除与绑定恢复（见 3.5）。Task-ready 仅由当前有效 WorldBindingReady 恢复，Prepared 本身只确认快照和代次，不授权恢复行动。
- Prepared 超时/丢失时，本次游戏保存写 unconfirmed。Saved 或明确 aborted 后保持任务准入关闭，以原 world_run_id 发起有界绑定恢复；未取得 Prepared 不发送猜测代次的 Finish。若 Finish 丢失或屏障尚在，恢复入口经内核的 10 秒屏障兜底及旧绑定收尾后，才允许返回 ready。绑定失败保持暂停并给出原因，连接失效时等待显式重连，不引入 Phase10 自动重连机制。
- 同 run 的恢复握手沿用本 run 进入世界时已验证的加载引用，选择最新 working head；本次保存的 unconfirmed 标记保持原样。新 SaveLoaded 必须读取磁盘实际标记并生成新 run，unconfirmed 仍导致暂停。恢复握手不把未确认保存变成 confirmed，也不回放旧动作。
- 迟到 Prepared 只清理对应 pending 请求，不能覆盖新绑定、恢复准入或改写标记。绑定请求与回复按当前 stream 和 correlation_id 匹配，旧请求回复不能使新一次保存提前 ready。

### 3.5 内存保存屏障与消息取值

WorldRegistry 的内存保存屏障在存在时使绑定返回 ErrSaveInProgress，因此屏障的清除顺序是硬约束，不能只依赖内核 10 秒兜底；内核兜底只解除持久化屏障，不解除内存屏障。

| 事件 | 内存保存屏障 | 内核屏障 | 任务准入 |
| --- | --- | --- | --- |
| CheckpointPrepare 受理 | 置位，authority epoch 递增 | 事务内置为 prepared | 关闭 |
| CheckpointFinish 成功 | 清除 | 清除 | 仍关闭，等绑定 ready |
| 明确 aborted | 清除 | 本次保存按未完成收尾 | 关闭，走恢复绑定 |
| Prepared 超时或回复丢失 | 保留 | 10 秒兜底解除 | 关闭，直到恢复绑定 ready |
| 流断开 | 保留 | 保留到恢复入口处理 | 关闭，等待显式重连 |

恢复入口在内核屏障解除或到期后清除内存屏障再执行绑定，显式重连不因屏障永久阻断；屏障未清除时的失败是明确的 ErrSaveInProgress，不是暂停。

保存窗口内断线或重绑时，断线收尾会因内核屏障仍在而返回 ErrSaveInProgress，WorldRegistry 会把该 world 标记为失败，而该标记在当前实现中没有清除路径，之后所有绑定都返回 ErrWorldNotReady。9.4 必须让该标记可恢复：恢复入口在内核屏障解除或到期后同时清除失败标记，或对 ErrSaveInProgress 不写失败标记；失败原因写入诊断，屏障解除且绑定成功后才重新开放准入。

清除内存屏障不等于重新开放准入：保存窗口内 slot.ready 保持 false，准入只由有效的 WorldBindingReady 恢复，避免 Adapter 尚未应用新代次时重新派发任务。

| 消息 | run / generation | checkpoint ref | Clock |
| --- | --- | --- | --- |
| 首次或新 run 的 WorldBinding | 新 run，generation=0 | 磁盘标记：confirmed 精确引用，或 absent（该存档无标记且该 world 无历史状态） | 当前世界时钟 |
| 保存后同 run 的 WorldBinding | 同一 run，generation=Prepared 新代次 | absent（Runtime 使用 working head） | 与 Runtime 当前 head 完全等值；已前进时先发等代次 WorldClockUpdate |
| Runtime 重启后同 run 的 WorldBinding | 同一 run，generation=0 或等于持久化代次 | absent | 不回退且 sequence 前进 |
| WorldClockUpdate | 当前 head 代次 | 不适用 | 单调不回退 |
| CheckpointPrepare | 当前 head 代次 | 不适用 | 保存采样值 |
| CheckpointFinish | Prepared 新代次 | 不适用 | 不适用 |

同实例重绑要求 Clock 完全等值，Runtime 重启后的重绑要求时钟不回退且序号前进；unconfirmed 标记使绑定进入 paused 并保留原因。

## 4. 加载、重启与故障边界

### 4.1 恢复选择

| 输入 | 唯一恢复路径 |
| --- | --- |
| Runtime 重启，游戏仍是同一 world_run_id | 最新 working head；重新观察未确认 operation |
| 同一 Runtime，Adapter 断线后显式重连（同一 run） | 内核屏障解除或到期后清除内存屏障；以当前 head 的等值 Clock 重新绑定当前代次；绑定 ready 前保持准入关闭。屏障未清除时绑定返回 ErrSaveInProgress |
| SaveLoaded 产生新 world_run_id | 该存档 checkpoint_id 指向的完整快照 |
| 新 run，存档 absent 且 Task DB 无该 world 状态 | 初始化空任务集 |
| 新 run，absent 但已有 world 状态 | paused；不得猜测应该沿用还是清空 |
| unconfirmed/丢失/损坏/错误 world/schema | paused，保留旧数据和明确原因 |

新 run 恢复按事务替换整个工作集。快照之外的新 Task 消失，快照内保存时 waiting 的 Task 可以从后来 succeeded/cancelled 恢复为 waiting；created_at 不承担版本选择职责。

保存后的同 run 恢复要求绑定请求携带与 Runtime 当前 head 一致的 Clock；同实例重绑要求完全等值，值已前进时先发等代次 WorldClockUpdate 再绑定，不得用旧 Clock 或旧代次直接绑定。

恢复后操作和来源分为两类：快照中已经确认的历史事实可保留；实际执行资格必须使用当前 generation 和当前游戏观察。既有 operation 的证据保持原始 binding，经观察回执由 Runtime 写入 RevalidatedIn；Adapter 不为旧 operation 改写来源代次，也不为旧代次发送新执行请求。TaskSpec 中的约定可恢复，旧玩家交互来源和旧 controller token 不可恢复。

### 4.2 故障窗口

| 注入点 | 断言 |
| --- | --- |
| SQLite 快照提交前失败 | 游戏写 unconfirmed，旧快照可保留 |
| SQLite 成功，游戏保存失败 | 新快照未被存档引用，下次按旧引用恢复 |
| 游戏保存成功，Saved ack 丢失 | 新引用正常恢复，不能因未收到 ack 宣告损坏 |
| Prepared 晚于本次保存超时 | 不改变已写 unconfirmed 或之后一次保存的标记 |
| Prepared 已持久提交但回复丢失 | 同 run 经屏障兜底和绑定握手恢复 working head；磁盘仍 unconfirmed，新 run 加载仍暂停 |
| Finish 丢失或其发送结果不确定 | ready 前保持关闭；有界绑定恢复后使用 Runtime 返回代次，禁止重发世界动作 |
| Clock 更新与 Prepare 交错、旧代次 Finish | 保存采样同步后精确匹配；旧代次 Finish 被拒绝，不能解除其他保存屏障 |
| 相同 request_id 不同内容 | 冲突，无半更新快照 |
| 保存时旧 LLM/Action 回调到达 | 不能越代推进新工作集 |
| Runtime 断线时保存并重读 | Task paused，普通游戏可继续，不自动赴约 |
| 保存窗口内等待中的 Task | 仍为 waiting，不产生 interrupted、control_lost 终态证据 |
| 保存后同 run 恢复的绑定顺序 | Finish → 清内存屏障 → 等值 Clock → 绑定新代次 → ready；屏障未清除时绑定返回 ErrSaveInProgress |
| 保存后 Adapter 在途 operation 来源 | 保持原始 binding 与 start_revision；经观察回执由 Runtime 写入 RevalidatedIn，事件推送路径的跨代证据被拒绝 |
| 保存窗口内断线或重绑 | 不写不可恢复的失败标记；屏障解除后可重新绑定并恢复，失败原因可诊断 |

这些用例使用数据库/transport 故障注入，不删除真实存档来制造异常。

## 5. Task 结果到 History

### 5.1 新来源类型

History 新增 `HistoryKindTaskResult = "task_result"`，版本为 1。HistoryBatch 增加 `TaskResult *HistoryTaskResult`，JSON 名为 task_result 并 omitempty，保持旧来源序列化内容及键/指纹不变。

HistoryTaskResult 的固定字段：ResultID、TaskID、Revision、State、Reason、OccurredAt、EvidenceRefs。owner 使用 HistoryBatch.Owner；原始事件/时间/ContextFact 使用现有来源字段，缺失 event/turn 时保持缺失，不创建虚假 ID。

- task_result 必须有 TaskResult、稳定 result_id、已确认的结果状态和时间关联；允许空 turn_id、无模型 Steps。
- terminal_turn 仍要求原有 turn_id 和合法终态；legacy_recent 仍按原合同验证。
- 把 CanonicalHistoryBatch 的“所有来源必须有 turn_id”前置检查移入各 kind 分支，不能为新类型放松旧类型要求。
- SQLite/InMemory 的 AppendHistory 和读取 allowlist 同步支持新 kind；旧 fixtures 和旧 fingerprint 测试必须逐项保留。
- 裁剪读取按 kind 决定 key 段数，task_result 的键为 [game_id, world_id, entity_id, result_id, "task_result", 1] 六段；kind allowlist、段数与身份校验必须同时支持 task_result，否则裁剪后读取会判为损坏。
- 新类型出现于旧二进制时不保证可读；升级验收要求新实现读取全部旧数据，不把 schema/kind 不兼容静默当作空历史。

批次幂等键为 `[game_id,world_id,entity_id,result_id,"task_result",1]`。同键同内容返回已有 source；同键不同内容返回 ErrHistoryConflict。新时间线重新发生的结果生成新 result_id，不能覆盖旧时间线来源。

### 5.2 写入与投影

发布器由同时依赖 task 与 memory 的调用方实现，接口形状如下：

```go
type ResultHistorySink struct {
    Store memory.HistoryStore
}

func (s ResultHistorySink) Publish(ctx context.Context, owner session.AgentSessionKey, result Result) (memory.HistorySource, error)
```

ResultHistorySink 只接收已提交、内容不可变的 Result；不得从模型进展文字生成事实，也不得在 Task 事务中等待 History。发布在终态提交完成之后由同时依赖 task 与 memory 的调用方执行，task 包保持仅依赖 session 与 idgen 的内核边界，不因结果发布引入 memory、model 或 protocol 依赖。

状态提交 → 结果 History 发布与 cleanup 分别执行。每个操作使用独立有限上下文；History 慢或失败不能延迟 NPC 本地释放。失败保存诊断，Result 仍在 TaskStore，后续 Task Context 仍可读取；不引入全局 History 补发系统。

ContextFact 表达实际 met/expired/interrupted；普通移动成功只表达“到达点位”，创建成功只表达“约定已登记”。task_result 与到达对话的 terminal_turn 可以关联同一 fact_id，但对话失败不抹去前者。

HistoryTextFields 以 task_result 明确呈现结果原因和事实，不推断说话者。复用现有 Summary、检索、时间可见性和原文读取；新增类型不意味着修改这些算法或留存规则。

## 6. 当前 Task Context

复用 9.2 的 agent/task_context.go 读取入口及只读快照，在普通玩家交互和 Task Turn 创建模型请求前加入最近已确认结果与恢复信息；Renderer 不访问数据库。当前任务仍使用 ListActive(owner, 1) 或 Service.Read，最近结果调用 ListRecentResults(owner, 3)。当前任务身份和版本继续使用展示快照校验；结果只作为事实投影，不赋予执行资格。

### 6.1 最近结果查询合同

```go
func (s *Service) ListRecentResults(ctx context.Context, owner session.AgentSessionKey, limit int) ([]Result, error)
```

此接口归入 9.4-D，在现有 Service/SQLiteStore 实现。在数据库只读一致快照内按完整 owner 筛选当前工作集中包含已确认 Result 的终态 Task，以 Result.OccurredAt 倒序、Result.ID 升序排序后应用 LIMIT；不能先调用 List(owner, limit) 再过滤或排序。保持记录完整性校验；合法 owner 下无结果或 limit <= 0 返回空列表，非法 owner、损坏记录和存储错误按现有错误合同返回。结果深复制，调用者修改不影响存储。

实现边界：Result 位于 tasks.record_json，本查询在只读一致快照内筛选该 owner 的终态 Task、解析其中的 Result 后排序并应用 LIMIT，不新增表、索引或结果缓存；扫描上界为该 world 的 Task 数，受 store_options.max_tasks_per_world 约束，接口自身的返回上限固定为 3。

查询只读当前 tasks 工作集，不合并历史 checkpoint 或 History 中的旧时间线结果。读档事务替换工作集后，后续查询仅返回恢复后仍成立的结果。agent 在同一 world 短临界区内读取当前任务与最近结果并核对当前绑定；发生回档/重绑时丢弃旧投影，执行前仍校验 Binding 和模型实际观察的 ExpectedRevision。保持现有 List / ListActive 语义，不新增协议、模型工具、数据库表或结果缓存。

### 6.2 投影与预算

Task 投影字段：task_id、目标、权威 state/revision、next_wakeup_at/deadline、wake_reason、最新已确认进展/结果 refs、恢复/暂停原因。模型进展说明与权威结果分开标注。

新增 max_task_context_tokens，默认 2048，通过既有 budget 配置加载并在 runtime/config/agent.json 可显式列出；它是对 Task 块的独立上限并计入 max_user_message_tokens / max_request_tokens，总预算不提高也不重复扣减。Task 块当前渲染在 history 区，本阶段先把它拆成独立 section 再计量，否则分项预算与裁剪顺序无法生效。裁剪顺序为历史进展说明 → 较旧结果 → 可选明细；以下字段不可被裁剪或裁剪成相反含义：当前 Task 的身份、状态、期限、恢复异常，以及 Task Context Contract Visibility ADR 定义的 contract。contract 在创建期受 4096 字节上限约束，因此它可以进入不可裁剪集合而不撑爆总预算。必需字段也放不下时明确 budget 错误，不继续执行缺少任务约束的模型请求。

当前 Task 状态与 Memory 冲突时，Task 决定执行状态，Observation 决定当前世界状态。Memory 中旧约定或回档外结果不能创建/完成/取消 Task。Phase8 既有时间可见性继续生效，不声称所有历史都按 Task checkpoint 回滚。

## 7. 开发任务

以下单元是 9.4 内部开发、测试和提交边界，由 agent 完成内部 CR 后连续推进。保存线程交接等必要实机前置证据在依赖实现前取得；本节保存、加载、回退与结果认知的实机检查项汇总为阶段验收步骤，由用户在代码收口后集中验收。阶段交付包含提交清单、变更说明、自动化结果、已知限制和实机验收步骤。

实机检查项以 9.3 实机验收通过为前置。9.3 实际达成的是：在实机标注点位上跑通 met 与 expired 两条完整闭环；固定路线的生产白名单仍只包含 `beach_meeting_spot`，新增点位为通配路线且未记录实测行程（见验收记录第 9.2 节）。9.3 实机未过时，9.4 先交付机制证据并如实标注实机限制。

### 9.4-A：保存桥接

提交边界：Adapter 保存桥接与 Runtime 保存屏障分别形成独立可审查提交。屏障一旦置位，解除路径依赖 9.4-B 的恢复入口，因此 A 的提交必须同时包含屏障可解除的最小路径（复用 10 秒兜底解除内核屏障并完成绑定恢复），不得留下保存后无法重新绑定的中间态。

- [x] 写 CheckpointMarker 的 confirmed/unconfirmed/非法字段 round-trip 测试。
- [x] 写网络回复不经过主线程 dispatcher 也能完成 Saving 等待的测试；设置有限测试截止，死锁必须失败。
- [x] 实现 request_id pending 表、有限等待、迟到拒绝、OnSaved/OnSaveAborted/断线幂等收尾。
- [x] 明确 OnSaveAborted 的真实触发信号与保存超时上界（1–8 秒，严格小于 Runtime 10 秒兜底），并覆盖越界配置被拒绝。
- [x] 覆盖内存保存屏障生命周期：Prepare 置位后，Saved 路径按 Finish → 清屏障 → 等值 Clock → 绑定 ready 执行；屏障未清除时绑定返回 ErrSaveInProgress。
- [x] 覆盖保存窗口内断线或重绑：失败标记可恢复，屏障解除后重新绑定成功，不出现进程内永久 ErrWorldNotReady。
- [x] 接线 ModEntry.Saving/Saved，与 9.1 PrepareCheckpoint/FinishCheckpoint 对齐。
- [x] TestCheckpointPrepareClock 验证保存采样先 UpdateClock 后 Prepare、同请求重试不重复同步旧代次；失败保持 task-not-ready。
- [x] TestCheckpointFinishGeneration 验证 Prepare 使用 G、Prepared 返回 G+1、Finish 使用 G+1；旧 G 被拒绝，发送完成或单独 Prepared 均不能开启执行准入。
- [ ] 实机核对本次 SaveData 引用确实随游戏保存落盘，不能只看内存对象。

```powershell
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --filter "FullyQualifiedName~TaskCheckpointBridgeTests|FullyQualifiedName~CheckpointMarkerTests"
go test ./runtime/internal/gateway -run 'TestCheckpointPrepare|TestCheckpointFinish|TestSaveBarrier|TestCheckpointBarrier' -count=1
```

### 9.4-B：恢复和故障窗口

- [x] 为第 4 节每一行给出断言，按故障族合并为四组集成测试：Prepared 丢失或超时、Finish 丢失或结果不确定、SQLite 与游戏保存失败、断线后重连续传；每组合并覆盖多行，断言不删减。使用实际 SQLite 和 in-process gRPC transport。
- [x] 同 run 重连时仅主动重新观察已登记 operation；旧 stream 推送仍拒绝。
- [x] 保存窗口内等待中的 Task 保持 waiting，保存后仍可 met 或 expired；证据经观察回执携带原 binding 并由 Runtime 写入 RevalidatedIn；健康等待不产生 control_lost 终态，也不新建第二次 travel。
- [x] TestCheckpointBindingRecovery 分别丢弃 Prepared、Finish 和 ready 回复，验证执行准入不提前开启；恢复后使用当前代次且无重复动作，迟到回复不污染下一次保存。Prepared 丢失后，同 run working head 恢复与新 run unconfirmed 暂停分别断言。
- [x] 新 run 使用精确引用恢复，覆盖“保存后完成的 Task 回到 waiting”。
- [x] 验证快照中的旧交互来源/租约不复活，未确认 world 操作先 Observe，且跨代证据只经观察回执被接纳。
- [ ] 在游戏执行跨日保存、退出加载和回退旧保存；未知恢复状态明确暂停。

```text
TestRuntimeRestartVsSaveLoaded
  C1：T waiting，wake=200
  同一 run 中 T 进展为 waiting，wake=300；关闭 Runtime Store 后重新打开
  同 run 绑定 → wake=300
  新 run 绑定 C1 → wake=200
  新 run 绑定 unconfirmed → paused，不能选择最新 300

TestTaskWaitSurvivesSave
  T waiting，Saving 只结束 travel 临时租约，Prepared 代次为 G+1
  绑定 G+1 后等待继续，玩家按时到达 → 观察回执携带原 binding 的 met 证据，Runtime 写入 RevalidatedIn
  事件推送路径的同一条跨代证据被拒绝
  全程不出现 control_lost 终态，也不产生第二次 travel
```

```powershell
go test ./runtime/internal/task ./runtime/internal/gateway -run 'TestCheckpoint|TestRuntimeRestart|TestSaveLoaded|TestWorldHead|TestTaskWaitSurvivesSave' -count=1
```

### 9.4-C：独立结果来源

提交边界：History 支持与结果发布器分别形成独立可审查提交。

- [x] 先写 CanonicalHistoryBatch 的新类型接受/旧类型约束不放松测试。
- [x] 实现 TaskResult DTO、键、存取、HistoryTextFields；保持原字段序列化顺序和 omitempty 兼容。
- [x] 接入 ResultHistorySink，验证没有模型 Turn 时仍可写事实，重复结果没有重复来源。
- [x] History 写入失败不回滚 Task，不阻塞 cleanup；检查日志中的独立状态。
- [x] 回归 Summary 混合来源、时间过滤、中文检索、原文读取与既有迁移 fixture。

```text
TestTaskResultHistory
  没有 turn_id/Steps 的确证 Result R → task_result 来源成功
  相同 R 重复 Publish → 相同 source_id
  相同 result_id 改变 state 或文本 → ErrHistoryConflict
  旧 terminal_turn 缺 turn_id → 仍然失败
  读取 Phase8 fixture → key、fingerprint、文本保持原值
  Summary/检索返回 R 的实际事实，不能称为“NPC 已说出某句台词”
```

```powershell
go test ./runtime/internal/memory ./runtime/internal/task -run 'TestTaskResult|TestHistory|TestSummary|TestRetrieval|TestPhase8' -count=1
go test ./runtime/internal/memory -count=1
```

### 9.4-D：最终模型请求与交互关联

- [x] 实现第 6.1 节 ListRecentResults 及 TestListRecentResults：旧 task_id 排在前仍取到最新三条、时间并列、空结果、limit 边界、owner/world 隔离、返回值修改无副作用、损坏记录拒绝；原 List / ListActive 合同保持不变。
- [x] ListRecentResults 在只读一致快照内解析 record_json 后排序，不新增表、索引或缓存，不先 List 再过滤；断言扫描上界受 store_options.max_tasks_per_world 约束。
- [x] TestListRecentResultsAfterRestore 验证保存后产生的结果在读旧保存后不再出现在当前结果查询；History 发布失败时，仍能从已提交 Task Result 取得事实。模型请求构建期间绑定变化时，不混入回档前结果。
- [x] 在 9.2 最小 Task Context 上扩展结果、恢复信息及分项预算报告，覆盖普通对话引用最近结果；保留当前任务取消和观察版本校验测试。
- [x] 测试 met 成功后仍可使用到达来源 approach/对话，Task terminal 过滤不吞掉该事件。
- [x] 检查最终 model.Request，而不是只判断模型回复听起来合理。
- [x] 测试玩家爽约、NPC 路线失败、保存回退三种不同上下文，不让 Memory 覆盖当前任务状态。
- [x] Trace 只新增 task_id、result_id、checkpoint_id 与状态码字段并给出断言；没有断言支撑的字段本阶段不引入。

```powershell
go test ./runtime/internal/task -run '^TestListRecentResults' -count=1
go test ./runtime/internal/context ./runtime/internal/agent -run 'TestTaskContext|TestTaskArrival|TestTaskResultRequest|TestTaskBudget' -count=1
go test ./... -count=1
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj
```

## 8. 交接条件

- [ ] 跨日保存后任务仍按约定行动；Runtime 重启与读档恢复路径不同且正确。
- [ ] 保存窗口内等待中的任务保持 waiting，保存后仍可 met 或 expired，不被收口为失败。
- [ ] 保存屏障解除后断线重连可以完成绑定并恢复调度；屏障未解除时绑定明确失败并可诊断。
- [x] 保存、引用、加载和旧回调的故障窗口全部有自动化断言。
- [x] 后台 task_result 无需 LLM 即可入 History；后续请求有真实结果来源。
- [ ] Task 终态不提前结束真实 UI，UI 真正结束后恢复原生日程。
- [x] 旧 Memory / Context 测试通过；实机限制如实记录，各模块完成本地提交后执行阶段整体自动化回归与内部 review，问题修复并复验后交付 9.4 阶段结果，由用户集中 CR、review 和实机验收；用户确认通过后进入 Phase9.5 最终验收和整分支/系统审查。
