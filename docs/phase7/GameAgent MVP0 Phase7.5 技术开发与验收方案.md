# GameAgent MVP0 Phase7.5 技术开发与验收方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-06
> **Phase:** Phase7.5 Stardew Context Integration 与验收
> **Roadmap Baseline:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md) v1.9
> **Previous Gate:** Phase7.4 Accepted
> **Implementation Gate:** Phase7.4 Code Accepted
> **Review Required Before Coding:** Yes
> **Planning Baseline:** `main` @ `f4b1946`
> **Coding Baseline:** `main` @ `e50794c`

---

# 1. 阶段目标

Phase7.5 是 Phase7 Context Subsystem 的最终集成验收阶段。

本阶段主要证明：

```text
Stardew Adapter
    GameEvent / ContextFacts / Observation / CapabilityList
        ↓
Gateway
    canonical Target EntityRef
    AgentSessionKey
    Final TurnToolView
        ↓
AgentLoop
    Definition lookup
    Context Engine BuildResult
    ContextBuildReport summary
        ↓
Renderer
    model.Request
        ↓
Provider
    ToolCall / settle
        ↓
Scheduler + Stardew Adapter
    ActionRequest / ActionResult / TurnCompletion
        ↓
Recent Memory / Transcript
    next AgentStep / next AgentTurn context
```

Phase7.5 不继续扩展 Context 架构。它在 Phase7.1-7.4 已完成的 Definition、Tool View、Context Projection、Budget 和 BuildReport 基础上，完成 Stardew 内容校对、自动化集成证明、真实游戏 smoke test 和阶段状态收尾。

本阶段的核心问题是：

```text
真实 Stardew 玩家点击 NPC 后，模型是否稳定收到正确、当前、有界、可解释的 Context？
```

验收重点不是让真实模型输出固定台词，而是证明 Runtime 主链路已经把正确材料交给模型，并且 Adapter 只负责游戏翻译，不接管完整 prompt。

---

# 2. 非目标

Phase7.5 不做：

```text
Context Engine 重设计
Budget / BuildReport 重设计
Tool View 生命周期重设计
Definition Catalog schema 重设计
新增协议字段或重新生成 proto
新增 Stardew capability
Provider-specific tokenizer
Persistent Memory backend
Adapter reconnect / Environment Recovery
完整 Evaluation Framework
向量检索或语义压缩
长期人格 / 关系 / 事件系统
字段级 Stardew Observation 解析进入 Runtime Core
Runtime Core 根据 Stardew tool name 写特殊逻辑
把 Adapter 改造成 prompt builder
固定真实模型输出台词
复制 Stardew 原作台词或 ValleyTalk 文本
```

如果真实 smoke 暴露 Adapter 现有链路缺陷，本阶段可以修复最小必要 bug；修复必须保持 Adapter 只输出协议事件、观察、能力和动作结果，不把最终模型 prompt 放到 Adapter。

---

# 3. 输入基线

## 3.1 Phase7.1 Definition 链路

当前已存在：

```text
runtime/internal/definition
    GameDefinition
    AgentDefinition
    AgentInstanceDescriptor
    Catalog
    LoadCatalogFromDir

runtime/config/games/stardew-valley/definitions
    game.json
    npc-*.json
    archetype-town-villager.json
```

Definition lookup 语义保持：

```text
Game Definition lookup
    game_id

Agent Definition lookup
    game_id + definition_id

AgentSession / Memory scope
    game_id + world_id + entity_id
```

Phase7.5 不引入新的 Resolver、Repository 或 Definition backend。

## 3.2 Phase7.2 Tool View 链路

当前 Tool View 主链路保持：

```text
CapabilityList
    ↓
EnvironmentToolCatalog
    ↓
Final TurnToolView
    ├── model.Request.Tools
    └── Scheduler lookup
```

Stardew production capability 目标集合是：

```text
present_dialogue
emote
face_player
move_to
```

`speak` 可以保留在测试 fixture 或 legacy 代码路径中，但 Stardew production `CapabilityList` 不应暴露 `speak`。

## 3.3 Phase7.3 / Phase7.4 Context 链路

Phase7.5 implementation 依赖 Phase7.4 Accepted 后稳定存在以下合同：

