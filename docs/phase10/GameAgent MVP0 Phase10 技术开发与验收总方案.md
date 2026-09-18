# GameAgent MVP0 Phase10 技术开发与验收总方案

> **Status:** 10.1 Mod 邮件能力已验收（缩减口径，见 [检查表](GameAgent%20MVP0%20Phase10%20验收检查表.md)）；10.2-1 Runtime Bootstrap & Data Root 已实现（§3.2.2）；10.2-2 本地控制面首个切片已实现并实机验证（[10.2-2 方案](GameAgent%20MVP0%20Phase10.2-2%20本地控制面技术开发方案.md)）；10.2-3 决策已冻结（[10.2-3 方案](GameAgent%20MVP0%20Phase10.2-3%20首次运行配置技术开发方案.md)）；10.2-4、发行步骤与 10.3 未开工
> **Date:** 2026-09-18
> **Phase:** Phase10 Ecosystem & Productization（生态接入、产品化与跨游戏验证）
> **目标:** 证明 WIA 的能力边界可以向外扩展——第三方 mod 能力可被 agent 自主调用、系统可以被外部用户装起来用、Adapter 架构可以被第二个真实游戏复用
> **Code Inspection Baseline:** `main` @ `ef50436`；Phase A 与 Mod 邮件能力接入已授权
> **技术栈:** Go、SQLite、gRPC / Protobuf、C#、SMAPI、Vue 3 + TypeScript + Vite（构建期）、Go `net/http` 与 `//go:embed`（本地控制面与 UI 分发）
> **Roadmap:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md)
> **Architecture:** [Runtime 整体架构设计规范](../summary/GameAgent%20Runtime%20整体架构设计规范.md)
> **前置:** [Mod 能力接入规范](../development/mod-capability-integration.md)、[Phase10.1 邮件能力方案](GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md)、[Phase A 逻辑分离](../development/logical-separation.md)

---

## 1. 阶段定位与目标

### 1.1 一句话目标

MVP0 已经证明了"单一游戏 + 自有能力 + 本地开发"的闭环。Phase10 要证明这套架构**能往外长**：

```text
10.1  Mod 生态     能力来自第三方 mod，agent 能自主选择并执行
10.2  产品化       外部用户能装起来用，并看见它怎么工作
10.3  跨游戏       同一 Runtime 能承载第二个真实游戏
```

三条线索共同回答一个问题：

> **WIA 是"一个能跑 demo 的项目"，还是"一个可以长大的 Runtime + Adapter 生态"？**

### 1.2 与"MVP0 已定型"的关系

`AGENTS.md` 的版本定位是"MVP0 定型、完成开源产品化"。Phase10 是在该定位下的**第一个扩展阶段**，边界是：

- 继续坚持不做完全自主 agent（无自主目标生成、长时程自主规划、多 Agent 协作）。
- **协议保持 additive**。10.1 与 10.3 都可能需要协议扩展，但只做增量；破坏性变更必须单独授权。
- Runtime Core 继续 game-agnostic。10.3 的价值就是检验这条边界是否真的成立。
- Mod 运行时标识（`UniqueID` / `EntryDll` / `AssemblyName`）不在本阶段改动范围。
- **平台范围：只服务同一台电脑上的本地浏览器**。不做 Mobile、不做 LAN、不做远程访问。手机形态留给未来的独立项目；WIA 不为其预埋 LAN / server 模式。

### 1.3 阶段总览

| 子阶段 | 主题 | 一句话验证目标 |
| --- | --- | --- |
| 10.1 | Mod 能力接入与自治调用验证 | 能力来自第三方 mod 时，模型能自主选择、正确执行、失败可收敛 |
| 10.2 | 客户端与产品化 | 一个外部用户能在不读源码的前提下装起来、跑起来、看见 agent 在做什么 |
| 10.3 | 第二款真实游戏接入 | 同一 Runtime 不改核心即可承载第二个 Adapter，Adapter 接入有可复用的检查表 |

---

## 2. 阶段 A：Mod 能力接入与自治调用验证（10.1）

### 2.1 目标

验证命题：

> **在一个已经由玩家或游戏事件触发的 AgentTurn 内，系统 prompt 不点名任何具体能力，模型根据当前意图自行从 Tool View 中选择由第三方 mod 提供的能力，并成功执行。**

要点在于"自主"的**范围**：它指 Turn 内的 **Tool Selection**，不是 NPC 自己产生目标。例如玩家说"之后给我留封信告诉我结果"，模型自己从 `present_dialogue`、`send_mail` 等工具中选出 `send_mail`——这已经足以证明 Capability 自主选择，**不需要引入 Background Trigger 或 Autonomous Goal**（那属于本版本明确不做的范围）。

首个接入对象已确定：**MailFrameworkMod（MFM 1.20.0，邮件能力）**。详细方案见 [Phase10.1 Mod 邮件能力技术开发方案](GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md)，本阶段遵守 [Mod 能力接入规范](../development/mod-capability-integration.md) 的全部硬边界。

