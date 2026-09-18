# GameAgent MVP0 Phase3 验收记录

> Date: 2026-08-20
> Status: Accepted with Known Limitations
> Scope: Protocol v1alpha2 + Runtime AgentSession 路由 + Stardew Adapter 多 NPC 泛化

## 1. 验收结论

Phase3 的核心目标已经通过自动化测试与 Stardew 真机 smoke test 验证：

```text
GameEvent(world_id + target_entity_id)
    -> Runtime 解析 AgentSession identity(game_id + world_id + entity_id)
    -> 按 AgentSession lane 串行调度
    -> Observe / Model / Action
    -> Stardew Adapter 执行 speak / emote
```

阶段状态定为 `Accepted with Known Limitations`。

该状态表示：Phase3 P0/P1 核心链路可进入 Phase4；少量非阻塞项作为后续阶段或 CI 环境补充。

## 2. 自动化验证

本轮验收执行结果：

```text
go test ./runtime/... ./protocol/gen/go/... -count=1                  PASS
protocol/tests/check-protocol-static.ps1                              PASS
protocol/tests/check-go-generation.ps1                                PASS
dotnet build adapters/stardew/GameAgent.Stardew.csproj                PASS
adapters/stardew/tests/ProtocolMapper.Tests                           PASS
adapters/stardew/tests/PlayerInteractProbe.Tests                      PASS
adapters/stardew/tests/ActionCancellationRegistry.Tests                PASS
adapters/stardew/tests/check-probe-static.ps1                          PASS
```

架构边界扫描结果：

```text
runtime/protocol has no Stardew or adapter references                  PASS
adapter has no runtime/internal references                             PASS
```

`go test -race` 当前 Windows 环境未通过环境门槛：

```text
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
```

该结果不是代码失败，而是本机 C toolchain 不支持 Go race detector 所需的 64-bit 编译模式。后续建议在 Linux/macOS CI 或正确安装 64-bit gcc 的 Windows 环境补跑：

```text
go test -race ./runtime/...
```

## 3. 真机 Smoke Test

证据文件：

```text
D:/data/project/game-agent/world-is-agent/runtime/.local/traces.jsonl
```

验证窗口：

```text
2026-08-20 18:27:00+08:00 之后
```

运行环境：

```text
game_id: stardew-valley
world_id: 火锅_416823588
protocol: v1alpha2
```

真机点击结果：

```text
npc:Linus       3 completed turns
npc:Gunther     3 completed turns
npc:Pam         1 completed turn
npc:Pierre      2 completed turns
npc:Caroline    1 completed turn
```

汇总：

```text
turn_completed: 10
turn_failed:    0
action_status:  ACTION_STATUS_SUCCEEDED x10
```

观察到的关键行为：

1. 多个 NPC 均能进入同一条 Runtime AgentTurn 链路。
2. `entity_id` 随点击目标变化，证明 Adapter 多 NPC 捕获与 `target_entity_id` 路由生效。
3. 同一 NPC 连续点击不会产生重叠 active turn；后续事件等待前一个 turn 完成后启动。
4. `world_id` 在真机 trace 中稳定为 `火锅_416823588`。
5. 快速连续点击同一 NPC 时，超过 lane 容量的事件会被 `session_queue_full` 拒绝，这是 Phase3 的有界背压语义，不是异常。

## 4. Phase3 Contract 对照

| 验收项 | 状态 | 证据 |
| --- | --- | --- |
| Protocol 升级到 v1alpha2 | PASS | static check + Go/C# build |
| `AdapterHello` 不携带 `save_id/world_id` | PASS | protocol static check |
| `GameEvent` 携带 `world_id + target_entity_id` | PASS | ProtocolMapper tests + SMAPI trace |
| `ObserveRequest / Observation / ActionRequest` 携带 `world_id` | PASS | protocol static check + gateway tests |
| Agent identity 使用 `game_id + world_id + entity_id` | PASS | `runtime/internal/session` 单测 |
| `session_id` 不参与 Agent identity | PASS | identity contract + 真机 trace 解读 |
| Runtime 按 `target_entity_id` 路由到目标实体 | PASS | gateway integration tests + 真机 trace |
| 同一 AgentSession 同时只有一个 active turn | PASS | same-NPC integration test + 真机连续点击 |
| 不同 AgentSession 有独立 lane | PASS | gateway integration test |
| EventAck duplicate / queue full / identity reject | PASS | gateway integration tests |
| Observation scope mismatch 可失败收敛 | PASS | stream environment tests |
| Adapter 执行前检查 `world_id` | PASS | ProtocolMapper / RuntimeClient tests and static check |
| Runtime 不依赖具体游戏 API | PASS | boundary scan |
| Adapter 不依赖 runtime/internal | PASS | boundary scan |

## 5. Known Limitations

1. 真机未强制验证“同时点击多个 NPC”。该操作在 Stardew 实际交互中不自然，Phase3 以 gateway 集成测试覆盖跨 NPC 独立 lane。
2. trace 中的 `session_id` 表示 EnvironmentSession，不表示 AgentSession。Phase3 真机验收按 `game_id/world_id/entity_id` 判断 Agent identity。
3. `ObservationBuilder` 的 nearby NPC 扩展未作为 P0 完成项落地；当前 Observation 仍满足 MVP0 one-turn loop 需要。
4. `go test -race` 需要在支持 Go race detector 的 CI 或本机 toolchain 中补跑。
5. Phase3 不实现 reconnect / durable queue / MemoryStore；这些属于后续阶段。

## 6. 进入 Phase4 的判断

Phase4 的前置依赖是 Agent Identity Contract Accepted。

本轮验收显示：

```text
稳定身份组成: game_id + world_id + entity_id
目标路由字段: GameEvent.target_entity_id
WorldScope 字段: world_id
同 Session 串行: ExecutionLane
多 NPC 泛化: Stardew Adapter AgentTargets + target selector
```

因此 Phase3 可以作为 Phase4 MemoryStore 设计与实现的身份基础。
