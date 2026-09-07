# GameAgent MVP0 Phase8.1 技术开发与验收方案

> **Status:** Implementation Plan Accepted
> **Date:** 2026-09-07
> **Phase:** Phase8.1 Persistent Recent Memory
> **Parent Plan:** [GameAgent MVP0 Phase8 技术开发与验收方案](./GameAgent%20MVP0%20Phase8%20技术开发与验收方案.md)
> **Memory Architecture Baseline:** [Memory架构设计](../summary/Memory/Memory架构设计.md) v0.1
> **Review Result:** Accepted for Phase8.1 code development
> **Review Required Before Coding:** Completed
> **Code Baseline:** `main` @ `fda66db`

---

# 1. 阶段目标

Phase8.1 只做一件事：

```text
将现有 Recent Memory 从进程内 InMemoryStore
升级为 SQLite 持久化 Store，
让同一 game_id + world_id + entity_id 的 Recent Memory
可以在 Runtime 重启后恢复。
```

Phase8.1 不改变 Memory 的认知含义，不引入长期记忆形成策略。

完成后，现有 AgentTurn 流程仍然是：

```text
AgentTurn starts
    -> turn-scoped Recent recall
    -> Context Build
    -> Model / Tool / Action loop
    -> AgentTurn terminal state
    -> project Recent Memory
    -> SQLite transaction commit
    -> release entity ExecutionLane
```

---

# 2. 非目标

Phase8.1 不做：

```text
长期 Semantic Memory
Relevant recall
Evidence Capsule 完整模型
MemoryItemKey
supersede / invalidate
Game Memory Policy 规则提取
未完成 Turn 恢复
工具自动重放
完整 Experience Store
Adapter 修改
proto 修改
早期草案持久格式迁移器
```

Current Turn Transcript 继续留在内存。JSONL Trace 只做诊断，不是 Memory 真源。

---

# 3. 当前代码事实

当前 `runtime/internal/memory/store.go` 的 Store 接口是：

```go
type Store interface {
    Append(ctx context.Context, record Record) error
    Recent(ctx context.Context, key session.AgentSessionKey, limit int) ([]Record, error)
}
```

当前 `runtime/internal/memory/record.go` 的 `Record` 已包含：

```text
MemoryID
SessionKey
SourceTurnID
SourceEventID
SourceEventSequence
EventType
GameTime
SourceContextFacts
Outcomes
CreatedAt
```

当前 `runtime/internal/memory/in_memory.go` 是进程内实现，按 `AgentSessionKey` 隔离，并有每 session 保留上限。

Phase8.1 需要在此基础上补最小投影身份：

```text
ProjectionKind
ProjectionVersion
ProjectionBatchKey
```

---

# 4. SQLite 选择

Phase8.1 使用纯 Go SQLite 驱动：

```text
modernc.org/sqlite
```

选择口径：

- 优先保证 Windows 本地开发和跨平台构建简单。
- 不引入 CGO 编译链作为 Phase8.1 前置条件。
- 如果后续发现 WAL、锁等待、事务取消或性能不满足，再评估 CGO 驱动。

SQLite 连接配置：

```text
journal_mode = WAL
synchronous = FULL
foreign_keys = ON
busy_timeout = 有界配置值
```

这些设置必须应用到实际使用的连接。技术实现不得只在未被后续操作复用的临时连接上设置。

同一 `game_id + world_id` 数据库可以被多个 Entity 的 ExecutionLane 并发访问。SQLite WAL 允许读写并行，但写事务仍是单写者；同 world 多 Entity 并发写入时，写事务通过 SQLite 锁等待和 `busy_timeout_ms` 排队。默认等待 5000ms，超过后该次 Memory 写入失败并记录诊断，不回滚已经完成的 Action 或 TurnCompletion。

---

# 5. 数据库粒度与路径

Phase8.1 按 `game_id + world_id` 物理分库。

默认根目录：

```text
runtime/.local/memory
```

默认数据库路径：

```text
runtime/.local/memory/<game_id_hash>/<world_id_hash>/memory.db
```

