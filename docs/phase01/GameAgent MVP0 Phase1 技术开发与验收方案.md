# GameAgent MVP0 Phase1 技术开发与验收方案

> Status: Phase1 Accepted
> Date: 2026-08-15
> Scope: Runtime + Stardew Adapter + DeepSeek one-turn vertical slice

------

# 1. 阶段目标

Phase1 的目标不是做完整 Agent Harness，而是用最小可运行链路证明：

```text
游戏事件可以进入 Runtime
Runtime 可以观察游戏环境
LLM 可以基于结构化上下文生成 ToolCall
Runtime 可以把 ToolCall 转成游戏动作
Adapter 可以在真实游戏中执行动作
```

最终验收链路：

```text
玩家点击 Linus
    ↓
Stardew Adapter 捕获交互
    ↓
Adapter 发送 GameEvent
    ↓
Runtime 返回 EventAck
    ↓
Runtime 请求 Observation
    ↓
Adapter 返回 Linus 当前环境状态
    ↓
Runtime 构造 model.Request
    ↓
DeepSeek Provider 返回 speak ToolCall
    ↓
Runtime 校验 ToolCall 并生成 ActionRequest
    ↓
Adapter 执行 speak
    ↓
游戏内显示模型生成的 NPC 台词
    ↓
Adapter 返回 ActionResult
```

Phase1 已经从早期 FakeProvider 验证升级为真实链路：

```text
真实 Stardew Adapter
真实 DeepSeek LLM Provider
真实游戏内 speak Action
```

------

# 2. 当前系统边界

Phase1 明确采用 One-Turn Agent Loop：

```text
Observe once
Think once
Act once
Done
```

也就是：

```text
One GameEvent
→ One Observation
→ One LLM ToolCall
→ One ActionRequest
→ Done
```

当前不做：

```text
长期 Memory
多轮 ReAct
复杂 Planner
Goal Scheduler
Sub-agent
Multi-Agent
Web Debug UI
断线重连
复杂权限系统
复杂工具生态
```

这个阶段只验证 harness 的核心骨架是否成立。

------

# 3. 核心设计原则

Phase1 保持三条边界：

```text
Runtime owns cognition.
Protocol owns contracts.
Adapter owns translation.
```

含义：

```text
Runtime 负责 Agent Loop、Provider、ToolCall 校验和动作决策。

Protocol 负责跨进程消息结构、ID 语义和 RPC contract。

Adapter 负责把 Stardew / SMAPI 世界翻译成 GameAgent Protocol。
```

Runtime 不直接引用 Stardew / SMAPI / Game1 / Farmer / NPC 类型。

Adapter 不直接理解 Agent Loop、Provider、Prompt、Tool Registry 的内部实现。

------

# 4. 模块分层

当前主要模块：

```text
protocol/proto
    定义 GameAgent Protocol 和 gRPC Gateway。

protocol/gen/go
    Go 侧生成代码。

runtime/cmd/server
    Runtime 启动入口，加载 Provider，启动 gRPC server。

runtime/internal/gateway
    实现 GameAgentGateway.Connect，维护双向流和 streamEnvironment。

runtime/internal/agent
    实现 One-Turn Agent Loop，构造 model.Request。

runtime/internal/model
    定义 Provider 抽象、Request、Message、ToolDefinition、ToolCall。

runtime/internal/llm/fake
runtime/internal/llm/openai
runtime/internal/llm/deepseek
    实现不同模型 Provider。

runtime/internal/llm/factory.go
    读取配置文件，创建具体 Provider。

runtime/internal/tool
    管理 Tool Registry，并把 ToolCall 转成 ActionRequest。

adapters/stardew
    SMAPI Mod，实现真实 Stardew Adapter。
```

------

# 5. Protocol 链路

Phase1 使用 gRPC bidirectional streaming：

```text
AdapterMessage  Adapter → Runtime
RuntimeMessage  Runtime → Adapter
```

连接启动：

```text
Adapter.Connect
    ↓
AdapterMessage.Hello
    ↓
RuntimeMessage.EnvironmentReady
    ↓
RuntimeMessage.CapabilityRequest
    ↓
AdapterMessage.Capabilities
    ↓
Runtime 注册 speak capability
```

事件处理：

```text
AdapterMessage.Event
    ↓
RuntimeMessage.EventAck
    ↓
AgentLoop.HandleEvent
    ↓
RuntimeMessage.ObserveRequest
    ↓
AdapterMessage.Observation
    ↓
Provider.Generate
    ↓
RuntimeMessage.ActionRequest
    ↓
AdapterMessage.ActionResult
```

------

# 6. ID 语义

Phase1 已经明确三类 ID：

```text
message_id
    每一条协议消息自己的 ID。

correlation_id
    回复某条请求时，指向原请求的 message_id。

action_id
    一次游戏动作调用的业务 ID。
```

关键规则：

```text
Observation.correlation_id 必须等于 ObserveRequest.message_id。

ActionResult.action_id 必须等于 ActionRequest.action.action_id。

ActionResult 不靠 correlation_id 唤醒 Runtime，而是靠 action_id。
```

