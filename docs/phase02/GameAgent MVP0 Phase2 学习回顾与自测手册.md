# GameAgent MVP0 Phase2 学习回顾与自测手册

> 用途：Phase2 验收记录 / Agent Harness 学习复盘 / 技术面试自测
> Scope：Turn observability + dynamic tools + timeout/cancel + prompt config
> 验收日期：2026-08-18

------

# 1. Phase2 到底交付了什么

Phase1 证明的是：

```text
GameEvent -> Observe -> LLM -> speak -> ActionResult
```

Phase2 证明的是：

```text
这一轮为什么发生、发生了多久、调用了哪个 tool、失败能否收敛、行为能否配置、新 tool 能否不改 Runtime 主链路接入。
```

换句话说，Phase2 不是把 Agent 做得更聪明，而是把 Agent Harness 做得更像一个可长期运行的 Runtime。

最终成果可以概括为四件事：

```text
1. Turn trace
    每一轮有效 GameEvent 都有 turn_id / trace_id；正常无 drop、无写入失败、进程未异常退出时会形成完整 JSONL timeline。

2. Dynamic tools
    Adapter 上报 Capability schema，Runtime 动态注册 speak + emote。

3. Timeout / cancel
    Observe / LLM / Action 都有阶段 timeout，Action 超时会 best-effort 发送 CancelActionRequest。

4. Prompt config
    输出语言、NPC 风格、回复长度和 tool instruction 从 agent.json 读取。
```

Phase2 完成后，GameAgent 从：

```text
能跑一次 demo
```

升级为：

```text
能观察、能配置、能失败收敛、能扩展简单工具的最小 Agent Turn Runtime
```

------

# 2. Phase2 不重复 Phase1 的地方

Phase1 主要回答：

```text
Runtime 和 Adapter 怎么连？
GameEvent 怎么触发 AgentLoop？
Capability / Tool / ToolCall / ActionRequest 是什么？
为什么需要 gRPC 双向流和 correlation？
```

Phase2 不再重复这些基础概念。

Phase2 主要回答：

```text
一轮 Agent Turn 怎么被观测？
新增 tool 为什么不应该改 Runtime 注册白名单？
LLM / Adapter 卡住时 Runtime 怎么脱身？
Action 已经发给 Adapter 后，timeout 为什么还需要 cancel？
Prompt 为什么要从代码常量变成配置？
```

如果 Phase1 是把链路接通，Phase2 是给链路加上运行时工程能力。

------

# 3. 当前完整链路

Phase2 后的一轮 turn：

```text
AdapterMessage.Event
    ↓
EventAck
    ↓
AgentLoop.HandleEvent
    ↓
事件类型与 NPC entity 过滤
    ↓
创建 turn_id / trace_id
    ↓
trace(turn_started)
    ↓
trace(observation_requested)
    ↓
Environment.Observe(with observe_timeout)
    ↓
trace(observation_received)
    ↓
BuildModelRequest(config-driven system prompt + messages + dynamic tools)
    ↓
trace(model_request_started)
    ↓
Provider.Generate(with llm_timeout)
    ↓
trace(model_response_received)
    ↓
ValidateToolCall(envelope only)
    ↓
trace(tool_call_selected)
    ↓
BuildActionRequest(generic ToolCall -> ActionRequest)
    ↓
trace(action_submit_started)
    ↓
Environment.SubmitAction(with action_timeout)
    ↓
ActionResult
    ↓
trace(action_result_received)
    ↓
trace(turn_completed / turn_failed)
```

如果 action 阶段超时：

```text
ActionRequest 已发送
    ↓
等待 ActionResult 超时
    ↓
Runtime 发送 CancelActionRequest(action_id, reason=action_timeout)
    ↓
turn_failed(action_timeout)
    ↓
Adapter 若尚未执行该 action，则执行前 gate 跳过
```

这里的 cancel 是 best-effort，不是事务回滚。

------

# 4. Turn Trace：我现在应该理解什么

Phase2 的 trace 不是故障排查平台，也不是 OpenTelemetry。

它只是一个轻量的 turn timeline：

```text
turn_started
observation_requested
observation_received
model_request_started
model_response_received
tool_call_selected
action_submit_started
action_result_received
turn_completed / turn_failed
```

默认落盘：

