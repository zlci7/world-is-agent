# GameAgent MVP0 Phase9.3 Stardew 行动与交互技术开发方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-10
> **执行方式:** 使用 executing-plans 连续实现；单元测试和游戏可行性验证随开发进行，集中 review 在 Phase9.5。
> **Goal:** NPC 能真实赴约、等待和接近玩家，并在交互结束后继续当前原生日程。
> **Architecture:** Adapter 管理当前世界效果及控制权；未来任务和唤醒始终由 Runtime 管理。
> **Tech Stack:** C# / .NET 6、SMAPI、游戏原生寻路与 UI；纯逻辑使用 xUnit。
> **Spec:** [Phase9 总方案](./GameAgent%20MVP0%20Phase9%20技术开发与验收方案.md)
> **前置接口:** [Phase9.2 协议与执行](./GameAgent%20MVP0%20Phase9.2%20Runtime%20Tools%20与调度接入技术开发方案.md)
> **后续:** [Phase9.4 检查点与结果记忆](./GameAgent%20MVP0%20Phase9.4%20检查点与结果记忆技术开发方案.md)

## 1. 全局约束

- 仅单人存档、当前玩家、配置受控 NPC；不实现多人控制权协商。
- 世界读取、位置/寻路、controller、UI、SaveData 均在游戏主线程。
- GameClock 输出稳定逻辑分钟，不能用 Game1.ticks 或现实时间计算预约到期。
- 使用总方案中的目标日、窗口、节日规则；跨天/季/年预约成立，单次会面窗口位于一个目标日。
- 一次性接近保持固定终点和所选路径；玩家途中移动不触发重选或模型调用。
- UI 展示成功、Turn 完成、Task 成功分别不是“玩家结束对话”。
- 只有自身持有的控制器可以被本模块停止；控制权丢失时保留新拥有者的控制器。

## 2. 文件与责任

路径以 `adapters/stardew/` 为根。

| 文件 | 责任 |
| --- | --- |
| `src/Tasks/GameClock.cs`、`MeetingContract.cs`、`LandmarkCatalog.cs` | 纯时间转换、不可变约定、点位配置校验 |
| `assets/landmarks.json` | 9.0 实测后的沙滩、酒馆、广场配置及行程预留 |
| `src/Tasks/TaskExecutionDriver.cs`、`GameNpcDriver.cs` | 当前跨地图路径、进展、操作终态与游戏 API 隔离 |
| `src/Tasks/MeetingWaitMonitor.cs` | 当前等待监听、[start,end) 判定及一次性事实 |
| `src/Tasks/TaskSourceContextStore.cs`、`TaskOperationReceipts.cs` | 背景/到达来源校验、当前 run 回执去重 |
| `src/Capabilities/ResolveMeetingCapability.cs`、`MoveToLandmarkCapability.cs`、`WaitForPlayerCapability.cs` | 三个世界能力的参数和执行 |
| `src/Capabilities/AdjacentTileSelector.cs`、`ApproachPlayerCapability.cs` | 四邻格固定选点与 Async 移动 |
| `src/Capabilities/NpcControlLease.cs`、`NpcNativeBehaviorRestorer.cs` | 唯一租约、控制权交接、当前原生日程恢复 |
| `src/Dialogue/NpcInteractionLifecycle.cs` | 跨 Turn 的交互占用与真实结束 |
| `src/Dialogue/DialoguePresentationFlow.cs`、`DialogueInteractionController.cs`、`DialogueInteractionMenu.cs` | 修改展示/回复/退出信号接线 |
| `src/Capabilities/MoveToCapability.cs`、`PresentDialogueCapability.cs` | 旧能力共享租约，保留原参数合同 |
| `src/Runtime/RuntimeClient.cs`、`CapabilityCatalog.cs`、`RuntimeWorldScope.cs`、`ProtocolMapper.Task.cs` | Task-ready、协议接线、来源和新能力；Mapper 为新增 partial |
| `src/State/StardewObservation.cs`、`StardewObservationFactory.cs`、`ObservationBuilder.cs` | 当前活动事实、控制权、节日与操作证据 |
| `src/Events/PlayerInteractProbe.cs`、`src/ModEntry.cs`、`GameAgent.Stardew.csproj` | 输入抑制、主线程事件和资源输出 |
| `tests/TaskExecution.Tests/TaskExecution.Tests.csproj` | 新增纯逻辑测试项目，不引用游戏 DLL |

新测试项目沿用既有 net6.0、xUnit 2.9.2、runner 2.8.2、Test SDK 17.11.1；需要协议映射时复用现有 Google.Protobuf/Grpc.Tools 版本。只链接纯模型和 fake driver，真实游戏包装留在主项目。

