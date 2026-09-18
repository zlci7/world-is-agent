# GameAgent MVP0 Harness 学习回顾与自测手册

> 用途：开发复盘 / Agent Harness 学习 / 简历项目准备 / 技术面试自测
> Scope：GameAgent MVP0 One-Turn Loop
> 核心目标：不仅知道“代码怎么写”，还能够解释“为什么这么设计”。

------

# 1. 我们到底在构建什么

GameAgent 第一阶段不是在做：

```text
LLM 对话 Mod
```

也不是在做：

```text
完整 Hermes / Pi 复制品
```

而是在从零实现一个最小的：

> **Game-native Agent Harness**

当前完整链路：

```text
Environment Bootstrap
        ↓
Adapter 与 Runtime 建立连接
        ↓
发现 Environment Capability
        ↓
Capability → Tool
        ↓
等待 GameEvent
        ↓
玩家点击 NPC
        ↓
AgentLoop
        ↓
Observe Environment
        ↓
BuildModelRequest
(System + Messages + Tools)
        ↓
LLM Provider Factory
        ↓
DeepSeek Provider
        ↓
ToolCall
        ↓
ActionRequest
        ↓
Adapter
        ↓
Game Action
```

当前 MVP0 明确采用：

```text
Observe once
Think once
Act once
Done
```

即 One-Turn Agent Loop。

当前第一阶段已经从 FakeProvider 验证升级为：

```text
真实 Stardew Adapter
真实 DeepSeek LLM Provider
真实游戏内 speak Action
```

但 Agent Loop 仍然是：

```text
One GameEvent
→ One Observation
→ One LLM ToolCall
→ One ActionRequest
→ Done
```

这一阶段可以验收的成果是：

```text
SMAPI Adapter 能连接 Runtime
Runtime 能发现 speak capability
点击 NPC 能发送 GameEvent
Runtime 能请求 Observation
DeepSeek 能返回 speak ToolCall
Adapter 能执行 ActionRequest
游戏内能看到 NPC 说出模型生成的文本
```

------

# 2. 为什么要学习 Harness，而不仅仅是调用 LLM

LLM API 本身只解决：

```text
Input
 ↓
Model
 ↓
Output
```

真正的 Agent 系统需要解决：

```text
Environment
Context
Tools
Execution
State
Control Flow
Model
```

因此一个最简单的 Agent Harness 可以理解成：

```text
            Environment
                 │
                 ↓
              Observe
                 │
                 ↓
              Context
                 │
                 ↓
               Model
                 │
                 ↓
             Tool Call
                 │
                 ↓
             Tool Runtime
                 │
                 ↓
              Action
                 │
                 └────→ Environment
```

Pi 官方目前直接将自身定位为 minimal terminal coding harness，并强调保持 Core 较小，把更具体的行为交给 extensions、skills、templates 和 packages。

Hermes 也采用类似的核心思想：多个外部入口最终复用同一个 Agent Core，而 Gateway 负责长期运行的外部平台接入和路由。

GameAgent 借鉴的不是它们的具体功能，而是：

```text
保持 Agent Core 小

外部环境与 Agent Core 解耦

工具成为 Agent 与环境交互的边界

Agent 的运行流程由 Harness 管理
```

------

# 3. 当前技术选型总览

## 3.1 Go Runtime

选择：

```text
Go
```

负责：

```text
Gateway
AgentLoop
Tool Runtime
Model Protocol
LLM Provider Factory
```

主要原因：

```text
并发模型简单
适合网络服务
gRPC 支持成熟
接口抽象清晰
适合后续做 Session / Scheduler / Runtime
```

当前 Runtime 中与模型相关的分层已经拆成：

```text
runtime/internal/model
    只定义统一协议：Request / Message / ToolDefinition / ToolCall / Provider interface

runtime/internal/llm/fake
runtime/internal/llm/openai
runtime/internal/llm/deepseek
    各自实现具体 Provider

runtime/internal/llm/factory.go
    读取 runtime/config/model.json，创建对应 Provider
```

