# GameAgent MVP0 Phase8.2-8.3 开发与验收记录

> **Date:** 2026-09-10
> **Status:** Phase8.2 Accepted；Phase8.3 Accepted
> **Code Baseline:** `006bf845e334312961e53fd0ee9da593bfaa9256`
> **Acceptance Scope:** 负责人授权无人值守开发与技术验收；Phase8.2 免游戏内实机验收，Phase8.3 使用正式 Runtime、受控 gRPC Adapter 和 DeepSeek 官方 API。

## 1. 交付范围

Phase8.2 交付终态 History、固定水位分页、来源读取、快照保护、近期历史投影、累计摘要、精确覆盖校验和 Fake/DeepSeek/OpenAI 文本生成接口。

Phase8.3 交付 `phase8_3_history_v1` 迁移、中文相邻双字与拉丁/数字连续词项索引、有界回填和查询、Retrieved Context 去重与预算、维护 lane、索引容量诊断，以及默认关闭的原文留存清理。

History 原文、索引和幂等凭据按 owner 隔离。新终态写入与索引同事务提交；索引容量不足保留完整原文并记录状态；清理事务同时处理原文、索引和 legacy 副本。Context 不访问数据库或调用摘要模型。

## 2. 生效配置

| 项目 | 值 |
| --- | --- |
| 模型 / API | `deepseek-v4-flash` / DeepSeek 官方 API |
| 模型窗口 / 决策输出 | 131072 / 49152 tokens |
| History 单批原文 | 8 MiB |
| History 每页 / 每 Turn | 64 条、8 MiB / 512 条、32 MiB |
| 摘要输入 / 输出 / 截止 | 32768 / 2048 tokens / 10000ms |
| 单来源索引 | 256 KiB 文本、1024 片段、16384 词项关联 |
| 检索 | 256 字符、32 词项、512 候选、5 片段、1024 tokens、500ms |
| 维护 | 16 来源、8 MiB、500ms；锁等待 100ms |
| 原文保留 | `retention_days = 0`，默认不清理 |

## 3. Phase8.2 证据

Phase8.2 的自动化、独立 Loop 进程和真实 DeepSeek 证据见[真实模型验收记录](./GameAgent%20MVP0%20Phase8.2%20真实模型验收记录.md)。生产模式样本完成压缩、重启回档和追时恢复，终态 History、摘要覆盖和近期尾部保持一致。

负责人已授权免游戏内实机验收。正式 server 的摘要 10 秒超时样本、模型延迟波动和诊断模式中的否定反转保留为运行限制；生产思考模式未因诊断样本修改。

## 4. Phase8.3 证据

最终验收构建正式 `runtime/cmd/server` 可执行文件，SHA-256 为 `4bc47e3bbafe6f42b71d972df9c36926de6fb64d1626d0ab57a2110cc177ccf7`，大小 24922624 bytes。三个独立进程使用同一临时 SQLite 世界和 DeepSeek 官方 API：

| 阶段 | PID | 游戏 Tick | 模型结果 | 检索耗时 / 扫描 / 读取 | 请求 / 模型响应 |
| --- | ---: | ---: | --- | --- | --- |
| 首次查询 | 59128 | 130 | `known / player:qinghe / 7319` | 4ms / 2 / 3651 bytes | 1925 tokens / 746ms |
| 回档 | 13868 | 100 | `unknown / empty / empty` | 3ms / 3 / 5154 bytes | 1581 tokens / 788ms |
| 时间追上 | 39380 | 120 | `known / player:qinghe / 7319` | 5ms / 4 / 8974 bytes | 2730 tokens / 675ms |

权威来源 `history_c7664239a57515f4cab18997383fe562af545e4d5bbae64126b0cb1893b7fa2f` 的原文为“青禾说：钓鱼时记下的数字是7319。”，GameTime Tick 为 120。摘要不含数字；最终请求只允许该来源的 Retrieved History 片段携带 `7319` 和来源 ID，并精确核对来源、主体、时间、字段路径和 Unicode 范围。回档请求中来源、数字和累计未来摘要均不可见。

三轮终态数据库写入分别为 16ms、187ms、45ms；索引维护分别为 3ms、4ms、3ms，lane 排队等待均为 0ms。原文载荷从 1759 增至 4650 bytes，索引文本列从 5732 增至 18567 bytes。验收报告的性能缺失项为空，传输错误为空，证据文件未包含 API key。

证据目录：`D:/data/project/game-agent/.tmp/phase8-real-20260910/retrieval-final3-real/retrieval-real-8904-2028806854/`。

## 5. 自动化与复审

- `go test ./... -count=1 -timeout=300s`：通过。
- `go vet ./runtime/internal/memory ./runtime/cmd/server`：通过。
- `scripts/check-architecture.ps1`：通过。
- `protocol/tests/check-go-generation.ps1`：通过。
- `gofmt -l runtime protocol`、`git diff --check`：无输出。
- 独立复审核对迁移原子性、检索预算、维护队列、清理保护、Context 来源保真及最终请求证据。发现的问题均增加复现测试并修正，最终没有未关闭的阻断发现。

## 6. 限制与数据安全

- 本机 C 编译器不支持 Go race 所需的 64 位构建，race 未验证。
- `go vet ./runtime/internal/agent` 的 protobuf mutex 复制告警在基线提交已存在，不属于本轮变更。
- Phase8.3 的真实验收使用受控 test world/entity 和 gRPC Adapter，没有操作 Stardew UI；同存档/NPC 的游戏内复验属于后续集成验证。
- 玩家 `runtime/.local` 数据库未参与清理或写入验收；测试全部使用临时数据库，生产保留策略仍为关闭。
- 工作区保持未提交状态，没有执行 commit 或 push。

## 7. 验收结论

Phase8.2 与 Phase8.3 的实现、迁移、失败降级、预算、时间可见性、独立进程恢复、真实模型调用和复审门禁均已通过。按负责人预先授权的无人值守技术验收范围，两个阶段状态均为 Accepted。
