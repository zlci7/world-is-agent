# GameAgent MVP0 Phase9 开发与验收记录

## 1. 记录基线

| 项目 | 值 |
| --- | --- |
| 实现分支基点 | `e48292389bd2544c9a326ad20cdb65268fdfdbc5` |
| 实现分支 | `codex/phase9-durable-task` |
| 已检查代码基线 | `daf4f98` |
| 游戏版本 | Stardew Valley `1.6.15` build `24356` |
| SMAPI 版本 | `4.3.2` |
| Adapter 版本 | `0.1.0` |
| 实机验证日期 | `2026-09-11`（Asia/Shanghai） |

Phase9.0 的路线与保存探针仅作为默认关闭、显式启用的 console-only 诊断能力。它们不注册模型可见 capability，不构成 Phase9.3 行动实现或 Phase9.4 生产检查点实现。

## 2. 自动化与构建验证

Phase9 代码改动前在实现基点 `e48292389bd2544c9a326ad20cdb65268fdfdbc5` 运行 Phase6/Phase8 全量基线：

| 验证项 | 结果 |
| --- | --- |
| `go test ./... -count=1` | 全部 Go 测试包通过 |
| `ProtocolMapper.Tests` | 53 passed，0 failed，0 skipped |
| `PlayerInteractProbe.Tests` | 11 passed，0 failed，0 skipped |
| `ActionCancellationRegistry.Tests` | 5 passed，0 failed，0 skipped |
| Protocol static / architecture check | passed |
| Stardew context static check | passed |
| Stardew Debug build | succeeded，0 warnings，0 errors |

最终探针源码在提交 `0d47bee49ef64343694191f15f19932c7003fef5` 上完成 fresh 验证；未被 Adapter 探针改动的 Go 全量测试与架构检查也再次通过：

| 验证项 | 结果 |
| --- | --- |
| `Phase9Feasibility.Tests` | 108 passed，0 failed，0 skipped |
| `ProtocolMapper.Tests` | 53 passed，0 failed，0 skipped |
| `PlayerInteractProbe.Tests` | 11 passed，0 failed，0 skipped |
| `ActionCancellationRegistry.Tests` | 5 passed，0 failed，0 skipped |
| `go test ./... -count=1` | 全部 Go 测试包通过 |
| Protocol static / architecture check | passed |
| Stardew context static check | passed |
| Stardew Debug build | succeeded，0 warnings，0 errors |
| `git diff --check` | passed |

实机安装 DLL 与本地 Debug 构建 DLL 的 SHA-256 均为 `C02FC6D0768BEA352CB01DD2DD49FFCDE7DB27444922B54C2F48233FD91D7A3B`。

## 3. 跨地图与原生日程恢复

统一命令为 `gameagent_phase9_route_probe Linus Beach 28 36 2 30`。玩家全程停留在 `FarmHouse`，NPC 通过原生 schedule path controller 独立完成 `Mountain → Town → Beach`，未使用瞬移、直接坐标赋值或玩家点击推动过图。目标、到达和释放位置均为 `Beach (28,36)`。

| 轮次 | Run ID | 起点 | 路线长度 | 过图 | 游戏时间 | travel | dwell | restore | 探针终态 | 释放至少 30 秒后 |
| --- | --- | --- | ---: | ---: | --- | ---: | ---: | ---: | --- | --- |
| 1 | `a68d342f3fa84a24a227048f9a0e05da` | `Mountain (39,5)` | 270 | 2 | `06:40 → 10:00`（200 分钟） | 145198 ms | 2000 ms | 18 ms | `Succeeded/native_movement_observed` | Linus 位于 `Town` |
| 2 | `a35636867ed74e90848cc68abb935321` | `Mountain (35,5)` | 266 | 2 | `06:30 → 10:10`（220 分钟） | 157915 ms | 2000 ms | 17 ms | `Succeeded/native_movement_observed` | Linus 位于 `Town` |
| 3 | `d70cc34865504d53b851241a642099ba` | `Mountain (35,5)` | 266 | 2 | `06:30 → 09:50`（200 分钟） | 143132 ms | 2000 ms | 17 ms | `Succeeded/native_movement_observed` | Linus 位于 `Town` |

三轮均记录 `restoration=native_schedule_rejoined`，随后位置变化由原生日程控制器产生。实测完整行程为 143.132–157.915 秒、200–220 游戏分钟，因此诊断行程上限采用单一有限值 180 秒；等于截止时刻仍允许依据真实到达进入 dwell，超过截止才报告 `route_timeout`。`Mountain → Beach` 的正式 `departure_lead_minutes` 取 240 游戏分钟，在三轮最大实测值之上保留 20 游戏分钟余量。

