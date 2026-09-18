# GameAgent MVP0 Phase1 工程设计规范

> Status: Phase1 Engineering Rules
> Date: 2026-08-15
> Scope: Runtime / Protocol / Stardew Adapter / LLM Provider

------

# 1. 文档定位

这份文档不重复完整技术链路。

它只回答一个问题：

> Phase1 之后继续开发 GameAgent 时，哪些工程规则不能乱？

技术方案负责说明“系统怎么跑通”。

本规范负责约束“后续怎么写才不会把 harness 写散”。

------

# 2. 总体边界

必须保持：

```text
Runtime owns cognition.
Protocol owns contracts.
Adapter owns translation.
```

禁止：

```text
Runtime import Stardew / SMAPI 类型。

Adapter 直接调用 LLM。

AgentLoop 直接理解 OpenAI / DeepSeek / Claude 的 API 结构。

LLM Provider 直接构造游戏 ActionRequest。

Protocol 字段语义随意复用。
```

允许：

```text
Runtime 通过 protocol generated code 理解 GameEvent / Observation / ActionRequest。

Adapter 通过 protocol generated code 与 Runtime 通信。

Provider 把 model.Request 翻译成厂商 API。

Tool Runtime 把 ToolCall 翻译成 ActionRequest。
```

------

# 3. Go Runtime 包分层规范

推荐边界：

```text
runtime/cmd/server
    只负责装配、配置、监听、优雅退出。

runtime/internal/agent
    只负责 Agent Loop 和 prompt/model request 构造。

runtime/internal/model
    只定义 Runtime 内部模型协议，不 import 任何具体 provider。

runtime/internal/llm
    负责 provider factory 和具体 provider 实现。

runtime/internal/tool
    负责 Tool Registry、ToolCall 校验、ActionRequest 构造。

runtime/internal/gateway
    负责 gRPC Connect、stream lifecycle、message correlation。
```

`model` 包保持纯协议层：

```text
Provider interface
Request
Message
ToolDefinition
ToolCall
Response
```

`llm` 包负责实现：

```text
fake.Provider
openai.Provider
deepseek.Provider
factory
```

新增模型厂商时：

```text
1. 新增 runtime/internal/llm/<provider>/provider.go。
2. 实现 model.Provider。
3. 在 llm/factory.go 中增加配置分支。
4. 不修改 AgentLoop。
5. 不修改 model.Provider 接口，除非多个 provider 都需要新的共同能力。
```

------

# 4. Adapter 分层规范

Stardew Adapter 推荐边界：

```text
RuntimeClient
    只处理 Runtime stream、send/recv、连接生命周期、日志。

CapabilityCatalog
    只声明 Adapter 支持哪些 capability。

ProtocolMapper
    只做 Stardew 对象与 GameAgent Protocol 的双向映射。

ObservationBuilder
    只采集游戏状态并构造 Observation。

SpeakCapability
    只执行 speak 动作。

PlayerInteractProbe
    只捕获玩家交互事件。

MainThreadDispatcher
    只解决 SMAPI 主线程执行约束。
```

禁止在多个文件里重复写 NPC 与 entity_id 映射。

必须集中在 `ProtocolMapper` 中维护：

```text
NPC -> entity_id
entity_id -> NPC
entity_type
display_name
```

------

# 5. Protocol ID 规范

`message_id`：

```text
每条协议消息自己的唯一 ID。
用于日志、排查、correlation。
```

`correlation_id`：

```text
回复某条请求时，填原请求的 message_id。
```

`action_id`：

```text
一次游戏动作调用的业务 ID。
由 Runtime 创建。
ActionResult 必须回显 ActionRequest.action.action_id。
```

硬性规则：

```text
CapabilityList.correlation_id = CapabilityRequest.message_id
Observation.correlation_id = ObserveRequest.message_id
ActionResult.action_id = ActionRequest.action.action_id
```

不要用 `message_id` 表示一次 Agent Turn。

未来如果需要完整链路追踪，新增：

```text
turn_id
trace_id
run_id
```