### 2.2 覆盖的能力类型

本阶段以**写类能力**为主，先证明一条最小闭环：

```text
写类    让 agent 触发 mod 的动作（首个且必做：send_mail）
判定类  能力不适用时模型不调用（证据由普通动机回合提供，见 10.1 §5.6）
读类    把 mod 的状态变成 Observation 事实（可选 / follow-up）
```

```text
MFM → send_mail → Capability → AgentTurn → ToolCall → 真实信件
```

读类**不作为硬验收**：为凑"读/写/判定三类齐全"而额外引入一个 mod，会把最小闭环扩成"再找一个 mod → 研究它的 API → 接 Observation → 验证模型使用"。如果 MFM 本身已有低成本且有价值的可读状态，可顺带验证；否则留作 follow-up。

### 2.3 范围

- `send_mail(title?, body)`：仅文本、无附件、无 recipe、无 AutoOpen，立即投递。
- Adapter 侧**白名单**输入校验（信件正文进入游戏 `TokenParser`，文本即命令通道）。
- MFM 通过 SMAPI `GetApi` 作为**可选依赖**接入；未安装时 adapter 正常加载，且 `send_mail` 不进入 Tool View。
- 模型自主调用的证据链：能力进入 Tool View → 模型主动产生 ToolCall → 真实执行 → 结果回灌 → 影响下一步决策。
- 读类能力接入为可选 follow-up，不作为本阶段硬验收（理由见 §2.2）。

### 2.4 明确不做

```text
发物品 / recipe 学习 / AutoOpen       附件会把"送信"变成"凭空发物品"，需单独授权
回信 / ReplyConfig                    对话闭环已由 present_dialogue 覆盖
content pack 写入用户 Mods 目录        修改用户环境，超出能力语义
玩家确认字段                          当前没有确认交互机制，字段没有执行点
批量接入 N 个 mod                      先证明机制，再谈规模
```

### 2.5 退出条件

1. MFM 可用时 `send_mail` 进入 Tool View；MFM 未安装时该能力不发布且 adapter 正常加载。
2. 在一个由玩家或游戏事件触发的 AgentTurn 内，系统 prompt 不点名 `send_mail`，模型自行从 Tool View 选中并调用它（有 SMAPI 日志证据）。动机由玩家台词制造，见 [10.1 方案](GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md) §5.5。
3. 信件成功投递，玩家可读，`mailReceived` 记录 letter.Id。
4. 白名单文本校验有自动化测试覆盖，超范围字符与注入样本被拒。
5. 负向结论成立：普通动机回合中模型未调用 `send_mail`（证据见 [10.1 方案](GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md) §5.6）。
6. [mod-capability-integration.md](../development/mod-capability-integration.md) §7 检查清单全部满足。
7. （可选）读类能力接入并进入 Observation。

不要求任何形式的 Background Trigger、Autonomous Goal 或 NPC 自发任务。

### 2.6 已评估但不纳入 MVP0：延迟邮件

本阶段只验证"回合内工具选择"，因此 **agent 不会主动起意写第一封信**。这是已知且有意接受的范围边界：Runtime 今天只有两条开门路径。

```text
Loop.HandleEvent      由 adapter GameEvent 驱动；adapter 目前只发玩家交互类事件
Loop.HandleTaskWake   由 Runtime 内的任务调度器按游戏时钟驱动，只对已存在的任务生效
```

#### 2.6.1 durable task + 唤醒已由 Phase9 实机验证，不需要用邮件重复证明

结论：**不要把这条描述成"为 WIA 建立 durable/deferred 实机证据"——该主张在 Phase9 已经成立，且场景比邮件更强。** 见 [Phase9 开发与验收记录](../phase09/GameAgent%20MVP0%20Phase9%20开发与验收记录.md) §9.1 的两条真实游戏闭环：

```text
met      task_1789450222152318800_2038  07:00 出发 → 08:40 到达 → 08:50 登记等待 → 11:00 met
expired  task_1789453595087640500_2070  07:00 出发 → 07:40 到达 → 07:50 登记等待 → 09:00 expired
         记录原文：「玩家全程未靠近，NPC 自行赴约并按时收口。」
```

两条都跑在真实 Stardew、真实 Runtime、真实 SQLite 与真实模型上：玩家交互产生任务后，在无后续玩家交互的情况下，Runtime 于未来游戏时间自行唤醒 Agent，完成跨地图行动并进入确定性终态。

#### 2.6.2 延迟邮件：实现过，评估后撤回

延迟邮件（Adapter 的 `TaskProposal` → `create_task` → durable task → 唤醒回合由模型重新写信）曾被完整实现并测试，结论是**撤回**：它不是设计问题，而是收益太窄。相比 `send_mail` 的"现在生成、现在投递"，它唯一新增的价值是"在唤醒时刻结合当时的上下文重新生成内容"。