## 3. 时钟、约定与语义点位

### 3.1 GameClock

```csharp
public static long ToTick(int year, int seasonIndex, int dayOfMonth, int hhmm);
public static int ToMinute(int hhmm);
```

标准日历：seasonIndex=0..3，dayOfMonth=1..28；绝对日序为 `(year-1)*112 + seasonIndex*28 + dayOfMonth-1`。Tick=绝对日序×1440+HHMM 转分钟，使用 checked long 运算。当前游戏时钟接受延长到次日凌晨的合法 HHMM；预约参数单独限制 06:00–22:00、10 分钟刻度。分钟部分必须在 0..59，不能把 1260 当合法时间。

SaveLoaded 生成新 world_run_id；DayStarted 保留同一个 run，只更新时间。Clock.sequence 在当前 run 内递增；Runtime 重新连接时继续当前 run 和最新 Clock，不能生成“读档”身份。

### 3.2 MeetingContract

不可变合同包含：SchemaVersion=1、LandmarkId、ParticipantEntityId、TargetDate、ClockId、DepartureAt、StartAt、EndAt。外层 TaskProposal 保存通用 wake/deadline/参与者/等价键，payload 保存游戏约定；读取时校验两层时间和参与者一致。

等价键按 game/world/NPC/player/landmark/规范化 start/end 稳定生成，不根据模型措辞变化。日期非法、地点不存在、目标日节日、节日信息未知、地点未开放、路线未支持或来不及出发均明确拒绝。

当前 NPC 实际能否执行在出发时重新确认；创建成功不是未来路径畅通保证。

### 3.3 LandmarkCatalog

每个配置项包含 landmark_id、display_name、location、tile{x,y}、open_start/open_end、supported_routes、departure_lead_minutes。

生产项固定为 beach_meeting_spot、saloon_meeting_spot、town_square。坐标、过图路线和提前量由 9.0 实测填写，不能拿 fake 地图测试坐标部署；加载时校验唯一 ID、整数坐标、开放窗口和正的 10 分钟倍数提前量。

提前量至少覆盖已测最慢正常行程并增加一个 10 分钟游戏刻度余量。若真实路线在已配置 Action/Turn 时间预算下无法完成，先调整受支持路线或有限技术预算并记录实测，不能把预约等待时间放进同一 Action。

静态 schema 公开 landmark_id 和名称；模型不需要地图坐标。实际坐标合法性、可达性、开放状态在 Game 主线程校验。

## 4. 能力与结果合同

| 能力 | 参数 | 模式 | 执行 |
| --- | --- | --- | --- |
| resolve_meeting | landmark_id、target_date{year,season,day_of_month}、start_time、end_time | Sync + Sequential | 当前有效玩家交互中校验并返回 TaskProposal，不创建任务 |
| move_to_landmark | landmark_id | Async + Sequential | 校验 task_source 与约定，当前开始真实跨地图移动 |
| wait_for_player | 空对象 | Sync + Sequential | 已到达该 Task 点位后安装有界等待，立即返回进展回执 |
| approach_player | 空对象 | Async + Sequential | 读取当前玩家位置，选择一个固定合法相邻格并移动 |

所有 schema 拒绝额外字段。resolve_meeting 的 season 使用 spring/summer/fall/winter，时间 HHMM 为整数。所有 Task/operation/来源字段从 Runtime 请求取得，不接受模型参数覆盖。

### 4.1 当前跨地图执行

GameNpcDriver 隔离实际游戏调用；TaskExecutionDriver 只编排一个已经开始的 operation，不保存未来任务列表。其接口使用：

```csharp
public sealed record WorldPosition(string Location, int X, int Y);
public sealed record OperationKey(string WorldId, string RunId, ulong Generation,
    string NpcEntityId, string TaskId, string OperationId);
public sealed record DriverResult(string Status, string Code, WorldPosition Position);

public interface ITaskNpcDriver
{
    WorldPosition ReadPosition(string npcEntityId);
    DriverResult StartTravel(OperationKey operation, string landmarkId);
    DriverResult Poll(OperationKey operation);
    void Release(OperationKey operation, string reason);
}
```

纯测试使用 fake；GameNpcDriver 实现必须基于 9.0 核对过的本地游戏 API。StartTravel 的成功启动状态是 running，SUCCEEDED 仅由真实终点确认。Poll 不负责重新计算未来预约。

执行顺序：

