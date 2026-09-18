# GameAgent MVP0 Phase10 延迟执行实机闭环技术开发方案

> **Status:** 代码侧已实现（`schedule_mail` + 闸门 + 18 项适配器测试，C# 511 项全过）；实机闭环待验证
> **Date:** 2026-09-18
> **Phase:** Phase10 / [总方案 §2.6 自主触发（已选路线）](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md) 的落地
> **前置:** [Phase10.1 邮件能力方案](GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md)、[Mod 能力接入规范](../development/mod-capability-integration.md)

---

## 1. 这一步在验证什么

命题：

> **WIA 已经具备 durable、deferred 的 agent 执行能力，缺的只是一条真实游戏闭环证据。**

它**不是**新架构。长期异步所需的关键性质 Phase9 已经做了（SQLite 持久化、游戏时钟调度、`WakeAt`/`DeadlineAt`、重试与 reconcile、世界/run binding、任务数量上限、同 Agent ExecutionLane、Task Wake AgentTurn、唤醒回合的 Tool View）。Runtime 侧在本步骤**零改动**。

要补的真实证据是这一条：

```text
玩家今天和 NPC 说了一件事
        ↓
NPC 留下未来意图（不是现在写信）
        ↓
玩家已经走了 / 游戏重启过
        ↓
下一个游戏日 06:00，没有新的玩家交互
        ↓
Runtime 自己收到 task wake
        ↓
Agent 恢复当时意图，结合当时的实况重新决策
        ↓
自主选 send_mail
        ↓
玩家真的收到信
```

## 2. 三个能力的分层

```text
schedule_mail   表达"未来想告诉玩家什么"。不写信、不碰邮箱、不碰 SQLite
create_task     Runtime 的 durable commit。只在存在合法 proposal 后进入 Tool View
send_mail       到点后真正把信投进邮箱。语义保持"现在把这封信发出去"，未改动
```

**为什么不把两者合成一个 `send_mail { body, send_at? }`：** 那会让一个能力同时承担环境动作与任务规划。而且 `send_at` 只有两种可能——模型填 tick（它不知道 Stardew 的 clock/world binding/合法窗口），或 Runtime 理解 `tomorrow` 这类 game-time 语义（把游戏时间概念塞进 Runtime）。两条路都比现状差。

**为什么不把 `create_task` 常驻开放：** 它现在的要求是 `{proposal_ref, instruction}` 且 `additionalProperties: false`，并且三重 gating：

```text
task_tools.go:101   entries 仅在 Source.Kind == Interaction 且已有 proposal 时才加入 create_task
task_tools.go:121   Execute 再校验 schema 与 proposal_ref
task_tools.go:126   再校验 source kind、proposal 存在、clock.id 一致
```

常驻开放会拆掉这套"模型不能凭空制造长期任务"的边界。只有出现 3～5 个 proposal producer、且它们只是重复"翻译时间 + 建 proposal"时，才值得回头抽象。

## 3. 适配器实现

### 3.1 窗口：下一个游戏日 06:00

`wake_at` 与 `deadline_at` 由适配器按游戏时钟计算，模型不参与：

```text
wake_at      = (ToAbsoluteDay(now) + 1) * 1440 + 360      下一个游戏日 06:00
deadline_at  = wake_at + 2 天
```

**这里有一个真实的坑，已被测试钉住：** 一个 Stardew 日期从 06:00 跨到 26:00，所以 24:00 之后 `ToMinute` 已经超过 1440，`tick / 1440` 会把日期多算一天（`tick 2970` 是第 1 天的 01:30，不是第 2 天）。因此反解必须是 `GameClock.ToAbsoluteDay(tick) = (tick - 360) / 1440`，它与 `ToTick` 的编码放在一起，并且有 `AbsoluteDayDoesNotRollOverAfterMidnight` 直接对照。

死线给 2 天余量，理由是唤醒动作的准入条件：

```text
task_execution.go:31   唤醒动作仅在 head.Clock.Tick < Spec.DeadlineAt 时才被准入
```

玩家睡觉会把时钟一次推进一整天，余量不足会让"跳过一天"变成硬失败。

### 3.2 确定性闸门

闸门只读游戏事实，**不交给模型**：

```text
MFM 不可用                → mail_framework_unavailable（能力本身也不发布，这是防御分支）
intent 空或超过 512 字符   → schedule_mail_intent_invalid
该 NPC 本游戏日已排过      → schedule_mail_already_scheduled
时钟不可用                → schedule_mail_window_unavailable
```

**适配器看不到 Runtime 的任务状态**，所以它能执行的规则是"每个 NPC 每个游戏日最多排一封"。另一条硬边界在 Runtime 侧：`task_tools.go:156` 的 `Admission{MaxActivePerOwner: 1}`。两层各管一段，不是重复。

该记录是内存态的：跨进程重启会丢失，后果是允许同一天多排一次，而不是丢信。

### 3.3 意图不是信件正文

`intent` 只进入任务上下文，**永不进入游戏**，也不需要过 `MailTextValidator`（那套白名单存在的原因是信件正文会被游戏 `TokenParser` 当命令解析）。唤醒回合由模型重新写正文，那一次才走正文校验。

意图有两个载体，都不依赖模型转抄：

```text
create_task.instruction    模型写的意图（≤2048），进 Task Context 的 authority 段落
proposal.payload.intent    Adapter 放的同一段意图；Contract 在唤醒回合被逐字发布给模型
```

### 3.4 发布条件

