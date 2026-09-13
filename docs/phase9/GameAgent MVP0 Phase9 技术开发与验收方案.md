# GameAgent MVP0 Phase9 技术开发与验收方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-13
> **Phase:** Phase9 Runtime Durable Task & Appointment Vertical Slice
> **目标:** 以跨天预约完成持久意图、到期认知、真实行动、结果记忆和存档回退的最小闭环
> **Code Inspection Baseline:** `main` @ `daf4f98`；实现分支基点：`e482923`；开发分支：`codex/phase9-durable-task`；Phase8.1、Phase8.2、Phase8.3 Accepted，保留各自验收限制
> **技术栈:** Go、SQLite、gRPC / Protobuf、C#、SMAPI
> **Roadmap:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md)
> **Architecture:** [Runtime 整体架构设计规范](../summary/GameAgent%20Runtime%20整体架构设计规范.md)
> **Memory 基线:** [Phase8.2–8.3 开发与验收记录](../phase8/GameAgent%20MVP0%20Phase8.2-8.3%20开发与验收记录.md)
> **后续阶段:** Phase10 Environment Recovery；Phase11 Evaluation、Developer Experience 与产品化

## 1. 目标、范围与交付边界

玩家通过自然语言与 NPC 约定未来某天在沙滩见面。Runtime 持久保存任务，游戏时间到期后唤醒该 NPC，以新的短 Turn 决定并执行赴约。NPC 跨地图到达、等待，玩家按时出现形成见面事实，未出现则结束等待并恢复原生日程。后续对话可以引用实际结果。

```text
玩家协商 → 游戏侧校验约定 → Runtime 创建持久 Task → 当前 Turn 结束
→ 正常保存 / 跨天推进 → Runtime 到期投递 NPC lane → 新 Turn 观察与行动
→ 世界进展 / 后续到期唤醒 → 见面、超时或中断 → 任务收敛与结果入 History
→ 对话真正关闭 → Adapter 释放交互占用 → NPC 继续当前时刻原生日程
```

### 1.1 本阶段交付

- Runtime 单实例、SQLite 持久 Task、到期唤醒、后续唤醒、状态与结果关联。
- 游戏时钟、活动世界绑定、内部 Task trigger、Runtime Tools 与最小检查点合同。
- TaskService 内部服务与模型工具入口分离；已确认结果、截止协调和控制权释放由确定性代码处理。
- 单人存档、当前玩家、配置中的受控 NPC；同一 NPC 最多一个非终态 Task。
- 一次性预约允许跨天、跨季和跨年；正常日终保存后仍可继续等待未来日期。
- 一次会面窗口位于目标日内；不将 NPC 连续多天驻留在会面点作为首个游戏场景。
- 沙滩主演示，酒馆与广场验证语义点位复用；真实跨地图行走，玩家无需跟随。
- `approach_player({})` 固定目标接近、思考期间停留、真实 UI 结束后恢复原生日程。
- 成功赴约、玩家爽约、运行中重启、存档回退与断线安全收尾的自动化和实机证据。

任务持久化成功表示 Runtime 当前工作状态已提交；任务与游戏进展共同恢复的边界是成功写入游戏存档的检查点。台词确认、任务创建、NPC 到达和实际见面分别验收。

### 1.2 明确边界

| 项目 | Phase9 合同 |
| --- | --- |
| 长期任务 | Runtime 保存未来意图、状态、唤醒和结果；支持跨天的一次性任务 |
| 当下游戏执行 | Adapter 校验并执行当前请求；短期移动、等待监听和安全释放属于世界效果 |
| 游戏加载 | 按该存档携带的 checkpoint_id 恢复任务集合和完整状态 |
| Runtime 重启，游戏未重新加载 | 从当前 world_run_id 的 working head 恢复；重新绑定后观察和协调 |
| Runtime 不在线 | 停止新的任务唤醒与未来行动；Adapter 释放当前占用，游戏继续运行 |
| 断线时保存 | 游戏照常保存，任务检查点写为 unconfirmed；加载后暂停任务恢复并提示 |
| 检查点异常 | 保留数据、暂停任务功能；普通游戏与不依赖 Task 的对话可以继续 |
| Memory | 使用已 Accepted 的 Phase8；接入通用 Task 结果来源，不作为任务执行权威 |

本阶段不包含循环任务、任意 DAG / 工作流、分布式调度、通用补偿事务、完整事件重放、跨连接动作续跑、任意时刻游戏存档、存档分支管理、全量 Memory 回滚，以及 Adapter 离线任务镜像。自动重连、退避、跨连接事件恢复和完整 pending operation 协调由 Phase10 验收。

## 2. 已有实现与开发基线

| 现有代码 | 已有能力 / 限制 | 本阶段接入 |
| --- | --- | --- |
| `runtime/internal/session/identity.go`、`lane.go` | 稳定 AgentSessionKey；同 NPC 有界串行 lane，默认队列容量 1 | Task 与玩家事件共用 lane；可靠处理队列满和旧投递 |
| `runtime/internal/gateway/gateway.go` | LaneStore 在 Connect 内创建，生命周期受 stream 约束 | 增加进程级 TaskService、活动世界注册与内部投递入口 |
| `runtime/internal/tool/types.go`、`agent/scheduler.go` | 当前工具执行仅有 Environment kind | 增加显式 Runtime kind 与本地执行器 |
| `runtime/internal/memory/sqlite_store.go` | SQLite、稳定 owner 和事务经验 | 复用驱动及隔离模式；任务独立表和存储边界 |
| `protocol/proto/gameagent.proto` | GameEvent、Action、GameTime、Heartbeat | 增补通用时钟、世界加载、Task 来源与检查点合同 |
| `adapters/stardew/src/Runtime/ProtocolMapper.cs` | `GameTime.tick` 来自 `Game1.ticks`，是运行帧计数 | 单独输出可持久比较的游戏逻辑时间 |
| `MoveToCapability.cs` | 当前地图移动；清理 controller / Halt 不等于恢复原生日程 | 保留输入合同，接入共享控制权和行为恢复 |
| `RuntimeClient.cs`、`InteractionContextStore.cs` | 当前动作依赖玩家交互快照，移动有距离门 | 增加独立的 Task 执行来源及真实到达交互来源 |
| `ModEntry.cs` | 尚未接入 TimeChanged、Saving、Saved、DayEnding | 世界时钟、保存交接、日终世界效果清理 |
| 对话 UI 与 PlayerInteractProbe | 已有输入抑制、Waiting UI 和回复流程 | 回归原版台词抑制；补思考停留、真实关闭与回复接续区分 |

以上是开发前基线，不代表 Phase9 已实现。Phase8 仅同步状态记录、配置基线和回归，不继续扩展摘要、检索或留存能力。

当前配置以 `runtime/config/model.json`、`runtime/config/agent.json` 及 Stardew override 为准：

| 配置 | 当前值 |
| --- | --- |
| 模型 | `deepseek-v4-flash`，context window `131072`，max output `12800` |
| Context | max request `25600`；max user message `22000`；transcript `4096` |
| History | keep recent `8000`；summary `2048`；summary input `12800` |
| 检索与留存 | 最多 5 片段 / 1024 tokens；`retention_days=0` |
| 单 Turn | 90 秒；最多 3 Steps、每 Step 4 / 每 Turn 6 次工具调用 |
| Async Action | 默认 45 秒；每 Turn 最多 1 个异步动作 |

`keep_recent_tokens` 是压缩后近期原文目标，不是“聊天超过 8000 tokens 必然触发压缩”的阈值。上表为开发前配置基线。9.3 在 `runtime/config/games/stardew-valley/agent.json` 将 async_action_timeout_ms 配为 190000、turn_timeout_ms 配为 270000，Adapter 当前行程上限为 180 秒；预算覆盖行程、回执、模型调用、观察和等待登记。Runtime 通用默认值及模型/token/Step 上限保持原值。预约等待由有限监听和持久 wake 管理，不占用长 Turn/Action；具体配置与测试见 9.3 第 3.4 节。

## 3. 架构与三个生命周期

| 对象 | 所有者 | 责任 |
| --- | --- | --- |
| 意图、协商、需要判断时的行动选择 | Agent | 根据当前事实决定接受、行动、再次等待或请求取消 |
| Task | Runtime TaskService | 跨 Turn 的持久身份、状态、下一次唤醒、证据、结果与恢复 |
| Turn | Runtime | 一次有界认知；需要模型决策时分配新 turn_id 并重新观察 |
| Action | Runtime + Adapter | 当前开始的动作生命周期；Adapter/Game 执行真实世界效果 |
| 点位、日历、路径、玩家到达、控制权 | Adapter / Game | 当前事实、可行性判断、动作及安全收尾 |
| Checkpoint | Runtime + 游戏保存 | Runtime 存完整任务快照，游戏存档携带精确引用 |
| History / Memory | Runtime | 已发生的交互来源，不驱动恢复或重新创建任务 |

Runtime Core 只理解通用 Task、Clock、Source、Evidence 和 Tool metadata。不按 Stardew 工具名、事件名、地图、节日或 `Observation.state.stardew` 写分支。

预约中的 `scheduled / travelling / waiting / met / expired / interrupted` 是任务携带的领域进展及游戏证据，不成为通用调度器的状态枚举。Runtime 调度状态与 NPC 物理状态分别保存。

Adapter 只接受“现在开始执行”的世界命令。局部等待监听从 NPC 已到达后开始，具有截止释放条件，不负责保存未来预约、扫描未来任务或重新拉起 Agent。

### 3.1 TaskService、Runtime Tools 与 Environment Capabilities

TaskService 是 Runtime 进程内的独立 Go 模块，内部接口与模型工具接口分别定义：

| 层次 | 调用者 / 入口 | 执行位置与责任 |
| --- | --- | --- |
| TaskService | Runtime Tools、时钟调度、证据协调、保存与加载生命周期 | Runtime 内部服务；校验身份、提交状态、持久唤醒、关联结果与检查点 |
| Runtime Tools | Agent 通过统一 Tool View 调用 `create_task / update_task` | Registry 中声明 `KindRuntime`，执行器本地调用 TaskService，返回 ToolResult |
| Environment Capabilities | Adapter 声明的校验、移动、等待和交互能力 | 映射为 `KindEnvironment`；执行器经 ActionRequest 调用 Adapter，由 Game 执行 |