路径规则：

```text
game_id_hash:
    sha256(game_id) 的 lowercase hex

world_id_hash:
    sha256(world_id) 的 lowercase hex
```

hash 只用于文件系统路径安全，避免 Windows 保留名、尾随点和特殊字符问题。数据库内仍保存原始 `game_id / world_id`。

数据库内必须保存原始身份：

```text
game_id
world_id
```

打开数据库时：

```text
新库:
    写入 game_id / world_id 绑定信息。

已有库:
    读取绑定信息。
    若绑定信息与请求的 game_id / world_id 不一致，
    首次打开该库失败，该 world 的 Memory 读写返回明确错误。
```

库内查询继续按 `entity_id` 隔离。物理分库不等同于 `WorldMemoryScope`，它只是存储部署边界。

---

# 6. 最小 Schema

Phase8.1 schema version：

```text
phase8_1_recent_v1
```

最小表职责：

| 表 | 职责 |
| --- | --- |
| `memory_schema_metadata` | 保存 schema version、game_id、world_id |
| `recent_records` | 保存 Recent Memory 记录 |
| `recent_projection_batches` | 保存幂等凭据和内容指纹 |

`memory_schema_metadata`：

```sql
CREATE TABLE memory_schema_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

必填 key：

```text
schema_version
game_id
world_id
created_at
```

`recent_records` 必须保存：

```text
memory_id
projection_kind
projection_version
projection_batch_key
game_id
world_id
entity_id
source_turn_id
source_event_id
source_event_sequence
event_type
game_time
source_context_facts_json
outcomes_json
created_at
```

约束：

```text
memory_id 唯一
projection_batch_key 唯一
game_id / world_id 必须等于数据库绑定身份
entity_id 非空
```

`recent_projection_batches` 必须保存：

```text
projection_batch_key
projection_kind
projection_version
game_id
world_id
entity_id
content_fingerprint
created_at
last_seen_at
```

`recent_records` 与 `recent_projection_batches` 必须在同一 SQLite transaction 中写入。

---

# 7. ProjectionBatchKey

Phase8.1 的 `ProjectionBatchKey` 由以下字段构成：

```text
game_id
world_id
entity_id
source_turn_id
source_event_id
projection_kind
projection_version
```

实现可以使用稳定 compact JSON array 或等价组合唯一约束，不能使用容易产生分隔符歧义的普通字符串拼接。

`projection_kind` 首版包含：

```text
settled_turn
prior_successful_actions
```

规则：

- `MemoryID` 是记录 ID，不参与幂等判断。
- `CreatedAt` 和本次保存时间不参与幂等等价。
- 同一 `ProjectionBatchKey` 且业务内容等价时 no-op。
- no-op 保留已有 `memory_id` 和 `created_at`。
- 同一 `ProjectionBatchKey` 但业务内容不等价时，返回 `duplicate_conflict`，不得覆盖旧记录。

等价比较使用确定性业务字段：

```text
projection_kind
projection_version
projection_batch_key
game_id
world_id
entity_id
source_turn_id
source_event_id
source_event_sequence
event_type
game_time
source_context_facts_json
outcomes_json
```

Phase8.1 的幂等只承诺同一次 Turn 投影重试。若同一个游戏事件重新生成新的 Turn，去重边界仍由 Gateway 入口语义负责。

---

# 8. 写入生命周期

写入顺序：

```text
AgentTurn terminal state
    -> collect confirmed recent sources
    -> project memory.Record
    -> SQLiteMemoryStore.Append
    -> begin SQLite transaction
    -> validate database binding
    -> check ProjectionBatchKey
    -> write recent_records
    -> write recent_projection_batches
    -> enforce recent_records retention
    -> enforce recent_projection_batches retention
    -> commit / rollback
    -> emit memory write diagnostic
    -> release entity ExecutionLane