## 4. Saving 有界交接

前四个场景均在 SMAPI 的真实 Saving/Saved 周期中运行。Saving 线程为 1，模拟 Runtime 回应在 worker 线程完成；无论准备结果如何，游戏保存均继续完成。每轮均复制实际落盘文件，并在重新启动、加载同一测试存档后观测到对应 `persisted_marker_observed`。

| 场景 | Request ID | Saving 决策 | 等待 | 回应线程 | Saved | 重载后 marker |
| --- | --- | --- | ---: | --- | --- | --- |
| 正常成功 | `2df19aaf3668418d9a5e2c4bbf01206f` | `prepared` | 102 ms | 6 | 完成 | 已观测 |
| 故意延迟 | `862dc5f60b5e44cab41fa254770ad79a` | `unconfirmed/timeout` | 5002 ms | 截止后由线程 5 返回 `late_response` | 完成 | 已观测，保持超时快照 |
| 断线 | `d0ea168da4ac4a6f940c0aed109c4975` | `unconfirmed/disconnected` | 103 ms | 14 | 完成 | 已观测 |
| 准备失败 | `5aa35e8ee8e74ba891ef544ece77b038` | `unconfirmed/prepare_failed` | 102 ms | 3 | 完成 | 已观测 |
| 游戏保存失败 | `88c2ad9240474a8287e6e7179dba4a50` | `prepared` | 104 ms | 5 | 未发生；`saveTask` 报 IOException | marker 缺失，原 4 个文件哈希未变 |

第五个场景在隔离测试目录中以同名文件占据全局 `Saves` 路径，令 `Directory.CreateDirectory` 确定性失败。Saving 先取得 `prepared`，随后游戏记录 `saveTask failed` 且未触发 `Saved`；恢复目录后，旧存档四个文件哈希不变，重新加载得到 `persisted_marker_absent`。这验证了“快照提交成功、游戏保存失败”时新引用不会进入旧游戏存档。

结果证明 Saving 可进行有限等待，网络 worker 不依赖 SMAPI 主线程回调；超时、断线和准备失败具有明确的 `unconfirmed` 结果，并且不会阻止游戏保存；游戏保存自身失败时，已准备的新引用不会被误认为已保存。诊断 marker 使用 `gameagent-phase9-feasibility-save`，不能解释为生产 Task 快照、checkpoint durable ack 或恢复引用。

## 5. 存档隔离与环境恢复

Windows 版 Stardew 通过系统 ApplicationData 定位全局 `StardewValley/Saves`；仅覆盖子进程 `APPDATA` 不能形成存档隔离。安全实测流程必须在游戏退出后移开完整原始 `Saves` 目录，在原位置只放复制的测试 slot，结束后再整体换回原目录。

隔离行为确认期间，测试探针曾写入原始存档目录。测试前快照随后被完整恢复，后续实测均采用全局 `Saves` 目录交换。验收结束时确认：

- 原始两个存档目录及 `steam_autocloud.vdf` 已恢复；9 个原始文件的 SHA-256 全部与测试前记录一致；
- 恢复后的原始存档中不存在 `gameagent-phase9-feasibility-save`；
- `startup_preferences` 恢复为 `timesPlayed=526`、`pauseWhenOutOfFocus=true`；
- 已安装 Adapter 配置恢复为测试前内容，`AgentTargets` 为空，Phase9 探针未启用；
- 一次性测试存档副本保留在正式 `Saves` 目录之外，不参与游戏读档。

## 6. Phase9.0 结论

Phase9.0 前置可行性门通过：当前游戏版本支持玩家不跟随时的原生跨地图 NPC 路线、精确到点与有界停留，释放后可重新加入原生日程；SMAPI Saving 支持不依赖主线程回调的有限准备等待，失败与不确定结果不会阻塞实际保存。

后续实现遵循以下已验证边界：

- Phase9.3 使用原生 schedule path controller、显式控制权租约、180 秒有限预算和 `Mountain → Beach` 的 240 游戏分钟出发预留。
- Phase9.4 必须实现真实 Runtime 快照、持久化确认与精确恢复协议；不得复用诊断 marker 充当生产证据。
- 所有实机存档测试采用完整全局 `Saves` 目录交换，并在结束后执行文件级校验。

