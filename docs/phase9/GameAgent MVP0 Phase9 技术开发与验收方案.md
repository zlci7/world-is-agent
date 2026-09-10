# GameAgent MVP0 Phase9 技术开发与验收方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-10
> **Phase:** Phase9 Appointment Vertical Slice
> **目标:** 完成可展示的游戏行为最小闭环
> **Roadmap:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md)
> **Architecture:** [Runtime 整体架构设计规范](../summary/GameAgent%20Runtime%20整体架构设计规范.md)
> **Code Inspection Baseline:** `006bf84` 与 2026-09-10 工作区；Phase8.2 方案已标记 Accepted，Phase8.3 处于 Implementation In Progress
> **后续阶段:** Phase10 Environment Reconnect and Capability Recovery；Phase11 Evaluation、Developer Experience 与产品化

## 1. 阶段目标与交付范围

玩家上午通过自然语言与 NPC 约定当天下午在沙滩见面。NPC 根据约定提前出发，在约定时间到达点位并等待；玩家按时到达时，NPC 根据预约上下文进行互动；玩家未到达时，NPC 在等待期限结束后离开并恢复正常日程。下一次对话可以引用这次见面或爽约的实际结果。

Phase9 的闭环为：

```text
玩家表达意图 → Agent 决定并调用能力 → Adapter 注册活动
→ 游戏时间推进 → NPC 赴约和等待 → 见面 / 超时 / 中断
→ 新事件触发短 Turn → 结果进入 Memory → 后续交互利用结果
```

首版范围固定为单人存档、当前本地玩家、配置中的受控 NPC、当天的一次性见面活动。同一 NPC 同时最多一个非终态活动。主验收使用一名 NPC；实体隔离通过第二名 NPC 的确定性测试补充。

提供 `beach_meeting_spot`、`saloon_meeting_spot`、`town_square` 三个 Adapter 点位。沙滩是主演示目标，酒馆与广场验证点位配置及时间限制能够复用同一条执行链路。点位的实际坐标和受支持路线由 M0 实机验证确定。

本阶段必须交付：

- 普通日成功赴约、玩家爽约两条真实游戏演示路径。
- `schedule_activity` capability、活动状态机、游戏时钟驱动和生命周期事件。
- `approach_player` capability：由 Adapter 选择玩家相邻空闲格，完成一次固定目标的异步接近。
- 普通对话、接近后的对话及赴约对话结束后，NPC 自动恢复原生日程和自主行为。
- 活动相关 Observation、来源校验、幂等注册、日程控制权释放及存档集成。
- 节日冲突、非法输入、寻路失败、时间跳变和 Runtime 断线的明确处理。
- 自动化测试、实机证据和可按活动关联的诊断记录。

Phase9 的阶段定位为最小闭环；阶段 Accepted 仍须满足本方案的开发及验收条件。

## 2. 当前实现与需要补齐的边界

| 当前代码 | 已有能力 | Phase9 接入点 |
| --- | --- | --- |
| `adapters/stardew/src/State/StardewObservation.cs`、`ObservationBuilder.cs` | 玩家/NPC 位置、游戏时间、天气、关系、日程、对话 | 增加节日、当前活动及最近活动结果 |
| `adapters/stardew/src/Runtime/CapabilityCatalog.cs` | `emote`、`present_dialogue`、`face_player`、`move_to` | 注册预约与接近玩家能力，提供点位枚举及空参数目标语义 |
| `adapters/stardew/src/Capabilities/MoveToCapability.cs` | 当前地图内的异步移动 | 复用固定终点执行，补活动、接近玩家与直接移动的控制权互斥 |
| `adapters/stardew/src/ModEntry.cs` | 游戏生命周期、主线程更新、玩家输入 | 接入游戏时间、活动推进、日终及保存事件 |
| `adapters/stardew/src/Runtime/RuntimeClient.cs` | 事件、动作、EventAck、TurnCompletion、断线清理 | 接入活动通知、活动来源及接近动作的异步结果 |
| `adapters/stardew/src/Runtime/InteractionContextStore.cs` | 已接纳玩家交互的来源快照与实时校验 | 保留玩家交互规则，为赴约事件提供独立来源校验 |
| `adapters/stardew/src/Dialogue/DialoguePresentationFlow.cs`、`DialogueInteractionController.cs` | 台词展示、回复提交、退出及 presentation 完成状态 | 区分实际对话结束与回复接续，通知 NPC 交互占用释放 |
| `runtime/internal/gateway/gateway.go`、`runtime/internal/session/lane.go` | 通用事件接纳、同实体有界 FIFO | 复用事件入口，覆盖新事件与队列背压测试 |
| `runtime/internal/agent/config.go` | Turn 默认 90 秒、Async Action 默认 45 秒 | 维持短 Turn；预约等待不延长这些超时 |
| Phase8 Memory / History | 稳定实体作用域、已提交历史、Context 读取 | 保存实际来源和动作结果，不承担游戏活动调度 |

当前 `move_to` 明确限制目标在 NPC 当前地图内，生产事件入口主要来自玩家点击和对话输入。跨地图赴约、自动时间事件以及无玩家点击的交互来源均须开发验证。

Phase8.1 的持久身份与近期来源读取是基础前置。已经验收的 Phase8.2 可提供完整终态 History 与摘要；Phase8.3 检索和留存清理不作为 Phase9 的硬前置条件。开发时以实际采用的 Memory 版本运行回归，不把工作区存在代码视为已完成相应验收。

## 3. 架构与生命周期归属

```text
Agent owns intent.
Runtime owns cognition.
Protocol owns contracts.
Adapter owns translation.
Game owns execution.
```

| 对象 | 所有者 | 合同 |
| --- | --- | --- |
| 是否接受预约、地点和时间的协商 | Agent | 结合角色、玩家输入、Observation 与可用工具决定 |
| AgentTurn、工具执行、超时、Memory、Trace | Runtime | 每个事件进入一次有界认知过程 |
| 点位、节日、游戏时钟、活动记录、NPC 控制权 | Adapter / Game | 读取真实游戏状态，执行与验证世界效果 |
| GameEvent、Observation、ActionRequest、ActionResult | Protocol | 复用现有跨游戏合同，私有字段放 namespaced 数据 |
| 到期唤醒后重新规划的持久 Goal / Task | 后续 Runtime 能力 | 具有独立的持久化、唤醒、重试和恢复合同 |

三种时间语义分别处理：

1. 当前开始、持续一段时间的动作沿用 Runtime Async Action，例如一次 `move_to` 或 `approach_player`。
2. 已确定的未来游戏活动通过 `schedule_activity` 注册，由 Adapter/Game 推进。
3. 未来需要 Agent 重新决策的长期目标由后续 Runtime Task/Goal 系统管理。

`schedule_activity` 的 Action 在注册完成时返回终态。上午的 Turn 随后结束，下午产生的新事件启动新的 Turn。活动调度不会占用跨越上午到下午的 goroutine、Turn continuation 或未完成 Action waiter。

Runtime Core 不按 `schedule_activity`、`approach_player`、Stardew 活动事件名、地图或节日字段增加分支；不引入 Stardew 专属 Tool Policy、proto message 或数据库活动表。

## 4. 点位与当前世界事实

### 4.1 LandmarkCatalog

点位定义保存在 `adapters/stardew/assets/landmarks.json`，由 Adapter 加载并校验。目录只包含游戏世界元数据。

