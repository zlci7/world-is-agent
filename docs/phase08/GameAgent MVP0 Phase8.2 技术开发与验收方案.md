# GameAgent MVP0 Phase8.2 技术开发与验收方案

> **Status:** Accepted (2026-09-09，负责人授权自动验收；本阶段免游戏内实机验收)
> **Date:** 2026-09-08
> **Phase:** Phase8.2 Persistent Session History & Context Compaction
> **Parent Plan:** [Phase8 总方案](./GameAgent%20MVP0%20Phase8%20技术开发与验收方案.md)
> **Previous Stage:** [Phase8.1 SQLite Recent Memory](./GameAgent%20MVP0%20Phase8.1%20技术开发与验收方案.md)，Accepted
> **Next Stage:** [Phase8.3 History Retrieval & Retention](./GameAgent%20MVP0%20Phase8.3%20技术开发与验收方案.md)
> **Architecture:** [Memory 架构](../summary/Memory/Memory架构设计.md)
> **Code Inspection Baseline:** `006bf845e334`
> **Coding Gate:** 本方案完成技术评审，并记录进入开发时的代码基线

## 1. 目标与边界

Runtime 按 `game_id + world_id + entity_id` 保存完整的约定终态交互来源，以历史摘要和近期原文维持跨 Turn、跨 Runtime 重启的连续性。

本阶段交付终态 History 原子追加、8.1 数据迁移、固定历史快照、时间可见性、token 预算、同步摘要生成和检查点恢复。

本阶段不包含原文自动清理、窗口外检索、Semantic 认知提取与更正、模型主动 Memory Tool、向量检索、Adapter/proto 修改、逐步骤落盘、未完成 Turn 恢复、动作重放和完整存档分支管理。8.2 技术验收通过后进入 8.3，两个阶段最终由项目负责人分别确认 Accepted。

## 2. 当前代码事实

以下是开发起点，不表示 History 或 Summary 已实现。

| 模块 | 已有行为 | 本阶段接入职责 |
| --- | --- | --- |
| `memory.Store` | `Append` / `Recent`，持久对象是 Recent `Record` | 增加终态 History 和 Summary 存储接口 |
| SQLite Store | 世界分库、entity 隔离、schema 校验、幂等与事务；默认保留 100 条 Recent、400 条凭据 | 在原库中事务迁移，保留身份与校验机制 |
| AgentLoop | Observe 后读取一次 Recent；当前 Turn 内积累 Transcript；终态进行 Memory 写入尝试 | 收集全部约定终态路径，协调快照和摘要 |
| Scheduler | 保有 ToolCall、实际 ActionResult 和动作生命周期信息 | 从执行链路采集来源，区分游戏回执与 Runtime 判定 |
| Context | Recent 最多 5 条、4096 estimated tokens；当前 Turn Transcript 独立预算 | 以 Summary 和历史尾部替换同内容的 Recent 注入 |
| `model.Provider` | `Generate` 返回的 `Response` 只有 `Decision` | 增加最小摘要文本生成接口及 Provider 适配 |

`max_request_tokens = 65536` 是 Runtime 的估算输入预算，不是自动发现的模型上下文窗口。`max_transcript_tokens = 16384` 只限制当前 Turn 的 earlier steps。

现有 Recent 的 Outcomes 只有工具名、参数和状态，不具备完整 action ID、call ID、实际 output。迁移不能声称补齐这些来源。

代码依据：[Record](../../runtime/internal/memory/record.go)、[SQLite Store](../../runtime/internal/memory/sqlite_store.go)、[AgentLoop](../../runtime/internal/agent/loop.go)、[Scheduler](../../runtime/internal/agent/scheduler.go)、[Provider](../../runtime/internal/model/provider.go)、[Context](../../runtime/internal/context/context.go)、[Protocol](../../protocol/proto/gameagent.proto)。

## 3. 完整历史的保存合同

### 3.1 来源

每个到达终态的 AgentTurn 形成一个有序历史单元，不按主题、句式、兴趣或重要性筛选。

