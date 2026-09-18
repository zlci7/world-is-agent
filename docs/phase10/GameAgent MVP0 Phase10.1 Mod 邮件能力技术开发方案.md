# GameAgent MVP0 Phase10.1 Mod 邮件能力技术开发方案

> **Status:** Implementation Plan Draft — 等待用户确认后开工
> **Date:** 2026-09-18
> **Phase:** Phase10.1 Mod 能力接入与自治调用验证
> **目标:** 让模型在一个已触发的 AgentTurn 内自主从 Tool View 选中并执行由第三方 mod 提供的 `send_mail`，完成"注册信件 → 立即投递 → 玩家读信 → 状态变化"闭环
> **Scope:** 首个第三方 mod 能力接入；仅文本、无附件、禁止游戏命令；只验证回合内工具选择，不含自主触发
> **Decision Record:** 立即投递（反射 `MailController.UpdateMailBox()`，无备选路径）；接入方式为**本地 API contract + `GetApi<T>`**，反射只留给投递，接口桥接由开工第一个探针验证；`send_mail` 仅在 MFM 可用时发布；安全边界由 §3 **白名单**输入校验承担（title 与 body 同为注入面），校验先于任何 MFM 调用；`mailReceived` 由 callback 显式写入；验收包含人设一致性；**能力默认直接执行，不引入"需确认"字段**（当前没有向玩家请求确认的交互机制，该字段没有执行点）。
> **Protocol Baseline:** `gameagent.protocol.v1alpha2`（本方案不改协议）
> **Parent Plan:** [Phase10 技术开发与验收总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md)
> **Related:** [Mod 能力接入规范](../development/mod-capability-integration.md)、[开发指南](../development/guide.md)

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
立即投递        反射 MailController.UpdateMailBox()
白名单校验      对模型文本做字符级白名单，先校验后调用
条件发布        仅 MFM 可用时进入 Tool View
验收链路        机制 → 游戏 → 人设三层
```

### 1.3 本轮不做

```text
不做附件 / 物品    附件会把"送信"变成"凭空发物品"，需要单独授权
不做 recipe 学习   同上
不做 AutoOpen     跳过信件 UI 会削弱证据强度
不做回信 / ReplyConfig
不做 content pack 不通过写 mail.json 的方式接入（见 §4.3）
不做自主触发       只验证回合内工具选择；agent 不会主动起意写第一封信，
                   后续路线见 Phase10 总方案 §2.6
不做读类能力       非硬验收；MFM 若无可低成本可读状态则整体留作 follow-up
不改协议            action capability 与 arguments 已是通用结构
不改 Runtime       Runtime 生产代码不含任何游戏工具名分支
```

---

## 2. 已核实的机制事实

以下全部来自 MFM 1.20.0 的源码与 DLL 反射，不是推测。

### 2.1 对外 API

`MailFrameworkModEntry.GetApi()` 返回 `MailFrameworkModApi`，实现 `IMailFrameworkModApi`。以下签名由反射本机 MFM 1.20.0 得到，**委托的泛型实参是 `ILetter`，不是 `Letter`**：

```text
Void    RegisterContentPack(IContentPack contentPack)
Void    RegisterLetter(ILetter iLetter, Func<ILetter,bool> condition, Action<ILetter> callback, Func<ILetter,List<Item>> dynamicItems)
ILetter GetLetter(String id)
String  GetMailDataString(String id)
```

`Letter`（具体模型）与 `ILetter`（接口）的委托类型不同，混用会编译不过，必须分清：

```text
MailFrameworkMod.Api.ILetter     public interface   属性全部 {get;}
MailFrameworkMod.Api.ApiLetter   public class       属性全部 {get;}，唯一构造函数 ApiLetter(Letter letter)
MailFrameworkMod.Letter          public class       属性全部 {get;set;}，无无参构造

Letter.Condition / Callback      Func<Letter,bool> / Action<Letter>       ← 具体模型那一版
IMailFrameworkModApi 的委托       Func<ILetter,bool> / Action<ILetter>     ← API 那一版，本方案使用
```

**本方案不构造 MFM 的 `Letter`，也不用反射造委托。** Adapter 自己声明一份与 MFM 同形的接口契约（§4.2），用自己的 DTO 实现 `ILetter` 直接传给 `RegisterLetter`。这样两个委托都是编译期可见的普通 lambda，不需要在运行时用 `System.Linq.Expressions` 构造泛型委托。

### 2.2 投递机制

```text
被动：GameLoop.DayStarted  → MailController.UpdateMailBox()
主动：控制台命令 player_debug_updatemailbox → MailController.UpdateMailBox()
```

`MailController.UpdateMailBox()` 的文档注释是 **"Call this method to update the mail box with new letters."**——它是公开的、被设计为可调用的。这正是本轮"立即投递"采用的入口。

已验证的 `MailController` 公开成员：

```text
MailFrameworkMod.MailController        public class
  static Void     UpdateMailBox()            投递入口
  static Boolean  HasCustomMail()            投递证据（§5.1 使用）
  static Void     UnloadMailBox()
  static Void     UnloadLetterMailbox(String id)
  static Void     ShowLetter()