| 字段 | 含义与校验 |
| --- | --- |
| `landmark_id` | 稳定、唯一标识；模型选择此标识 |
| `display_name`、`description` | 模型可理解的地点名称与会面位置说明 |
| `location` | Stardew 地图标识，例如 `Beach`、`Saloon`、`Town` |
| `tile` | 经验证的 NPC 停留格子；整数坐标、地图内、可到达 |
| `available_from`、`available_until` | 当天可使用窗口；完整预约窗口必须位于其中 |
| `departure_lead_minutes` | 为受支持路线预留的游戏内行程分钟数 |

三处点位分别配置：沙滩上的会面格子、酒馆内的会面格子、镇广场会面格子。酒馆窗口须结合真实开放时间校验，点位避免固定占用、过图口及玩家交互热点。

Capability bootstrap 使用加载时通过结构校验的稳定点位 ID 构建参数枚举，description 同时提供地点名称、窗口和时间含义。玩家尚未加载存档时只做静态校验；地图、占用和 NPC 可达性在游戏主线程执行时校验。天气或节日变化通过 Observation 和执行校验反映，不依赖在线替换整份 CapabilityList。

模型不传地图坐标。静态点位表不在每次 Observation 中完整重复，也不进入 Runtime Core。M0 必须提交实际坐标与路线证据；未验证的点位不计入支持范围。

### 4.2 Observation 扩展

沿用 `Observation.state.stardew`，在 Adapter 的 schema 中进行 additive 扩展：

| 字段 | 内容 |
| --- | --- |
| `festival` | `known`、`is_festival_day`、可获得的名称；未知不能当作普通日 |
| `current_event` | 当前是否处于过场、节日或其他阻止接管 NPC 的事件 |
| `current_activity` | 目标 NPC 的非终态活动；活动 ID、日期、点位、约定窗口、状态及 revision |
| `last_activity_result` | 目标 NPC 最近一个终态活动的结果、实际发生时间、原因及日程释放状态 |

已有 `player.location/tile`、`agent.location/tile`、`time` 和 `schedule` 继续作为当前事实来源。本阶段无需额外的玩家位置查询工具。

`current_activity` 与 `last_activity_result` 来自活动状态机的同一份记录，不能按 NPC 台词、Memory 摘要或模型参数猜测。原来的 `schedule` 保留其游戏日程含义，不兼作预约结果。

## 5. Capability 合同

### 5.1 schedule_activity 输入及执行策略

首版只注册“目标 NPC 当天与当前玩家见面”的活动。NPC 身份来自 `ActionRequest.entity_id`，玩家来自经过校验的交互来源，游戏日期来自主线程当前世界。

```json
{
  "type": "object",
  "properties": {
    "landmark_id": {
      "type": "string",
      "enum": ["beach_meeting_spot", "saloon_meeting_spot", "town_square"]
    },
    "start_time": {"type": "integer"},
    "end_time": {"type": "integer"}
  },
  "required": ["landmark_id", "start_time", "end_time"],
  "additionalProperties": false
}
```

枚举从实际点位目录生成。时间采用 Stardew 的 HHMM 表示，`1500` 表示 15:00；Adapter 校验小时、分钟及 10 分钟刻度。首版约定窗口限制在当天 06:00–22:00，且满足点位可用时间，`start_time < end_time`。所有比较和加减先转换为分钟，不能直接对 HHMM 做减法。

`start_time` 表示约定开始见面的时间；NPC 应在此时前到达。`end_time` 是玩家到达的截止时间，到达窗口为 `[start_time, end_time)`。Adapter 按点位的行程预留计算 `departure_time`，注册时要求当前时间早于出发时间。信息不足或用户只说“下午”而未约定明确窗口时，Agent 先完成协商。

| Metadata | 值 |
| --- | --- |
| `execution_mode` | `ExecutionMode.Sync` |
| `concurrency_mode` | `Sequential` |
| `extensions.gameagent.tool_policy.exclusive_per_step` | `true` |
| `extensions.gameagent.tool_policy.settle_after_success` | `false` |

注册返回后，下一步才能按结果调用 `present_dialogue` 确认或解释失败。继续沿用该对话能力的独占与成功后结束 Turn 策略。不能先向玩家宣告预约成功，再尝试注册。

### 5.2 schedule_activity 注册与返回

注册顺序为：核对当前世界与目标身份 → 查询当前加载代次的 action 幂等记录 → 对新请求校验交互来源和参数 → 查询等价业务预约 → 校验活动冲突、节日和路线条件 → 创建活动 → 准备存档数据 → 返回结果。已处理 action 的等价重试可以在原交互来源释放后返回原结果；它不重新检查当前可预约时间，也不产生新活动。任何实际 NPC 日程接管都发生在出发阶段。

成功返回 `ActionResult.status = SUCCEEDED`，示例 output：

```json
{
  "activity_id": "activity-123",
  "activity_revision": 1,
  "state": "scheduled",
  "created": true,
  "game_date": {"year": 1, "season": "spring", "day_of_month": 8},
  "landmark_id": "beach_meeting_spot",
  "start_time": 1500,
  "end_time": 1600
}
```

`SUCCEEDED` 证明当前已加载世界中的活动注册成功，存档持久性遵循第 9 节。它不证明 NPC 已到达或玩家已见面。后续赴约失败保留这次注册结果，并通过新的活动状态和事件表达。

重复的等价业务预约返回既有 `activity_id`、当前状态和 `created=false`。相同 ActionRequest 的传输重试返回该 Action 已记录的原结果，不重新执行。

### 5.3 schedule_activity 注册失败

| 条件 | Action 状态 / `error.code` | 世界效果 |
| --- | --- | --- |
| 点位不存在、未通过配置校验 | `REJECTED / invalid_landmark` | 不创建活动 |
| 时间格式、刻度、先后顺序或开放窗口错误 | `REJECTED / invalid_time_window` | 不创建活动 |
| 距出发时间不足 | `REJECTED / insufficient_travel_time` | 不创建活动 |
| 来源失效、世界或实体不匹配 | `REJECTED / interaction_context_*` 或 `world_mismatch` | 不创建活动 |
| 已有不同的非终态活动 | `REJECTED / activity_conflict` | 保留既有活动 |
| 当天已达到注册容量 | `REJECTED / activity_capacity_exceeded` | 保留既有活动 |
| 节日当天 | `REJECTED / festival_conflict` | 保留原生日程 |
| 节日事实未知、事件占用或 NPC 不支持接管 | `REJECTED / activity_unavailable` | 保留原生日程 |
| 已知路线不可用 | `REJECTED / route_unavailable` | 不创建活动 |
| 同一 action ID 携带不同请求内容 | `REJECTED / activity_request_conflict` | 保留首次记录 |
| 注册记录或存档数据准备失败 | `FAILED / activity_registration_failed` | 回滚本次注册的局部变更 |

首版对节日当天采取整日拒绝预约的确定性规则，模型可以解释冲突。节日场景内的约会、自动改期和备选地点重规划不进入 Phase9 验收。

### 5.4 approach_player 目标与执行合同

`approach_player({})` 表达“移动到动作开始时玩家位置旁的一个合法格子”。NPC 身份来自 `ActionRequest.entity_id`，玩家来自有效的玩家交互或赴约到达来源，位置由 Adapter 在执行主线程读取。模型只选择目标语义，不传玩家坐标或具体落脚格。

