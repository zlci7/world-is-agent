# GameAgent MVP0 Phase8.2-8.3 联合开发流程

> **Status:** Completed
> **Execution:** Phase8.2 Accepted; Phase8.3 Accepted
> **Evidence:** [Phase8.2 真实模型验收记录](./GameAgent%20MVP0%20Phase8.2%20真实模型验收记录.md)、[Phase8.2-8.3 开发与验收记录](./GameAgent%20MVP0%20Phase8.2-8.3%20开发与验收记录.md)
> **Date:** 2026-09-08
> **Code Baseline:** `main @ 006bf845e334`
> **Specifications:** [Phase8.2](./GameAgent%20MVP0%20Phase8.2%20技术开发与验收方案.md)、[Phase8.3](./GameAgent%20MVP0%20Phase8.3%20技术开发与验收方案.md)

## 1. 目标与边界

连续完成持久终态 History、Context Compaction、历史原文字面检索和可选原文清理，最终按两个阶段分别核对验收证据，统一交项目负责人评审。

- History 是约定终态来源的权威记录，Summary 是有损摘要，检索索引是可重建派生数据。
- Runtime 管理来源、存储、摘要、检索与维护；Context 保持纯选择、预算和渲染。
- 沿用 Go、`database/sql`、`modernc.org/sqlite`、世界 hash 分库、entity 隔离、通用 GameTime 和现有 token 估算器。
- 本轮不包含 Semantic Memory、向量或同义检索、FTS、模型 Memory Tool、Adapter/proto 修改、未完成 Turn 恢复、动作重放、存档分支隔离和多 Runtime 实例共享写入保证。
- `retention_days = 0` 为默认值。清理功能必须完成测试，实际玩家数据库的清理由部署配置显式开启。
- `memory_enabled` 控制整体读写；关闭 compaction 仍保存 History；关闭 retrieval 仍提供 Summary 与近期原文；关闭 retention 保留剩余原文。
- 开发过程不自动执行 git commit 或 push；提交与推送按项目负责人明确授权执行。

## 2. 开发门禁

8.3 的编码准入以 8.2 技术验收通过为条件。负责人于 2026-09-09 授权无人值守开发与自动验收，8.2 免游戏内实机验收。8.3 完成自动化、实现复审和受控真实模型及进程验证后确认验收，保存交付记录并关机。模型延迟和未验证项目如实记录；存在阻断问题时不执行关机。

| 门禁 | 必须满足的条件 | 后续工作 |
| --- | --- | --- |
| G0 开发准入 | 联合流程确认；M0 合同、容量 fixture、数值限制与基线完成核对 | 进入 8.2 功能开发 |
| G2 8.2 技术验收 | M1-M4 完成；8.2 自动化、真实模型及真实进程证据齐全；复审问题收敛；实际 schema/API/config 冻结 | 按已确认的门禁进入 8.3 |
| G3 检索验收 | M5-M6 完成；窗口外原文进入最终模型请求；重建、失败降级与队列回归通过 | 进入原文清理开发 |
| G4 联合交付 | M7-M8 完成；两阶段证据完整；所有阻断问题修正并复测 | 交负责人评审，分别确认 Accepted |

```text
M0 合同与容量基线
  -> M1 History 存储与迁移
  -> M2 全终态来源与主链路写入
  -> M3 固定快照、时间与 Context 预算
  -> M4 文本生成、Compaction 与 8.2 验收
  -> G2
  -> M5 索引、查询、回填与维护调度入口
  -> M6 Retrieved Context 与检索验收
  -> G3
  -> M7 原文留存与原子清理
  -> M8 联合回归、复审与实机验收
  -> G4
```

## 3. M0：合同与容量基线

M0 是功能编码前的设计收口与 fixture 准备。以下每项都须形成明确合同或可运行验证，不以未确定的默认值进入 M1。

### 3.1 跨阶段合同

| 对象 | 必须固定的内容 |
| --- | --- |
| History 来源 | owner、稳定批次键、持久序号、来源种类与版本、规范 payload、内容指纹、原始现实写入时间；缺失字段与零值的区分；指纹不依赖 map 遍历顺序、随机记录 ID 或当前时间 |
| 执行来源 | 按 step/call 顺序表达决策、实际游戏回执与 Runtime 判定；技术错误仍带回已知部分结果；并行动作按调用位置归并 |
| Summary 覆盖 | parent 检查点、精确来源集合、来源指纹、所有累计时间；非连续覆盖、前置版本校验和原子发布；回档选中较早可见检查点时的提交条件 |
| 实际显示范围 | 来源 ID、稳定字段定位、完整单元标记或 Unicode 字符区间；与 Summary 的完整来源覆盖分开建模 |
| 快照保护 | owner、水位、读取生命周期和释放规则；保护信息在 Runtime 内共享，覆盖同 owner 不同连接的并发读取 |
| 清理后来源头 | 稳定键、指纹、时间、首次写入年龄、覆盖和清理状态可保留；原文与 legacy 副本有明确关联 |
| 索引覆盖 | 索引格式版本、完成范围和未完成来源；晚写入记录不能使早期缺口被误报为已完成 |