1. 校验 world/run/generation、实体、Task 合同、点位一致、已到 departure、仍在有效窗口。
2. 校验 TaskOperationReceipts；同 operation/指纹重试返回旧回执，不重复安装路径；冲突拒绝。
3. 取得 travelling 租约，调用原生支持的跨地图路径入口。
4. 在正常地图出口通过游戏过图机制转换地图；不能绕过步行直接把 NPC 放到目标地图/格子。
5. 玩家不在该地图时仍能推进；不得对游戏已经更新的 controller 重复 Update 导致双倍速度。
6. 到 start_at 采样真实位置：尚未抵达则 interrupted/arrival_deadline_missed；恰好抵达合法。
7. 抵达后返回 Action SUCCEEDED 和 progress 证据；短暂持有同 Task 交接占用，等待 wait_for_player，技术交接最多 120 秒。

路径失败、取消、原生事件接管、控制权丢失均终态一次。移动成功仅证明到达，不生成 satisfied。

### 4.2 当前等待

安装等待时采用新的 operation_id，向同 Task 的到达占用请求原子交接。返回 lease_id、operation_id、progress Evidence、`wait_until=end_at`；Action 随即终态。

MeetingWaitMonitor 在 UpdateTicked/TimeChanged 使用实际位置与 Clock：

```text
if operation 已终态：无副作用
if 控制权/世界/来源失效：interrupted + 释放自身占用
else if now >= end：
    全程合法等待且未见面 → expired / unsatisfied
    否则 → interrupted 或未知，保留明确原因
else if now >= start 且玩家同地图且 ManhattanDistance <= 2：
    met / satisfied，建立 task_arrival 交互来源并交接停留
else：继续当前有界等待
```

此先后顺序保证 [start,end)；恰好截止不算按时到达。跨阈值跳时如果漏过整个窗口，不能推断玩家一直未出现或 NPC 持续等待，结果标记中断/未知。

监听直接释放过期租约，即使 Runtime/LLM 不在线也不会让 NPC 永久停住。future Task 的状态由 Runtime 协调，不在 Adapter 维护另一套任务状态机。

### 4.3 固定目标 approach_player

开始时先验证玩家交互或 task_arrival 来源、同地图和控制权；不复用 move_to 的“两格内才能开始”条件。

候选仅为玩家上、右、下、左四格。排除越界、不可通行、被其他实体占用和不可达格；NPC 自己所在格不是“其他实体占用”。已在合法相邻格则直接完成；其余比较真实路径边数，按上右下左打破并列。

```csharp
public readonly record struct Tile(int X, int Y);
public sealed record AdjacentCandidate(Tile Tile, bool Passable, bool OccupiedByOther,
    IReadOnlyList<Tile>? Path);
public sealed record AdjacentSelection(Tile Target, IReadOnlyList<Tile> Path);

public static AdjacentSelection? Select(Tile npc, Tile player,
    IReadOnlyList<AdjacentCandidate> candidates);
```

候选 Path 包含起点和终点，长度比较使用 Count-1；空/缺失路径不可达，已在候选格时路径为单格。Selector 校验候选确为四邻格，返回复制的路径，防止外部修改。

选择之后锁定玩家位置快照、地图、终点和路径。玩家移动或换地图时继续走原终点；原路径失效则终止，不重算路径、不选择其他候选、不额外调用模型。

结果字段固定为 player_position_at_start、target_tile、final_position、player_is_adjacent。SUCCEEDED 只表示到达固定终点，player_is_adjacent 在完成时重新读取真实玩家位置判断；两者可以是 SUCCEEDED + false。

错误沿用总方案第 8 节：different_location、no_reachable_adjacent_tile、npc_control_busy、move_failed、control_lost、world_changed、CANCELLED。清理只撤销自身控制器。

## 5. 来源、租约与对话

### 5.1 唯一租约

NpcControlLease 管理每个 NPC 唯一 owner token，模式为 interaction/travelling/waiting/approach；无租约表示由游戏原生行为接管。

```csharp
public sealed record LeaseToken(string Value, OperationKey Scope, string Mode);
public LeaseToken Acquire(OperationKey scope, string mode);
public LeaseToken Transfer(LeaseToken from, OperationKey to, string mode);
public bool Release(LeaseToken token, string reason);
```

Acquire 冲突返回稳定的 npc_control_busy；Transfer 先校验旧 token、同 world/run/NPC 和交接来源，再原子替换；过期 token 的 Release 返回 false 且无副作用。

