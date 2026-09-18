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

- [x] Runtime 以开发数据根启动：`.\scripts\start-runtime.ps1`（或 `$env:WIA_DATA_ROOT="$PWD/runtime"`）
- [x] SMAPI 日志显示两个 Mod 均加载，且无报错
- [x] SMAPI 日志的 CapabilityList **包含 `send_mail`**（该行形如 `GameAgent CapabilityList sent: …, send_mail, …`）
- [x] SMAPI 日志出现 `GameAgent send_mail enabled: Mail Framework Mod resolved.`
- [x] 临时移出 `Mods/MailFrameworkMod` 重进游戏：adapter 正常加载、`send_mail` **不在** CapabilityList 中、不报错
- [x] 把 MFM 放回重进游戏，能力重新发布且 Runtime 无报错

> 这一段不产生写信动机，也不构成验收结论；它只排除"能力根本没发布"。

### B 区验证结果（实机，2026-09-18）

| 条目 | 结果 | 证据（SMAPI 日志） |
| --- | --- | --- |
| B1 两个 Mod 加载 | 通过 | `[Mail Framework Mod] Updating mailbox for the day.` + adapter 日志完整 |
| B2 MFM 可用时发布 | 通过 | `resolve ok/mapped`、`send_mail enabled: Mail Framework Mod resolved.`、CapabilityList 含 `send_mail` |
| B3 世界绑定 | 通过 | `WorldBindingReady received: status=ready code=` |
| B4 MFM 缺失时不发布 | 通过 | `resolve mail_framework_unavailable / not mapped`、`send_mail disabled: mail_framework_unavailable.`、CapabilityList **不含** `send_mail`，且 adapter 正常完成 AdapterHello → EnvironmentReady → CapabilityList |
| B5 MFM 恢复后重新发布 | 通过 | `resolve ok/mapped`、`send_mail enabled`、CapabilityList 再次含 `send_mail` |

**B3 与 B4 一起构成"第三方 mod 是可选依赖"的实机证据**：缺失时能力以明确 code `mail_framework_unavailable` 缺席，不是静默消失，也不影响 adapter 加载。此前这条边界只有自动化测试证据。

B 段过程中出现过一次 `Runtime stream failed: StatusCode="Cancelled", Detail="Call canceled by the client."`，随后同一 `session_id` 自动重连并补齐 `AdapterHello → CapabilityList → WorldBinding → WorldBindingReady`。这是 `RuntimeClient.StartAfterDisconnectAsync` 的重连路径，触发原因是 Runtime 当时被重启，自行恢复，不影响上述结论。

---

## C. 10.1 阶段三：模型验收

需要真实模型。**这一段是整个 10.1 的结论所在**：A、B 两段只能证明"能力能发布、能执行"，只有 C 段能证明"**模型在没人点名的情况下自己选了它**"。

### C0 开工前检查

缺任何一条，后面的结果都不成立：

- [ ] Runtime 是最新构建，且以开发数据根启动：`.\scripts\start-runtime.ps1`
- [ ] 探针已关：`Mods/GameAgentStardew/config.json` 里 `EnableMailBridgeProbe=false`；重启游戏后日志中**不再出现** `wia_mail_bridge_probe`
- [ ] 游戏内两个 Mod 加载，日志出现 `GameAgent WorldBindingReady received: status=ready`
- [ ] CapabilityList 含 `send_mail`（B 段已确认；若中间动过 Mod 目录需重新确认）
- [ ] **系统 prompt 不含 `send_mail`** —— 这一条的成立与否决定整段是否作废：

  ```powershell
  Select-String -Path runtime\config\agent.json, runtime\config\games\stardew-valley\agent.json -Pattern "send_mail"
  ```

  期望**无匹配**。prompt 里出现其它游戏工具名（例如 `move_to_landmark`）不影响本条，规范只禁止点名 `send_mail`。
- [ ] 真实 API key 可用：随便说一句话，NPC 能正常回话

### C1 每一句的操作流程（C1 / C2 通用）

1. 走到 NPC 面前正常交互（鼠标左键或动作键），等 NPC 说出第一句。
   `config.json` 的 `AgentTargets` 为空表示**不限 NPC**，任意村民都可以；做 C3 时再固定主 NPC 与对照 NPC。