输入 schema：

```json
{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}
```

Capability 声明 `execution_mode=ExecutionMode.Async`、`concurrency_mode=Sequential`，沿用现有异步 Action 的启动、进展、终态、取消及超时合同。成功后按现有流程重新观察并恢复当前 Turn；玩家移动本身不触发额外模型调用、自动重试或新 Turn。

选点与执行规则：

1. 校验来源、world、entity、加载代次、玩家身份与同地图条件。接近动作不以玩家当前仍在 NPC 两格内作为执行前置条件；现有 `move_to` 和对话能力保留各自距离规则。
2. 检查控制权。有效的赴约到达来源按第 6.3 节完成本活动控制权交接；其他活动或动作占用时明确拒绝。
3. 在同一次主线程处理内读取玩家位置快照，枚举上、右、下、左四个相邻格，即曼哈顿距离为 1 的候选格。排除越界、不可通行、被其他实体占用及不可达的格子；NPC 自己当前所在格不视为被其他实体占用。
4. NPC 已在合法相邻格时，以当前位置作为目标直接完成。否则采用 Game 的真实寻路结果，选择路径长度最短的候选；长度相同时按上、右、下、左排序。
5. 保存玩家位置快照、选定目标格和初始路径，复用既有固定终点移动的异步执行逻辑。目标确定后不随玩家移动重新选点、重新寻路或切换路径；途中无法完成原路径则返回失败或中断。

候选评估阶段可以为四个格子分别求路径，固定目标原则约束选定后的执行过程。玩家途中在当前世界内移动或换地图时，NPC 继续前往已选的原地图格子；世界切换、读档、取消和控制权丢失仍按动作生命周期收尾。

### 5.5 approach_player 结果与失败

`ActionResult.status=SUCCEEDED` 只表示 NPC 实际抵达选定的固定终点。Capability description 必须说明这一完成条件和目标固定规则；不能将到达玩家旧位置解释为当前仍与玩家相邻。

成功 output 必须包含：

| 字段 | 含义 |
| --- | --- |
| `player_position_at_start` | 选点时的玩家位置快照，包含 `location` 和 `tile` |
| `target_tile` | 选定目标格，包含整数 `x / y`，所在地图为开始时的共同地图 |
| `final_position` | 完成时 NPC 的真实 `location` 和 `tile` |
| `player_is_adjacent` | 完成时 NPC 与来源玩家同地图且曼哈顿距离为 1 时为 true，否则为 false |

玩家途中离开时，可以同时返回 `SUCCEEDED` 和 `player_is_adjacent=false`；完成状态与当前相邻事实分别表达。后续对话按实时交互条件校验，不因移动成功而跳过距离检查。

| 条件 | Action 状态 / `error.code` | 执行结果 |
| --- | --- | --- |
| 参数含额外字段或类型错误 | `REJECTED / invalid_action_arguments` | 不开始移动 |
| 来源无效或世界、实体不匹配 | `REJECTED / interaction_context_*`、`activity_context_expired` 或 `world_mismatch` | 不开始移动 |
| 玩家与 NPC 不同地图 | `REJECTED / different_location` | 不跨地图追赶 |
| 四个相邻格均不合法或不可达 | `REJECTED / no_reachable_adjacent_tile` | 保持原位置 |
| 无法取得或交接控制权 | `REJECTED / npc_control_busy` | 保留原控制器 |
| 原路径受阻或执行失败 | `FAILED / move_failed` | 停止自身移动并释放控制权，不重新选点 |
| 执行中控制权被替换 | `INTERRUPTED / control_lost` | 释放自身状态，保留新控制器 |
| 执行中世界切换或重新加载 | `INTERRUPTED / world_changed` | 结束旧加载代次的动作 |
| 收到取消或超时取消 | `CANCELLED`，沿用既有取消原因 | 停止自身移动并释放控制权 |

已相邻的直接完成、移动完成、失败、中断及取消都只发送一次 ActionResult 终态。清理只影响该动作仍拥有的控制器；一次接近的成功或失败不修改预约已发生的 `met` 事实。

## 6. 活动状态机与游戏时钟

### 6.1 活动记录

活动至少记录以下字段：

| 分组 | 字段 |
| --- | --- |
| 身份 | `activity_id`、`world_id`、`npc_entity_id`、`player_entity_id` |
| 约定 | `game_date`、`landmark_id`、`start_time`、`end_time`、`departure_time` |
| 状态 | `state`、`revision`、`created_game_time`、`arrived_game_time`、`terminal_game_time`、`reason_code` |
| 来源 | `source_event_id`、`source_turn_id`、`source_action_id`、规范化请求指纹 |
| 释放 | `release_state`，取 `not_acquired / pending / released / failed`，及失败原因 |

日期、地点和时间在注册后保持不变。修改预约需要未来独立能力；首版以冲突拒绝表达。

```text
scheduled → travelling → waiting → met
     │           │           ├──→ expired
     └───────────┴───────────┴──→ interrupted
```

| 状态 | 进入条件 | 后续动作 |
| --- | --- | --- |
| `scheduled` | 注册成功，尚未到出发时间 | 原生日程继续；等待游戏时间到达 |
| `travelling` | 到达出发时间，取得控制权并成功安装行程 | 由 Game 执行跨地图移动并报告真实进展 |
| `waiting` | NPC 实际处于目标地图和配置格子 | 保持停留；约定窗口内检测玩家到达 |
| `met` | 窗口内玩家与 NPC 同地图且曼哈顿距离不超过 2 格 | 记录真实见面，触发到达交互并释放控制权 |
| `expired` | NPC 已按时到达，截止时间前玩家未满足到达条件 | 记录等待超时，恢复当前时刻原生日程 |
| `interrupted` | 路线、控制权、事件、日期或世界状态使活动无法继续 | 记录具体原因，释放自身控制权 |

`met / expired / interrupted` 是活动终态；终态不得重新变成等待或出发。NPC 物理见面与台词展示分别记录，`met` 不意味着某条台词已成功展示。

### 6.2 推进规则

`TimeChanged` 用于跨越出发、开始和截止边界；主线程 `UpdateTicked` 用于检查实际到达、控制权丢失和玩家接近。玩家到达检测最多每 15 个 update tick 一次，只检查存在等待活动的 NPC。每次世界事实转换最多发布一次对应通知，不按 tick 调用模型。

时间采用游戏日期与游戏内分钟。暂停期间不按现实时间缩短等待窗口；关闭菜单后按实际游戏时间继续。现实时间仅用于网络请求、UI 等待和技术错误超时。

边界规则：

- 提前到达时进入 `waiting`，从 `start_time` 开始识别赴约；玩家已在附近时，开始时间到达后同样能触发一次见面。
- 在 `start_time` 检查时先采样真实位置；恰好到达目标点位可以进入 `waiting`，尚未抵达的 NPC 进入 `interrupted / arrival_deadline_missed`，不将其归责为玩家爽约。
- 检测到 `now >= end_time` 时先处理截止，再处理玩家到达。恰好在截止时间到达计为超时。
- 时间直接跳过完整窗口时，尚未赴约的活动进入 `interrupted / time_window_missed`；已经在等待的活动进入 `expired`。不补造途中移动或玩家到达事实。
- 在同一天检测到游戏时间倒退时，当前活动进入 `interrupted / clock_rewound`；正常读档走存档加载流程，不对旧内存活动倒带。
- `DayEnding` 收敛当天尚未结束的活动；`DayStarted` 不延续前一天的非终态活动。

