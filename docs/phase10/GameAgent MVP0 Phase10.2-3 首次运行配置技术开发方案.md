# GameAgent MVP0 Phase10.2-3 首次运行配置技术开发方案

> **Status:** 决策已冻结，待开工
> **Date:** 2026-09-18
> **Phase:** Phase10 Ecosystem & Productization / 10.2 客户端与产品化
> **总方案:** [GameAgent MVP0 Phase10 技术开发与验收总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md) §3.2 / §3.4 / §3.5 / §3.7
> **前置:** [10.2-2 本地控制面技术开发方案](GameAgent%20MVP0%20Phase10.2-2%20本地控制面技术开发方案.md)

---

## 1. 目标与命题

> **一个只有游戏和 Runtime 的普通用户，不读源码、不改配置文件，就能把 WIA 配起来并确认它真的连上了游戏。**

10.2-1 解决了"没有配置也能启动"，10.2-2 解决了"能看见"。剩下的缺口是：**模型配置只能手工写文件**，`api_key` 只接受 `env:VAR`，而 Runtime 本身不依赖游戏、也不知道游戏装在哪，所以用户没有任何地方能确认"我装对了"。

## 2. 冻结决策

| # | 决策 | 结论 |
| --- | --- | --- |
| D1 | Secret 形式 | 保留 `env:`，**新增 `file:`**；`file:` 的**相对路径相对于 `model.json` 自身所在目录**解析 |
| D2 | 静态保护 | **明文文件 + 权限收紧**：`<root>/secrets/` 0700、secret 文件 0600；Windows 用当前用户 ACL。OS 密钥库不在本轮 |
| D3 | 依赖体检 | 文件系统只做**启发式**；`ready + 能力列表`做**在线权威判断**；游戏路径启发式预填、**用户可改且以用户为准** |
| D4 | 可修改配置 | 本轮**只做首次 model 配置**；不做 Ready 之后的热切换；默认 UI 不暴露 `base_url` |
| D5 | 默认配置来源 | **embed + 首次 seed**；只在首次初始化 seed，不 merge / 不 overwrite / 不自动升级；显式 agent config override 时完全不 seed |
| D6 | 成功状态 | **三个状态独立**：Runtime Ready / Model Connection / Game Connected，互不冒充 |
| D7 | seed 的 `agent.json` | seed 源是 `games/stardew-valley/agent.json`，落到 `<root>/config/agent.json`；顶层通用基线只作开发/测试用（§4.1、§5.2） |
| D8 | 游戏路径 | 持久化到 `<root>/config/console.json`，**只写用户显式 override**；Core 不读它（§4.2） |

### 2.1 D1 的完整语义

```text
api_key: "env:DEEPSEEK_API_KEY"       现有方式，不变
api_key: "file:../secrets/model.key"  新增；相对路径基于 model.json 所在目录
api_key: "sk-..."                     继续拒绝（现有测试就是这条立场）
```

`file:` 不依赖进程 cwd，因此：Data Root 整体搬移后仍有效；`llm` 包不需要知道什么叫 Data Root；`GAMEAGENT_MODEL_CONFIG` 指向自定义路径时规则仍然自洽。

读取 secret 文件时的边界：

```text
必须是普通文件（拒绝目录、设备、符号链接目标以外的形式）
尺寸上限 8 KiB
读取后 TrimSpace；结果为空视为未配置
```

**不回传 secret 内容，也不回传 secret 文件路径。** 客户端只知道"已配置 / 未配置"。

### 2.2 D2 的硬线

```text
保存 secret → 设置权限失败 → 本次配置失败，且明确提示
```

绝不允许"ACL/权限设置失败 → 只记一条 warning → 仍然把 key 写成普通文件"。

### 2.3 D3 的权威关系

```text
Adapter 已连接 + WorldBindingReady   → WIA ↔ Stardew 的真实链路成立
能力列表含 send_mail                 → MFM 已被 Adapter 成功解析
```

**MFM 是可选依赖，缺少它不能让"游戏已连接"变红。** 正确呈现是"已连接 + 邮件能力不可用"，而不是"安装失败"。

## 3. 三个独立状态

