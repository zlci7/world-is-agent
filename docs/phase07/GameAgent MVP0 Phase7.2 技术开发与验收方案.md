# GameAgent MVP0 Phase7.2 技术开发与验收方案

> **Status:** Accepted
> **Date:** 2026-09-03
> **Phase:** Phase7.2 Environment-scoped Tool View
> **Roadmap Baseline:** [GameAgent 阶段规划](../summary/GameAgent%20阶段规划.md) v1.9
> **Previous Gate:** Phase7.1 code reviewed
> **Review Required Before Coding:** Yes
> **Code Baseline:** `main` @ `263d829`
> **Accepted Commit:** `main` @ `3f5a490`
> **Review Result:** Accepted
> **Reviewer:** zlc7
> **Review Date:** 2026-09-03

---

# 1. 阶段目标

Phase7.2 让模型看到的工具，和 Runtime 实际执行的工具，来自同一份 EnvironmentSession Tool View。

本阶段主要证明：

```text
Adapter Connect
    ↓
Runtime 接收 CapabilityList
    ↓
Gateway 为当前 EnvironmentSession 建立 Tool Catalog
    ↓
AgentTurn 捕获不可变 Tool View snapshot
    ↓
Model Request 使用这份 Tool View
    ↓
Scheduler 只执行这份 Tool View 中存在的工具
```

Phase7.1 已经完成 canonical Target、Definition 和 Agent Instance Descriptor 的主链路。Phase7.2 不重新设计这些内容，只补齐“当前环境连接到底有哪些工具可用”这一段。

---

# 2. 非目标

Phase7.2 不做：

```text
完整 Context Engine
Context Projection 分层选择
Context Budget
完整 BuildReport
工具长度 / token admission
Provider-specific tokenizer
Adapter reconnect
Capability hot refresh
Capability subscription
entity-scoped Tool View 完整支持
Tool View persistent registry
跨连接 replay
Persistent Memory backend
Stardew 最终实机验收
```

本阶段可以记录最小 bootstrap / consistency diagnostics，但不把 diagnostics 扩展成 Phase7.4 的完整 BuildReport。

---

# 3. 当前代码事实

## 3.1 协议已有 CapabilityList

当前协议已有：

```text
CapabilityRequest:
    entity_id

CapabilityList:
    entity_id
    capabilities[]
    revision

Capability:
    name
    description
    input_schema_json
    execution_mode
    concurrency_mode
    extensions
```

Phase7.2 不新增协议字段。Runtime 继续复用现有 `CapabilityRequest` / `CapabilityList`。

## 3.2 Gateway 当前把 capability 注册到共享 registry

当前 `runtime/internal/gateway/gateway.go` 中：

```text
Connect:
    发送 CapabilityRequest{}
    接收 CapabilityList
    调用 s.tools.RegisterEnvironmentCapabilities(capabilityList.Capabilities)
```

`s.tools` 是 `Server` 持有的进程级 `tool.Registry`。这意味着不同 Adapter stream 上报的 environment tool 当前会进入同一个共享 registry。

当前 Gateway 只读取 `capabilityList.Capabilities`，没有检查 `CapabilityList.entity_id`。

## 3.3 AgentLoop 当前从共享 registry 读取工具

当前 `runtime/internal/agent/loop.go` 中：

```text
buildModelRequest:
    Tools: l.tools.Available()

newToolBatchScheduler:
    registry: l.tools

concurrencyModesForCalls:
    l.tools.Lookup(call.Name)
```

因此模型可见工具、Scheduler lookup 和 trace 中的 concurrency mode 当前都依赖同一个进程级 registry，而不是某个 EnvironmentSession 或 AgentTurn 自己的工具快照。

## 3.4 Registry 当前会跳过非法项，也会按 name 覆盖

当前 `runtime/internal/tool/registry.go` 中：

```text
invalid input_schema_json:
    fmt.Printf 后跳过

invalid tool_policy:
    fmt.Printf 后跳过

重复 capability name:
    后写覆盖前写
```

这些行为能让单 Adapter happy path 跑通，但不能证明多 EnvironmentSession 隔离，也不能证明同一个 Turn 中“模型看到的工具”和“实际执行的工具”完全一致。