### 6.3 日程控制权与释放

Adapter 使用按 NPC 隔离的控制权标记协调活动驱动器、`approach_player` 与现有 `move_to`。在 `travelling / waiting` 期间，新的接近或直接移动返回 `REJECTED / npc_control_busy`，不覆盖活动路径。出发时存在无法安全释放的其他控制器，则以 `npc_control_busy` 中断活动。

游戏过场、节日及其他 Mod 替换控制器时，活动记录 `control_lost` 或 `world_event_started`。释放逻辑只操作仍属于该活动的控制器和日程覆盖，不能清空后来由游戏或其他 Mod 安装的控制器。

`met` 后由 Adapter 维护到达交互占用，覆盖同一对话中台词、回复和后续 Turn 的接续。实际对话结束、无接近动作执行时玩家离开交互范围、约定结束或连接断开时释放。`TurnCompletion` 用于结束对应事件来源和技术等待；当前仍有对话 UI 或已提交回复正在接续时，交互占用保留。当前 Turn 未展示对话，且没有回复接续或在途动作时，在 Turn 终态释放。业务状态保持 `met`，释放过程由 `release_state` 表示，不把 UI 失败改写成没有见面。

处于 `met` 的有效到达交互可以将本活动持有的停留控制权原子交接给一次 `approach_player`。交接必须核对同一 world、NPC、activity、到达来源与加载代次；只交接移动控制权，保留到达交互来源。若选点或启动失败，保留仍有效的到达交互占用，或按其释放条件恢复原生日程。

接近动作执行期间，玩家距离变化不单独取消动作，固定目标继续有效；约定结束、等待 Runtime 的 120 秒上限、所属 Turn 在动作未完成时终止、断线或世界切换仍会结束相应占用，并收尾正在执行的接近动作。接近动作终态后，当前到达交互仍有效且玩家处于交互范围内时交回该活动的交互占用；否则释放控制权并恢复原生日程。后续对话继续检查真实距离。

释放应按当前日期和时刻恢复原生日程，并验证 NPC 能继续正常行动；不能仅将 `npc.controller` 设为空，也不能沿用上午快照把 NPC 拉回过时位置。M0 必须证明这一行为。

清理异常设为 `release_state=failed` 并记录原因，停止继续写入该 NPC 的控制器。正常路径的验收要求 `released` 且可观察到 NPC 离开会面点；存在持续卡住 NPC 的问题不得标记阶段 Accepted。

### 6.4 对话结束与原生行为恢复

普通对话、无预约的接近后对话及赴约对话遵循同一条恢复规则：**对话真正结束后，在下一次可运行的游戏主线程更新中释放本次交互的临时占用，由游戏继续当前日期和时刻的原生日程与自主行为。** 恢复不需要模型调用、额外 capability 或玩家再次点击。

对话结束的判断以 UI 生命周期为准：最后一段 NPC 台词被关闭且没有后续回复菜单，或玩家退出回复/自由输入，或交互因失败、取消、断线而关闭。确认同一交互没有正在接续的玩家提交和仍有效的在途动作后，执行恢复。

`present_dialogue` 在台词展示成功时即可返回 ActionResult，Runtime 随后可以发送 TurnCompletion；这两个信号不代表玩家已读完对话。`ConversationStateStore` 的逻辑关闭也不能替代 UI 关闭。回复提交只结束当前 presentation，并接续下一 Turn，不作为整段对话结束。

Adapter 通过 `NpcInteractionLifecycle` 按 `world_id + npc_entity_id + conversation_id + 加载代次` 管理交互占用，与按 event/turn 清理的来源记录分开。对话控制器报告“台词结束、回复接续、退出或异常关闭”等本地信号；自然结束、主动退出和错误清理汇入同一幂等释放入口，旧交互回调不能释放新的交互。

无预约的接近动作也接入该占用：移动终态后释放移动控制器，所属 Turn 仍在继续或已经展示对话时由交互占用承接；Turn 已结束且没有 UI、回复接续或在途动作时直接恢复原生行为。接近后无需继续对话的路径同样不能把 NPC 留在 Adapter 的临时停留状态。

恢复执行由 Adapter/Game 负责：

- 撤销本次交互实际设置且仍拥有的临时停留、朝向锁和移动占用；保留游戏或其他 Mod 后来接管的状态。
- 以当前日期和时刻恢复原生行为。原日程已有应执行的移动时，重新衔接该行程；原生行为本来要求驻足时，允许由游戏决定正常停留。
- 不将 NPC 传回交互前坐标，不仅以清空路径控制器作为恢复完成，也不生成随机移动代替原生日程。
- 普通对话结束不删除尚未到出发时间的预约；`met` 对话结束后释放会面占用，终态记录保持。

实际日程恢复复用 `NpcNativeBehaviorRestorer`，活动结束与普通对话结束均使用该入口。验收选择原生日程应当移动的时间段，验证关闭对话后 NPC 实际继续行程，且无需新模型调用；仅记录“已释放”或仅关闭 UI 不计为通过。

## 7. 真实游戏执行与主线程接线

`StardewActivityDriver` 包装实际的日程、寻路、停留及恢复操作。`ScheduledActivityManager` 只管理活动规则和状态；其测试输入为可注入的时间、位置及驱动结果，不依赖 `NPC`、`Game1` 或 SMAPI。

`NpcInteractionLifecycle` 汇总对话、回复接续及相关 Turn/Action 的本地生命周期；`NpcNativeBehaviorRestorer` 包装游戏侧的行为恢复。对话关闭后即使对应 Turn 已结束，Adapter 仍必须接收到本地结束信号并完成恢复。

日程中的时间键可能表示出发时间，不能直接把用户说的见面时间写入日程后视为准时到达。驱动器必须根据点位行程预留提前出发，并以 NPC 真实位置验证到达。

| 接线入口 | 行为 |
| --- | --- |
| `GameLaunched` / 初始化 | 加载点位配置、构建能力目录，不读取未加载存档中的 NPC |
| `SaveLoaded` | 加载当前存档活动快照，更新本地加载代次，按当前世界重新校验 |
| `TimeChanged` | 推进所有当前非终态活动的游戏时间边界 |
| `UpdateTicked` | 收集活动与固定目标接近的执行结果、检测玩家到达、处理本地通知、实际对话结束及原生行为恢复 |
| `DayEnding` | 结束当天活动并释放控制权，准备存档快照 |
| `Saving` | 将活动快照写入当前游戏存档 |
| `ReturnedToTitle` / Dispose | 释放仍持有的活动控制权，清空当前加载代次的内存与通知 |
| Runtime stream 关闭 | 清理交互等待和通知；已注册的游戏活动继续受游戏时间驱动 |

所有游戏 API、位置读取、存档访问和控制器修改在 SMAPI 主线程执行。网络线程只传递消息与确认。驱动回调带上本地加载代次与活动 revision，过期回调无法修改重新加载后的同名世界。

## 8. 生命周期事件与交互来源

### 8.1 事件合同

| GameEvent.event_type | 产生时机 | 交互权限 |
| --- | --- | --- |
| `scheduled_activity_changed` | 进入 `travelling / waiting / expired / interrupted` | 提供活动事实，不主动打开远处玩家的对话 UI |
| `player_arrived_at_activity` | 从 `waiting` 进入 `met` | 允许在当前交互条件下发起一次赴约对话 |