Runtime Tools 与 Environment Tools 共用 Registry、Tool View、参数校验、预算、执行策略和 Trace；`kind` 由受信注册项确定，模型不能指定或覆盖。名称冲突在注册时拒绝，工具不可用时从 Tool View 移除并保留诊断。Runtime 工具不会注册成 Adapter capability，也不经过虚构的 Environment Action。

TaskService 的内部入口包括：创建规范化 TaskSpec、接纳证据、提交等待/取消意图、协调一次 wake、创建/恢复检查点。时钟、结果和保存事件直接调用内部入口，不通过模型生成工具调用。首版采用进程内接口和依赖注入，部署单位仍是单个 Runtime 服务。

### 3.2 通用任务内核与预约准入规则

| 通用 Task 内核 | Phase9 预约场景规则 |
| --- | --- |
| owner、逻辑时钟、任务状态、revision、幂等、后续 wake、恢复 | 从有效玩家交互创建一次性未来会面；同 NPC 最多一个非终态任务 |
| 接受受信调用方提交的规范化 TaskSpec，保留创建来源 | `create_task` 通过 `proposal_ref` 取得已校验日期、窗口、参与者和点位的约定 |
| 按规范化证据及任务结果合同推进；opaque 数据只保存和转发 | Adapter 判断实际到达、等待、见面、爽约及节日可用性 |

玩家交互来源、proposal_ref 和单任务限制属于本阶段预约准入策略，不成为 TaskService 所有任务类型的固定前置条件。TaskSpec 保存通用目标、时钟约束、结果合同和来源关联；预约额外携带不可变世界约定。首版只装配预约的生产入口，通过不含玩家/点位/Proposal 的内部 fake TaskSpec 验证内核边界，不增加其他产品场景或动态插件框架。

### 3.3 唤醒处理与模型决策

Task wake 表示一次持久协调机会，不等于一次 LLM 调用。协调器先处理已接纳事实和时间约束，必要时直接读取当前 Observation，再决定是否启动认知。

| 触发 | 确定性处理 | 是否启动模型 Turn |
| --- | --- | --- |
| 首次出发到期，任务与世界条件仍有效 | 校验绑定、版本和窗口，取得新 Observation | 是，由 Agent 选择当下行动 |
| 等待操作返回有效回执 | 保存进展与截止 wake，结束当前 Task Turn | 否，不为登记等待增加一次决策 |
| 接纳 satisfied / unsatisfied 结果 | 按结果合同提交终态、结果入账、发起控制权收尾 | 否，业务结果不依赖模型确认 |
| 有效到达交互 | 保留真实交互来源及已经确认的任务结果 | 可以，按独立交互来源开始对话 |
| 截止或恢复协调 | 查询证据/当前操作，确认则收敛，无法确认则有界重试或暂停 | 不为已知结果或技术重试调用模型；只有仍需行动判断时才调用 |

模型输出表达意图，不直接修改数据库状态、revision、wake claim 或执行代次。确定性收尾在模型超时或不可用时仍然成立。Task 成功与到达对话分属不同生命周期：成功终态停止任务调度，不撤销仍有效的交互来源。

## 4. 游戏时钟、世界绑定与 Protocol

### 4.1 持久游戏时间

新增 `WorldClock`：`clock_id`、`now_tick:int64`、`sequence`，绑定 `game_id + world_id + world_run_id`。`now_tick` 使用 Adapter 定义的稳定游戏时间单位；Runtime 只比较同一 clock_id 内的整数，不推算季节长度、HHMM 或地点开放时间。

Stardew 使用 `stardew.calendar-minute.v1`，由游戏日期的绝对日序与当天分钟换算。跨天、跨季、跨年单调推进；暂停时逻辑时间不推进。现有 `GameTime` 继续表示事件的可读时间，`GameTime.tick` 帧计数不作为 Task 到期依据。

- Adapter 在加载握手、TimeChanged 和跨日时报告时钟；序号单调递增。
- Runtime 的现实时间 ticker 仅驱动扫描与技术重试，不把离线时长加到游戏时间。
- 同一次加载中的倒退时钟暂停该世界任务并报告 `clock_rewound`；正常读档使用新 world_run_id 和检查点恢复。
- 时钟未同步、连接不健康、世界正在加载/保存或恢复未完成时，禁止投递新的任务。
- 跳过多个唤醒点时合并为一次当前状态协调；超过 deadline 的任务不先补执行过时的出发动作。

### 4.2 稳定身份与运行代次

`AgentSessionKey = game_id + world_id + entity_id` 保持不变。

`world_run_id` 由 Adapter 在每次 SaveLoaded 创建，跨普通日切换及 Runtime 重新连接保持不变；重新读取同一存档也必须创建新值。它用于区分“同一游戏继续运行”和“游戏真正读档”，不替代 world_id。

Runtime 为每次有效执行绑定分配 `execution_generation`。存档恢复、保存屏障或连接替换会 fence 旧执行代次。Task revision 负责业务版本，generation 负责拒绝旧 Turn、旧事件和旧 controller 回调，两者不能混用。

活动世界注册保存当前连接、完整 EntityRef / definition_id、最新时钟及 ready 状态。Task 唤醒先解析有效绑定，再获取该 NPC 的 lane。无连接或实体暂不可用时保留任务；不建立一个没有 Environment 的模型 Turn。

Phase9 支持显式建立新连接并重新绑定同一运行实例，以验证 Runtime 重启恢复；不要求自动 reconnect loop。旧 stream 的 lane 必须停止，新绑定完成前不得执行任务。

### 4.3 增量协议合同

以下通用消息和字段在 `v1alpha2` 上 additive 增补；精确字段、类型和空闲编号见 9.2 第 3 节，兼容已有消息和普通对话。

| 合同 | 方向 / 附着位置 | 必备内容 |
| --- | --- | --- |
| `gameagent.tasks.v1` 协商 | Hello / EnvironmentReady 增量字段 | supported_extensions / accepted_extensions；未协商不开放 Task 工具 |
| `WorldBinding` / `WorldBindingReady` | Adapter ↔ Runtime 控制消息 | game/world/run、实体目录、clock、加载时的检查点标记；Runtime 返回 generation 与 ready / paused |
| `WorldClockUpdate` | Adapter → Runtime | 当前绑定、clock_id、now_tick、sequence |
| `CheckpointPrepare` / `CheckpointPrepared` | Adapter ↔ Runtime | save_request_id、当前绑定、采样 clock、保存收尾的 final_evidence；返回 checkpoint_id、schema、checksum、新 generation 或明确失败 |
| `CheckpointFinish` | Adapter → Runtime | save_request_id、saved / aborted；解除对应保存屏障 |
| `TaskActionSource` | ActionRequest 可选字段 | task_id、start_revision、wake_id、operation_id、world_run_id、execution_generation，以及原样转发的不可变 task_contract |
| `InteractionSource` | GameEvent 可选字段 | source_id、绑定、玩家身份、player / task_arrival；到达来源关联 Task/operation，执行时由 Adapter 校验 |
| `TaskControlRequest` / `TaskControlResult` | Runtime ↔ Adapter | 按 Task/operation/绑定执行幂等 release，并报告 released / handed_off / unconfirmed；不用于提交新行动 |
| `TaskProposal` | ActionResult 可选字段 | 规范化 clock、wake_at、deadline_at、参与者、等价约定键、opaque payload |
| `TaskEvidence` | GameEvent / ActionResult / Observation 可选字段 | fact_id、Task/operation 关联、绑定、发生时间、progress / satisfied / unsatisfied / interrupted、可选 wait_until、opaque details |

TaskProposal 是世界任务的当次校验结果，不是已注册任务，也不是 TaskService 内部创建接口的唯一输入类型。Runtime 为有效结果生成 `proposal_ref`；TaskEvidence 的 refs 也由 Runtime 根据收到的权威结果生成，模型不能自造证据。

`wait_until` 是 progress 证据对当前操作已经进入有界等待的规范化回执，使用任务同一 clock 的逻辑时间；在证据发生时刻之后且不超过 deadline。Runtime 接纳该回执时原子提交 waiting 与后续 wake；处理时已到截止则立即协调，不延后原约定。Runtime 不解析 opaque 的 `waiting / met` 字样。结果确认由已登记 operation、当前时间线及 TaskSpec 的结果合同约束；普通移动成功不能被升级为任务目标已满足。

TaskActionSource 由 Runtime 注入，模型参数不包含 start_revision、generation 或 operation_id。start_revision 取自已登记的 Operation.StartRevision，表示该动作启动时的任务版本；TaskEvidence.start_revision 原样承接同一 operation 的启动版本。原 source_event_id / source_turn_id 继续表达当前来源；内部 wake 使用 Runtime trigger 身份，不伪造玩家点击。

预约的 task_contract 来自已持久保存的 Proposal，包含参与者、规范化时间约束及 opaque 约定。Runtime 原样传递，Adapter 用它校验当下命令的点位和窗口；Adapter 无需查询或保存未来任务列表。没有世界约定的内部任务不携带此字段。

Protocol 只保存跨游戏结构；具体日期、landmark、`met` 等细节留在 opaque 数据与 ContextFact。没有协商这些扩展的 Adapter 继续普通对话，Task 工具不进入其 Tool View。同步更新 proto、Go 生成代码、C# 构建生成、静态检查及兼容测试。

## 5. Runtime Task 数据与 Runtime Tools

### 5.1 TaskRecord

| 分组 | 字段与规则 |
| --- | --- |
| 身份 | task_id、game_id、world_id、entity_id；身份从当前执行上下文取得 |
| 目标 | instruction、结果合同、创建来源；预约另有参与者、opaque payload、proposal 来源与等价约定键 |
| 时间 | clock_id、created_at_game_tick、next_wakeup_at、deadline_at；现实 created_at 仅审计 |
| 状态 | waiting / running / paused / succeeded / failed / cancelled；revision |
| 进展 | opaque progress、last_wake_reason、needs_reconcile、pause_reason |
| 关联 | 创建 event/turn/tool_call，最近 wake/turn/action/operation，已确认 evidence refs |
| 结果 | result_id、outcome、reason、发生时间、证据 refs；世界效果及控制权交接/释放状态独立记录 |

TaskSpec 是创建输入，TaskRecord 是持久状态。Phase9 的规范化结果合同为 `authoritative_evidence`：任务目标是否达成来自受信证据，TaskService 只处理通用结果枚举；具体“何时算见面”由 Adapter 校验。内部 fake task 同样通过受信测试来源提交规范化结果，无需构造玩家交互或预约 Proposal。

