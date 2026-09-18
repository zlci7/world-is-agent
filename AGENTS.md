# World Is Agent Agent Instructions

本文件是 `world-is-agent` 仓库的项目级常驻指令。所有 agent 在本仓库工作时应遵守这些边界。

## 版本定位

当前版本（MVP0）已经定型，工作方向调整为**固定该版本、完成开源产品化与对外展示**。任何 agent 在本仓库工作时应以这个目标为准：

- 本版本不做完全自主 agent。自主目标生成、长时程自主规划、多 Agent 协作、自我改进、无人监督的长时间自主运行均不在范围内，也不为它们预留未验证的框架。
- 新工作必须能对应到“对外可运行、可复现、可验证”或“公开事实源更准确”的收益。只有内部技术分层价值、且不影响对外结论的重构不做。
- 稳定性优先：破坏已发布协议字段、配置格式或使用方式，必须有明确理由和用户授权，不随普通改动顺带进行。
- 已授权的例外是 **Phase A 逻辑分离**（解除 Adapter 对 monorepo 目录布局的依赖，按 [docs/development/logical-separation.md](docs/development/logical-separation.md) 执行，不改变协议内容、仓库结构和 Mod 运行时标识）与 **Mod 邮件能力接入**（按 [docs/development/mail-capability.md](docs/development/mail-capability.md) 执行，仅文本、无附件、禁止游戏命令）。

## 跨 Mod 集成边界

Adapter 可以包装第三方 mod 能力并暴露为 Capability，但必须遵守：

- 第三方 mod 是**可选依赖**。未安装时 adapter 必须正常加载，能力以明确 code 返回 `REJECTED`，不崩溃、不产生硬依赖。
- 通过 SMAPI `GetApi` 或运行时反射调用，不直接引用第三方 mod 的程序集，避免未安装时加载失败。
- 所有 mod 调用都在游戏主线程执行；异常转为带明确 code 的 `failed` ActionResult，不静默吞掉。
- 第三方 mod 的返回值会进入模型上下文，不得把路径、存档内容等不应暴露的信息直接回灌。
- **外部文本必须先校验再交给游戏**：游戏侧可能把它当命令或 token 解析（例如 MFM 的信件正文）。校验失败返回 `REJECTED`，不写入未经校验的文本。
- 能力暴露什么，模型就会尝试什么。高风险副作用（发物品、改世界状态、执行命令）不得作为默认能力，需要单独授权。

## 仓库模型

WIA 按 Runtime + Protocol + Adapter 组织，分两阶段演进：

```text
Phase A（逻辑分离，已授权）
    消除 Adapter 对仓库目录布局的依赖：显式协议依赖点、协议版本契约、脚本与布局解耦、脱离仓库构建验证。
    物理上仍在同一仓库。

Phase B（物理拆仓，未授权）
    Adapter 迁往独立仓库 wia-adapter-<game>。
    触发条件：出现第二个真实 Adapter 时优先拆；“Runtime 开始正式 Release”是强信号，但不是必须拆仓的条件。
```

- Runtime 主仓库保留 `world-is-agent`，不改名为 `wia-runtime`。
- 独立 Adapter 仓库统一使用 `wia-adapter-<game>` 命名，例如 `wia-adapter-stardew-valley`。
- 协议定义暂留在主仓库 `protocol/`，不单独拆仓；Adapter 通过版本化契约依赖它。
- Mod 的 `UniqueID`、`EntryDll`、`AssemblyName` 等运行时标识与本条命名规范无关，属已发布契约，改动需单独授权。

## 基本工作方式

- 默认使用中文沟通；面向公开读者的文档使用英文。
- 先读现有代码与阶段文档，再修改实现。
- 用 `grep` / `glob` 查找文件和文本，用 `edit` / `write` 修改文件，用 `pwsh` 执行命令。
- 不还原用户或其它 agent 已经做出的无关改动。
- 只执行本地 `git commit`，不执行 `git push`；推送由用户本人完成。即使用户在别处授权过推送，也按本规则执行。
- 只有用户明确要求时才提交 git；如需保存阶段性结果，最多执行本地 `git commit`。