```text
Engine.Build(BuildInput) -> BuildResult, error

BuildResult
    Projection ContextProjection
    Report     ContextBuildReport

Renderer.Render(BuildResult.Projection) -> model.Request

AgentLoop.buildModelRequest(...)
    -> model.Request
    -> ContextBuildReport
    -> error
```

`ContextBuildReport` 不进入模型输入，只进入测试、trace summary 和调试诊断。

Phase7.5 只验证这些合同在 Stardew-shaped 和真实 Stardew 链路中可用；如果 Phase7.4 Accepted commit 对类型名或函数签名有最终调整，Phase7.5 文档在开工前只同步命名，不改变阶段范围。

## 3.4 Stardew Adapter 当前能力

Stardew Adapter 已具备：

```text
AdapterHello / CapabilityList bootstrap
player_interacted_with_npc GameEvent
player_said_to_npc GameEvent
ContextFact(kind=utterance)
Observation.state.stardew
Observation.nearby_entities
present_dialogue native dialogue flow
reply options / free text handoff
face_player
emote
move_to async Action
TurnCompletion handling
gameagent_probe_npc [NPC name]
```

Phase7.5 以这些能力作为真实验收入口。

---

# 4. 设计范围

## 4.1 Definition 内容校对

Phase7.5 的最小内容验收对象是两个 Stardew NPC：

```text
npc:Abigail
npc:Linus
```

选择这两个 NPC 的原因：

```text
Abigail
    冒险、游戏、音乐、矿洞兴趣明显，角色语气容易与其他 NPC 区分。

Linus
    自然生活、独立、温和、远离城镇规则，角色设定与 Abigail 差异足够大。
```

硬性要求：

```text
game.json 可加载
npc-abigail.json 可加载
npc-linus.json 可加载
两个 NPC 的 definition_id 与 Stardew Adapter EntityRef.definition_id 一致
两个 NPC 的 Agent Definition 内容在 identity / personality / speech_style / preferences / behavior_guidelines 上有可测试差异
所有 model-visible 字段只描述稳定角色定义
source_version 说明当前内容来源
```

内容规则：

```text
不写当前存档事实
不写玩家关系、好感度、当天位置或当前任务
不写原作完整台词
不复制 ValleyTalk 或其他 Mod 文本
不把 Observation 中的实时事实搬进 Definition
不通过大小写变化表达 alias
```

当前已有完整 NPC JSON 集合。Phase7.5 不要求把每个 NPC 都做人审内容验收，但需要做全量结构校验，防止某个文件破坏 Catalog 加载。

## 4.2 Recording Provider Context 验证

Phase7.5 需要用 Fake / Recording Provider 证明 Runtime 生成的 `model.Request` 至少包含：

```text
Runtime Policy -> model.Request.System
Game Definition
Agent Definition
Agent Descriptor
Current Event
Current Event Context Facts
Current Observation
Recent Memory
Current Turn Transcript
Instruction
Final TurnToolView tools
```

Recording Provider 是精确断言 prompt 内容的权威入口。真实模型 smoke 只能证明链路和体验，不用于固定 prompt 断言。

核心断言：

```text
Abigail request
    包含 npc:Abigail Agent Definition
    不包含 npc:Linus Agent Definition

Linus request
    包含 npc:Linus Agent Definition
    不包含 npc:Abigail Agent Definition

player_said_to_npc request
    Current Event Context Facts 包含玩家回复文本
    Recent Memory 可包含上轮 successful visible outcome
    Current Observation 仍作为当前事实进入模型

missing definition request
    Agent Definition 渲染 fallback
    不伪造角色定义
    Provider 仍可被调用

shared definition request
    多个 entity 可共享同一 definition_id
    Agent Descriptor 使用各自 entity_id / display_name
    Memory 按 AgentSessionKey 隔离
```

## 4.3 Adapter 不提供完整 prompt

本阶段必须证明 Adapter 没有接管完整 prompt。

可验证合同：

```text
Adapter 提供：
    GameEvent
    ContextFact
    Observation
    CapabilityList
    ActionResult

Runtime 负责：
    Runtime Policy
    Definition lookup
    ContextProjection
    Budget / BuildReport
    model.Request
```

自动化验证方式：