```text
runtime/.local/traces.jsonl
```

每一行是一条 event：

```json
{"schema_version":1,"trace_id":"turn_...","turn_id":"turn_...","seq":6,"event":"tool_call_selected","tool":"speak"}
```

关键约束分两层。

TurnTracer emission：

```text
trace_id == turn_id
    Phase2 简化规则，未来跨 turn trace 再拆开。

seq 连续递增
    用于复盘同一 turn 内的执行顺序。

terminal event 必须唯一且最后
    turn_completed 和 turn_failed 只能出现一个。
```

JSONL persistence：

```text
已持久化事件的 seq 递增
    但因为 drop / 写入失败 / 崩溃，文件里允许出现 seq 缺口。

terminal event 可能缺失
    JSONL 是 best-effort 观测数据，不是强一致审计日志。

Recorder 必须非阻塞
    Trace 不能拖慢游戏交互。

JSONL 是派生观测数据
    不能用它恢复 Runtime 状态或游戏状态。
```

自测问题：

1. 为什么 `message_id` 不能代表一次 Agent Turn？
2. 为什么 Phase2 让 `trace_id == turn_id`？
3. 为什么 terminal event 必须是最后一个事件？
4. 为什么 trace event 允许丢弃？
5. 为什么 JSONL recorder 不能阻塞 AgentLoop？
6. 为什么 trace 不进入 protobuf protocol？
7. 如果 trace 文件写失败，Runtime 应该启动失败吗？

你应该能回答：

> Trace 是 Runtime 内部的观测面，不是功能面。它帮助复盘一轮 Agent Turn，但不能改变 AgentLoop 的结果，也不能对游戏响应形成背压。

------

# 5. Dynamic Tools：为什么 schema 权威属于 Adapter

Phase1 里 Runtime 还隐含知道：

```text
speak(text: string)
```

Phase2 改成：

```text
Adapter CapabilityList
    speak + input_schema_json
    emote + input_schema_json
        ↓
Runtime ToolRegistry
    动态注册 ToolDefinition
        ↓
LLM tools
        ↓
ToolCall
        ↓
ActionRequest(capability = tool name, arguments 原样透传)
```

核心原则：

```text
谁执行，谁定义 schema。
```

Runtime 不理解 Stardew，也不应该理解：

```text
emote 有哪些合法值
speak 文本最终怎么展示
NPC.doEmote(int) 怎么映射
```

所以 Runtime 只做最小 envelope 检查：

```text
tool name 已注册
arguments 非 nil
input_schema_json 能被解析成 JSON
```

Runtime 不做：

```text
不校验 required
不校验 enum
不判断字段语义
不写死 speak / emote
不把 Stardew 知识塞回 Runtime
```

这里的信任不是“完全不检查任何东西”，而是：

```text
Runtime 信任第一方 Adapter 的 schema 业务语义。
Runtime 仍会做 name 非空、JSON 可解析这类协议 envelope / 解析健壮性检查。
```

Phase2 的策略也不是永久取消权限系统，而是：

```text
第一方 Stardew Adapter 的简单短时 capability
    ↓
解析成功后 1:1 暴露为 Tool
```

第三方 Adapter、危险动作或长任务 capability，后续仍需要 permission / policy 层。

自测问题：

1. 为什么 Adapter 是 Capability schema 的唯一事实来源？
2. Runtime 为什么不应该校验 emote 的 enum？
3. `ValidateToolCall` 为什么收窄成 envelope 校验？
4. 如果 Adapter 明天新增 `wave`，理想情况下 Runtime 哪些文件不用改？
5. Provider 层为了 OpenAI strict schema 改写 schema copy，和 Runtime 信任 Adapter schema 是否冲突？

你应该能回答：

> Runtime 是 narrow waist，只负责把 Adapter schema 透传给模型，并把模型 ToolCall 转成 ActionRequest。能力语义由 Adapter 定义，也由 Adapter 执行和兜底失败。

------

# 6. Timeout / Cancel：为什么不是简单 context.WithTimeout

Phase2 引入四层 timeout：

```text
turn_timeout_ms
observe_timeout_ms
llm_timeout_ms
action_timeout_ms
```

它们解决的是 bounded waiting：

```text
Runtime 不会因为 Adapter 不回、LLM 卡住、ActionResult 丢失而永久挂住当前 turn。
```