配置文件只保存：

```text
provider / model / base_url / api_key env 引用
```

真实 API key 不写入仓库，而是通过：

```json
"api_key": "env:DEEPSEEK_API_KEY"
```

在运行时读取环境变量。

项目里 Go 的定位不是：

```text
“为了调用 LLM”
```

而是：

> **承担真正的 Agent Runtime。**

------

# 3.2 C# Stardew Adapter

选择：

```text
C# + SMAPI
```

负责：

```text
GameEvent 捕获
游戏状态读取
Capability 实现
Action 执行
```

边界：

```text
C# Adapter
=
理解 Stardew

Go Runtime
=
不理解 Stardew
```

因此 Runtime 永远不应该出现：

```text
Game1
NPC
Farmer
SMAPI
```

这些具体游戏类型。

------

# 3.3 为什么使用 Protobuf + gRPC

当前使用：

```text
Protobuf
+
gRPC Bidirectional Streaming
```

不是简单 HTTP：

```text
POST /event
POST /observe
POST /action
```

原因是 Game Environment 是一个：

> **长期在线、双方都需要主动发送消息的运行环境。**

Runtime 需要主动发送：

```text
ObserveRequest
ActionRequest
CapabilityRequest
```

Adapter 也需要主动发送：

```text
GameEvent
Observation
ActionResult
```

因此关系不是：

```text
Client
→
Server
→
Response
```

而更像：

```text
        long-lived connection

Adapter  ←────────────────→ Runtime
```

这里可以把 gRPC bidirectional streaming 和 WebSocket 都理解成“长期双向通信”，但二者不是同一种抽象。

```text
gRPC bidirectional streaming
    基于 HTTP/2，支持 protobuf 强 schema、代码生成、多路复用、流控和 RPC 语义。

WebSocket
    更接近裸双向帧通道，消息 schema、错误模型、请求响应关联通常需要应用层自己定义。
```

Pi 的 headless RPC 同样把 Agent 与外围程序之间设计成 command / response / event 的异步通信模型，只是它采用 JSON 协议和 stdin/stdout，而不是 gRPC。

因此 GameAgent 借鉴的是：

> **异步命令与生命周期事件的设计思想。**

而不是复制 Pi 的 Transport。

------

# 4. 第一部分自测：Environment Bootstrap

## 4.1 Bootstrap 是什么

AgentLoop 运行之前，需要有一个已经准备好的 Environment。

流程：

```text
Runtime Start
    ↓
LLM Provider Factory
ToolRegistry
AgentLoop
Gateway
    ↓
gRPC Server Ready
    ↓
Adapter Connect
    ↓
AdapterHello
    ↓
EnvironmentReady
    ↓
CapabilityRequest
    ↓
CapabilityList
    ↓
Capability → Tool
    ↓
BOOTSTRAPPED
    ↓
wait GameEvent
```

Bootstrap 与 AgentRun 是两个生命周期。

### Bootstrap

回答：

> Agent 在什么环境里运行？

准备：

```text
Connection
Environment
Capabilities
Tools
Model
```

### AgentRun

回答：

> 这一次 Agent 要做什么？

处理：

```text
Event
Observation
Context
Model
Tool
Action
```

------

## 4.2 为什么 Tool 要提前注册

Adapter 提供：

```text
Capability
```

比如：

```text
speak
```

Runtime 将它转换成：

```text
Tool
```

然后存入：

```text
ToolRegistry
```

之后 AgentRun 才能知道：

```text
当前 Environment 能执行哪些动作？
```

当前 MVP0 使用：

```text
Environment-level capability discovery
```

后续才考虑：

```text
Entity-level capability
lazy discovery
revision refresh
```

这是为了保持第一阶段简单。

------

## 4.3 自测问题

### 基础

1. Runtime 启动和 AgentRun 开始是一回事吗？
2. 为什么必须先建立 Environment，再运行 AgentLoop？
3. `AdapterHello` 和 `EnvironmentReady` 分别解决什么问题？
4. Capability 为什么来自 Adapter，而不是 Runtime？
5. Capability 和 Tool 是同一个东西吗？
6. 为什么 MVP0 选择 Bootstrap 时一次性发现 `speak`？