## 3.5 现有测试还没有覆盖环境级隔离

当前 `runtime/internal/tool/registry_test.go` 已覆盖：

```text
Capability schema 解析
tool_policy 解析
并发 Register / Available
Available 排序
Lookup
sync / async execution mode
sequential / parallel_safe concurrency mode
```

Phase7.2 需要新增 environment-scoped catalog、turn snapshot 和 gateway / agent loop 集成测试。

---

# 4. 设计范围

## 4.1 最小对象

Phase7.2 引入两个最小对象：

```text
EnvironmentToolCatalog
    当前 EnvironmentSession 的工具目录
    在 capability bootstrap 后形成
    对 AgentTurn 只读

TurnToolView
    单个 AgentTurn 的工具快照
    在 AgentTurn 开始时从 EnvironmentToolCatalog 捕获
    同一个 Turn 内不可变
```

`EnvironmentToolCatalog` 和 `TurnToolView` 都包含完整 `tool.Entry`：

```text
model-facing ToolDefinition
Tool Kind
Concurrency Mode
Execution Mode
Tool Policy
Lookup Entry
```

本阶段复用现有 `tool.Entry`、`model.ToolDefinition`、`ToolPolicy`、`ConcurrencyMode` 和 `ExecutionMode`，不新增 repository、resolver 或持久化 registry。

## 4.2 CapabilityList.entity_id 的 MVP0 语义

Phase7.2 只支持 environment-level capability：

```text
Runtime 发送:
    CapabilityRequest{}

Adapter 返回:
    CapabilityList.entity_id unset
    或 CapabilityList.entity_id == ""
```

`CapabilityList.entity_id` 的空值语义：

```text
unset
    environment-level capability list

""
    environment-level capability list

strings.TrimSpace(entity_id) != ""
    entity-scoped capability list
    Phase7.2 MVP0 不支持
    Gateway 记录 unsupported_entity_scope diagnostic
    Gateway 的 Connect 返回非 nil error 并结束 stream
    Runtime 不构造 EnvironmentToolCatalog
    Runtime 不把这些工具暴露成 EnvironmentSession 全局工具
    Runtime 不进入后续 GameEvent admission

entity_id 非空且 strings.TrimSpace(entity_id) == ""
    invalid entity scope
    Gateway 记录 invalid_entity_scope diagnostic
    Gateway 的 Connect 返回非 nil error 并结束 stream
    Runtime 不构造 EnvironmentToolCatalog
    Runtime 不进入后续 GameEvent admission
```

这样可以避免把某个实体专属能力错误暴露给整个环境连接。

## 4.3 Capability 校验规则

Capability bootstrap 采用确定性规则：

```text
nil Capability
    跳过并记录 diagnostic

空 name
    记录 invalid_tool_name diagnostic
    不进入 Catalog

首尾有空白的 name
    记录 invalid_tool_name diagnostic
    不进入 Catalog

invalid input_schema_json
    记录 invalid_schema diagnostic
    不进入 Catalog

invalid tool_policy
    记录 invalid_tool_policy diagnostic
    不进入 Catalog

同一个 CapabilityList 内重复 name
    该 name 对应的所有 capability 都不暴露
    记录 duplicate_tool_name diagnostic
```

Capability name 是 Adapter 声明的能力身份。Runtime 不做大小写转换、不做别名映射、不修改 Adapter 声明的名称。

合法 name 必须满足：

```text
name 非空
name == strings.TrimSpace(name)
```

重复检查使用通过合法性校验后的精确 name。重复 name 不保留 first-write，也不保留 last-write。本阶段用“重复即不暴露”避免模型和 Scheduler 对同名工具产生不同理解。

`input_schema_json` 的最小 bootstrap 规则：

```text
必须是合法 JSON
顶层必须是 JSON object
root type 缺省或为 "object"

以下情况不进入 Catalog：
    scalar
    array
    null
    root type 明确不是 "object"
```

Phase7.2 不建设完整 JSON Schema validator。`properties`、`required`、`additionalProperties`、`enum` 和基础类型检查继续复用现有 arguments validation。