注册本身由 ActionResult 返回，不额外产生一个用于重复决策的注册事件。终态事实在本地先成立，发送事件或模型失败均不回滚世界结果。

每个事件包含：真实 `world_id`、目标 NPC 的 `target_entity_id`、完整目标 EntityRef、实际发生的 GameTime、当前连接事件 sequence，以及来自活动记录的 `activity_id / revision / state / reason_code`。目标 EntityRef 的 `definition_id` 沿用现有映射。

活动事实复用通用 `ContextFact.kind=interaction`，`scope_id=activity_id`。`text` 明确谁在什么时间、地点发生了什么，`attributes` 只保留状态、revision 等必要附加信息；地图私有数据留在 payload / Observation。

示例 ContextFact：

```json
{
  "kind": "interaction",
  "actor_entity_id": "npc:Abigail",
  "target_entity_id": "npc:Abigail",
  "scope_id": "activity-123",
  "text": "春季8日，Abigail 已到达约定的沙滩点位，等待玩家在15:00至16:00之间见面。",
  "attributes": {"state": "waiting", "revision": 3}
}
```

玩家到达事实的 actor 使用实际玩家实体；超时事实说明 NPC 已到达并等待至截止时间。路由失败只记录活动中断，不生成“玩家爽约”的文本。

### 8.2 来源校验与 UI

现有玩家点击/自由输入继续使用 `InteractionContextStore`。活动事件通过独立的活动来源记录保存 `event_id → world/entity/activity/revision/加载代次/交互权限`，在 `EventAck.ACCEPTED` 后变为可执行来源，在拒绝、超时、TurnCompletion 或断线时释放。来源释放不等于正在展示的对话已经关闭；NPC 的交互占用按第 6.4 节独立管理。因队列满而重试时重新预留同一事件的来源，保留原发生事实，并重新校验其当前交互资格；重复确认不得释放已经接纳且仍在执行的来源。

注册预约要求有效玩家交互来源。到达事件无需伪造一次玩家点击，但只有真实玩家接近、当前没有冲突 UI 且同 NPC 无在途交互时，才建立可发起对话的到达来源。玩家已在会面范围内时也按此规则执行。

`present_dialogue` 的活动来源校验至少检查：ActionRequest 的 source event、world、entity；活动 ID 和加载代次；到达交互尚未释放；当前玩家与 NPC 同地图且仍在 2 格范围内；没有覆盖其他菜单的风险。上午的 interaction snapshot 不能复用于下午。

`approach_player` 接受有效玩家交互来源或 `met` 对应的有效到达来源，校验 world、entity、玩家身份、加载代次和同地图条件。两格距离只用于产生到达事实及校验后续对话，不作为接近动作开始的距离门。执行期间的玩家距离变化不自行撤销已开始的固定目标动作；后台活动状态通知不授予接近玩家的交互权限。

后台活动事件提供事实后允许模型直接结束 Turn。模型调用不适用的 UI 能力时返回 `REJECTED / activity_not_interactive`；到达来源已失效时返回 `REJECTED / activity_context_expired`。这些规则由 Adapter 执行校验落实，capability description 和当前事实负责向模型说明使用条件。

### 8.3 有界通知与背压

活动通知使用当前连接、当前加载代次内的有界队列，每个 NPC 最多 8 条，按活动 revision 保序；每次只有一条通知等待 EventAck。事件 ID 在首次创建时固定，同一通知重试不生成新 ID。

明确收到 `session_queue_full` 时，最多按 1、2、4 秒间隔重试三次；EventAck 等待超过 5 秒或通知队列超限时记录 `activity_event_delivery_failed`。已收到 `ACCEPTED / DUPLICATE` 的事件不再发送。没有收到确定确认时不自动跨连接重放。

重试仅影响通知送达，活动状态机和 NPC 截止离开继续运行。到达事件排队后玩家可能离开，实际动作仍需重新校验实时来源。

交互等待 Runtime 返回最长 120 秒现实时间；达到上限后关闭对应 waiting UI、释放来源及交互控制权，并保留已发生的见面结果。正常阅读 NPC 台词或填写回复由 UI 生命周期和游戏原生暂停规则管理，不计入这项技术等待。该上限不改变游戏预约窗口。

断线期间继续更新 Adapter 活动事实和日志，停止通知发送。Phase9 不承诺这些离线事件后来进入 Runtime History；重新连接、状态协调及通知恢复由 Phase10 单独定义。

## 9. 活动存储、幂等与存档边界

### 9.1 权威来源

活动数据属于游戏世界状态，保存在当前存档的 SMAPI SaveData 中，键为 `scheduled-activities`，数据版本为 `1`。快照包含 `world_id`、活动记录及仍在保留范围内的幂等信息。

Memory / History 属于 Runtime。它们保存玩家说过什么、调用过什么以及已确认的结果，不能据此在加载游戏时重建一个存档中不存在的预约。

快照只包含值对象和稳定身份，不序列化 NPC 实例、路径控制器、委托、Task 或网络 waiter。保存失败需报告诊断，不能通过清空记录自动修复。

### 9.2 幂等范围

- 在当前加载代次内，以 `world_id + npc_entity_id + action_id` 查询已处理动作。指纹覆盖能力名、规范化参数及 source event/turn 关联；相同请求返回原 ActionResult，不因当前游戏时间或来源已释放而重新执行。不同请求指纹拒绝冲突。
- 业务重复键为 `world_id + npc_entity_id + player_entity_id + game_date + landmark_id + start_time + end_time`。不同 action ID 的等价请求返回同一活动，不覆盖状态。
- 每个 NPC 最多一个非终态活动，每个游戏日最多新建 16 个活动。保存最新 32 个终态记录，当前日的记录与凭据始终保留。
- `revision` 仅在状态实际变化时递增。重复 tick、重复完成回调、重复玩家接近和重复截止检查均不增加终态副本。
- 加载存档时只从快照恢复业务记录。旧连接动作和来源上下文不恢复；过去的活动记录不自动重新产生生命周期事件。

这些规则保证已声明作用域内的重复执行安全，不提供跨进程、跨连接、任意存档分支的全链路 exactly-once。

### 9.3 保存与加载

`WriteSaveData` 的数据随游戏正常保存落盘。当天未保存就退出时，该日新注册的预约与其他游戏进展一起回到上次存档；注册成功不等同于即时磁盘提交。首版不提供脱离游戏存档的独立活动数据库。

正常单人游戏在日终保存，因此当天预约通常会以终态进入存档。实机保存验收验证这些终态及新一天状态；自动化可注入非终态快照验证加载规则，但不以此声称支持任意时刻保存游戏。

加载时：

1. 核对 schema、world 绑定、实体、点位、日期和输入合法性。数据异常时保留原数据并禁用活动功能，普通对话仍可使用。
2. 终态记录保持终态，控制器与来源上下文全部重新初始化。
3. 当天尚未出发的活动恢复为 `scheduled`；控制权不从存档直接视为已取得。
4. 当天窗口内的非终态记录只有在重新验证位置、路线和截止条件后才能继续；无法满足准时到达条件时中断。
5. 已过日期或窗口的非终态记录按第 6 节收敛，不重新执行历史行程。未来日期的预约不属于首版合法数据。