### 进阶

1. 如果不同 NPC 拥有不同 Capability，当前设计要如何演进？
2. 如果 Capability 在游戏运行过程中改变怎么办？
3. 为什么不应该每次调用 LLM 时重新建立 gRPC？
4. Environment disconnect 后原 ToolRegistry 中的 Tool 应该如何处理？

------

## 4.4 你应该能口述

> Runtime 启动后首先完成 Harness 基础设施初始化。Adapter 连接后通过 Hello 建立 Environment，并通过 Capability Discovery 告诉 Runtime 当前环境能够执行哪些行为。Runtime 将这些 Capability 转换为 Tool 并注册。只有 Bootstrap 完成之后，GameEvent 才能够触发 AgentRun。

如果这段无法脱稿讲清楚，说明 Bootstrap 还没有真正理解。

------

# 5. 第二部分自测：Environment Abstraction

## 5.1 为什么 AgentLoop 不应该依赖 gRPC

AgentLoop 真正需要的只有：

```go
type Environment interface {
    Observe(...)
    SubmitAction(...)
}
```

AgentLoop 不应该知道：

```text
gRPC
stream
AdapterMessage
RuntimeMessage
correlation_id
SMAPI
```

结构：

```text
AgentLoop
    ↓
Environment Interface
    ↓
Gateway Environment
    ↓
gRPC
    ↓
Adapter
```

这就是一个典型的：

> **依赖倒置 / Port-Adapter 思路。**

Agent 核心依赖一个抽象能力：

```text
Observe
Act
```

而不是依赖：

```text
Stardew gRPC implementation
```

------

# 5.2 这样做有什么收益

今天：

```text
AgentLoop
↓
Stardew Environment
```

以后可以：

```text
AgentLoop
├── Stardew Environment
├── Minecraft Environment
├── Fake Environment
└── Test Environment
```

AgentLoop 完全不用改。

这也是 GameAgent 从：

```text
Stardew AI Mod
```

升级成：

```text
Game Agent Runtime
```

的关键边界。

Hermes 的 Gateway 同样承担外部平台与 Agent Core 之间的接入职责，不同外部平台通过 Adapter/Gateway 进入共享核心。

GameAgent 将这个思想映射为：

```text
Messaging Platform
      ↓
Game Environment
```

------

## 5.3 自测问题

1. 为什么 AgentLoop 不能直接调用 `stream.Send()`？
2. 为什么 AgentLoop 不能 import Stardew 类型？
3. `Environment` interface 的最小职责是什么？
4. Gateway 和 Environment 是一个概念吗？
5. Fake Environment 对单元测试有什么价值？
6. 如果未来替换 gRPC 为 WebSocket，AgentLoop 是否应该修改？
7. 如果答案是“需要大改 AgentLoop”，当前架构哪里出了问题？

------

## 5.4 面试白板题

尝试画出：

```text
Game
 ↓
Adapter
 ↓
Protocol
 ↓
Gateway
 ↓
Environment
 ↓
AgentLoop
```

然后解释：

> 每一层知道什么、不知道什么。

这是非常好的架构面试练习。

------

# 6. 第三部分自测：Agent Loop

## 6.1 MVP0 AgentLoop

当前：

```text
GameEvent
    ↓
Resolve Entity
    ↓
Observe
    ↓
Get Tools
    ↓
BuildModelRequest
    ↓
Model
    ↓
ToolCall
    ↓
Execute Tool
    ↓
Done
```

代码思想：

```go
func HandleEvent(event) {
    entity := resolveEntity(event)

    observation := env.Observe(entity)

    tools := registry.Available()

    request := BuildModelRequest(
        event,
        observation,
        tools,
    )

    response := provider.Generate(request)

    tool := registry.Resolve(
        response.ToolCall.Name,
    )

    tool.Execute(
        env,
        entity,
        response.ToolCall.Arguments,
    )
}
```

