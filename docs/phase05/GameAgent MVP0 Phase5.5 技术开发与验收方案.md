# GameAgent MVP0 Phase5.5 技术开发与验收方案

> **Status:** Implementation Baseline
> **Date:** 2026-08-27
> **Scope:** Stardew Adapter Context Enrichment
> **Architecture Baseline:** GameAgent Runtime Architecture v0.3
> **Roadmap Baseline:** GameAgent 阶段规划 v0.5
> **Protocol Baseline:** gameagent.protocol.v1alpha2 after Phase5
> **Reference:** [Stardew Adapter 方案对比](../adapter/Stardew Adapter 方案对比.md)

---

# 1. 阶段目标

Phase5 已经完成有界 multi-step AgentTurn、ordered ToolCall batch、ToolResult transcript 和 settle control。

Phase5.5 定义 Stardew Adapter 的正式当前事实模型：

```text
Stardew API
  -> StardewObservation
  -> Protocol Observation.state.stardew
  -> Runtime generic context renderer
  -> Model Request
```

本阶段主要证明：

> **Adapter 可以通过通用 Observation narrow waist 提供成熟的游戏当前事实；Runtime Core 不需要理解 Stardew 字段，也能把这些事实稳定交给 Agent 使用。**

---

# 2. 架构边界

## 2.1 Adapter 拥有 Stardew 当前事实

Stardew Adapter 负责从 Stardew API 采集、命名和归一化当前事实。

本阶段建立生产用 `StardewObservation` 模型，作为 Stardew Adapter 的 canonical observation schema。

```text
StardewObservation
  Time
  Weather
  Agent
  Player
  Relationship
  Scene
  Schedule
```

## 2.2 Runtime 只拥有通用承载

Runtime 负责接收 `Observation.state`，并以 provider-neutral 文本/消息格式交给模型。

Runtime 不新增：

```text
StardewObservation
StardewTime
StardewWeather
Stardew relationship parser
Stardew-specific prompt section
```

Runtime 可以增加通用 nested state 渲染测试，但不得读取 `state.stardew.*` 的具体语义。

## 2.3 AgentDefinition 不属于 Observation

`EntityRef.definition_id` 是 Agent Definition 绑定 key。

Observation 只描述当前世界事实，不承载：

```text
biography
traits
relationships prose
speaking style
canonical dialogue examples
long-term event history
```

这些内容属于未来 AgentDefinition source / Context source。

---

# 3. Observation State Schema

`Observation.state` 使用 adapter-owned namespace：

```json
{
  "stardew": {
    "schema_version": "0.1",
    "time": {
      "year": 2,
      "season": "fall",
      "day_of_month": 12,
      "weekday": "fri",
      "time_of_day": 1820,
      "time_bucket": "evening"
    },
    "weather": {
      "rain": true,
      "snow": false,
      "lightning": false,
      "green_rain": false
    },
    "agent": {
      "entity_id": "npc:Linus",
      "name": "Linus",
      "location": "Mountain",
      "tile": {
        "x": 35,
        "y": 12
      }
    },
    "player": {
      "entity_id": "player:local",
      "name": "ZLC",
      "location": "Mountain",
      "tile": {
        "x": 34,
        "y": 12
      },
      "gender": "male"
    },
    "relationship": {
      "known": true,
      "friendship_points": 850,
      "hearts": 3,
      "is_spouse": false,
      "is_roommate": false
    },
    "scene": {
      "trigger": "player_interacted_with_npc",
      "nearby_npcs_total": 2,
      "nearby_npcs_omitted_count": 0,
      "nearby_npcs": [
        {
          "entity_id": "npc:Robin",
          "name": "Robin",
          "location": "Mountain",
          "tile": {
            "x": 40,
            "y": 14
          }
        },
        {
          "entity_id": "npc:Demetrius",
          "name": "Demetrius",
          "location": "Mountain",
          "tile": {
            "x": 42,
            "y": 14
          }
        }
      ]
    },
    "schedule": {
      "destination": "Saloon",
      "future_locations": ["Saloon"]
    }
  }
}
```

字段规则：

