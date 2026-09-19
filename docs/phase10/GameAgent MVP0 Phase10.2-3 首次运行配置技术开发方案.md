# GameAgent MVP0 Phase10.2-3 首次运行配置技术开发方案

> **Status:** ①②③④ 已实现并实机验证；首次配置页待做
> **Date:** 2026-09-19
> **Phase:** Phase10 Ecosystem & Productization / 10.2 客户端与产品化
> **总方案:** [GameAgent MVP0 Phase10 技术开发与验收总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md) §3.4 / §3.5 / §3.7
> **前置:** [10.2-2 本地控制面技术开发方案](GameAgent%20MVP0%20Phase10.2-2%20本地控制面技术开发方案.md)

---

## 1. 目标与范围

一句话目标：

> **启动 Runtime → 浏览器自动打开 → 填 Provider / Model / API Key → 保存（保存即验证）→ Runtime Ready → 去玩游戏。**

10.2-1 解决了"没有配置也能启动"，10.2-2 解决了"能看见"。剩下的缺口只有一个：**模型配置只能手工写文件，`api_key` 只认环境变量，而且没有任何地方能让用户确认"我配对了"**。

### 1.1 范围收窄记录

本子阶段开工后收窄过一次，结论如下（记录结论，不记录过程）：

```text
移出 10.2-3 → 10.2-4
  依赖体检（.NET / SMAPI / 游戏路径 / MFM 探测）
  Steam 注册表 / libraryfolders.vdf / 游戏路径 override / console.json
  "游戏是否已连接"的状态呈现，以及为此给 gateway 加的只读世界状态面
  多游戏选择器

理由  前两项要 Game Runtime 去做 Stardew 安装诊断，第三项要给 gateway 开只读世界状态面，
      都不服务于"用户能不能把 Runtime 配起来"这个唯一问题；最后一项现在只有 Stardew
      一个适配器，且 Runtime 没有按游戏选 profile 的机制（见 §4.1），做出来是个假控件
```

相应代价：总方案 §3.7 退出条件 4 在本子阶段只兑现"产出可用配置"这半句，"对缺失依赖给出提示"归 10.2-4。

## 2. 冻结决策

| # | 决策 | 结论 |
| --- | --- | --- |
| D1 | 凭据形式 | 保留 `env:`，新增 `file:`；相对引用相对 `model.json` 自身目录解析 |
| D2 | 静态保护 | POSIX `secrets/` 0700、凭据文件 0600；**Windows 不重写 DACL**，依赖数据根所在用户目录已有的权限 |
| D3 | 默认配置来源 | embed + **首次 seed**；锚点是 `agent.json`；不 merge / 不覆盖 / 不升级；显式 agent config override 时完全不 seed |
| D4 | 可修改范围 | 只做首次 model 配置；不做 Ready 之后的热切换；默认 UI 不暴露 `base_url` |
| D5 | 连接测试 | **必须先测过才允许落盘**；没有 skip 逃生口，也没有单独的"只测试不保存"路由（§6.3、§6.4） |
| D6 | 状态 | Runtime 三态：`needs_configuration` / `blocked` / `ready`，外加原因。连接测试的结果不持久化、不进状态 |

### 2.1 D2 的准确表述

```text
必须做   后端独占持有：凭据只由 Go 后端读写，不落浏览器存储，任何接口不回传明文
必须做   Unix 上 0700/0600，并且设置后复核（chmod 可能成功而 mode 不是要的那个）
         Windows 上 restrictPlatform 是有理由的 no-op：该平台没有等价于 POSIX mode 的东西
不做     重写 DACL
```

**"只有属主可读，否则不保存"是一条 POSIX 保证。** Windows 上的保护来自数据根所在用户目录本身的权限。安全模型上也说得通：以当前用户身份运行的进程本来就能读到这个文件、这个进程的内存，以及一切该用户能读的东西。真正要防的是凭据进 git、进 `model.json`、进响应、进浏览器存储、以及被**其他** OS 用户读到——`file:` 加用户目录已经覆盖这些。

**这条保护依赖数据根在用户的私有目录里。** 默认位置（`%LOCALAPPDATA%\WorldIsAgent` 等）满足这一点，但用户可以用 `--data-root` / `WIA_DATA_ROOT` 把数据根指到共享目录，**此时 WIA 不保证该目录的 ACL**——那是用户对自己选定位置的负责。这一点只在文档里说明，不重新引入 DACL 代码。

**保留 `file:` 而不把 key 写进 `model.json` 的理由不是理论安全，而是真实事故：** 用户把 `model.json` 贴到 issue / Discord / GitHub 时不会连 key 一起贴出去。`model.json` 是可分享配置，凭据是私密数据。