当前 `BuildModelRequest` 不再只是拼一个 `Prompt string`，而是构造：

```text
System
    稳定角色、行为边界、输出规则

Messages
    当前 GameEvent + Observation

Tools
    ToolRegistry 当前可用工具
```

这借鉴了 Anthropic Messages / Pi provider design 的思想：Runtime 内部使用中立的结构化请求，各厂商 Provider 再翻译成自己的 API 格式。

------

# 6.2 为什么称为 One-Turn

因为当前只有：

```text
Observe
↓
Think
↓
Act
↓
Done
```

真正完整的 AgentLoop 以后可能是：

```text
while true:

    Model
      ↓
    ToolCall?

      YES
       ↓
    Execute
       ↓
    ToolResult
       ↓
    append Context
       ↓
    Model again

      NO
       ↓
    Finish
```

也就是：

```text
Reason
→ Act
→ Observe Result
→ Reason
```

MVP0 刻意不做这一层。

原因：

> 先证明 Harness 基础链路，再让循环复杂度自然出现。

Pi 的 agent core 本身明确包含 tool execution 和 state management，这也是成熟 Harness 中 Agent Loop 的典型职责。

------

# 6.3 Event 为什么触发 AgentLoop

游戏 Agent 与普通 Chatbot 最大区别之一：

Chatbot：

```text
User Message
↓
Agent
```

GameAgent：

```text
Environment Event
↓
Agent
```

未来可能包括：

```text
player_interacted
goal_due
action_finished
day_started
item_received
```

所以 Agent 是：

> **Event-driven**

而不是：

> 永远后台不停调用 LLM。

------

## 6.4 自测问题

1. AgentLoop 的输入是什么？
2. AgentLoop 为什么首先 Observe，而不是直接调用 LLM？
3. Event 和 Observation 有什么区别？
4. 为什么 Event 不能完全代替 Observation？
5. ContextBuilder 第一阶段应该包括哪些内容？
6. 什么是 One-Turn AgentLoop？
7. 什么情况下需要升级成 Multi-Turn / ReAct？
8. Tool Result 为什么未来需要重新进入 Context？
9. Agent 是主动轮询世界，还是由 Event 唤醒？为什么？

------

## 6.5 一分钟面试回答

你应该能说：

> 我第一阶段实现的是 event-driven one-turn AgentLoop。游戏事件只负责唤醒 Agent，Runtime 随后通过 Environment interface 获取当前 Observation，把 Event、Observation 和可用 Tools 构造成模型输入。模型输出统一 ToolCall，Tool Runtime 执行之后本次 Run 结束。后续再扩展成 ToolResult 回填模型的多轮循环。

------

# 7. 第四部分自测：Tool Runtime

这是整个项目最重要的知识点之一。

------

# 7.1 四个概念不能混

### Capability

```text
环境能够做什么
```

由 Adapter 提供。

例如：

```text
speak
```

------

### Tool

```text
Runtime 暴露给模型的可调用能力
```

例如：

```text
speak(text: string)
```

------

### ToolCall

```text
模型决定调用什么
```

例如：

```json
{
  "name": "speak",
  "arguments": {
    "text": "Good morning."
  }
}
```

------

### ActionRequest

```text
Runtime 真正要求游戏执行什么
```

例如：

```text
entity_id = npc:Linus
capability = speak
arguments.text = Good morning.
```

完整链：

```text
Adapter Capability
       ↓
Tool Factory / Registry
       ↓
LLM Tool Definition
       ↓
LLM ToolCall
       ↓
Tool Runtime
       ↓
ActionRequest
       ↓
Adapter
```

------

# 7.2 为什么不能让 LLM 直接发 ActionRequest

因为模型输出属于：

```text
Untrusted Decision
```

必须经过 Runtime。

Runtime 可以负责：

```text
Tool 是否存在
参数是否合法
Capability 是否存在
未来的 Permission
未来的 Policy
```

所以：