校验顺序固定为：

```text
1. nil capability 直接跳过；
2. 校验 name 非空且没有首尾空白；
3. 按合法 name 分组；
4. 同名出现多次则整组排除；
5. 只对唯一 name 校验 schema 和 tool_policy；
6. 构造 Catalog。
```

不同 EnvironmentSession 可以拥有同名 tool。它们属于不同 `EnvironmentToolCatalog`，互不覆盖。

## 4.4 Bootstrap 返回语义

`BuildEnvironmentToolCatalog` 不强制固定 Go 签名，但语义上必须返回：

```text
catalog
bootstrap diagnostics
error
```

返回规则：

```text
CapabilityList.entity_id trim 后非空，或纯空白非法
    catalog = nil
    diagnostics 记录 unsupported_entity_scope 或 invalid_entity_scope
    error != nil
    Gateway 记录 diagnostics 后结束 stream

environment-level CapabilityList 合法，但所有 capability 都被过滤
    catalog = empty catalog
    diagnostics 记录 catalog_tool_count = 0
    error = nil

至少一个 capability 合法
    catalog = EnvironmentToolCatalog
    diagnostics 记录 accepted tools 和 skipped reasons
    error = nil
```

空 Catalog 是合法结果。它表示当前 EnvironmentSession 没有可用工具，不等于 transport failure。Stardew-shaped Runtime fixture 单独证明与 Stardew Adapter 当前声明等价形状的 capability list 可以进入 catalog 和 TurnToolView snapshot。

成功返回时 `catalog` 必须是非 nil 的显式对象。`empty catalog` 也是显式对象：`Available()` 返回空列表，`Lookup()` 返回 false。`nil catalog` 表示 bootstrap 或接线失败，不能被当作“当前环境没有工具”。

## 4.5 EnvironmentToolCatalog

`EnvironmentToolCatalog` 由 capability bootstrap 生成：

```text
CapabilityList
    ↓
entity_id 检查
    ↓
Capability 结构校验
    ↓
重复 name 检查
    ↓
EnvironmentToolCatalog
    ↓
BootstrapDiagnostics
```

`EnvironmentToolCatalog` 提供只读能力：

```text
Available() []model.ToolDefinition
Lookup(name string) (tool.Entry, bool)
Snapshot() TurnToolView
```

`Available()` 必须按 tool name 稳定排序。`Lookup()` 必须只查当前 catalog，不读取进程级 environment registry。

## 4.6 TurnToolView

AgentTurn 开始后、第一次构建 Model Request 前，Runtime 从当前 `EnvironmentToolCatalog` 捕获 `TurnToolView`。

```text
EnvironmentToolCatalog
    ↓
TurnToolView snapshot
    ├── Model Request.Tools
    ├── Tool Scheduler.Lookup
    ├── Tool argument validation
    ├── concurrency mode
    ├── execution mode
    └── Tool Policy
```

同一个 AgentTurn 内，所有 AgentStep 使用同一份 `TurnToolView`。

`TurnToolView` 是 validated catalog snapshot。它不做 tool count、schema size 或 token admission。Phase7.4 可以在 Catalog 到 Final Turn Tool View 的边界加入确定性的 size admission，但不能改变“模型可见工具”和“Scheduler lookup”同源这一合同。

`Snapshot()` 必须复制同一批 `tool.Entry`，`Available()` 的排序结果和 `Lookup()` 的 map 都来自这批拷贝。创建 snapshot 后，后续 catalog 变量变化不能影响当前 turn。

Phase7.2 不实现 capability hot refresh，因此同一条 EnvironmentSession 的 catalog 在连接生命周期内保持不变。未来 reconnect 或 capability refresh 进入后，新连接或新 revision 可以生成新的 catalog，但旧 turn 仍使用自己的 snapshot。

## 4.7 Gateway / AgentLoop 接线

Gateway 在 `Connect` 中完成 capability bootstrap，并把 `EnvironmentToolCatalog` 绑定到当前 stream 的执行上下文。