但 action 有副作用，所以 action timeout 多一步：

```text
Runtime 已经把 ActionRequest 发给 Adapter
    ↓
Runtime 超时不等了
    ↓
还要告诉 Adapter：如果这个 action 还没执行，请不要再执行
```

这就是：

```text
CancelActionRequest
```

Adapter 侧的关键点：

```text
CancelActionRequest 必须在 gRPC 接收线程直接记录 cancelled action_id。
不能把 cancel 记录丢进 SMAPI dispatcher。
SMAPI 主线程执行 ActionRequest 前检查 cancel gate。
```

原因：

```text
如果主线程卡住，ActionRequest 还没执行。
Runtime 超时后发 CancelActionRequest。
只有后台线程能先记录 cancel。
主线程恢复后，执行前 gate 才能跳过陈旧 action。
```

自测问题：

1. `observe_timeout` 和 `action_timeout` 的语义有什么区别？
2. 为什么 action timeout 后还要发 CancelActionRequest？
3. CancelActionRequest 为什么不是回滚机制？
4. 为什么 Adapter 记录 cancel 不能走 dispatcher？
5. 如果 action 已经执行，cancel 还能做什么？
6. 为什么迟到的 ActionResult 当前会被 Runtime 丢弃？

你应该能回答：

> Timeout 解决 Runtime 不再无限等待；CancelActionRequest 解决已经提交给 Adapter 的副作用动作不要晚到乱执行。它是 best-effort cancel gate，不是事务系统。

------

# 7. Prompt Config：为什么从代码常量变成配置

Phase1 的 prompt 是硬编码的。

Phase2 改成：

```text
runtime/config/agent.json
```

当前配置：

```json
{
  "turn_timeout_ms": 15000,
  "llm_timeout_ms": 8000,
  "observe_timeout_ms": 3000,
  "action_timeout_ms": 3000,
  "prompt": {
    "language": "Simplified Chinese",
    "npc_style": "自然、简短、符合 Stardew Valley NPC 的语气",
    "max_speak_chars": 60,
    "tool_instruction": "Use exactly one available tool. Prefer speak for dialogue; use emote only for clear emotional reactions."
  }
}
```

这里有一个重要边界：

```text
tool_instruction
    是写给模型看的自然语言提示。

tool_policy
    如果未来需要 provider 层强制 tool_choice，应另行设计机器可读字段。
```

不要把自然语言 prompt hint 和 provider API policy 混成一个字段。

自测问题：

1. 为什么 prompt 不应该继续硬编码在 `BuildSystemPrompt`？
2. 为什么 `language` 用 `Simplified Chinese` 比 `zh-CN` 更直观？
3. `tool_instruction` 和未来的 `tool_policy` 有什么区别？
4. 为什么 `agent.json` 可以带 Stardew 风格，而 `DefaultConfig` 应该更通用？
5. timeout 和 prompt 放在同一个 `agent.json` 是否合理？

你应该能回答：

> `agent.json` 是 Agent Runtime 的运行策略配置，控制一轮 turn 的超时边界和模型行为提示；`model.json` 则是 Provider 接入配置，控制使用哪个模型服务和 API key 来源。

------

# 8. 验收记录

代码级验收命令：

```powershell
go test -count=1 ./runtime/...
dotnet run --project adapters\stardew\tests\ActionCancellationRegistry.Tests\ActionCancellationRegistry.Tests.csproj
dotnet build adapters\stardew\GameAgent.Stardew.csproj
```

最近一次确认结果：

```text
go test -count=1 ./runtime/... 通过
ActionCancellationRegistry tests passed
dotnet build GameAgent.Stardew 通过，0 warning / 0 error
```

真机 smoke test 已验收：

```text
DeepSeek 中文 speak
fake provider emote
低 action_timeout_ms 触发 CancelActionRequest
```

CancelActionRequest 的验收边界：

```text
已确认：
    Runtime action timeout 后会发送 CancelActionRequest。
    ActionCancellationRegistry 单元测试覆盖 MarkCancelled / TryConsumeCancelled。
    Adapter gate 命中后会返回 ActionResult(status=CANCELLED, action_id=原 action_id)。

如需归档完整取消链路证据：
    需要保留主线程延迟场景的 SMAPI 日志，证明 ActionRequest 已入队但未执行，
    Runtime 超时后后台线程记录 cancelled action_id，
    主线程恢复后 TryConsumeCancelled 命中且游戏动作未执行。
```