```text
Recording Provider 捕获的 Request.System 来自 Runtime config
Recording Provider 捕获的 Game / Agent Definition 来自 Definition Catalog
fake Adapter 发送的 Event payload 不包含 Game Definition / Agent Definition / Instruction 也能生成完整 context
Observation.state.stardew 作为 generic JSON 进入 Current Observation，不被 Runtime Core 解析成 Definition 或 Instruction
adapters/stardew/tests/check-context-static.ps1 继续禁止 Adapter prompt-builder drift
```

## 4.4 ContextBuildReport 验证

Phase7.5 不改变 `ContextBuildReport` schema，但需要在 Stardew-shaped 集成测试和真实 smoke 中确认：

```text
context_request_built trace event 出现
report summary 记录 definition fallback 状态
report summary 记录 section estimated token summary
report summary 记录 final request estimated token summary
report summary 记录 ToolAdmissionReport bounded summary
report 不包含完整 prompt 文本
report 不包含大段 Observation.state 原文
```

预算失败路径只做回归确认，不把真实 Stardew smoke 建成 over-budget 场景。

## 4.5 真实 Stardew Smoke

Phase7.5 的真实 smoke 是 hard gate。

smoke 环境要求：

```text
Windows
.NET SDK
Stardew Valley
SMAPI
可启动的 Go Runtime
可用的真实 Provider 配置
Stardew 存档中至少有 Abigail 或 Linus 可到达
```

Runtime 启动命令：

```powershell
$env:GAMEAGENT_AGENT_CONFIG="runtime/config/games/stardew-valley/agent.json"
go run ./runtime/cmd/server
```

Adapter build / install：

```powershell
$gamePath = "D:\SteamLibrary\steamapps\common\Stardew Valley"
dotnet build adapters/stardew/GameAgent.Stardew.csproj `
  --configuration Debug `
  -p:GamePath="$gamePath"

powershell -ExecutionPolicy Bypass -File scripts/install-stardew-adapter.ps1 -GamePath "$gamePath"
```

SMAPI 内推荐使用：

```text
gameagent_probe_npc Abigail
gameagent_probe_npc Linus
```

也可以使用正常点击 NPC 触发。

真实 smoke 至少需要一条 `present_dialogue` action path 通过：某一轮必须出现 `ActionRequest` 和 `ActionResult`。`settle` 是合法收敛结果，但 settle-only turn 不能单独满足 Stardew dialogue smoke。

通过标准：

```text
SMAPI log:
    Runtime connected
    CapabilityList sent
    GameEvent sent
    EventAck accepted
    Observation sent
    ActionRequest received
    ActionResult sent
    TurnCompletion received

Runtime trace:
    agent turn started
    context_request_built
    model request generated
    present_dialogue action requested in at least one dialogue smoke turn
    settle allowed on additional turns
    turn completed

Game UI:
    present_dialogue 时 NPC 台词先出现在 Stardew 原生对话框
    继续后出现 reply options 或 free text row
    玩家选择 option 后产生 player_said_to_npc
    玩家 free text 提交后产生 player_said_to_npc
    交互结束后不出现重复 waiting menu 或卡死输入
```

真实模型验收只要求：

```text
Recording Provider 已证明角色定义进入请求
真实对话没有明显忽略或串用角色定义
对话自然且不明显串 NPC
Runtime / Adapter 无错误终态
TurnCompletion 正常收敛
```

不要求：

```text
固定工具选择
固定台词
固定 reply option 文案
固定移动路线
固定 token 数
```

---

# 5. 预计代码与文档改动

## 5.1 Runtime Definition / Context Tests

预计修改：

```text
runtime/internal/definition/catalog_test.go
runtime/internal/context/engine_test.go
runtime/internal/agent/loop_test.go
runtime/internal/gateway/gateway_integration_test.go
```

目标：

```text
加载 Stardew Definition Catalog
验证 Abigail / Linus 差异
验证 missing definition fallback
验证 shared definition memory isolation
验证 ContextBuildReport summary 可观测
验证 model.Request 不来自 Adapter prompt
```

## 5.2 Stardew Adapter Tests

预计运行现有测试：

```text
adapters/stardew/tests/ProtocolMapper.Tests
adapters/stardew/tests/ActionCancellationRegistry.Tests
adapters/stardew/tests/PlayerInteractProbe.Tests
adapters/stardew/tests/check-context-static.ps1
```

仅当真实 smoke 或测试暴露 adapter bug 时，才修改：