而 MVP0 既没有自然产生这类需求的后台触发（§2.1 明确不做 Background Trigger），主要入口仍是玩家面对面交互，公开 Demo 也不需要它。为这点收益增加一个 model-visible capability、一套 Adapter 侧特殊闸门与一组行为边界，与 [AGENTS.md](../../AGENTS.md) 的"能力暴露什么，模型就会尝试什么"相冲突。

MVP0 保持：

```text
send_mail 语义不变          现在把这封信投出去
不引入 Background Trigger    适配器自行开回合属于产品能力扩张，不只是机制问题
不新增 mail-specific durable task capability
```

撤回发生在同一交付周期内，没有形成历史验收价值，因此不留实现或评估文档。唯一保留的产物与邮件无关：`GameClock.ToAbsoluteDay` 及对应测试。

#### 2.6.3 仍然成立的经验观测

**10.1 验收的实机观测：** 普通对话里模型不会写信——3 次"关系疏远 / 临别 / 未说出口"动机采样全部未选中；换成玩家明确要求"现在别说、之后再告诉我"的延迟动机后，1 次即选中。这不是模型或描述的问题，而是**阶段边界的结构性结果**：本阶段的回合只在玩家与 NPC 面对面时产生，而描述禁止在"话可以当面说完"时用信；两个条件同时成立时，信只有在玩家主动要求延迟时才成为唯一合理选择。

因此"写信频率过低"不能靠放宽描述解决（那会打穿负向用例），也不值得为此打开新的触发治理体系。本版本接受这个频率：`send_mail` 已经被实机验证可用，它的发现路径是玩家自然提出"以后/等我走后告诉我"。

---

## 3. 阶段 B：客户端与产品化（10.2）

### 3.1 目标

验证命题：

> **一个只有游戏和 Runtime 的普通用户，能在不读源码、不改配置文件的前提下把 WIA 跑起来，并看见 agent 在做什么。**

当前配置面仍然是手工的：配置写在数据根下的 `config/`，`GAMEAGENT_MODEL_CONFIG` / `GAMEAGENT_AGENT_CONFIG` 可以覆盖。10.2-1 已经让 Runtime 不再依赖 cwd，也不再因为缺少模型配置而直接退出；10.2-2 已经让进程在启动时提供本地控制面并自动打开浏览器。剩下的问题是"用户怎么把它配起来"——控制面目前只能**显示**缺什么，不能写入配置。

### 3.2 关键前置：Runtime Bootstrap 与统一 Data Root

10.2-1 开工前核实的三件事（现已处理，保留作为问题定义）：

```text
启动顺序     main 先加载 model provider，失败即 log.Fatalf，此时还没有任何 server
             → 一个什么都没配置的新用户，会在 Web UI 出现之前直接退出
凭证来源     API Key 只接受 "env:VAR" 形式，不接受直接写入的 key
路径         trace 硬编码 "runtime/.local/traces.jsonl"，依赖从仓库根目录启动
             gRPC 端口硬编码 127.0.0.1:50051
```

因此这里有**两个**必须解决的入口问题，不是一个：

**① 控制面必须先于模型就绪。** Runtime 必须能在"尚未配置模型"的状态下启动本地 HTTP 面，让首次运行向导完成配置与密钥写入，之后 Agent Core 才进入 Ready。把模型加载失败当成致命错误，等于让向导永远没有机会出现。

```text
Bootstrap / Control Plane
        ↓  即使尚未配置 Model 也能启动 HTTP UI
用户完成配置与 Secret
        ↓
Runtime Agent Core 进入 Ready
```

**② 所有路径必须从统一的 App/Data Root 解析。** 需要脱离 cwd 的不止 trace：

```text
model.json / agent.json / trace / memory SQLite / task SQLite / definition root / 未来的 secrets
```

先定义 App/Data Root（含默认位置与可覆盖方式），再让上述每一项都从它解析。逐项打补丁会在 10.2 的后续工作中反复返工。

**下载一个二进制、在任意目录启动，当前会写错位置或直接失败。** 这两条是 10.2 的入场券，不是可选项。

> 本子阶段的第一项工作从"Runtime 脱离 cwd"改名为 **Runtime Bootstrap & Data Root**——它同时覆盖启动顺序与路径解析，原名只说了后一半。

#### 3.2.1 Data Root 决策记录（已冻结）

```text
解析优先级    --data-root   >   WIA_DATA_ROOT   >   平台默认目录

平台默认
Windows      %LOCALAPPDATA%\WorldIsAgent
macOS        ~/Library/Application Support/WorldIsAgent
Linux        $XDG_DATA_HOME/wia，未设置则 ~/.local/share/wia

统一一个 root，不拆 config/data 双 root
<root>/config/       model.json、agent.json
<root>/data/         traces.jsonl、memory/、tasks/tasks.sqlite
<root>/secrets/      预留（10.2-3）
```