`waiting` 必须带下一次游戏时间唤醒；世界事件可以提前唤醒。最终结果已确认时可结束任务，不为终态保留例行唤醒。`paused` 不自动调用模型，必须显示暂停原因；明确绑定恢复或人工处置后才能继续。

`succeeded / failed / cancelled` 是当前运行时间线的终态。读档可以从保存时的快照恢复同一 task_id 的非终态版本；这是存档恢复，不是终态 Task 在当前运行中自行重启。

### 5.2 Runtime Tools：模型意图入口

本地工具注册为 `KindRuntime`，由独立执行器调用 TaskService，不封装为发往 Adapter 的 ActionRequest。Tool View、schema 验证、调用预算、结果裁剪和 Trace 沿用通用工具链；catalog 名称冲突明确拒绝。

工具层负责模型参数解析、proposal_ref 和结果封装，TaskService 保留通用状态接口。Registry 只合并条目并复用现有准入；Scheduler 在构造 ActionRequest 前按 kind 分流。Environment 同步/异步发送前的操作登记检查可以返回错误并阻止发送。

| 工具 | 模型输入 | 执行与返回 |
| --- | --- | --- |
| `create_task` | `proposal_ref`、`instruction` | 验证当前 owner、校验结果与时效；复制权威 proposal；事务创建 Task 与首次 wake，返回 task_id、revision、waiting、created |
| `update_task` | `task_id`、`intent`；wait 时 `next_wakeup_at` 和可选 `progress_note`；cancel 时 `reason` | 校验访问来源、服务端版本和时钟；原子提交等待意图及后续 wake，或取消请求 |

两项工具为 Sync + Sequential，并以通用 policy 声明独占 Step、成功后允许继续下一 Step。创建成功后模型才可确认“约好了”。创建事务失败时明确返回失败，不能只靠台词成立。

背景 Task Turn 在 waiting 或终态已经提交后直接结束，不因工具允许继续 Step 而再调用模型。有效玩家交互仍可继续确认取消等对话；是否继续认知同时受通用工具策略、任务状态和真实交互来源约束。

Phase9 的 `create_task` 只接受本次有效交互中已经校验的 proposal_ref；工具入口先完成预约准入校验，再构造 TaskSpec 调用 TaskService。Task 唤醒不自行创建新的预约。一个 NPC 已有非终态任务时，相同约定返回现有 task_id 和 `created=false`，不同约定返回 `task_conflict`。精确调用重试返回同一响应；同一调用身份携带不同内容返回冲突。

`update_task.intent` 只允许 `wait / cancel`。Task Turn 只修改自身任务；有效玩家交互可以取消该 NPC 的任务。wait 要求尚未确认终态，且等待时间在当前游戏时间之后、不超过 deadline；progress_note 是模型进展说明，不是世界证据。cancel 要求明确原因。各 intent 仅接受对应字段，schema 拒绝额外参数；首版不提供改期、编辑已确认地点或循环预约。

owner、expected_revision、当前运行代次、claim 和访问来源由 Runtime 执行上下文注入，不进入模型输入 schema。expected_revision 使用本次上下文已经读取的任务版本；若在决策期间发生变化，返回 `task_changed` 并刷新上下文，不能偷偷换成最新版本覆盖新事实。两项工具返回已提交的 task_id、revision、state、next_wakeup_at 和已知结果。

任务提交使用当前有效绑定的权威 Clock，重新校验预约/等待窗口；expected_revision 保持模型实际观察值。Clock 更新与读取当前 Clock 后的任务提交在同 world 的短临界区串行完成，模型思考和网络等待位于临界区之外；绑定失效时拒绝旧调用。

### 5.3 TaskService：状态与结果入口

服务内部提供创建 TaskSpec、提交意图、接纳 Evidence、协调 Wake、保存/恢复 Checkpoint 的接口。调用者携带 Runtime 验证后的身份与执行上下文，接口保留通用 owner、时钟、幂等和版本校验；模型工具只是其中一个调用方。

9.2 增加两项内部只读查询：ListActive(ctx, owner, limit) 按 owner 筛选 waiting/running/paused，按 task_id 排序后限制数量；InspectWake(ctx, binding, wakeID) 在同一数据库读事务快照中返回 Head、Wake 和对应 Task，并校验绑定与记录关联。原 List 保持全部状态列表语义，BeginWake 保持 enqueued → running 状态转换。查询不赋予执行资格，不新增模型工具、协议消息、数据库表或任务状态；精确合同见 9.2 第 4.4.1、5.1.1 节。

Phase9 预约的成功由属于本 Task、当前时间线、已接纳且满足结果合同的 `satisfied` 证据触发。`unsatisfied` 触发失败；`interrupted` 或确定性 Runtime 错误按已知结果结束或暂停。进展文字、创建结果、到达固定移动终点及模型自述均不能冒充见面证据。结果映射直接由代码提交，无需 Agent 再调用工具声明成功或失败。

任务取消或终态提交后，Runtime 自动通过 TaskControlRequest 释放仍属于任务的世界操作；已交接给真实对话的占用返回 handed_off，按 UI 生命周期收尾。Sync 的等待注册已经终态，释放其租约不能依赖 CancelAction 取消一个已结束的 Action。断线无法确认释放时记录 `cleanup_unconfirmed`，不伪造成功。

业务终态、History 写入和控制权收尾分别记录。终态事务生成稳定 result_id，重复证据不重复生成结果；后续 cleanup 状态更新不会重新启动任务、改变见面事实或重写已发布结果内容。

### 5.4 状态与原子更新

```text
waiting ──到期或世界事件──> running ──确认继续等待──> waiting
                             ├──结果确认────────> succeeded / failed
                             └──不可安全继续────> paused
waiting / running / paused ──有效取消────────────> cancelled
```

一次进展更新的事务同时执行：

1. 校验 owner、working head、generation 与 expected_revision。
2. 保存进展、已确认事实、结果关联并递增 revision。
3. 消费当前 wake；使旧 revision 的待投递项失效。
4. 写入下一次 wake，或提交终态 / 暂停状态。

提交 waiting 时检查已接纳但未处理的证据；已经出现结果证据时保留立即协调资格，不能用未来 deadline 覆盖它。

`running` 表示 Task 正被 lane 中的协调器处理，不意味着模型一定在运行。进入该状态后，确定性路径也可以直接提交 waiting 或终态。

需要认知的 Turn 正常结束但没有推进 Task 时，协调器标记 needs_reconcile；重新检查事实后仍需认知才安排新 Turn，连续三次认知尝试无法获得可提交进展后进入 `paused / no_progress`。只读协调失败使用有界技术重试；连续三次仍无法确认时进入 `paused / evidence_unconfirmed`。两类计数分别记录，均不重放整段工具调用；技术重试不产生模型调用。

## 6. 持久调度、lane 与崩溃恢复

### 6.1 存储和扫描

新增 `runtime/internal/task`，SQLite 作为唯一权威来源，任务库与 Memory 库分离，按 game/world 隔离。最小表集合：

| 表 | 内容 |
| --- | --- |
| `task_world_heads` | 当前 working head、world_run_id、checkpoint 基点、generation 和暂停状态 |
| `tasks` | 当前任务完整记录、revision、创建幂等凭据与结果 refs |
| `task_wakeups` | wake_id、task_id、预期 revision、due_tick、reason、dispatch 状态与 attempt |
| `task_checkpoints` | 不可变全量快照、schema、game/world、clock、checksum、save_request_id |

为 `clock_id + due_tick` 和 owner 建索引。单 Runtime 扫描器初始按 1 秒现实间隔扫描，WorldClock 更新可以主动触发扫描；使用数据库查询即可完成首版，内存索引仅作可重建优化。

任务数量和 payload 必须有可配置上限，超限拒绝新建；快照过大明确返回失败。Phase9 保留已有检查点，不自动删除旧存档可能引用的快照。任务/检查点数据不得套用 Memory retention 策略。

### 6.2 可靠投递

`wake_id` 对一次持久唤醒固定，Task 的业务 revision 与投递重试 attempt 分开。投递状态为 `pending → claimed → enqueued → running → consumed`。

```text
读取 ready 世界的 due wake
→ 事务认领并保留数据库记录
→ 获取当前绑定的 NPC lane
→ Enqueue 成功后记录 enqueued；失败则恢复 pending
→ lane 真正执行前再次校验 generation、revision、截止时间及 Task 状态
→ 协调已接纳事实，必要时读取当前 Observation
→ 确定性提交进展/结果；确需决策时再启动新 Turn
→ Task 更新事务消费 wake 并写下次 wake / 终态
```

复用 lane Task 的 admission 屏障：队列项在 enqueued 持久提交前不得调用模型或执行动作；提交失败则撤销执行资格并保留重试项。

提交结果不明确时先调用 InspectWake：pending 交回正常调度，claimed 核对原领取身份后继续入队确认或释放，enqueued 才能重试 BeginWake，running 使用已提交的 Task 版本进入证据协调与 FinishAttempt，consumed 结束旧项。查询后状态变化仍由后续操作的身份/版本校验拒绝，running 不再次启动或重放动作。无法确认则停止该 world 新任务准入并报告。投递入口负责有界重试与停止条件，具体状态表见 9.2 第 5.1 节；持续错误可以明确暂停，保留原数据供显式恢复。

- 入队成功不删除 Task，也不等于 wake 已完成。
- 队列满时保留持久投递项，使用有界退避；不能丢任务，也不能忙循环。
- 唤醒协调与玩家输入共用同一 lane，同 NPC 不并发执行任务意图或认知；其他 NPC 可以独立执行。
- 当前 `EnqueueMaintenance` 会合并/替换维护任务，不适合持久 wake，不复用该入口。
- 到期和到达事件竞争时先接纳可校验的世界事实，再按最新 revision 进行一次协调；过期队列项直接丢弃执行资格。
- EventAck 与 Task 协调/Turn 完成分开。携带 TaskEvidence 的事件须先完成持久接纳/去重，再确认已接收，避免确认后丢失唯一结果。
- TaskEvidence 的持久接纳不依赖 lane 空闲；之后只为该 Task 保留一个待协调 wake。与当前 ActionResult 属于同一 operation 的重复进展不重复触发模型。
- 当前 lane 中收到的 ActionResult 直接在当前执行上下文处理；不得再次入同一 lane 并等待自己。
- 接纳确证结果后的状态提交和 cleanup 不经过 LLM。确定性路径直接消费 wake；需要决策的路径分配新 turn_id，在工具执行前取得新 Observation。
- 终态过滤只停止 Task wake。同一事实附带的有效到达交互按交互入口入 lane；可在结果提交后继续对话，同一 fact_id 的重复事件不重复创建交互。