```text
adapters/stardew/src/Runtime/ProtocolMapper.Core.cs
adapters/stardew/src/Runtime/RuntimeClient.cs
adapters/stardew/src/Runtime/CapabilityCatalog.cs
adapters/stardew/src/State/*
adapters/stardew/src/Dialogue/*
adapters/stardew/tests/*
```

Adapter 修复不能新增 Runtime prompt builder，不能把 Definition、Budget 或 BuildReport 逻辑搬到 Adapter。

## 5.3 Definition 内容文档

预计修改：

```text
docs/summary/Context/Stardew NPC Definitions.md
docs/summary/Context/Definition Catalog.md
```

目标：

```text
记录 Phase7.5 内容验收 NPC
记录 Definition 内容来源规则
记录实机验收时实际使用的 NPC
```

如果 JSON 内容没有变化，不为了写验收记录而改动 Definition 文件。

## 5.4 阶段状态文档

预计修改：

```text
docs/phase7/GameAgent MVP0 Phase7.5 技术开发与验收方案.md
docs/summary/GameAgent 阶段规划.md
docs/STATUS.md
```

Phase7.5 代码和实机验收都通过后，才把状态更新为：

```text
Phase7.5 Code Review:
Accepted

Phase7:
Accepted
```

如果真实 smoke 通过但存在已知体验限制，状态使用：

```text
Accepted with Known Limitations
```

---

# 6. 测试计划

## 6.1 Runtime 自动化测试

必须运行：

```powershell
go test ./runtime/internal/definition -count=1
go test ./runtime/internal/context -count=1
go test ./runtime/internal/agent -count=1
go test ./runtime/internal/gateway -count=1
go test ./runtime/internal/tool -count=1
go test ./... -count=1
```

建议补充：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
```

如果 Phase7.5 不修改 proto，不运行 protocol generation check 作为 hard gate。

## 6.2 Definition Catalog 测试

覆盖：

```text
stardew-valley game.json 可加载
npc:Abigail 可按 game_id + definition_id 查询
npc:Linus 可按 game_id + definition_id 查询
Abigail / Linus model-visible 字段存在差异
所有 Stardew definition JSON 使用 schema_version=v1alpha1
所有 Stardew definition JSON 的 game_id 都是 stardew-valley
重复 definition_id 会继续失败
missing Agent Definition fallback 不阻止 Turn
```

## 6.3 Recording Provider 集成测试

覆盖：

```text
Abigail prompt 包含 Abigail Agent Definition
Linus prompt 包含 Linus Agent Definition
两个 prompt 都包含同一份 Stardew Game Definition
两个 prompt 的 Agent Descriptor entity_id / display_name 与各自 target 一致
player_said_to_npc 的 ContextFact text 进入 Current Event Context Facts
Recent Memory 不串 NPC
shared definition 多实体不串 Memory
missing definition fallback 不伪造 Definition
Final TurnToolView 中四个 Stardew-shaped tools 进入 model.Request.Tools
Scheduler 只能执行同一份 Final TurnToolView 中存在的 tool
ContextBuildReport summary 出现在 trace，不进入 prompt
```

## 6.4 Adapter 自动化测试

必须运行：

```powershell
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet test adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet test adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
```

真实 smoke 前必须成功 build：

```powershell
$gamePath = "D:\SteamLibrary\steamapps\common\Stardew Valley"
dotnet build adapters/stardew/GameAgent.Stardew.csproj `
  --configuration Debug `
  -p:GamePath="$gamePath"
