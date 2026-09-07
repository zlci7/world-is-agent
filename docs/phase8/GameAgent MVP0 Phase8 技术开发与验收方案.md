# GameAgent MVP0 Phase8 技术开发与验收方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-07
> **Phase:** Phase8 Game-native Memory Persistence 与 Lightweight Long-term Memory
> **Roadmap Baseline:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md) v1.9
> **Memory Architecture Baseline:** [Memory架构设计](../summary/Memory/Memory架构设计.md) v0.1
> **Previous Gate:** Phase7.5 integration evidence pending
> **Review Required Before Coding:** Yes
> **Code Baseline:** `main` @ `fda66db`
> **Roadmap Sync:** Required after this draft is reviewed
> **Phase8.1 Coding Gate:** Accepted, tracked in the independent Phase8.1 plan
> **Phase8.2 Coding Gate:** Design Preview, requires a dedicated implementation review after Phase8.1

---

# 1. 阶段目标

Phase8 将 GameAgent Runtime 的 Memory 从进程内短期列表升级为 game-native 的持久记忆能力。

本阶段分两个增量：

```text
Phase8.1 Persistent Recent Memory
    使用 SQLiteMemoryStore 持久化现有 Recent Memory，
    让同一 game_id + world_id + entity_id 的 Agent 在 Runtime 重启后恢复最近经历。

Phase8.2 Lightweight Long-term Memory
    在同一世界 SQLite 数据库上扩展轻量长期 Memory，
    支持少量重要经历的保存、窗口外召回、更正和 Context 接入。
```

本文是 Phase8 总体方案。Phase8.1 的可开发合同由独立文档承接，Phase8.2 当前只保留边界预览。

---

# 2. 数据职责

Phase8 区分三类数据：

| 数据 | 位置 | 职责 |
| --- | --- | --- |
| Current Turn Transcript | Runtime 内存 | 保存当前 AgentTurn 内的模型消息、ToolCall 和 ToolResult，供同一 Turn 后续 step 构建请求 |
| JSONL Trace | 可选诊断文件 | 记录调用顺序、参数摘要、结果、耗时和失败原因 |
| SQLite Memory Store | Runtime 本地 SQLite | 保存已确认 Recent Memory、后续长期 Memory 和必要 evidence |

边界：

- Transcript 不落为 Phase8 权威持久状态。
- Trace 不参与 Memory 提交一致性。
- Trace 写入失败不影响模型所需 Transcript、Memory 写入、Memory 召回或 Turn 结果。
- Memory 不从 Trace 反向重建，不从 Trace 自动重放工具。
- 未完成 Turn 恢复、工具自动重放和完整 Experience Log 不属于 Phase8。

---

# 3. SQLite 持久化边界

Phase8 选择 SQLite 作为正式持久化后端。

实现边界：

```text
SQLiteMemoryStore
    -> implements memory.Store
    -> uses modernc.org/sqlite
    -> one database per game_id + world_id
    -> records still scoped by game_id + world_id + entity_id
```

数据库物理路径按世界划分：

```text
runtime/.local/memory/<game_id_hash>/<world_id_hash>/memory.db
```

路径规则：

- `game_id_hash` 使用 `sha256(game_id)` 的 lowercase hex。
- `world_id_hash` 使用 `sha256(world_id)` 的 lowercase hex。
- hash 只用于文件系统路径安全，避免 Windows 保留名、尾随点和特殊字符问题。
- 数据库内必须保存原始 `game_id` 和 `world_id`。
- 打开数据库时必须核对库内绑定身份与请求的 `game_id / world_id` 一致。
- 只有绑定正确后，Runtime 才能按 `entity_id` 查询或写入个人记忆。

Scope 规则：

- 数据库按 `game_id + world_id` 物理分库。
- 物理数据库不等同于 `WorldMemoryScope`。
- `WorldMemoryScope = game_id + world_id` 表示世界级记忆归属。
- `EntityMemoryScope = game_id + world_id + entity_id` 表示个人记忆归属。
- 同一个世界数据库可以同时保存世界级记忆和多个 Entity 的个人记忆。
- `definition_id` 不进入数据库路径，也不进入 Memory Scope。

---

# 4. Phase8.1 边界

Phase8.1 只实现 SQLite Recent Memory。