------

# 6. gRPC Stream 规范

Runtime 是 gRPC server。

Adapter 是 gRPC client。

Adapter 主动建立双向流：

```text
GameAgentGateway.Connect
```

接收循环必须遵守：

```text
不能因为未知 RuntimeMessage 直接崩溃。

不能在 recv loop 中同步执行耗时 AgentLoop。

不能在接收线程里同步等待 SMAPI 主线程完成复杂操作。

stream 断开时必须唤醒 pending Observe / SubmitAction。
```

当前 Runtime 允许：

```text
sendMu + stream.Send
```

未来主动发送复杂后，再引入：

```text
send queue
sendLoop
backpressure
heartbeat
reconnect
```

------

# 7. Agent Loop 规范

Agent Loop 当前只做 one-turn：

```text
GameEvent
→ Observe
→ BuildModelRequest
→ Provider.Generate
→ Validate ToolCall
→ Build ActionRequest
→ SubmitAction
→ Done
```

禁止：

```text
AgentLoop 直接调用 gRPC stream。

AgentLoop 直接拼厂商 API JSON。

AgentLoop 直接构造 Stardew 对象。

AgentLoop 绕过 ToolRegistry 直接提交 action。
```

允许：

```text
AgentLoop 依赖 Environment interface。

AgentLoop 依赖 model.Provider interface。

AgentLoop 依赖 ToolRegistry 读取可用 tools。

AgentLoop 调用 Environment.SubmitAction。
```

新增多轮能力前，必须先明确：

```text
turn lifecycle
retry semantics
interrupt semantics
memory boundary
trace_id
```

------

# 8. Tool / Capability 规范

四个概念必须分清：

```text
Capability
    Adapter 声明的游戏环境能力。

ToolDefinition
    Runtime 暴露给 LLM 的工具定义。

ToolCall
    LLM 选择调用的工具。

ActionRequest
    Runtime 发给 Adapter 执行的游戏动作。
```

转换方向：

```text
Capability
    ↓
ToolDefinition
    ↓
ToolCall
    ↓
ActionRequest
```

禁止：

```text
LLM 直接生成 ActionRequest。

Adapter 直接决定 ToolCall。

Capability schema 未经 Runtime 校验就直接暴露给模型。
```

Phase1 当前只支持：

```text
speak
```

后续新增 tool 时，至少要补：

```text
ToolDefinition schema
ToolCall validation
ActionRequest mapping
Adapter capability implementation
ActionResult failure path
```

------

# 9. Provider 规范

Provider 必须实现：

```go
Generate(ctx context.Context, req model.Request) (model.Response, error)
```

Provider 负责：

```text
把 model.Request 翻译成厂商 API。
把厂商 tool call 翻译成 model.ToolCall。
处理厂商特有错误。
处理厂商特有 schema 要求。
```

Provider 不负责：

```text
选择游戏实体。
提交 ActionRequest。
执行游戏动作。
修改 Tool Registry。
保存长期 Memory。
```

配置规范：

```json
{
  "provider": "deepseek",
  "model": "deepseek-v4-flash",
  "api_key": "env:DEEPSEEK_API_KEY",
  "base_url": "https://api.deepseek.com"
}
```

API key 规则：

```text
可提交配置文件只能写 env:XXX。

真实 key 只能放环境变量、secret manager 或本地未入库覆盖文件。

禁止提交真实 key。

本地私密覆盖文件统一使用 *.local.json，并加入 .gitignore。
```

------

# 10. Prompt 规范

Prompt 构造入口放在：

```text
runtime/internal/agent/prompt.go
```

当前结构：

```text
System
Messages
Tools
```

不要退回到一个大字符串里硬拼全部内容。

System Prompt 负责：

```text
角色边界
输出语言
工具使用规则
安全约束
```

Turn Message 负责：

```text
当前 GameEvent
当前 Observation
本轮任务
```

Tools 负责：

```text
结构化工具定义
参数 schema
```

中文/英文输出风格建议后续配置化，不要散落在 Provider 实现里。

