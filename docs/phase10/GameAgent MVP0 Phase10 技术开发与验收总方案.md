# GameAgent MVP0 Phase10 技术开发与验收总方案

> **Status:** Implementation Plan Draft — 等待用户确认后开工
> **Date:** 2026-09-18
> **Phase:** Phase10 Ecosystem & Productization（生态接入、产品化与跨游戏验证）
> **目标:** 证明 WIA 的能力边界可以向外扩展——第三方 mod 能力可被 agent 自主调用、系统可以被外部用户装起来用、Adapter 架构可以被第二个真实游戏复用
> **Code Inspection Baseline:** `main` @ `ef50436`；Phase A 与 Mod 邮件能力接入已授权
> **技术栈:** Go、SQLite、gRPC / Protobuf、C#、SMAPI、Go HTTP（新增本地控制面）
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

> **agent 能在没有任何点名提示的情况下，自主选择并成功执行一个由第三方 mod 提供的能力，并在真实游戏里产生可观察、可验收的效果。**

首个接入对象已确定：**MailFrameworkMod（MFM 1.20.0，邮件能力）**。详细方案见 [mail-capability.md](../development/mail-capability.md)，本阶段遵守 [mod-capability-integration.md](../development/mod-capability-integration.md) 的全部硬边界。

### 2.2 覆盖的能力类型

一次覆盖三类，避免只测一条 happy path：

```text
读类    把 mod 的状态变成 Observation 事实
写类    让 agent 触发 mod 的动作（首个：send_mail）
判定类  能力不适用时模型不调用（负向用例）
```

### 2.3 范围

- `send_mail(title?, body)`：仅文本、无附件、无 recipe、无 AutoOpen，立即投递。
- Adapter 侧输入校验（信件正文进入游戏 `TokenParser`，文本即命令通道）。
- MFM 通过 SMAPI `GetApi` 作为**可选依赖**接入；未安装时 adapter 正常加载、能力不发布或以明确 code 拒绝。
- 模型自主调用的证据链：能力进入 Tool View → 模型主动产生 ToolCall → 真实执行 → 结果回灌 → 影响下一步决策。
- 至少再接入一个读类 mod 能力，证明"读"与"写"两侧都成立。

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
2. 模型在无点名提示下自主选择 `send_mail`（有 SMAPI 日志证据）。
3. 信件成功投递，玩家可读，`mailReceived` 记录 letter.Id。
4. 文本注入有自动化测试覆盖，注入样本被拒。
5. 至少一个读类 mod 能力进入 Observation 并被模型使用。
6. 负向用例通过：能力不适用时模型不调用。
7. [mod-capability-integration.md](../development/mod-capability-integration.md) §7 检查清单全部满足。

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

### 3.2 技术选型与优先级（已确认）

```text
桌面端    Wails v2 + Vue 3 + TypeScript + Vite + Go
优先级    Desktop 为主产品入口
          CLI 保留为开发入口
          本地 Web 暂不单独做
```

选型依据：

- Wails v3 目前仍是 beta / 预发布（最新为 `v3.0.0-beta.23`），**v2 是稳定线**——与"固定版本、优先稳定"的定位一致。
- 采用桌面形态后，**不需要为客户端新增本地 HTTP 面**：Go 侧通过 Wails 绑定直接暴露方法，前端通过 bridge 调用。这消除了原方案里"新增对外接口需单独授权"的问题。
- 运行时仍需要 10.2-1 的路径与端口可配置，因为嵌入式启动同样不能依赖 cwd。

本机工具链现状（已核实）：

```text
Go       1.25.3            ✅
Node     24.14.1 / npm 11.11.0  ✅
WebView2 运行时 153.0.4234.32   ✅（Windows 上 Wails 的前置依赖）
Wails CLI                        ❌ 未安装，需要 go install
```

### 3.3 范围（按依赖顺序，不是按 UI 好看程度）

| 步骤 | 内容 | 为什么在这个位置 |
| --- | --- | --- |
| 10.2-1 | Runtime 运行形态：数据目录、端口、配置路径可配置；可作为独立产物在任意目录运行 | 不做这个，桌面端嵌入式启动同样会写错位置 |
| 10.2-2 | Wails 应用骨架 + Go 侧服务绑定：启动/停止 Runtime、读取状态 | 先把"能驱动 Runtime"打通，再谈界面 |
| 10.2-3 | 首次运行向导与依赖体检：写配置与 key、检查 .NET / SMAPI / 游戏路径 / key 有效性 | 这一步做完，"外人能跑起来"才成立 |
| 10.2-4 | 可视化：AgentTurn 时间线（复用现有 JSONL trace）、对话记录、任务与记忆查看 | 这是"好看"，前三步才是"能用" |

### 3.4 明确不做

```text
多用户 / 远程访问 / 云端托管        本地优先，不做账号体系
本地 Web 独立入口                   优先桌面端；Web 形态待桌面端稳定后再评估
配置运行时热改并立即生效           先保证"配好能跑"，不做在线调参
完整可观测平台                     只暴露定位问题所需的最小面
替代游戏内 UI                      游戏内交互仍由 Adapter 与游戏负责
```

### 3.5 退出条件

1. 全新环境（无现有配置）能在**任意目录**启动 Runtime，数据落点正确、可重复。
2. 首次运行向导能产出可用配置，并对缺失依赖给出可操作的明确提示。
3. 用户在游戏里触发一次交互后，能在客户端看到该 Turn 的关键链路。
4. 模型 key 无效或缺失时，错误信息指向具体原因，不表现为"agent 不说话"。
5. 不引入对 Runtime Core 的 game-specific 依赖。

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

### 4.6 退出条件

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
具体 UI 形态与技术选型
具体游戏与 Adapter 目录结构
```

### 6.2 已确认与仍待确认的开放项

已确认：

| # | 决策 | 结论 |
| --- | --- | --- |
| 1 | 阶段重排 | 旧 Phase10 → Phase11，旧 Phase11 → Phase12；Phase10 为 Ecosystem & Productization |
| 2 | Phase B 拆仓 | 授权，包含在 10.3 |
| 3 | 客户端形态 | Wails v2 + Vue 3 + TypeScript + Vite + Go；桌面端为主入口，CLI 保留开发入口，本地 Web 暂不单独做 |

仍待确认：

| # | 决策 | 影响 |
| --- | --- | --- |
| 4 | 10.3 的游戏与接入方式 | 决定成本与可行性；选择标准见 §4.3 |
| 5 | 10.1 读类能力的具体 mod 清单 | 决定接口稳定性风险 |
| 6 | 10.2 发行形态：安装包 / 免安装 / 是否随 Runtime 一起分发 | 决定打包与更新机制的工作量 |

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
