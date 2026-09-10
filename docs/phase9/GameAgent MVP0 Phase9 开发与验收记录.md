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

最终探针源码在提交 `0d47bee49ef64343694191f15f19932c7003fef5` 上完成 fresh 验证：

| 验证项 | 结果 |
| --- | --- |
| `Phase9Feasibility.Tests` | 108 passed，0 failed，0 skipped |
| `ProtocolMapper.Tests` | 53 passed，0 failed，0 skipped |
| `PlayerInteractProbe.Tests` | 11 passed，0 failed，0 skipped |
| `ActionCancellationRegistry.Tests` | 5 passed，0 failed，0 skipped |
| Stardew context static check | passed |
| Stardew Debug build | succeeded，0 warnings，0 errors |
| `git diff --check` | passed |

实机安装 DLL 与本地 Debug 构建 DLL 的 SHA-256 均为 `C02FC6D0768BEA352CB01DD2DD49FFCDE7DB27444922B54C2F48233FD91D7A3B`。

## 3. 跨地图与原生日程恢复

统一命令为 `gameagent_phase9_route_probe Linus Beach 28 36 2 30`。玩家全程停留在 `FarmHouse`，NPC 通过原生 schedule path controller 独立完成 `Mountain → Town → Beach`，未使用瞬移、直接坐标赋值或玩家点击推动过图。目标、到达和释放位置均为 `Beach (28,36)`。

| 轮次 | Run ID | 起点 | 路线长度 | 过图 | travel | dwell | restore | 探针终态 | 释放后 30 秒 |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- | --- |
| 1 | `a68d342f3fa84a24a227048f9a0e05da` | `Mountain (39,5)` | 270 | 2 | 145198 ms | 2000 ms | 18 ms | `Succeeded/native_movement_observed` | Linus 位于 `Town` |
| 2 | `a35636867ed74e90848cc68abb935321` | `Mountain (35,5)` | 266 | 2 | 157915 ms | 2000 ms | 17 ms | `Succeeded/native_movement_observed` | Linus 位于 `Town` |
| 3 | `d70cc34865504d53b851241a642099ba` | `Mountain (35,5)` | 266 | 2 | 143132 ms | 2000 ms | 17 ms | `Succeeded/native_movement_observed` | Linus 位于 `Town` |

三轮均记录 `restoration=native_schedule_rejoined`，随后位置变化由原生日程控制器产生。实测完整行程为 143.132–157.915 秒，因此诊断行程上限采用单一有限值 180 秒；等于截止时刻仍允许依据真实到达进入 dwell，超过截止才报告 `route_timeout`。

## 4. Saving 有界交接

四个场景均在 SMAPI 的真实 Saving/Saved 周期中运行。Saving 线程为 1，模拟 Runtime 回应在 worker 线程完成；无论准备结果如何，游戏保存均继续完成。每轮均复制实际落盘文件，并在重新启动、加载同一测试存档后观测到对应 `persisted_marker_observed`。

| 场景 | Request ID | Saving 决策 | 等待 | 回应线程 | Saved | 重载后 marker |
| --- | --- | --- | ---: | --- | --- | --- |
| 正常成功 | `2df19aaf3668418d9a5e2c4bbf01206f` | `prepared` | 102 ms | 6 | 完成 | 已观测 |
| 故意延迟 | `862dc5f60b5e44cab41fa254770ad79a` | `unconfirmed/timeout` | 5002 ms | 截止后由线程 5 返回 `late_response` | 完成 | 已观测，保持超时快照 |
| 断线 | `d0ea168da4ac4a6f940c0aed109c4975` | `unconfirmed/disconnected` | 103 ms | 14 | 完成 | 已观测 |
| 准备失败 | `5aa35e8ee8e74ba891ef544ece77b038` | `unconfirmed/prepare_failed` | 102 ms | 3 | 完成 | 已观测 |

结果证明 Saving 可进行有限等待，网络 worker 不依赖 SMAPI 主线程回调；超时、断线和准备失败具有明确的 `unconfirmed` 结果，并且不会阻止游戏保存。诊断 marker 使用 `gameagent-phase9-feasibility-save`，不能解释为生产 Task 快照、checkpoint durable ack 或恢复引用。

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

- Phase9.3 使用原生 schedule path controller、显式控制权租约和 180 秒有限预算；正式点位与出发预留以实测路线为依据。
- Phase9.4 必须实现真实 Runtime 快照、持久化确认与精确恢复协议；不得复用诊断 marker 充当生产证据。
- 所有实机存档测试采用完整全局 `Saves` 目录交换，并在结束后执行文件级校验。