- `schema_version` 标识 Stardew adapter state schema，不是 protocol version；
- 字段名使用 snake_case；
- `agent.entity_id` 与 `scene.nearby_npcs[*].entity_id` 使用稳定实体 ID，Stardew 固定 NPC 当前由 `npc.Name` 映射为 `npc:<Name>`；
- `name` 使用游戏展示名；展示名不得参与 entity identity；
- `time_bucket` 使用 `early_morning / late_morning / midday / afternoon / evening / late_night`；
- `time_bucket` 映射规则为 `<=800 early_morning`、`<=1130 late_morning`、`<=1400 midday`、`<=1700 afternoon`、`<=2200 evening`、其他为 `late_night`；
- `weekday` 优先由 `ObservationBuilder` 使用 `Game1.Date.DayOfWeek` 读取，并映射为 `Sunday=sun`、`Monday=mon`、`Tuesday=tue`、`Wednesday=wed`、`Thursday=thu`、`Friday=fri`、`Saturday=sat`；
- 纯归一化测试输入不可用游戏日期时，使用 `day_of_month % 7` fallback：`0=sun`、`1=mon`、`2=tue`、`3=wed`、`4=thu`、`5=fri`、`6=sat`；两条路径对同一天必须产出相同 weekday code；
- `relationship.hearts` 由 friendship points 派生，按 Stardew 250 points = 1 heart；
- `relationship.known` 的唯一判定源是 `player.friendshipData.ContainsKey(agent.Name)`；
- `relationship.known=false` 时不输出 `friendship_points` 与 `hearts`；
- `scene.nearby_npcs` 只包含同一 location 内、排除当前 agent 的 NPC 摘要；
- `scene.nearby_npcs` 最多输出 5 个 NPC，按与 agent tile 的 Manhattan distance 升序选择；距离相同时按 `entity_id` 升序稳定排序；
- `scene.nearby_npcs_total` 表示过滤后的附近 NPC 总数，`scene.nearby_npcs_omitted_count` 表示因 top-N 上限省略的数量；
- `Observation.nearby_entities` 必须包含 player 和 nearby NPC 的 `EntityRef`；`state.stardew.scene.nearby_npcs` 是模型可读摘要，不是 identity 真源；
- 本阶段沿用 Stardew 固定 NPC 的 `entity_id` 作为 `definition_id` alias，不新增 AgentDefinition source；
- `Observation.nearby_entities` 的 nearby NPC 集合必须与 `scene.nearby_npcs` 的 top-N 集合一致；
- `EntityRef.definition_id` 只表示 Agent Definition 绑定 key，不表示当前实体身份；
- `schedule.destination` 只在 NPC 正在移动到新 location 时出现；
- `schedule.future_locations` 只包含当前时间之后、去重后的非空 location names；
- `schedule` 是 best-effort optional object；没有日程、日程结构不完整或解析失败时省略整个 `schedule` 对象，Observation 构建继续完成。

---

# 4. 开发范围

## 4.1 Adapter State Model

修改范围：

```text
adapters/stardew/src/State/StardewObservation.cs
adapters/stardew/src/State/StardewObservationFactory.cs
adapters/stardew/src/State/ObservationBuilder.cs
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Runtime/ProtocolMapper.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/tests/ProtocolMapper.Tests
adapters/stardew/tests/check-context-static.ps1
```

开发内容：

- 新增生产模型 `StardewObservation`；
- 新增纯归一化入口 `StardewObservationFactory`，负责 time bucket、weekday、relationship、nearby NPC 和 schedule summary 的确定性映射；
- `StardewObservationFactory` 不引用 `Game1`、`NPC`、`Farmer` 或其他 Stardew live object；
- `ObservationBuilder.Build(...)` 只负责从 Stardew API 读取事实并交给 `StardewObservationFactory` 归一化；
- 新增 `adapters/stardew/tests/check-context-static.ps1`，覆盖 Stardew context schema static checks；
- `ProtocolMapper.BuildObservation(...)` 输出 `Observation.state.stardew`；
- `ProtocolMapper.BuildObservation(...)` 将 player 与 nearby NPCs 同步输出到 `Observation.nearby_entities`；
- 生产 mapper 的 canonical state shape 为 `Observation.state.stardew`。

## 4.2 Stardew Fact Collection

采集内容：

- 时间：`Game1.year`、`Game1.currentSeason`、`Game1.dayOfMonth`、weekday、`Game1.timeOfDay`、time bucket；
- 天气：`Game1.IsRainingHere()`、`Game1.IsSnowingHere()`、`Game1.IsLightningHere()`、`Game1.IsGreenRainingHere()`；
- NPC：stable id、display name、location、tile；
- 玩家：name、location、tile、gender；
- 关系：`player.friendshipData.ContainsKey(agent.Name)`、points、hearts、married、roommate；
- 场景：trigger、nearby NPC entity_id / display name / location / tile、nearby total、omitted count；
- 日程：current destination、future locations；日程采集失败时省略 schedule。

## 4.3 Capability Metadata

修改范围：

```text
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/tests/ProtocolMapper.Tests
```

开发内容：

- `speak` 描述当前环境效果：NPC 向玩家显示一条对话文本；
- `emote` 描述当前环境效果：NPC 显示一个 emote bubble；
- tool description 只描述能力效果，不承载 per-turn policy。

## 4.4 Runtime Boundary Test

修改范围：

```text
runtime/internal/context
```

开发内容：

- 增加 nested game-specific state 渲染测试；
- 测试只断言通用结构能稳定进入 model request；
- 测试不得读取、解释或特判 `stardew` 字段语义。