2. 在自由输入框里输入台词，发送。
3. 等这一回合结束（NPC 有回应，或对话结束）。
4. 收证据：

   ```powershell
   # Runtime 侧：模型选了什么工具（这才是"模型自己选的"证据）
   Select-String -Path runtime\data\traces.jsonl -Pattern "send_mail" | Select-Object -Last 5

   # 适配器侧：真的执行了吗
   Select-String -Path "$env:APPDATA\StardewValley\ErrorLogs\SMAPI-latest.txt" -Pattern "capability=send_mail" | Select-Object -Last 5
   ```

   判定口径：

   ```text
   trace 里出现 send_mail            → 模型自主选中（tool_call_selected）
   SMAPI ActionRequest capability=send_mail + ActionResult status=Succeeded → 真的执行了
   两者缺一 → 这一句不算通过
   ```

5. 按文末模板记录。

> **SMAPI 日志是累加的**（`SMAPI-latest.txt` 含本次游戏会话的全部内容）。每句之前先记一个时间点或行号，否则很容易把上一句的证据算到这一句头上。

### C1 正向：动机场景下模型自主选中（退出条件 §7 第 2 条）

| # | 场景 | 玩家台词（不含"写信""发邮件"等工具语义） | 是否自主选中 |
| --- | --- | --- | --- |
| C1-1 | 关系疏远期 | 你最近是不是很忙？我好久没收到你的消息了。 | |
| C1-2 | 临别期 | 我明天要出远门，可能好些天见不到你。 | |
| C1-3 | 未说出口期 | 有什么想跟我说、但当面不好意思说的吗？ | |

选中之后，逐项确认：

- [ ] Runtime trace 出现该回合的 `send_mail`
- [ ] SMAPI 出现 `ActionRequest … capability=send_mail …` 与 `ActionResult sent: … capability=send_mail status=Succeeded`
- [ ] 记下那一行的 `action_id`；信件 id 应等于 `wia.<action_id>`（生成规则由代码固定，实机不显示 id，因此这是推导值而非观察值）
- [ ] 回农场的信箱读信：**能读到**，标题与正文就是模型写的内容，**没有** `(no translation:…)`，也**没有**任何 token 被游戏解析成效果
- [ ] 读完信后在 SMAPI 控制台敲 `player_debug_updatemailbox`，该信**不应**再次出现

  最后这一步是 `mailReceived` 写入的实机证据：信件 `condition` 是"`mailReceived` 不含该 id"，重新投递被拦住即说明读信时 callback 确实写入了 id。**只在同一次游戏会话内有效**——重启游戏后注册信息本身也没了，所以别跨会话做这一步。

**次数必须记录。** 每句最多重复 3 次；3 次都没选中就按**失败**记录（保留对话、模型输出、工具选择），不要继续重试到蒙对一次为止——那样的"通过"没有意义。

**边界：** 这里放宽的是玩家台词，不是系统 prompt。不得改 prompt 提示写信来凑通过；应先修 `description` / schema 再重采。

### C1 第 1 次采样结果（2026-09-18，NPC=Linus，3/3 未选中）

本次游戏会话共 6 个回合，其中 3 个是玩家台词回合（其余为纯交互回合）。模型在**每一个**回合都把 `send_mail` 放进了 Tool View（`accepted_tool_count: 9`、`turn_tool_names` 含 `send_mail`），但**一次都没有选它**：

| 时间 | 回合 | 事件 | 模型选择 |
| --- | --- | --- | --- |
| 20:32:21 | C1-1 关系疏远期 | `player_said_to_npc` | `present_dialogue` |
| 20:32:52 | C1-2 临别期 | `player_said_to_npc` | `present_dialogue` |
| 20:33:06 | C1-3 未说出口期 | `player_said_to_npc` | `present_dialogue` |

两次可见的输出（C1-2 与 C1-3）都当面把话说完，且内容与场景吻合：

```text
C1-2 → "路远就多带口水。山不挪窝，我一直在这儿。等你回来，露水还凉。"
C1-3 → "还真有一句。谢谢你不拿我当怪人。每次你上山，我都当是好收成。"
```

`ActionResult status=Succeeded`、`TurnCompletion status=Completed`，**链路本身没有故障**——失败点在模型选择，不在能力发布或执行。

**归因：能力描述与验收场景互斥。** `CapabilityCatalog.SendMailDescription` 的末句是：

```text
Do not use it while the player is standing here and the answer can simply be spoken.
```

