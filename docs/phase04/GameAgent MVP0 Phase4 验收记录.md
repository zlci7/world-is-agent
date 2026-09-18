# GameAgent MVP0 Phase4 验收记录

> Date: 2026-08-22
> Status: Accepted with Known Limitations
> Scope: Runtime short-term memory + ContextBuilder + deterministic gateway tests

## 1. 验收结论

Phase4 已通过自动化测试、架构边界检查和 Stardew 真机 smoke test：

```text
AgentSessionKey(game_id + world_id + entity_id)
    -> Runtime-scoped MemoryStore
    -> Recent Memory recall
    -> ContextBuilder / Renderer
    -> Provider model request
    -> successful ActionResult
    -> deterministic MemoryRecord append
```

当前阶段状态定为 `Accepted with Known Limitations`。

该状态表示：Runtime 侧短期记忆、ContextBuilder、MemoryStore contract、确定性测试、架构边界与基础真机连贯性已经完成。Phase4 不试图实现完整长期记忆、玩家文本输入、向量召回或复杂 freshness policy，这些留给 Phase5+ 之后按真实需求继续扩展。

## 2. 自动化验证

本轮验收执行结果：

```text
go test ./runtime/internal/gateway -count=1                          PASS
go test ./runtime/...                                                PASS
go test ./protocol/gen/go/...                                        PASS
protocol/tests/check-protocol-static.ps1                             PASS
protocol/tests/check-go-generation.ps1                               PASS
git diff --check                                                     PASS
```

`go test -race` 当前 Windows 环境未通过环境门槛：

```text
go test -race ./runtime/...                                          ENV BLOCKED
# runtime/cgo
cc1.exe: sorry, unimplemented: 64-bit mode not compiled in
```

该结果不是代码失败，而是本机 C toolchain 不支持 Go race detector 所需的 64-bit 编译模式。后续建议在 Linux/macOS CI 或正确安装 64-bit gcc 的 Windows 环境补跑：

```text
go test -race ./runtime/...
```

## 3. 自动化测试证据

| 验收项 | 状态 | 证据 |
| --- | --- | --- |
| 同一 NPC 后续 Turn 可以引用前一次相关信息 | PASS | `TestHandleEventLoadsRecentMemoryOnLaterTurn`, `TestConnectQueuedSameNPCEventReadsPreviousTurnMemory` |
| 不同 NPC Memory 不串线 | PASS | `TestInMemoryStoreIsolatesByAgentSessionKey`, `TestConnectDoesNotLeakMemoryAcrossNPCs` |
| Memory 绑定 AgentSessionKey | PASS | `memory.InMemoryStore` key = `AgentSessionKey`; store scope tests |
| 同一 AgentSessionKey 跨 EnvironmentSession 共享 Memory | PASS | `TestConnectSameAgentSessionReadsMemoryAfterReconnect` |
| MemoryEnabled=false 后 One-Turn 仍正常 | PASS | `TestHandleEventSkipsMemoryWhenDisabled` |
| Memory load failure fail-open | PASS | `TestHandleEventFailOpenWhenMemoryLoadFails` |
| Memory append failure 不改写成功 Turn | PASS | `TestHandleEventCompletesWhenMemoryAppendFails` |
| Memory projection failure 不改写成功 Turn | PASS | `TestHandleEventCompletesWhenMemoryProjectionFails` |
| Recent Memory 携带游戏内时间语境 | PASS | `TestProjectorBuildsRecordFromSuccessfulTurn`, renderer tests |
| Renderer 只输出短摘要，不暴露存储字段 | PASS | `TestRendererBuildsModelRequestWithMemoryObservationInstructionAndTools` |
| 同一天 / 非同一天 Memory 有可读时间标记 | PASS | `TestRendererMarksPreviousDayMemory` |
| Trace 说明 Context 加载 / 更新 | PASS | loop timeline assertions include `context_loaded`, `context_updated`, failure tests include `context_load_failed`, `context_update_failed` |
| RecentMemoryLimit 生效且不被 store 固定上限截断 | PASS | `TestHandleEventDefaultStoreRetainsAtLeastRecentMemoryLimit` |
| InMemoryStore 有 per-session 保留上限 | PASS | `TestInMemoryStorePrunesOldRecordsAfterDefaultLimit` |
| Context size limit 生效 | PASS | `TestRendererMemoryBudgetKeepsLatestRecordWhenSingleRecordExceedsLimit` |
| SourceTurnID 与 trace turn_id 同源 | PASS | `TestTurnTracerCanUseCallerProvidedTurnID`, `MemoryProjector` tests |
| Phase5 可脱离真实 Stardew / LLM 测多 step | PASS | gateway bufconn tests + fake provider + test-local deterministic helpers |

## 4. Architecture Boundary Check

独立记录：

```text
docs/phase4/GameAgent MVP0 Phase4 Architecture Boundary Check.md
```

边界结论：