做：

```text
SQLite schema version
SQLiteMemoryStore
Recent Memory 持久化
Runtime 重启恢复
Scope 隔离
ProjectionBatchKey 幂等
事务提交
Recent 保留上限
TimeVisibilityPolicy 现有语义
读取失败 fail-open
写入失败诊断
```

不做：

```text
长期 Semantic Memory
Relevant recall
Evidence Capsule 完整模型
MemoryItemKey
supersede / invalidate
未完成 Turn 恢复
工具自动重放
Adapter / proto 修改
早期草案持久格式迁移器
```

Phase8.1 独立文档已作为 SQLite Recent Memory 的开发合同。Phase8.2 仍需后续独立 review。

---

# 5. Phase8.1 主链路

```text
AgentTurn terminal state
    -> MemoryProjector projects existing memory.Record
    -> ProjectionBatchKey
    -> SQLiteMemoryStore.Append
    -> SQLite transaction commits recent record + idempotency credential
    -> trace memory commit result
    -> release entity ExecutionLane
    -> next AgentTurn
    -> turn-scoped Recent recall
    -> Context Engine
    -> Renderer
    -> model.Request
```

写入生命周期：

- Memory Write 发生在 AgentTurn 终态后。
- 同一 Entity 的必要写入尝试必须在释放该 Entity 的 ExecutionLane 前结束。
- 取消或超时后，实际 SQLite 事务必须已经提交、回滚或明确失败。
- 不允许释放 Lane 后留下后台事务继续提交。
- Memory 写入失败不回滚已经完成的 Action 或 TurnCompletion。
- Memory 读取失败时 AgentTurn 继续执行，本轮 Context 不包含读取失败的 Memory。

---

# 6. Phase8.1 存储合同

Phase8.1 至少需要以下 SQLite 数据职责：

| 数据 | 职责 |
| --- | --- |
| schema metadata | 记录 schema version、绑定的 `game_id / world_id` 和数据库初始化状态 |
| recent records | 保存现有 `memory.Record` 及最小投影身份 |
| projection credentials | 保存 `ProjectionBatchKey`、投影版本和内容指纹，用于幂等 |

`recent_records` 保存现有 Recent 所需字段：

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
source_context_facts
outcomes
created_at
```

幂等规则：

- `ProjectionBatchKey` 必须使用无歧义结构化编码或等价组合唯一约束。
- `MemoryID` 是记录 ID，不参与幂等判断。
- `CreatedAt` 和本次保存时间不参与等价判断。
- 同一 `ProjectionBatchKey` 且业务内容等价时 no-op。
- no-op 保留原始 `memory_id` 和 `created_at`。
- 同一 `ProjectionBatchKey` 但业务内容不同，应 reject 为冲突，不能覆盖旧记录。
- Phase8.1 的幂等主要保证同一次 Turn 投影重试；同一游戏事件重新生成 Turn 的去重仍由 Gateway 入口语义负责。

事务规则：

- Recent Record 和对应幂等凭据必须在同一 SQLite transaction 中提交。
- 任一步失败时不能暴露半条业务状态。
- 事务失败测试必须同时检查 Recent 记录和幂等凭据。
- 已提交结果不因随后收到 context cancel 而改报未提交。
- 提交结果未知时，记录诊断，并通过稳定 `ProjectionBatchKey` 核对。

保留策略：

- `recent_memory_limit` 控制进入 Context 的 Recent 条数。
- `memory_store.max_records_per_entity` 控制每个 Entity 的磁盘 Recent 保留上限。
- `memory_store.max_projection_batches_per_entity` 控制每个 Entity 的 Recent 幂等凭据保留上限。
- 读取 `limit` 不等于存储容量限制。
- Recent 超限清理发生在 Append 的同一事务中。
- 幂等凭据超限清理也在 Append 的同一事务中按保留窗口执行。
- Recent 记录清理不代表幂等凭据立即清理。
- 幂等凭据保留期限必须覆盖 Phase8.1 承诺支持的重试窗口。
- Recent 记录已清理但幂等凭据仍保留时，等价重试返回 no-op，不重新插入旧 Recent。
- 幂等凭据窗口外不承诺去重。

Recent recall 必须先读取该 Entity 保留上限内的候选，再过滤 future GameTime，最后选出最近 `recent_memory_limit` 条可见记录并按旧到新返回给 Context。

---

# 7. Phase8.2 设计预览

Phase8.2 在 Phase8.1 验收后单独补齐详细方案并 review。

方向：

```text
Default Game Memory Policy
    -> MemoryCandidate
    -> MemoryRecord + evidence capsule
    -> MemoryItemKey
    -> synchronous retrieval fields
    -> Relevant recall
    -> MemoryProjection candidates
    -> Context Engine final inclusion