```text
Runtime Configuration   本地结构状态   配置格式正确、secret 可读、provider 可构造
Model Connection        外部网络诊断   向 provider 发过一次请求，真的通了
Game Connected          Adapter 状态   适配器在线且世界绑定就绪
```

**`Configure()` 成功不等于 API Key 有效。** 它只证明配置能读、provider 能构造，没有发生过任何网络请求。因此 UI 不得把 `Runtime Ready` 翻译成 "API Key verified"。

### 3.1 由此得出的一个必要设计：先验证，再落盘

`Configure()` 在真实 core 上重复调用是 **no-op**：

```go
if r.loop != nil && !r.placeholder { return nil }
```

也就是说首次 `needs_configuration → ready` 之后，改 `model.json` 再调 `Configure()` **不会生效**。如果向导允许"先保存、后测试"，那么用户打错一个字符就会进入 Ready、之后无法在 UI 里纠正（只能改文件 + 重启）。

因此本轮的顺序固定为：

```text
填写 → 测试连接 → 成功才写入 secret 与 model.json → Configure() → Runtime Ready
```

测试失败就**不落盘**，用户可以直接改。唯一的例外是离线用户：提供一次显式确认（"不测试直接保存"），并在 UI 上标明该配置未经验证。未落盘 key 的测试路径见 §6，权威提交的复测见 §7.1。

## 4. 两个结构问题及其结论

### 4.1 D7：seed 的 `agent.json` 用哪一份

**已核实：per-game profile 根本不会被 Runtime 自动加载。** 仓库里没有 profile 解析机制——`agent.LoadConfigFile` 只读一个路径，来源是 `GAMEAGENT_AGENT_CONFIG` 或 `<root>/config/agent.json`。`runtime/config/games/stardew-valley/agent.json` 只是**开发脚本**用 `-AgentConfig` 显式传进去的。

于是两份配置的差别是实打实的：

```text
runtime/config/agent.json                     通用基线：max_steps 3、无 definition_catalog_root、
                                              通用 prompt（"符合当前角色与世界状态的语气"）
runtime/config/games/stardew-valley/agent.json Stardew：max_steps 5、definition_catalog_root
                                              "config/games"、Stardew prompt + tool_instruction
```

缺 `definition_catalog_root` 就不会加载 33 个 NPC definition；`max_steps 3` 也不够一次"工具 → 确认 → 收尾"的回合。所以**seed 哪一份直接决定"配好了"是不是开发/README 验证过的那个 agent**。

**结论：seed 源是 Stardew profile，目标是 `<root>/config/agent.json`（逐项映射见 §5.2）。** `runtime/config/agent.json` 保留为开发与测试基线，不参与产品 seed。

不采用"按 `game_id` 选 profile"的机制：`game_id` 只在适配器 Hello 之后才知道，那要求 profile 延迟加载，属于 10.3 的 Runtime 生命周期工作。

### 4.2 D8：用户填的游戏路径

Runtime 自己不使用游戏路径，它只服务于依赖体检。

**结论：持久化到 `<root>/config/console.json`，且只写用户显式确认的 override。** 自动探测结果不写——否则一次错误的启发式结果会从"临时探测值"变成"永久配置"。该文件属于控制台，Agent Core 不读它。

## 5. Seed 机制

### 5.1 资产位置（零文件搬迁）

Go 的 `//go:embed` 不能使用 `..`，所以 embed 包必须位于资产目录或其上层。`runtime/config/` 恰好是 `games/**` 的**同一层**，因此直接在该目录加一个 Go 包即可：

```text
runtime/config/defaults.go        package config；//go:embed all:games
```

这样不用移动任何现有文件，也不用把包命名成 `runtime`（那会与标准库 `runtime` 同名，在每个同时用 `runtime.GOOS` 的文件里都要别名）。

**embed 只包含 `games/**`，不含顶层的 `runtime/config/agent.json`。** 后者是通用的开发/测试基线，不是产品默认值（见 D7）。

已实测该布局可行：把 `package config` 放在 `runtime/config/` 并 embed `all:games`，可嵌入 **36 个文件**（`games/stardew-valley/agent.json` 加 `definitions/` 下 35 个），且 `check-architecture.ps1` 仍然通过。