catalog 是 `Connect` 的 stream 局部状态。GameEvent admission 成功后，`dispatchGameEvent` 通过 task closure 把当前 catalog 传给 `AgentLoop.HandleEvent`；AgentLoop 在 `HandleEvent` 内捕获 `TurnToolView`，后续构建模型请求和创建 Scheduler 时都显式传入这份 snapshot。

Phase7.2 固定采用这条接线路径：

```text
Gateway Connect stream
    持有非 nil EnvironmentToolCatalog

dispatchGameEvent task closure
    将 EnvironmentToolCatalog 传给 AgentLoop.HandleEvent

AgentLoop.HandleEvent
    从 EnvironmentToolCatalog 捕获 TurnToolView
```

`AgentLoop.HandleEvent` 必须收到非 nil `EnvironmentToolCatalog`。如果入口参数缺失，Runtime 应返回明确错误；不能静默创建空视图，也不能把 nil 解释成“当前环境没有工具”。

Environment tool 不通过 `Server.tools`、包级变量、`session_id` map 或共享 `Registry` 间接读取。

Composition Root 也要完成迁移。`runtime/cmd/server/main.go` 不能再创建一份 environment capability 全局 registry 并同时注入 Gateway 和 AgentLoop。

Phase7.2 完成后：

```text
buildModelRequest
    从 TurnToolView 读取 Tools

newToolBatchScheduler
    使用 TurnToolView Lookup

concurrencyModesForCalls
    使用 TurnToolView Lookup
```

旧的进程级 Runtime Tool 未来可以继续存在，但当前 Environment Tool 不再由进程级 registry 决定完整工具视图。

Phase7.2 后，旧 `tool.Registry` 不能继续承载 environment capabilities。如果没有其他真实调用方，可以删除或改造成 Catalog 的内部实现。如保留该类型，只能作为未来 runtime-global tool 的独立来源；runtime-global tool 也必须先进入 `TurnToolView`，才能被模型看到并被 Scheduler 执行。

## 4.8 最小 diagnostics

Phase7.2 记录最小工具诊断。诊断分为 bootstrap 阶段和 AgentTurn 阶段。

`BootstrapDiagnostics` 只记录 capability bootstrap 结果：

```text
accepted tool count
accepted tool names
skipped nil capability count
invalid tool names
skipped invalid schema names
skipped invalid tool_policy names
duplicate tool names
unsupported entity_id
invalid entity_id
capability revision
catalog tool count
```

`capability revision` 只记录，不驱动 replacement、hot refresh 或 subscription。

`BootstrapDiagnostics` 是 EnvironmentSession scope。它只在 Connect / capability bootstrap 过程中返回，用于 Gateway log 和 bootstrap 测试。

AgentTurn trace / consistency diagnostic 记录 turn 级快照信息：

```text
turn snapshot tool count
turn snapshot tool names
```

Turn snapshot count / names 是 AgentTurn scope，进入 turn trace 字段。

`BuildEnvironmentToolCatalog` 不负责填写 AgentTurn 尚未创建时才会出现的 snapshot 数据。

这些 diagnostics 用于开发期测试、日志和 trace 字段。它们不承担完整 BuildReport 职责。

---

# 5. Implementation Handoff

本节是给 Phase7.2 coding agent 的开发交接说明。本文档只定义架构合同、实现顺序和验收口径，不执行代码改动。

## M1：EnvironmentToolCatalog builder

新增 environment-scoped catalog 构造逻辑。

验收点：

```text
有效 capability 可以进入 catalog
invalid tool name 不进入 catalog
invalid input_schema_json 不进入 catalog
非 object input schema 不进入 catalog
invalid tool_policy 不进入 catalog
重复 name 不进入 catalog
所有 capability 都被过滤时返回 empty catalog，error = nil
Available 稳定排序
Lookup 只读可用
BootstrapDiagnostics 可测试
```

## M2：Gateway 绑定 EnvironmentSession catalog

Gateway 在 capability discovery 完成后，为当前 stream 建立独立 catalog。

验收点：

```text
每条 Connect stream 拥有自己的 EnvironmentToolCatalog
CapabilityList.entity_id trim 后非空或纯空白非法时 Connect 返回非 nil error 并结束 stream
CapabilityList.entity_id trim 后非空或纯空白非法时不进入后续 GameEvent admission
不同 stream 的 capability 不写入共享 environment registry
runtime/cmd/server/main.go 不再双向注入 environment capability 全局 Registry
```

