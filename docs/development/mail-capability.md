# 能力接入：Mod 邮件能力（send_mail）技术开发方案

> **Status:** Proposed — 等待用户确认后开工
> **Date:** 2026-09-18
> **Scope:** 让 agent 自主调用 MailFrameworkMod 的邮件能力，完成"注册信件 → 立即投递 → 玩家读信 → 状态变化"闭环
> **Decision Record:** 立即投递（触发 MFM 命令）；仅文本（无附件、禁止游戏命令）；验收包含人设一致性
> **Related:** [guide.md](guide.md)、[logical-separation.md](logical-separation.md)

---

## 1. 目标与范围

### 1.1 目标

验证一个明确命题：

> **agent 能在没有任何点名提示的情况下，自主选择并成功执行一个由第三方 mod 提供的能力，并在真实游戏里产生可观察、可验收的效果。**

这条链路同时验证三件事：Adapter 能把外部 mod 能力包装成 Capability；Runtime 能把它注册成 Tool；模型能自主选择它。

### 1.2 本轮做

```text
能力 send_mail(title?, body)
仅文本          无附件、无 recipe、无 AutoOpen
立即投递        通过 MFM 自身的投递入口
禁止游戏命令    对模型文本做 token 校验
验收链路        机制 → 游戏 → 人设三层
```

### 1.3 本轮不做

```text
不做附件 / 物品    附件会把"送信"变成"凭空发物品"，需要单独授权
不做 recipe 学习   同上
不做 AutoOpen     跳过信件 UI 会削弱证据强度
不做回信 / ReplyConfig
不做 content pack 不通过写 mail.json 的方式接入（见 §4.3）
不改协议            action capability 与 arguments 已是通用结构
不改 Runtime       Runtime 生产代码不含任何游戏工具名分支
```

---

## 2. 已核实的机制事实

以下全部来自 MFM 1.20.0 的源码与 DLL 反射，不是推测。

### 2.1 对外 API

`MailFrameworkModEntry.GetApi()` 返回 `MailFrameworkModApi`，实现 `IMailFrameworkModApi`：

```text
Void    RegisterContentPack(IContentPack contentPack)
Void    RegisterLetter(ILetter iLetter, Func<Letter,bool> condition, Action<Letter> callback, Func<Letter,List<Item>> dynamicItems)
ILetter GetLetter(String id)
String  GetMailDataString(String id)
```

`ILetter` 的可写属性（用于构造模型驱动的信件）：

```text
Id, Text, Title, GroupId, Items, Recipe, WhichBG,
LetterTexture, TextColor, UpperRightCloseButtonTexture, AutoOpen, I18N
```

### 2.2 投递机制

```text
被动：GameLoop.DayStarted  → MailController.UpdateMailBox()
主动：控制台命令 player_debug_updatemailbox → MailController.UpdateMailBox()
```

`MailController.UpdateMailBox()` 的文档注释是 **"Call this method to update the mail box with new letters."**——它是公开的、被设计为可调用的。这正是本轮"立即投递"采用的入口。

投递后的读信链路（Harmony patch `GameLocation.mailbox`）：

```text
UpdateMailBox()  → 向 Game1.player.mailbox 插入占位符 MailFrameworkPlaceholderId
玩家走近信箱      → mailbox_prefix 拦截 → MailController.ShowLetter() 打开 LetterViewerMenu
玩家关闭信件      → OnMenuClose → 执行 callback → Game1.player.mailReceived.Add(letter.Id)
```

### 2.3 约束

| 约束 | 证据 | 影响 |
| --- | --- | --- |
| `condition` **必需**，不能为 null | `MailRepository.GetValidatedLetters()` 直接调用 `l.Condition(l)`，无 null 检查；异常被 catch 并静默忽略该信 | 必须提供一个返回 true 的条件 |
| `Id` 不能为 null 且会移除空格 | `Letter.Id` 的 setter 执行 `value.Replace(" ","")` | 必须给非空、去空格的稳定 id |
| 同 id 注册是**替换**而非累积 | `MailRepository.SaveLetter` 对已存在 id 执行 `Letters[index] = letter` | 重复调用不会无限堆积；但同 id 会覆盖旧信 |
| `Text` 进入游戏 token 解析器 | `Letter.TranslatedText => TokenParser.ParseText(...)` | **注入面，见 §3** |
| 信件必须被玩家打开才产生终止状态 | `callback` 只在 `OnMenuClose` 触发 | 不读信就没有 `mailReceived` 记录 |