接口按两份方案定义的行为合同落实为 Go 类型，再开始依赖这些接口的任务。拟新增职责包括 History/Summary Store、SummaryGenerator、来源片段和快照保护；保持 `WithMemoryStore` 的 legacy 注入兼容。

终态收口顺序固定为：确定结果与已知来源、进行一次有界同步 History 写入尝试、维持既有 TurnCompletion 语义、发出唯一终态 trace、释放快照与 lane。取消后的持久化使用单独有截止时间的写入 context，只服务于已知终态来源，不继续模型或游戏动作。未创建 Turn 的 admission 拒绝不构造终态历史。

### 3.2 容量与时间限制

保留方案已给定的初始值：近期目标 20000 tokens、摘要上限 2048 tokens、压缩超时 10000ms、失败冷却 3 个后续 Turn；query 256 个 Unicode 字符、32 个词项、扫描 512 个候选片段、最终 5 个片段、检索预算 1024 tokens、保留期 0 天。

容量测试使用通用 FakeGame 来源，包含大 ContextFacts、多 step/call、嵌套参数、大 ActionResult、中文及补充平面字符。区分协议可接受范围和 History 配置允许的单批范围；超过单批限制的合法输入仍须按合同整批失败并诊断。

| 限制组 | M0 必须产出的结果 |
| --- | --- |
| 持久化 | 单批序列化字节上限、终态写入截止时间、SQLite 锁等待的组合验证 |
| 读取 | 单页条数/字节、每 Turn 扫描与累计字节上限、Summary 覆盖校验与检查点回退上限 |
| 生成 | 摘要输入和输出上限、HTTP 响应读取上限、所选模型窗口与输出预留、父 Turn 剩余时间约束 |
| 索引 | 单来源可索引字节、片段和词项容量，回填每批条数/字节、查询截止时间 |
| 维护 | 单任务批量、耗时与锁等待上限，快照保护不能完成核对时的保留行为 |
| 性能 | 固定数据规模、测试机器与配置，查询/维护的验收阈值；与模型生成耗时分开统计 |

新批次符合原文容量限制、但超过索引容量时，完整保存原文并记录索引未覆盖状态；数据库实际写入错误仍整笔回滚。存量回填遇到超限来源时同样保留原文并标记索引覆盖不完整。容量或索引版本未变化时不重复进行必然失败的回填。

新增安全默认值：新批次原文 8 MiB；历史每页 64 条、8 MiB，单 Turn 扫描 512 条、32 MiB，读取截止 1000ms、终态写入截止 5000ms；摘要累计校验 16384 个来源、64 个检查点，完整生成请求输入 32768 estimated tokens、响应体 1 MiB；每来源索引文本 256 KiB、1024 个片段、16384 条去重词项关联；查询截止 500ms；维护每批 16 个来源、8 MiB、500ms，锁等待 100ms。迁移完整保留已有原文。

维护的 16 个来源限制同时约束完整原文读取和处理候选。删除资格证明只读取有界元数据，分别受 16384 个覆盖来源、64 个摘要检查点和 512 个近期来源头的扫描上限约束，与原文共同计入 8 MiB 和 500ms。证明不完整时保留原文。维护只访问已升级数据库，迁移由正常 History 初始化入口执行。

当前 DeepSeek 运行配置声明 `context_window_tokens = 131072`、`max_output_tokens = 49152`；摘要输出仍受 `max_summary_tokens = 2048` 约束。模型配置通过 `env:DEEPSEEK_API_KEY` 引用本地凭据。

历史预算从必需上下文、工具、当前因果链预留之后计算，检索预算也计入总预算分配。最终 Renderer 裁剪后重新核对实际显示范围与重复片段；后续 step 可以重新选择冻结候选的显示内容，不重新访问数据库或调用摘要。

## 4. 模块分工

以下为拟定文件拆分，不表示这些文件或接口已经存在。对应测试与实现放在同一 package，配置随所属任务接入。