```

Phase8.2 继续使用同一个世界 SQLite 数据库，但引入独立数据模型：

| 数据 | 职责 |
| --- | --- |
| long-term memory records | 保存 Episodic / Semantic 最小长期记忆 |
| memory sources | 保存必要 evidence capsule |
| memory relations | 保存 supersede / invalidate / evidence 关系 |
| retrieval fields | 保存首版规则检索需要的 topic、participant、event kind、claim type 等字段 |

Phase8.1 的 `source_context_facts_json` / `outcomes_json` 只保存 Recent 原始结构，不作为 Phase8.2 Relevant recall 的直接查询面。Phase8.2 必须定义 FTS5、规范化索引表或等价同步检索字段，不能依赖在 JSON TEXT 上做临时 `WHERE` 匹配。

Phase8.2 进入开发前必须重新确认：

- 一份 source 形成多条 MemoryRecord 的批次写入。
- `MemoryItemKey = ProjectionBatchKey + stable_candidate_key`。
- 新记录、证据、索引字段和旧状态更新的原子提交。
- 8.1 数据库升级到 8.2 后 Recent 数据仍可读。
- 默认查询闭环：来源输入 -> 候选 -> 后续输入生成 query -> 召回 -> Context inclusion。
- 普通自然语言否定不直接触发 supersede；结构化 `memory_correction` 才能更正。

---

# 8. 测试计划

Phase8.1 文档必须覆盖：

```text
SQLite 初始化和 schema version
modernc.org/sqlite 依赖与平台验证
按 game_id + world_id 分库
game_id_hash 路径生成
world_id_hash 路径生成
Windows 保留名和尾随点不会进入路径目录名
库内 game_id / world_id 绑定校验
同一世界数据库内不同 Entity 不串线
不同 world_id 打开不同数据库
读取实体保留上限内候选后再过滤 future GameTime
选出最近 N 条和按旧到新返回分开处理
同一 ProjectionBatchKey 等价重试 no-op
同一 ProjectionBatchKey 业务冲突 reject
事务失败不会只提交 Recent 或只提交幂等凭据
取消 / 超时后没有 Lane 释放后的后台提交
Recent 清理不等于幂等凭据立即清理
Recent 清理后凭据窗口内等价重试仍 no-op
Runtime 重启后已提交 Recent 可读
future GameTime 记忆不进入当前 Context
读取失败 fail-open
写入失败不回滚已完成 Action
Trace 失败不影响 Memory 读写
```

建议回归命令：

```powershell
go test ./runtime/internal/memory -count=1
go test ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/gateway -count=1
go test ./... -count=1
git diff --check
```

若本地工具链支持，增加：

```powershell
go test -race ./runtime/internal/memory -count=1
```

---

# 9. Review Gate

进入 Phase8.1 代码开发前必须确认：

```text
Phase7.5 已完成必要验收，或 known limitations 不影响 Phase8.1 Memory 持久化开发。
Memory 架构文档已作为 Phase8 baseline。
Phase8.1 独立技术方案已 Review 通过并标记 Accepted。
SQLiteMemoryStore 路线、路径规则、绑定校验、事务语义和保留策略被确认。
Phase8.1 与 Phase8.2 不混在同一个代码提交里开发。
```

Phase8.1 验收后，再编写或升级 Phase8.2 独立技术方案。Phase8.2 进入开发前必须重新 review 长期记忆形成、批次多条写入、格式升级、默认查询闭环和 Context handoff。

---

# 10. 一句话总结

Phase8 先用 SQLite 让同一游戏世界里的具体 Agent 恢复 Recent Memory，再在同一世界数据库上扩展轻量长期记忆；当前 Turn Transcript 仍在内存，Trace 只做诊断，Memory Store 才是持久记忆真源。