Runtime 断开且游戏继续运行时，已注册预约仍可赴约、等待、见面或超时离开。游戏进程重启恢复的是存档状态；Runtime 未完成 Turn 与 Action continuation 不随活动快照恢复。

## 10. Memory、Context 与诊断

新的活动事件通过现有 ContextFact 入口进入当前模型请求。Runtime 只投影通用事实，不解析 Stardew payload 或 Observation 来推测历史。

使用 Phase8.1 时，成功注册的工具参数和状态、成功 Turn 的活动 ContextFact 按已有 Recent 合同保存；使用 Phase8.2 时，按其终态批次合同保存原始来源与已知 ActionResult。两种路径均须保持“注册成功、NPC 到达、玩家见面、台词展示成功”的区别。

`approach_player` 的真实结果进入当前 Turn 的工具反馈。其成功含义为抵达选定终点，`player_is_adjacent` 单独表达完成时的相邻关系；模型和历史投影不能将这两个事实合并为持续跟随或保证当前可对话。历史保存继续遵循所用 Memory 版本的来源与结果合同。

活动事实如果因事件未接纳、Turn 失败或写入失败未进入历史，遵循所用 Memory 版本的失败合同。当前 Observation 仍反映实际活动结果，诊断必须能识别历史缺失，不能伪造补记。

后续对话验收同时核对来源和最终模型请求：有预约的地点、时间和结果来源，模型才能据此作答。最近活动结果来自当前存档，Memory 是历史背景；读档后历史中出现的预约陈述不能触发自动执行。

Adapter 日志至少记录：

```text
world_id, npc_entity_id, activity_id, activity_revision,
from_state, to_state, game_time, landmark_id, reason_code,
source_event_id, source_turn_id, source_action_id,
event_id, action_id, release_state, delivery_status
```

通过日志中的 `event_id / action_id / turn_id` 连接现有 Runtime JSONL Trace；不需要给 Runtime Core 增加游戏活动模型。Trace、Memory 和存档分别保留诊断、经历和当前世界状态的职责。

对话行为恢复另记录 `conversation_id`、加载代次、结束原因、释放结果与原生行为恢复结果，关联到同一 NPC；验收以实际游戏行为确认恢复结果，不以日志声明代替移动证据。

## 11. 修改范围与组件职责

以下为开发目标文件；本方案不要求重构无关目录。

| 文件 | 类型 | 责任 |
| --- | --- | --- |
| `adapters/stardew/assets/landmarks.json` | 新增 | 三处实机验证点位及时间、行程约束 |
| `adapters/stardew/src/Activities/LandmarkCatalog.cs` | 新增 | 配置加载、验证、点位解析及 schema 数据 |
| `adapters/stardew/src/Activities/ActivityModels.cs` | 新增 | 注册输入、活动记录、快照、状态和转换值对象 |
| `adapters/stardew/src/Activities/ActivityTime.cs` | 新增 | HHMM 校验、分钟运算、日期和时间窗口规则 |
| `adapters/stardew/src/Activities/ScheduledActivityManager.cs` | 新增 | 幂等注册、状态转换、容量及游戏时钟推进 |
| `adapters/stardew/src/Activities/StardewActivityDriver.cs` | 新增 | 原生日程接管、跨地图行程、停留及调用共用行为恢复入口 |
| `adapters/stardew/src/Activities/ActivitySaveStore.cs` | 新增 | 快照、版本/绑定校验与加载；读写入口注入，由主线程装配 SMAPI SaveData |
| `adapters/stardew/src/Activities/ActivityEventPublisher.cs` | 新增 | 通知队列、固定事件 ID、确认、有限重试和诊断 |
| `adapters/stardew/src/Activities/ActivitySourceContextStore.cs` | 新增 | 活动事件来源、加载代次、交互权限及释放 |
| `adapters/stardew/src/Events/ActivityArrivalDetector.cs` | 新增 | 玩家真实接近、窗口判断和单次到达触发 |
| `adapters/stardew/src/Capabilities/ScheduleActivityCapability.cs` | 新增 | Action 输入、来源校验、注册调用和结果映射 |
| `adapters/stardew/src/Capabilities/ApproachPlayerCapability.cs` | 新增 | 玩家位置快照、相邻格选择接线、固定目标移动及真实结果 |
| `adapters/stardew/src/Capabilities/AdjacentTileSelector.cs` | 新增 | 四邻格候选的合法性与路径长度排序；不依赖游戏实时对象 |
| `adapters/stardew/src/Capabilities/NpcControlLease.cs` | 新增 | 活动、接近玩家和直接移动的互斥，以及有效到达交互内的控制权交接 |
| `adapters/stardew/src/Capabilities/NpcNativeBehaviorRestorer.cs` | 新增 | 释放本次临时控制，按当前日期和时刻衔接游戏原生日程与自主行为 |
| `adapters/stardew/src/Dialogue/NpcInteractionLifecycle.cs` | 新增 | 对话、回复及 Turn/Action 接续的交互占用；真实结束后的幂等释放 |
| `adapters/stardew/src/Dialogue/DialoguePresentationFlow.cs`、`DialogueInteractionController.cs`、`adapters/stardew/src/Capabilities/PresentDialogueCapability.cs` | 修改 | 明确 UI 自然结束、回复接续、退出与异常关闭信号，接入 NPC 行为恢复 |
| `adapters/stardew/src/Runtime/ProtocolMapper.Activities.cs` | 新增 | 活动 GameEvent、ContextFact 与 ActionResult 的纯映射 |
| `adapters/stardew/src/Runtime/CapabilityCatalog.cs` | 修改 | 点位枚举、接近玩家的空参数 schema、能力描述及通用执行 metadata |
| `adapters/stardew/src/Runtime/RuntimeClient.cs` | 修改 | 动作路由、接近动作来源及结果、通知确认与断线收尾 |
| `adapters/stardew/src/Capabilities/MoveToCapability.cs` | 修改 | 复用固定终点移动执行，获取/释放移动控制权，保持既有坐标输入与异步语义 |
| `adapters/stardew/src/State/StardewObservation.cs`、`StardewObservationFactory.cs`、`ObservationBuilder.cs` | 修改 | 活动和节日当前事实；纯模型与游戏读取继续分离 |
| `adapters/stardew/src/Runtime/ProtocolMapper.Core.cs` | 修改 | 新的 namespaced Observation 字段、接近动作参数及结果映射 |
| `adapters/stardew/src/ModEntry.cs`、`adapters/stardew/GameAgent.Stardew.csproj` | 修改 | 依赖装配、生命周期接线、点位资源随构建输出 |

测试范围：

- 新增 `adapters/stardew/tests/ScheduledActivities.Tests/` xUnit 项目，链接纯活动模型、时间、管理器、来源和通知组件；使用注入的时钟与 Fake Driver，不依赖游戏 DLL。
- 在上述测试项目新增 `ApproachPlayerCapabilityTests`、`AdjacentTileSelectorTests`，以 Fake Driver / 可注入位置与路径结果验证选点、固定终点和控制权交接。
- 修改现有 `ProtocolMapper.Tests` 中 capability、Observation、事件和 ActionResult 测试及项目链接，覆盖接近玩家的空参数、Async metadata 与实际结果字段。
- 在现有 `ProtocolMapper.Tests` 补充 `NpcInteractionLifecycleTests` 及对话流程测试，通过 Fake 行为恢复入口验证自然结束、回复接续、异常关闭与旧回调隔离；真实原生日程恢复另做实机验收。
- 更新 `adapters/stardew/tests/check-context-static.ps1`，验证新能力、纯状态组件边界和主线程接线，保留既有对话与移动检查。
- 新增 `runtime/internal/gateway/deferred_activity_test.go`，使用通用 fake capability 和环境事件验证多 Turn 闭环、背压及实体隔离。
- 按实际缺口补充 Runtime Context / Memory 集成测试；生产 Runtime 变更只允许修正经测试证明的通用接入缺口。