## 范围与交付

- 只完成用户明确要求的内容，以及让这些内容正常成立所必需的配套工作。
- 范围模糊时按最小必要集合处理，并在聊天中说明可选扩展，不自行扩大产品或协议范围。
- 最终交付物只描述当前确认后的结果，不记录被放弃的方案、调试过程或修改痕迹。
- 文档和代码注释只解释当前系统真实存在的约束、规则、风险或兼容逻辑。

## 迭代与交付流程

- 以“一个用户可见的改进或一项公开文档修正”为工作单元，不再以阶段 / 子阶段作为开发、交付和验收单位。
- 一个工作单元完成相关测试、直接受影响的回归、内部 CR 和 `git diff --check` 后本地提交即可继续，不要求先产出阶段方案文件或等待逐项验收。
- 缺陷修复、文档一致性修正、既有行为的健壮性补强按上述方式直接完成；新增对外能力、改变产品行为或扩大范围时，先向用户确认范围与验收条件。
- 交付时说明：改了什么、跑了哪些验证、结果如何、还有什么已知限制。内部自查与独立上下文审查应如实区分。
- 需要实机验证的改动，交付时写清实机验证步骤和失败时的安全行为。自动化测试证明机制正确性，不替代实机结论，也不把只有 fake 通过记成实机通过。
- 达到对外可见的里程碑时，同步更新 `README.md`、`docs/STATUS.md` 等公开事实源，让对外结论与已通过验证的改动一致。

## 架构边界

WIA 的长期边界是：

```text
Agent owns intent.
Runtime owns cognition.
Protocol owns contracts.
Adapter owns translation.
Game owns execution.
```

Runtime Core 必须保持 game-agnostic：

- 不依赖 Stardew / SMAPI 类型。
- 不解析 game-specific `Observation.state` 字段。
- 不按 game-specific `event_type` 写 Trigger / Memory / Context 分支。
- 不按 game-specific capability name 写执行策略分支。
- 不接管路径规划、UI 展示、游戏主线程调度或具体动作实现。

Adapter 是游戏事实来源：

- 负责读取游戏 API。
- 负责构建 namespaced Observation。
- 负责 capability 的 schema、description、extensions、执行和失败原因。
- 负责 UI、pathfinding、主线程执行和游戏侧状态检查。

Protocol 是 Runtime 与 Adapter 的契约，不承载单个游戏的私有模型。

## Capability 与 Tool Policy

`Capability.description` 是 model-facing 自然语言说明，用于告诉模型工具用途、参数语义和游戏侧效果。

`Capability.extensions.gameagent.tool_policy` 是 Runtime-facing 结构化执行策略，用于表达通用调度约束。

Runtime 执行逻辑只能依赖结构化 metadata：

```text
Capability.execution_mode
Capability.concurrency_mode
Capability.extensions.gameagent.tool_policy
```

Runtime 不得从以下来源推断执行策略：

```text
capability name
Capability.description
game-specific event_type
game-specific Observation.state
```

同类能力在不同游戏中可以有不同名称。例如 Stardew 可以叫 `present_dialogue`，其它游戏可以叫 `ask_player` 或 `show_choices`。Runtime 应通过 `exclusive_per_step`、`settle_after_success` 等通用 policy 理解执行语义。

后续玩家输入或环境进展由新的 GameEvent 驱动这一事实，属于 capability description 与 event contract，不作为 Runtime-facing policy。

## Prompt 配置边界

- Runtime 默认 prompt 必须保持通用，不写死 Stardew capability name。
- 游戏特定 prompt profile 如有需要，应放在 Runtime 配置树，例如 `runtime/config/profiles/<game>.json`。
- 不把 Runtime prompt 配置放入 Adapter 目录。
- Prompt 只能引导模型选择工具，不能作为 Runtime 执行约束的唯一来源。
- 需要强制执行的规则必须进入结构化 metadata、Registry、Scheduler 或 Protocol。

## Context 与 Memory