```

**一个必须澄清的机制事实：`player_debug_updatemailbox` 无法由 adapter 触发。** SMAPI 4.3.2.0 的 `ICommandHelper` 只有 `Add` 一个公开方法，不存在任何"触发其它 mod 注册的命令"的公开接口（`MailFrameworkMod.Commands.DebugUpdateMailbox` 只是该命令的实现体，不改变这一点）。因此立即投递只能直接调用上表的 `UpdateMailBox()`，详见 §4.4。

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
| 同 id 注册是**替换**而非累积 | `MailRepository.SaveLetter` 对已存在 id 执行 `Letters[index] = letter` | 同 id 会覆盖旧信，因此 id 必须唯一，规则见 §4.1 |
| `condition` 是 API 路径**唯一**的重复投递守卫 | `Letter` **没有** `Repeatable` 属性；`Repeatable` 只存在于 content pack 的 `MailItem` | 内容包路径由 MFM 做"已投递"检查，API 路径没有；恒返回 true 会导致每天重复投递，见 §4.2.2 |
| `Text` 与 `Title` 都进入游戏 token 解析器 | `Letter.TranslatedText` 与 `Letter.TranslatedTitle` 都调用 `TokenParser.ParseText(...)` | **注入面，两个字段都要校验，见 §3** |
| 信件必须被玩家打开才产生终止状态 | 终止状态只在信件关闭路径上产生 | 不读信就没有 `mailReceived` 记录 |
| API 注册的 id 会被去空格改写 | `Letter.Id` setter 执行 `value.Replace(" ", "")`（MFM 内含 `Replace`） | id 里不能含空格，否则查不回自己，见 §4.1 |

### 2.4 可观察状态（决定怎么验收）

```text
已注册      MailRepository 中存在该 id
已投递      Game1.player.mailbox 含占位符；MailController.HasCustomMail() == true
已读        Game1.player.mailReceived 含 letter.Id（信件关闭路径写入，写入方见 §6）
```

前两项可在没有玩家配合时确认；第三项必须由玩家读信。

---

## 3. 最重要风险：文本注入

`Letter.TranslatedText` 会把信件文本交给游戏的 `TokenParser.ParseText`，而 `mail.json` 的官方注释明确写着：

> You can use @ to put the players name and ^ for line breaks. **You can also use the base game commands to add money, items and stuff.**

也就是说 **`Text` 是游戏命令的载体**（`Title` 同理，见下）。如果模型产出的文本里出现命令或 token 语法，就会变成对游戏状态的修改——在本轮"仅文本"的约束下，这是**唯一仍然存在的副作用通道**。

**因此 `send_mail` 必须在 Adapter 侧做输入校验。校验采用白名单，不采用黑名单。**

黑名单在这里必然不完整：`Text` 最终交给游戏的 `TokenParser`，而**我们没有它的完整语法**。方案早期版本只列了 `^ @ [ ]` 三个字符，但原版 `Data/mail` 的命令语法使用 `%` 前缀，该前缀是否也在解析链上尚未验证。凡是"猜哪些输入危险"的做法，都会漏掉没想到的那一类。

**`title` 与 `body` 是同一个注入面，两者都必须校验。** MFM 的 `TranslatedTitle` 与 `TranslatedText` **都**调用 `TokenParser.ParseText`（本机 1.20.0 符号表中 `get_TranslatedTitle` 与 `TokenParser` 同时存在）。只校验正文，会留下一个完全等价的未校验命令通道。

白名单必须是**封闭集合**，不能写成"允许可打印字符、再拒绝几个特殊字符"——后者仍然是黑名单，因为 `$ # { } < > \ | ~ `` 等会全部放行，而这恰恰是本方案开头说不能依赖的做法。

先做规范化，再做白名单判定，两步顺序不能反（规范化的理由见下）。

判定规则（两个字段共用，只有 `^` 与长度不同）：

