# GameAgent MVP0 Phase9.3 路线采集与实机验收清单

## 1. 生产白名单现状

| landmark_id | NPC | 起点 | 终点 | 三次真实路线 | 实际耗时 | 原生日程恢复 | departure_lead_minutes | 状态 |
|---|---|---|---|---|---|---|---:|---|
| `beach_meeting_spot` | `npc:Linus` | `Mountain` | `Beach (28,36)` | 3/3 成功 | 143.132–157.915 秒；200–220 游戏分钟 | 3/3 `native_schedule_rejoined` | 240 | 已进入 `supported_routes` |

生产配置同时包含上表已验证路线和第 2 节的实机标注点位。路线身份固定为 `npc_id + origin_location + landmark_id`；同一终点的其他 NPC 或其他起点仍是未验证路线。

## 2. 实机标注点位

以下点位由实机命令 `gameagent_mark_landmark` 采集，未走第 4 节的晋升流程：`supported_routes` 为通配（任意 NPC、任意起点），且未记录实测行程。

| landmark_id | 位置 | 预留 | supported_routes | 实测行程 | 实机用途 |
|---|---|---:|---|---|---|
| `community_center` | Town (55,22) | 240 | `*` + `*` | 未记录 | 达成闭环（met） |
| `carpenter_shop` | Mountain (10,26) | 90 | `*` + `*` | 未记录 | 未达成闭环（expired） |
| `pierre_store` | Town (46,59) | 240 | `*` + `*` | 未记录 | 仅用于约定 |
| `trailer` | Town (79,67) | 240 | `*` + `*` | 未记录 | 仅用于约定 |
| `mine_entrance` | Mountain (53,6) | 240 | `*` + `*` | 未记录 | 仅用于约定 |

原计划的酒馆与广场候选点位未采集。通配点位只用于当前实机测试；若需要成为"已验证路线"，必须按第 4 节补三条独立路线证据，并把 `supported_routes` 收敛为具体 NPC 与起点。

## 3. 单条路线采集记录

命令：

```text
gameagent_phase9_route_probe <NPC> <Location> <X> <Y> 2 30
```

| 轮次 | run_id | 日期 | 开始时刻 | NPC/起点坐标 | 目标坐标 | 跨图路径 | 到达时刻 | 实际秒数 | 游戏分钟 | native_movement_observed | restoration | 结果/错误码 |
|---:|---|---|---:|---|---|---|---:|---:|---:|---|---|---|
| 1 |  |  |  |  |  |  |  |  |  |  |  |  |
| 2 |  |  |  |  |  |  |  |  |  |  |  |  |
| 3 |  |  |  |  |  |  |  |  |  |  |  |  |

同时保存以下证据：

- 玩家停留在其他地图时 NPC 仍由原生 controller 推进。
- 路线通过正常地图出口，不调用传送或直接位置赋值。
- 到达格与配置完全一致；等于行程截止时刻的到达单独标记。
- 取消、断连和第三方 controller 接管均不清理外部 controller。
- 释放后按当前日期与时刻建立新的原生日程路线，不复播旧 controller。

## 4. 晋升条件

每个 `npc_id + origin_location + landmark_id` 组合满足以下全部条件后，才加入 `assets/landmarks.json`：

- 地图名和坐标由实机读取并完成三次独立路线。
- 三次均到达同一目标格，记录真实秒数、游戏分钟和完整过图路径。
- 三次均观察到原生移动，玩家不跟随也能推进。
- 三次均完成当前时刻的原生日程恢复。
- 开放窗口来自实际场景条件；节日和关闭时段保持拒绝。
- `departure_lead_minutes` 为 10 分钟倍数，覆盖三次最大游戏行程并保留明确余量。
- 180 秒 Adapter 行程上限能够覆盖三次实际耗时。
- 自动化配置测试随生产白名单同步更新。

任何失败、未测试或证据不完整的候选项保持不可用，不写入 `supported_routes`。
