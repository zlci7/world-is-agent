# GameAgent MVP0 Phase8 技术开发与验收方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-08
> **Phase:** Phase8 Persistent Memory, History & Context Compaction
> **Roadmap:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md)
> **Architecture:** [Memory 架构设计](../summary/Memory/Memory架构设计.md)
> **Code Inspection Baseline:** `main @ fe0c2d6` 与 2026-09-08 工作区中的 Phase8.1 修订
> **Phase8.1:** Accepted，验收结论与限制见独立方案
> **Phase8.2 / Phase8.3:** Implementation Plan Draft，分别评审、开发和验收

## 1. 阶段目标与分期

同一世界中的具体 Agent 持续保存交互来源，通过有界历史摘要与近期原文维持连续性，并在需要时找回仍保留的旧原文。

| 阶段 | 主题 | 独立交付 | 状态 |
| --- | --- | --- | --- |
| [8.1](./GameAgent%20MVP0%20Phase8.1%20技术开发与验收方案.md) | SQLite Recent Memory | 现有 Recent 持久保存、隔离、幂等与重启读取 | Accepted |
| [8.2](./GameAgent%20MVP0%20Phase8.2%20技术开发与验收方案.md) | Persistent Session History & Context Compaction | 完整约定终态来源、历史快照、同步摘要、检查点恢复 | Implementation Plan Draft |
| [8.3](./GameAgent%20MVP0%20Phase8.3%20技术开发与验收方案.md) | History Retrieval & Retention | 中文字面检索、原文片段、可选留存清理 | Implementation Plan Draft |

8.2 不以 8.3 为验收前置条件。Environment Recovery 的 Memory 前置能力是 8.1 的持久身份与按 AgentSession 读取，8.2/8.3 不额外阻塞 Phase9；恢复 pending action 和自动重连仍由 Phase9 定义。

## 2. 数据职责

| 数据 | 职责 | 持久性质 |
| --- | --- | --- |
| Current Turn Transcript | 当前 Turn 内 earlier steps 的模型消息与工具因果链 | 执行时内存数据，不承担崩溃恢复 |
| JSONL Trace | 调用顺序、耗时、结果与失败诊断 | 可选 observer，不是 Memory 真源 |
| Recent Record | 8.1 已交付的有限近期来源投影 | SQLite 保留窗口，不是完整历史 |
| Session History | 8.2 约定终态来源，含玩家原话、决策、已知动作结果和终态 | 权威原文，成功提交后可恢复 |
| Context Summary | 8.2 有界生成摘要与精确覆盖、累计时间来源 | 有损读取投影，不替代游戏事实 |
| Retrieved History | 8.3 从仍保留的原文取得的来源片段 | 索引派生的读取结果，不是独立认知表 |

当前 Observation 表达当前世界事实。过去的陈述、模型调用意图、游戏确认结果分别保留性质，不能将台词成功展示提升为台词描述的事实已发生。

Trace 失败不影响 Memory 的一致性；Memory 不从 Trace 重建，不归档每步重复包含旧历史的完整模型 Request，不自动重放工具。

## 3. 共同存储与生命周期

- Memory 归 Runtime，Adapter 只提供事件、观察和实际 ActionResult。
- `AgentSessionKey = game_id + world_id + entity_id`；连接 ID、UI conversation ID、definition ID 不改变 Memory owner。
- 使用 `modernc.org/sqlite`，按 game/world 分库，库内按 entity 隔离；物理世界库不是公共记忆池。
- 默认路径为 `runtime/.local/memory/<sha256(game_id)>/<sha256(world_id)>/memory.db`，hash 为 lowercase hex。原始身份保存在 metadata，打开时必须核对绑定。
- 稳定逻辑键承担幂等；记录 ID 和写入时间不承担幂等。同键等价 no-op，内容冲突拒绝。
- 终态后的有界写入尝试在当前 entity lane task 结束前完成；实际事务已提交、回滚或明确失败才释放 lane。
- 读失败继续 Turn 并诊断；写失败不改变已经发生的 Action 或 TurnCompletion。数据损坏或 schema 不匹配不自动 reset。
- Context Build 是确定性选择与渲染，不读库、不调用模型。Agent 层在 Turn 开始准备固定历史候选，后续 step 复用。
- 沿用已有 lane 的调度边界，不新增多 Runtime 共享数据库写入或跨连接 exactly-once 保证。

## 4. Phase8.1 已交付合同

`SQLiteSchemaVersion = phase8_1_recent_v1`，包含 metadata、`recent_records` 与 `recent_projection_batches`。Recent 与凭据同事务提交和清理，仍被 Recent 引用的凭据不得提前删除。

当前默认值：

| 参数 | 默认值 | 作用 |
| --- | --- | --- |
| SQLite Recent 容量 | 100 条 / entity | 磁盘实际保留上限 |
| SQLite 幂等凭据容量 | 400 批 / entity | 重试去重保留窗口 |
| Recent Context 数量 | 最多 5 条 | 进入模型的条数限制 |
| Recent Context token | 最多 4096 estimated tokens | 同时受最终请求预算限制 |
| SQLite busy timeout | 5000 ms | 锁等待上限 |