```text
LLM
↓
ToolCall
↓
Runtime validation
↓
ActionRequest
```

而不是：

```text
LLM
↓
Game
```

------

# 7.3 为什么 Provider 返回统一 ToolCall

当前 Provider abstraction：

```text
Fake
OpenAI Responses API
DeepSeek Chat Completions API
未来的 Claude / Local Model
    ↓
Provider
    ↓
GameAgent ToolCall
```

AgentLoop 不应该理解：

```text
OpenAI tool_calls
OpenAI Responses function_call
DeepSeek choices[].message.tool_calls
Claude tool_use
某个 Local Model JSON 格式
```

不同 Provider 自己完成转换。

这与 Pi 将多 Provider API 和 Agent runtime 分离的模块化方向类似；Pi 当前仓库明确区分统一 LLM API 与 agent core。

当前代码中：

```text
model.Provider
    是 Runtime 内部统一接口

llm/fake.Provider
    用于稳定单测和 fake 闭环

llm/openai.Provider
    将 model.Request 转成 OpenAI Responses API

llm/deepseek.Provider
    将 model.Request 转成 DeepSeek Chat Completions API

llm/factory
    根据 runtime/config/model.json 选择 provider
```

真实联调时，DeepSeek Provider 已经验证可以返回：

```text
tool name = speak
arguments.text = LLM 生成的 NPC 台词
```

一个工程细节是：`deepseek-v4-flash` 不支持 `tool_choice=required`，因此 DeepSeek Provider 不强制传这个字段，而是通过 System Prompt 引导模型调用 tool。OpenAI Provider 则走 Responses API，并在 strict tool schema 下补充 `additionalProperties:false`。

------

## 7.4 自测问题

1. Capability、Tool、ToolCall、ActionRequest 分别是谁产生的？
2. Capability 为什么不能直接叫 Tool？
3. 为什么 ToolRegistry 属于 Runtime？
4. 为什么 Adapter 不应该理解 LLM ToolCall？
5. 为什么 LLM 不能直接操作 Game API？
6. `input_schema_json` 有什么用途？
7. Tool validation 应该在哪一层完成？
8. 如果 Adapter 明天增加 `move_to`，理想情况下 Runtime 哪些代码不应该修改？
9. OpenAI 与 Claude Tool Calling 格式不同，差异应该由谁吸收？

------

# 8. 第五部分自测：Async RPC Correlation

这是 MVP0 最有“后端 Runtime 工程味”的部分。

------

# 8.1 为什么双向流不能简单写成

```text
Recv
Send
Recv
Send
Recv
Send
```

因为消息并不严格一问一答。

可能同时发生：

```text
GameEvent
Heartbeat
Observation
ActionResult
```

同时 Runtime 可能主动发：

```text
ObserveRequest
ActionRequest
```

因此需要两个独立方向：

```text
                 gRPC Stream

       Adapter                Runtime

          │                     │
          │       messages      │
          ├────────────────────→│ recvLoop
          │                     │
          │                     │
          │      messages       │
          │←────────────────────┤ send path
```

当前 MVP0 还没有实现完整独立的 `sendLoop`，而是通过：

```text
sendMu + stream.Send
```

保证同一条 gRPC stream 上的发送被串行化。后续如果 Runtime 侧主动消息变多，可以自然演进为真正的 sendLoop / send queue。

------

# 8.2 为什么 recvLoop 不能执行 AgentLoop

错误：

```text
recvLoop
  ↓
GameEvent
  ↓
AgentLoop
  ↓
Observe()
  ↓
等待 Observation
```

问题：

```text
Observation 已到 stream
```

但唯一能够：

```text
stream.Recv()
```

的 recvLoop 正在等待 AgentLoop。

因此形成：

```text
AgentLoop 等 Observation
Observation 等 recvLoop
recvLoop 等 AgentLoop
```

即死锁。

正确结构：

```text
recvLoop
    ↓
只负责 Recv + Dispatch

GameEvent
    ↓
go AgentRun

Observation
    ↓
resolve pending waiter
```