```

## 6.5 真实 Stardew Smoke

最小验收脚本：

```text
1. 使用 stardew-valley agent config 启动 Runtime。
2. 编译并安装 Stardew Adapter。
3. 通过 SMAPI 启动 Stardew Valley。
4. 进入可交互存档。
5. 运行 gameagent_probe_npc Abigail。
6. 观察 SMAPI log、Runtime trace 和游戏 UI。
7. 对 Abigail dialogue 选择一个 generated option。
8. 再次触发 Abigail，观察上一轮玩家回复或 visible outcome 可进入上下文。
9. 运行 gameagent_probe_npc Linus。
10. 确认 Linus 对话没有出现 Abigail 的 Memory 或 Agent Definition。
11. 对 Linus 提交 free text。
12. 确认 player_said_to_npc 事件、ContextFact、Observation、ActionResult 和 TurnCompletion 都收敛。
```

通过记录：

```text
Runtime command
Adapter build command
SMAPI log excerpt
Runtime trace excerpt
测试 NPC
Provider 类型
是否出现 ActionRequest
是否出现 ActionResult
是否出现 TurnCompletion completed
是否出现 error / timeout / rejected
已知限制
```

验收记录只保存必要摘要，不把完整 prompt、完整模型输出或大段 Stardew state 写入文档。

---

# 7. 验收条件

Phase7.5 完成后必须满足：

```text
1. Phase7.4 已 Code Accepted。
2. Stardew Definition Catalog 可加载。
3. Abigail / Linus Agent Definition 可按 game_id + definition_id 查询。
4. Abigail / Linus 的模型可见 Definition 内容有稳定差异。
5. Recording Provider 能捕获包含正确 Game Definition、Agent Definition、Agent Descriptor、Event、ContextFacts、Observation、Recent Memory、Transcript、Instruction 和 Tools 的 model.Request。
6. Adapter 不提供完整 prompt；Runtime 负责 model.Request 构造。
7. Observation.state.stardew 作为 Current Observation generic JSON 可见，Runtime Core 不解析 Stardew-specific 字段生成 Definition / Instruction / ContextFact。
8. player_said_to_npc 的玩家回复通过 ContextFact 进入当前 turn context。
9. Recent Memory 可以承载上一轮 visible outcome，并在同一 AgentSession 后续 turn 可见。
10. 不同 NPC 的 Memory 不串线。
11. 多实体共享 definition_id 时，Descriptor 和 Memory 仍按各自 AgentSessionKey 隔离。
12. missing Agent Definition fallback 不伪造角色定义，也不阻止合法 Turn。
13. Final TurnToolView 中模型可见 tools 与 Scheduler 可执行 tools 一致。
14. 两个 EnvironmentSession 的 Tool View 不串线。
15. ContextBuildReport summary 可在 trace 中看到，且不进入模型 prompt。
16. 预算失败路径不调用 Provider、不提交 Action。
17. Phase5 multi-step 回归通过。
18. Phase6 async move_to 回归通过。
19. Tool Policy / TurnCompletion 回归通过。
20. Stardew Adapter 自动化测试通过。
21. Stardew Adapter context static check 通过。
22. Stardew Adapter build 通过。
23. 真实 Stardew smoke test 通过。
24. Runtime / Adapter 分离边界保持稳定。
25. Phase7.5 文档、Roadmap、Status 和验收记录同步完成。
```

Phase7.5 不以固定模型台词、固定 ToolCall 或固定 token 数作为验收条件。

---

# 8. Review Checklist

Review Phase7.5 时重点看：

```text
1. 是否没有重新设计 Phase7.4 的 Budget / BuildReport。
2. 是否没有重新设计 Phase7.2 的 Tool View 生命周期。
3. 是否没有重新设计 Phase7.1 的 Definition Catalog。
4. Runtime Core 是否仍不依赖 Stardew / SMAPI 类型。
5. Runtime Core 是否仍不解析 Observation.state.stardew 的具体字段。
6. Adapter 是否仍不构造完整模型 prompt。
7. Recording Provider 测试是否足以证明不同 NPC 的 Definition 差异。
8. Recent Memory 隔离是否覆盖不同 entity 与 shared definition 两种情况。
9. Tool View 隔离是否覆盖多 EnvironmentSession。
10. ContextBuildReport 是否只作为旁路诊断，不进入 prompt。
11. 真实 Stardew smoke 是否有明确日志和 trace 证据。
12. 真实模型输出是否没有被当成 deterministic 测试。
13. Definition 内容是否没有复制受版权保护的原作台词或第三方 Mod 文本。
14. Phase7 是否可以在本阶段结束后整体进入 Accepted 或 Accepted with Known Limitations。
```

---

# 9. Implementation Handoff

## M0：Phase7.4 Gate 与开发基线

交付：

```text
确认 Phase7.4 Code Accepted
记录 Phase7.5 code baseline commit
确认工作区没有未归属改动
确认真实 Stardew smoke 环境是否可用
```

验收点：

```text
Phase7.5 不在 Phase7.4 未验收代码上开始 implementation
7.5 代码提交范围可追溯
真实 smoke 的缺失环境在开工前记录为 blocker
```

## M1：Stardew Definition 内容与 Catalog 验证

交付：

```text
Definition Catalog fixture tests
Abigail / Linus acceptance assertions
Stardew NPC Definitions 文档同步
```

验收点：

```text
game.json、npc-abigail.json、npc-linus.json 可加载
Abigail / Linus 内容差异可测试
全量 Stardew JSON 不破坏 Catalog
Definition 内容保持静态设定边界
```

建议提交：

```text
test: validate Stardew context definitions
```

## M2：Recording Provider Context 主链路验证

交付：

```text
Gateway / AgentLoop integration tests
Recording Provider prompt capture
Abigail / Linus context comparison
Adapter-not-prompt-builder assertions
ContextBuildReport trace assertions
```

验收点：

```text
模型请求包含正确 Definition / Descriptor / Event / ContextFacts / Observation / Memory / Tools / Instruction
两个 NPC 的 Definition 不串
Adapter payload / Observation 不替代 Runtime Definition / Instruction
report summary 可见但不进入 prompt
```

建议提交：

```text
test: verify Stardew context request integration
```

## M3：Phase7 回归闭环

交付：

```text
missing definition fallback test
shared definition memory isolation test
multi EnvironmentSession Tool View isolation regression
multi-step / async / tool policy / TurnCompletion regression
```

验收点：

```text
Definition fallback 不阻断 Turn
shared definition 不共享 Memory
Final TurnToolView 不串 EnvironmentSession
Phase5 / Phase6 行为不因 Context 收口退化
```

建议提交：

```text
test: close Phase7 context regressions
```

## M4：Adapter 自动化与 Build 验证

交付：

```text
Stardew Adapter dotnet tests
Adapter context static check
Adapter Debug build
必要的最小 Adapter bugfix
```

验收点：

```text
ProtocolMapper 保持 ContextFact / Observation / CapabilityList 语义
present_dialogue / face_player / emote / move_to 能继续映射
Adapter 仍不暴露 production speak capability
Adapter 不生成完整 prompt
```

建议提交：

```text
test: verify Stardew adapter context surface
```

如果没有 Adapter 代码变化，只记录测试证据，不创建空提交。

## M5：真实 Stardew Smoke 与阶段收尾

交付：

```text
真实 Stardew smoke test
Phase7.5 验收记录
Roadmap / Status 同步
Phase7 总结
```

验收点：

```text
Abigail 或 Linus 至少完成一轮真实 NPC dialogue
玩家 option 或 free text 至少完成一轮 player_said_to_npc
Runtime trace 与 SMAPI log 能对应同一 event / turn
无 runtime panic、stream crash、stuck waiting menu 或 unresolved Turn
Phase7.5 状态准确反映验收结果
Phase7 总体状态可进入 Accepted 或 Accepted with Known Limitations
```

建议提交：

```text
docs: record Phase7.5 acceptance evidence
```

---

# 10. 最终 Gate

Phase7.5 最终 Gate：

```text
Architecture Direction:
Accepted