- Observation 是 narrow waist，不是跨游戏统一状态 schema。
- `Observation.state.<game>` 可以承载 Adapter namespaced 当前事实，Runtime Core 不直接解析其游戏私有结构。
- `ContextFact` 表示 model-visible 的通用事件事实，kind 必须保持跨游戏语义。
- Runtime Memory projection 不得以 `player_said_to_npc` 等 game-specific event type 作为核心分支条件。
- Recent Memory 只表示 previous turns；Current Turn Transcript 只表示当前 Turn 内 earlier steps。
- 过滤或排序 Memory 时使用通用时间、sequence 和 AgentSession 边界，不依赖具体游戏字段。

## Protocol 变更原则

- 优先 additive 变更。
- 不为单个游戏字段扩展 Protocol。
- 不引入双来源字段；同一语义应有唯一权威来源。
- `definition_id` 的协议来源是 `EntityRef.definition_id`，不得重新放入 `Observation`。
- `ActionRequest.source_event_id` / `source_turn_id` 是 Runtime 写入的来源关联，不由模型或 Adapter 猜测。
- `TurnCompletion` 是 Runtime -> Adapter 的 Turn 终态信号，不替代 `ActionResult`。
- 尚未稳定为协议核心的 capability policy 优先使用 `Capability.extensions` 验证。

## Async Action 边界

- Action 不等于同步函数。
- Runtime 可以管理 action lifecycle、timeout、cancel、suspend、resume 和 trace。
- Adapter / Game 负责真实动作执行、可达性判断、路径规划和中断原因。
- 当前 continuation 是 in-memory；Runtime 崩溃恢复、Adapter reconnect 恢复 pending action 和长期持久化属于未来工作。

## 命名与标识

- 对外产品名统一使用 `World Is Agent` 和 `WIA`。`gameagent` 只作为历史阶段名称和下列不可变标识保留。
- 以下标识属于已发布的兼容契约或构建入口，保持不变，不做产品名统一：
  - Go module 与导入路径 `gameagent/...`；
  - proto 包名 `gameagent.protocol.v1alpha2` 及生成代码命名空间；
  - `option csharp_namespace`（例如 `GameAgent.Protocol.V1Alpha2`）；
  - C# 项目文件名 `GameAgent.Stardew.csproj`、C# namespace `GameAgent.Stardew`；
  - Mod 安装目录 `Mods/GameAgentStardew` 与安装脚本中的同名配置；
  - SMAPI 日志前缀、游戏内命令名等运行时可见标识。
- 改动以上标识会破坏兼容或构建路径，必须由用户单独授权，不随文档整理或品牌统一顺带执行。

## 文档边界

文档按用途分三类：

```text
公开事实源
    README.md、ARCHITECTURE.md、docs/README.md、docs/STATUS.md、protocol/README.md、docs/development/guide.md、docs/development/testing.md
    必须与当前实现一致，是对外读者的准确入口。

详细基线与规范
    docs/summary/ 下的架构规范、多游戏兼容性决策等
    保留既有文件名以维持引用稳定；内容按需要对当前版本补充说明。

历史记录
    docs/phase*/ 阶段方案与验收记录、docs/archive/、docs/pro/、docs/superpowers/、docs/adapter/
    属于历史事实记录，不回填修改、不做名称批量替换；仅在链接或引用确实误导外部读者时修正引用。
```

- 公开事实源不得引用不存在的文件，链接必须有效。
- 公开事实源只描述当前已经成立的能力、验证范围和已知限制，不承诺尚未实现的细节已通过验收。
- 尚未验证或有明确限制的能力，必须在公开事实源中如实标注为实验性或未支持。

## 验证要求

- 修改 Protocol 时同步更新 static check、生成代码和相关 Runtime / Adapter 测试。
- 修改 Runtime Tool / Scheduler / Loop 时覆盖通用 fake capability，避免只用 Stardew 工具名证明行为。
- 修改 Adapter capability 时同步更新 `CapabilityCatalog` 测试和 static check。
- 文档修改至少运行 `git diff --check`，并确认改动的链接仍然有效。
- 代码完成前运行与改动范围匹配的测试；不能运行时在最终回复中说明原因。