```

取消与超时：

- context cancel 或 timeout 不能只停止等待。
- 释放 ExecutionLane 前，实际 SQLite transaction 必须已经提交、回滚或明确失败。
- 不允许后台事务在 Lane 释放后继续提交。
- 已提交结果不因随后收到 cancel 而改报失败。
- 事务结果未知时，记录明确诊断，并通过 `ProjectionBatchKey` 查询核对。

失败行为：

| 情况 | 行为 |
| --- | --- |
| 事务开始前失败 | 不写入 Recent，也不写入幂等凭据 |
| 事务内任一步失败 | rollback，Recent 与幂等凭据都不暴露 |
| commit 成功 | 下一 Turn 和 Runtime 重启后可读 |
| commit 成功后 cancel | 按成功报告 |
| commit 结果未知 | 记录 unknown 诊断，通过 `ProjectionBatchKey` 核对 |

Memory 写入失败不回滚已完成 Action 或 TurnCompletion。

---

# 9. 读取语义

`Recent(ctx, key, limit)` 行为：

```text
输入:
    game_id
    world_id
    entity_id
    limit

读取:
    打开 game_id + world_id 对应数据库
    校验数据库绑定身份
    按 entity_id 查询候选
    最终交付数量不超过 limit 的可见 Recent
    返回顺序从旧到新，供 Context 使用
```

Phase8.1 的 Runtime recall 不应直接用 `recent_memory_limit` 作为数据库候选数量。

读取当前 Context 时：

```text
candidate_limit = memory_store.max_records_per_entity
从数据库读取该 Entity 保留上限内的候选
过滤 future GameTime
选出最近 recent_memory_limit 条可见记录
再按旧到新返回
```

排序分两步：

```text
选择最新 N 条:
    使用稳定时间顺序从新到旧选取。

返回给 Context:
    将已选记录按旧到新排列。
```

稳定时间顺序：

```text
如果两条记录都有可比较、非空 GameTime:
    先比较 GameTime。
    当 GameTime 相等且两条记录的 source_event_sequence 都非 0 时，
    再比较 source_event_sequence。

如果 GameTime 缺失、不可比较，或无法用 sequence 判断同一时间内顺序:
    使用 created_at。
    若仍相同，使用 memory_id 保持稳定。
```

`source_event_sequence` 只用于同一非空可比较 GameTime 内的稳定排序，不作为跨 GameTime 或 unknown GameTime 的全局主排序字段。

读取失败：

```text
AgentTurn 继续
trace memory_read_failed
本轮 Context 不包含持久 Recent Memory
```

未来时间过滤保持现有语义：

```text
record.GameTime > current GameTime
    -> 不进入当前 Model Context

record.GameTime == current GameTime
    -> 可见

record.GameTime 不可比较
    -> 不执行未来时间过滤
```

Phase8.1 不承诺完整回档分支恢复。

---

# 10. 保留策略

Phase8.1 区分读取窗口和磁盘容量：

```text
recent_memory_limit:
    控制进入 Context 的 Recent 条数。

memory_store.max_records_per_entity:
    控制每个 Entity 在 SQLite 中保留的 Recent 记录上限。

memory_store.max_projection_batches_per_entity:
    控制每个 Entity 在 SQLite 中保留的 Recent 幂等凭据上限。