## M3：AgentTurn 捕获 TurnToolView

AgentLoop 在 `HandleEvent` 中捕获 `TurnToolView`，并将其贯穿当前 Turn 的模型请求和工具执行。

验收点：

```text
Model Request.Tools 来自 TurnToolView
Scheduler Lookup 来自 TurnToolView
argument validation 使用 TurnToolView 中的 schema
concurrency mode / execution mode / Tool Policy 来自 TurnToolView
同一 Turn 内多个 AgentStep 使用同一份 snapshot
```

## M4：集成测试与 Stardew-shaped fixture 回归

补齐多 EnvironmentSession、同名工具、非法 capability 和 Stardew-shaped Runtime fixture 回归测试。

验收点：

```text
Runtime fixture 能接受与 Stardew Adapter 当前声明等价形状的 capability list
present_dialogue / emote / face_player / move_to 能进入 catalog 和 TurnToolView snapshot
present_dialogue / emote / face_player / move_to 的 schema、execution mode、concurrency mode 和 Tool Policy 不退化
Phase5 multi-step 与 Phase6 async move_to 行为不因 Tool View 改造退化
多 EnvironmentSession 同名工具测试同时验证模型工具、参数校验、Scheduler lookup 和 Tool Policy 均使用各自 snapshot
```

## M5：测试与回归命令

代码实现完成后，coding agent 按第 6 章测试计划执行单元测试、集成测试和回归命令。

---

# 6. 测试计划

## 6.1 Tool catalog 单元测试

需要覆盖：

```text
valid capability 进入 EnvironmentToolCatalog
invalid input_schema_json 被跳过并记录 diagnostic
顶层为 array / scalar / null 的 input_schema_json 被跳过并记录 diagnostic
root type 明确不是 object 的 input_schema_json 被跳过并记录 diagnostic
invalid tool_policy 被跳过并记录 diagnostic
nil capability 被跳过并记录 diagnostic
空 name 被拒绝并记录 invalid_tool_name
首尾空白 name 被拒绝并记录 invalid_tool_name
同一 CapabilityList 内重复 name 不暴露
所有 capability 都无效时 bootstrap 成功生成 empty catalog
empty catalog 的 accepted_count / catalog_tool_count 为 0
成功 bootstrap 返回非 nil explicit empty catalog
Available 按 name 稳定排序
Lookup 不改变 catalog
TurnToolView 捕获后不受后续 catalog 变量变化影响
```

## 6.2 Gateway 集成测试

需要覆盖：

```text
两个 EnvironmentSession 上报不同 capabilities，新 Turn 只看到当前连接的 tools
两个 EnvironmentSession 上报同名 capability 但 schema / policy 不同时，不互相覆盖
CapabilityList.entity_id trim 后非空时 bootstrap 明确失败，并记录 unsupported_entity_scope
CapabilityList.entity_id 纯空白时 bootstrap 明确失败，并记录 invalid_entity_scope
CapabilityList.entity_id scope 非法时没有 catalog，且不进入 GameEvent admission
CapabilityList.entity_id unset 和显式 "" 都按 environment-level 处理
bootstrap diagnostics 可以在测试中确认
```

## 6.3 AgentLoop / Scheduler 测试

需要覆盖：

```text
Model Request.Tools 来自 TurnToolView
Scheduler 对模型不可见工具返回 tool_not_registered
Scheduler 不从进程级 environment registry 查当前 Turn 工具
argument validation 使用 TurnToolView 中的 schema
concurrency mode 记录来自 TurnToolView
async / sync execution mode 来自 TurnToolView
Tool Policy 来自 TurnToolView
同一 AgentTurn 的第 1 个 AgentStep 和后续 AgentStep 使用同一份 TurnToolView
多 EnvironmentSession 同名工具的模型列表、参数校验、Scheduler lookup 和 Tool Policy 各自隔离
HandleEvent 收到 nil EnvironmentToolCatalog 时返回明确错误
explicit empty TurnToolView + 模型 settle 可以正常完成 Turn
explicit empty TurnToolView + 模型 ToolCall 返回 tool_not_registered
```