Protocol `v1alpha2` 保持现有字段；本阶段没有预定 proto 或生成代码变更。

## 12. 开发里程碑

每个里程碑以业务断言驱动实现，新增代码完成相应失败测试、实现和回归后再进入下一项。开发中记录实际代码基线与验证结果。

| Milestone | 输入与交付 | 修改及测试重点 | 通过标准 |
| --- | --- | --- | --- |
| M0 游戏执行可行性 | 在现有游戏/SMAPI 版本验证指定 NPC 到三处点位、停留及正常日程恢复；冻结坐标与行程预留 | `StardewActivityDriver.cs`、点位配置；受控实机试验 | NPC 从主演示起点跨地图去沙滩并按时到达；无玩家跟随仍能推进；离开后继续日程；同一路线连续 3 次通过 |
| M1 活动领域与存档 | 纯活动记录、游戏时间、幂等、状态机、快照读写合同 | `ActivityModels.cs`、`ActivityTime.cs`、`ScheduledActivityManager.cs`、`ActivitySaveStore.cs`；`ActivityTimeTests`、`ScheduledActivityManagerTests`、`ActivitySaveStoreTests` | 所有合法转换与时间边界可由假时钟复现；重复请求/终态/回档不制造新活动；存档边界明确 |
| M2 能力与 Observation | 预约点位和接近玩家的目标语义、参数与结果合同，活动当前事实 | `ScheduleActivityCapability.cs`、`CapabilityCatalog.cs`、Observation 与 Mapper；两项能力的合同测试及现有 Mapper tests | 合法预约仅创建一次；接近输入不含坐标且声明 Async；结果区分固定终点与当前相邻；地图私有字段留在 Adapter |
| M3 世界行为执行 | 时间推进、跨地图赴约、等待、固定目标接近、到达检测、控制权交接和原生行为恢复 | 活动驱动、接近选点、NpcControlLease、NpcNativeBehaviorRestorer、到达检测及主线程接线；对应动作/选点/控制权测试 | 上午 Turn 结束后自动赴约；接近目标固定；到达触发一次；结束交互后 NPC 继续原生行为；尚未出发的预约保留 |
| M4 事件与认知闭环 | 活动事件、动作来源、UI 与 Turn 生命周期、回复接续、Memory 和诊断关联 | Publisher、SourceContextStore、NpcInteractionLifecycle、对话流程、RuntimeClient、Mapper；来源/生命周期/结果与通用环境集成测试 | 到达交互可接近；对话检查距离；Turn 终态不误判 UI 结束；真正对话结束无需模型即可恢复行为；实际结果进入模型；无游戏特判 |
| M5 联调与阶段验收 | 正向/爽约演示、异常分支、存档与断线边界、完整证据 | 第 13–14 节全部检查；`docs/phase9/` 下记录实测结果 | 约定自动化通过，实机两条主路径分别连续 3 次通过，风险已明确处置，负责人确认阶段状态 |

M0 是跨地图路线和原生日程恢复的可行性门。现有游戏 API 若无法稳定达到主场景要求，应报告具体限制并调整场景范围后再推进依赖任务；同地图测试通过不替代沙滩跨地图验收。

M1 向 M2 提供注册/查询及快照能力，向 M3 提供确定性的状态转换；M3 产出的真实转换交给 M4 映射为事件。Game Driver 只报告真实执行结果，Publisher 不改变业务状态，SourceContextStore 不充当活动存储。

## 13. 验收矩阵与演示脚本

### 13.1 确定性自动化

| 场景 | 必须验证的结果 |
| --- | --- |
| 09:00 注册 15:00–16:00 沙滩见面 | 注册 Action 立即终态、当前 Turn 可以结束；出发前原生日程继续 |
| 重复 action、不同 action 的等价预约 | 原响应/同一活动 ID；不同内容冲突；只有一次实际注册；原来源释放后等价 action 重试仍返回原结果 |
| NPC 已有另一预约 | 返回冲突，既有活动时间和点位保持 |
| HHMM 输入 1560、非 10 分钟刻度、结束早于开始 | 明确拒绝；分钟运算不能将 15:00 减 30 分钟算成 1470 |
| 出发、提前到达、到点玩家已在附近 | 真实位置驱动等待；开始时间触发一次见面 |
| 玩家在 15:50 到达 / 16:00 到达 | 分别为 `met` / `expired`；终态只发生一次 |
| NPC 至开始时仍未到达 | `interrupted / arrival_deadline_missed`；无玩家爽约事实 |
| 暂停、跨阈值跳时、直接跳过窗口、时间倒退 | 遵循第 6 节；不按现实时间赴约，不补造世界效果 |
| 节日、点位关闭、路线失效、控制器被替换 | 拒绝或中断原因准确；不覆盖游戏的新控制器 |
| `travelling / waiting` 期间调用 move_to 或 approach_player | 返回控制权冲突；活动继续按原状态推进 |
| 接近玩家时 NPC 已在合法相邻格 | 直接完成且无移动；当前位置为目标；终态一次 |
| 四邻格可达路径长度不同或并列 | 选择路径最短的合法格；并列按上、右、下、左排序；忽略其他实体占用格 |
| 某相邻格被占用、全部不可达、玩家跨地图 | 分别选择其他合法格、拒绝无可达格、拒绝不同地图；不移动到玩家占用格 |
| 有效交互来源执行接近时玩家已超出两格 | 同地图条件下允许选点，不误用既有 move_to 的距离门 |
| 玩家在接近途中移动或换地图 | 固定目标与初始路径保持；不重新选点/寻路，不增加模型调用；完成时准确返回 player_is_adjacent |
| 接近路径失效、控制器被替换、取消或读档 | 失败/中断/取消准确，终态一次；只释放自身控制器，不寻找替代目标 |
| `met` 后有效到达来源请求接近 | 原子交接本活动控制权，保留来源；完成后按到达交互条件交回或释放；met 事实保持 |
| 上午对话结束、普通 TurnCompletion、ClearConversations | 不删除已注册预约、不提前释放尚未到点的活动数据 |
| ActionResult / TurnCompletion 已到达，NPC 台词或回复 UI 仍打开 | 保持有效对话占用；不将 Turn 终态误判为用户结束对话 |
| 玩家读完最后一句、关闭回复菜单或退出自由输入 | 真实 UI 结束触发一次交互释放；下一次可运行更新恢复原生行为；不新增模型调用 |
| 提交回复并等待下一 Turn | 作为同一对话的接续，不在两个 presentation 之间提前恢复日程 |
| 普通对话、无预约的接近后对话、赴约对话结束 | 三条路径均恢复原生行为；没有后续对话的接近在 Turn 结束时释放占用 |
| 对话失败、取消、断线或收到旧对话结束回调 | 对应占用幂等收尾；旧回调不释放新对话或其他 Mod 的控制器 |
| 背景到达/超时事件请求远程对话 | 能力被拒绝；无对话窗口覆盖 |
| 到达事件已接纳，对话执行前玩家离开范围或重新读档 | 对话来源校验拒绝；接近按自身来源及距离规则处理；旧代次回调不能修改当前活动 |
| 队列繁忙、重复 EventAck、通知失败 | 固定事件 ID、有限重试、无重复认知；世界状态独立收尾 |
| 保存、换 world、重载、损坏快照 | 按存档作用域恢复；不串存档；损坏时不自动清空原数据 |
| 运行中断开 Runtime | 关闭本活动交互等待；预约不依赖连接继续，截止后释放 |
| 后续查询结果、第二 NPC、第二 world | 对应来源进入正确模型请求；身份隔离；成功/爽约/中断不混淆 |