如果未来要标识一次完整 Agent Turn，应新增 `turn_id` 或 `trace_id`，不要复用 `message_id` 或 `action_id`。

------

# 7. Agent Loop

当前 `AgentLoop.HandleEvent` 做的事情：

```text
1. 接收 GameEvent。
2. 从 event.entities 中选择目标 NPC entity。
3. 调用 Environment.Observe(ctx, entityID)。
4. 读取 Tool Registry 中可用 tools。
5. 构造 model.Request。
6. 调用 Provider.Generate。
7. 校验 ToolCall 是否是已注册 tool。
8. 把 ToolCall 转换成 ActionRequest。
9. 调用 Environment.SubmitAction。
10. 结束本轮 turn。
```

当前模型输入不是单一 `Prompt string`，而是结构化请求：

```go
type Request struct {
    System   string
    Messages []Message
    Tools    []ToolDefinition
}
```

Provider 负责把这个统一请求翻译成具体厂商 API 格式。

------

# 8. Provider 设计

Runtime 内部只依赖：

```go
type Provider interface {
    Generate(ctx context.Context, req Request) (Response, error)
}
```

当前实现：

```text
llm/fake
    用于单测和稳定 fake 闭环。

llm/openai
    将 model.Request 翻译成 OpenAI Responses API。

llm/deepseek
    将 model.Request 翻译成 DeepSeek Chat Completions API。
```

配置入口：

```json
{
  "provider": "deepseek",
  "model": "deepseek-v4-flash",
  "api_key": "env:DEEPSEEK_API_KEY",
  "base_url": "https://api.deepseek.com"
}
```

`api_key` 只允许写 env 引用，不允许把真实 key 写进配置文件。

DeepSeek 当前注意点：

```text
deepseek-v4-flash 不支持 tool_choice=required。

DeepSeek Provider 不强制传 tool_choice，而是通过 System Prompt 引导模型调用 tool。
```

OpenAI 当前注意点：

```text
OpenAI Responses API strict tool schema 需要 additionalProperties:false。

Provider 内部会对 object schema 做 normalize，避免污染 registry 中的原始 schema。
```

------

# 9. Tool Runtime

当前只有一个环境能力：

```text
speak
```

Adapter 上报：

```text
Capability(name = "speak")
```

Runtime 注册：

```text
ToolDefinition(name = "speak")
```

LLM 返回：

```text
ToolCall(name = "speak", arguments.text = "...模型生成台词...")
```

Runtime 转换：

```text
ActionRequest(capability = "speak", arguments.text = ...)
```

Adapter 执行：

```text
SpeakCapability
```

Phase1 中 `CapabilityList` 的 schema、description、execution_mode 暂时只作为协议信息保留，Runtime 当前只读取 capability name。`speak` 的模型 tool schema 仍由 Runtime 侧 registry 定义。

------

# 10. Stardew Adapter

Adapter 侧主要职责：

```text
连接 Runtime gRPC stream
发送 AdapterHello
响应 CapabilityRequest
捕获玩家与 NPC 交互
发送 GameEvent
响应 ObserveRequest
执行 ActionRequest
返回 ActionResult
```

关键模块：

```text
RuntimeClient
    维护 gRPC 双向流，打印 send / recv 链路日志。

CapabilityCatalog
    上报 speak capability。

PlayerInteractProbe
    捕获玩家点击 Linus 事件。

ObservationBuilder
    从 Stardew 当前世界构造 Observation。

ProtocolMapper
    集中处理 NPC 与 entity_id 的双向映射。

SpeakCapability
    在游戏中显示 Runtime 返回文本。

MainThreadDispatcher
    把游戏状态读取和游戏动作执行切回 SMAPI 主线程。
```

实体 ID 规则：

```text
Linus NPC -> entity_id = "npc:Linus"
entity_type = "npc"
```

Runtime 当前会在事件实体里选择 `entity_type == "npc"` 的目标。

------

# 11. 异步与阻塞边界

Runtime 的 `gateway.Connect` 需要同时做两件事：

```text
持续接收 Adapter 消息
异步触发 AgentLoop
```

所以事件处理不能直接阻塞 `stream.Recv()`。

当前设计：

```text
recvLoop
    持续读取 AdapterMessage。

eventCh
    缓冲 GameEvent。

agent goroutine
    串行消费 eventCh，调用 AgentLoop.HandleEvent。

streamEnvironment
    把异步 Observation / ActionResult 包装成同步 Observe / SubmitAction。

sendMu + stream.Send
    保证同一条 stream 上发送串行化。
```

Phase1 暂时没有完整独立 sendLoop。当前主动发送量有限，`sendMu + stream.Send` 可以接受。后续如果 Runtime 主动消息更多，再演进成 send queue / sendLoop。

------

# 12. 错误处理现状

已处理：