Phase7.5 Scope:
Accepted

Runtime / Adapter Boundary:
Accepted

Context Mainline:
Accepted

Stardew Integration:
Accepted

Development Readiness For Phase8:
Ready
```

Required Action：

```text
Phase7.5 implementation 前，先等待 Phase7.4 Code Accepted。
Phase7.5 技术方案 review 通过后，将本文档状态改为 Implementation Plan Accepted。
Phase7.5 代码、自动化测试和真实 Stardew smoke test 全部通过后，再把 Phase7.5 Code Review 标记为 Accepted。
Phase7.5 Accepted 后，同步 Roadmap / Status，并确认 Phase8 Persistent Recent Memory 是否仍是下一阶段最高优先级。
```

---

# 11. 下一阶段衔接

Phase7.5 完成后，Phase8 可以在稳定的 Context 主链路上增加 Persistent Recent Memory。

Phase8 主要看：

```text
Recent Memory 如何按 AgentSessionKey 持久保存
Runtime 重启后如何恢复同一 NPC 的 recent context
持久化失败如何 fail-open / fail-soft
世界时间回退时如何继续过滤 future memory
```

Phase8 不应重新设计 Phase7 已完成的 Definition、Tool View、Context Projection、Budget、BuildReport 或 Stardew Adapter prompt 边界。