------

# 8.3 什么是 RPC Correlation

Runtime 发：

```text
ObserveRequest

message_id = msg_100
```

Adapter 回：

```text
Observation

correlation_id = msg_100
```

Runtime：

```text
pendingRequests[msg_100]
```

找到正在等待这个 Observation 的调用。

因此外层 AgentLoop 可以写得像同步代码：

```go
observation, err := env.Observe(...)
```

但底层其实是：

```text
create waiter
↓
send message
↓
AgentRun waits
        │
        │
recvLoop continues
        ↓
receive response
↓
correlation lookup
↓
wake waiter
```

这是非常经典的：

> **在异步消息通道之上构造 request-response abstraction。**

------

# 8.4 Action 为什么使用 action_id

Observation 是：

```text
request
↓
response
```

所以使用：

```text
message_id / correlation_id
```

Action 则拥有自己的生命周期：

```text
ActionRequest
↓
ACCEPTED
↓
RUNNING
↓
SUCCEEDED
```

因此业务身份应该是：

```text
action_id
```

而不是只依赖网络 message_id。

当前 MVP0 的 `speak` 可以非常快，但这个设计为后续：

```text
move_to
```

这样的长时间 Action 留出了正确边界。

Phase1 当前实际使用的是同步 `speak` 动作，Adapter 只返回 `SUCCEEDED` 或 `FAILED`，Runtime 也暂时不消费 `ACCEPTED` / `RUNNING` 这类中间状态。上面的状态机是为后续异步动作预留的协议边界，不表示当前已经完整实现。

------

## 8.5 自测问题

1. 为什么 gRPC bidirectional stream 至少需要独立 `recvLoop`，后续复杂场景可能需要 `sendLoop`？
2. 为什么不能在 recvLoop 中同步执行 AgentLoop？
3. `message_id` 解决什么问题？
4. `correlation_id` 解决什么问题？
5. `action_id` 和 `message_id` 有什么区别？
6. 什么是 `pendingRequests`？
7. `Observe()` 明明底层异步，为什么 AgentLoop 可以同步等待？
8. Stream 断开时所有 pending waiter 应该发生什么？
9. 为什么通常应该只有一个 goroutine 负责 `stream.Send()`？
10. 如果两个 AgentRun 同时 Observe，不使用 correlation 会出现什么问题？

------

# 9. 五个知识点之间到底是什么关系

不要把它们看成五个孤立技术。

它们实际上组成一条链：

```text
① Environment Bootstrap
        │
        │ 准备
        ↓
② Environment Abstraction
        │
        │ 提供 Observe / Act
        ↓
③ Agent Loop
        │
        │ 做决策
        ↓
④ Tool Runtime
        │
        │ 将决策变成环境操作
        ↓
⑤ Async RPC Correlation
        │
        │ 让环境操作真正跨进程完成
        ↓
       Game
```

换句话说：

```text
Bootstrap
=
Agent 能不能开始工作

Environment
=
Agent 能看到和操作什么

AgentLoop
=
Agent 怎么思考

Tool Runtime
=
Agent 怎么行动

RPC Correlation
=
行动如何真正跨进程执行
```

如果这五句话你可以脱口而出，说明第一阶段的大框架已经真正理解。

------

# 10. 综合自测：请不看代码画完整时序图

你应该能够从空白开始画：

```text
Stardew
Adapter
Gateway
AgentLoop
ToolRegistry
LLM Provider
```

然后画：

```text
Runtime Start
↓
Adapter Connect
↓
Hello
↓
Ready
↓
Capability Discovery
↓
Tool Registration

────────────

Player Click
↓
GameEvent
↓
AgentLoop
↓
ObserveRequest
↓
Observation
↓
Model Request
↓
ToolCall
↓
ActionRequest
↓
Speak
↓
ActionResult
```

如果画的时候频繁查看文档，说明还需要再复盘。

------

# 11. 综合自测：架构 Why Questions

这部分比“接口叫什么”更重要。