Runtime 集成测试使用名称为 `reserve_slot` 的 fake capability 和 `reservation_state_changed` 事件，执行“注册 Turn 终态 → 环境时间推进 → 新事件新 Turn → 后续读取”的序列。无需真实等待数小时，也不能只使用 Stardew 名称证明通用性。

### 13.2 成功赴约演示

1. 准备普通日测试存档，记录游戏/SMAPI/Adapter/Runtime/模型版本、NPC、日期、出发路线与点位配置。
2. 上午对 NPC 输入“今天 15:00 在沙滩见面，你等我到 16:00，可以吗？”。核对模型调用 `schedule_activity`、成功 ActionResult 与随后确认的台词。
3. 关闭对话，玩家继续其他活动。核对上午 Turn 已终态、NPC 预约仍是 `scheduled`，没有持续运行的预约 Action。
4. 游戏时间推进，NPC 按已验证路线前往沙滩。玩家无需持续与 NPC 同地图；核对 NPC 在 15:00 前到达目标点位。
5. 玩家在 15:00–16:00 进入 NPC 两格范围，触发一次到达事件。核对新的 Turn 具有预约上下文，并产生真实的到达互动。
6. 关闭最后一段台词及回复 UI，核对 `met`、交互控制权释放与正常日程继续。在原生日程应移动的时间段，NPC 应实际离开交互位置，无需玩家再次点击或模型追加动作。再次对话时，检查模型请求含本次见面的真实来源，并观察合理的关联回应。

### 13.3 爽约与失败演示

使用独立测试运行再次预约同一窗口。玩家不去沙滩，观察 NPC 按时到达并等待，16:00 后离开；核对 `expired` 与正常日程恢复。稍后再与 NPC 对话，检查上下文中的等待地点、截止时间和未见面事实。

另选真实节日或受控输入验证 `festival_conflict`，并以不可达路线/控制权抢占验证 `interrupted`。模型的具体用词不固定；预约失败不能被表述为已创建，NPC 自身无法赴约不能被保存为玩家爽约。

### 13.4 保存、断线与证据

- 活动结束后正常睡觉保存、退出并重新加载，核对终态保留、新一天没有重复活动。
- 当天未保存就退出的测试，核对活动随游戏进展回到上次存档；Runtime 历史不能重建该预约。
- NPC 出发或等待时停止 Runtime，保持游戏继续运行，验证活动能够到期收尾且没有永久 waiting UI；自动重连不列为此项通过条件。
- 记录每次运行的 `activity_id`、事件/Turn/Action 关联、真实游戏位置和时间、保存结果、最终模型请求及日程释放结果。

成功赴约和爽约两条主路径各连续通过 3 次。连续失败应保留完整证据，不靠修改台词、控制台直接移动 NPC 或跳过跨地图段计为通过。受控推进时间用于边界测试时须单独标记，不能替代真实行程验证。

### 13.5 接近玩家实机验收

1. 在同地图有效交互中请求 NPC 走到玩家旁边，核对 `approach_player({})`、Adapter 选择的空闲格、真实移动和一次终态；模型参数中没有玩家坐标。
2. 在存在其他实体占用邻格的场景验证避让；NPC 已相邻时验证直接完成，不产生无意义移动。
3. 动作开始后移动玩家，核对目标格与初始路径保持不变。NPC 抵达旧位置旁的固定格时，output 反映真实终点及 `player_is_adjacent`，没有因玩家移动而新增的模型调用。
4. 在预约到达 Turn 中触发接近，核对活动停留控制权交接、来源保留、正常动作收尾和后续对话距离校验；同一活动的 `met` 事实保持不变。

### 13.6 对话结束后自主行动验收

1. 选择 NPC 原生日程应当移动的时间段，分别执行普通对话、无预约的接近后对话和赴约对话。
2. NPC 台词展示后观察 Runtime Turn 已结束但 UI 尚在的状态，确认没有提前恢复日程；提交一次回复，确认对话自然接续。
3. 读完最后一句或退出回复/自由输入，确认 NPC 在游戏恢复更新后继续原生日程并实际移动。记录 UI 结束、控制权释放和游戏移动证据，确认恢复过程没有新增模型请求或需要玩家再次点击。
4. 在交互中注入失败或断线，验证对应临时占用释放；原日程本就要求停留的场景以恢复原生控制为准，不额外生成随机移动。

## 14. 验收命令与通过标准

以下命令从仓库根目录执行，属于代码开发完成后的检查；文档发布本身不代表这些检查已执行。

```powershell
dotnet test adapters/stardew/tests/ScheduledActivities.Tests/ScheduledActivities.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj --configuration Debug
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
go test ./runtime/internal/gateway ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/memory -count=1
go test ./... -count=1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug
git diff --check
```

真实联调使用已验证游戏路径构建并安装 Adapter，通过仓库既有安装流程启动；Runtime 使用 `runtime/config/games/stardew-valley/agent.json`。工具链或游戏环境导致未能执行的检查单独记录，不能计入通过。

阶段验收同时满足：

1. M0–M5 的交付与自动化断言完成，Phase6 对话/异步动作和采用的 Phase8 Memory 版本回归通过。
2. 两条主路径具有真实模型、真实游戏、活动状态和后续上下文证据，均连续通过 3 次。
3. 已注册活动按游戏时间执行；接近玩家选点正确、目标固定、结果真实；普通及赴约对话真正结束后 NPC 自动继续原生行为；后台事件、交互与控制权边界稳定，异常能够收尾。
4. 活动数据与 Memory 的存储边界、游戏保存限制、离线通知限制均已验证并记录。
5. 实现复审的问题收敛，项目负责人依据证据确认 `Accepted` 或带明确限制的阶段状态。

## 15. 与后续阶段的衔接

| 阶段 | 验证重点 | 与预约活动的关系 |
| --- | --- | --- |
| Phase9 | 游戏行为最小闭环 | 在已建立的连接中完成多 Turn 活动，游戏侧具有独立执行与收尾能力 |
| Phase10 | Environment Reconnect / Capability Recovery | 重连、bootstrap、旧请求收尾、能力列表替换、实体与 Memory 作用域连续性；验证重连不会重复注册世界活动 |
| Phase11 | Evaluation / Developer Experience / 产品化 | 将已验证场景转为可重复评估与接入合同，完善安装、诊断及新 Adapter 开发体验 |

Runtime Durable Task/Goal 保持独立候选：负责持久意图、未来唤醒、重新决策及相应恢复语义，可调用世界提供的预约能力。Phase9 的活动存档及状态机属于游戏执行侧，不构成该 Runtime 子系统的实现验收。