```

默认：

```text
memory_store.max_records_per_entity = max(recent_memory_limit, memory.DefaultMaxRecordsPerSession)
memory_store.max_projection_batches_per_entity = max(100, memory_store.max_records_per_entity * 4)
```

规则：

- `max_records_per_entity` 不得小于 `recent_memory_limit`。
- Recent 超限清理发生在 Append 的同一事务中。
- 幂等凭据超限清理也在 Append 的同一事务中按保留窗口执行。
- 清理顺序固定为：先清 `recent_records`，再清 `recent_projection_batches`。
- `recent_projection_batches` 只有在对应 `recent_records` 已不存在时才允许被清理。
- 清理按同一 Entity 独立执行，不影响其他 Entity。
- 清理 Recent 记录不等于清理幂等凭据。
- 幂等凭据按每 Entity 批次数保留。
- 幂等凭据数量上限由 `max_projection_batches_per_entity` 控制。
- 幂等凭据不得早于对应 Recent 记录被清理。
- Recent 记录已清理但幂等凭据仍保留时，等价重试返回 no-op，不重新插入旧 Recent。
- 幂等凭据窗口外不承诺去重。

读取当前 Context 时，应取最近可见记录：

```text
按 entity_id 查询 max_records_per_entity 条候选
过滤 future GameTime
保留最近 recent_memory_limit 条可见记录
按旧到新返回给 Context
```

这样回档后，未来时间记录不会占满 Context 的 Recent 窗口。

---

# 11. 配置

新增 agent 配置：

```json
{
  "memory_store": {
    "kind": "sqlite",
    "root": "runtime/.local/memory",
    "busy_timeout_ms": 5000,
    "max_records_per_entity": 20,
    "max_projection_batches_per_entity": 100
  }
}
```

默认行为：

```text
memory_enabled = true
memory_store.kind = sqlite
memory_store.root = runtime/.local/memory
memory_store.busy_timeout_ms = 5000
memory_store.max_records_per_entity = max(recent_memory_limit, memory.DefaultMaxRecordsPerSession)
memory_store.max_projection_batches_per_entity = max(100, memory_store.max_records_per_entity * 4)
```

测试继续支持 `WithMemoryStore` 注入内存 store。

配置不包含早期草案持久格式迁移开关。

---

# 12. 开发步骤

## M1：Record 与 ProjectionBatchKey

修改范围：

```text
runtime/internal/memory/record.go
runtime/internal/memory/projector.go
runtime/internal/memory/projector_test.go
runtime/internal/agent/loop.go
```

目标：

```text
memory.Record 增加 ProjectionKind / ProjectionVersion / ProjectionBatchKey。
MemoryProjector 接收 projection kind。
settled_turn 和 prior_successful_actions 生成不同 ProjectionBatchKey。
MemoryID 继续作为记录 ID。
CreatedAt 不参与幂等等价。
```

测试：

```powershell
go test ./runtime/internal/memory -count=1
go test ./runtime/internal/agent -run Memory -count=1
```

## M2：SQLiteMemoryStore

新增：

```text
runtime/internal/memory/sqlite_store.go
runtime/internal/memory/sqlite_store_test.go
```

目标：

```text
实现 SQLiteMemoryStore。
使用 modernc.org/sqlite。
按 game_id + world_id 解析数据库路径。
初始化 phase8_1_recent_v1 schema。
写入并校验数据库绑定身份。
Append 使用 ProjectionBatchKey 幂等。
Recent 按 entity_id 隔离返回。
Recent 与幂等凭据同事务提交。
同 world 多 Entity 并发写通过 SQLite busy_timeout_ms 排队，超时后返回写入失败诊断。
Append 同事务内先清 recent_records，再清 recent_projection_batches。
```

测试：

```powershell
go test ./runtime/internal/memory -count=1
```

重点测试：

```text
新库写入 metadata
已有库校验 game_id / world_id
绑定不一致时首次打开该库失败，不表示整个 Runtime 进程必须退出
game_id = con / nul / game. 时路径使用 hash 而不是原始目录名
同一世界不同 Entity 不串线
不同 world_id 使用不同数据库
同一世界不同 Entity 并发写入时，一个写事务等待另一个写事务完成
busy_timeout_ms 超过后该次 Memory 写入失败并记录诊断
重复 ProjectionBatchKey no-op
ProjectionBatchKey conflict rejected
等价比较排除 MemoryID / CreatedAt
事务失败不只提交 Recent 或只提交幂等凭据
recent_records 清理先于 recent_projection_batches
仍被 recent_records 引用的 projection batch 不会被清理
可控慢事务中途 cancel 后，事务结果为 commit、rollback 或明确失败
cancel-mid-transaction 不会产生 Lane 释放后的后台提交
```

## M3：配置与 Composition Root

修改范围：

```text
runtime/internal/agent/config.go
runtime/internal/agent/config_test.go
runtime/cmd/server
```

目标：

```text
新增 memory_store.kind / memory_store.root / memory_store.busy_timeout_ms / memory_store.max_records_per_entity / memory_store.max_projection_batches_per_entity。
默认 kind = sqlite。
默认 root = runtime/.local/memory。
Composition Root 创建共享 SQLiteMemoryStore。
保留 WithMemoryStore 测试注入能力。
```

测试：

```powershell
go test ./runtime/internal/agent -run Config -count=1
go test ./runtime/internal/agent -run Memory -count=1
```

## M4：AgentLoop 写入顺序与读取回归

修改范围：

```text
runtime/internal/agent/loop.go
runtime/internal/agent/loop_test.go
runtime/internal/context/builder_test.go
```

目标：

```text
Turn 终态后同步完成 Memory 写入尝试。
事务实际结束后才释放同 Entity ExecutionLane。
写入失败记录诊断，不回滚 Action。
读取失败 fail-open。
Runtime 重启后同一 AgentSessionKey 可读取 Recent。
future GameTime 仍不进入 Context。
读取 Recent 时先取 max_records_per_entity 候选，再过滤 future GameTime，最后截取 recent_memory_limit。
```

测试：

```powershell
go test ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/gateway -count=1
```

## M5：Phase8.1 验收

验收命令：

```powershell
go test ./runtime/internal/memory -count=1
go test ./runtime/internal/agent -count=1
go test ./runtime/internal/gateway -count=1
go test ./runtime/internal/context -count=1
go test ./... -count=1
git diff --check
```

建议补充：

```powershell
go test -race ./runtime/internal/memory -count=1
```

Phase8.1 Accepted 后再进入 Phase8.2 技术方案。

---

# 13. 验收标准

Phase8.1 完成条件：

```text
SQLiteMemoryStore 使用 modernc.org/sqlite。
Runtime 重启后，同一 AgentSessionKey 可以读取此前 Recent Memory。
数据库按 game_id + world_id 物理分库。
数据库路径使用 game_id_hash / world_id_hash。
数据库内 game_id / world_id 绑定校验生效。
绑定不一致时首次打开该库失败，该 world 的 Memory 读写报错，不要求 Runtime 进程退出。
同一世界数据库内不同 Entity 不串线。
同 world 多 Entity 并发写入通过 busy_timeout_ms 排队，超时后写入失败并记录诊断。
不同 world_id 不共享数据库。
ProjectionBatchKey 稳定生成。
同一 ProjectionBatchKey 等价重试 no-op。
同一 ProjectionBatchKey 业务冲突 reject。
MemoryID / CreatedAt 不参与幂等等价。
Recent 记录与幂等凭据同事务提交。
事务失败不会暴露半条业务状态。
取消 / 超时后没有 Lane 释放后的后台提交。
Recent 磁盘保留上限生效。
Recent 记录清理先于幂等凭据清理。
仍被 Recent 记录引用的幂等凭据不得清理。
Recent 清理不导致幂等凭据提前丢失。
Recent 记录已清理但幂等凭据仍在时，等价重试 no-op，不重新插入旧记录。
幂等凭据窗口外不承诺去重。
读取时先取实体保留上限内候选，再过滤 future GameTime，最后返回最近 N 条可见记录。
选出最近 N 条和按旧到新返回是两个独立步骤。
future GameTime 记忆不进入当前 Context。
读取失败 fail-open。
写入失败不回滚 Action / TurnCompletion。
Trace 写入失败不影响 Memory 读写。
```

---

# 14. Review Gate

进入代码开发前必须确认：

```text
Phase8.1 文档状态已由 Draft 改为 Accepted。
Phase7.5 已完成必要验收，或其 known limitations 不影响 Memory 持久化开发。
SQLite 驱动、路径规则、数据库绑定校验、事务边界和保留策略已 review 通过。
Phase8.1 与 Phase8.2 不混在同一个代码提交里开发。
```

---

# 15. 一句话总结

Phase8.1 用纯 Go SQLite 把现有 Recent Memory 按世界分库、按实体隔离地持久保存；提交成功后下一 Turn 和 Runtime 重启后都能读取，失败只进入诊断，不改变已经发生的游戏动作。