| 来源 | 保存内容 | 来源性质 |
| --- | --- | --- |
| GameEvent | owner、event/turn ID、event type、sequence、GameTime 和 ContextFacts 的规范字段 | Runtime 接收的事件，不解释游戏私有 payload |
| Observation 引用 | 可获得的 revision、通用 GameTime 及时间来源 | 当前观察引用，不归档完整 Observation.state |
| 模型决策 | step 顺序、ToolCall 的 call ID、工具名、参数和 control | 请求意图，不证明游戏效果 |
| 游戏回执 | 已知 action ID、实际 ActionResult 的 status、output、error 及关联调用 | Adapter/Game 确认的结果 |
| Runtime 处理 | 拒绝、跳过、超时、取消和调用错误等已知结果 | 不伪造为游戏侧 ActionResult |
| Turn 终态 | 实际完成、失败、取消或其它现有终态及原因 | 包含已知的部分执行情况 |

正常失败或取消路径仍保存输入与已知执行情况；Observe 或 Provider 失败时，缺失部分明确缺失。NPC 台词通过已有工具参数及结果保存，不声称保存 Provider 未采集的普通文本或 thinking。

模型每步 Request 中已经注入的历史、工具定义和当前状态不再作为新经历保存。Trace 只用于诊断，History 不从 Trace 回填。

ActionResult 当前没有独立 GameTime。保留事件时间与可获得的 Observation 时间及其来源，不补造动作精确发生时刻。保留时间字段存在性，零值不等于缺失。

### 3.2 完整性与提交

- 完整性以成功提交的终态批次为界。进程中途崩溃、写入失败、Adapter 未上报的内容不在恢复保证内。
- History 保存约定来源的完整规范字段；Context 可以裁剪显示内容，持久原文不能静默截断。
- 单批序列化超过安全容量时整批失败，报告实际大小和限制，不保存声称完整的部分批次。
- 每个终态路径进行有界同步写入尝试。提交、回滚或明确失败后，才结束该 entity 的 lane task。
- 写入失败不回滚 Action、TurnCompletion 或玩家已看到的内容；后续 Turn 可能缺少这轮历史。
- 数据库事务中不等待模型或游戏动作；lane 释放后不留下继续提交的后台事务。

## 4. 数据模型与接口职责

### 4.1 持久对象

在现有世界 SQLite 数据库中增加两类数据，仍逐次校验 entity 所有权。

| 数据 | 最小职责 |
| --- | --- |
| `session_history` | 一个终态 Turn 或一条 legacy Recent 来源；owner、稳定批次键、持久序号、版本、来源标识、完整原文、时间、终态、内容指纹和首次写入时间 |
| `context_summaries` | 有界摘要文本、生成版本、前序检查点、精确覆盖集合、来源指纹、累计时间来源和创建时间 |

持久序号用于快照、分页和覆盖定位；GameTime 用于可见性，二者没有单调对应关系。数据库原始 game/world 绑定和安全 hash 路径沿用 8.1。

History 原文是权威来源。Summary 是有损生成结果，其覆盖、时间和来源信息必须可核对；摘要文本不能作为新的游戏事实。

### 4.2 Runtime 内部接口

以下是拟新增接口的行为合同，均不是 Adapter 或公开协议 API。

| 接口职责 | 输入 | 输出与保证 |
| --- | --- | --- |
| AppendHistory | owner、终态批次、稳定写入键 | 返回原有或新提交的来源标识；等价重试 no-op，冲突拒绝 |
| ReadHistorySnapshot | owner、分页游标、固定最高序号、扫描与字节限制 | 有序来源页、下一游标、水位和完整性诊断 |
| ReadSummary | owner、固定快照、当前通用时间 | 最新可用检查点或空；无法完成覆盖校验时不返回该摘要 |
| CommitSummary | owner、预期前序检查点、固定来源集合与指纹、生成结果 | 原子发布摘要及覆盖关系；前置条件变化或来源不符时拒绝 |
| SummaryGenerator | 旧摘要、待覆盖来源、输出预算、deadline | 摘要文本或错误；没有游戏工具调用权限 |

新历史批次键以 compact JSON array 编码 owner、source turn ID、source event ID、`terminal_turn` 和版本。每个批次必须有稳定 Turn ID。记录 ID、首次写入时间不参与业务等价判断。重复游戏事件产生新 Turn 的去重仍属于 Gateway。

Legacy 来源使用独立种类和原 `ProjectionBatchKey`，不与新终态批次混用。指纹依据规范结构生成，不依赖 map 遍历顺序、随机 ID 或当前时间。

