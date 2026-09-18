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

测试失败就**不落盘**，用户可以直接改。唯一的例外是离线用户：提供一次显式确认（"不测试直接保存"），并在 UI 上标明该配置未经验证。

## 4. 需要补决的两个结构问题

### D7（阻塞）：seed 的 `agent.json` 用哪一份

**已核实：per-game profile 根本不会被 Runtime 自动加载。** 仓库里没有 profile 解析机制——`agent.LoadConfigFile` 只读一个路径，来源是 `GAMEAGENT_AGENT_CONFIG` 或 `<root>/config/agent.json`。`runtime/config/games/stardew-valley/agent.json` 只是**开发脚本**用 `-AgentConfig` 显式传进去的。

于是两份配置的差别是实打实的：

```text
runtime/config/agent.json                     通用基线：max_steps 3、无 definition_catalog_root、
                                              通用 prompt（"符合当前角色与世界状态的语气"）
runtime/config/games/stardew-valley/agent.json Stardew：max_steps 5、definition_catalog_root
                                              "config/games"、Stardew prompt + tool_instruction
```

缺 `definition_catalog_root` 就不会加载 33 个 NPC definition；`max_steps 3` 也不够一次"工具 → 确认 → 收尾"的回合。所以**seed 哪一份直接决定"配好了"是不是 README 里那个 agent**。

| 选项 | 代价 |
| --- | --- |
| **a（建议）** seed `<root>/config/agent.json` ← **Stardew profile 的内容**；`runtime/config/agent.json` 保留为开发/测试基线，不参与 seed | 语义明确：发行默认就是 Stardew 配置。代价是两个 `agent.json` 角色不同，必须在文档里写清 |
| b seed 通用基线，另加一个"按 game_id 选 profile"的机制 | 那是一次真正的 Runtime 生命周期改动：`game_id` 只在适配器 Hello 之后才知道，profile 必须延迟加载。属于 10.3 的工作 |
| c 只 seed definitions，`agent.json` 用通用基线 | 省事，但 agent 行为与开发环境不一致（无 definition、步数不足），等于交付一个"能跑但不像"的东西 |

**建议 a。**

### D8（小）：用户填的游戏路径是否持久化

Runtime 自己不使用游戏路径，它只服务于依赖体检。选项：

```text
不持久化      每次打开控制台重新探测；探测失败就要用户重填
持久化覆盖值  只在"探测失败/用户改过"时写入控制台自己的设置文件
```

**建议持久化覆盖值**（空值不写），落点 `<root>/config/console.json`，并在文档里标明它属于控制台、Agent Core 不读它。若你更看重"配置文件只由 Core 拥有"，就选不持久化。

## 5. Seed 机制

### 5.1 资产位置（零文件搬迁）

Go 的 `//go:embed` 不能使用 `..`，所以 embed 包必须位于资产目录或其上层。`runtime/config/` 恰好是 `games/**` 与 `agent.json` 的**同一层**，因此直接在该目录加一个 Go 包即可：

```text
runtime/config/defaults.go        package config；//go:embed agent.json games
```

这样不用移动任何现有文件，也不用把包命名成 `runtime`（那会与标准库 `runtime` 同名，在每个同时用 `runtime.GOOS` 的文件里都要别名）。

**embed 只包含要 seed 的两项**：`agent.json`（来自 D7 的选择）与 `games/**`。**不含 `model.json`**——那正是向导要生成的。

已实测该布局可行：把 `package config` 放在 `runtime/config/` 并 embed `agent.json` + `all:games`，可嵌入 **37 个文件**（`agent.json` 加 `games/` 下 36 个），且 `check-architecture.ps1` 仍然通过。

**该文件有一个约束**：`scripts/check-architecture.ps1` 会检查 `runtime/` 下所有 `*.go` 与 `*.json` 是否含游戏专有词（`runtime/config/games/` 被排除，`runtime/config/defaults.go` 不被排除）。所以这个文件里不能出现游戏名，注释也要保持通用措辞。

### 5.2 触发规则：`agent.json` 作为首次初始化锚点

```text
<root>/config/agent.json 不存在  → 判定为 fresh，执行 seed
<root>/config/agent.json 已存在  → 不 seed、不补文件、不 merge、不覆盖
GAMEAGENT_AGENT_CONFIG 已设置    → 完全跳过 seed（显式配置优先，不能背后再塞一套 Stardew 默认值）
```

**为什么必须用单一锚点，而不是"逐个文件检查缺失就补"：** 后者等价于隐式配置升级——未来版本新增一个 definition，老用户一启动它就自动出现了，这与"不升级"直接矛盾。

**落盘顺序：** 先写 `games/**` 与其它文件，**`agent.json` 最后原子落盘**（临时文件 + rename）。中途崩溃时下次仍判定为未初始化，可以重新完成。

### 5.3 seed 什么

```text
✅ <root>/config/agent.json                                     （D7 决定内容）
✅ <root>/config/games/stardew-valley/definitions/**.json
❌ <root>/config/model.json
❌ 任何 secret
```