| 边界 | 状态 |
| --- | --- |
| Runtime source has no Stardew / SMAPI / Adapter API dependency | PASS |
| Adapter source has no `runtime/internal` dependency | PASS |
| Memory / Context packages do not depend on Trace | PASS |
| ContextBuilder does not introduce provider-specific tokenizer | PASS |
| Phase4 does not introduce DB / vector / embedding dependency | PASS |
| Protocol source and generated Go code remain aligned | PASS |

## 5. Mandatory Deliverables

| 交付物 | 状态 | 证据 |
| --- | --- | --- |
| 技术方案文档 | PASS | `docs/phase4/GameAgent MVP0 Phase4 技术开发与验收方案.md` |
| Context Scope Contract | PASS | 技术方案 §5 |
| MemoryStore Contract | PASS | `runtime/internal/memory/store.go`, `record.go` |
| Deterministic TestEnvironment | PASS | test-local gateway bufconn helpers, fake provider, fake stream messages |
| 自动测试 | PASS | `go test ./runtime/...` |
| Stardew 真机验收记录 | PASS | 本文 §7 |
| Architecture Boundary Check | PASS | 本轮新增 Boundary Check 文档 |
| Phase4 Review / 学习回顾 | PASS | 本验收记录 |

## 6. Known Limitations

1. 当前 Stardew 交互事件没有玩家文本输入、选择或自然语言意图；NPC 只能基于“玩家又触发了一次交互”、当前 Observation 和自己最近说过 / 做过的事情继续回应。因此 Phase4 验收的是有限连贯性，不是完整双向对话。
2. Phase4 Memory 是 in-memory、Runtime 进程级临时记忆；Runtime 重启后记忆消失。
3. `MemoryContextSizeLimit` 是 soft limit。Phase4 优先保留最新一条 Recent Memory；如果未来出现很大的工具参数，需要在 Phase5+ 引入字段级摘要或截断策略。
4. Trace 只记录 `memory_count` / `memory_ids` / `memory_id` 等诊断信号，不写入完整 Memory 内容。这是为了避免 Trace 变成状态恢复来源，也避免日志膨胀。
5. Memory append 当前仍使用 turn context；未来持久化 backend 需要重新评估独立写入 timeout。
6. `go test -race` 受当前 Windows C toolchain 限制未完成，需要在 CI 或可用环境补跑。

## 7. Stardew Smoke Test 记录

本轮真机 smoke test 使用 Stardew Adapter 连续点击同一 NPC，验证短期记忆能被后续 Turn 召回，并且不会再把同一游戏分钟内的连续触发误判成“玩家离开后又回来”。

```text
GameID:     stardew-valley
WorldID:    火锅_416823588
EntityID:   npc:Linus
SessionID:  e8faca579248486abb25318dc83c86cb
Turns:      6
Result:     6 completed turns, 6 succeeded actions
Memory:     memory_count 0 -> 1 -> 2 -> 3 -> 4 -> 5
```

Adapter 侧对话样例：

```text
1. 这么早就来山里散步？清晨的空气真好。
2. 早啊，渝大师。山里的清晨总是很安静呢。
3. 早上好，渝大师。要一起看日出吗？
4. 早啊，渝大师。太阳快出来了，一起看吧。
5. 太阳快升起来了。看，那边已经开始泛红了。
6. 看，太阳出来了。新的一天开始了。
```

这次 smoke test 的关键变化是：Recent Memory 中不再只暴露“上次调用了 speak”，而是带有游戏内时间语境，例如 `today 06:20: said "..."`。模型能够把连续点击理解成同一天、同一时间附近的连续互动，而不是默认说“又来了 / 你回来了”。

当前 trace 可用于确认 recall 是否发生：

```text
context_loaded memory_count=0
context_loaded memory_count=1
context_loaded memory_count=2
...
context_updated memory_id=...
```

Trace 不保存完整 Recent Memory 文本；如需检查模型实际收到的 prompt，应在受控调试模式下临时记录 ModelRequest，不能把 Trace 当 Memory dump 使用。

## 8. 进入 Phase5 的判断

验收显示 Phase4 已提供 Phase5 需要的 deterministic foundation：

```text
Runtime-scoped MemoryStore
AgentSessionKey-scoped recent context
ContextBuilder / Renderer boundary
Provider-free deterministic gateway tests
Memory failure fail-open semantics
Trace events for context load/update diagnosis
```

Phase5 可以在不依赖真实 Stardew / 真实 LLM 的情况下继续开发多 step AgentTurn。

但 Phase5 开工前必须先接受多游戏兼容性 gate：

```text
docs/summary/GameAgent 多游戏兼容性与 Agent Binding 决策.md
```

该 gate 不改变 Phase4 的验收结论，也不改变 Phase4 Memory scope。

它只冻结后续架构语义：

```text
Entity != Agent Definition
Agent Definition / Archetype = game_id + definition_id
Agent Instance Descriptor / State / Memory = game_id + world_id + entity_id
```