关联 operation 的 TaskEvidence.start_revision 是该世界操作的启动版本。Adapter 按 operation 保存接收到的 TaskActionSource.start_revision，后续回执沿用该值；接纳时与已登记且仍相关的 Operation.StartRevision 比较。Task 从 running 更新到 waiting 后，原等待操作的到达证据仍有效。Task.Revision、ExpectedRevision 和 Result.Revision 保持各自当前状态、修改校验和结果版本语义；无 operation 的 Evidence 沿用 9.1 的当前任务版本校验规则。

本阶段为单实例 SQLite 调度，不增加分布式锁服务。进程实例标识和 CAS 防止并发扫描重复认领；同一任务库同时由多个 Runtime 服务写入应拒绝启动。

### 6.3 不确定执行的恢复

Runtime 在下发有世界效果的命令前，先持久记录 `operation_id`、Task revision、命令指纹与执行意图。当前游戏运行实例内，Adapter 对 operation_id 去重并保留有界操作回执；重试不重复安装控制器。

- `claimed / enqueued` 在进程崩溃后可以重新投递，但仍校验当前世界和 revision。
- `running` 恢复为 needs_reconcile，由协调器直接请求 Observation，查询真实位置、当前控制权和操作回执；确认仍需行动选择时才启动新 Turn。
- 已执行但未收到结果时，不默认“失败所以重发”。能确认结果则关联现有证据；确认未开始且条件有效才允许新动作；无法确认则暂停或以明确中断结束。
- 不恢复原 goroutine、LLM 调用栈、Action waiter 或旧 Turn。恢复的是 Task 意图和已确认进展。
- 读档使用新 generation；存档之外的旧操作回执、旧到达来源和迟到消息不能推进恢复后的 Task。

同一 world_run_id 重新绑定时，允许通过当前连接的 Observation 重新核实已登记 operation 的旧回执，并明确标记其来源代次；这不同于直接接纳旧连接迟到消息。游戏读档只能使用检查点内的已确认事实及新观察，不能引用被回退运行中的结果。

调度保证采用“持久投递 + 版本去重 + 世界效果协调”，不承诺跨进程、跨连接的端到端 exactly-once。

## 7. 点位、预约校验与世界能力

### 7.1 语义点位与当前事实

`adapters/stardew/assets/landmarks.json` 保存 `landmark_id`、名称、地图、停留格、开放窗口、受支持路线和 `departure_lead_minutes`。提供 `beach_meeting_spot`、`saloon_meeting_spot`、`town_square`。9.0 验收记录中的 Linus：Mountain → Town → Beach (28,36) 为沙滩路线基线，departure_lead_minutes=240。9.3-A 收集酒馆、广场及新增起点的候选坐标，9.3-B 验证真实路线、耗时和原生恢复后纳入生产配置；用户可提供点位，开发侧完成路线验证。未验证路线不进入 supported_routes，三处生产点位与对应证据均为 9.3 交接要求。

静态 schema 提供稳定 ID 和含义，动态可达性在动作执行时重新校验。模型不传地图坐标；真实玩家/NPC 位置复用现有 Observation，无需独立位置查询工具。

`Observation.state.stardew` 增加：

| 字段 | 权威内容 |
| --- | --- |
| `festival / current_event` | 当前节日、事件接管及 known 标记 |
| `current_task_execution` | 当前世界操作的 task_id / operation_id、地点、状态、租约 |
| `last_task_execution_result` | 本运行实例最近已知的到达、等待、见面或中断事实 |
| `control_state` | 当前占用者与可交互条件 |

未来任务的 scheduled 状态由 Runtime Task Context 提供，不在 Adapter 中复制一份任务列表。现有 `schedule` 保持原生日程含义。

### 7.2 resolve_meeting：当次校验

`resolve_meeting` 为 Sync + Sequential、只读世界能力。输入 `landmark_id`、`target_date {year, season, day_of_month}`、`start_time`、`end_time`；身份从有效玩家交互来源取得。

Adapter 在主线程校验目标日期、节日、开放窗口、支持路线和行程预留，返回 TaskProposal 及可读约定。时间输入采用 HHMM、10 分钟刻度，窗口在目标日 06:00–22:00 内且服从地点限制；先转换分钟再计算。允许未来任意受支持日期，不设“当天创建当天到期”限制。

Proposal 含规范化 `wake_at = departure_at`、`deadline_at = end_at` 和 opaque 约定；`start_at`、目标点位、参与玩家与到达规则也保存在约定中。当前时间必须早于出发时间。模型只能在取得成功校验结果后创建 Task。

错误明确区分 `invalid_date`、`invalid_time_window`、`invalid_landmark`、`insufficient_travel_time`、`festival_conflict`、`availability_unknown`、`route_unavailable` 和来源错误。目标日是节日时整日拒绝；节日信息未知时不按普通日放行。该能力不承诺未来路线必定畅通，到期执行仍须重新观察。

### 7.3 move_to_landmark：现在出发

`move_to_landmark` 为 Async + Sequential，输入为 `{landmark_id}` 且拒绝额外字段。模型选择已确认约定中的 landmark_id；Task 身份、operation_id 和约定约束由 Runtime source 注入。Adapter 校验目标与 task_contract 一致、出发时间已到、当前世界条件及 NPC 可控性，成功后立即开始实际跨地图移动。

移动开始前持久保存 operation 意图；实际进展由 Adapter 回报为 TaskEvidence。SUCCEEDED 只证明到达目标点位，不能结束“实际见面”任务。到 start_at 时先采样真实位置，恰好抵达合法；此时仍未到达则 `interrupted / arrival_deadline_missed`，不能记为玩家爽约。

路线须在玩家不跟随、目标地图不在玩家视野内时也能正常推进。跨地图执行不扩大现有 `move_to` 的同地图坐标合同；不以瞬移或玩家点击推动过图代替路径执行。

到达后允许有界停留，等待同一 Task 的后续命令；连接关闭、命令交接失败或技术超时均释放。不得出现移动 Action 已完成却遗留无期限的临时控制器。

### 7.4 wait_for_player：当前地点的有界世界效果

`wait_for_player({})` 为 Sync + Sequential，输入为空对象并拒绝额外字段。地点、参与玩家及窗口从 Runtime 注入的 task_contract 取得，模型不能重写截止时间。它仅在 NPC 已实际到达该 Task 点位时，安装当前生效的停留租约和玩家到达监听，返回 operation_id / lease_id 及 `waiting` 回执；Action 随即终态，不等待数小时。

此回执携带规范化 progress Evidence 与 `wait_until=end_at`。Runtime 的结果处理入口自动在同一 Task 更新事务中保存进展并设置截止 wake，随后结束当前 Task Turn；不要求模型再调用 update_task 登记已经成立的等待。玩家到达事件可提前唤醒；未收到事件时 deadline 唤醒由协调器重新观察、确认结果并收敛任务。

- 等待窗口为 `[start_at, end_at)`；NPC 提前到达可以停留，到 start_at 才识别会面。
- 玩家与 NPC 同地图、曼哈顿距离不超过 2 且在窗口内，产生一次 `met` 事实。
- `now >= end_at` 时先截止，再检查新到达；恰好截止到达计为未按时见面。
- NPC 按时到达且等待未中断、玩家始终未满足条件，产生 `expired`。
- 路径失败、控制权丢失、离线清理或证据不完整产生中断/未知，不伪造 `expired`。
- 跨阈值跳时按当前事实协调；跳过完整窗口且未执行过等待的任务不能产生“等了一小时”的结果。
- 监听只对当前生效操作工作；截止、安全取消、断线、日终或 world 切换直接释放自己的租约，不依赖模型及时响应。
- `met` 可把停留交接给有效到达交互；后续对话失败不抹去已发生的见面事实。

Adapter 维护的仅是当前操作、有限回执和安全截止，不持久保存未来约定、不负责在未来日期开始行程。未来任务跨日继续保存在 Runtime；DayEnding 只收尾当日世界操作。

### 7.5 领域进展与证据

| 领域进展 | 事实来源 | Task 处理 |
| --- | --- | --- |
| scheduled | Runtime 创建事务 | waiting，首次 wake 为 departure_at |
| travelling | 实际安装行程后的回执 | 当前短 Turn / Action 执行；记录 operation |
| waiting | 真实到达并安装等待租约 | 按 progress/wait_until 回执自动提交 next_wakeup_at=end_at，结束当前 Task Turn |
| met | 游戏侧真实玩家到达 | 按 satisfied 证据自动提交 succeeded；有效到达交互可另启新 Turn |
| expired | 真实等待完整收敛且未见面 | 按 unsatisfied 证据自动提交 failed，记录原因并收尾 |
| interrupted | 路线、控制权、事件或执行条件中断 | 根据已确认结果结束或暂停，收尾不依赖模型 |

若到达证据已持久接纳，随后截止 wake 不得将 met 改写成 expired。Task 在见面确认后由服务提交成功终态；到达对话是否生成、模型是否可用均不改变该事实。已合法交接给交互生命周期的占用由真实对话结束释放，不随 Task 终态提前撤销。

## 8. approach_player 能力合同

`approach_player({})` 表达“走到动作开始时玩家位置旁的合法格子”。声明 Async + Sequential，沿用现有异步 Action 生命周期。

```json
{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}
```

### 8.1 目标选择与执行

1. 主线程校验有效玩家交互或 met 到达来源、world、entity、运行代次、玩家身份及同地图条件；不套用 move_to 的两格距离前置条件。
2. 检查控制权；travelling / waiting 的占用返回冲突。met 后只允许同一 Task 的有效到达来源原子交接停留控制权。
3. 同一次主线程处理读取玩家位置，按上、右、下、左枚举曼哈顿距离为 1 的格子。
4. 排除越界、不可通行、被其他实体占用和不可达的格子；NPC 自己所在格不视为其他实体占用。
5. NPC 已处于合法相邻格时直接完成。否则比较 Game 给出的真实路径长度，选择最短者，并列按上、右、下、左排序。
6. 固定玩家位置快照、目标格和选定路径；执行后不因玩家移动重新选点、重新寻路或触发额外模型决策。

候选阶段可以分别寻路。选定之后玩家移动或换地图，NPC 继续前往固定终点；原路径失效则终止，不自动寻找替代目标。世界重载、取消和控制权丢失仍会中断动作。