### 2.4 可观察状态（决定怎么验收）

```text
已注册      MailRepository 中存在该 id
已投递      Game1.player.mailbox 含占位符；MailController.HasCustomMail() == true
已读        Game1.player.mailReceived 含 letter.Id（callback 写入）
```

前两项可在没有玩家配合时确认；第三项必须由玩家读信。

---

## 3. 最重要风险：文本注入

`Letter.TranslatedText` 会把信件文本交给游戏的 `TokenParser.ParseText`，而 `mail.json` 的官方注释明确写着：

> You can use @ to put the players name and ^ for line breaks. **You can also use the base game commands to add money, items and stuff.**

也就是说 **`Text` 是游戏命令的载体**。如果模型产出的文本里出现命令或 token 语法，就会变成对游戏状态的修改——在本轮"仅文本"的约束下，这是**唯一仍然存在的副作用通道**。

**因此 `send_mail` 必须在 Adapter 侧做输入校验**，至少覆盖：

```text
拒绝或转义   ^           换行控制
拒绝或转义   @           玩家名替换
拒绝         [ ] 形式的 token / 游戏命令
限制         长度上限（与信件 UI 容量对齐）
限制         非可打印字符
```

校验失败必须以 `REJECTED` 返回明确 code（例如 `mail_body_invalid`），**不得把未经校验的文本写入 `Text`**。

这条不是"安全加分项"，而是本轮能否算"仅文本"的前提。

---

## 4. 工作模块

### 4.1 能力与注册

**改动**：

- 新增 `send_mail` 到 `CapabilityCatalog`：`ExecutionMode.Sync`，`ConcurrencyMode.Sequential`，description 说明"给玩家信箱投递一封由 NPC 撰写的信"。
- 输入 schema：`body`（必填，string，maxLength 上限）、`title`（可选）。
- 在 `RuntimeClient` 的执行分派中接入该能力。

**验收**：

- `CapabilityList` 的 Stardew 静态检查与 `CapabilityCatalog` 测试同步更新并通过。
- 模型可见的工具清单包含 `send_mail`，schema 正确。

### 4.2 MFM 集成层（可选依赖）

**关键设计：MFM 是第三方可选 mod，不能让 adapter 产生硬依赖。**

理由：若 adapter 直接引用 `MailFrameworkMod.dll`，未安装该 mod 的用户会让 adapter 在加载/运行时崩溃。

**做法**：新增独立的 mod 集成模块，通过反射在运行时解析 MFM 的类型与方法：

```text
Mods registry:   helper.ModRegistry.GetApi("DIGUS.MailFrameworkMod")
类型解析:        按名字找 ILetter / Letter 构造与属性
调用:            在主线程上构造信件并 RegisterLetter
```

**必须满足**：

- MFM 未安装时，adapter 正常加载，`send_mail` 不进入 Tool View，或调用时以明确 code（例如 `mail_framework_unavailable`）REJECTED。
- 所有 MFM 调用都在游戏主线程（`MainThreadDispatcher`）上执行。
- MFM 调用抛异常时必须转成 `failed` ActionResult，带明确 code，不吞掉、不崩溃。

### 4.3 为什么不用 content pack 接入

MFM 的主流用法是内容包：写 `mail.json` + `manifest.json`（`ContentPackFor: DIGUS.MailFrameworkMod`）静态声明信件。本方案**不采用**，原因：

- 内容包是**静态数据**，而本轮需要模型在运行时产出信件正文；
- 写入用户 Mods 目录属于修改用户环境，超出 `send_mail` 的语义；
- 运行时 `RegisterLetter` 已足够，且不污染用户目录。

### 4.4 立即投递

**改动**：注册成功后触发 MFM 的投递入口。

**首选**：触发 MFM 注册的控制台命令 `player_debug_updatemailbox`（SMAPI `ICommandHelper`，需在主线程执行）。
**备选**：若命令触发不可行，反射调用 `MailController.UpdateMailBox()`。

