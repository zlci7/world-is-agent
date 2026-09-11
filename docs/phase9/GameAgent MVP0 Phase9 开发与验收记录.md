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