| 职责 | 拟新增文件 | 现有接入位置 |
| --- | --- | --- |
| History/Summary 合同与内存实现 | `runtime/internal/memory/history.go`、`summary.go`、`in_memory_history.go` | `memory/store.go`、`memory/record.go` |
| SQLite 迁移与持久化 | `runtime/internal/memory/sqlite_migrations.go`、`sqlite_history.go`、`sqlite_summary.go` | `memory/sqlite_store.go` |
| 终态来源与协调 | `runtime/internal/agent/turn_history.go`、`history_context.go`、`compaction.go` | `agent/loop.go`、`agent/scheduler.go` |
| 历史显示与预算 | `runtime/internal/context/history_projection.go` | `context/context.go`、`renderer.go`、`request_sizing.go` |
| 最小文本生成能力 | `runtime/internal/model/text.go`、各 `llm` Provider 下的 `summary.go` | `model/provider.go`、`llm/factory.go` |
| 派生索引与查询 | `runtime/internal/memory/history_index.go`、`sqlite_history_index.go` | History 事务与 Agent 快照协调 |
| 检索片段投影 | `runtime/internal/context/retrieved_history_projection.go` | Context Build、Renderer 与报告 |
| 维护与清理 | `runtime/internal/agent/history_maintenance.go`、`runtime/internal/memory/sqlite_retention.go` | `session/lane.go`、`gateway/gateway.go` |
| 配置与装配 | 对应现有配置测试 | `agent/config.go`、`llm/factory.go`、`runtime/config/agent.json`、`runtime/config/model.json`、`runtime/cmd/server/main.go` |

SQL schema、迁移和共享类型由同一开发主线维护。Provider 文本适配可在 M0 合同固定后与 M1-M3 并行；纯 Context 选择测试可在来源合同固定后准备。8.3 功能实现等待 G2，清理实现等待 G3。

## 5. 分步开发与通过条件

每个任务执行“失败测试、确认失败原因、最小实现、定向回归、diff 复审”的循环。复审发现先复现、补回归再修正；既有 legacy 路径的测试与新 History 行为分开验证。

### M1：History 存储与迁移

- [x] 固定旧 schema SQL 和样例数据 fixture，覆盖旧版时间编码、显式零值、不同 owner 和已被 Recent 清理但仍有凭据的批次；fixture 不依赖新版 schema 自动生成。
- [x] 实现来源规范化、稳定键、指纹、顺序、History 内存实现及 SQLite Append；等价重试返回既有来源，冲突不改原数据。
- [x] 实现 `phase8_1_recent_v1 -> phase8_2_history_v1` 事务迁移，保留原 ID/键/时间/年龄、legacy 原文和凭据；核对来源映射及数量。
- [x] 注入建表、导入和 metadata 更新失败，断言旧 schema 与数据完整保留；覆盖空库、重复打开、并发首次打开和世界绑定错误。

通过条件：新 History 不受旧 100 条保留上限影响；第 101 轮后首轮仍在；原文、身份与幂等记录没有半提交；迁移不制造旧 Recent 缺失的 action ID 或完整 output。

### M2：全终态来源与写入接线

- [x] 在 Loop/Scheduler 测试中列出终态矩阵：正常 settle、Observe 失败、Provider 失败、Context hard gate、无效决策、动作拒绝、技术错误、部分成功、异步后重新观察失败、预算耗尽和取消。
- [x] 从执行返回结果采集完整已知来源，保留真实 ActionResult；Runtime 拒绝、跳过、未启动、超时等单独表达，按 step/call 顺序归并并行结果。
- [x] 来源直接采集 GameEvent、Observation 引用和执行结果；不归档完整 Request/Observation.state，不从 Trace 回填，不为 ActionResult 补造独立 GameTime。
- [x] 接入单一终态收口和有界持久化 context；游戏/模型等待期间不开 SQLite 写事务，lane 释放前结束提交或回滚。
- [x] 接入 memory 开关及兼容配置；新路径唯一写入 History，legacy 注入路径保留已有约定。

通过条件：每个已创建 Turn 的终态只尝试提交一个批次；取消后仍能在受控写入预算内保存已知来源；History 失败不改变动作或 TurnCompletion 结果；唯一终态 trace 仍位于该 Turn 末尾；队列中下一轮可读取前轮已提交数据。

### M3：固定快照、时间与 Context

- [x] 实现 owner-scoped 水位、分页、来源读取及快照保护生命周期；候选读取有条数、字节、扫描和时间上限。
- [x] 在最终窗口选择前执行 owner、水位、GameTime 与覆盖判断；未来来源占满一页时继续有界翻页，范围不完整有明确报告。
- [x] 实现纯历史投影及实际显示范围，当前 Transcript 与 previous turns 分开；请求与承载消息共同受硬预算约束。
- [x] 加入 RecordingProvider 测试，验证同 Turn 多 step 的水位与历史来源不变，显示可以随当前因果链增长而缩减。

