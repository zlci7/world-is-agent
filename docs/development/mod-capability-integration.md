# 第三方 Mod 能力接入规范

> **Status:** Normative — Adapter 接入第三方 mod 能力时必须遵守
> **Date:** 2026-09-18
> **Origin:** 从第一个第三方 mod 接入（[mail-capability.md](mail-capability.md)）沉淀
> **Related:** [guide.md](guide.md)、[logical-separation.md](logical-separation.md)、[testing.md](testing.md)

---

## 1. 这份规范要解决的问题

Adapter 可以包装第三方 mod 的能力并暴露为 Capability。本规范定义这条路的**边界**：哪些是允许的、哪些必须拒绝、失败怎么表达、验收要什么证据。

先明确一个容易被误解的事实：

```text
adapter 本身就是一个 mod（manifest.json + GameAgent.Stardew.dll，装在 Mods/ 下）
```

因此对 adapter 而言，**调用我们自己的能力实现和调用另一个 mod，是同一件事——都是进程内的 C# 调用**。两者在机制上没有复杂度差异。真正的差异只有两个：

| 差异 | 我们自己的能力 | 第三方 mod 能力 |
| --- | --- | --- |
| 接口是否正式 | 永远直接调用（同一程序集） | 有公开 API 则直接调用；只有私有字段才被迫反射 |
| 接口是否由我们控制 | 是 | 否，升级可能无预告变更 |

**"接入第三方 mod 成本更高"不是事实。** 成本结构与新增我们自己的能力相同（声明 + handler），额外需要管理的是"接口稳定性不由我们控制"这一风险项。

---

## 2. 核心机制：能力是声明出来的，不是发现的

```text
CapabilityCatalog 声明能力（Name / Description / InputSchema / execution_mode / extensions）
        ↓
可选：按条件发布（已有先例：includeTaskCapabilities 控制 task 能力是否出现在 CapabilityList）
        ↓
Runtime 把发布出来的能力建成 Turn Tool View → 成为模型可见的 tool
        ↓
模型自主选择 → ActionRequest
        ↓
RuntimeClient 的执行分派调用对应 handler
        ↓
handler 调用 mod 能力 → ActionResult
```

必须理解的三个结论：

1. **没有自动发现机制。** 不存在"扫描已装 mod 自动变成 tool"。每个 mod 能力都需要：一次声明 + 一个 handler。
2. **发布侧可以条件化**，执行侧不能。发布可以按 mod 是否存在、任务是否 ready 等条件决定；执行端是显式分派，每个能力一个 handler。
3. **模型只看得到 Adapter 声明出来的东西。** Runtime 不解析 `Description` 做执行决策，也不理解 mod 概念。

**推论：让 Runtime 自动理解任意 mod 违背架构边界。** 协议是 narrow waist，Adapter 是事实来源。这条不能为了"少写代码"而打破。

---

## 3. 硬边界

以下每条都是"必须"，违反即不通过验收。

### 3.1 第三方 mod 必须是可选依赖

- adapter 未安装该 mod 时**必须正常加载**，不崩溃、不产生硬依赖。
- 不直接引用第三方 mod 的程序集（不 `Reference` 其 DLL）。直接引用会让未安装的用户在加载或运行时崩。
- 未安装该 mod 时的两种处理，按语义选择：
  - **首选：不发布该能力。** 在构建 CapabilityList 时检查 mod 是否存在，不存在就不发布，模型根本看不到它。
  - **兜底：发布但拒绝执行**，返回 `REJECTED / <mod>_unavailable`。用于发布时无法确定、而执行时才能确定的情况。

### 3.2 通过公开接口访问

按稳定性优先选择访问方式：

```text
1  SMAPI GetApi        mod 提供 IModApi 时首选（helper.ModRegistry.GetApi("<UniqueID>")）
2  控制台命令           mod 暴露 SMAPI 控制台命令时可用（ICommandHelper）
3  运行时反射           以上都不可用时才用，且必须在文档中记录兼容性风险
```