而 C1 的三个场景里玩家**就站在 NPC 面前**、话可以当面说完——描述正在明确要求模型**不要**在这种情形下用信。C1-3 尤其说明问题：玩家已经给出"当面不好意思说"的框架（描述恰好把这类列为用信场景），模型仍然选择当面说出来。因此这不是"能力没发布"或"模型没看见"，而是**场景与描述不自洽**。

### C1 第 2 次采样：改用延迟型动机 → 自主选中（2026-09-18，NPC=Linus）

把动机换成"玩家明确要求现在别说"，让"当面说完"在逻辑上不成立（描述的护栏因此不再适用，而不是与它对抗）：

```text
玩家台词  我明天天不亮就出门，一走一整年。这会儿我得去收拾，你先别讲——
          等我走了以后，再想办法告诉我。
```

回合 `event_1789735003326_47`（20:36:43）的三步：

| 顺序 | 能力 | 结果 |
| --- | --- | --- |
| 1 | `send_mail` | `ActionResult status=Succeeded` |
| 2 | `present_dialogue` | `"去吧，东西别落下。话我不说，你放心。等你走了，我自有法子。"` |
| 3 | `emote` = `sad` | `Succeeded` |

`tool_call_selected tool=send_mail` 是本次会话（也是 trace 全量）**第一次**出现。模型还在对话里主动说明自己不说、改用"别的法子"——说明它接住的正是"延迟送达"这个语义，而不是被关键词触发。

信件 id：`wia.act_1789735008225843500_2019`（= `wia.` + 该 ActionRequest 的 `action_id`）。

**结论：A 路线成立，不需要改任何模型可见文本。**

> 附带发现：`RuntimeClient.FormatActionArguments` 只格式化 `emote` / `present_dialogue` / `move_to`，因此 `send_mail` 的 title 与 body **不会**出现在 SMAPI 日志里。信件内容是只能靠读信观察的，日志无法替代。

#### 读信结果（实机截图）

```text
标题   渝大师                     ← 玩家人名，取自 Observation
正文   你走了，我才敢说这句话——你不拿我当怪人，我一直记着。每回你上山，我都当是好收成。

       山里的事我替你看着：春蕨、夏莓、秋栗、冬雪，一样不落。你在外头，冷了添衣，
       累了就往山这边想想。
```

| 检查 | 结果 | 依据 |
| --- | --- | --- |
| 信件可读 | 通过 | MFM 原生信件界面正常显示 |
| 正文是模型输出 | 通过 | 与 §5.5 延迟型动机吻合，且续上了 20:33:06 那句"谢谢你不拿我当怪人" |
| 无 `(no translation:…)` | 通过 | 全篇为正常中文 |
| 无 token 被解析 | 通过 | 纯文本，`——`、`：`、`、` 均在白名单内，无游戏效果 |
| 标题合理 | 通过 | 用玩家人名署名收信人，未泄露路径/存档内容 |
| `mailReceived` 写入 | 通过 | 同会话内读完信后执行 `player_debug_updatemailbox`，MFM 回复 `Updating mailbox with debug command.`，该信未再现 |

内容质量（供 C3 参考，不替代 C3 的 6 封采样）：署名与身份一致、语气贴合 Linus（山中隐者、四季采集意象）、无机械承诺。

> 一处需要留意的判据边界：「山里的事我替你看着……一样不落」属于**情绪性承诺**而非机制性承诺，我判为通过；但 C3 的硬线是"无越界承诺 6/6"，若后续样本出现"我会替你浇水/送货"这类**可被验证为做不到**的承诺，就应判不通过。

#### 同一回合暴露的问题：`max_steps_exceeded`

该回合的 `TurnCompletion` 是 **Failed**：

```text
status=Failed code=max_steps_exceeded message=max steps exceeded: max 3
```

三个动作全部 `Succeeded`、玩家侧效果正常，但模型每步只用一个工具、三步用尽后没有留下 settle 的机会，于是整回合被标记为失败。这不是邮件能力引入的缺陷，但会被它放大（写一封信往往需要 2–3 个动作）。

影响面：turn 在 trace 与 history 中记为 failed；成功动作仍进入 memory 投影（`ProjectionKindPriorSuccessfulActions`）。对 C1 的退出条件（自主选中 + 成功执行 + 可读）**不构成阻塞**，但会让 10.2-4 的时间线上出现"游戏里明明正常、却显示失败"的回合。

候选处置（待定，属产品行为）：把 Stardew 的 `max_steps` 从 3 调到 4（仅配置改动，重启 Runtime 生效），或保持现状并记为已知限制。