**该文件有一个约束**：`scripts/check-architecture.ps1` 会检查 `runtime/` 下所有 `*.go` 与 `*.json` 是否含游戏专有词（`runtime/config/games/` 被排除，`runtime/config/defaults.go` 不被排除）。所以这个文件里不能出现游戏名，注释也要保持通用措辞。

### 5.2 seed 的源与目标（逐项映射，不是整树复制）

```text
源  runtime/config/games/stardew-valley/agent.json
→   <root>/config/agent.json

源  runtime/config/games/stardew-valley/definitions/**.json
→   <root>/config/games/stardew-valley/definitions/**.json
```

**只有这一个 Stardew profile 权威源。** 产品默认就是这份内容，不额外复制出第三份 `agent.json`，也不把 `games/stardew-valley/agent.json` 写进用户树——已核实 catalog loader 只读 `games/<game>/definitions/`（`runtime/internal/definition/loader.go:26`），用户树里那份文件没有任何消费者，写进去只会变成一份会漂移的副本。

`runtime/config/agent.json`（通用基线）继续只服务开发与测试，完全不参与 seed。

### 5.3 触发规则：`agent.json` 作为首次初始化锚点

```text
<root>/config/agent.json 不存在  → 判定为 fresh，执行 seed
<root>/config/agent.json 已存在  → 不 seed、不补文件、不 merge、不覆盖
GAMEAGENT_AGENT_CONFIG 已设置    → 完全跳过 seed（显式配置优先，不能背后再塞一套 Stardew 默认值）
```

**为什么必须用单一锚点，而不是"逐个文件检查缺失就补"：** 后者等价于隐式配置升级——未来版本新增一个 definition，老用户一启动它就自动出现了，这与"不升级"直接矛盾。

**落盘顺序：** 先写 `games/**` 与其它文件，**`agent.json` 最后原子落盘**（临时文件 + rename）。中途崩溃时下次仍判定为未初始化，可以重新完成。

### 5.4 seed 什么

```text
✅ <root>/config/agent.json                                  ← games/stardew-valley/agent.json 的内容
✅ <root>/config/games/stardew-valley/definitions/**.json    ← 35 个 definition
❌ <root>/config/model.json
❌ <root>/config/games/stardew-valley/agent.json（无消费者，见 §5.2）
❌ 任何 secret
```

## 6. 连接测试：未落盘 key 的瞬态路径

### 6.1 为什么需要一条独立路径

首次向导要在**写盘之前**测试用户刚输入的 key，但持久化配置只承认 `env:` 与 `file:`，且内联 key 继续被拒绝。所以不能把用户输入塞进 `Config.APIKey` 再交给 `NewProvider`——那等于绕开刚冻结的边界。

**正确的区分是：`inline key 不得持久化` 与 `向导需要用未落盘的 key 测一次` 是两条不同的规则。**

```text
持久化配置路径            Config.APIKey ∈ {env:, file:} → NewProviderFromConfigFile
                         resolveAPIKey 继续拒绝其它形式
瞬态候选项路径            ProbeConfig{provider, model, base_url, api_key} → ProbeConnection
                         api_key 是函数参数，不来自任何配置文件
```

### 6.2 瞬态候选项的边界（必须逐条成立）

```text
只存在于该次请求与 Go 内存
不写 model.json、不写 secret 文件
不进入日志
不进入 trace
不进入任何 /api 响应
调用返回后立即丢弃
```

测试连接的结果因此也必须分类返回，且**错误消息不得包含请求头或凭证**：

```text
verified                 请求成功
authentication_failed    401 / 403
network_unavailable      连接失败、DNS、超时
provider_error           其它非成功响应
```

实现上：用最小的一次调用（例如 `max_tokens=1`）加一个较短超时；分类只依赖状态码与错误类别，不回传 provider 的原始响应体。**要有一条测试断言 key 不出现在结果里**，与 `llm` 包现有的配置摘要测试同一种做法。

## 7. 接口