路径解析规则——这条决定了"已有显式配置仍然生效"的边界：

```text
绝对路径     原样使用
相对路径     相对 Data Root 解析
未配置       使用 Data Root 下的默认值
```

即 Data Root 是**默认值与相对路径的基准**，不是"把已有绝对路径也强制搬过去"，也不是删掉既有的路径配置能力。

明确不做：exe 同级作为默认 root；`wia.portable` 标记文件；config/data 双 root；把 secret 的 OS 密钥库实现纳入本轮。

两个选择依据：

1. **用 `%LOCALAPPDATA%` 而不是 `%APPDATA%`。** SQLite 与 trace 放进漫游配置文件是反模式。
2. **exe 同级不是好默认值。** `go run`、开发脚本、自动化验收三个场景都会因此依赖 override——一个需要被处处绕开的默认值没有价值。Portable Release 的含义是"不需要 Installer"，不是"状态必须和 exe 放在一起"。

代价（明确接受）：用户不容易自己找到这个目录。这项代价由 Web UI 承担——配置入口本来就是 UI，后续补一个 `Open Data Directory` 即可。

#### 3.2.2 10.2-1 实现结果

```text
Root Resolver        --data-root > WIA_DATA_ROOT > 平台默认目录
路径规则             绝对路径原样使用；相对路径相对 root；未配置用 root 默认值
Runtime-owned path   config / trace / task db / memory root / definition root 全部从 root 解析
启动生命周期         Bootstrap 与 Agent Core 分离：未配置模型时不再 Fatal，
                     进程继续提供 gRPC，状态为 needs_configuration 并带上期望的配置路径
显式覆盖              GAMEAGENT_MODEL_CONFIG / GAMEAGENT_AGENT_CONFIG 仍然生效，
                     其相对值同样相对 root 解析
验收测试             acceptance 以 --data-root 指向自己的临时目录
```

仍然属于后续子阶段：首次运行向导、secret 存储与权限收紧、可视化。**模型配置目前仍需手工写入数据根下的 `config/model.json`**；HTTP 面已由 10.2-2 提供。

### 3.3 技术选型与优先级（已确认）

```text
主入口    本地 Web UI：Vue 3 + TypeScript + Vite 构建，产物用 //go:embed 打进 runtime 二进制
分发      单个二进制同时提供 gRPC 与 HTTP；浏览器访问 127.0.0.1 即可，无需安装
保留      CLI 作为开发与无头环境入口
不做      桌面打包（Wails / Electron）；除非将来明确需要系统托盘、原生窗口或自动更新
```

选型依据（对比桌面端后的结论）：

| 维度 | Web UI（选定） | Wails 桌面端（未选） |
| --- | --- | --- |
| 新增工作量 | HTTP 端点 + 静态资源服务 | 需把 capability 从 `package main` 抽成可导入包 + Wails 服务层 + 单例 refactor |
| 构建链 | 前端只在开发时需要 Node，Go 单命令出产物 | 每平台分别构建，还需 Wails CLI 与前端 toolchain |
| 跨平台 | 浏览器即可，零平台成本 | 平台各异；Linux 还要 webkit2gtk 依赖 |
| Windows 杀软 | 不额外引入桌面壳与桌面打包产物，少一层分发与签名复杂度 | 打包 Go 二进制存在误报，对开源项目是劝退级问题 |
| 代码签名 | 不需要 | 分发体验好就需要证书 |
| 对外展示 | README 放截图，任何平台可见 | 别人要下载运行才能看到 |
| 锁定 | Go 标准库 + Web 标准 | 绑在 Wails 抽象上（v3 已 beta） |

两条决定性的判断：

1. **WIA 当前没有 Native UI 需求。** Wails 会增加桌面构建、打包、更新与平台维护成本，而本地 Web 已完全满足当前 PC 用户目标。

   补充更正：桌面端**并不强制**要求 Runtime Core 可导入——桌面壳完全可以 spawn 独立的 `wia-runtime.exe`，两者是独立进程。因此"避免抽包重构"不是否决桌面端的理由，真正的理由是上面这条，以及下面的分发复杂度。
2. **目标用户是"装 mod 玩 Stardew 的人"。** 他们不需要安装步骤、证书和杀软白名单；打开浏览器是最低摩擦的形态。

代价（明确接受）：需要自己设计 HTTP 接口，并加本地访问保护（见 §3.4）。这是标准做法，不是需要设计的新机制。

已核实的代码事实（10.2-2 开工前）：

```text
runtime 当时没有任何 HTTP 服务        （无 ListenAndServe / ServeHTTP）
runtime 当时没有任何 embed 静态资源    （无 //go:embed）
因此 HTTP 面与资产内嵌都是净新增，不影响现有 gRPC 链路
```

10.2-2 已按此实现：HTTP 面在 `runtime/internal/httpapi`，资产内嵌在 `gameagent/console`。

本机工具链现状（已核实）：