## 6.4 回归测试

需要运行：

```text
go test ./runtime/internal/tool
go test ./runtime/internal/gateway
go test ./runtime/internal/agent
go test ./runtime/...
```

Stardew-shaped Runtime fixture 是 hard gate。coding agent 需要证明与 Stardew Adapter 当前声明等价形状的 capability list 可以进入 catalog 和 TurnToolView snapshot。

如果 Phase7.2 代码修改触碰协议生成、Adapter fixture 或 Stardew adapter capability 声明，再运行对应 adapter unit / build test。真实进游戏 smoke 和最终 Stardew 实机表现不属于 Phase7.2 hard gate。

---

# 7. 验收条件

Phase7.2 代码开发完成后必须满足：

```text
1. Environment Tool 不再通过进程级共享 registry 决定当前 Turn 的完整工具视图。
2. 每条 EnvironmentSession 都有独立 Tool Catalog。
3. 每个 AgentTurn 都捕获不可变 TurnToolView。
4. Model Request.Tools 与 Scheduler Lookup 使用同一份 TurnToolView。
5. 同一 CapabilityList 内重复 name 不会静默覆盖。
6. 空 name 或首尾有空白的 name 不会进入模型，也不能被 Scheduler 执行。
7. invalid input_schema_json 或非 object input schema 不会进入模型，也不能被 Scheduler 执行。
8. invalid tool_policy 不会进入模型，也不能被 Scheduler 执行。
9. CapabilityList.entity_id trim 后非空或纯空白非法时不会被暴露成 EnvironmentSession 全局工具。
10. CapabilityList.entity_id unset 和显式 "" 都按 environment-level 处理。
11. CapabilityList.entity_id 纯空白时 bootstrap 明确失败。
12. 所有 capability 都被过滤时，bootstrap 返回合法 empty catalog。
13. nil EnvironmentToolCatalog 不会被静默当成 empty catalog。
14. 不同 EnvironmentSession 的同名工具不互相覆盖。
15. Stardew-shaped Runtime fixture 可以进入 catalog 和 TurnToolView snapshot。
```

本阶段不以完整 Context Engine、Budget、BuildReport 或 Stardew 最终实机效果作为验收条件。

---

# 8. Review Checklist

Review Phase7.2 时重点看：

```text
1. 是否只解决 Tool View，没有提前实现 Phase7.3 到 Phase7.5 的内容。
2. EnvironmentToolCatalog 和 TurnToolView 的生命周期是否清楚。
3. CapabilityList.entity_id 的 MVP0 语义是否明确。
4. 是否消除了进程级共享 registry 导致的工具串线风险。
5. 模型可见工具和 Scheduler 可执行工具是否来自同一份 snapshot。
6. diagnostics 是否保持最小，没有变成 BuildReport。
7. bootstrap 的 catalog / diagnostics / error 语义是否统一。
8. schema 最小对象规则是否足够明确。
9. 测试是否覆盖多 EnvironmentSession、重复 name、非法 schema / policy 和 Stardew-shaped Runtime fixture 回归。
```

---

# 9. 下一阶段衔接

Phase7.2 完成后，Phase7.3 可以同时消费 Phase7.1 的身份与定义输入，以及 Phase7.2 已捕获完成的 turn 级工具快照：

```text
Phase7.1:
    Game Definition
    Agent Definition
    Agent Instance Descriptor
    canonical Target EntityRef

Phase7.2:
    TurnToolView snapshot
    最小 Tool diagnostics
```

Phase7.2 产出的 `EnvironmentToolCatalog` 保持在 Gateway / Tool Runtime / AgentTurn setup 边界。Phase7.3 Context Engine 不直接消费 `EnvironmentToolCatalog`，只消费已经捕获完成的 `TurnToolView snapshot`。

Phase7.3 负责把 Definition、Observation、Memory、Transcript 和 Tool View 组合成结构化 Context Projection。Phase7.2 不负责最终 Context 选择和预算裁剪。