沿用 10.2-2 已有的会话、Host 校验与静态资源机制；新增端点都在 `/api` 下、都需要会话凭证。

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| POST | `/api/setup/test` | 交互式预检查。**永不落盘。** |
| POST | `/api/setup/model` | 权威提交：服务端对**这次请求的同一组参数**再测一次，成功才写 secret → 原子写 `model.json` → `Configure()` |
| GET | `/api/setup/environment` | 依赖体检：探测结果 + 用户覆盖值 + 在线权威状态 |
| PUT | `/api/setup/environment` | 保存用户选定的游戏路径（仅显式 override 时写） |

### 7.1 权威提交不复用前端的测试结论

前端的 Test 只是 UX 预检查。**服务端不信任"前端已经测过了"**，否则存在 TOCTOU：前端测 A 通过、提交时 payload 变成 B，就直接落盘了一个未验证的配置。

```text
正常路径   Test → Save → 服务端再 Test → Commit
离线路径   Save with skip_connection_test=true → Commit + UI 标记 Unverified
```

离线逃生口由请求显式携带：

```json
{ "skip_connection_test": true }
```

它必须是用户二次确认后的请求，且 UI 要同时说明两件事：**保存成功不等于模型可用**；由于本轮不支持 Ready 之后替换 model，**离线保存错了 key 需要重启 Runtime 才能纠正**。

`GET /api/status` 扩展为三段状态（Runtime / Model Connection / Game Connected）加 seed 结果，仍然**不返回任何 secret**。

约束（沿用 10.2-2 的既有边界）：只监听 loopback、Host 必须回环、`/api` 需要会话、响应不回传凭证、`Cache-Control: no-store`。

## 8. Runtime 侧最小新增面

三个状态里有两条来自 Runtime 之外的信息，需要**只读**访问器（不改变 gRPC、协议、Loop 与调度行为）：

```text
Runtime Ready        已有：bootstrap.Runtime.State()
Model Connection     新增：一条最小 provider 调用，结果只用于显示，不写入 Bootstrap State
Game Connected       新增：gateway 只读世界状态（workspace 绑定是否就绪 + 实体 + 能力名列表）
```

`gateway.Server.WorldRegistry()` 已经是导出方法，但 `ReadyWorlds()` 只返回 `task.Head`，不含能力名。因此需要新增一个只读快照访问器（例如 `WorldStatuses()`，返回世界键、就绪标志、实体 id、能力名），由 `httpapi` 通过窄接口消费——与 10.2-1 的 `coreReadiness` 同一种做法。

## 9. 依赖体检状态矩阵

```text
Stardew installation      Detected / Not found / User selected
GameAgent Adapter         Detected on disk / Unknown
Live connection           Connected / Waiting for game
Mail Framework Mod        Available / Unavailable / Unknown
```

- 前三行里，只有 `Live connection = Connected` 是**权威**的；`Detected` 只表示"目录像那么回事"。
- 第四行只在已连接时才有权威值；未连接时显示 `Unknown`，不得显示"缺失"。
- 游戏路径的启发式来源（Steam 注册表、`libraryfolders.vdf`、常见路径）**只是提示，不是事实源**；实现时逐个验证并记录，找不到就留空让用户填。

## 10. 实现顺序

按依赖从底向上，不从 UI 开始：

```text
①  Default seed          空数据根启动后 agent.json + Stardew definitions 自动就位
②  Secret storage        env: / file:、0600 与 ACL、权限失败即保存失败、secret 不外泄
③  Model Setup API       写 secret → 原子写 model.json → Configure()；只接受显式 skip_connection_test
④  Connection probe      瞬态候选项路径；③ 的提交改为"先复测再落盘"，skip 变成逃生口
⑤  First-run UI          Provider / Model / API Key；Save 前先测试
⑥  Stardew diagnostics   启发式路径 + 在线权威状态 + MFM 可选显示
⑦  空数据根实机验收        见 §11
```

①必须在③之前：③的验收前提是"新用户拿到的是一个能用的默认配置树"。

③与④的先后有一个必须交代的点：④之前不存在瞬态测试路径，因此 ③ 的提交路径**只接受显式的 `skip_connection_test`**，不会在没人注意的情况下落盘一个"看起来验证过"的配置；④落地后才把复测设为默认、把 skip 降级为逃生口。⑤ 必须在 ④ 之后，否则 UI 会暴露一个还不能验证的保存按钮。