通过条件：大量未来记录不会遮蔽扫描预算内的可见历史；零值、unknown、时间相等、回档后晚写入的较早 GameTime 均符合通用比较语义；裁剪的原文未被标记为摘要覆盖；当前必需事实与最新因果组受保护。

### M4：文本生成、Compaction 与 G2

- [x] 为 Fake、DeepSeek、OpenAI 增加独立的最小文本生成能力；用 HTTP fixture 验证解析、取消、超时、输入/输出/响应字节上限，生成请求没有游戏工具。
- [x] 实现纯压缩选择：90% 压力线、有效尾部公式、最近完整组保护、最早有界压缩批次和每 Turn 至多一次尝试。
- [x] 在事务外生成摘要，在短事务内校验 owner、parent、精确集合与指纹并提交；保留旧检查点、累计时间和完整覆盖关系。
- [x] 验证空输出、超限、解析失败、子请求超时和 CAS 冲突均不推进覆盖；父 Turn 有效时使用仍可见的旧摘要与有界尾部继续，并报告未覆盖和省略来源；冷却、新增来源与压力三个条件同时满足才重试。
- [x] 完成 8.2 全部回归与独立实现复审；真实模型与独立 Loop 进程验证摘要、尾部、回档和主体来源。负责人授权免游戏内实机验收，正式 server 的模型超时样本保留在验收记录中。

通过条件：覆盖可以包含非连续来源，累计未来来源隐藏整个摘要；覆盖检查包含前序检查点，unknown 保留其不确定性，检查点回退无法核对时安全降级；请求始终有界；成功摘要不删除原文；G2 证据齐全后冻结实际 schema、接口和配置。

### M5：索引、查询与维护入口

- [x] 实现 `phase8_2_history_v1 -> phase8_3_history_v1` 事务迁移，并验证最终程序可顺序完成 `8.1 -> 8.2 -> 8.3`。
- [x] 结构化提取 ContextFact、工具参数和实际结果中的文本，生成稳定字段定位与词项；中文相邻双字、拉丁/数字连续词项共用一条通用管线。
- [x] 实现新 History 与完整索引同事务提交；旧原文有界回填、幂等重建、覆盖进度和未完成诊断；索引格式与来源指纹可核对。
- [x] 实现 owner/水位约束和稳定排序的有界候选查询；参数绑定，显示前核对权威原文；高频词查询单独测量 SQL 成本与截止时间。
- [x] 扩展现有 lane 的可合并维护入口：维护请求不占玩家待执行队列容量，已有玩家任务先运行，每次至多一个有界维护批次，关闭连接时可取消。
- [x] 每次 Turn 持久化尝试后可请求维护；旧索引回填独立于 retention 开关，重启后续接未完成进度。没有新 Turn 时不承诺自动完成全部回填。

通过条件：维护不会新增 `session_queue_full` 拒绝；索引未完整时仍能使用 8.2 连续性；重复重建无副本，索引失败不留下半批 History；扫描 512 个候选的应用层上限不被误当作 SQL 总成本保证。

### M6：Retrieved Context 与 G3

- [x] 当前 ContextFact 只生成一次 query，与同一 Turn 水位绑定；后续 step 复用有界候选。
- [x] 先按时间过滤、再按实际近期显示片段去重，最后执行条数与 token 限制；Summary 已覆盖的原文仍可参与检索。
- [x] 保留主体或字段归属、source ID、时间、原文定位及裁剪标记；最终 Renderer 后核对重复和预算，保持 Context 无数据库 I/O。
- [x] 将包含指令性文本的历史 fixture 作为来源数据渲染，断言其不会变成 System、工具定义或 Runtime 控制字段；SummaryGenerator 同样保持无工具权限。
- [x] 建立“旧原文含 7319、已离开近期窗口、Fake 摘要不含 7319、当前只问钓鱼”的端到端测试，直接断言 RecordingProvider 的最终请求。
- [x] 覆盖前五个候选重复而第六个有效、近期原文部分裁剪、大量未来候选、索引部分完成、页读取失败和扫描耗尽。

通过条件：扫描预算内的窗口外原文可进入最终请求且不重复；检索预算不挤出必需事实与最新因果链；无查询、无匹配、读取失败、未就绪、扫描不完整与预算未纳入可区分；普通未命中不报告“已清理”。