**验收**：调用后 `HasCustomMail()` 为 true，且 `Game1.player.mailbox` 出现占位符。

### 4.5 校验与结果

**改动**：实现 §3 的文本校验；构造 ActionResult 时区分：

```text
succeeded                    信件已注册并投递
REJECTED mail_body_invalid   文本未通过校验
REJECTED mail_framework_unavailable  MFM 未安装
failed   mail_register_failed / mail_delivery_failed  并带原因
```

---

## 5. 验收

### 5.1 三层证据

| 层 | 证据 | 是否需要玩家 |
| --- | --- | --- |
| 机制 | 自动化测试：文本校验规则、幂等注册、MFM 缺失时的 REJECTED 路径 | 否 |
| 游戏 | SMAPI 日志出现注册/投递记录；`HasCustomMail() == true`；`mailbox` 含占位符 | 否 |
| 闭环 | 玩家走近信箱读到信；`mailReceived` 含 letter.Id | 是 |

**明确不采用**的替代证据：

- 用 `AutoOpen=true` 让信件不显示就写入状态——它证明的是"我们写了数据"，不是"agent 送出了一封信"。
- 用 `player_addreceivedmail` 直接写 `mailReceived`——那绕过了投递机制，验证的就不是我们的能力。

### 5.2 人设一致性验收（本轮决策要求）

链路打通后，需要在同一 NPC 上做多轮真实模型采样，验证信件内容与角色定义一致：

```text
方法        同一 NPC（含 AgentDefinition 的角色）× 不同场景/玩家输入，各生成一封
检查项      署名与身份一致、语气与 speech_style 一致、内容与该角色关系状态一致、无越界承诺
判定        逐条人工评判并记录；不达标样本保留原文与判定理由
```

样本数量与判定标准在开工前与用户确认，避免"跑通了就算合格"。

### 5.3 验证命令

```powershell
$game = "D:\data\project\game-agent\Stardew Valley"

go test ./... -count=1
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --configuration Debug -p:GamePath="$game"
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
git diff --check
```

### 5.4 实机步骤

1. 确认 `Mods/MailFrameworkMod` 与 `Mods/GameAgentStardew` 均加载，SMAPI 日志无报错。
2. 加载存档，触发一次 NPC 对话，使 agent 有自主决策机会。
3. 观察 SMAPI 日志：`send_mail` 是否被模型自主选择（不是被 prompt 点名）。
4. 确认信件已投递（`HasCustomMail()` / 日志）。
5. 走近信箱读信，确认文本与模型输出一致、无 token 被解析。
6. 确认 `mailReceived` 含 letter.Id。

---

## 6. 未确认项（开工后需实机确认）

| # | 未知 | 影响 | 计划 |
| --- | --- | --- | --- |
| 1 | `RegisterLetter` 的 API 内部实现（该文件本轮未取到） | 无法确认为 null 的 callback/dynamicItems 是否安全 | 传非 null 空实现，规避不确定性 |
| 2 | 运行时 `RegisterLetter` 在存档加载后是否立即可投递 | 决定立即投递是否成立 | 第 1 个模块就用日志验证 |
| 3 | `player_debug_updatemailbox` 能否由 adapter 触发 | 决定立即投递用命令还是反射 | 命令不可行则走 `MailController.UpdateMailBox()` |
| 4 | `TokenParser` 对 `^`/`@`/`[]` 的确切行为 | 决定校验是拒绝还是转义 | 以实机验证为准，保守先拒绝 |

`AutoOpen=false` + 无附件 + 无 recipe + 非 null condition：这四点使信件走标准 UI 路径，是本轮设计的基础。

---

## 7. 退出条件

1. `send_mail` 进入 Tool View，且 MFM 未安装时以明确 code REJECTED、adapter 不崩溃。
2. 模型在无点名提示下自主选择 `send_mail`（有 SMAPI 日志证据）。
3. 信件成功投递，玩家可读，`mailReceived` 记录 letter.Id。
4. 文本校验有自动化测试覆盖，注入样本被拒。
5. 人设一致性按 §5.2 完成一轮采样与判定。
6. §5.3 全部通过；`check-architecture.ps1` 的 game-agnostic 断言未被削弱。
7. 协议与 Mod 运行时标识未改动。