读取实体容量范围内的候选，先过滤可比较的 future GameTime，再选择最多 5 条可见 Recent。InMemory 测试实现仍有独立的 20 条默认容量，不应混同于 SQLite 默认值。

Phase8.1 已由项目负责人验收。既有验证包括 Store/Loop 实例重建、自动化回归和真实对话落库；真实 Runtime 进程重启、指定跨版本端到端场景和 race 等未完成验证仍按 [8.1 验收结论](./GameAgent%20MVP0%20Phase8.1%20技术开发与验收方案.md#验收结论) 记录，不因本方案更新而变成通过。

## 5. Phase8.2 开发合同

终态 History 不按重要性筛选，以一次 Turn 的完整约定来源为提交单元。完整性只保证成功提交的终态批次；中途崩溃和写入失败可能缺失本轮，不承诺逐步骤恢复。

在原世界数据库中事务迁移至 `phase8_2_history_v1`，增加 History 与 Summary 数据。现存 Recent 按 legacy 来源导入；保留原 ID、键、时间和原有字段，不补造 action ID、output 等缺失证据。新 History 不继承 100 条磁盘淘汰和 5 条 Context 窗口。

```text
Observe -> history snapshot -> valid summary + uncovered sources
    -> optional synchronous compaction
    -> Context: current facts + summary + recent original history + current transcript
    -> model/tool/action loop
    -> terminal History transaction
```

压缩在 Observe 后、首次决策前同步执行，每 Turn 最多一次有界尝试。`keep_recent_tokens = 20000` 是压缩后尾部目标，低预算时允许更小，两次压缩间允许历史增长。摘要有独立上限，当前 Turn 有预留空间，最终请求受现有硬预算限制。

Provider 需要最小文本生成能力，复用现有配置与凭据；生成在 SQLite 事务外进行，成功校验后原子发布精确覆盖的摘要检查点。失败保留旧检查点和未覆盖历史，按预算降级并限制重试；摘要成功不删除原文。

8.2 里程碑：来源与迁移、历史投影、摘要生成、主链路与实机验收。具体接口、默认值、失败与测试合同以独立方案为准。

## 6. Phase8.3 开发合同

Runtime 每 Turn 从当前通用 ContextFact 文本生成一次有界字面查询，对同 owner、同快照的可用原文检索。中文短词、标点、数字和实体名是硬验收；索引是可重建派生数据，不要求语义或同义词理解。

Context 以来源片段与近期原文去重，限制单独检索预算。摘要覆盖的来源仍可返回被摘要省略的原文细节。诊断区分命中、未纳入、部分扫描和已知来源已清理。

`retention_days = 0` 默认关闭清理。启用后按原始现实写入时间计算年龄，仅清理超期、已被保留摘要完整覆盖、且不在近期或在用保护范围内的完整原文单元。原文、legacy 副本和索引清理保持事务一致，保留指纹、时间与必要覆盖信息。

清理不触发模型，不删除未摘要来源，不在交互热路径自动 VACUUM。原文删除后不能精确检索或重新生成相应早期摘要；原文保留期不等同于数据库总大小上限。

8.3 里程碑：检索与中文样例、Context 接入、留存维护、重启与回档验收。

## 7. 游戏时间与恢复边界

- GameTime 控制 Context 可见性，现实时间控制写入年龄；数据库序号控制分页和覆盖，三者职责分开。
- 可比较的未来来源隐藏，相等仍可见；缺失或不可比沿用 unknown 语义并诊断。
- 摘要可见性检查全部累计来源，包括前序检查点；不能只看摘要创建时间、最后来源时间或最大序号。
- 回档后摘要包含未来来源时隐藏整个摘要，选择更早有效检查点或可见原文。原文已清理时仍保持时间检查，只能使用剩余可用内容。
- SQLite 独立于游戏存档，时间追上后已放弃分支的经历可能重新出现。完整存档分支恢复、未完成 Turn resume 与工具重放不属于 Phase8。

## 8. 验收与评审

| 阶段 | 自动化证据 | 实机证据 |
| --- | --- | --- |
| 8.1 | 按已 Accepted 方案保留结果与限制 | 已有对话落库与后续使用；未完成项明确记录 |
| 8.2 | 原子写入、迁移、精确覆盖、固定快照、时间与最终请求预算 | 真实摘要忠实性、压缩延迟、Runtime 进程重启与回档 |
| 8.3 | 中文命中、去重、索引重建、留存保护和事务回滚 | 原文细节进入最终请求、清理后能力边界与性能 |

自动化以 SQLite 内容、提交结果、覆盖元数据、ContextBuildReport 和 RecordingProvider 捕获的最终 `model.Request` 为准。候选存在不等于进入模型；NPC 表示记得不代替来源链路证据。

代码开发后，从仓库根目录运行：

```powershell
go test ./runtime/internal/memory -count=1
go test ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/gateway -count=1
go test ./... -count=1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
git diff --check
```

8.2 另测 `runtime/internal/model/...` 与 `runtime/internal/llm/...`；race 按工具链能力执行，受阻时报告限制。仅更新文档时检查内容、链接与 `git diff --check`，不把文档校验当作上述能力已通过。

每个子阶段在自己的技术评审、代码复审、自动化与实机证据完成后交项目负责人确认。方案允许编码与阶段实现 Accepted 是两个不同结论。