## 7. Phase9.1 Runtime Task 内核验收

Phase9.1 在 `codex/phase9-durable-task` 上完成，代码范围为 `runtime/internal/task/`，实现从领域合同、SQLite 权威状态、创建与意图，到证据收敛、可靠 wake、重启恢复和精确 checkpoint 的最小持久任务闭环。

| 审查单元 | 本地提交范围 | 结果 |
| --- | --- | --- |
| 9.1-A 模型、隔离与 SQLite | `1ba06df`–`3fde5f8` | 通过 |
| 9.1-B 创建与模型意图 | `8d5b347`–`37415b6` | 通过 |
| 9.1-C operation、证据与确定性收敛 | `47d94bc`–`dd22599` | 通过 |
| 9.1-D 可靠 wake 与不确定执行 | `a5cba6e`–`2b2bc78` | 通过 |
| 9.1-E 完整快照与 working head | `0b32564` | 通过 |
| 阶段整体 CR 修正 | `4563eb7` | 通过；最终对抗复核无剩余 Critical |

阶段整体 CR 验证了同 run Runtime 接管的 generation fencing 和最新单调 Clock、恢复时不受较小新建容量配置阻断、保存失败锁存、孤儿保存屏障、checkpoint Evidence 幂等去重，以及 Intent/Reconcile 消费 wake 前的持久关系校验。Runtime 保存屏障兜底为 10 秒；Adapter 的单次保存交接初始为 5 秒，在 5 秒边界仍保持 `save_in_progress`，到 10 秒才允许校验快照后解除。

### 7.1 自动化结果

| 验证项 | 结果 |
| --- | --- |
| `go test ./runtime/internal/task -run 'TestStore\|TestTaskValidation\|TestTaskIsolation' -count=1` | passed |
| `go test ./runtime/internal/task -run 'TestCreate\|TestIntent\|TestAdmission' -count=1` | passed |
| `go test ./runtime/internal/task -run 'TestEvidence\|TestOperation\|TestReconcile\|TestCleanup' -count=1` | passed |
| `go test ./runtime/internal/task -run 'TestWake\|TestNoProgress\|TestWorldClock\|TestRestart\|TestRecovery' -count=1` | passed |
| `go test ./runtime/internal/task -run 'TestCheckpoint\|TestWorldHead\|TestSaveBarrier' -count=1` | passed |
| `go test ./runtime/internal/task ./runtime/internal/session -count=1` | passed |
| `go test ./... -count=1` | 全部 Go 测试包通过 |
| `go test ./runtime/internal/task -run 'TestCheckpoint\|TestWorldClock\|TestRestart' -count=20` | passed |
| `go vet ./runtime/internal/task ./runtime/internal/session` | passed |
| `go list -deps ./runtime/internal/task` 禁止 agent / gateway / llm / model / tool 依赖 | passed |
| `git diff --check` | passed |

本机 `go test -race` 受当前 C 编译器不支持 64 位 cgo 阻断，未形成 race 通过结论。全仓 `go vet ./...` 仍报告 `runtime/internal/agent/loop.go:1189` 复制含锁 protobuf 值；该文件不在 Phase9.1 代码差异中，Phase9.1 范围内 vet 已通过。

### 7.2 阶段边界

Phase9.1 通过表示 Runtime Task 内核及其故障恢复边界可用。该阶段不接入 LLM、gRPC、Stardew API 或真实地图坐标，也不包含游戏预约、跨地图动作和实机存档联调；这些分别属于 Phase9.2–9.5，不能由本阶段自动化结果替代。

---

## 8. Phase9.2 Runtime Tools 与调度接入验收

### 8.1 提交范围

| 模块 | 提交 | 状态 |
|---|---|---|
| 合同锁定 | `7135236` | 通过 |
| A1 协议消息 | `e4d351e` | 通过 |
| A2 协议映射 | `71f2e1f` | 通过 |
| B1 Runtime 自有工具 | `f0ab4c4` | 通过 |
| C1 世界绑定 | `c969b8b` | 通过 |
| B2 Runtime Task 工具 | `28c301e` | 通过 |
| C2 wake 分发 | `79eb85e` | 通过 |
| D 执行闭环 | `29d49f4` | 通过 |
| 修复：运行期权威与有界收敛 | `f4c9917` | 通过 |
| 修复：手写测试移出生成目录 | `07c2047` | 通过 |

### 8.2 验证结果