```text
允许    Unicode 字母、数字、文字（含汉字）
允许    普通空格
允许    明确列出的 ASCII 标点：, . ! ? ; : ( ) - ' "
允许    明确列出的全角标点：，。！？、；：（）《》〈〉「」『』…—""''
允许    @                    两个字段都限次
允许    ^                    仅 body，限次；title 按单行处理
拒绝    其余一切字符          默认拒绝，含 [ ] % $ # { } < > \ | ~ ` 等
限制    长度上限              title 较短，body 与信件 UI 容量对齐
```

实现必须写成"**在 safe set 里才放行**"，而不是"不在 deny set 里就放行"。ASCII 标点要逐个参数化测试：

```text
! " # $ % & ' ( ) * + , - . / : ; < = > ? @ [ \ ] ^ _ ` { | } ~
```

只有明确列进安全集的才通过，其余一律返回 `REJECTED`。**只要一个字符没有被显式允许，它就不该通过**——这就是白名单与黑名单的区别。

**换行必须被规范化，不能拒绝。** 模型写正文时会自然地输出真实换行符，如果一律拒绝，几乎每封真实信件都会失败——而模型并不知道 MFM 用 `^` 表示换行。Adapter 的职责正是这层翻译：

```text
body   先将 \r\n 与 \r 归一为 \n，再把每个 \n 转换为 ^，然后按白名单校验并统计 ^ 数量
title  换行归一为单个空格（标题是单行），再按白名单校验
```

这不增加攻击面：`^` 本来就在允许集内、本来就有次数上限，规范化只是把同一种语义的另一种写法映射过去。规范化之后，**任何残留的控制字符都直接拒绝**。

**Unicode 先折叠为组合形式（FormC），再做白名单判定。** `café` 可以写成预组合的 `é`（U+00E9），也可以写成 `e` + 组合重音（U+0301）。组合重音既不是字母也不是允许的标点，若不先折叠，同一段文本会因为编码形式不同而出现一个通过、一个被拒。

完整顺序固定为：**FormC 折叠 → 去首尾空白 → 换行规范化 → 长度与计数上限 → 白名单判定**。判定与输出使用同一份规范化结果，避免"校验的是一种形式、写进去的是另一种"。

> 换行这一条是实机验证收口时才发现的：方案早先写的是"拒绝裸换行字节"，那会与"模型写正文"这件事直接冲突。宁可让 Adapter 做翻译，也不要让模型去猜 MFM 的换行语法。

**为什么 `^` 和 `@` 是允许而不是拒绝**：两者都是排版能力，不是副作用通道（一个换行、一个替换玩家名）。全部拒绝会让每封信挤成一行，直接损害 §5.2 的人设一致性验收。允许并限次，而不是拒绝。

校验失败必须以 `REJECTED` 返回明确 code（`mail_body_invalid` / `mail_title_invalid`），**不得把未经校验的文本写入 `Text` 或 `Title`**。

这条不是"安全加分项"，而是本轮能否算"仅文本"的前提。

**注意：不引入 `requires_player_confirmation` 这类字段。** 当前没有向玩家请求确认的交互机制，字段没有执行点，加了也只是无人消费的元数据。本轮的安全边界完全由上面的输入校验承担，并遵守 [Mod 能力接入规范](../development/mod-capability-integration.md) §3.4 与 §3.7。

---

## 4. 工作模块

### 4.1 能力与注册

**改动**：

- 新增 `send_mail` 到 `CapabilityCatalog`：`ExecutionMode.Sync`，`ConcurrencyMode.Sequential`，description 说明"给玩家信箱投递一封由 NPC 撰写的信"。
- 输入 schema：`body`（必填，string，maxLength 上限）、`title`（可选）。
- 在 `RuntimeClient` 的执行分派中接入该能力。

**信件 id 必须绑定 Action，不能留给实现时随手决定**：

`MailRepository` 对同 id 是**替换**，不同 id 会持续累积；两种极端都不可接受——固定 id 会让玩家永远只看到最后一封，纯随机 id 会让仓库无限增长且无法在验收时核对。

更要紧的是：**Repository 的同 id 替换，不等于投递队列去重。** `MailController` 自己还持有一份待投递信件对象。`RegisterLetter(A)` 之后重试 `RegisterLetter(A')`，Repository 里 A 被 A' 替换，但 A 与 A' 已经是两个对象，**队列里可能已经排了两封**。

因此 id 绑定到 `ActionRequest.action_id`，**只用它，不拼 NPC 名**：

```text
wia.{action_id}
```

`action_id` 本身已是形如 `act_<UnixNano>_<counter>` 的 ASCII 稳定字符串，天然适合做 MFM 的 Id。**不要再拼 `npc_entity_id`**：它是 `"npc:" + npcName`，而 NPC 名可以含空格（例如 `Mr. Qi`），MFM 的 `Letter.Id` setter 会执行 `value.Replace(" ", "")`，于是存进去的 id 与我们用来查的 id 不一致，Action 级幂等就被 MFM 自己的 Id 规范化绕过去了。NPC 归属关系本来就在 `ActionRequest.entity_id`、trace 和 turn 里，不需要塞进信件 id。

同一个 Action 重放必须复用同一个 mail id；**且注册前先判断该 id 是否已注册，已存在则不再注册**。不使用随机数，也不使用会在重试时漂移的序号。

**注册判据不能依赖 `api.GetLetter(id) != null`。** MFM 的 `GetLetter` 是 `new ApiLetter(MailRepository.FindLetter(id))`，即使 `FindLetter` 返回 null，它仍然给出一个非 null 的 `ApiLetter` 包装（属性访问时才会失败）。所以这个判据在信件不存在时也恒为真。改为由 `MailFrameworkIntegration` 自己维护**已注册 id 的簿记**，对外只暴露 `IsRegistered(mailId)`：MFM 的仓库是它的实现细节，Adapter 不该依赖它的空值语义，RuntimeClient 更不该理解它。

**"已注册"不等于"已完成"，投递必须每次都尝试。** 两者的失败是独立的：

```text
已注册？
    yes → 跳过 RegisterLetter
    no  → RegisterLetter
无论哪一支，都要执行 RequestDelivery()
```

否则一次 `mail_delivery_failed` 的 Action 重试会因为"仓库里已经有了"而直接返回 succeeded，Action 就永久卡在未投递状态。**幂等只应消除重复注册，不应消除投递重试。**

**分派位置（选错会重复发送 ActionResult）**：

`RuntimeClient` 现有两种分派写法，语义不同：

```text
if + return     处理器自己 SendActionResult（present_dialogue / resolve_meeting）
switch 返回值    由外层统一 SendActionResult（emote / face_player）
```

`send_mail` 是 Sync、无 continuation，**必须走后者**。

**能力发布方式（本节冻结，不留"或"）**：

`send_mail` **只在 MFM 可用时进入 CapabilityList**；MFM 不可用时能力不发布，模型看不到它。调用侧的 `mail_framework_unavailable` 分支仍要实现，但它是防御路径而非主路径。

采用条件发布而不是"始终发布、调用时拒绝"的理由有两层：其一，"能力暴露什么，模型就会尝试什么"，让模型看不见一个当前无法执行的工具，比让它先选中再被拒绝更干净；其二，条件发布的先例已存在于同一个方法（`BuildEnvironmentCapabilities` 的 `includeTaskCapabilities` 参数），不需要新机制。

**验收**：

- `CapabilityList` 的 Stardew 静态检查与 `CapabilityCatalog` 测试同步更新并通过。
- 模型可见的工具清单包含 `send_mail`，schema 正确。
- MFM 不可用时 `send_mail` 不在工具清单中。

**可用性检查的时点（正式实现必须遵守）**：

`GetApi<T>` 只能在所有 mod 初始化完成之后调用，所以在 `Entry()` 里解析并永久缓存 unavailable 是错的——那一刻 MFM 的 API 还没注册完。正确顺序是把它挂到 `GameLaunched`：

```text
Entry
    ↓  只注册事件与命令，不做 MFM 解析
OnGameLaunched
    ↓  先 Resolve MailFrameworkIntegration
    ↓  再 StartRuntimeClient
Runtime 请求 CapabilityList
    ↓
按 integration != null 条件发布 send_mail
```

`ModEntry` 现在本就是 `OnGameLaunched → StartRuntimeClient()`，把解析插在 `StartRuntimeClient()` 之前即可，不需要新的事件钩子。探针已按这个时点工作（§4.2.3），正式实现照搬。

### 4.2 MFM 集成层（可选依赖）

**关键设计：MFM 是第三方可选 mod，不能让 adapter 产生硬依赖。**

理由：若 adapter 直接引用 `MailFrameworkMod.dll`，未安装该 mod 的用户会让 adapter 在加载/运行时崩溃。

**做法：本地 API contract + `GetApi<T>`，反射只留给立即投递。**

Adapter 在 `Integrations/MailFramework/` 下自己声明一份与 MFM 同形的契约，**不引用 DLL**：

```text
Integrations/MailFramework/
    IMailFrameworkModApi.cs      与 MFM 同形的接口声明
    ILetter.cs                   与 MFM 同形的信件接口声明
    MailLetter.cs                自己的 DTO，实现上面的 ILetter
    MailFrameworkIntegration.cs  取 API、注册、投递、可用性判断
```

取 API 并注册：

```csharp
IMailFrameworkModApi? api = helper.ModRegistry.GetApi<IMailFrameworkModApi>("DIGUS.MailFrameworkMod");

api.RegisterLetter(
    letter,                                        // MailLetter，I18N 恒为 null，见 §4.2.1
    condition:    letter => !Game1.player.mailReceived.Contains(letter.Id),
    callback:     letter => { /* 见 §4.2.1 */ },
    dynamicItems: _ => new List<Item>());
```

SMAPI 的 `GetApi<T>` 会把 MFM 的 API 映射到本接口（"mapped to a given interface which specifies the expected properties and methods"，不兼容时返回 `null`）。映射由 **Nanoray.Pintail** 实现（本机 SMAPI 4.3.2.0 内含 `Nanoray.Pintail` 与 `IProxyManager` / `ObtainProxy`；注意不是 Castle DynamicProxy，早期版本的本方案把这一点写错了）。

这条路线不是本方案的新发明，有两重现成依据：SMAPI 自 3.14.0 起就支持把**自定义 interface 作为入参**；MFM 作者自己的 CustomCaskMod 正是用"本地声明 `IMailFrameworkModApi` / `ILetter` + 自建 `ApiLetter` + `GetApi<T>`"的同一模式调用 MFM。探针因此只用来确认当前 **SMAPI 4.3.2 + MFM 1.20.0 + WIA** 这个具体组合，而不是验证一个未经验证的设想。

`condition` 必须非 null 且**不能恒真**：`MailRepository.GetValidatedLetters()` 直接调用它且没有 null 检查（§2.3），而 MFM 在 API 注册路径上不做"已投递"检查，恒真会让同一封信每天重复投递（§4.2.2）。

`callback` 同样不能是空实现，它承担 `mailReceived` 的写入（§4.2.1）。

**仍然需要反射的只有立即投递与投递证据**——它们是静态方法，无接口可映射：

```text
MailFrameworkMod.MailController.UpdateMailBox()     立即投递
MailFrameworkMod.MailController.HasCustomMail()     投递证据
```

定位这两个方法**不能**用 `api.GetType().Assembly`：`api` 是 Pintail 生成的映射代理，它的程序集里没有 MFM 的类型。要用非泛型的 `helper.ModRegistry.GetApi(uniqueId)` 拿到未代理的对象，再取其程序集；找不到时回退为扫描已加载程序集。

#### 4.2.1 I18N 必须为 null，callback 必须写入 mailReceived

**`ILetter.I18N` 必须返回 `null`。** MFM 解析标题与正文的方式是：

```text
TranslatedText  => TokenParser.ParseText(I18N != null ? I18N.Get(Text)  : Text)
TranslatedTitle => TokenParser.ParseText(I18N != null ? I18N.Get(Title) : Title)
```

一旦传入 translation helper，MFM 就把**模型生成的动态文本当成 i18n key 去查**。而 SMAPI 查不到 key 时不会原样返回，它返回自己的占位符：

```text
Translation.PlaceholderText = "(no translation:{0})"      ← 本机 SMAPI 4.3.2.0 字面量已确认
```

也就是说玩家会读到 `(no translation:GameAgent mail bridge probe...)`，而不是模型写的那段话。**本轮所有信件正文都是动态文本，不是 translation key**，因此：

```csharp
public ITranslationHelper? I18N => null;      // 恒为 null，永远不要传 helper
```

`Title` 可选这一点让问题更严重：`Title == null` 且 `I18N != null` 时，MFM 会拿 `null` 去查 key，本身就是未定义行为。

> 注意这一条逃过了第一轮实机探针的检查：探针证明了注册、投递与回调都成立，**却没有证明"玩家看到的是我们传进去的文本"**。这是两件不同的事，而后者要到 §5.4 真正看一眼信件内容才算验证。

**`callback` 必须显式写入最终的 `letter.Id`。** MFM **不会**替你写入。它的实际行为是：

```text
投递时        mailReceived 临时加入 letter.Id + ValidTagSuffix
玩家关信时    letter.Callback?.Invoke(letter)
              mailReceived.Remove(letter.Id + ValidTagSuffix)
```

临时的带后缀标记会被移除，**最终记录必须由 callback 自己写入**，否则 §5.1 的"已读"证据与 §4.2.2 的防重复投递都不成立。（`ValidTagSuffix` 已在本机 MFM 1.20.0 的符号表中确认存在。）

```csharp
callback: letter =>
{
    if (!Game1.player.mailReceived.Contains(letter.Id))
        Game1.player.mailReceived.Add(letter.Id);
}
```

这与"用调试命令直接写 `mailReceived`"不是一回事：callback 是 MFM 自己的信件完成钩子，只在玩家真正关掉那封信之后才被调用（边界说明见 §5.1）。

#### 4.2.2 condition 必须防重复投递（P0）

**`condition: _ => true` 会让玩家每天重新收到同一封信。**

`MailRepository` 里的 Letter **不会被删除**，而 `UpdateMailBox` 在每次 `DayStarted` 都会重新跑一遍 `condition`。所以读完信之后：

```text
Repository 里仍有这封信
condition 仍然返回 true
→ 再次投递
```

关键证据：`Letter` **没有** `Repeatable` 属性（只有 Id / Text / Items / Recipe / Condition / Callback 等），`Repeatable` 是 content pack `MailItem` 的字段。也就是说**内容包路径由 MFM 自己做"已投递"检查，而 API 注册路径没有——`condition` 是唯一的守卫**。MFM 作者的 CustomCaskMod 用的正是：

```csharp
condition: letter => !Game1.player.mailReceived.Contains(letter.Id)
```

因此本方案固定为：

```csharp
condition: letter => !Game1.player.mailReceived.Contains(letter.Id),
callback:  letter => { if (!Game1.player.mailReceived.Contains(letter.Id))
                           Game1.player.mailReceived.Add(letter.Id); }
```

语义才闭合：

```text
未读  → condition = true  → 可投递
读完  → callback 写入最终 id
之后  → condition = false → 永不重复投递
```

`check-context-static.ps1` 已加断言禁止 `condition: _ => true` 回归。

#### 4.2.3 开工第一个提交必须是接口桥接探针

已经有 SMAPI 自 3.14.0 起的自定义 interface 入参支持、以及 MFM 作者 CustomCaskMod 的同类实践作依据（§4.2），但**"当前这套 SMAPI 4.3.2 + MFM 1.20.0 + WIA 组合"仍需要一个实证**。因此 10.1 的第一个提交是最小探针：注册一封固定文本的信并投递，只验证接口映射与参数桥接成立，不掺其它改动。

**探针不通过再设计退路**，不提前细化：

```text
若 GetApi<T> 与参数桥接不成立：
改为全反射调用 MFM API，按 RegisterLetter 的运行时参数类型
构造 ILetter 与 Func<ILetter,bool> / Action<ILetter> / dynamicItems。
具体实现由探针的失败结果决定——提前设计容易把 Letter 与 ILetter 又混一次。
```

**已实现并通过实机验证**（`src/Integrations/MailFramework/` + `src/Diagnostics/StardewMailProbe.cs`）。运行、读信、再运行构成完整证据：

```text
第一次运行   resolve ok / register ok / deliver ok / has_custom_mail True / verdict bridge_ok
             register detail 打印出传入的 title 与 body 原文
读信之后     玩家确认信件正文就是该原文，未出现 "(no translation:...)"
             callback_fired  detail=wia.probe.bridge
第二次运行   reset ok（清掉探针自己的标记）→ register ok / deliver ok
             has_custom_mail True / verdict bridge_ok
再读信再运行 mail_received=True → has_custom_mail False → verdict bridge_ok_already_read
```

结论：

```text
接口映射成立      GetApi<IMailFrameworkModApi> 由 Nanoray.Pintail 映射成功
参数桥接成立      我方 MailLetter DTO 与 lambda 委托跨过边界（register=ok）
内容传递成立      玩家看到的是传入的原文；I18N=null 生效，见 §4.2.1
回调反向桥接成立  MFM 以它的 ILetter 回调我方 Action<ILetter>（callback_fired）
mailReceived 写入 callback 体执行了 Add(letter.Id)
防重复投递生效    读信后再运行 has_custom_mail=False，而 condition 正是查 mailReceived
```

**契约方案成立，退路作废。** 最初担心的"必须在运行时用 `System.Linq.Expressions` 构造泛型委托"整块复杂度因此消失，反射只剩 `MailController` 的两个静态方法。

`has_custom_mail=False` 之所以能证明后两项，是因为因果链只有一条：`condition` 查 `!mailReceived.Contains(id)`，而 `mailReceived` 只由 callback 写入。condition 转为 false，只可能是 callback 写成功了。**这一步同时闭合了两个验证项，不需要再写额外探针。**

**内容传递这一条差点被漏掉。** 前两轮探针全部 `bridge_ok`，但那时 `I18N` 传的是 translation helper，玩家实际读到的是 SMAPI 的占位符。机制全绿而内容错误——所以 §5.4 把"看一眼正文"列成独立步骤，不允许用机制结论代替。

另外两点已被独立核实：

```text
契约逐成员一致   本地 ILetter（12 项）与 IMailFrameworkModApi（4 项）与 MFM 1.20.0 完全相同
接口必须 public  否则映射代理无法实现它
```

首轮曾失败两次并已修复：`api.GetType()` 是 Pintail 生成的代理，其程序集里没有 MFM 的类型，所以 `MailController` 必须用非泛型 `helper.ModRegistry.GetApi(uniqueId)` 定位；探针自身的 verdict 曾把"已读且正确不再投递"错报成 delivery incomplete，已改为 `bridge_ok_already_read`。

**必须满足**：

- MFM 未安装时，adapter 正常加载，`send_mail` 不进入 Tool View；调用侧另有 `mail_framework_unavailable` 防御分支。
- 所有 MFM 调用都在游戏主线程（`MainThreadDispatcher`）上执行。
- MFM 调用抛异常时必须转成 `failed` ActionResult，带明确 code，不吞掉、不崩溃。

### 4.3 为什么不用 content pack 接入

MFM 的主流用法是内容包：写 `mail.json` + `manifest.json`（`ContentPackFor: DIGUS.MailFrameworkMod`）静态声明信件。本方案**不采用**，原因：

- 内容包是**静态数据**，而本轮需要模型在运行时产出信件正文；
- 写入用户 Mods 目录属于修改用户环境，超出 `send_mail` 的语义；
- 运行时 `RegisterLetter` 已足够，且不污染用户目录。

### 4.4 立即投递

**改动**：注册成功后反射调用 MFM 的投递入口 `MailController.UpdateMailBox()`。

**不存在备选路径。** 方案早期版本把"触发控制台命令 `player_debug_updatemailbox`"列为首选，但该路径不成立：SMAPI 4.3.2.0 的 `ICommandHelper` 只有 `Add` 一个公开方法，不提供触发其它 mod 命令的接口（见 §2.2）。绕过它去反射 SMAPI 内部的命令管理器，只会引入更脆弱的依赖。

`UpdateMailBox()` 本来就是 MFM 自己的投递实现，控制台命令只是它的入口之一。直接调用少一层，也更稳定。

**验收**：调用后 `HasCustomMail()` 为 true，且 `Game1.player.mailbox` 出现占位符。

### 4.5 校验与结果

**改动**：实现 §3 的白名单校验；构造 ActionResult 时区分：

```text
succeeded                            信件已注册并投递
REJECTED mail_body_invalid           body 未通过白名单校验
REJECTED mail_title_invalid          title 未通过白名单校验
REJECTED mail_framework_unavailable  MFM 未安装（防御分支，正常路径下该能力不发布）
failed   mail_register_failed        注册抛异常
failed   mail_delivery_failed        投递抛异常
```

**顺序约束**：校验必须在**任何 MFM 调用之前**完成。文本未通过校验时，不得构造 `Letter`、不得注册、不得投递——校验失败必须是纯本地拒绝，不留下部分生效的状态。校验内部则先做换行规范化（§3），再做白名单判定。

---

## 5. 验收

### 5.1 三层证据

| 层 | 证据 | 是否需要玩家 |
| --- | --- | --- |
| 机制 | 自动化测试：title / body 白名单校验、**同一 Action 重放不产生第二封待投递邮件**、MFM 缺失时能力不发布、参数解析 | 否 |
| 游戏 | SMAPI 日志出现注册/投递记录；`HasCustomMail() == true`；`mailbox` 含占位符 | 否 |
| 闭环 | 玩家走近信箱读到信；`mailReceived` 含 letter.Id | 是 |

**明确不采用**的替代证据：

- 用 `AutoOpen=true` 让信件不显示就写入状态——它证明的是"我们写了数据"，不是"agent 送出了一封信"。
- 用 `player_addreceivedmail` 直接写 `mailReceived`——那绕过了投递机制，验证的就不是我们的能力。

> 边界说明：在信件 `callback` 内写 `letter.Id`（§4.2.1）与上面的调试命令**不是一回事**。`callback` 是 MFM 自己的信件完成钩子，只在玩家真正关掉那封信之后才被调用；调试命令则跳过整条投递链路。前者是必须做的，后者不可接受。

### 5.2 人设一致性验收（本轮决策要求）

链路打通后，需要对真实模型采样，验证信件内容与角色定义一致。样本量与合格线必须事先固定，否则容易滑向"跑通就算合格"。

```text
样本       主 NPC（含 AgentDefinition）× 4 个场景，各 1 封
           场景取：普通问候 / 玩家请求 / 玩家拒绝或负面回应 / 涉及长期承诺
           另加对照 NPC × 2 个场景，各 1 封
           合计 6 封
检查项     署名与身份一致
           语气与 speech_style 一致
           内容与该角色当前关系状态一致
           无越界承诺（不承诺游戏做不到的事，不替代玩家决策）
判定       逐封逐项人工评判，记录通过 / 不通过
合格线     「无越界承诺」必须 6/6 通过（硬性，无例外）
           其余三项允许合计 1 处瑕疵
不通过     保留信件原文、模型输入与判定理由；修复后重采，不修改已记录的样本
```

合格线对"无越界承诺"要求 6/6，因为它直接对应 §7 的对外结论；其余三项属表现质量，允许一处瑕疵但必须记录在案。

### 5.3 验证命令

本机存在两个 Stardew 安装，用途不同，不要混用：

```text
仓库概览路径  是仓库的上级目录，不是游戏安装
E:\SteamLibrary\steamapps\common\Stardew Valley   干净安装：提供游戏 DLL，供构建与自动化测试
D:\data\project\game-agent\Stardew Valley         已 mod 化的测试安装：装有 MFM 与 GameAgentStardew，
                                                  实机验收必须用这一个
```

构建与自动化测试只需要游戏 DLL，两者皆可；**实机验收必须用已装 MFM 的测试安装**。

```powershell
# 构建与自动化测试（提供游戏 DLL）
$game = "E:\SteamLibrary\steamapps\common\Stardew Valley"

go test ./... -count=1
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --configuration Debug -p:GamePath="$game"
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
git diff --check
```

实机安装到测试环境（`install-stardew-adapter.ps1` 的 `-GamePath` 同时决定构建引用与安装目标）：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-stardew-adapter.ps1 `
  -GamePath "D:\data\project\game-agent\Stardew Valley" `
  -ProjectPath adapters/stardew/GameAgent.Stardew.csproj
```

### 5.4 实机步骤

**探针已跑通（§4.2.3），本节记录复现方式。** 在 `Mods/GameAgentStardew/config.json` 里把 `EnableMailBridgeProbe` 设为 `true`，启动游戏并加载存档，然后执行控制台命令：

```text
gameagent_mail_probe
```

SMAPI 日志会按步骤输出 `wia_mail_bridge_probe` 前缀的行：

```text
resolve          API 是否映射成功
register         我方 letter 与委托能否跨过接口边界   ← 这条失败才是"桥接不成立"
deliver          反射定位 MailController 并调用 UpdateMailBox()
has_custom_mail  MFM 是否认为有自定义信待投递
callback_fired   玩家关掉那封信之后出现，证明回调也跨过了边界
verdict          bridge_ok / bridge_ok_already_read / bridge_ok_delivery_incomplete / bridge_failed
```

**探针有两个模式，用途不同（不要揉在一起）：**

```text
gameagent_mail_probe          默认。不动 mailReceived，因此能观察"已读"状态
gameagent_mail_probe reset    先清掉探针自己那个 id 的标记，再跑完整流程
```

**验证"防重复投递"用默认模式三步：**

```text
1. 走到信箱前把信读掉
2. 确认信件正文就是探针传入的原文，没有出现 "(no translation:...)"（§4.2.1）
3. 确认日志出现 callback_fired，再执行一次 gameagent_mail_probe
```

第 2 步不能用"机制通了"代替——注册成功、投递成功、回调触发都不蕴含"玩家看到的是我们传的文本"。第一轮探针正是在这一点上给了假阴性。探针会把 `title` 与 `body` 原文打进 `register` 那一行，直接对照即可。

预期第 3 步 `has_custom_mail=false`、verdict 为 **`bridge_ok_already_read`**、`mail_received=true`。这同时证明 callback 的反向桥接与 §4.2.2 的 `condition` 都生效了。

**要重看信件内容则用 `reset` 模式**：固定 id 加防重复投递的 `condition` 会让默认模式在读完信后不再投递，`reset` 显式清掉标记即可重新走完整流程。它只动探针自己的命名空间 `wia.probe.bridge`，不涉及任何玩家进度。

> 早期版本把 reset 做成了每次运行都执行，结果是 `bridge_ok_already_read` 这条路径**再也跑不到**，而文档还在描述它。默认不 reset、reset 显式，是分开这两个用途的最小代价。

探针用固定文本、不经过 §3 校验，它验证的是机制而不是安全规则，不能当成能力调用。

**然后才是能力链路：**

1. 确认 `Mods/MailFrameworkMod` 与 `Mods/GameAgentStardew` 均加载，SMAPI 日志无报错。
2. 确认 SMAPI 日志中的 CapabilityList **包含 `send_mail`**（条件发布生效）。
3. 加载存档，按 §5.5 的动机场景与 NPC 对话——不要随口闲聊，否则模型没有理由选择 `send_mail`。
4. 观察 SMAPI 日志：`send_mail` 是否被模型自主选择（不是被 prompt 点名）。
5. 确认信件已投递（`HasCustomMail()` / 日志），且信件 id 符合 §4.1 的生成规则。
6. 走近信箱读信，确认文本与模型输出一致、无 token 被解析。
7. 确认 `mailReceived` 含 letter.Id。
8. 临时移出 `Mods/MailFrameworkMod` 重进游戏，确认 adapter 正常加载、`send_mail` 不在 CapabilityList 中、不报错（负向验证）。

---

### 5.5 写信动机场景设计（本轮的验收风险）

退出条件 §7 第 2 条要求模型在系统 prompt 不点名 `send_mail` 的情况下自行选中它。**这条有真实的失败风险**：一次普通对话里，模型没有任何理由去写信，它可能连这个工具都不会看一眼。不做场景设计，实机很可能反复出现"跑了很多次都没选中"。

因此实机采样必须使用**能产生写信动机的玩家台词**，而不是随便聊两句：

```text
动机类型      玩家台词示例（均不出现"写信""发邮件"等工具语义）
关系疏远期    你最近是不是很忙？我好久没收到你的消息了。
临别期        我明天要出远门，可能好些天见不到你。
未说出口期    有什么想跟我说、但当面不好意思说的吗？
```

**边界：这里放宽的是玩家台词，不是系统 prompt。** 第 2 条的"不点名"指的是系统 prompt 不写工具名；玩家表达自己的需求属于正常对话，不是点名。两者不能混为一谈——把系统 prompt 改成提示写信，等于放弃了这条验收。

**若仍不选中**：记录实际对话、模型输出与工具选择，作为失败证据分析，**不放宽"不点名"条件来凑通过**。若确认问题出在能力描述或工具清单，先修 `description` / schema 再重采。

### 5.6 负向用例：不该发信时不调用（总纲硬验收）

总纲 §2.2 把"能力不适用时模型不调用"列为判定类硬验收，§2.5 第 5 条是它的退出条件。它是这一轮的**另一半结论**：

```text
模型会选 send_mail  ≠  模型看见新工具就乱用
```

只验正向，等于只证明了一半。因此 §5.5 的采样必须包含 2～3 个**明显不需要邮件**的场景：

```text
场景类型        玩家台词示例
当下事实询问    今天天气怎么样？ / 现在几点了？
面对面即时话题  你手上拿的是什么？
简单确认        好的，那明天见。
```

这三类都满足：NPC 就在玩家面前，对话可以当场结束，没有任何需要异步转达的内容。

**预期**：模型可以 `present_dialogue` 或直接结束，**不得调用 `send_mail`**。判定标准与正向一致——逐场景记录实际工具选择，出现 `send_mail` 即判不通过，并保留完整对话与模型输出。

---

## 6. 未确认项与已解决项

**没有阻塞开工的未知项了。** 机制侧全部由实机探针确认，剩下的两项是设计上**不需要**验证的：

| # | 项 | 为什么不验证 |
| --- | --- | --- |
| 1 | 原版 mail 的 `%` 前缀命令是否也在 `Text` 解析链上 | 只影响"为什么拒绝 `%`"的解释，不影响校验本身；白名单已一律拒绝 `%` |
| 2 | `TokenParser` 的确切语法 | 白名单不依赖它。只有黑名单才需要先摸清语法，本轮已弃用黑名单 |

**已解决**：

```text
接口映射与参数桥接     实机探针确认（§4.2.3）
内容传递               玩家确认读到的是传入原文；I18N=null 是前提（§4.2.1）
回调反向桥接           读信后出现 callback_fired，且读信后条件转 false
mailReceived 写入      由 condition 转 false 反推确认（§4.2.1）
防重复投递             §4.2.2，Letter 无 Repeatable，condition 是唯一守卫
存档加载后立即可投递    探针 deliver=ok 且 has_custom_mail=True
RegisterLetter 签名    Func<ILetter,bool> / Action<ILetter>，已反射核实（§2.1）
立即投递入口           MailController.UpdateMailBox()（§2.2、§4.4）
跨 Mod 控制台命令       不可行，ICommandHelper 只有 Add（§2.2）
契约是否与 MFM 一致     逐成员一致：ILetter 12 项、API 4 项
```

**MFM 集成机制的验证到此关闭**，不再继续研究这个 mod。后续（CapabilityCatalog、条件发布、`MailTextValidator`、Action 幂等、正负向模型验收）都属于 WIA 已知架构内的常规实现，不再是"这个第三方 mod 能不能接"的未知。

`AutoOpen=false` + 无附件 + 无 recipe + 非 null 且非常真的 condition：这四点使信件走标准 UI 路径且不会重复投递，是本轮设计的基础。

---

## 7. 退出条件

1. MFM 可用时 `send_mail` 进入 Tool View；MFM 未安装时该能力不发布、adapter 正常加载，调用侧防御分支以明确 code REJECTED。
2. 在一个由玩家或游戏事件触发的 AgentTurn 内，系统 prompt 不点名 `send_mail`，模型自行从 Tool View 选中并调用它（有 SMAPI 日志证据）。动机由玩家台词制造，场景见 §5.5。这验证的是 Turn 内 Tool Selection，**不要求 Background Trigger 或 NPC 自发目标**；自主触发的路线见 [Phase10 总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md) §2.6。
3. 信件成功投递，玩家可读，`mailReceived` 记录 letter.Id。
4. §3 的 title / body 白名单校验、§4.1 的 Action 级幂等（同一 Action 重放不产生第二封待投递信）均有自动化测试覆盖，超范围字符与注入样本被拒。
5. 人设一致性按 §5.2 完成一轮采样与判定。
6. 负向用例按 §5.6 通过：能力可用但语义不适用时，模型未调用 `send_mail`。
7. §5.3 全部通过；`check-architecture.ps1` 的 game-agnostic 断言未被削弱。
8. 协议与 Mod 运行时标识未改动。