## 3. 凭据与配置的存储

```text
<root>/config/model.json     { "provider": ..., "api_key": "file:../secrets/model.key", ... }
<root>/secrets/model.key     凭据本体
```

- 相对 `file:` 引用相对 `model.json` 所在目录解析 → Data Root 整体搬移后仍然有效，`llm` 包不需要知道什么叫 Data Root。
- 读取边界：必须是普通文件、上限 8 KiB、读取后 TrimSpace、空视为未配置。
- 写入先写临时文件再 rename：**半截凭据比没有凭据更糟**，它看起来像配好了。
- 写入顺序：**先凭据、后引用它的配置**。反过来会让数据根停在一个指向不存在文件的配置上，比两个都没写更难恢复。
- 错误消息只带用户写下的引用（`file:../secrets/model.key`），**不带解析后的绝对路径**——这些消息会到达本地客户端。

## 4. Seed 机制

### 4.1 为什么要 seed

**per-game profile 不会被自动加载。** Runtime 读的是**一个** `agent.json` + **一个** `definition_catalog_root`，来源是 `GAMEAGENT_AGENT_CONFIG` 或 `<root>/config/agent.json`；`games/<game>/agent.json` 只有开发脚本会显式传进去。按 `game_id` 选 profile 需要延迟加载（`game_id` 只在适配器 Hello 之后才知道），属于 10.3。

所以不 seed 的结果是：用户看到 `Runtime Ready`，跑的却是通用基线——没有 definition、`max_steps=3`。**界面说配好了，运行的却不是我们验证过的 agent。**

### 4.2 资产位置

`//go:embed` 不能使用 `..`，而 `runtime/config/` 恰好是 `games/**` 的同一层，因此直接在该目录放一个 Go 包：

```text
runtime/config/defaults.go     package config；//go:embed all:games
```

embed 只含 `games/**`，**不含**顶层 `runtime/config/agent.json`（那是开发/测试基线，不是产品默认值）。该文件受架构检查约束、不能出现游戏名，所以 profile 是 `fs.Glob("games/*/agent.json")` **发现**出来的，并且要求恰好一个——发行版带两个 profile 时会明确报错而不是猜一个。

### 4.3 seed 的源与目标

```text
源  games/<game>/agent.json          →  <root>/config/agent.json
源  games/<game>/definitions/**.json →  <root>/config/games/<game>/definitions/**.json
```

用户树里**不写** `games/<game>/agent.json`：catalog loader 只读 `games/<game>/definitions/`（`runtime/internal/definition/loader.go:26`），那份文件没有消费者，写进去只会变成会漂移的副本。

### 4.4 触发规则

```text
<root>/config/agent.json 不存在 → 判定为 fresh，执行 seed
<root>/config/agent.json 已存在 → 不 seed、不补文件、不 merge、不覆盖
GAMEAGENT_AGENT_CONFIG 已设置   → 完全跳过 seed
```

**为什么必须用单一锚点，而不是"逐个文件检查缺失就补"：** 后者等价于隐式配置升级——未来版本新增一个 definition，老用户一启动它就自动出现了，这与"不升级"直接矛盾。definitions 先写、`agent.json` 最后原子落盘，中途崩溃时下次仍判定为未初始化。

## 5. 首次配置页

```text
Set up your model

Provider   [ DeepSeek ▾ ]
Model      [ deepseek-v4-flash ]
API Key    [ ••••••••••••••• ]

[ Save and continue ]
```

- 只有一个 card、一个按钮。**不显示游戏选择器**（只有 Stardew，且底层没有 profile 选择机制，见 §4.1）。
- **保存即验证。** 没有单独的"测试连接"按钮：后端本来就必须在落盘前自己探一次（§6.4），所以页面上再放一个测试按钮只会多花一次真实模型调用，并让"测过"和"保存了"变成两个可能不一致的状态。
- 失败时显示分类结果（`Authentication failed` / `Network unavailable` / …），**什么都不写**，用户改完再点。
- 保存成功后显示 `Runtime Ready ✓`，回到现有首页。
- 页面不显示 `base_url`（D4）。协议与后端支持它，开发者可手改；将来真需要 OpenAI-compatible endpoint 再放进 Advanced。
- 若将来用户确实要"先测不存"，再把那条路由加回来；现在为它保留接口与状态不值得。

## 6. 接口

沿用 10.2-2 的会话、Host 校验与静态资源机制；`/api` 一律需要会话凭证。

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| POST | `/api/setup/model` | 唯一入口：先探测**这次请求的同一组参数**，通过才写凭据 → 原子写 `model.json` → `Configure()` |

