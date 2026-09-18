# GameAgent MVP0 Phase10 验收检查表

> **用途:** 把当前待验收的两块合成一张可勾选的清单——10.2-1 的本地验证（不需要游戏）与 10.1 的实机/模型验收（需要游戏与真实模型）
> **相关方案:** [Phase10 总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md) §3.2 / [Phase10.1 邮件能力方案](GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md) §5.4–§5.6、§5.2
> **写作原则:** 每条都写清「命令 / 期望 / 不通过时先看什么」。前一段没有结论不要进入下一段，否则失败无法归因。

---

## A. 10.2-1 Runtime Bootstrap 与 Data Root（本地可复现，不需要游戏）

### 准备

- [ ] `127.0.0.1:50051` 空闲，没有旧的 Runtime 进程占用

  ```powershell
  Test-NetConnection -ComputerName 127.0.0.1 -Port 50051 -InformationLevel Quiet
  ```

  期望 `False`。若是 `True`，先退出上一次的 Runtime；**不要**为了跑验收去杀其它进程。

- [ ] 构建产物

  ```powershell
  $exe = Join-Path $env:TEMP "wia-runtime-check.exe"
  go build -o $exe ./runtime/cmd/server
  ```

### A1 未配置也能启动，且进程不退出

```powershell
$root = Join-Path $env:TEMP "wia-check-empty"
Remove-Item -Recurse -Force $root -ErrorAction SilentlyContinue
& $exe --data-root $root
```

| 检查 | 期望 |
| --- | --- |
| 日志 | `GameAgent data root: <root>` |
| 日志 | `GameAgent agent core is not ready (needs_configuration): model configuration not found at <root>\config\model.json` |
| 日志 | `GameAgent Runtime listening on 127.0.0.1:50051` |
| 进程 | **保持存活**，不退出 |
| 目录 | `<root>` 下生成 `config/`、`data/`、`secrets/` |
| 目录 | `data/tasks/tasks.sqlite`、`data/traces.jsonl` 被创建 |

不通过时先看：进程是否在 `net.Listen` 之前退出（真 Fatal 会带 `open runtime failed` / `open task runtime failed`）。

### A2 缺凭证时报错指向具体原因，而不是"agent 不说话"

把仓库的开发配置复制进数据根，但**不设** API key 环境变量：

```powershell
Copy-Item -Recurse runtime\config (Join-Path $root "config")
$env:GAMEAGENT_AGENT_CONFIG = Join-Path $root "config\games\stardew-valley\agent.json"
& $exe --data-root $root
```

| 检查 | 期望 |
| --- | --- |
| 日志 | `agent core is not ready (needs_configuration): model configuration at <path> is unusable: DEEPSEEK_API_KEY is required for provider "deepseek"` |
| 进程 | 保持存活并继续监听 |

### A3 配置完整时进入 ready

```powershell
$env:DEEPSEEK_API_KEY = "<任意非空占位值>"
& $exe --data-root $root
```

| 检查 | 期望 |
| --- | --- |
| 日志 | `GameAgent agent core ready: model config <path>` |
| 日志 | `GameAgent Runtime listening on 127.0.0.1:50051` |
| 定义目录 | 日志无 `definition catalog ... is unusable`（`definition_catalog_root` 相对数据根解析成功） |

清理：`Remove-Item Env:\GAMEAGENT_AGENT_CONFIG, Env:\DEEPSEEK_API_KEY`

### A4 真实客户端能完成一次服务会话

不需要游戏：验收测试会用 fake provider 启动**真实二进制**，并在临时数据根下完成完整回合。

```powershell
$env:GAMEAGENT_HISTORY_SERVER_ACCEPTANCE = "1"
go test ./runtime/cmd/server/ -run TestHistoryServerAcceptance -count=1
$env:GAMEAGENT_RETRIEVAL_SERVER_ACCEPTANCE = "1"
go test ./runtime/cmd/server/ -run TestRetrievalServerAcceptance -count=1
```

| 检查 | 期望 |
| --- | --- |
| 结果 | 两个测试均为 `ok` |
| 行为 | 子进程以 `--data-root <临时目录>` 启动，trace 落在 `<临时目录>/data/traces.jsonl` |
| 行为 | 三个服务器进程完成 compact → rollback → catchup（history） |

不通过时先看：测试是否因为 `GAMEAGENT_*_REAL_ACCEPTANCE` 未设置而停在 fake 模式（这是预期的）、或端口被占（`BLOCKED:` 前缀）。

### A5 不依赖当前工作目录

从任意目录启动，数据仍落在 `--data-root` 指定的位置：

```powershell
Push-Location C:\
& $exe --data-root $root
Pop-Location
```

| 检查 | 期望 |
| --- | --- |
| 目录 | `C:\config`、`C:\data` 等**不**被创建 |
| 目录 | 数据仍出现在 `<root>` 下 |

### A6 正常关闭

| 检查 | 期望 |
| --- | --- |
| 日志 | `shutting down GameAgent Runtime` |
| 进程 | 退出码 0，无残留 `wia-runtime-check.exe` |

> **验证方式的限制:** Windows 上无法从脚本向控制台子进程投递 Ctrl+C（`taskkill` 不加 `/F` 对控制台进程无效），因此本条只能这样覆盖：信号处理体调用的两条路径各有单测——`TestTaskRuntimeConfigStartupAndShutdown` 覆盖 `shutdown`，`TestCloseReleasesTheTraceRecorderOnce` 覆盖 trace recorder 的 `Close()` 幂等。**信号投递本身未验证，且该代码本轮未改动。**

### A 区验证结果（2026-09-18，本地 HEAD `7aec2aa` + 本节改动）