### 8.2 结果与错误

SUCCEEDED 仅表示抵达固定终点，output 必须包含：

| 字段 | 含义 |
| --- | --- |
| player_position_at_start | 选点时玩家的 location / tile |
| target_tile | 选定整数 x / y，地图为开始时共同地图 |
| final_position | 结束时 NPC 的真实 location / tile |
| player_is_adjacent | 完成时与来源玩家同地图且曼哈顿距离为 1 |

SUCCEEDED 与 `player_is_adjacent=false` 可以同时成立；后续对话仍检查实时距离。

| 条件 | 明确结果 |
| --- | --- |
| 额外参数、无效来源、错误 world/entity/generation | REJECTED，invalid_action_arguments / source_invalid / world_mismatch |
| 不同地图 | REJECTED / different_location |
| 无合法可达相邻格 | REJECTED / no_reachable_adjacent_tile |
| 占用不可交接 | REJECTED / npc_control_busy |
| 原路径失效 | FAILED / move_failed |
| 控制器被替换 | INTERRUPTED / control_lost |
| 重新加载或切换世界 | INTERRUPTED / world_changed |
| 取消或超时取消 | CANCELLED，保留取消原因 |

每个 Action 终态只发送一次。清理只释放自身控制器，不对其他控制器执行 Halt。选点/启动失败时保留仍有效的原交互占用，否则恢复原生行为；移动成功与否均不修改已发生的 met。

## 9. 来源、控制权与真实对话结束

### 9.1 两类执行来源

玩家交互仍使用现有 InteractionContextStore。Task 唤醒使用受信的 TaskActionSource，仅授权该 Task 当下所需的移动、等待与释放；它不是任意距离打开玩家 UI 的许可。

世界 GameEvent 使用实际发生时间、完整 NPC EntityRef、固定 event_id 和 TaskEvidence。当前 Task 的 deadline wake 可以更新结果，不能凭空获得“玩家就在附近”的来源。

met 到达事件建立独立交互来源，关联 world/run/entity/task/operation/player。EventAck 后才可执行到达交互；动作执行时再次校验。上午的交互快照不能作为未来赴约时的凭据。Task 已 succeeded 时该真实交互来源仍可有效；它只授权当前交互，不重新开放已完成 Task 的出发、等待或任务修改。

- present_dialogue 检查实时同地图、两格范围、菜单冲突和有效到达/玩家来源。
- approach_player 检查有效来源和同地图，开始时不要求仍在两格内。
- 背景事件未获交互授权时拒绝 UI 能力；不覆盖玩家其他菜单。
- 队列延迟后玩家已离开时保留到达事实，但拒绝失效的 UI。
- 事件使用 world_run_id / fact_id 去重并校验来源 generation；操作回执以 world_run_id / entity / operation_id 唯一，保存原始 generation 和命令指纹。同 run 协调可重新核实旧回执，读档后的同名任务不接纳被回退结果。

### 9.2 共享控制权

`NpcControlLease` 按 NPC 互斥协调原生行为、普通交互、跨地图移动、等待、move_to 与 approach_player。取得、交接、释放都带 owner token；释放前检查实际 controller 仍属于本操作。

travelling / waiting 时，独立 move_to 或 approach_player 返回 `npc_control_busy`。met 到达交互可将本任务停留租约交给 approach_player，并保留交互来源。完成后来源仍有效且玩家在交互范围内则交回交互占用，否则恢复原生行为。

接近途中玩家距离变化不取消固定终点；所属 Turn 在动作未完成时终止、技术等待超时、断线和世界重载会安全收尾。取消或截止释放仍属于任务的占用；已交接的交互按真实 UI 生命周期结束，Task 成功或原预约 deadline 不提前关闭它。其他 Mod 或游戏接管时保留其 controller，并记录 control_lost。

### 9.3 对话生命周期与行为恢复

普通对话、无预约的接近后对话、赴约对话统一遵循：

**最后一段台词和回复 UI 真正结束，且没有有效回复接续或在途动作后，在下一次可运行的主线程更新中释放本次临时控制，由游戏继续当前日期和时刻的原生日程。**

- present_dialogue 的 ActionResult 表示展示成功，TurnCompletion 表示认知结束，均不等于玩家已读完对话。
- 回复提交结束当前 presentation，但继续同一 conversation；跨 Turn 保持交互停留。
- 最后台词关闭且无回复、退出回复菜单/自由输入、交互取消/失败关闭均汇入幂等结束入口。
- 没有后续对话的接近动作，在 Turn 结束且没有 UI/回复接续/在途动作后同样释放。
- Thinking / Waiting 期间 NPC 保持有效停留；输入抑制先于原版互动，不在 LLM 台词前插入原版台词。
- 技术等待 Runtime 最长 120 秒；正常阅读台词和填写回复不计入技术等待超时。
- NpcInteractionLifecycle 以 world/run/entity/conversation 管理占用，独立于按 event/turn 释放的来源快照；旧回调不能关闭新交互。

`NpcNativeBehaviorRestorer` 只撤销自己仍拥有的暂停、朝向和移动状态，按当前日期/时间重新衔接原生日程。恢复不需要额外模型调用、玩家点击或随机移动命令；不传回上午位置，不仅以置空 controller 作为完成。

原生日程本来要求驻足时允许正常停留；实机验收选择原生应移动的时间，观察 NPC 真实继续行程。释放失败记录具体错误并停止反复覆盖控制器；永久卡住 NPC 属于阶段阻断问题。

## 10. 最小检查点系统

### 10.1 保存对象

Runtime 当前任务工作集持续提交到 SQLite；每次游戏保存另外创建不可变全量 Task 快照。快照包含该 game/world 下的任务集合、终态与非终态记录、进展、后续 wake、已确认结果及 result_id、创建幂等凭据及 operation 关联。恢复所需的 TaskSpec、结果合同、Proposal 和 Evidence 内容随快照保存，不依赖 History 原文或临时回执仍存在。

不序列化 goroutine、Turn 栈、LLM 请求、NPC 对象、controller、UI、网络 waiter 或 lane 队列。运行中任务在快照中标记 needs_reconcile，保留已有事实；恢复后重新观察。

游戏 SaveData 键为 `runtime-task-checkpoint`，保存：

```json
{
  "schema_version": 1,
  "game_id": "stardew-valley",
  "world_id": "world-example",
  "status": "confirmed",
  "checkpoint_id": "checkpoint-example",
  "checksum": "sha256-of-runtime-task-snapshot"
}
```

`confirmed` 表示此标记引用的快照已在 Runtime 持久提交；只有该标记成功随游戏保存后，才构成可加载的检查点。`unconfirmed` 标记附带原因，不带可执行的 checkpoint_id，不能沿用上一次 confirmed 引用冒充本次状态。

首次接入、存档没有该键时：仅在该 game/world 的任务库也没有既有状态时初始化空任务集；已有状态却没有引用则暂停并提示，不能猜测这是新存档还是旧存档。

### 10.2 保存屏障与顺序

```text
Saving：暂停本世界的新任务修改及世界命令，清理本次保存的临时游戏占用
→ CheckpointPrepare(save_request_id, binding, clock)
→ Runtime 在 world 短临界区内同步采样 Clock，再由 PrepareCheckpoint 事务接纳 final_evidence、fence 旧执行并持久化快照
→ CheckpointPrepared(新 generation, checkpoint_id, checksum)，执行准入仍关闭
→ Adapter 在本次 SaveData 中写引用 → 游戏完成保存
→ Saved / aborted → 使用 Prepared 新代次的 CheckpointFinish
→ 同 run 的 WorldBinding / WorldBindingReady 握手 → 重新观察未确认操作后调度
```

屏障覆盖当前世界的任务创建/更新、wake 投递和 Action 发送；其他世界不受影响。已经发生的效果保留，未确定结果记为待协调。旧 LLM 返回不能在屏障后提交到新 generation。

Prepared 只确认持久快照和新 generation，Adapter 在保存结束后的当前 WorldBindingReady 才恢复执行准入；Finish 发送完成不等于 Runtime 确认。首次 Prepare 前同步 Clock，同请求重试保留原参数并走内核幂等路径。Prepared 超时/丢失时写 unconfirmed，不猜测代次发送 Finish；同 run 通过有界屏障兜底和绑定恢复选择 working head，仍不改写磁盘标记，新 run 加载 unconfirmed 仍暂停。加载引用、迟到回复和握手合同见 9.4 第 3.4 节。

保存不等待模型完成，也不要求所有 Task 终态。SMAPI 主线程只进行有限保存交接，网络接收和 SQLite 快照不依赖主线程回调；请求必须有有限超时，超时写 unconfirmed 并允许游戏保存。具体 Saving / Saved 时序与有界等待的可行性在 9.0 实测，禁止引入主线程—网络相互等待的死锁。

CheckpointPrepare 按 save_request_id 幂等；迟到响应不能改写已结束的保存。Saved 确认丢失时，Runtime 可以在有界屏障超时后解除暂停；下次加载以磁盘游戏存档里的精确引用为准，不以是否收到 finish 判定快照有效。

### 10.3 崩溃边界

| 故障点 | 行为 |
| --- | --- |
| 快照提交前失败 | 写 unconfirmed；游戏可保存，任务恢复暂停 |
| 快照提交成功，游戏保存失败 | 快照成为未引用记录；现有存档仍引用原检查点 |
| 游戏保存成功，Saved 确认丢失 | 加载可按已写入的 checkpoint_id 正常恢复 |
| Runtime 断线 / Prepare 超时 | 写 unconfirmed，保留任务 DB，不声称与此次游戏保存一致 |
| 引用不存在、checksum 错、schema 不支持、world 不匹配 | 暂停任务恢复并提示；保留原数据，不退回“最新数据库版本” |

游戏文件与 Runtime SQLite 不组成分布式事务。最小版保证精确引用、先持久后引用、故障可辨认和不误执行；不提供断线时完整保存任务的 Adapter 镜像。

### 10.4 加载与重启

收到新的 world_run_id 时：

1. fence 旧绑定、Turn、wake 和回调；释放 Adapter 旧占用。
2. 校验游戏保存的标记与目标快照。
3. 在事务中按快照完整替换当前 working set；新建 working head，不修改原快照。
4. 将保存时在途项规范化为 needs_reconcile，重新生成当前代次可执行的 wake；不恢复旧内存投递状态。
5. 绑定最新 clock / EntityRef / capabilities，完成协调准入后开启任务调度。

以下是必须成立的回档语义：