**没有单独的测试路由。** 探测本身就是提交路径的第一步，所以"只测试不保存"只是同一个动作去掉落盘那半；为它单开一条路由与一套状态，换不到闭环需要的东西。将来确有需求再加。

### 6.1 未落盘凭据的瞬态路径

首次配置要在写盘**之前**测试用户刚输入的 key，而持久化配置只承认 `env:` 与 `file:`。两者不冲突：

```text
持久化配置路径   Config.APIKey ∈ {env:, file:} → NewProviderFromConfigFile；内联 key 继续被拒
瞬态候选项路径   ProbeCandidate{provider, model, base_url, api_key} → ProbeConnection
                api_key 是函数参数，不来自任何配置文件
```

瞬态候选项的边界：只存在于该次请求与 Go 内存，不写 `model.json`、不写凭据文件、不进日志、不进 trace、不进任何响应，调用返回即丢弃。

### 6.2 结果分类

```text
ok                       提供商答复了
invalid_configuration    根本没到网络：provider 未知、window 非法、凭据为空
authentication_failed    提供商拒绝了凭据
network_unavailable      连不上、DNS、超时
provider_error           其它非成功响应
```

**provider 的原始响应体不回传**：那是给日志写的，而这些响应到达浏览器。

### 6.3 没有 skip 逃生口

不提供"不测试直接保存"。WIA 本身需要在线 provider 才能工作，离线用户即使保存成功也用不了；为这种情况引入二次确认、Unverified 状态与后续重启说明，不值得。测试失败就**什么都不写**，用户改完再试。

### 6.4 服务端自己探测，不信任何前端结论

即使将来页面加了"测试"按钮，**服务端也不复用那个结论**：前端测 A 通过、提交时 payload 变成 B，就会落盘一个未验证的配置。权威提交永远对**本次请求的参数**自己探一次。

提交顺序：**探测 → 通过 → 写凭据 → 写配置 → `Configure()`**。

**`blocked` 的数据根在探测之前就被拒**（code `setup_blocked`）。这类根的问题是"默认配置没能就位"或"既有 `agent.json` 不可用"，配一个模型解决不了它，本进程内也不会变 Ready；先探测等于花一次模型调用、再为它留下一个凭据文件，然后拒绝自己刚刚验证过的结果。探测前拒绝同时也保证了这一档**一个文件都不写**。

过了这道关之后 `Configure()` 仍可能失败（provider 不可用时 `NewProviderFromConfigFile` 报错，或 `definition_catalog_root` 指向的目录不可读）。这种情况下凭据与 `model.json` 已落盘，状态为 `needs_configuration` 并带原因——文件与状态是自洽的，用户修正后再次提交即可，无需重启。

### 6.5 Ready 之后拒绝再提交

`Configure()` 在真实 core 上重复调用是 no-op，所以 Ready 之后再写配置会让文件和正在运行的核心互相矛盾。接口直接拒绝并说明需要重启。本轮不做热切换。

## 7. 客户端可见的状态

```text
Runtime   needs_configuration / blocked / ready（外加 reason）
Model     provider、model、凭据是否可用（api_key_configured），以及不可用时为什么（api_key_problem）
```

页面只需要处理这三种状态：`needs_configuration` 显示表单；`blocked` 显示原因并说明要修正后重启（本轮没有其它出路）；`ready` 说明已配置完成。

`api_key_problem` 让界面能说"你引用的文件不存在"，而不是笼统的"未配置"——后者会把用户指向错误的下一步。它只包含用户写下的引用，不含解析后的路径。

**不引入 `Game Connected` / `MFM Available` / capability 快照。** 那属于"运行状态可视化"，归 10.2-4；本阶段也不为此给 gateway 加只读世界状态面。用户配好之后启动游戏、和 NPC 交互、在现有 Console 里看到 Turn，已经足以证明游戏链路工作。

## 8. 已核实的代码事实

```text
bootstrap.Configure()     只重读 model.json；真实 core 上重复调用是 no-op
                          模型不可用时装载 unavailableProvider，turn 仍能正常终止
bootstrap.Open()          seed 先于 agent 配置加载，且失败时置 blocked
                          agent.json 缺失时 LoadConfigFile 返回 DefaultConfig() 且不报错
                          → 通用基线只在【显式 override 或用户自备 agent.json】下出现；
                            空数据根要么拿到 seed 结果，要么 blocked，不会静默降级
llm.resolveAPIKey()       只接受 env:，且有测试显式拒绝内联 key；file: 是新增的第二种形式
model.WindowLimits        零值合法；省略时 provider 跳过自身窗口校验，且 Runtime 关闭摘要生成
per-game profile          不存在自动加载机制
definition loader         只读 <root>/<game>/definitions/*.json
gateway.Server            本阶段不使用其世界注册表
```