覆盖关系必须能表达非连续来源集合。可以压缩为不相交序号区间，但不能用单一最大 ID 暗示所有较早记录均已覆盖。新摘要覆盖为前序覆盖与本次新增来源的并集，重复来源只计一次。

## 5. Schema 迁移与兼容

目标 schema 为 `phase8_2_history_v1`，从 `phase8_1_recent_v1` 单步事务迁移。

1. 校验旧 schema、完整表结构与原始 game/world 绑定，拒绝不兼容或损坏的库。
2. 增加 History/Summary 数据，按旧持久顺序导入仍存在的 Recent，保留原 ID、投影键、事实、Outcomes、时间和字段存在性。
3. 保留旧 Recent 表与幂等凭据；缺失字段保持缺失，已被旧保留策略删除的内容不恢复。
4. 校验导入数量、来源映射和指纹后，在同一事务中更新 metadata 并提交。任一步失败保留原 schema 和数据。

新版 Runtime 读取升级库；旧版 Runtime 降级读取升级库不列为兼容保证。schema 不匹配时不自动 reset。

启用新版 History 路径后，新交互以 History 为唯一权威写入，Recent 由历史读取投影提供，不双写两份独立提交的经历。既有 `WithMemoryStore` 注入测试继续可用；History/Summary 测试提供相应内存实现，不依赖真实模型。

`memory_enabled` 继续是整体读写开关。摘要可独立关闭，关闭后 History 继续保存，模型使用有界近期原文。`memory_store.kind`、root 和 busy timeout 延续原含义；旧 Recent 条数、凭据容量与 5 条注入项只约束 legacy 路径，不限制新 History，并对仍配置这些项的升级场景给出一次明确诊断。

## 6. 快照、时间与 Context

### 6.1 主链路

```text
AgentTurn -> Observe
    -> capture owner-scoped history watermark
    -> read valid summary and bounded history pages
    -> scope/time visibility and coverage checks
    -> optional synchronous compaction
    -> freeze summary + uncovered history for this Turn
    -> Context selection and Renderer hard gate
    -> model/tool/action steps using the same historical snapshot
    -> terminal History write attempt
    -> release entity lane
```

Context Build 保持确定性，不读取数据库，不调用 LLM。Agent 层负责 I/O 和摘要协调，Context 负责候选选择、预算、渲染和报告。当前 Turn Transcript 仍承载当前 Turn 内的因果链。

### 6.2 时间可见性

- 复用 [公共 GameTime 比较](../../runtime/internal/memory/game_time.go)，保持字段存在性与可比性语义。
- 可比较的未来来源隐藏；时间相等仍可见；缺失或不可比较为 unknown，沿用 8.1 行为并诊断。
- Owner、水位和时间可见性在最终窗口 limit 前处理。未来来源占满一页时继续有界翻页；达到扫描限制时报告不完整，不把部分结果称为全部可见历史。
- 当前时间采用既有 Event/Observation 通用时间规则，不解析游戏私有状态。
- 原文显示保留持久交互顺序及来源时间，不根据回档后的数据库序号猜测世界时间单调增长。

### 6.3 摘要可见性

摘要检查所有累计来源，包括前序检查点覆盖的内容。任一可比较来源位于当前未来，整个摘要隐藏；使用较早且可验证的检查点或仍可见的原文。

例：摘要包含 08:00 与 12:00 来源，回档至 10:00 时不能使用该摘要。回档后新增的 09:00 来源即使数据库序号更大，也须参与可见性和未覆盖集合计算。

摘要创建时间、最后来源时间和最大数据库序号均不能替代累计时间检查。时间元数据可以压缩，但必须证明与逐来源判定等价；无法证明时保留完整时间引用。unknown 不能被提升为“已证明不存在未来来源”。

SQLite 与游戏存档独立保存。时间追上后，已放弃分支的经历可能重新可见；本阶段不承诺分支隔离。

## 7. Token 与压缩合同

### 7.1 参数与预算归属

配置沿用唯一 Context 预算体系；新增项属于本阶段拟实现合同，不是当前已可用配置。

| 参数 | 初始默认值 | 职责 |
| --- | --- | --- |
| `keep_recent_tokens` | 20000 | 压缩后近期原文目标，不是每轮固定取量 |
| `max_summary_tokens` | 2048 | 注入和生成摘要的 estimated-token 上限 |
| `compaction_enabled` | true | 关闭时继续持久保存 History |
| `compaction_timeout_ms` | 10000 | 单次摘要 deadline，同时受 Turn 剩余时间限制 |
| `compaction_retry_cooldown_turns` | 3 | 失败后至少经过三个后续 Turn 再重试 |