------

# 11. 错误处理规范

Go 侧：

```text
不要 `_ = err` 静默吞错。

基础设施层错误至少 log。

pending wait 必须能在 stream 断开时解除。

Observe / SubmitAction 必须检查空参数。

SubmitAction 必须检查 action_id。
```

Adapter 侧：

```text
接收未知 RuntimeMessage 时 debug log 后忽略。

Action 执行失败时返回 ActionResult(status=FAILED, error=...)。

Observe 失败时不要让 Runtime 永久等待。

fire-and-forget Task 需要记录 faulted error。
```

MVP0 可以不做重试，但必须让失败可观察。

------

# 12. 日志规范

SMAPI 控制台应能看到 Adapter 与 Runtime 的核心收发链路：

```text
[GameAgent][send] GameEvent
[GameAgent][recv] EventAck
[GameAgent][recv] ObserveRequest
[GameAgent][send] Observation
[GameAgent][recv] ActionRequest
[GameAgent][send] ActionResult
```

日志不要求打印完整 payload，但必须能回答：

```text
发了什么消息？
收了什么消息？
对应哪个 event_id / message_id / action_id？
执行结果是什么？
```

不要在日志中打印 API key。

------

# 13. 注释规范

Go / C# 注释可以使用中文，保留必要英文术语。

推荐：

```go
// RunTurn 执行一次完整的 Agent Turn。
```

不要追求注释覆盖率。

写注释前先判断它回答什么：

```text
What
    代码通常已经回答，谨慎写。

Why
    值得写。

Constraint
    非常值得写。

Invariant
    非常值得写。

Lifecycle
    并发和 RPC 代码非常值得写。

Failure semantics
    基础设施代码非常值得写。

Protocol semantics
    Protocol / RPC / Agent Runtime 非常值得写。
```

简单结构体、简单 getter、显而易见的函数不需要注释。

------

# 14. 测试规范

Runtime 单测优先覆盖：

```text
ToolCall validation
ActionRequest mapping
AgentLoop one-turn
streamEnvironment pending resolve
Provider factory config
OpenAI / DeepSeek request mapping
```

Runtime 集成测试优先覆盖：

```text
fake adapter connects
hello
environment_ready
capability_request
capabilities
game_event
event_ack
observe_request
observation
action_request
action_result
```

Adapter 测试优先覆盖：

```text
ProtocolMapper entity_id 映射
CapabilityCatalog speak 上报
RuntimeMessage switch default 安全忽略
ActionResult 失败路径
```

手动验收必须保留：

```text
Apifox 手动链路
Go fake adapter integration test
真实 Stardew + SMAPI smoke test
```

------

# 15. 配置规范

Runtime 配置：

```text
runtime/config/model.json
GAMEAGENT_MODEL_CONFIG
DEEPSEEK_API_KEY
```

Adapter 配置：

```text
RuntimeAddress
TargetNpc
```

配置规则：

```text
默认地址可以是 127.0.0.1:50051。

真实 API key 不进入 git。

示例配置可以提交，但只能使用 env:XXX 引用。

本地私密覆盖配置统一命名为 *.local.json。

私密覆盖文件通过 GAMEAGENT_MODEL_CONFIG 指向，例如 runtime/config/model.local.json。
```

------

# 16. 后续演进准入规则

新增功能前先问：

```text
这个能力属于 Runtime、Protocol 还是 Adapter？

是否破坏 one-turn 的清晰边界？

是否需要新的 ID 语义？

是否需要 trace / retry / failure semantics？

是否可以先用 fake provider / fake adapter 测通？
```

优先级建议：

```text
先增强可观测性
再增强测试
再增加 tool
再增加多轮
最后再考虑 Memory / Planner / Multi-Agent
```

不要因为想做 Agent 而过早堆复杂抽象。

------

# 17. 一句话原则

> GameAgent 的核心不是“让 LLM 说话”，而是把游戏世界、模型工具调用和动作执行放进一个边界清晰、可测试、可演进的 Agent Harness。