### M7：原文留存与原子清理

- [x] 注入时钟并验证 retention 关闭、未到期、恰好到期和严格超期；年龄不因迁移、重建、重试或 GameTime 回退改变。
- [x] 根据完整 Summary 覆盖、当前有效摘要的近期尾部、最新完整单元与所有在用快照确定保护集合；不能证明删除资格时保留。
- [x] 在 M5 的同一维护入口接入有界清理，事务内重新校验资格；同 owner 不同连接的在用快照也能阻止清理，无长事务等待模型。
- [x] 在同一事务清理原文、索引片段/词项与 legacy 原文副本，保留来源头、指纹、年龄、时间、覆盖、映射、幂等凭据和已清理状态。
- [x] 注入每个删除步骤失败，验证整体回滚；验证已清理来源的等价重试、冲突重试和索引重建不会恢复原文。

通过条件：只有全部满足资格的来源被清理；未覆盖、部分覆盖、尾部和在用来源保留；清理不调用模型；删除后累计未来时间判断仍成立；关闭 retention 保留剩余数据，不能恢复已删原文。

### M8：联合回归、复审与交付

- [x] 回归空库、8.1 库、8.2 库和已部分回填的 8.3 库；两次迁移分别原子提交，第二次失败可停在完整 8.2 状态，再次启动可重试。
- [x] 以 8.2、8.3 实现 diff 分别做独立复审，再检查跨阶段组合：覆盖、时间、预算、幂等、原文/索引提交、队列和清理保护。
- [x] 所有发现逐项复现、评估影响、修正并复测；数据错属、丢失、未来泄漏、错误覆盖、动作结果回归与请求越界均阻断交付。
- [x] 在独立测试 world/entity 使用受控中文来源、正式 Runtime 可执行文件和 DeepSeek 官方 API，完成窗口外细节检索、三个真实进程重启、回档隐藏及时间追上的证据链；Stardew UI 同存档/NPC 复验保留为集成验证限制。
- [x] 清理验收仅使用独立测试世界或一致性数据库副本，核对默认关闭、显式开启和删除后的降级能力。
- [x] 按固定规模记录 SQLite 查询、索引回填、维护等待与执行、输入 token、摘要耗时和首次决策延迟，交负责人评审。

## 6. 验收执行与证据

定向测试随任务执行；G2 和 G4 执行完整回归。以下命令均从仓库根目录运行：

```powershell
go test ./runtime/internal/memory -count=1
go test ./runtime/internal/agent ./runtime/internal/context -count=1
go test ./runtime/internal/session ./runtime/internal/gateway -count=1
go test ./runtime/internal/model/... ./runtime/internal/llm/... -count=1
go test ./... -count=1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
git diff --check
```

工具链支持时运行：

```powershell
go test -race ./runtime/internal/memory ./runtime/internal/agent ./runtime/internal/session ./runtime/internal/gateway -count=1
```

| 证据 | 判断通过的依据 |
| --- | --- |
| 迁移与持久化 | owner、source ID、指纹、首次写入时间、迁移数量和失败回滚前后数据核对 |
| 摘要 | 固定水位、parent、精确覆盖集合、累计时间、输入输出限制、真实模型忠实性 |
| 检索 | 查询词项、候选和最终纳入来源、原文片段、近期显示范围、最终模型 Request |
| 生命周期 | 各终态来源、写入结果、唯一终态信号、lane 释放顺序与下一轮可见性 |
| 清理 | 显式配置、注入时钟、资格与保护理由、已清理状态、索引/legacy 原子变化 |
| 真实进程 | 终止与启动的进程证据、重连后的相同 world/entity、恢复来源与最终请求 |
| 性能 | 固定数据规模与配置、SQL/维护/模型分项耗时、限制命中和降级报告 |

真实模型测试使用受控来源与请求记录，不默认开启玩家完整 prompt 的生产日志。NPC 表示“记得”、中间候选命中或重建 Store 对象，都不能替代最终请求与真实进程证据。

## 7. 升级与评审交付

- 升级测试使用独立 memory root。真实世界升级前停止写入并取得一致性备份，不直接复制仍在写入的单个数据库主文件。
- 正式回退同时考虑程序与匹配的升级前数据库备份；旧程序不保证读取升级 schema，也不自动 reset。
- 交付包包含代码基线、变更范围、实际配置、两阶段测试摘要、实机证据、独立复审结论和剩余限制。没有执行或被工具链阻断的检查列为未验证。
- Phase8.2 与 Phase8.3 各自满足验收标准后，由项目负责人分别确认 Accepted。