| 保存时 / 保存后的变化 | 读取该保存后的 Task |
| --- | --- |
| 保存时不存在，之后创建 | 不存在 |
| 保存时 waiting，之后成功或取消 | 恢复保存时 waiting、progress 和 next_wakeup |
| 保存时已完成 | 保持完成，不再触发 |
| 保存时已有进展，之后更新了下一次唤醒 | 恢复保存时的进展和唤醒点 |
| 多次变化发生在同一游戏分钟 | 仍按 checkpoint_id 区分，创建时间相等不构成歧义 |

created_at 只提供审计与筛选，不能替代这些状态恢复规则。保存后新建任务在 working set 中消失，不要求物理删除旧不可变快照或审计资料。

Runtime 重启但 world_run_id 未变时，恢复最新 working head；不能回到上次日终检查点。working head 与游戏运行标识不能匹配时暂停并要求明确加载绑定。

### 10.5 断线及 Memory 边界

Runtime 断线后 Adapter 立即停止任务交互等待和当前临时世界操作，释放自身控制权；不保证 NPC 继续未来赴约。已发生事实尽可能保留在当前运行实例回执，自动跨连接补发不属于本阶段保证。

断线期间游戏保存为 unconfirmed。再次加载后任务功能暂停；即使 Runtime DB 中有较新的 working head，也不能自动用于该保存。诊断展示 game/world、标记状态和原因，不从聊天记忆推断任务。

Task 检查点与 Phase8 Summary 检查点是两套独立语义。Phase8 继续其时间可见性与历史恢复规则，不承诺任意分支的所有聊天均回滚。Task Context 显式提供当前任务权威状态和恢复情况；Memory 中的承诺或旧结果不得创建、推进或复活任务。

## 11. 游戏生命周期、结果与可观测性

### 11.1 主线程接线

| 入口 | 行为 |
| --- | --- |
| GameLaunched | 加载点位配置与 capability schema |
| SaveLoaded | 新建 world_run_id；读取检查点标记；发起绑定与恢复 |
| DayStarted | 更新日期/clock；保留 Runtime 中未来 Task；不创建新 run |
| TimeChanged | 发布 clock，执行当前等待租约的时间边界 |
| UpdateTicked | 真实移动、玩家到达、UI 结束、操作回执与原生恢复 |
| DayEnding | 收尾当日临时世界操作；未来 Task 继续等待 |
| Saving / Saved | 保存屏障、精确引用写入、完成确认 |
| ReturnedToTitle / Dispose | 释放当前占用、撤销绑定和来源；保留已保存任务数据 |
| Runtime stream 关闭 | 清理当前操作与等待 UI；停止新 Task 执行，保留诊断 |

Game API、存档读取、位置和 controller 操作都在主线程。网络线程处理传输与确认，通过带代次的消息交回主线程。

### 11.2 事件、Context 与 History

游戏可以使用 `task_execution_changed`、`player_arrived_at_meeting` 等事件名；Runtime 依据通用 TaskEvidence 和 target 路由，不按这些名称分支。ContextFact 使用通用 interaction kind，scope_id=task_id，文本明确实际到达、等待、见面或中断。

当前 Task Context 包含目标、权威进展、wake_reason、游戏时间、最近证据及终态结果、剩余窗口和恢复状态，参与现有 Context 预算。需要决策的 Task Turn 使用新 Observation；只处理已经确认的结果时不额外调用模型或 Observe。恢复/截止但证据不足时由协调器读取当前 Observation，不把创建时的快照固定复用到未来。

9.2 为普通玩家交互和 Task Turn 提供同一最小任务投影：task_id、目标、state/revision、next_wakeup_at、deadline_at，以及入口所需的 wake_reason/暂停原因，计入既有请求预算。普通交互调用 ListActive(owner, 1)，先筛选非终态再限制数量；Task Turn 按自身 task_id 读取并核对执行上下文。9.4 在同一读取入口和投影上扩展近期结果、恢复信息及 Task Context 分项预算。9.4-D 增加内部 ListRecentResults(ctx, owner, limit)，从当前工作集按 owner 筛选已确认终态结果，以 OccurredAt 倒序、Result.ID 升序排序后限制数量；投影取最多三条。读档后不合并旧 checkpoint/History 的回档外结果，保持 List / ListActive 语义，不新增协议、表或协调组件；接口、并发边界与测试见 9.4 第 6.1 节。

History 增加通用 `task_result` 来源类型，接收 TaskService 已提交的不可变结果；不创建空模型 Turn，不伪造 NPC 台词或 terminal_turn：

- 来源包含 owner、result_id、task_id、结果对应的 revision、发生游戏时间、outcome/reason、证据 refs 与真实 ContextFact；已有关联 event/turn/operation 时保留这些 ID。
- result_id 在终态事务中生成且重试复用；History 批次键为 owner + result_id + kind/version。读档后新发生的结果使用新 result_id，不覆盖原时间线的来源。
- task_result 可没有 turn_id；既有 terminal_turn / legacy_recent 的字段、批次键与指纹保持兼容。文本投影明确区分世界结果、Runtime 状态和模型意图，复用现有 History 读取、时间可见性、预算和检索合同。
- 终态提交后通过现有 AppendHistory 写入结果，不等待到达对话成功。任务结果与到达交互 History 可关联同一 fact_id，分别保留事实及真实对话，不重复提交业务终态。

TaskEvidence 接纳、业务终态和 History 写入分别可诊断。History 失败不回滚已发生的游戏效果或已提交 Task，Task Context 仍可提供持久结果；本阶段不增加全局跨连接 History 补发。Phase9 只接入该来源类型及必要兼容测试，Summary、检索和留存策略沿用 Phase8。后续对话必须检查最终模型请求确实含实际结果来源，而非仅检查 NPC 台词。

### 11.3 诊断字段

统一关联 `game_id / world_id / world_run_id / execution_generation / entity_id / task_id / revision / wake_id / operation_id / event_id / turn_id / action_id / fact_id`。

记录 clock、next_wakeup、dispatch 状态、协调原因、是否需要 cognition、tool kind / 执行位置、result_id、checkpoint_id / save_request_id、恢复路径、失败原因和控制权释放结果。对话另记录 conversation_id、实际 UI 结束及原生日程恢复；以实机行为核验日志，不以日志声明替代 NPC 移动证据。

## 12. 组件与文件边界

以下列出组件级边界；每个子阶段的文件表进一步定义拆分文件、精确接口及测试接线。标记“新增”的文件不表示已存在。

| 目录 / 文件 | 类型 | 责任 |
| --- | --- | --- |
| `runtime/internal/task/model.go`、`store.go`、`sqlite_store.go` | 新增 | TaskSpec / TaskRecord / Wake / Evidence / Result 值对象、事务、幂等、working head |
| `runtime/internal/task/service.go` | 新增 | 内部创建/意图/证据入口、确定性状态转换、服务端 CAS 与结果生成；9.2-B2 增加 ListActive 及 sqlite_store 查询 |
| `runtime/internal/task/wake.go` | 扩展 | 9.2-C2 增加 InspectWake，model.go 定义 WakeInspection；保持既有 wake 状态转换 |
| `runtime/internal/task/admission.go` | 新增 | 通用任务准入、owner 数量限制与幂等校验 |
| `runtime/internal/tool/task_tools.go`、`proposal.go`、`runtime_tool.go` | 新增 | Runtime Tool schema、参数解析、校验结果引用、TaskSpec 构造与 TaskService 调用 |
| `runtime/internal/task/dispatcher.go`、`checkpoint.go` | 新增 | 扫描认领、协调/认知分流、恢复分类、全量快照和保存屏障 |
| `runtime/internal/task/result_history.go` | 新增 | 已提交结果到通用 History 来源的投影与写入，保留失败诊断 |
| `runtime/internal/gateway/world_registry.go`、`task_dispatch.go` | 新增 | 活动世界、内部 trigger、lane 绑定和控制消息 |
| `runtime/internal/gateway/gateway.go`、`runtime/cmd/server/main.go` | 修改 | 进程级服务装配、连接解绑、任务库生命周期 |
| `runtime/internal/session/lane.go` | 修改 | 安全复用 admission / abort；保持 FIFO 和维护任务合同 |
| `runtime/internal/tool/registry.go` | 新增 | 合并 Runtime 与 Environment 注册项，按显式 kind 查找；复用现有准入预算，拒绝同名冲突 |
| `runtime/internal/tool/types.go`、`environment_catalog.go` | 修改 | KindRuntime、共用 Tool View / policy 与现有 Environment catalog 接入 |
| `runtime/internal/agent/scheduler.go`、`loop.go` | 修改 | kind 执行分流、Task Turn、状态收敛后的认知停止、操作提交前记录与结果关联 |
| `runtime/internal/agent/task_context.go` | 新增 | 普通交互与 Task Turn 共用的当前任务快照；9.4 扩展结果与恢复信息 |
| `runtime/internal/context/`、`runtime/internal/trace/` | 修改 | 有界 Task Context、通用关联字段及诊断 |
| `runtime/internal/memory/history.go`、`history_text.go`、`in_memory_history.go`、`sqlite_history.go` | 修改 | task_result 来源校验、存取及文本投影；保持 Phase8 来源键、指纹和策略兼容 |
| `protocol/proto/gameagent.proto`、`protocol/gen/go/` | 修改 / 生成 | 第 4.3 节合同、兼容和生成结果 |
| `adapters/stardew/assets/landmarks.json` | 新增 | 点位、开放窗口与路线预留 |
| `adapters/stardew/src/Tasks/MeetingContract.cs`、`GameClock.cs`、`LandmarkCatalog.cs` | 新增 | 游戏日期换算、约定校验和配置 |
| `adapters/stardew/src/Tasks/TaskExecutionDriver.cs`、`MeetingWaitMonitor.cs` | 新增 | 当前跨地图命令、等待监听、真实证据 |
| `adapters/stardew/src/Tasks/TaskSourceContextStore.cs`、`TaskOperationReceipts.cs` | 新增 | 来源、当前运行实例幂等回执、到达交互授权 |
| `adapters/stardew/src/Tasks/TaskCheckpointBridge.cs` | 新增 | SaveData 引用、保存交接与恢复状态；不保存任务镜像 |
| `adapters/stardew/src/Capabilities/ResolveMeetingCapability.cs`、`MoveToLandmarkCapability.cs`、`WaitForPlayerCapability.cs` | 新增 | 第 7 节输入、执行及结果合同 |
| `adapters/stardew/src/Capabilities/ApproachPlayerCapability.cs`、`AdjacentTileSelector.cs` | 新增 | 固定目标选择与异步接近 |
| `adapters/stardew/src/Capabilities/NpcControlLease.cs`、`NpcNativeBehaviorRestorer.cs` | 新增 | 共享控制权及当前时刻原生恢复 |
| `adapters/stardew/src/Capabilities/MoveToCapability.cs`、`PresentDialogueCapability.cs` | 修改 | 复用控制权、真实展示与结束区分 |
| `adapters/stardew/src/Dialogue/NpcInteractionLifecycle.cs` | 新增 | 跨 Turn 交互占用、回复接续与幂等结束 |
| `DialoguePresentationFlow.cs`、`DialogueInteractionController.cs`（同 Dialogue 目录） | 修改 | UI 结束信号和恢复接线 |
| `adapters/stardew/src/Runtime/RuntimeClient.cs`、`RuntimeWorldScope.cs`、`CapabilityCatalog.cs`、`ProtocolMapper*.cs` | 修改 / 新增局部 Mapper | 世界绑定、Task 来源、协议映射、capability 与断线清理 |
| `adapters/stardew/src/State/`、`src/ModEntry.cs`、`GameAgent.Stardew.csproj` | 修改 | Observation、主线程事件、装配和资源输出 |