## 6. 接口

沿用 10.2-2 已有的会话、Host 校验与静态资源机制；新增端点都在 `/api` 下、都需要会话凭证。

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| POST | `/api/setup/model` | 测试（可选）→ 写 secret → 原子写 `model.json` → `Configure()` |
| POST | `/api/setup/test` | 只做连接测试，返回分类结果，不落盘 |
| GET | `/api/setup/environment` | 依赖体检：探测结果 + 用户覆盖值 + 在线权威状态 |
| PUT | `/api/setup/environment` | 保存用户选定的游戏路径（D8 选持久化时才有） |

`GET /api/status` 扩展为三段状态（Runtime / Model Connection / Game Connected）加 seed 结果，仍然**不返回任何 secret**。

约束（沿用 10.2-2 的既有边界）：只监听 loopback、Host 必须回环、`/api` 需要会话、响应不回传凭证、`Cache-Control: no-store`。

## 7. Runtime 侧最小新增面

三个状态里有两条来自 Runtime 之外的信息，需要**只读**访问器（不改变 gRPC、协议、Loop 与调度行为）：

```text
Runtime Ready        已有：bootstrap.Runtime.State()
Model Connection     新增：一条最小 provider 调用，结果只用于显示，不写入 Bootstrap State
Game Connected       新增：gateway 只读世界状态（workspace 绑定是否就绪 + 实体 + 能力名列表）
```

`gateway.Server.WorldRegistry()` 已经是导出方法，但 `ReadyWorlds()` 只返回 `task.Head`，不含能力名。因此需要新增一个只读快照访问器（例如 `WorldStatuses()`，返回世界键、就绪标志、实体 id、能力名），由 `httpapi` 通过窄接口消费——与 10.2-1 的 `coreReadiness` 同一种做法。

## 8. 依赖体检状态矩阵

```text
Stardew installation      Detected / Not found / User selected
GameAgent Adapter         Detected on disk / Unknown
Live connection           Connected / Waiting for game
Mail Framework Mod        Available / Unavailable / Unknown
```

- 前三行里，只有 `Live connection = Connected` 是**权威**的；`Detected` 只表示"目录像那么回事"。
- 第四行只在已连接时才有权威值；未连接时显示 `Unknown`，不得显示"缺失"。
- 游戏路径的启发式来源（Steam 注册表、`libraryfolders.vdf`、常见路径）**只是提示，不是事实源**；实现时逐个验证并记录，找不到就留空让用户填。

## 9. 实现顺序

按依赖从底向上，不从 UI 开始：

```text
① Default seed           空数据根启动后 agent.json + Stardew definitions 自动就位
② Secret storage         env: / file:、0600 与 ACL、权限失败即保存失败、secret 不外泄
③ Model Setup API        写 secret → 原子写 model.json → Configure() → needs_configuration → ready
④ Model connection check 区分 authentication failed / network unavailable / provider error
⑤ First-run UI           Provider / Model / API Key；Save 前先测试
⑥ Stardew diagnostics    启发式路径 + 在线权威状态 + MFM 可选显示
⑦ 空数据根实机验收        见 §10
```

①必须在③之前：③的验收前提是"新用户拿到的是一个能用的默认配置树"。

## 10. 端到端验收

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

## 11. 明确不做

```text
Ready 之后的热切换 provider/model/key   需要真实的 core replacement 语义，本轮不做
OS 密钥库（DPAPI / Keychain / Secret Service）  记为增强项
运行时热改 agent.json                   Configure() 只重读 model.json
由 Runtime 直接向游戏目录安装 mod        §3.6 明确 Adapter 单独安装，且写游戏目录需单独授权
Background Trigger                      见总方案 §2.6
Mobile / LAN / 远程访问                 见总方案 §3.4
```

## 12. 已核实的代码事实

```text
bootstrap.Configure()     只重读 model.json；真实 core 上重复调用是 no-op（首次配置可用，热切换不可用）
                          模型不可用时装载 unavailableProvider，turn 仍能正常终止
bootstrap.Open()          agent.json 缺失时 LoadConfigFile 返回 DefaultConfig() 且不报错
                          → 空数据根不会 blocked，但会拿到通用基线（无 catalog、max_steps 3）
llm.resolveAPIKey()       只接受 env:；有测试显式拒绝内联 key
agent.LoadConfigFile()    文件不存在时返回默认值，不报错
per-game profile          不存在自动加载机制；只能由 GAMEAGENT_AGENT_CONFIG 显式指定
dataroot.Layout           secrets/ 由 Ensure() 以 0700 创建
gateway.Server            WorldRegistry() 已导出；ReadyWorlds() 只返回 task.Head，不含能力名
```

## 13. 待你确认

```text
D7  seed 的 agent.json 用哪个        建议 a：用 Stardew profile 内容，通用 baseline 只留作开发基线
D8  游戏路径覆盖值是否持久化          建议：持久化到 <root>/config/console.json（Core 不读它）
3.1  Save 前必须先测试连接            建议：是；离线用户走一次显式"不测试直接保存"
```