反射是**妥协**而不是方案：它依赖 mod 的内部结构，mod 升级可能直接崩。使用反射时必须在能力文档中写明依赖的 mod 版本范围。

### 3.3 所有 mod 调用在主线程

- 一律通过 `MainThreadDispatcher` 在游戏主线程执行。
- 不得从网络回调线程直接触碰游戏对象。
- 异常必须转为带明确 code 的 `failed` ActionResult，**不静默吞掉**。这一点尤其重要：有些 mod 内部会 catch 异常并静默忽略，我们不能让"什么都没发生"表现成成功。

### 3.4 外部文本必须先校验再交给游戏

这是本规范中最容易出事故的一条。

**未知 mod 可能把玩家侧文本当作命令或 token 解析。** 已核实的具体例子：MFM 的信件正文会进入游戏的 `TokenParser`，其官方文档明确写着可以用 base game commands 加钱加物品——**文本即命令通道**。

因此：

- 凡是会把模型产出的文本交给游戏或第三方 mod 的文本字段，**必须先校验**。
- 校验失败返回 `REJECTED`，**不得写入未经校验的文本**。
- 校验规则按具体 mod 的解析行为确定，并由自动化测试覆盖。
- 校验是**保守优先**：不确定某字符是否被解析时，先拒绝。

### 3.5 不得回灌不应暴露的信息

第三方 mod 的返回值会进入模型上下文。不得把文件路径、存档内容、本机信息等直接回灌给模型。需要暴露的状态必须显式投影为通用事实。

### 3.6 高风险副作用不得作为默认能力

发物品、改世界状态、执行命令这类能力需要单独授权，不随普通接入一起开放。

### 3.7 默认直接执行，不做玩家确认

当前**没有**向玩家请求确认的交互机制。因此不引入"需确认"类字段：它没有执行点，加了只是无人消费的元数据。

需要限制副作用时，用以下手段，而不是加一个不生效的标志：

```text
Adapter 侧输入校验        收窄参数（例如"仅文本、无附件"）
收窄能力语义              把大能力拆成具体小能力
干脆不暴露                最高风险的能力不进入 CapabilityList
```

---

## 4. Capability 声明要求

### 4.1 Description 是模型唯一的说明书

模型只会调用它看得懂的能力。**Description 的质量直接决定"自主调用"与"乱调"的分界。** 第三方 mod 不会提供模型可读的说明，这份说明必须由我们写。

Description 必须说明：

```text
这个能力做什么（面向模型的语言，不是面向实现的）
参数语义（每个参数是什么意思，什么情况该填什么）
游戏侧效果（会改变什么，谁是受益者/受影响者）
适用条件（什么情况下该用，什么情况下不该用）
失败/拒绝的常见原因（例如能力不可用、参数非法）
```

**写 Description 是接入第三方 mod 的主要工作量，不是"接入"本身。**

### 4.2 输入 Schema

- 用 JSON Schema 声明，`additionalProperties: false`。
- 给每个字段写 `description`，取值范围用 `enum` / `maxLength` / `maxItems` 表达。
- 参数面**越小越好**：参数越少，模型用错的概率越低，校验面也越小。

### 4.3 执行与并发模式

```text
execution_mode    Sync    动作在原地完成（注册、查询等）
                  Async   动作跨越游戏 tick，需要 terminal result
concurrency_mode  Sequential / ParallelSafe
```

涉及 NPC 控制权、UI、存档写操作的能力一律 `Sequential`。多步长动作必须 `Async` 并等待终态，不得建模为同步函数。

### 4.4 extensions 与 tool_policy

`Capability.extensions` 是 `Struct`，**新增字段不需要改 proto**，属于 additive 范围。

现有 Runtime-facing 策略字段（`runtime/internal/tool/types.go` 的 `ToolPolicy`）：

```text
exclusive_per_step      该能力必须独占一步
settle_after_success    成功后直接收敛本 Turn
```

新策略字段必须满足两个条件才允许加入：

1. **有明确执行点。** 没有执行点的字段只是注释，而 Runtime 不把 policy 注入模型输入，所以它对模型毫无影响。
2. **是通用调度语义，不是游戏私有概念。** Runtime 不得按 capability name 分支。