```text
GameEvent / Observation / ActionResult nil 检查
收到 GameEvent 后发送 EventAck
AgentLoop.HandleEvent 错误打印日志
stream 断开时唤醒 pending Observe / SubmitAction
SubmitAction 检查 nil request 和 empty action_id
Observe 检查 empty entityID
ActionResult 失败路径仍回传 action_id，Runtime 可以解除等待
```

已知限制：

```text
Observe 失败目前没有独立失败响应通道。

Adapter 如果无法构造 Observation，MVP0 推荐返回一个最小 Observation 解除 Runtime 等待，并在日志中记录错误。

Runtime 暂时不根据 ActionResult.status/error 做重试或反馈。

LLM Provider 暂无独立 HTTP 超时策略，当前依赖调用方 ctx；如果模型长时间不返回，会挂住当前 turn。后续应在 Provider 或 AgentLoop 层补 `context.WithTimeout`。

MVP0 不做断线重连。
```

------

# 13. 手动验收

Runtime 启动：

```powershell
$env:DEEPSEEK_API_KEY="你的真实 key"
go run ./runtime/cmd/server
```

如果端口占用：

```powershell
netstat -ano | findstr :50051
```

Adapter 验收：

```text
1. 编译 Stardew Adapter。
2. 把 Mod 放到 Stardew Valley/Mods/GameAgentStardew。
3. 启动 StardewModdingAPI.exe。
4. 加载 Linus 可见的存档。
5. 点击 Linus。
6. 查看 SMAPI 控制台日志。
7. 查看游戏内 Linus 是否显示 LLM 生成台词。
8. 查看 Runtime 终端是否输出 Provider 和 action 相关日志。
```

SMAPI 日志应能看到：

```text
[GameAgent][send] GameEvent
[GameAgent][recv] EventAck
[GameAgent][recv] ObserveRequest
[GameAgent][send] Observation
[GameAgent][recv] ActionRequest capability=speak text="..."
[GameAgent][send] ActionResult status=Succeeded
```

------

# 14. 自动测试

Go 侧测试重点：

```text
AgentLoop 单测
Tool Registry / EnvironmentTool 单测
streamEnvironment 单测
Gateway fake adapter integration test
LLM factory 单测
DeepSeek request 构造单测
OpenAI strict schema 单测
```

推荐命令：

```powershell
go test ./runtime/...
go test ./protocol/gen/go/...
```

Adapter 侧当前以手工 smoke test 为主，后续可补：

```text
ProtocolMapper 单测
ObservationBuilder 可测试封装
RuntimeClient 消息处理单测
SMAPI 手动验收 checklist
```

------

# 15. 参考项目与设计取舍

Phase1 参考过几类项目，但没有照搬：

```text
Pi / p-agent 类 harness
    借鉴小核心、Provider abstraction、tool execution、state management 的方向。

Hermes 类 provider 设计
    借鉴中立 model.Request + 厂商 Provider adapter 的分层方式。

SMAPI / Stardew Mod 生态
    借鉴 Mod 生命周期、主线程约束、游戏状态读取与动作执行方式。
```

没有照搬的点：

```text
没有使用 stdio JSON RPC。
    因为游戏 Adapter 与 Runtime 是两个长期运行进程，gRPC 双向流更适合强 schema、跨语言、长连接通信。

没有把参考项目的完整 harness 搬进来。
    因为 Phase1 目标是游戏场景下的最小 vertical slice，不是做通用 Agent 平台。

没有让 Adapter 直接调用 LLM。
    因为 cognition 边界应该留在 Runtime，Adapter 只做游戏翻译层。
```

------

# 16. Phase1 验收结论

Phase1 可以验收。

验收证据：

```text
Runtime 可以加载 DeepSeek Provider 并启动 gRPC server。

Stardew Adapter 可以连接 Runtime 并完成 capability bootstrap。

点击 Linus 后，Adapter 可以发送 GameEvent。

Runtime 可以发送 EventAck 和 ObserveRequest。

Adapter 可以返回 Observation。

DeepSeek 可以基于 Observation 返回 speak ToolCall。

Runtime 可以发送 ActionRequest。

Adapter 可以在游戏中执行 speak。

Adapter 可以返回 ActionResult(Succeeded)。
```

当前阶段成果可以概括为：

> GameAgent 已经跑通“真实游戏事件 -> Runtime Agent Loop -> 真实 LLM ToolCall -> 真实游戏动作”的最小闭环。

------

# 17. 下一阶段建议

下一阶段不要马上做复杂 Agent，而是优先补强 Phase1 的工程质量：

```text
1. Prompt 可配置化，支持中文/英文 NPC 输出风格。
2. Adapter 侧补 ProtocolMapper / RuntimeClient 单测。
3. Runtime 侧补 inbound Error 处理或 Observe failure response。
4. 增加 turn_id / trace_id，改善链路观测。
5. 让 CapabilityList schema 真正参与 ToolDefinition 构造。
6. 再考虑多 tool、多事件、多 NPC。
```

只有当单轮链路足够稳定后，再进入 Memory、Planner、multi-turn Agent。