| 检查 | 结果 |
|---|---|
| `go test ./... -count=1` | passed |
| `protocol/tests/check-protocol-static.ps1` | passed |
| `protocol/tests/check-go-generation.ps1` | passed |
| `scripts/check-architecture.ps1` | passed |
| `git diff --check` | passed |

9.2 的闭环证据全部来自真实 gRPC、临时 SQLite 与 fake Adapter/model。真实模型行为与真实游戏行为不属本阶段。

### 8.3 阶段边界

Phase9.2 通过表示 Runtime 侧的持久任务链路可用：协议消息、边界映射、Runtime 自有工具分流、世界绑定与 task-ready、wake 分发、执行收口。该阶段不接入 Stardew 真实能力、跨地图动作、原生日程与实机存档联调，也不能由 fake 闭环替代这些证据。

---

## 9. Phase9.3 Stardew 行动与交互验收

### 9.1 实机闭环证据

两条真实链路均在 Stardew Valley 实机、真实 Runtime、真实 SQLite 上完成。

**约定达成（met）**：`task_1789450222152318800_2038`

| 时刻 | 事实 |
|---|---|
| 07:00 | 出发（`community_center`，`Mountain` 起点） |
| 08:40 | `arrived`，到达 `Town (55,22)` |
| 08:50 | `wait_registered`，交出控制权并驻留 |
| 11:00 | `satisfied / met` |

终态 `succeeded`，`result.reason = satisfied`。清理结果为 `released`（移动）与 `handed_off`（等待已交接给合法对话）；对话结束后 NPC 恢复当前原生日程。

**约定未达成（expired）**：`task_1789453595087640500_2070`

| 时刻 | 事实 |
|---|---|
| 07:00 | 出发（`carpenter_shop`） |
| 07:40 | `arrived`，到达 `Mountain (10,26)` |
| 07:50 | `wait_registered` |
| 09:00 | `unsatisfied / expired` |

终态 `failed`，`result.reason = unsatisfied`，两个 operation 均 `released / task_terminal`。玩家全程未靠近，NPC 自行赴约并按时收口。

两条闭环的构建来源：met 在 `2fdd269` 上验证，expired 在 `5286b14` 上验证。此后到当前 HEAD 的改动只涉及交互日志与点击抑制，不改变这两条路径。

### 9.2 点位与路线范围变更

原计划要求"沙滩、酒馆、广场的生产点位、受支持路线、行程预留和实际加载预算均有对应证据；未验证路线保持不可用"。实际交付与计划不同，此处明确记录为范围变更：

```text
保留     beach_meeting_spot，Beach (28,36)，实测 200–220 游戏分钟，预留 240，
         supported_routes 固定为 npc:Linus + Mountain

新增     community_center   Town (55,22)
         pierre_store       Town (46,59)
         trailer            Town (79,67)
         mine_entrance      Mountain (53,6)
         carpenter_shop     Mountain (10,26)
         以上五点 supported_routes 为 "*" + "*"，即任意 NPC 从任意地图，
         且均未记录 measured_travel_minutes
```

变更理由是实机测试需要就近点位，避免每次都走跨图长路线。代价是这五个点不构成"已验证路线"：行程是否可达由 NPC 出发时的原生寻路判定，行程是否够用由预留值与 180 秒现实时间上限共同限制。酒馆与广场点位未采集。

### 9.3 已知限制

```text
- 等待期间不提供交流：等待持有 NPC 控制权，普通交互申请同一租约失败，
  点击已被抑制，不再弹出无法继续的原生对话框。
- 玩家离开两格范围会结束会话：NPC 待发的台词会丢失。该结束现在带原因日志
  （player_not_near / control_lost / no_dialogue_before_deadline）。
- update_task 参数为空时只返回 tool_arguments_invalid，对模型无指导性。
- 每回合仅允许一个异步动作，模型不知道该限制，可能浪费一步。
- 跨 run 任务恢复未接通，新 run 绑定无检查点时世界保持 paused。
```

### 9.4 阶段边界

Phase9.3 通过表示 Stardew 侧可完成"约定 → 出发 → 驻留等待 → met 或 expired → 释放控制并恢复原生日程"的真实闭环。该阶段不包含生产保存检查点、跨 run 任务恢复（属 Phase9.4）、真实模型行为评测与最终系统验收（属 Phase9.5），也不把自动重连退避与未验证路线纳入交付。

### 9.5 审查说明

