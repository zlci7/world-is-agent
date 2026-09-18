# GameAgent MVP0 Phase10 技术开发与验收总方案

> **Status:** Implementation Plan Draft — 等待用户确认后开工
> **Date:** 2026-09-18
> **Phase:** Phase10 Ecosystem & Productization（生态接入、产品化与跨游戏验证）
> **目标:** 证明 WIA 的能力边界可以向外扩展——第三方 mod 能力可被 agent 自主调用、系统可以被外部用户装起来用、Adapter 架构可以被第二个真实游戏复用
> **Code Inspection Baseline:** `main` @ `ef50436`；Phase A 与 Mod 邮件能力接入已授权
> **技术栈:** Go、SQLite、gRPC / Protobuf、C#、SMAPI、Vue 3 + TypeScript + Vite（构建期）、Go `net/http` 与 `//go:embed`（本地控制面与 UI 分发）
> **Roadmap:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md)
> **Architecture:** [Runtime 整体架构设计规范](../summary/GameAgent%20Runtime%20整体架构设计规范.md)
> **前置:** [Mod 能力接入规范](../development/mod-capability-integration.md)、[邮件能力方案](../development/mail-capability.md)、[Phase A 逻辑分离](../development/logical-separation.md)

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

首个接入对象已确定：**MailFrameworkMod（MFM 1.20.0，邮件能力）**。详细方案见 [mail-capability.md](../development/mail-capability.md)，本阶段遵守 [mod-capability-integration.md](../development/mod-capability-integration.md) 的全部硬边界。

### 2.2 覆盖的能力类型

本阶段以**写类能力**为主，先证明一条最小闭环：

```text
写类    让 agent 触发 mod 的动作（首个且必做：send_mail）
判定类  能力不适用时模型不调用（负向用例，必做）
读类    把 mod 的状态变成 Observation 事实（可选 / follow-up）
```

```text
MFM → send_mail → Capability → AgentTurn → ToolCall → 真实信件
```

读类**不作为硬验收**：为凑"读/写/判定三类齐全"而额外引入一个 mod，会把最小闭环扩成"再找一个 mod → 研究它的 API → 接 Observation → 验证模型使用"。如果 MFM 本身已有低成本且有价值的可读状态，可顺带验证；否则留作 follow-up。

### 2.3 范围

- `send_mail(title?, body)`：仅文本、无附件、无 recipe、无 AutoOpen，立即投递。
- Adapter 侧输入校验（信件正文进入游戏 `TokenParser`，文本即命令通道）。
- MFM 通过 SMAPI `GetApi` 作为**可选依赖**接入；未安装时 adapter 正常加载、能力不发布或以明确 code 拒绝。
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

1. `send_mail` 进入 Tool View；MFM 未安装时以明确 code 拒绝且 adapter 不崩溃。
2. 在一个由玩家或游戏事件触发的 AgentTurn 内，系统 prompt 不点名 `send_mail`，模型自行从 Tool View 选中并调用它（有 SMAPI 日志证据）。
3. 信件成功投递，玩家可读，`mailReceived` 记录 letter.Id。
4. 文本注入有自动化测试覆盖，注入样本被拒。
5. 负向用例通过：能力不适用时模型不调用。
6. [mod-capability-integration.md](../development/mod-capability-integration.md) §7 检查清单全部满足。
7. （可选）读类能力接入并进入 Observation。

不要求任何形式的 Background Trigger、Autonomous Goal 或 NPC 自发任务。

---

## 3. 阶段 B：客户端与产品化（10.2）

### 3.1 目标

验证命题：

> **一个只有游戏和 Runtime 的普通用户，能在不读源码、不改配置文件的前提下把 WIA 跑起来，并看见 agent 在做什么。**

当前配置面完全是手工的：`runtime/config/model.json`、`runtime/config/agent.json`、环境变量 `GAMEAGENT_MODEL_CONFIG` / `GAMEAGENT_AGENT_CONFIG`，且 runtime **没有任何 HTTP 面**。这既是"对外可运行"的最大缺口，也是本子阶段的起点。