`schedule_mail` 与 `send_mail` 同生共死，都在 `includeMailCapability` 分支里发布（`CapabilityCatalog.cs`）。否则模型能建一个"到点无人可调"的任务，最后以超时收场。

## 4. Runtime 侧：零改动（已核实的代码事实）

```text
tool/proposal.go:48        CaptureProposal 只看 result.GetStatus() 与 result.GetTaskProposal()，不含 capability name
loop.go:591                每个 tool batch 之后都调 captureProposals，普通交互回合同样捕获
task_context.go:99-103     tool view = NewRegistry(环境 catalog, task tools)：proposal 能力与 create_task 同一个 registry
task_context.go:136-146    捕获成功后把 proposal_ref 注入工具结果输出
task_wake.go:35            唤醒回合的 tool view 只有环境 catalog —— create_task 在唤醒回合结构上不存在
loop.go:274                唤醒回合的 event_type 记为 "task_wake"，event_id 记为 WakeID
```

## 5. 已知风险：静默失败链

构造失败时 Ctrl 流是**静默跳过**，不是报错。三个条件必须同时成立：

```text
task 必须 enabled                          （Stardew profile 已为 true）
proposal.clock.id 必须等于世界时钟 id
clock.tick ≤ 当前 tick 且 当前 tick < wake_at
```

其中 `proposal.Clock.Tick > exec.Clock.Tick` 时会**直接跳过捕获**（`proposal.go:64` 返回 nil，不是 error）。因为 `captureProposals` 在 `ref == ""` 时 `continue`，所以症状是精确的：

```text
schedule_mail 调用成功 → 工具输出里没有 proposal_ref → create_task 永不出现 → 全程无任何报错
```

适配器上报的时钟快照可能落后于 Runtime 的 head（模型思考期间游戏时间在推进）。`resolve_meeting` 走同一条路径且已被 Phase9 验证，所以该风险低但真实。实机时如果撞上，先看工具输出有没有 `proposal_ref`。

## 6. 自动化验收结果

| 项 | 结果 |
| --- | --- |
| 窗口计算（含 24:00 后不跨日、06:00 整点、季/年滚动的边界） | 通过 |
| 闸门四类拒绝与通过路径、intent 长度边界与 trim | 通过 |
| 与 `send_mail` 同时发布 / 同时缺席 | 通过 |
| `ActionRequest` 参数形状（缺失、非字符串） | 通过 |
| ActionResult 携带窗口、意图、双方 participant 与时钟 | 通过 |
| `GameClock.ToAbsoluteDay` 反解与 `ToTick` 互逆 | 通过 |
| 适配器全量测试 | **511 项通过**（新增 18 项 + 时钟 7 项） |
| `check-context-static.ps1` | 通过（含 `schedule_mail` 的发布、闸门、无 `send_at`、禁用 `tick / 1440` 断言） |

## 7. 实机验收步骤

准备：Runtime 运行且模型配置有效；适配器已重装到 `Mods/GameAgentStardew`；MFM 已安装（否则两个邮件能力都不发布，日志会写 `send_mail disabled`）。

1. 找一个 NPC，用**延迟告知型动机**（10.1 的 C1 已证明这是唯一可靠选中写信的动机）：

   > "我明天天不亮就出门，你先别讲——等我走了以后，再想办法告诉我。"

2. **本回合**期望 trace（`data/traces.jsonl`）：

   ```text
   tool_call_selected   schedule_mail
   action_result_received  输出里出现 proposal_ref
   tool_call_selected   create_task
   tool_call_selected   present_dialogue（或 settle）
   turn_completed
   ```

3. 期望落库：任务处于 `waiting`，`wake_at` = 下一个游戏日 06:00。

4. **退出游戏、退出 Runtime，再都重新进入**（这一步顺带验证跨进程 durable）。睡到第二天早上 06:00 之后。

5. **唤醒回合**期望 trace：

   ```text
   turn_started   event_type = "task_wake"，event_id = WakeID
                  turn_tool_names 含 send_mail，且不含 create_task
   tool_call_selected   send_mail
   ```

6. 期望游戏内：邮箱出现该 NPC 的信，读过之后 `mailReceived` 含该 letter id。

失败时先看什么：

```text
第 2 步就断（create_task 不出现）   proposal 被静默丢弃。看 schedule_mail 的工具输出里有没有
                                   proposal_ref；没有就比对适配器上报时钟与 Runtime head
第 5 步不醒                        任务是否仍为 waiting、跨重启后 dispatcher 是否派发、
                                   时钟是否真的推进过 wake_at
第 5 步醒了但没发信                看唤醒回合的 turn_failed reason（max_steps_exceeded /
                                   一次工具被拒后步数耗尽）
```

## 8. 明确不做与已知限制

```text
只有 tomorrow，没有 later_today    later_today 是唯一会让 wake 落在同一游戏日的分支，需要额外
                                  处理"今天已过该时刻就顺延"，且当天几小时后到达更像提醒而非信
没有 Background Trigger            起意仍然来自玩家交互。这一步证明的是 deferred execution，
                                  不是 autonomous initiative
不下发 tick 给模型                 wake_at/deadline_at/payload 之外的时钟概念都不进 schema
闸门不持久化                       跨重启可能允许同一天多排一次
无跨存档行为定义                   任务随世界 binding 存续；换存档后的归并属未来工作
```

**公开表述的边界：** 这一步跑通后可主张 **durable, deferred agent execution**；不可主张"NPC 自主产生目标"。