尝试独立回答：

1. 为什么要拆 Adapter 和 Runtime？
2. 为什么 Runtime 使用 Go，Adapter 使用 C#？
3. 为什么不把 LLM 放 Adapter？
4. 为什么使用长期双向连接？
5. 为什么 Protocol 和 Agent Harness 要分开？
6. 为什么 AgentLoop 依赖 Environment interface？
7. 为什么 Capability 由 Environment 决定？
8. 为什么 Capability 和 Tool 分开？
9. 为什么 Provider 要统一 ToolCall？
10. 为什么 ToolCall 必须经过 Runtime validation？
11. 为什么 Event 之后还需要 Observation？
12. 为什么 AgentLoop 是 Event-driven，而不是不停调用 LLM？
13. 为什么 MVP0 是 one-turn，而不是直接实现完整 ReAct？
14. 为什么 recvLoop 不能同步运行 AgentLoop？
15. 为什么现在不做 Memory / Planner / Multi-Agent？

这 15 个问题如果都能自然回答，这个项目才真正属于你。

------

# 12. 简历准备：30 秒版本

> 我实现了一个面向游戏环境的 Agent Runtime。游戏侧使用 C#/SMAPI Adapter，把游戏事件、状态和可执行能力抽象成统一 Protocol，通过 protobuf/gRPC 双向流连接 Go Runtime。Runtime 在 Environment Bootstrap 阶段发现 Capability 并映射成模型 Tool，游戏事件触发 one-turn AgentLoop，Loop 通过 Environment interface 获取 Observation，将 Event、Observation、Tools 构造成结构化 model.Request，再通过可配置 LLM Provider 调用 DeepSeek 得到 ToolCall，最后转换为 ActionRequest 返回游戏执行。第一阶段已经跑通真实 Stardew + 真实 DeepSeek + speak Tool 的闭环，重点解决了 Environment 解耦、Provider 解耦、Tool Runtime 和异步 RPC correlation。

------

# 13. 简历准备：两分钟技术版本

回答结构建议：

### 第一层：为什么做

```text
不想只做 NPC 对话，
想验证 LLM Agent 如何真正接入持续运行的游戏世界。
```

### 第二层：核心架构

```text
Game
↓
Adapter
↓
Protocol
↓
Go Runtime
↓
AgentLoop
↓
LLM Provider
```

### 第三层：关键设计

重点讲：

```text
Environment Bootstrap

Environment Interface

Capability → Tool

One-Turn AgentLoop

Configurable LLM Provider

Bidirectional RPC correlation
```

### 第四层：为什么这样设计

```text
Adapter 与 Agent Core 解耦

模型与 Provider 解耦

模型配置与 API key 解耦

Environment capability 动态成为 Tool

AgentLoop 不依赖 gRPC

异步消息之上提供同步 Environment API
```

### 第五层：下一步演进

```text
one-turn
↓
multi-turn

speak
↓
move_to

stateless
↓
memory

sync action
↓
suspend / resume
```

这能很好展示：

> 架构不是一开始过度设计出来的，而是随着 Agent Harness 能力逐渐演进。

------

# 14. 面试追问题库

## RPC

- gRPC 双向流与 WebSocket 有什么区别？
- 为什么不用 unary RPC？
- 如果 stream disconnect，pending request 怎么处理？
- 如何避免 goroutine leak？
- correlation map 是否需要锁？
- 超时如何实现？

## Agent

- Agent 和普通 LLM 调用有什么区别？
- AgentLoop 的停止条件是什么？
- Tool Result 为什么需要回填？
- 下一阶段如何实现 ReAct？
- AgentRun 和长期 Session 有什么区别？

## Tool

- Tool schema 从哪里来？
- 如何验证 LLM arguments？
- Permission 放在哪一层？
- Adapter Capability 改变如何同步？
- 如何支持 Runtime internal tool？

## Environment

- Observation 的 freshness 如何保证？
- Event 和 State 有什么区别？
- 世界在模型思考期间改变怎么办？
- 一个 Runtime 如何支持多个游戏环境？