```text
Go       1.25.3                 ✅ 后端与 embed 所需
Node     24.14.1 / npm 11.11.0  ✅ 仅前端开发时需要；终端用户不需要
WebView2 运行时 153.0.4234.32   — 选 Web UI 后不再是前置依赖
```

### 3.4 本地访问与密钥边界（必须遵守）

新增 HTTP 面是本子阶段唯一的安全相关改动。约束：

```text
绑定        只监听 127.0.0.1，不对外监听
Host 校验   校验 Host 头，防 DNS rebinding——这是本地 Web 服务最真实的攻击面
暴露范围    只暴露定位与配置所需的最小面，不代理任意请求
不做        多用户、远程访问、云端托管、端口转发支持
```

#### 3.4.1 访问凭证不能让用户手工复制

"启动时打印 token、让用户复制到浏览器"与另一条目标冲突：**普通用户不读源码、不改配置就能跑起来**。手工搬运凭证会把产品打回开发工具形态。

因此凭证必须自动交接：

```text
启动 Runtime
    ↓
生成一次性 bootstrap token
    ↓
自动打开浏览器 http://127.0.0.1:<port>/bootstrap#token=<token>
    ↓
前端用它交换 Runtime Session 凭证
    ↓
清理 URL 片段
```

用户视角只有三步：**启动 → 浏览器自动打开 → 看到 WIA**。token 是内部实现。

具体用 cookie 还是 bearer session、bootstrap token 的有效期与单次性，由 10.2 子方案决定，总纲不冻结。

#### 3.4.2 API Key 由后端独占持有

用户可以在 Web UI 里填写 API Key（这是必要的，否则"配好就能跑"不成立），但必须遵守：

```text
1  API Key 只由 Go 后端持久化与读取，不进入浏览器存储（localStorage / sessionStorage / IndexedDB）
2  任何读接口都不得回传 Key 明文，即使是本地回环
3  前端只能看到"已配置 / 未配置"与用于替换的写入入口
```

即前端看到的是：

```text
DeepSeek API Key      Configured ✓      [ Replace ]
```

而不是任何形式的 `sk-...` 回显。

#### 3.4.3 关于"静态加密"的准确认识

**加密本身不构成保护，密钥放哪里才是。** 如果加密密钥与被加密的数据在同一台机器、同一权限下（例如密钥写在配置文件、或硬编码进二进制），那么任何能读到密文的攻击者同样能读到密钥——这层加密只是让数据看起来安全。

静态加密真正有意义的形式是**绑定操作系统用户**：Windows 上用 DPAPI `/ DPAPI-NG`，macOS 上用 Keychain，Linux 上用 Secret Service 或同等机制。此时密文只能由该用户在该机器上解开。

据此确定本阶段做法：

```text
必须做   后端独占持有（§3.4.2）
必须做   secret 文件权限收紧（Windows ACL / POSIX 0600）
必须做   不回传明文、不落浏览器
建议做   用 OS 用户绑定的密钥库加密静态存储（DPAPI 等），作为纵深防御
不要做   自己发明加密方案；不要用与密文同权限存放的密钥"加密"
```

第一版的最低可接受形态是**权限受限的本地 secret 文件 + 后端独占**；OS 密钥库作为增强项，由 10.2 子方案决定是否纳入本轮。

### 3.5 范围（按依赖顺序，不是按 UI 好看程度）

| 步骤 | 内容 | 为什么在这个位置 |
| --- | --- | --- |
| 10.2-1 | Runtime Bootstrap & Data Root：Bootstrap 与 Agent Core 分离（未配置模型时进程继续提供 gRPC，状态为 `needs_configuration`）；统一 App/Data Root，配置路径、trace、SQLite、memory root、definition root 全部从它解析 | 当前未配置模型会在任何 server 之前 `log.Fatalf`，且 trace 路径硬编码依赖从仓库根启动；不做这个后面都不成立 |
| 10.2-2 | 极简本地 HTTP 面 + 资产内嵌：status / turn 列表；`//go:embed` 前端产物；**HTTP 的 bind、端口与本地访问保护** | 客户端要有东西可连；同时它就是 10.1 与 10.3 的调试面。**它是 10.2-1 控制面的载体，不是第二套 HTTP 面** |
| 10.2-3 | 首次运行向导与依赖体检：写配置与 key、检查 .NET / SMAPI / 游戏路径 / key 有效性 | 这一步做完，"外人能跑起来"才成立 |
| 10.2-4 | 可视化：AgentTurn 时间线（复用现有 JSONL trace）、对话记录、任务与记忆查看 | 这是"好看"，前三步才是"能用" |

注意 10.2-2 与 10.2-4 共用同一个 HTTP 面：**不需要为"控制面"和"UI 接口"各做一套。**