**不要为单个 mod 扩 policy 字段。** 先证明它是通用的，再加入。

---

## 5. 失败与拒绝的表达

Adapter 负责把失败原因表达成模型和调用方都能理解的结果。约定：

```text
succeeded                        动作完成
REJECTED  <mod>_unavailable      依赖的 mod 未安装 / 未就绪
REJECTED  <字段>_invalid         输入校验失败，指名字段
REJECTED  <原因>                 前置条件不满足（例如不在对话中、目标不可达）
failed    <操作>_failed          执行期异常，附原因
cancelled <原因>                 执行被中断
```

要求：

- 每个 code 必须由代码常量产生，不拼字符串。
- 拒绝与失败**不得都写成同一句泛化 message**：模型靠 code 和 message 决定下一步，含糊的失败会让它反复重试或放弃。
- 执行期异常必须保留原因；静默吞掉会让问题不可诊断。

---

## 6. 验收要求

### 6.1 三层证据，不得互相替代

| 层 | 证据 | 需要玩家 |
| --- | --- | --- |
| 机制 | 自动化测试：输入校验规则、mod 缺失时的拒绝路径、幂等性、参数边界 | 否 |
| 游戏 | 真实运行日志显示注册/执行/投递记录 + 游戏内可观察状态 | 否 |
| 闭环 | 玩家侧真实动作触发的最终状态变化 | 是 |

**禁止的替代证据**（它们验证的不是我们的能力）：

```text
直接写状态字段绕过机制       例如直接写 mailReceived 而不经过投递
跳过正常路径让过程不可观察   例如用 AutoOpen 跳过信件 UI
```

### 6.2 必须包含负向用例

- **能力不适用时模型不应调用它。** 这是"自主"与"乱调"的分界线，必须测试。
- **依赖缺失时不得崩溃。** 卸载 mod 后 adapter 正常加载。
- **非法输入必须被拒绝**，且拒绝后系统状态不变。

### 6.3 实机验证

真实游戏 + 真实模型的验证不能由 fake 通过代替。实机步骤必须写清：如何触发、观察什么、失败时的安全行为是什么。

---

## 7. 落地检查清单

接入一个新的第三方 mod 能力时逐条确认：

```text
[ ] mod 未安装时 adapter 正常加载，能力不可见或被明确拒绝
[ ] 未直接引用第三方 DLL
[ ] 访问方式已选（GetApi > 控制台命令 > 反射），反射方案已记录兼容性风险
[ ] 所有调用在主线程，异常转为带 code 的 failed
[ ] 所有会交给游戏/该 mod 的文本已校验，校验有自动化测试
[ ] 返回值不回灌路径、存档内容等敏感信息
[ ] Description 写清用途、参数语义、游戏侧效果、适用条件
[ ] InputSchema 完整，additionalProperties=false
[ ] execution_mode / concurrency_mode 选择正确
[ ] handler 已接入 RuntimeClient 执行分派
[ ] 失败 code 用常量表达，拒绝与失败可区分
[ ] 负向用例覆盖：不适用时不调用、依赖缺失不崩溃、非法输入被拒
[ ] CapabilityCatalog 测试与 adapter static check 已更新
[ ] check-architecture.ps1 的 game-agnostic 断言未被削弱
[ ] Runtime 生产代码未新增该能力名的执行分支
```

---

## 8. 与更长期边界的关系

```text
Phase A（逻辑分离，已完成）
    Adapter 不再依赖仓库目录布局。

本规范（跨 Mod 集成边界）
    Adapter 可以包装第三方 mod，但遵守可选依赖、公开接口、主线程、文本校验、
    默认直接执行、不扩单 mod policy 字段。

Phase B（物理拆仓，未授权）
    出现第二个真实 Adapter 时优先拆仓，命名 wia-adapter-<game>。
```

本规范不改变 Runtime / Protocol / Adapter 的分工：第三方 mod 是**游戏侧事实与能力的来源之一**，Adapter 负责翻译它，Runtime 始终不需要知道 mod 的存在。