---

# 5. 非目标

```text
Protocol 字段变更
Go / C# protocol regeneration
Runtime Stardew-specific logic
AgentDefinition store
Agent biography / traits / relationships prose
Canonical dialogue retrieval
Long-term event memory persistence
Vector retrieval
ValleyTalk prompt builder migration
New Harmony patches for gift / typed response / event dialogue
move_to
Async Action lifecycle
Turn suspend / resume
```

---

# 6. Milestones And Acceptance

## M1：Stardew Observation Schema

目标：

```text
生产路径使用 StardewObservation，Protocol Observation.state 输出 namespaced stardew object。
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
```

通过标准：

- 测试覆盖 `state.stardew.schema_version`；
- 测试覆盖 time、weather、agent、player、relationship、scene、schedule；
- mapper 测试断言 namespaced canonical state；
- `Observation.nearby_entities` 包含 player 与 nearby NPC entity refs；
- `Observation.nearby_entities` 的 nearby NPC 与 `state.stardew.scene.nearby_npcs` 的 top-N 结果一致；
- nearby NPC `EntityRef.entity_id` 使用 `npc:<Name>`，`EntityRef.definition_id` 只作为 Agent Definition 绑定 key。

## M2：Stardew Fact Collection

目标：

```text
ObservationBuilder 从 Stardew API 采集当前事实，并归一化为 StardewObservation。
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj
```

通过标准：

- time bucket 与 weekday 有确定性映射；
- weekday 优先使用游戏原生 `Game1.Date.DayOfWeek`；
- `StardewObservationFactory` 可脱离运行中的游戏单测，且不引用 `Game1` 静态状态；
- relationship known 使用 `player.friendshipData.ContainsKey(agent.Name)`；
- `known=false` 时不输出 `friendship_points` 与 `hearts`；
- nearby NPCs 排除当前 agent，并按距离取前 5 个；
- nearby NPCs 同时进入 `Observation.state.stardew.scene.nearby_npcs` 和 `Observation.nearby_entities`；
- `nearby_npcs_total` 与 `nearby_npcs_omitted_count` 能说明被省略数量；
- schedule summary 对 null schedule / null destination / 解析失败安全；
- schedule 解析失败不阻断 Observation 构建；
- adapter build 无 warning / error。

## M3：Capability Metadata

目标：

```text
speak / emote capability description 只描述环境效果，避免把 per-turn policy 写进 tool schema。
```

验收命令：

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
```

通过标准：

- `speak` description 说明 dialogue text 的游戏效果；
- `emote` description 说明 emote bubble 的游戏效果；
- `speak` / `emote` 保持 `Sequential`；
- capability schema 不新增 runtime policy 字段。

## M4：Runtime Boundary Regression

目标：

```text
Runtime 证明可以承载 nested game-specific Observation state，同时不引入 Stardew-specific 分支。
```

验收命令：

```powershell
go test ./runtime/internal/context
go test ./runtime/...
```

通过标准：

- context renderer 输出 nested `state.stardew` 内容；
- runtime/internal 不新增 Stardew-specific 类型或 parser；
- Phase5 transcript、memory、multi-step agent tests 保持通过。

## M5：Full Regression

验收命令：

```powershell
go test ./runtime/... ./protocol/gen/go/...
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet run --project adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet run --project adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
dotnet build adapters/stardew/GameAgent.Stardew.csproj
git diff --check
```

通过标准：

- 全部 PASS；
- protocol static check 不需要变化；
- Runtime 不引用 adapter 项目或 Stardew API；
- Adapter 不依赖 runtime/internal；
- 真实 Stardew trace 中 `[Current Observation]` 可以看到 `stardew` namespaced state。

---

# 7. 阶段验收状态

Phase5.5 可以验收为 `Accepted` 的最低条件：

1. `Observation.state.stardew` 成为 Stardew Adapter 的 canonical current-fact schema；
2. Adapter 测试覆盖核心字段与 null-safe 场景；
3. Runtime 只做通用 nested state 渲染；
4. Phase5 multi-step 行为回归通过；
5. `Observation.nearby_entities` 携带 player 与 nearby NPC entity refs；
6. 文档明确 AgentDefinition source、canonical dialogue retrieval、long-term event memory 不属于本阶段。

---

# 8. 后续进入 Phase6 的边界

Phase6 继续聚焦异步 Action lifecycle 与 Turn Resume。

Phase5.5 输出的 richer observation 作为 Phase6 的输入事实基础，用于支持 `move_to` 等真实长 Action 的上下文判断。Phase6 不因为 richer observation 而接管 Stardew 语义。

AgentDefinition source、canonical dialogue retrieval 和长期 event memory 作为 Phase6 之后的独立候选能力重新评审。