## 11. 端到端验收

用**完全空的数据根**跑完整条链路：

```text
空目录 → 启动 Runtime → 浏览器自动打开
      → 默认 Stardew definitions 已 seed，config/agent.json 已就位
      → 填写 Provider / Model / API Key → 测试连接通过
      → 保存 → Runtime 从 needs_configuration 变为 ready
      → 重启 Runtime → 配置仍然有效，且不重复 seed
      → 启动 Stardew → UI 从 Waiting for game 变为 Connected
      → 与 NPC 说一句话 → 该 Turn 出现在控制台
```

| 检查 | 期望 |
| --- | --- |
| 权限 | `<root>/secrets/` 为 0700，secret 文件为 0600；权限设置失败时不落盘 |
| 不泄露 | `/api/status` 与所有 setup 响应中不出现 key；进程日志不出现 key |
| seed 幂等 | 第二次启动不新增/不覆盖任何文件；`agent.json` 存在即完全跳过 |
| 显式 override | 设 `GAMEAGENT_AGENT_CONFIG` 时一个默认文件都不写 |
| 状态独立 | 模型不可用但适配器在线时，只显示 Model Connection 失败，Game Connected 保持 Connected |
| MFM 缺失 | 显示"邮件能力不可用"，但 Game Connected 仍为 Connected |
| 重启 | Ready 之后重启，配置与 seed 结果都保持 |

## 12. 明确不做

```text
Ready 之后的热切换 provider/model/key   需要真实的 core replacement 语义，本轮不做
OS 密钥库（DPAPI / Keychain / Secret Service）  记为增强项
运行时热改 agent.json                   Configure() 只重读 model.json
由 Runtime 直接向游戏目录安装 mod        §3.6 明确 Adapter 单独安装，且写游戏目录需单独授权
Background Trigger                      见总方案 §2.6
Mobile / LAN / 远程访问                 见总方案 §3.4
```

## 13. 已核实的代码事实

```text
bootstrap.Configure()     只重读 model.json；真实 core 上重复调用是 no-op（首次配置可用，热切换不可用）
                          模型不可用时装载 unavailableProvider，turn 仍能正常终止
bootstrap.Open()          agent.json 缺失时 LoadConfigFile 返回 DefaultConfig() 且不报错
                          → 空数据根不会 blocked，但会拿到通用基线（无 catalog、max_steps 3）
llm.resolveAPIKey()       只接受 env:；有测试显式拒绝内联 key
agent.LoadConfigFile()    文件不存在时返回默认值，不报错
per-game profile          不存在自动加载机制；只能由 GAMEAGENT_AGENT_CONFIG 显式指定
definition loader         只读 <root>/<game>/definitions/*.json；games/<game>/agent.json 没有任何消费者
dataroot.Layout           secrets/ 由 Ensure() 以 0700 创建
gateway.Server            WorldRegistry() 已导出；ReadyWorlds() 只返回 task.Head，不含能力名
```

## 14. 决策记录

```text
D1  b   env: 保留，新增 file:；相对路径基于 model.json 所在目录
D2  a   明文 + 权限收紧；权限设置失败即保存失败
D3  b   文件系统只做启发式；在线权威判断来自 ready 与能力列表；路径启发式预填、用户可改
D4  a   本轮只做首次 model 配置；不做 Ready 后热切换；默认 UI 不暴露 base_url
D5  a   embed + 首次 seed；锚点为 agent.json；不 merge / 不覆盖 / 不升级；显式 override 时完全不 seed
D6  分级 Runtime Ready / Model Connection / Game Connected 三态独立
D7  a   seed 源是 games/stardew-valley/agent.json，落到 <root>/config/agent.json（§5.2）
        顶层通用 agent.json 只作开发/测试基线，不参与 seed
D8  持久化到 <root>/config/console.json；只写用户显式 override，自动探测结果不写；Core 不读它
3.1 先测试再落盘；保留显式 skip_connection_test 逃生口（§6、§7.1）
```