**10.2-1 已实现**（见 §3.2.1、§3.2.2）。**10.2-2 的首个切片已实现并实机验证**：控制面、凭证交接、`/api/status`、`/api/turns`、资产内嵌与自动打开浏览器；服务端不提供 `health` 路由——它没有消费者，且总方案 §3.4 要求 `/api` 全部带凭证，一个免凭证的健康检查与该约束直接冲突。**10.2-3 的决策已冻结**，见 [10.2-3 首次运行配置技术开发方案](GameAgent%20MVP0%20Phase10.2-3%20首次运行配置技术开发方案.md)。10.2-4 与发行步骤尚未开工。

### 3.6 发行形态与明确不做

**本阶段只做 Portable Release，不做 Installer**（该决策已确认，不再是开放项）：

```text
world-is-agent-v0.x-windows-amd64.zip
├── wia-runtime.exe      ← 内含 embed 的 Web UI
├── LICENSE
└── README.txt

运行：wia-runtime.exe → 自动打开浏览器
Adapter 按 Mod 形态单独安装（不进这个包）
```

理由：安装包会立刻引入注册表、卸载器、每平台打包器与代码签名，而这些都不服务于当前目标（"不读源码就能跑起来"）。免安装包 + 自动开浏览器已经满足。

```text
桌面打包（Wails / Electron）    见 §3.3；除非需要系统托盘、原生窗口或自动更新
Installer / 安装器               本阶段只做 Portable ZIP
多用户 / 远程访问 / 云端托管     见 §3.4 本地访问与密钥边界
把 runtime 抽成可导入包          仅在将来做桌面壳时才需要；当前无此需求
配置运行时热改并立即生效         先保证"配好能跑"，不做在线调参
完整可观测平台                   只暴露定位问题所需的最小面
替代游戏内 UI                    游戏内交互仍由 Adapter 与游戏负责
Mobile / LAN / 手机浏览器形态     本阶段只服务同一台电脑上的本地浏览器
```

### 3.7 退出条件

1. 全新环境（无现有配置）能在**任意目录**启动 Runtime，数据落点正确、可重复。
2. 启动 Runtime 后浏览器**自动打开**并可用，用户不需要复制 token、不需要安装 Node 或任何前端工具链。
3. 免安装包解压后可直接运行；不需要管理员权限、不需要安装步骤。
4. 首次运行向导能产出可用配置，并对缺失依赖给出可操作的明确提示。
5. 用户在游戏里触发一次交互后，能在 Web UI 看到该 Turn 的关键链路。
6. 模型 key 无效或缺失时，错误信息指向具体原因，不表现为"agent 不说话"。
7. §3.4 的约束全部生效：非 loopback 不可访问、伪造 Host 被拒、无有效凭证被拒、任何接口都不回传 Key 明文。
8. 不引入对 Runtime Core 的 game-specific 依赖。

---

## 4. 阶段 C：第二款真实游戏接入（10.3）

### 4.1 目标

验证命题：

> **同一 Runtime、同一协议，能在不修改 Runtime Core 的前提下承载第二个真实游戏。**

这是对"Runtime owns cognition / Adapter owns translation"这条长期边界的**唯一真实检验**。协议当初是按多游戏设计的，但至今只有 Stardew 一个真实验证环境。

### 4.2 触发一个已规划的结构性动作

[AGENTS.md](../../AGENTS.md) 已写明：

```text
Phase B（物理拆仓）
    Adapter 迁往独立仓库 wia-adapter-<game>。
    触发条件：出现第二个真实 Adapter 时优先拆。
```

**本子阶段就是这个触发条件，且拆仓已获授权。** 因此 10.3 包含两个交付物：

1. 第二个游戏的真实 Adapter（`wia-adapter-<game>` 命名）。
2. Stardew Adapter 的物理拆仓（Phase B）。

Phase A 已完成逻辑分离，拆仓应当是低风险目录搬迁 + 建仓，而非重构。

### 4.3 游戏选择标准

选择必须在开工前明确记录，不接受"随便找一个小游戏"：

```text
有可程序化调用的 mod / 插件 API     否则只能反射，长期维护成本不可控
有真实玩家可见的状态与动作          否则只能验证协议，验证不了"世界"
能在本地被自动化驱动或脚本化验证    否则每条结论都要人工陪伴
与 Stardew 的差异足够大             复用度太高就证明不了泛化
许可证与分发方式允许接入与开源      避免法务与分发风险
```

### 4.4 验收重点：复用度而不是功能量

第二个 Adapter **不需要**功能对齐 Stardew。它要回答的是：

```text
Runtime 是否真的不需要为新游戏改核心？
协议是否有 Stardew-only 的隐含假设？
Adapter 接入是否有可复用的检查表与最小脚手架？
Phase A 的协议依赖方式在独立仓库里是否成立？
```

### 4.5 协议字段提升判定：tool_policy

Runtime-facing 执行策略当前承载在 `Capability.extensions.gameagent.tool_policy`，是一个未类型化的 `google.protobuf.Struct`，Runtime 侧解析见 `runtime/internal/tool/types.go` 的 `toolPolicyFromCapability`。这是 Phase6 的显式决策，不是遗留做法：

