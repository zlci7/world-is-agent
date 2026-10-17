# Phase11.1 开发与验收记录

日期：2026-10-17（Asia/Shanghai）

状态：生产代码、自动化回归、四个核心包 race 检测、浏览器检查与便携包进程验证通过；真实游戏组合恢复验收待完成。

## 交付范围

- 已有 Task SQLite 上的世界目录、任务列表、详情、状态筛选和分页。
- 查询绑定当前 Runtime 实例、游戏、世界与存档工作集；详情绑定角色和任务。
- 当前 head、计数和任务页来自同一只读事务。单次数据库查询截止 500ms，记录读取预算 16MiB，JSON 响应上限 256KiB，默认 20 条、最多 50 条。
- 世界执行锁忙碌时立即保守返回未确认状态；查询取消和实例退役能够结束读取。HTTP 查询不激活世界、推进时钟或调度任务。
- 中文正式 Vue 控制台，包含运行概览、只读预约、游戏与模型、运行记录和高级详情。
- 星露谷田园与环世界殖民地像素背景、分层透明窗口、统一间距与响应式布局。游戏配置成功后直接更新主题。
- 任务状态、世界可用性、任务结果与控制权清理分别表达。页面关闭不影响已保存任务。

Runtime 版本保持 `0.2.0`；Stardew Adapter `0.1.1`、RimWorld Adapter `0.1.0`，Protocol 保持 `v1alpha2`。Adapter 源码及数据库 schema 保持现有版本。Phase11.2 的账号、WIA Key、云端托管、额度和部署均属于独立方案。

## 自动化证据

| 检查 | 结果与范围 |
| --- | --- |
| `go test ./... -count=1` | 全部通过，包含现有 Task、Gateway、Context、Memory 和实例生命周期回归 |
| 独立进程恢复 | 子进程提交 Task 后不调用 Close 直接退出；新进程读取最新 working head，旧 generation 失效；精确 checkpoint 恢复保留对应集合 |
| 回档结果一致性 | 回到执行前版本后，Console 终态集合移除未来结果，任务恢复等待，`ListRecentResults` 不再返回未来结果 |
| 只读查询 | 跨角色分页、全部状态计数、详情 owner 隔离、旧工作集游标失效、非法范围和读取预算；原 Task、wake 和幂等历史保持一致 |
| 世界目录 | 已绑定 Ready、保存中、断线和锁忙碌场景；普通断线与恢复错误分开；过期保存屏障不无限显示保存中；展示名为防御性复制 |
| HTTP 与实例 | 沿用本地认证和 Host 校验；拒绝写请求、重复和未知参数；同游戏实例替换产生新身份，旧请求失效；并发替换不混用实例 |
| 前端 | `type-check`、`test:ordering`、`build` 通过；门控覆盖实例、世界、筛选变化与失效请求 |
| 架构与协议 | `scripts/check-architecture.ps1` 通过，包含 Protocol static check |
| Stardew 相关回归 | `dotnet test`：ProtocolMapper 186、RuntimeClient 26、TaskExecution 166 项通过；显式引用主仓库 Protocol 与本机游戏程序集 |
| race | Task、Gateway、Bootstrap、HTTP 四包完整检测通过，无数据竞争报告；499 个顶层测试和 788 个子用例通过，2 个 Windows 符号链接权限相关用例跳过 |

现有检查点、证据和 Protocol 链路回归覆盖 prepared/Finish、同 run 重绑、等待证据、恢复失败锁存、错误或损坏引用、旧代次和重复事实。测试中的游戏能力与模型是 fake；它们证明机制断言，不证明真实 NPC 路线或游戏主线程行为。

核心新增测试位于 [Task 查询](../../runtime/internal/task/console_query_test.go)、[Gateway 世界快照](../../runtime/internal/gateway/console_world_test.go)、[实例替换](../../runtime/internal/bootstrap/task_console_test.go)、[HTTP 安全边界](../../runtime/internal/httpapi/tasks_test.go) 和 [真实 HTTP 持久任务读取](../../runtime/internal/httpapi/tasks_integration_test.go)。

## 后台验收

验证时间：2026-10-17 16:50–16:53（Asia/Shanghai，race 执行区间）。测试源码为 `02ec54a240295632bf9ea44bc2670265e45e1ecb`，测试开始时工作区干净。

| race 包 | 顶层测试通过 | 子用例通过 | 跳过 | 包执行耗时 |
| --- | ---: | ---: | ---: | ---: |
| `runtime/internal/task` | 265 | 648 | 2 | 82.237 秒 |
| `runtime/internal/gateway` | 157 | 134 | 0 | 41.288 秒 |
| `runtime/internal/bootstrap` | 40 | 6 | 0 | 14.597 秒 |
| `runtime/internal/httpapi` | 37 | 0 | 0 | 7.975 秒 |

四个包退出成功，无失败用例、无 `WARNING: DATA RACE` 报告。跳过项为 `TestStoreLockCanonicalizesExistingDatabaseSymlink` 和 `TestStoreLockRejectsDanglingDatabaseSymlinkBeforeCreatingSidecar`，原因是普通 Windows 账户缺少创建文件符号链接的权限。该路径仍需在支持符号链接的环境验证。