新终态批次原文上限为 8 MiB，迁移完整保留已有原文。历史读取每页最多 64 条、8 MiB，每 Turn 最多扫描 512 条、32 MiB，读取截止 1000ms；终态写入截止 5000ms。摘要覆盖每 Turn 最多核对 16384 个来源、检查 64 个检查点，未完成校验的摘要不注入。完整摘要请求输入最多 32768 estimated tokens，响应体最多 1 MiB。各项均可配置，并以低于、等于和超过限制的 fixture 验证；不得用 Recent 条数代替字节或 token 限制。

预算统一使用现有估算器。历史可用空间为现有请求及承载消息预算中，扣除当前必需上下文、工具、当前 Turn 预留后可分配的部分。摘要生成请求同样执行输入硬上限；模型实际窗口与输出预留由所选 Provider 配置验证，不从 `65536` 推断。

### 7.2 压缩选择

1. 对可见历史形成未裁剪的逻辑候选，测量摘要与未覆盖历史的规模。
2. 使用历史有效预算的 90% 作为压力线，触及该线时检查是否存在可压缩的较早完整来源组。
3. 有效尾部目标为 `max(0, min(keep_recent_tokens, floor(历史有效预算 * 0.70) - max_summary_tokens))`，摘要加尾部目标与触发线之间保留增长空间。
4. 保留最近的完整 Turn/因果组，其余较早且未覆盖的可见来源进入压缩候选；单组超目标可投影裁剪，但不得把裁掉的部分标记为已摘要。
5. 单次模型输入装不下所有候选时，只处理最早的有界批次；覆盖精确推进，剩余来源仍未覆盖。每 Turn 最多一次尝试。
6. 当前必需上下文占满预算，或没有可压缩完整组时，执行现有 Context 降级与最终请求 hard gate，不循环调用摘要。

两次压缩之间，近期原文随交互增长。低预算配置可以使有效尾部小于 20000；实际注入量、裁剪原因、摘要耗时与首次决策等待时间均进入诊断。后续 step 保留当前因果链优先级，必要时缩减冻结历史的显示投影，不重新读库或摘要。

## 8. 摘要生成、校验与失败

SummaryGenerator 复用已配置 Provider 的模型、base URL 和凭据，增加文本响应解析、输出限制、取消与超时支持。生产 DeepSeek/OpenAI 和 Fake Generator 均覆盖；不通过只返回 Decision 的接口假装获得文本。

摘要输入只有旧有效摘要与指定历史来源，来源文本作为数据而非指令处理，不提供游戏工具。输出保留主体、陈述性质、否定、尚未解决事项及动作实际结果；“某人说已完成”不能变成“游戏已完成”。

检查点发布流程：

1. 在固定快照上选择来源和前序检查点，生成规范内容指纹。
2. 在 SQLite 事务外调用模型，应用输入、输出和剩余 Turn 时间限制。
3. 校验非空、输出长度、解析结果、owner、前序版本、精确覆盖集合和指纹。覆盖 ID 由 Runtime 决定，不信任模型自报。
4. 在短事务中重新验证前置条件，原子保存摘要及覆盖元数据。并发或来源冲突拒绝覆盖已有检查点。

摘要子请求超时、空内容、超限输出、解析或提交失败时，旧检查点和覆盖关系不变；只要父 Turn 仍有效，就使用仍可见的旧摘要及有界近期原文继续，并报告未覆盖或因预算省略的来源。父 Turn 已取消或截止时遵循既有终态路径，不继续调用决策模型。重试须同时满足冷却期、存在新增未覆盖来源和压力条件；失败状态保持在当前 Runtime 的 entity 状态中，重启不承诺延续冷却计数。

摘要成功不删除原文。覆盖元数据和旧检查点保留，支持回档可见性核对。摘要是有损压缩，不承诺逐字恢复全部细节；细节原文检索属于 8.3。

## 9. 修改范围与里程碑

涉及 `runtime/internal/memory`、`agent`、`context`、`model`、`llm` 及 Runtime 配置与对应测试。使用现有 lane、预算、Provider 和 SQLite 基础设施，不新增通用事件总线、后台队列或第二套 Context Engine。