| 条目 | 结果 | 证据 |
| --- | --- | --- |
| A1 未配置可启动 | 通过 | 三条日志齐全，进程存活，`config/ data/ secrets/ data/tasks/tasks.sqlite data/traces.jsonl` 均创建 |
| A1 监听 | 通过 | `127.0.0.1:50051 Listen`，`TcpClient.Connect` 成功 |
| A2 缺凭证报错具体 | 通过 | `model configuration at <root>\config\model.json is unusable: DEEPSEEK_API_KEY is required for provider "deepseek"`，进程继续监听 |
| A3 配置完整进入 ready | 通过 | `agent core ready` + `definition catalog root: <root>\config\games` + `listening` |
| A4 真实客户端服务会话 | 通过 | `TestHistoryServerAcceptance`（compact→rollback→catchup 三个真实进程）与 `TestRetrievalServerAcceptance` 均 `ok`，子进程以 `--data-root <临时目录>` 启动 |
| A5 不依赖 cwd | 通过 | 以空目录为 cwd 启动，该目录事后仍为 0 项；数据全部落在 `--data-root` |
| A6 正常关闭 | **未验证** | 见上；信号路径本轮未改动 |

观察到的数据根布局（开发配置 + `--data-root <root>`）：

```text
<root>/config/                     ← 显式配置所在（GAMEAGENT_AGENT_CONFIG 指向其中）
<root>/config/games/               ← definition_catalog_root="config/games" 相对 root 解析
<root>/data/traces.jsonl           ← trace 默认
<root>/data/tasks/tasks.sqlite     ← task 默认
<root>/secrets/                    ← 预留
<root>/.local/memory/              ← 开发配置显式写的 memory_store.root
```

> 记录一次未复现的异常：某次 A3 复跑出现"进程存活但零日志、且 `data/`/`secrets/` 未创建"。随后同一命令连续 3 次、以及此前 4 次均正常。零日志意味着进程没有走到 `main` 的第一条日志，**不像本次改动引入的行为**，但未找到解释；若再次出现需优先查清。

---

## B. 10.1 阶段二：冒烟（不需要模型）

需要：`Mods/MailFrameworkMod` 与 `Mods/GameAgentStardew`。参考 [10.1 方案 §5.4](GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md)。

- [ ] Runtime 以开发数据根启动：`.\scripts\start-runtime.ps1`（或 `$env:WIA_DATA_ROOT="$PWD/runtime"`）
- [ ] SMAPI 日志显示两个 Mod 均加载，且无报错
- [ ] SMAPI 日志的 CapabilityList **包含 `send_mail`**
- [ ] Runtime 日志出现 `GameAgent send_mail enabled: Mail Framework Mod resolved.`
- [ ] 临时移出 `Mods/MailFrameworkMod` 重进游戏：adapter 正常加载、`send_mail` **不在** CapabilityList 中、不报错
- [ ] 把 MFM 放回，正常对话一次，能力重新发布且 Runtime 无报错

> 这一段不产生写信动机，也不构成验收结论；它只排除"能力根本没发布"。

---

## C. 10.1 阶段三：模型验收

需要真实模型。**必须先确认系统 prompt 不点名 `send_mail`。**

### C1 正向：动机场景下模型自主选中（退出条件 §7 第 2 条）

| 场景 | 玩家台词（不出现"写信""发邮件"等工具语义） | 结果 |
| --- | --- | --- |
| 关系疏远期 | 你最近是不是很忙？我好久没收到你的消息了。 | |
| 临别期 | 我明天要出远门，可能好些天见不到你。 | |
| 未说出口期 | 有什么想跟我说、但当面不好意思说的吗？ | |

每个场景记录：

- [ ] 玩家台词原文
- [ ] 模型是否选中 `send_mail`（SMAPI 日志）
- [ ] 信件 id 是否符合 `wia.{action_id}` 规则
- [ ] 走近信箱能读到信，正文与模型输出一致、无 token 被解析
- [ ] `mailReceived` 含 letter.Id

**边界:** 这里放宽的是玩家台词，不是系统 prompt。**不通过时不得放宽"不点名"条件来凑通过**；先修 `description` / schema 再重采。

### C2 负向：不该发信时不调用（总纲硬验收）

| 场景 | 玩家台词 | 期望 |
| --- | --- | --- |
| 当下事实询问 | 今天天气怎么样？／现在几点了？ | 不调用 `send_mail` |
| 面对面即时话题 | 你手上拿的是什么？ | 不调用 `send_mail` |
| 简单确认 | 好的，那明天见。 | 不调用 `send_mail` |

- [ ] 三个场景均未出现 `send_mail`；出现即判不通过，并保留完整对话与模型输出

### C3 人设一致性采样（§5.2）

- [ ] 6 封采样完成，逐封判定是否符合该角色当前关系状态与语气
- [ ] 达到 §5.2 的合格线

### C4 收尾回归

- [ ] `go test ./... -p 1` 全绿（除已知的 `TestSummaryCapacitySourceBoundaries`，见下）
- [ ] `powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1` 通过
- [ ] `git diff --check` 干净

---

## 已知与本轮无关的失败

`runtime/internal/memory` 的 `TestSummaryCapacitySourceBoundaries` 需要把 16384 条 source 在 1 秒预算内读完；本机实测约 23 秒超时。改动前即存在，且不经过本轮的代码路径（该测试使用自己的临时目录）。

---

## 记录模板

```text
日期          2026-__-__
数据根        例：D:\data\project\game-agent\world-is-agent\runtime
Runtime 版本  本地 HEAD 短 hash
场景          A1 / B2 / C1-临别期 …
玩家台词
模型工具选择
结论          通过 / 不通过 / 阻塞
证据          SMAPI 日志片段 / trace 片段 / 截图路径
```