环境为 Go `1.25.3 windows/amd64`、GCC `14.3.0`、MinGW-w64 UCRT `12.0.0`。便携编译器位于本机忽略目录 `runtime/.local/phase11-qa/toolchain/gcc14`。`CGO_ENABLED=1`、`CC`、PATH 和 `GOMAXPROCS=2` 仅作用于本次测试进程，系统配置和普通启动环境保持原值。

命令：

```powershell
$env:CGO_ENABLED = '1'
$env:CC = (Resolve-Path 'runtime/.local/phase11-qa/toolchain/gcc14/mingw64/bin/gcc.exe').Path
$env:PATH = (Split-Path -Parent $env:CC) + ';' + $env:PATH
$env:GOMAXPROCS = '2'
go test -race -json -p 1 ./runtime/internal/task ./runtime/internal/gateway ./runtime/internal/bootstrap ./runtime/internal/httpapi -count=1 -timeout 15m
```

工具链来自 [WinLibs 固定版本发布](https://github.com/brechtsanders/winlibs_mingw/releases/tag/14.3.0posix-12.0.0-ucrt-r1)。归档为 `winlibs-x86_64-posix-seh-gcc-14.3.0-mingw-w64ucrt-12.0.0-r1.7z`，SHA-256 与发布方文件一致：`0ca3d2b5c2eb11ad60227b5da3099ff1fb2d0ac0f129d95f34b78255d9a822b1`。`libsynchronization.a` 检查满足 [Go Windows race 工具链要求](https://go.dev/doc/articles/race_detector#Requirements)。

本轮同时复验 Task、Gateway、Bootstrap、HTTP、Context、Memory 六包普通测试，前端类型与请求顺序检查，以及交付包在独立数据根的真实进程启动、配置、切换与重启，均通过。包校验值与交付记录一致。测试未启动游戏，未读取或改写玩家正式存档。

原始 race 事件和摘要保存在本机忽略目录的 `runtime/.local/phase11-qa/race-results.jsonl` 与 `race-summary.json`。race 检测结论覆盖本轮执行过的路径；真实 NPC 路线、游戏主线程和保存读档组合仍属于实机验收。

## 浏览器与视觉证据

gstack `/browse` 检查正式构建的 Vue 页面，连接隔离测试数据库和真实 HTTP API。截图中的 `test-actor-*`、预约和世界均为合成测试数据，没有运行真实游戏。

- 1440px、1024px、390px：未出现横向溢出，侧栏、标题、世界选择及任务面板对齐。
- 状态筛选为空、已结束任务及取消结果、手机详情与返回路径均可操作。
- 手机详情打开后焦点进入返回按钮，返回后恢复所选任务按钮。
- 模型连接检查失败时保留运行实例，表单继续可编辑，取消清空输入密钥。
- 游戏候选选择保持当前主题；成功应用后切换主题并清理旧 Task 视图；RimWorld 明确显示预约未启用。
- 测试服务器关闭时，页面撤销 Ready 信息并显示连接失败。检查过程中出现的 HTTP 400 与连接拒绝来自主动故障验证。

| 正式页面 | 截图 |
| --- | --- |
| 桌面任务 | [1440px](acceptance/desktop.png) |
| 窄屏任务 | [1024px](acceptance/user-1024.png) |
| 手机列表 | [390px](acceptance/mobile.png) |
| 手机详情 | [390px 详情](acceptance/mobile-detail.png) |
| 游戏与模型 | [设置](acceptance/settings.png) |
| 环世界任务边界 | [环世界](acceptance/rimworld.png) |

内部 CR：后端独立上下文审查的锁等待问题已复验解决。视觉独立上下文审查判定构图与透明层级符合确认样稿；复验将手机焦点恢复和设置截图记为 resolved，disposition 为 `ship`，结论范围为该修复清单。自动化测试由主任务执行。

## 便携包

产物：`dist/phase11.1/world-is-agent-v0.2.0-windows-amd64.zip`，内含 `wia-runtime.exe`、README 和许可证。该目录为本地忽略的验收产物，没有创建 tag、公开 Release 或推送。

包从仓库外解压，以独立数据根和 fake provider 启动真实进程：首次游戏选择、模型配置需求、Task 实例身份、空世界目录、内嵌中文任务资源、游戏替换及再次启动后配置保留通过。RimWorld 返回任务未启用。该验证没有调用真实模型或连接真实游戏。

文件校验见 [构建校验值](phase11.1-package.sha256)。

## 实机验收条件

使用测试存档副本、独立数据根和已验证路线。按 [实机步骤](../development/testing.md#appointment-recovery-and-console) 执行正常保存退出再加载、等待时 Runtime 重启、移动时断线、到达后等待期间保存重连、见面与过期两种终态，以及创建前和执行前回档。

每次核对当前 Task 工作集、有效 wake、operation 与证据、Web 状态、NPC 位置、控制权释放或合法交接、原生日程恢复及后续对话。无法确认执行时保持协调或暂停；引用损坏时保持恢复受阻。测试失败保留隔离数据和日志，不清空正式任务库或改写玩家正式存档。

普通异步 Action 的内存等待仍不跨进程恢复；游戏关闭时任务不推进；通用 Memory 没有完整废弃分支隔离；延迟寄信 `schedule_mail` 不在本次交付范围。当前结果不构成长时间无人值守成功保证。