| Milestone | 交付 | 通过标准 |
| --- | --- | --- |
| M1 来源与迁移 | 终态来源收集、稳定键、History 事务、8.1 升级 | 所有终态路径有完整已知来源；第 101 轮后首轮新历史仍存在；重复无副本，冲突拒绝；迁移失败完整回滚 |
| M2 历史投影 | 固定水位、分页、时间、覆盖与预算 | 大量未来记录跨页后仍能选择可见来源；扫描不完整可诊断；历史与当前 Transcript 不重复；最终请求有界 |
| M3 摘要能力 | 文本生成适配、触发、检查点、失败降级 | Fake 验证覆盖与预算；Provider 文本解析可测试；失败不推进覆盖；长对话可压缩且不无限重试 |
| M4 主链路与验收 | 配置、trace、自动化、真实模型与进程重启 | 重启后恢复提交的摘要与尾部；回档隐藏未来摘要；记录摘要忠实性、压缩频率和交互延迟 |

每个里程碑先增加失败测试，再实现，完成后检查 diff。实现复审与修正完成后交项目负责人评审。

## 10. 验收样例与命令

### 10.1 自动化

- FakeGame 通过通用 ContextFact 记录“我不喜欢雨天，昨天提到的数字是 7319”，同时记录一次成功动作、一次拒绝或超时动作。核对主体、原话、否定、调用意图和实际结果未混淆。
- 覆盖 Observe 失败、Provider 失败、部分动作成功后失败、取消和正常完成；SQLite 只声明实际拥有的来源。
- 8.1 fixture 同时包含 v1 时间和具有零值存在性的时间。迁移后逐字段核对；注入失败后检查旧 schema、Recent、凭据均保持。
- 同 owner 等价重试 no-op，内容冲突拒绝；跨 game/world/entity 不串线；读写失败不改变动作或终态结果。
- 压缩前历史允许超过 20000 tokens；触发后摘要与尾部满足有效预算，后续少量输入不连续触发压缩。
- 记录实际覆盖 ID，证明摘要与尾部不重复、不跨快照；超长单组或扫描限制产生明确降级，不把省略当覆盖。
- 新摘要继承 12:00 来源，当前时间回到 10:00，即使最新新增来源是 09:00，仍隐藏整个含未来内容的摘要。
- 模型超时、空输出、超限、提交冲突及冷却重试均测试；已完成动作不因 Memory 失败回滚。
- 重建 Store/Loop 后读取相同已提交来源与摘要；用 RecordingProvider 检查最终 `model.Request`，而不是仅断言中间候选。

### 10.2 实机

使用同一存档、同一 NPC 多轮中文交互，记录 world/entity、来源 ID、GameTime、配置、SQLite 提交及最终输入证据。通过受控预算触发压缩，核对主体、否定和动作状态；记录压缩前后输入 token、摘要耗时、首次决策等待时间。

结束并重新启动真实 Runtime 进程，按当前可用的手动连接流程重新接入，证明已提交摘要与近期原文进入下一轮最终请求。另行执行回档和时间追上场景，核对隐藏与重新可见边界。对象重建测试不能替代进程重启证据。

20000 的功能正确性与该默认值的实机延迟分别记录。真实摘要出现主体错置、否定丢失或动作结果提升为事实时，不能仅凭 Go 测试通过验收。

### 10.3 命令

以下从仓库根目录执行，是代码开发后的验收要求：

```powershell
go test ./runtime/internal/memory -count=1
go test ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/gateway -count=1
go test ./runtime/internal/model/... ./runtime/internal/llm/... -count=1
go test ./... -count=1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
git diff --check
```

工具链支持时执行 `go test -race ./runtime/internal/memory ./runtime/internal/agent -count=1`；无法执行时记录限制，不报告通过。

## 11. Review Gate

进入编码前核对来源完整性、迁移、实际 Provider 文本能力、预算与失败边界，冻结测试 fixture 和代码基线。方案评审通过只表示允许编码。

阶段技术验收需要四个里程碑完成、约定自动化通过、实现复审问题收敛、真实模型与真实进程重启证据齐全。技术验收通过后可进入 8.3；阶段 Accepted 由项目负责人最终确认。方案批准不构成实现验收记录。