- travelling/waiting 拒绝独立 move_to / approach_player。
- met 后本 Task 的合法到达来源可把停留转交 interaction，再交接 approach。
- TaskControlRequest 对已交接租约返回 handed_off，不关闭已开始的真实对话。
- approach 结束时若合法交互仍继续则交回 interaction，否则恢复原生行为。
- 实际 controller 被其他模块替换时，记录 control_lost；不得 Halt 他人的 controller。

### 5.2 来源存储

TaskSourceContextStore 保存当前 TaskActionSource 及独立 InteractionSource。背景 Task 来源只允许约定内的当下移动/等待，不允许远程打开玩家 UI。

met 时在主线程生成 task_arrival 来源，关联 source_id/world/run/generation/NPC/player/task/operation；发送真实 GameEvent。EventAck ACCEPTED 后才提交交互来源，动作时再次校验实际状态。Task 已 succeeded 不撤销该来源。

普通交互同样生成 kind=player 的 InteractionSource，并保留原 InteractionContextStore 的 event/turn 生命周期。模型不能通过填写 task_id 获得新的玩家来源。

present_dialogue 在实际展示时要求同地图、两格范围和菜单可用；approach 只要求来源有效及同地图。玩家离开后保留真实 met 事实，拒绝失效 UI，并释放未使用交互占用。

### 5.3 NpcInteractionLifecycle

按 world/run/NPC/conversation 跟踪：WaitingRuntime、NpcLine、ReplyInput、ReplySubmitted、Ended，以及当前在途 Action。它独立于单个 event/turn 的来源快照。

| 信号 | 行为 |
| --- | --- |
| 输入被接受、发送 Runtime 前 | 占用 NPC 并显示技术等待；失败则收尾 |
| present_dialogue 展示成功 | 切到 NpcLine；不释放 NPC |
| TurnCompletion | 释放该 Turn 来源；有 UI/回复接续/在途动作则保留交互占用 |
| 提交三选项或自由输入 | ReplySubmitted → WaitingRuntime；保持同 conversation 占用 |
| 最后台词关闭且无回复 | Ended；下一次可运行主线程更新恢复原生行为 |
| 退出回复菜单/自由输入 | Ended；恢复原生行为 |
| 接近完成但无后续 UI，Turn 已结束 | Ended；恢复原生行为 |
| 技术等待超过 120 秒/连接关闭/世界切换 | 幂等关闭自己的等待和占用 |

正常阅读和填写回复不计入技术等待超时。旧 conversation 回调不能结束新交互；展示结束与回复提交通过明确枚举信号区分，不用单个 IsFinished 推断两者。

PlayerInteractProbe 对已接受的受控 NPC 点击在原版处理前抑制输入；已经 in-flight 的重复点击也抑制。获取临时占用失败时不冒出一条原版台词后再补 LLM 台词。

### 5.4 当前原生日程恢复

NpcNativeBehaviorRestorer 只恢复本租约改变的暂停/朝向/移动标记，并根据当前日期和时刻衔接当前日程项与剩余路线。不要把上午的 controller 重新装回去，也不要仅置空 controller/Halt 后宣告恢复完成。

游戏 API 签名和离屏移动策略由 9.0 的本地程序集与实机探针确认，集中放在 GameNpcDriver / Restorer，不散落到纯状态机。日程当前要求驻足时允许驻足；验收选择原生应行走的时间并记录真实移动。

## 6. 主线程接线

| 事件 | 接线 |
| --- | --- |
| GameLaunched | 配置、能力、路线资源加载 |
| SaveLoaded | 新 run，清理旧控制权/来源，读取检查点标记并发起绑定 |
| DayStarted | 保留 run，更新 Clock 与实体目录；不清空未来 Task |
| TimeChanged | 先处理当前等待边界，再发布 Clock / Evidence |
| UpdateTicked | dispatcher、移动状态、等待采样、UI 结束、原生恢复 |
| DayEnding | 结束当日临时世界占用，保留 Runtime 未来 Task |
| ReturnedToTitle / Dispose | 释放自身占用与来源、撤销绑定 |
| Runtime 断连 | 主线程关闭技术等待、取消当前移动/等待，记录真实中断 |

Saving/Saved 的完整交接在 Phase9.4 实现。此阶段读取到已有但无法处理的 checkpoint 标记时保持 task-not-ready，不允许以空标记覆盖存档。

## 7. 开发单元及验收

### 9.3-A：纯时间、合同与配置

- [ ] 创建 TaskExecution.Tests，链接 GameClock/MeetingContract/LandmarkCatalog。
- [ ] 编写跨季/跨年、2400/2600 当前时钟、非法 HHMM、非法日期、目标日节日、开放时间和等价键测试。
- [ ] 实现 resolve_meeting schema/参数校验及 TaskProposal 映射；失败时不返回有效 proposal。
- [ ] 把 9.0 已验证配置作为资源输出，验证缺失/重复/非法点位明确失败。