这些目前不要求全部实现。

但应该知道：

> 当前设计未来准备在哪里解决。

------

# 15. 实践型自测

完成 MVP0 后，不看代码尝试完成以下任务。

### Challenge 1

增加：

```text
Capability: wave
```

要求：

```text
不修改 AgentLoop。
```

------

### Challenge 2

增加：

```text
FakeEnvironment
```

要求：

```text
不启动 Stardew
也能测试 AgentLoop。
```

------

### Challenge 3

让两个并发：

```text
ObserveRequest
```

同时存在。

验证：

```text
correlation_id
```

不会串响应。

------

### Challenge 4

把：

```text
runtime/config/model.json
```

从：

```text
provider = fake
```

切换为：

```text
provider = deepseek
```

或：

```text
provider = openai
```

要求：

```text
AgentLoop 不修改。
model 包不 import 任何具体 provider。
真实 API key 不写入配置文件。
```

------

### Challenge 5

假设未来换成：

```text
Minecraft Adapter
```

列出：

```text
哪些代码需要增加
哪些代码不应该修改
```

理想答案：

```text
新增 Adapter

Protocol / AgentLoop 核心尽可能不变
```

------

# 16. 自评分标准

每部分给自己打分：

```text
0 = 不知道
1 = 看代码能理解
2 = 能自己解释
3 = 能自己实现
4 = 能解释 trade-off
5 = 能应对追问和修改设计
```

评分：

| 模块                    | 分数 |
| ----------------------- | ---- |
| Environment Bootstrap   | /5   |
| Environment Abstraction | /5   |
| Agent Loop              | /5   |
| Tool Runtime            | /5   |
| Async RPC Correlation   | /5   |

总分：

```text
0–10
还停留在“跟着方案写代码”

11–17
理解了主要结构

18–21
能够独立实现和解释

22–25
已经可以作为面试项目深入讨论
```

真正目标不是：

```text
25 分
```

而是：

> **每一次项目演进以后重新回来答一遍，观察自己的理解发生了什么变化。**

------

# 17. 第一阶段完成后的学习复盘

MVP0 做完后，应该记录：

### 我最开始为什么这样设计？

### 哪些地方实际实现与预想不同？

### gRPC 双向流遇到了什么问题？

### Environment abstraction 是否真的降低了耦合？

### Capability → Tool 是否真的做到动态？

### Provider 是否真的能够无侵入替换？

### DeepSeek 与 OpenAI 的 Tool Calling 差异在哪里被吸收？

### 为什么 API key 只写 env 引用，不写入 model.json？

### 为什么 deepseek-v4-flash 不能使用 tool_choice=required？

### AgentLoop 当前最大的限制是什么？

然后再决定下一阶段。

不要提前假定下一阶段一定需要：

```text
Memory
Scheduler
Multi-Agent
```

应该先问：

> **当前最真实的限制是什么？**

这也是 Pi 目前强调小核心、通过外部扩展增加具体行为的思路值得借鉴的地方。

------

# 18. 最终记忆模型

如果最后只记住一张图，就记这一张：

```text
                     BOOTSTRAP

Game Adapter
     │
     │ Connect
     ↓
Environment Gateway
     │
     │ Capabilities
     ↓
Tool Registry
     │
     ↓
Environment Bootstrapped


                     RUNTIME

GameEvent
    ↓
AgentLoop
    ↓
Environment.Observe()
    ↓
BuildModelRequest
(System + Messages + Tools)
    ↓
LLM Provider Factory
    ↓
DeepSeek / OpenAI / Fake Provider
    ↓
ToolCall
    ↓
ToolRegistry
    ↓
EnvironmentTool
    ↓
Environment.SubmitAction()
    ↓
Game Adapter
    ↓
Game
```

以及一句话：

> **Harness 的作用，是把模型放进一个拥有 Context、Tools 和 Environment 的可执行控制循环中。**

GameAgent 第一阶段要做的，就是亲手把这个最小循环建立起来。