测试目标：

- 新增 `runtime/internal/task/{service,admission,sqlite_store,dispatcher,checkpoint,result_history}_test.go`；通用 TaskSpec、意图/证据分离、纯时钟、事务、重启和故障注入。
- 9.2-B2 在 service/sqlite_store tests 覆盖 ListActive 的筛选顺序、暂停任务、边界和 owner 隔离；9.2-C2 在 wake/dispatcher/gateway tests 覆盖 InspectWake 的只读一致快照、BeginWake 提交成功但调用结果不确定及查询后状态变化。
- 新增 `runtime/internal/gateway/task_dispatch_test.go`、`world_binding_test.go`；内部 wake、queue full、第二 NPC/world。
- 新增 `runtime/internal/tool/registry_test.go`、`task_tools_test.go`，修改 scheduler / loop / Context tests；新增 `runtime/internal/agent/task_context_test.go`、`task_loop_test.go`，覆盖新对话取消当前任务，使用非 Stardew 名称证明 kind 路由和策略通用性。
- `runtime/internal/gateway/task_e2e_test.go` 在 9.2 使用真实 gRPC/临时 SQLite 与 fake environment/model 证明阶段闭环，9.5 扩展 History、保存和交互生命周期组合场景。
- 新增 `runtime/internal/memory/task_result_history_test.go`；覆盖无 Turn 来源、幂等冲突、旧数据读取、时间可见性、文本检索及混合来源 Summary 回归。
- 新增 `adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj`；链接纯模型、Fake Driver、时钟、选点、等待、控制权及保存桥接逻辑，不依赖游戏 DLL。
- 在该项目增加 `MeetingContractTests`、`MeetingWaitMonitorTests`、`AdjacentTileSelectorTests`、`TaskSourceContextStoreTests`、`TaskCheckpointBridgeTests`。
- 扩展现有 `ProtocolMapper.Tests`、`PlayerInteractProbe.Tests`、`ActionCancellationRegistry.Tests`，覆盖新合同、UI 接续和旧能力回归。
- 同步 `adapters/stardew/tests/check-context-static.ps1`、Protocol checks 与架构检查；真实 pathfinding / SMAPI 保存时序另做实机验收。

## 13. 分模块开发流程与子阶段

Phase9 使用 9.0 前置可行性门和 9.1–9.5 五个开发子阶段，以完整子阶段作为开发、交付和用户验收周期，小型提交作为内部质量控制和后续审查单位。每个独立模块涉及代码时先写失败测试，再实现；完成相关测试、直接受影响的回归及 `git diff --check` 后，创建一个包含实现与测试的本地提交并连续推进。阶段代码完成后统一执行 agent 内部 CR，问题由独立 `fix:` 本地提交修复并复验。协议与生成文件处于同一提交，每个提交保持可构建、可测试。

模块内按改动范围验证；子阶段代码完成后，agent 执行阶段整体自动化回归和阶段级内部 review，修复并复验后交付用户集中 CR、review 与该阶段适用的实机验收。验收通过且用户确认后进入下一阶段。9.3 交付当前运行中的真实预约闭环，9.4 增加保存、恢复与结果认知能力；9.1/9.2 作为必要基础设施按各自机制合同验收，不将 fake 证据当作实机通过。Phase9.5 执行最终整分支/系统审查和真实游戏组合验收，不替代各阶段验收。所有本地提交均不得 `git push`，除非用户明确授权。

### 13.1 子方案与交付顺序

| 子阶段 | 技术方案 / 责任 | 继续条件 |
| --- | --- | --- |
| 9.0 基线与可行性 | 本节的代码基线、跨地图、原生日程恢复及保存交接探针 | 明确真实游戏 API 和可行性；不存在瞬移、无期限占用或保存死锁 |
| 9.1 Task 内核 | [Runtime Task 内核](./GameAgent%20MVP0%20Phase9.1%20Runtime%20Task%20内核技术开发方案.md)：值对象、内部服务、SQLite、revision、幂等、wake 和全量快照 | 纯内核与故障测试通过；公开接口可供 9.2 调用 |
| 9.2 Runtime 接入 | [Runtime Tools 与调度接入](./GameAgent%20MVP0%20Phase9.2%20Runtime%20Tools%20与调度接入技术开发方案.md)：协议、工具分流、Clock、世界绑定、lane、Task Turn 与最小当前任务上下文 | 创建 Turn 结束后独立唤醒；普通对话可取消任务；确定性结果无需模型；阶段回归通过 |
| 9.3 游戏闭环 | [Stardew 行动与交互](./GameAgent%20MVP0%20Phase9.3%20Stardew%20行动与交互技术开发方案.md)：点位、校验、移动、等待、接近、来源、租约、UI 结束和原生恢复 | 9.0 已通过；当前运行中的真实预约行为成立；游戏侧自动化通过 |
| 9.4 恢复与认知 | [检查点与结果记忆](./GameAgent%20MVP0%20Phase9.4%20检查点与结果记忆技术开发方案.md)：保存桥接、精确恢复、结果 History，扩展既有 Task Context 的结果与恢复投影 | 9.0 已通过；跨日保存/加载和故障窗口通过；后续请求含实际结果 |
| 9.5 验收与交付 | [联调验收与交付](./GameAgent%20MVP0%20Phase9.5%20联调验收与交付技术方案.md)：全量回归、真实模型/游戏、整分支/系统 review、修复和复验 | 成功赴约/爽约各连续三次，无阻断 review 问题，最终产物与证据一致 |

公共约束由本总方案定义；9.1 固定内核接口，9.2 固定 wire 与工具合同，9.3 固定游戏执行接口，9.4 固定保存与 History 接入，9.5 固定最终验收。跨阶段接口变更时同步实现、调用方、测试与相关文档，不让两个阶段保留不同合同。

9.1 保持 Accepted；当前阶段所需的最小通用接口扩展归入当前阶段。后续开发按用户确认的阶段范围连续推进，阶段代码完成后统一执行 agent 内部 CR；阶段交接时交付审查材料并暂停，由用户统一审查与验收。9.2 的提交顺序固定为 A1 协议 → A2 映射 → B1 工具分流 → C1 绑定/Clock → B2 任务工具/上下文 → C2 持久投递 → D 阶段闭环。

ListActive 的实现与测试纳入 B2，InspectWake 的实现与测试纳入 C2，提交模块数量保持七个。9.2 完成 TaskControl 的 Runtime 侧和 Checkpoint 协议/映射；真实行动、等待与释放在 9.3 实现，完整保存桥接、恢复和结果记忆在 9.4 实现。

### 13.2 9.0 前置可行性门

9.0 是连续执行的第一项技术检查，不单独扩展产品功能：

1. 核对实际工作区、基线 commit、Go/.NET/游戏/SMAPI 版本、游戏程序集和现有部署脚本；运行 Phase6/Phase8 全量基线并保存真实结果。
2. 查阅本地游戏 API，建立最小路线探针。选择受控 NPC 的真实起点和沙滩点位，验证玩家不跟随时的跨地图行走、到达后短停留、按当前时间恢复原生日程；同一路线连续三次。
3. 记录实际地图/格子、所用 API、路线耗时与游戏时间提前量。正常出口过图允许使用游戏机制，不得跳过行走直接放到目标格。
4. 验证 Saving 内有限等待与网络回复不互相等待主线程；覆盖正常成功、故意延迟、断线和保存失败。保存未拿到有效引用时能明确写 unconfirmed 并让游戏继续。
5. 形成 9.3 的 GameNpcDriver/Restorer 实现依据，以及 9.4 的保存线程交接依据，再接入正式实现。

探针通过受控测试入口运行，不暴露为模型工具，不作为预约创建或完成证据。测试入口默认关闭且只操作本次测试取得的控制权。

若缺少游戏环境，可先完成不依赖实机结论的 9.1/9.2 和纯逻辑测试；9.0 保持未通过。9.3/9.4 中依赖真实游戏的代码以 9.0 通过为硬前提，涉及真实 API 的结论不能以 fake 替代。发现跨地图、恢复或保存边界不可行时报告具体阻断，不擅自取消这些验收条件。

### 13.3 阶段执行与验收约定

开发沿用以 `e482923` 为基点的 `codex/phase9-durable-task` 分支，`daf4f98` 为已检查代码基线。执行前读取仓库 AGENTS.md、本总方案和当前子方案，明确阶段范围、验收条件和实机前置条件；按已授权阶段开展工作，已验收阶段保持其结论。每个模块完成测试和 `git diff --check` 后本地提交并连续推进；阶段代码完成后执行整体自动化回归与阶段级内部 review。向用户交付提交清单、变更说明、验证结果、已知限制和实机验收步骤，等待用户集中 CR、review 与实机验收；反馈修复并复验，用户确认通过后进入下一阶段。Phase9.5 执行最终整分支/系统审查、修复和交付产物复验。

保持 Runtime TaskService、Runtime Tools、Environment Capabilities 的分层。保留跨天任务、完整检查点回退、真实跨地图行走、固定目标 approach_player 和真实 UI 结束后的原生日程恢复。禁止通过假回执、瞬移、跳过测试、修改既有通过标准或改成仅当天任务完成交付。