```powershell
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --filter "FullyQualifiedName~GameClockTests|FullyQualifiedName~MeetingContractTests|FullyQualifiedName~LandmarkCatalogTests"
```

### 9.3-B：租约、跨地图及等待

- [ ] 先用 fake driver 测 Acquire/Transfer/Release、同 operation 重试、第三方接管。
- [ ] 实现 GameNpcDriver 与 TaskExecutionDriver；通过 9.0 路线验证，禁止在 production 分支使用 fake 成功回执。
- [ ] 实现 wait_for_player 和 MeetingWaitMonitor，确保 Sync Action 返回后仍有有限世界监听。
- [ ] 更新 Observation 与 TaskEvidence；对同 operation 只产生一次终态 fact_id。

```text
MeetingWaitMonitorTests
  start=900,end=960：899 玩家在旁仍不 met；900 到达可 met
  959 到达后再收到 960：只能 met，不得 expired
  960 才到达：不能 met
  NPC 没有按时到、路径中断、控制权丢失、跳过整个窗口：不能归责玩家
  合法等待到 960 且玩家未出现：unsatisfied，释放次数=1
```

```powershell
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --filter "FullyQualifiedName~NpcControlLeaseTests|FullyQualifiedName~TaskExecutionDriverTests|FullyQualifiedName~MeetingWaitMonitorTests"
```

### 9.3-C：approach_player

- [ ] 写纯 Selector 测试；候选占用、无路、并列、自身已相邻都覆盖。
- [ ] 实现固定路径控制和结果输出；测试过程中移动玩家，确认没有第二次选点/寻路。
- [ ] 接入普通交互和 met 到达来源；Task travelling/waiting 冲突保持明确。
- [ ] 保留 move_to 旧参数和同地图合同，迁移其清理到共享租约。

```csharp
[Fact]
public void TiesUseRightAfterBlockedUp()
{
    var npc = new Tile(12, 12);
    var player = new Tile(10, 10);
    var candidates = new[]
    {
        new AdjacentCandidate(new Tile(10, 9), true, true, null),
        new AdjacentCandidate(new Tile(11, 10), true, false,
            new[] {npc, new Tile(12, 11), new Tile(11, 11), new Tile(11, 10)}),
        new AdjacentCandidate(new Tile(10, 11), true, false,
            new[] {npc, new Tile(11, 12), new Tile(10, 12), new Tile(10, 11)})
    };
    var selected = AdjacentTileSelector.Select(npc, player, candidates);
    Assert.NotNull(selected);
    Assert.Equal(new Tile(11, 10), selected!.Target);
}
```

```powershell
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --filter "FullyQualifiedName~AdjacentTileSelectorTests|FullyQualifiedName~ApproachPlayerTests"
dotnet test adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
```

### 9.3-D：真实交互与原生恢复

- [ ] 给 DialoguePresentationFlow 增加显式结束原因及幂等回调，覆盖 ReplySubmitted 与 Ended。
- [ ] 在 PlayerInteractProbe/RuntimeClient 中取得交互占用后再排队发送；发送失败释放。
- [ ] TurnCompletion 不直接关闭仍活动的 UI；TaskControl 对 handed_off 不撤销合法交互。
- [ ] 接线最后台词关闭、菜单退出、自由输入退出和无 UI 的动作完成。
- [ ] 以实机验证思考停留、无原版台词、关掉对话后继续当前原生日程。

```text
NpcInteractionLifecycleTests
  begin → displayed → TurnCompleted：release=0
  replySubmitted → nextTurn：release=0，conversation 不变
  finalLineClosed 且无在途动作：restore=1
  再次收到 finalLineClosed / old TurnCompletion：restore 仍为 1
  读取/输入持续超过 120 秒：不超时；WaitingRuntime 超时：关闭并恢复
  Task succeeded + handed_off：UI 持续；原预约截止不取消该交互
```

```powershell
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet test adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug
```

## 8. 交接条件

- [ ] 连续运行下预约 → 出发 → 等待 → met/expired 的真实行为成立，玩家无需跟随。
- [ ] 四个能力、来源和租约测试通过；旧 move_to / 对话/取消回归通过。
- [ ] NPC 恢复原生行为有真实移动证据，而非仅日志和 controller=null。
- [ ] 无重复终态、第三方控制器误清理或永久停留。
- [ ] 保存未接通的能力边界在验收记录中明确，自动进入 Phase9.4 完成检查点与认知闭环。