**写出的 `model.json` 必须带 window。** 省略是合法的，但代价是静默的：provider 跳过窗口校验、Runtime 关掉摘要生成。默认值取本仓库已在跑的那组（deepseek 131072/12800）；未知 provider 留空而不猜——给没测过的模型编一个 context size 是把推测当配置交付。

## 9. 实现顺序与状态

```text
① Default seed        空数据根自动获得 profile + definitions               ✅ 已实现并实机验证
② Secret storage      env: / file:、0600 复核、原子写、限长、路径不外泄     ✅ 已实现并实机验证
③ Connection probe    瞬态候选项路径 + 五类结果                            ✅ 已实现（单测）
④ Setup commit        test → 通过 → 落盘 → Configure；Ready 后拒绝          ✅ 已实现并实机验证
⑤ First-run UI        一个 card；测试成功才允许 Save                        ⬜ 待做
⑥ 空数据根端到端验收    见 §10                                             ⬜ 待做
```

把 ③ 放在 ④ 之前是有意的：先有探测能力，提交路径才能永远遵守"测过才落盘"，不需要任何 skip 分支。开发顺序服务最终产品，而不是反过来。

## 10. 验收

| 项 | 期望 |
| --- | --- |
| 空数据根启动 | `config/agent.json` + 35 个 definition 就位，状态 `needs_configuration`，日志出现 seed 行 |
| seed 幂等 | 第二次启动不新增/不覆盖任何文件，且无 seed 日志 |
| 显式 override | 设 `GAMEAGENT_AGENT_CONFIG` 时一个默认文件都不写 |
| 未通过测试不可提交 | 探测失败时返回明确 code，且凭据与配置**都没写** |
| 默认配置就位失败 | 状态 `blocked` 且带原因；提交被拒（`setup_blocked`）且**不探测、不写任何文件**；有效模型配置也不能让它变 Ready |
| 配置正确 | 写出的 `model.json` 引用凭据而非包含它，带 window，`api_key` 为相对引用 |
| 不泄露 | `/api/status` 与 `/api/setup/model` 的响应都不出现 key；进程日志不出现 key |
| 权限 | Unix：`secrets/` 0700、凭据 0600；权限设置失败时不落盘 |
| Ready 后拒绝 | 再次提交被拒、配置文件未被改动，提示需要重启 |
| 重启 | 配置与 seed 结果都保持，且不重复 seed |
| 端到端（⑥） | 空目录 → 浏览器自动打开 → 默认配置已就位 → 填表 → 保存 → `Ready` → 重启仍 Ready → 启动游戏并交互 → Turn 出现在 Console |

## 11. 明确不做

```text
依赖体检 / 游戏路径探测 / Steam 注册表             → 10.2-4
游戏是否已连接 / MFM 可用 / gateway 只读世界状态面   → 10.2-4
多游戏选择器与 profile 选择机制                    → 10.3（出现第二个 Adapter 时）
Ready 之后的热切换 provider/model/key              需要真实的 core replacement 语义
OS 密钥库（DPAPI / Keychain / Secret Service）     记为增强项
Windows DACL 重写                                 见 §2.1
不测试直接保存（skip）                              见 §6.3
运行时热改 agent.json                              Configure() 只重读 model.json
由 Runtime 向游戏目录安装 mod                       §3.6 明确 Adapter 单独安装
Background Trigger / Mobile / LAN / 远程访问        见总方案 §2.6、§3.4
```

## 12. 决策记录

```text
D1  env: 保留 + 新增 file:，相对引用基于 model.json 目录
D2  POSIX 0700/0600 并复核；Windows 不重写 DACL，依赖用户目录权限（准确表述见 §2.1）
D3  embed + 首次 seed；锚点 agent.json；不 merge/覆盖/升级；显式 override 时完全不 seed
D4  只做首次 model 配置；不做 Ready 后热切换；默认 UI 不暴露 base_url
D5  必须先测过才落盘；无 skip 逃生口；也没有单独的"只测试不保存"路由
D6  只有 Runtime 两态 + 原因；测试结果不持久化、也不进状态
另  window 默认值保留（本仓库已在跑的值）；未知 provider 不猜
另  api_key_problem 保留：让界面能说出"凭据为什么不可用"
另  seed 失败必须阻断 Ready，而非只记日志：否则界面会显示 Ready 而实际跑的是通用基线
另  不暴露 seeded 状态字段：它只说"本次启动有没有 seed"，容易被读成"当前是否运行在发行默认值上"
```