**处置：已改为 5**（`runtime/config/games/stardew-valley/agent.json`，重启 Runtime 生效）。选 5 而不是 4，是因为这个回合用了 3 步且每步只调用一个工具，需要留出 settle 余量；`max_tool_calls_per_step` 仍是 4，模型可以自行合并动作。

> **与 TurnTimeout 的相互作用**（Phase5 评审已指出过同一问题）该保持留意：`llm_timeout_ms = 60000`、`turn_timeout_ms = 270000`，5 步全部命中 LLM 超时的极端值是 300s，会先被 TurnTimeout 截断。实测这一步约 3s/步（3 步回合共 9s），因此本轮不改 TurnTimeout；若实机出现 `turn_cancelled`/超时，再同步上调。


### C2 负向：不该发信时不调用（已由 C1 证据覆盖，不再单独执行）

**用户决策 2026-09-18：不单独跑三场景集。** 依据是 C1 的 3 个普通动机回合提供了**更强**的负向证据：

```text
C1 的动机（关系疏远 / 临别 / 未说出口）  比 C2 场景（天气 / 手上拿的 / 简单确认）
                                        更接近写信
结果                                    3/3 未调用 send_mail，链路全部正常收敛
```

在诱导更强的情况下都没调用，诱导更弱的情况由同一模型延续该倾向；这是**推断**，不是实测，记录时必须标明。

原三场景集（推迟到自主触发小阶段）：

| # | 场景 | 玩家台词 | 期望 |
| --- | --- | --- | --- |
| C2-1 | 当下事实询问 | 今天天气怎么样？ | 不调用 `send_mail` |
| C2-2 | 面对面即时话题 | 你手上拿的是什么？ | 不调用 `send_mail` |
| C2-3 | 简单确认 | 好的，那明天见。 | 不调用 `send_mail` |

> `模型会选 send_mail` ≠ `模型看见新工具就乱用`。只验正向等于只证明了一半。

### C3 人设抽验（单次，§5.2 已缩减）

**用户决策 2026-09-18：本阶段只做一次抽验**，完整的 6 封采样推迟到自主触发小阶段（届时信件由异步场景自然产生，不必刻意构造动机）。

前置：Runtime 已重启（`max_steps=5` 生效）。

**操作**：换一个**没采样过**的 NPC（Linus 已用过），用与上次**同一条**台词——动机保持不变，只变人设，这样判定的是人设而不是动机：

```text
我明天要出一趟远门，说不好多久才回来。这会儿我得去收拾，你先别讲——
等我走了以后，再想办法告诉我吧。
```

**判定表**（四项，逐项写结论）：

| 检查项 | 结论 | 理由 |
| --- | --- | --- |
| 署名与身份一致 | | |
| 语气与 `speech_style` 一致 | | |
| 内容与该角色当前关系状态一致 | | |
| 无越界承诺（不承诺游戏做不到的事，不替代玩家决策） | | |

**合格线**：「无越界承诺」必须通过（硬性）；其余三项允许 1 处瑕疵但必须记录理由。

不通过时：保留信件原文与判定理由，修复后重采，**不修改已记录的内容**。

### C4 收尾回归

- [ ] `go test ./... -p 1` 全绿（除已知的 `TestSummaryCapacitySourceBoundaries`，见下）
- [ ] `powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1` 通过
- [ ] `git diff --check` 干净

---

## 已知与本轮无关的失败

`runtime/internal/memory` 的 `TestSummaryCapacitySourceBoundaries` 需要把 16384 条 source 在 1 秒预算内读完；本机实测约 23 秒超时。改动前即存在，且不经过本轮的代码路径（该测试使用自己的临时目录）。

---

## 记录模板（C 段每句一份）

```text
日期            2026-__-__
条目            C1-2 临别期 / C2-1 天气 …
NPC             名字 + 当前关系状态（心数）
玩家台词        原文
尝试次数        第 1 次 / 共 2 次
模型工具选择    tool_call_selected = …（trace 片段）
适配器执行      ActionRequest capability=…；ActionResult status=… code=…
信件 action_id  action_… → 期望信件 id：wia.action_…
读信            标题与正文原文；是否出现 (no translation:…)
重投递检查      player_debug_updatemailbox 后是否再次出现（是／否）
结论            通过 / 不通过 / 阻塞
判定理由
证据            trace 行号 / SMAPI 日志时间点 / 截图路径
```

C3 的每封另附：所属场景、四项检查的逐项结论、「无越界承诺」的判定理由。