### 3.2 关键前置：Runtime 必须脱离 cwd

已核实的事实（`runtime/cmd/server/main.go`）：

```text
trace 路径硬编码 "runtime/.local/traces.jsonl"，依赖从仓库根目录启动
gRPC 端口硬编码 127.0.0.1:50051
数据目录与配置路径没有统一的定位规则
```

**下载一个二进制、在任意目录启动，当前会写错位置或直接失败。** 这是 10.2 的入场券，不是可选项。

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

已核实的代码事实：

```text
runtime 当前没有任何 HTTP 服务        （无 ListenAndServe / ServeHTTP）
runtime 当前没有任何 embed 静态资源    （无 //go:embed）
因此 HTTP 面与资产内嵌都是净新增，不影响现有 gRPC 链路
```

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
| 10.2-1 | Runtime 运行形态：数据目录、端口、配置路径可配置；可作为独立产物在任意目录运行 | 当前 trace 路径硬编码且依赖从仓库根启动，不做这个后面都不成立 |
| 10.2-2 | 极简本地 HTTP 面 + 资产内嵌：health / status / turn 列表；`//go:embed` 前端产物 | 客户端要有东西可连；同时它就是 10.1 与 10.3 的调试面 |
| 10.2-3 | 首次运行向导与依赖体检：写配置与 key、检查 .NET / SMAPI / 游戏路径 / key 有效性 | 这一步做完，"外人能跑起来"才成立 |
| 10.2-4 | 可视化：AgentTurn 时间线（复用现有 JSONL trace）、对话记录、任务与记忆查看 | 这是"好看"，前三步才是"能用" |

注意 10.2-2 与 10.2-4 共用同一个 HTTP 面：**不需要为"控制面"和"UI 接口"各做一套。**

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

### 4.5 明确不做

```text
与 Stardew 功能对等             第二个 Adapter 是泛化验证，不是功能追赶
第三个游戏                    第二款跑通后再评估
Adapter 之间的能力共享框架      先证明边界，再谈抽象；过早抽象必然错
完整游戏覆盖                    只覆盖证明泛化所需的最小能力集
```

### 4.6 内部拆分与提交粒度

10.3 包含三件性质不同的事，**不得揉成一个巨大提交**：

```text
10.3-A  第二游戏最小闭环        Adapter 实现 + 真实闭环证据
10.3-B  Stardew Adapter 拆仓    Phase B 物理分离
10.3-C  Adapter 接入检查表      从 A/B 沉淀可复用规范
```

各自独立方案、独立验收、独立提交。A 与 B 的顺序可依实际情况调整，但 C 必须在 A、B 之后——检查表应由真实经历沉淀，而不是先写规范再套用。

### 4.7 退出条件

1. 第二个 Adapter 能在独立仓库中构建、运行并与 Runtime 建立连接。
2. Runtime Core 未新增该游戏的任何专属分支（`check-architecture.ps1` 断言不被削弱，需覆盖新 Adapter）。
3. 该游戏至少完成一条"游戏事件 → AgentTurn → 能力执行 → 结果回灌"的真实闭环。
4. Stardew Adapter 完成物理拆仓，两个仓库都能独立构建与测试。
5. 沉淀出可复用的 Adapter 接入检查表，且第二个 Adapter 是按该检查表接入的。

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

- 10.2-1（Runtime 脱离 cwd）是纯参数化，不依赖任何 UI 决策，建议在 10.1 期间顺手完成——10.1 会反复重启 Runtime，受益直接。
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

仍待确认：

| # | 决策 | 影响 |
| --- | --- | --- |
| 8 | 10.3 的游戏与接入方式 | 决定成本与可行性；选择标准见 §4.3 |
| 9 | 静态加密是否用 OS 密钥库（DPAPI 等） | 纵深防御增强项，不改变 §3.4.2 的硬约束；由 10.2 子方案决定 |
| 10 | 10.1 读类 follow-up 是否本轮做 | 非硬验收；若 MFM 有低成本可读状态可顺带验证 |

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