需要新增授权、扩大范围、改变产品规则，或缺少权限、游戏环境、凭据及必要实机前置条件时，报告具体阻断并请求用户决策或协作；可在当前已授权阶段内继续不依赖阻断的工作，不跨越阶段验收门。点位采集、路线探针等实机前置协作可以在开发期间开展，不要求用户逐模块 CR 或验收。保留用户现有修改；本地提交限于已授权的开发范围，未经用户明确授权绝不 `git push`。仓库默认任务配置保持 `enabled=false`；专用演示/本地配置显式启用，不提交凭据或私有配置。真实执行结果写入 `docs/phase9/GameAgent MVP0 Phase9 开发与验收记录.md`；总方案和子方案保持最终合同。

代码完成、自动化通过、实机通过和独立 review 通过分别记录。9.5 第 4.4 节分别指定自动化与实机必需证据：路径、原生恢复、实际保存/加载和 UI 使用实机验证，查询排序、版本冲突及重复回执使用自动化精确断言。成功/爽约各三次是已记录 NPC、路线、日期和配置条件下的独立生产闭环验收，不代表任意游戏条件的普遍可靠性。只有全部满足第 14–15 节条件后，才提供 Phase9 Accepted 的确认材料。

## 14. 验收矩阵与演示

### 14.1 自动化矩阵

| 场景 | 必须成立 |
| --- | --- |
| TaskService 内部入口与预约准入 | 无玩家/Proposal 的 fake TaskSpec 通过内核；生产预约入口仍校验 proposal_ref 与单任务限制 |
| Runtime / Environment Tools 混合 | 共用 Tool View、预算与 policy；按 kind 分流，本地 Task 工具不发送 ActionRequest；名称冲突拒绝 |
| 模型工具修改请求 | 仅接受 wait/cancel 意图；服务端注入身份与 revision；过期上下文拒绝，模型不能指定 succeeded 或 generation |
| 今天创建后天预约；跨季/跨年 | 到期由游戏 clock 唤醒；保存和跨日不取消未来任务 |
| 创建后当前 Turn 结束 | 无跨小时 Turn、Action waiter 或每任务睡眠 goroutine |
| 后续 wake | 等待回执自动原子提交进展与截止 wake；到达事件可提前唤醒，不额外调用模型登记 |
| LLM 不可用，已经有确证结果 | Task 自动提交终态和结果、发起 cleanup；不依赖到达对话或模型确认 |
| 截止缺少真实等待证据 | 协调器直接 Observe；有界技术重试后仍未知则暂停，不虚构 expired、不反复调用模型 |
| 现实时间流逝、游戏暂停 | 不提前出发或缩短窗口 |
| 队列满 / lane 关闭 / NPC 未绑定 | 持久项保留，有限重试；可用后先协调，确需决策才执行新 Turn |
| 同 NPC 玩家事件与 wake | 串行执行；旧 revision 不能覆盖新事实 |
| 重复 create/update/event/Action 回执 | 稳定身份、指纹校验、CAS、证据去重；无重复世界效果 |
| 执行中 Runtime 崩溃 | 协调器重新观察回执，按当前事实恢复；禁止盲目重放动作 |
| 超 deadline / 跳过完整窗口 / 倒退 | 收敛或暂停，不补造行走、等待和玩家爽约 |
| 非法日期/HHMM/点位/节日/未知可用性 | 明确拒绝，创建前不宣告成功 |
| 到达时刻与截止时刻竞争 | 窗口为 [start,end)，已确认 met 不被 deadline 覆盖 |
| NPC 未按时抵达 / 控制权丢失 | interrupted，不归责玩家爽约 |
| 同地图 approach，已相邻 / 占用 / 并列路径 | 直接完成 / 选择其他可达格 / 上右下左决胜 |
| approach 全不可达 / 跨地图 | 明确拒绝，不改变世界位置 |
| approach 途中玩家移动或换地图 | 固定目标/路径；结果反映实际相邻关系；无额外决策 |
| approach 路径失败/取消/旧回调 | 明确终态一次；释放自身 controller，保留他人 controller |
| travelling/waiting 与移动争用 | npc_control_busy；met 后同来源可交接 |
| met 已使 Task 成功，交互尚未结束 | 有效来源仍允许 approach/对话；任务终态和 deadline 不提前回收已交接租约 |
| 台词已展示、Turn 已结束、UI 未关闭 | NPC 保持交互停留 |
| 回复接续 / 退出自由输入 / 最后台词关闭 | 接续保留；真实结束才恢复原生日程 |
| 思考延迟 / 重复点击 | 无 NPC 走开、无原版台词先行、无重复交互 |
| 保存前等待，保存后成功/取消 | 读档恢复等待及原 wake，不只删除新建任务 |
| 同分钟内创建/更新、两个不同检查点 | 按精确快照恢复，不依赖时间戳辨别先后 |
| 保存中任务/事件竞争，迟到 Prepare/LLM 返回 | 屏障和 generation 隔离，无半更新快照或迟到覆写 |
| 快照提交、游戏保存、Saved ack 各故障窗口 | 按第 10.3 节恢复；孤立快照不被误选 |
| unconfirmed / 丢失/损坏/错误 world 引用 | 暂停任务恢复，保留原数据；普通游戏可继续 |
| Runtime 同 run 重启 / 同 world 重新读档 | 分别恢复 working head / 存档快照 |
| 断线 / 日终 / 返回标题 | 当前临时占用收尾；未来任务状态不被随意删除 |
| task_result History，无认知 Turn | 确证结果独立入账、幂等；真实交互另记，无伪造台词；旧 History / Summary / 检索回归 |
| Memory / 第二 NPC / 第二 world | 结果进正确 Context；不串任务、存档、来源或控制器；Memory 不推进 Task |
| 旧 Adapter、不支持扩展 | 普通链路保持，Task 工具不可用且原因明确 |

Runtime fake adapter 使用不同名称的校验、导航、等待工具和事件，配合同一套 TaskProposal / Evidence，证明 generic core 不依赖 Stardew 约定。另由内部 fake 调用方直接提交规范化 TaskSpec，证明 TaskService 不依赖模型工具调用或预约 Proposal 才能工作。

### 14.2 成功赴约演示

1. 使用普通日、已验证路线及受控 NPC，记录模型、Runtime、游戏/SMAPI/Adapter 版本。
2. 输入“后天 15:00 在沙滩见面，等我到 16:00”。核对 resolve_meeting → create_task 持久提交 → 台词确认。
3. 关闭对话，确认 NPC 恢复原生日程，创建 Turn 已终态，任务仍 waiting。
4. 正常睡觉保存并继续到约定日；至少一次退出游戏后加载。核对存档 checkpoint_id、任务恢复及出发 wake。
5. 玩家不跟随 NPC；观察其真实跨地图到达沙滩。等待阶段核对 Action / Turn 已结束、next_wakeup 指向截止。
6. 玩家在窗口内靠近，形成 met 证据，核对服务自动提交 succeeded 与结果来源；有效到达交互开启新 Turn，验证一次 approach_player 的合法交接及对话。
7. 关闭最后台词/回复，观察 NPC 在应移动的原生日程中离开；再次对话检查模型输入中的实际见面来源。

### 14.3 爽约与恢复演示

- 独立运行同样创建未来预约，玩家不出现；NPC 按时到达，截止后无需模型调用即可释放并恢复日程，Task 自动关联 expired 证据并提交失败，后续对话能引用真实结果。
- 在任务仍 waiting 时保存，之后完成或取消，再加载该保存；验证任务恢复到保存时状态。另验证保存后新建的任务不在恢复集合中。
- 保持游戏运行，停止并重启 Runtime，显式重新绑定同一 run；验证恢复 working head 而非日终快照，未确认动作先观察。
- 在等待时断线，验证临时占用安全释放；断线保存后加载得到 unconfirmed 提示和任务暂停，不自动赴约。
- 单独验证节日、跳时、路线中断、损坏引用；真实未等待的情况不能显示“玩家爽约”。

成功赴约与爽约各连续通过 3 次。记录 task/wake/operation、真实位置/时间、checkpoint、来源和最终模型请求。控制台瞬移、手动调用完成事件或仅修改台词不计入通过；受控跳时只用于标记清楚的边界测试。

## 15. 检查命令与通过标准

以下从仓库根目录执行，属于实现后的验收清单。文档修订不代表已运行这些代码测试或完成实机验收。

```powershell
powershell -ExecutionPolicy Bypass -File protocol/scripts/gen-go.ps1
go test ./runtime/internal/task ./runtime/internal/session ./runtime/internal/gateway ./runtime/internal/tool ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/memory -count=1
go test ./... -count=1
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj --configuration Debug
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug
git diff --check
```

并发相关 Go tests 在具备相应工具链的环境执行 `go test -race`；无法运行的检查单独记录。实机联调通过仓库安装流程构建并覆盖游戏 Mod，记录实际部署版本与配置。

阶段通过同时要求：

1. 9.0–9.5 及自动化矩阵通过；协议增量兼容、Phase6 交互/Async、Phase8 Memory 均无回归。
2. 跨天任务在创建 Turn 结束后被 Runtime 唤醒；Runtime Tools 与内部 TaskService 分离，后续唤醒、状态与结果可核对，确定性收尾不依赖模型。
3. 真实行走、固定接近、有效控制权交接及 UI 结束后的原生日程恢复通过。
4. 当前 run 重启与游戏读档分流正确，完整任务状态随检查点恢复。
5. 断线保存降级和不确定执行有明确结果，无重复任务、虚构见面或永久卡住 NPC。
6. 两条真实游戏/真实模型主路径各连续 3 次通过；实现复审无阻断问题，负责人依据证据确认阶段状态。

文档检查覆盖章节一致性、本地链接、能力参数/状态命名、9.0–9.5 依赖关系和 `git diff --check`。验收记录保存实际命令输出、未执行项和实机证据，不把计划中的断言记为已通过。

## 16. 后续阶段

| 阶段 | 责任 |
| --- | --- |
| Phase9 | Runtime 单实例持久任务、跨天唤醒、最小全量检查点与预约垂直闭环 |
| Phase10 | 自动重连、退避、bootstrap / capability replacement、跨连接事件与 pending operation 协调；复用 Task 身份和恢复合同 |
| Phase11 | 可重复评估、诊断、交付与新 Adapter 接入体验 |

复杂 Goal Planner、循环任务、工作流/DAG、分布式调度、完整动作 continuation、离线任务镜像和存档分支管理保持独立候选范围。