> Phase6 不新增 `CapabilityPolicy` 正式 proto 字段，Runtime 可读的 tool policy 使用现有 `Capability.extensions.gameagent.tool_policy` 承载。

依据是 [AGENTS.md](../../AGENTS.md) 的协议变更原则：尚未稳定为协议核心的 capability policy 优先使用 `Capability.extensions` 验证；详情见 [Mod 能力接入规范](../development/mod-capability-integration.md) §4.4。

**这个选择有真实代价，必须如实记录，不能当成零成本：**

```text
无编译期类型检查       Adapter 侧是 toolPolicy.Fields.Add("exclusive_per_step", Value.ForBool(true))，字符串 key
拼写错误静默失败       错误 key 不报错，Runtime 按零值处理，能力调度行为静默改变
分层解析复杂           三层可空拆解，且"字段缺失"与"类型非法"语义不同，后者跳过该 capability 并记 diagnostic
测试覆盖有限           现有断言只覆盖 Stardew present_dialogue 这一个使用点，不能替未来的 Adapter 挡住拼写错误
```

**判定时机就是本子阶段。** 第二个 Adapter 是第一个非 Stardew 的 policy 消费者，只有它能回答"key 集合是否已经稳定"。但要注意**复用不等于成熟**：第二个 Adapter 复用现有 key 只能证明"这不是 Stardew-only"，不能证明"字段集合已经定型"。因此判据分三段，而不是二值判断：

```text
第二个 Adapter 需要 Stardew 没有的新 key
    → key 集合仍在演化，继续留在 extensions

第二个 Adapter 需要的正好是现有那两个 key
    → 进入"可以提升"的候选状态，但不自动提升
    → 再判断：语义是否已稳定、字符串 key 解析是否已产生真实维护成本
    → 两项都成立才提升
```

判据依据的是可观察事实，不是"感觉稳定了"。若判定为提升，必须满足：

```text
additive 变更       新增 Capability.tool_policy，不移除、不重命名现有字段
双读过渡            Runtime 同时读一等字段与 extensions；Adapter 独立发布，老 Adapter 不得因此失效
不引入双来源        过渡期内同一语义只有一处权威来源，不得两处并行生效
```

提升与否及其依据必须在 10.3 结束时给出明确结论并记录，不接受无结论收尾。

### 4.6 明确不做

```text
与 Stardew 功能对等             第二个 Adapter 是泛化验证，不是功能追赶
第三个游戏                    第二款跑通后再评估
Adapter 之间的能力共享框架      先证明边界，再谈抽象；过早抽象必然错
完整游戏覆盖                    只覆盖证明泛化所需的最小能力集
```

### 4.7 内部拆分与提交粒度

10.3 包含三件性质不同的事，**不得揉成一个巨大提交**：

```text
10.3-A  第二游戏最小闭环        Adapter 实现 + 真实闭环证据
10.3-B  Stardew Adapter 拆仓    Phase B 物理分离
10.3-C  Adapter 接入检查表      从 A/B 沉淀可复用规范
```

各自独立方案、独立验收、独立提交。A 与 B 的顺序可依实际情况调整，但 C 必须在 A、B 之后——检查表应由真实经历沉淀，而不是先写规范再套用。

### 4.8 退出条件

1. 第二个 Adapter 能在独立仓库中构建、运行并与 Runtime 建立连接。
2. Runtime Core 未新增该游戏的任何专属分支（`check-architecture.ps1` 断言不被削弱，需覆盖新 Adapter）。
3. 该游戏至少完成一条"游戏事件 → AgentTurn → 能力执行 → 结果回灌"的真实闭环。
4. Stardew Adapter 完成物理拆仓，两个仓库都能独立构建与测试。
5. 沉淀出可复用的 Adapter 接入检查表，且第二个 Adapter 是按该检查表接入的。
6. §4.5 的 tool_policy 提升判定已给出结论，并记录支持该结论的第二个 Adapter 实际 policy 需求。

---

## 5. 依赖与顺序

### 5.1 为什么是这个顺序

你的规划顺序（mod → 客户端 → 第二款游戏）成立，理由不是"重要程度"而是**依赖与风险**：

```text
10.1 先做        它验证"能力可以来自外部"。如果这一步不成立，客户端展示什么、第二款游戏复用什么都无从谈起
10.2 其次        它把 10.1 的成果变成别人能看见、能复现的东西；也是 10.3 的调试基础（需要一个可观察面）
10.3 最后        成本最高、触发结构性改动（拆仓），应由前两步的稳定产物支撑
```

### 5.2 允许的重叠

以下工作**可以**与前一子阶段并行，因为它们不依赖前者的结论：