Trace 文件检查：

```text
runtime/.local/traces.jsonl
```

应至少能看到：

```text
成功 turn:
    turn_started -> ... -> action_result_received -> turn_completed

action timeout turn:
    turn_started -> ... -> action_submit_started -> turn_failed(stage=action, reason=action_timeout)
```

如果最新一条 trace 是 `provider_failed`，不代表 Phase2 失败，只代表最后一次手动运行的模型调用失败。做归档截图时，建议最后再跑一条成功 speak，让文件末尾停在 `turn_completed`。

------

# 9. 当前已知限制

Phase2 明确不解决：

```text
多轮 ReAct
长期 Memory
复杂 Planner
move_to 这类长动作状态机
断线自动重连
多 adapter 连接隔离
trace viewer
trace 文件轮转
严格区分 action_timeout 与 turn_timeout 落在 action 阶段
CancelActionRequest 的事务回滚
CancelActionRequest send 的有界异步化
第三方 Adapter schema 大小上限与权限策略
```

这不是缺陷，而是边界。

当前阶段要保证的是：

```text
一轮有效 GameEvent 可以稳定完成或失败收敛。
失败能进入 trace。
新增简单 tool 不破坏 Runtime 主链路。
```

------

# 10. Phase2 后你应该能回答的问题

请尝试不看代码回答：

1. Phase2 为什么先做 trace / config / timeout，而不是 Memory？
2. `turn_id`、`trace_id`、`message_id`、`action_id` 分别解决什么问题？
3. 为什么 trace 是 best-effort observer，而不是功能性 hook？
4. 为什么 JSONL writer 要异步写？
5. 为什么 queue 满时宁可 drop trace，也不能阻塞 AgentLoop？
6. 为什么 Runtime 信任 Adapter schema？
7. Runtime 对 ToolCall 还保留哪些最小校验？
8. `emote` 为什么适合作为 Phase2 的第二个 tool？
9. 为什么 `move_to` 不适合这个阶段？
10. 为什么 action timeout 后还要发送 CancelActionRequest？
11. 为什么 Adapter cancel gate 必须用并发安全结构？
12. 为什么业务失败不一定要伪造 Go error？
13. `tool_instruction` 和 `tool_policy` 的边界是什么？
14. Phase2 完成后，Phase3 为什么更适合进入 Memory / Multi-turn？

如果这些问题能讲清楚，说明你已经从“能把 demo 跑起来”进入了“能解释 Runtime 为什么这样设计”的阶段。

------

# 11. 简历准备：30 秒版本

> Phase2 我把 GameAgent 从一个能跑通的 one-turn demo，升级成了一个可观测、可配置、失败可收敛、支持动态工具的最小 Agent Turn Runtime。Runtime 现在会为每个有效 GameEvent 创建 turn_id / trace_id，并把 Observation、模型调用、ToolCall、ActionResult 和终态写入非阻塞 JSONL trace；Capability schema 的权威交给 Adapter，Runtime 动态注册 speak 和 emote，不再写死工具；Observe、LLM 和 Action 都有 timeout，Action timeout 后会向 Adapter 发送 best-effort CancelActionRequest；Prompt 语言、风格和 tool instruction 通过 agent.json 配置。这个阶段的重点不是提升模型智能，而是把 Agent Harness 的运行边界打牢。

------

# 12. 最终记忆模型

Phase2 只需要记住这一张图：

```text
               Agent Turn Runtime

GameEvent
   ↓
TurnTracer(turn_id / trace_id)
   ↓
Observe with timeout
   ↓
Config-driven Prompt + Dynamic Tools
   ↓
LLM with timeout
   ↓
ToolCall envelope validation
   ↓
ActionRequest with timeout
   ↓
CancelActionRequest(best effort, if timeout)
   ↓
turn_completed / turn_failed
   ↓
JSONL timeline
```

一句话：

> Phase2 的核心，是让每一轮 Agent Turn 都有边界、有记录、有配置、有失败出口，并证明 Runtime 可以接入多个 Adapter-defined tool。