方案要求由两个只读子 agent 完成阶段内部 review。本次执行中子 agent 未按预期接收任务并返回占位结果，内部 review 由执行者逐行自查完成，覆盖状态机、并发、失败路径、测试缺口与兼容性，发现的问题以独立 `fix:` 提交修复。该项如实标注为自查替代，不代表已获得独立上下文审查。

---

## 10. Phase9.4 检查点与结果记忆验收

### 10.1 提交范围

| 模块 | 提交 | 状态 |
|---|---|---|
| A 保存屏障（Runtime） | `fdbeec9` | 通过 |
| A 保存桥接（Adapter） | `b1c8f97` | 通过 |
| B 存档恢复与故障窗口 | `8b31ee8` | 通过 |
| B 保存后等待证据经观察回执交付 | `e36c971` | 通过 |
| C History 结果来源 | `2734ee7` | 通过 |
| C 终态结果发布器 | `c9c873f` | 通过 |
| D 最近结果与 Task Context | `c1e0b65` | 通过 |

### 10.2 验证结果

| 检查 | 结果 |
|---|---|
| `go test ./... -count=1` | passed |
| `TaskExecution.Tests` | 159/159 passed |
| `ProtocolMapper.Tests` | 104/104 passed |
| `Phase9Feasibility.Tests` | 108/108 passed |
| `PlayerInteractProbe.Tests` | 15/15 passed |
| `RuntimeClient.Tests` | 14/14 passed |
| `ActionCancellationRegistry.Tests` | 5/5 passed |
| `protocol/tests/check-protocol-static.ps1` | passed |
| `protocol/tests/check-go-generation.ps1` | passed |
| `scripts/check-architecture.ps1` | passed |
| `adapters/stardew/tests/check-context-static.ps1` | passed |
| Stardew Adapter Debug 构建 | 0 警告 0 错误 |
| `git diff --check` | passed |

保存、加载与故障窗口的证据来自真实 SQLite、真实 gRPC 与进程内 fake Adapter/model；结果记忆与 Task Context 的证据来自真实 SQLite 与脚本化 model，断言对象是最终 `model.Request`。真实模型行为、真实游戏保存与实机行为不属自动化范围。

### 10.3 阶段边界

Phase9.4 通过表示：游戏保存会把任务工作集写成精确引用并在屏障解除后重新绑定；同一 run 重启恢复最新工作集，读档按精确引用恢复，未确认引用保持 paused；任务终态无需模型 Turn 即可进入 History，并作为事实投影进入后续请求。

以下不在本阶段：真实模型行为评测与最终系统验收（属 Phase9.5）；SaveLoaded 新 run 的读档恢复与回退到更早存档的实机核验；自动重连退避；回档外结果的 History 补发。

### 10.4 待实机验收

代码全部完成后由用户在一次实机过程内执行，改动 Adapter 前需关闭游戏：

1. 保存后继续：完成一次游戏保存，任务仍按约定出发、等待并完成 met 或 expired。
2. 断线重连：Runtime 与 Adapter 断开后重连同一 run，绑定恢复 ready 并继续调度。
3. Runtime 重启恢复：重启 Runtime 后同一 run 绑定恢复 ready，任务与最近结果仍在。

验收重点：9.3 记录的"新 run 无检查点即 paused、每次开游戏必须清任务库与记忆"应消失——保存过一次的存档重新加载应凭精确引用恢复为 ready。

### 10.5 审查说明

本阶段按方案在每个模块完成后本地提交并暂停，代码收口后再次尝试启动只读子 agent 独立复核；子 agent 仍未按预期接收任务正文（只做了环境自检便等待指令），因此内部 review 由执行者逐行自查完成，覆盖批次键与指纹兼容、发布路径的幂等与失败隔离、只读快照与排序、预算裁剪顺序与不可裁剪集合。该项如实标注为自查替代，不代表已获得独立上下文审查。

### 10.6 预约时间语义

约定时刻即出发时刻：玩家说"下午 2 点在哪见"，任务的 `wake_at`、`departure_at`、`start_at` 三者相同，NPC 在 2 点触发任务并出发，不再提前出发抢答。点位配置的 `departure_lead_minutes` 因此只作为行程耗时估计，用于判定约定窗口是否够用（`end_at - start_at` 必须覆盖该估计），不再参与出发时刻计算。窗口不足时以 `meeting_window_below_travel_time` 拒绝；约定时刻不在未来时以 `departure_too_late` 拒绝。

该语义服务于"到点执行一件事"的通用异步任务，不再假定任务必须提前到达。移动类预约因此不再是执行异步任务的前提。