- 10.2-1（Runtime Bootstrap & Data Root）不依赖任何 UI 决策，建议在 10.1 期间顺手完成——10.1 会反复重启 Runtime，受益直接。10.2-1 只做 Bootstrap 与 Data Root，不含 HTTP：HTTP 面、bind、端口与本地访问保护都属于 10.2-2。
- 10.3 的游戏选型调研可以提前开展，但**接入实现**必须等 10.1 的 Adapter 接入边界稳定。
- 10.1 的实机验证与 10.2-3 的环境体检共用"检查依赖是否就绪"的逻辑，避免重复实现。

---

## 6. 跨子阶段的共同约束

无论哪个子阶段，都必须保持：

```text
Runtime Core game-agnostic           不依赖 Stardew / SMAPI 类型，不解析 Observation.state 游戏字段
协议 additive                        破坏性变更需单独授权
Adapter 是事实来源                   能力、schema、description、执行与失败原因都由 Adapter 拥有
不改 Mod 运行时标识                  UniqueID / EntryDll / AssemblyName 属已发布契约
不引入没有执行点的元数据字段         有执行点才加 policy（见 mod-capability-integration.md §4.4）
每个能力都要有负向用例               能力不适用时模型不应调用它
```

### 6.1 与技术方案文档的关系

本文件是阶段总纲，只回答"每个子阶段验证什么、边界在哪、什么算完成"。以下内容必须在各子阶段自己的技术开发与验收方案中另行设计，不在本文件冻结：

```text
具体接口与对象命名
协议字段定义
具体测试命令与通过标准
具体页面结构、HTTP endpoint、状态管理方案与视觉设计
具体游戏与 Adapter 目录结构
```

注：客户端**形态与技术选型**（本地 Web、Vue 3 + TypeScript + Vite、`//go:embed`、不做桌面打包、Portable ZIP 发行）已在 §3.3 与 §3.6 冻结，不属于本节所指的"待定内容"；非冻结部分是该形态**内部**的页面与接口设计。

### 6.2 已确认与仍待确认的开放项

已确认：

| # | 决策 | 结论 |
| --- | --- | --- |
| 1 | 阶段重排 | 旧 Phase10 → Phase11，旧 Phase11 → Phase12；Phase10 为 Ecosystem & Productization |
| 2 | Phase B 拆仓 | 授权，包含在 10.3 |
| 3 | 客户端形态 | 本地 Web UI：Vue 3 + TypeScript + Vite，产物 `//go:embed` 进 runtime 二进制；CLI 保留开发与无头入口；不做桌面打包 |
| 4 | 发行形态 | 只做 Portable Release（免安装 ZIP / 单个 runtime 二进制），不做 Installer |
| 5 | 平台范围 | 只服务同一台电脑上的本地浏览器；不做 Mobile、LAN、远程访问；手机形态留给未来的独立项目，不在 WIA 预埋 LAN/server 模式 |
| 6 | 访问凭证 | 不让用户手工复制 token；启动后自动打开浏览器并自动完成凭证交接（§3.4.1） |
| 7 | API Key | 允许在 Web UI 填写；只由 Go 后端持久化与读取，任何接口不回传明文，不落浏览器存储（§3.4.2） |
| 8 | Data Root | 方案 2：`--data-root` > `WIA_DATA_ROOT` > 平台默认目录；统一一个 root，相对路径相对 root 解析；不做 exe 同级默认、不做 portable marker、不做双 root（§3.2.1） |

仍待确认：

| # | 决策 | 影响 |
| --- | --- | --- |
| 9 | 10.3 的游戏与接入方式 | 决定成本与可行性；选择标准见 §4.3 |
| 10 | 静态加密是否用 OS 密钥库（DPAPI 等） | 纵深防御增强项，不改变 §3.4.2 的硬约束；由 10.2 子方案决定 |
| 11 | 10.1 读类 follow-up 是否本轮做 | 非硬验收；若 MFM 有低成本可读状态可顺带验证 |
| 12 | `tool_policy` 是否提升为 `Capability.tool_policy` 一等字段 | 由第二个 Adapter 的实际 policy 需求判定，判据见 §4.5；这是 10.3 的退出条件之一，不是可选项 |
| 13 | 自主触发 | **MVP0 不纳入。** durable task + 游戏时钟唤醒已由 Phase9 实机验证（§2.6.1），不再单独安排 mail-specific 后续；不引入 Background Trigger |

---

## 7. 阶段退出条件

Phase10 完成需要同时满足：

1. 三个子阶段各自的退出条件全部满足。
2. 至少一条"第三方 mod 能力被 agent 自主调用并产生真实游戏效果"的完整证据链。
3. 外部用户可以不读源码地把系统跑起来，并看到 agent 的工作过程。
4. 第二个真实游戏接入完成，且 Runtime Core 未出现该游戏专属分支。
5. Stardew Adapter 完成物理拆仓，两个仓库独立构建通过。
6. 协议保持 additive，Mod 运行时标识未改动。
7. 各子阶段的实机验证按其自身方案执行并记录，fake 与自动化结果不替代实机结论。
