# World Is Agent 当前实现与验证状态

截至日期：2026-10-17（Asia/Shanghai）

| 项目 | 当前代码基线 |
| --- | --- |
| Runtime 仓库 | `world-is-agent`，Phase11.1 开发版本（起始基线 `c14a75c`） |
| 官方 Adapter 仓库 | `world-is-agent-adapters`，`ebc295485d6655a6bb5dab830633ca2aa6a69e41` |
| Runtime 版本 | `0.2.0` |
| Stardew Valley Adapter 版本 | `0.1.1` |
| RimWorld Adapter 版本 | `0.1.0` |
| Protocol | `gameagent.protocol.v1alpha2`；Adapter 声明的测试版本为 `protocol-v1alpha2.0` |

实现状态以以上两个仓库的源码为准；实机结论以对应验收记录为准。代码版本、自动化通过与游戏内验收是不同状态，不代表用户当前安装的二进制与上述源码一致，也不代表同版本已经完成公开 Release。

## 1. 当前产品状态

World Is Agent 已有可运行的 MVP0：游戏 Adapter 提供角色身份、事件、观察和能力，Runtime 构建上下文、调用模型、调度动作，并保存历史和执行结果。

当前版本的核心能力包括角色对话、动态工具选择、同步与异步动作、持久任务、历史摘要和检索、本地 Web 控制台，以及 Stardew Valley 与 RimWorld 两个真实游戏接入。

长期行为已经具备持久化、游戏时间唤醒、执行证据协调和检查点恢复机制。Stardew Valley 的预约赴约已有真实游戏闭环；跨保存、重启、重新读档和回档的完整实机可靠性仍有验收缺口。当前没有任意长期任务持续自主完成的保证。

MVP0 的工作方向是稳定现有版本、完成开源产品化和对外展示。自主目标生成、长时程自主规划、多 Agent 协作和自我改进均不在当前版本范围内。

## 2. 已实现的公共基础能力

| 领域 | 已实现内容 | 当前边界 |
| --- | --- | --- |
| 架构与协议 | Runtime、Protocol、Adapter 分工；gRPC 双向流；环境启动、能力发布、事件、观察、动作结果和回合终态消息 | Runtime Core 保持 game-agnostic，具体游戏规则和执行由 Adapter 负责 |
| 角色身份 | `game_id + world_id + entity_id` 隔离角色运行身份和历史 | 不同游戏、世界和角色分别管理 |
| 回合执行 | 有预算的多步 AgentTurn，模型工具选择、结果反馈、重新观察与结束 | 受步数、工具次数、并发和超时限制 |
| 工具系统 | EnvironmentSession 范围的动态 Capability；每回合固定 Tool View；结构化执行策略 | 模型只获得当前环境发布且通过预算准入的工具 |
| 异步动作 | 启动、状态、终态结果、超时和取消；等待结束后继续当前回合 | 运行中的普通 Action 等待状态保存在进程内 |
| 持久任务 | SQLite Task、到期唤醒、新回合执行、证据收敛、终态结果和恢复协调 | 当前游戏侧主要接入 Stardew Valley 预约链路 |
| 历史与上下文 | SQLite 历史、近期历史、累计摘要、原文检索、当前回合记录、Task Context 和上下文预算 | 受数据读取、检索和估算 token 预算约束 |
| 模型接入 | DeepSeek、OpenAI；Fake Provider 供测试使用 | 兼容 Base URL 可配置，具体第三方服务仍需单独验证 |
| Game Profile | 随 Runtime 提供两个游戏的 prompt、definitions 和配置；网页显式选择与资产准备 | 一个 Runtime 进程同一时刻加载一个 Game Profile |
| 运行中配置 | 在同一进程切换游戏或模型；候选模型检查；旧实例退役、排空与新实例发布 | 切换取消当前回合，保留已写入历史；监听器和网页会话保持有效 |
| 本地控制台 | 中文像素界面、世界选择、只读任务列表与详情、状态筛选、首次配置、游戏与模型设置、运行状态和近期回合 | Task SQLite 为权威来源；尚无完整逐步 Trace 详情浏览 |
| 本地交付 | Windows x64 便携 Runtime 包，内嵌网页和游戏配置；Adapter 独立构建、安装与打包 | 游戏 Adapter 仍需单独安装，实际游戏执行受 Mod 和游戏环境约束 |

主要实现入口：[Runtime 装配](../../runtime/internal/bootstrap/bootstrap.go)、[Agent Loop](../../runtime/internal/agent/loop.go)、[Task 内核](../../runtime/internal/task/)、[Protocol](../../protocol/proto/gameagent.proto)。

## 3. 异步 Action、持久 Task 与长期行为

### 3.1 两种异步机制

| 机制 | 负责的事情 | 等待和恢复方式 |
| --- | --- | --- |
| 异步 Action | 一次具体动作，例如 NPC 从当前位置移动到目标点 | 当前回合等待动作终态；普通 Action 的原始等待上下文不跨 Runtime 进程恢复 |
| 持久 Task | 保存未来意图，在后续游戏时间继续处理，例如预约赴约 | SQLite 保存任务和唤醒；到期启动新回合；重连或重启后依据任务状态及游戏证据协调 |

Task 恢复不等于恢复崩溃前的同一个模型调用、goroutine 或 Action 等待。运行中动作结果不确定时，Task 先重新观察、协调证据；无法确认时暂停，避免盲目重发动作。

### 3.2 已接通的 Stardew Valley 预约链路

```text
玩家与 NPC 约定未来时间和地点
  → resolve_meeting 校验并返回 proposal_ref
  → create_task 持久化任务
  → 当前对话回合结束
  → 游戏时间到期，Task Dispatcher 唤醒 NPC
  → 新回合读取 Task Context 并选择动作
  → move_to_landmark 执行跨地图移动
  → wait_for_player 登记游戏侧等待并立即返回
  → Adapter 独立报告 met、expired 或中断事实
  → Runtime 收敛任务终态并记录结果
  → 释放或合法交接 NPC 控制权，恢复原生日程
```

任务终态能够在没有额外模型回合的情况下写入 History，后续对话可读取实际结果。任务创建成功、NPC 到达、玩家见面和模型口头承诺分别记录，互不替代。

### 3.3 已实现的保障

- SQLite 保存任务、状态、revision、下一次唤醒、operation、执行证据和终态结果。
- 身份、run、generation、时钟和版本检查限制过期请求及旧连接的影响。
- 唤醒和证据处理具有幂等与重复处理保护。
- 同一游戏 run 的 Runtime 重启按最新 working head 协调恢复；新的游戏 run 按存档携带的精确 checkpoint 引用恢复任务集合。
- 保存屏障与检查点桥接使任务工作集和游戏保存建立明确关联；引用缺失、损坏或未确认时暂停相关世界。
- 连续三次无进展或连续三次证据协调失败会暂停任务，并保留原因。
- 移动、等待和交互具有控制权管理及释放路径，失败和中断有明确结果。

这些机制保障状态一致性、恢复边界和失败收口。任务能否成功仍取决于模型选择、游戏可达性、时间窗口及环境持续可用性。

### 3.4 当前约束

- Stardew Valley 的仓库配置为 `task.enabled=true`；RimWorld 为 `false`。用户数据目录内已有配置可能与仓库默认值不同。
- 任务按游戏时钟推进，要求游戏世界在线、完成绑定并达到 task-ready；没有游戏关闭后的世界离线执行能力。
- 当前 Task 产品链路围绕已约定的会面，尚无任意任务类型的用户入口和完整游戏执行接入。
- 当前预约语义是“约定时刻开始出发”。例如约定 14:00，NPC 在 14:00 开始移动，抵达时间取决于路线。
- 两个游戏的发布配置均为每回合最多 5 步、1 个异步动作、270 秒回合超时和 190 秒异步动作超时。Stardew 预约移动另有 180 秒现实时间上限；Task 本身可跨越多个回合和游戏日。

依据：[任务配置](../../runtime/config/games/stardew-valley/agent.json)、[失败暂停规则](../../runtime/internal/task/attempt.go)、[重启恢复](../../runtime/internal/task/restart.go)、[检查点](../../runtime/internal/task/checkpoint.go)、[预约与恢复验收记录](../phase09/GameAgent%20MVP0%20Phase9%20开发与验收记录.md)。

## 4. 两个游戏的实际能力

### 4.1 Stardew Valley

| 能力 | 当前实现 |
| --- | --- |
| 角色对话 | 原生 NPC 对话、模型生成的玩家回复选项和可选自由输入 |
| 环境观察 | 时间、天气、场景、关系、日程、对话和附近 NPC 等游戏事实 |
| 表情与朝向 | `emote`、`face_player` |
| 同地图移动 | `move_to`，异步执行到当前地图的可达格子 |
| 靠近玩家 | `approach_player`，移动到动作开始时选定的玩家相邻格子；玩家移动后不持续追踪 |
| 预约解析 | `resolve_meeting`，校验未来日期、出发时刻、时间窗口和地标 |
| 跨地图赴约 | `move_to_landmark`，在有效 Task 内使用原生跨地图寻路 |
| 等待玩家 | `wait_for_player`，Adapter 按游戏时钟判断见面或过期 |
| 邮箱信件 | `send_mail`，现在生成并立即投递文本信件；Mail Framework Mod 为可选依赖 |
| 自动重连 | 断开或暂时准入失败后重试，重新进行协议启动和世界绑定 |

Mail Framework Mod 未安装时不发布 `send_mail`，Adapter 仍正常加载；防御性调用路径返回明确拒绝原因。邮件仅支持经过字符集和长度校验的文本，不开放附件、发物品或游戏命令。

跨地图路线存在覆盖限制：`beach_meeting_spot` 的受支持路线限定为 Linus 从 Mountain 出发，已有行程测量。另有 `community_center`、`pierre_store`、`trailer`、`mine_entrance` 和 `carpenter_shop` 五个点位接受任意 NPC、任意起点，但缺少逐路线测量，运行时仍可能因原生寻路或时间预算失败。

源码：[Stardew CapabilityCatalog](https://github.com/zlci7/world-is-agent-adapters/blob/ebc295485d6655a6bb5dab830633ca2aa6a69e41/stardew-valley/src/Runtime/CapabilityCatalog.cs)、[地标配置](https://github.com/zlci7/world-is-agent-adapters/blob/ebc295485d6655a6bb5dab830633ca2aa6a69e41/stardew-valley/assets/landmarks.json)。

### 4.2 RimWorld

- Windows x64、RimWorld 1.6 的真实 Mod，通过 `Grpc.Core` 与 Runtime 通信。
- 存档内持久世界 ID、基于游戏原生 ID 的 Pawn 身份，以及角色独立的上下文和历史。
- 有界的殖民者观察：特性、背景、技能、位置、需求、心情和健康。
- 殖民者交互入口、`present_dialogue` 和玩家回复驱动的下一回合。
- 自动重连和重新启动协议会话。

当前只发布对话能力；没有接入殖民者移动、工作指派、预约执行、邮件或持久 Task 游戏闭环。

源码：[RimWorld 能力发布](https://github.com/zlci7/world-is-agent-adapters/blob/ebc295485d6655a6bb5dab830633ca2aa6a69e41/rimworld/src/Runtime/ProtocolMapper.cs)。

## 5. 已实现的历史、摘要与检索

| 能力 | 当前状态 |
| --- | --- |
| 终态 History | 已实现；保存约定范围内的回合来源、模型决策、动作结果和终态，并按角色隔离 |
| 近期历史 | 已实现；以固定水位和有界分页读取进入上下文 |
| 累计摘要 | 已实现；来源覆盖校验、摘要检查点、近期原文尾部及失败降级 |
| 原文检索 | 已实现；中文相邻双字与拉丁/数字词项索引、有界查询、来源片段和上下文去重 |
| 任务结果历史 | 已实现；任务终态结果进入 History 和后续 Task Context |
| 原文留存清理 | 已实现；`retention_days=0` 默认关闭，按配置显式启用 |
| 游戏时间过滤 | 已实现；回档时隐藏可比较的未来来源和累计摘要 |

摘要和历史检索在代码默认配置中均启用。原文索引检索属于已有能力，向量数据库、embedding 语义检索及长期 Semantic Memory 抽取与纠错尚未实现。

Memory SQLite 独立于游戏存档。时间过滤可以隐藏当前时间线中的未来记录，但没有完整的分支隔离：游戏时间追上后，已放弃分支的经历可能再次可见。Task 的精确检查点恢复不等于 Memory 原文、摘要和检索数据库随存档整体回退。

摘要的来源和覆盖校验不能证明自然语言语义完全正确；模型延迟、摘要超时和语义误述仍是运行风险。

依据：[摘要默认配置](../../runtime/internal/agent/history_config.go)、[检索默认配置](../../runtime/internal/agent/retrieval_config.go)、[Phase8.2–8.3 验收记录](../phase08/GameAgent%20MVP0%20Phase8.2-8.3%20开发与验收记录.md)、[真实模型摘要记录](../phase08/GameAgent%20MVP0%20Phase8.2%20真实模型验收记录.md)。

## 6. 未实现或当前未支持的能力

| 能力 | 当前边界 |
| --- | --- |
| 任意长期任务产品入口 | 尚无通用的任意任务创建与游戏执行接入；当前预约链路不能视为所有任务类型均可执行 |
| 延迟写信与定时投递 | 当前没有 `schedule_mail`；`send_mail` 的效果是立即投递，不能承诺未来时刻重新写信或投递 |
| 自主生成目标 | NPC 不会持续自行产生新目标；Task wake 只继续已有任务 |
| 长时程自主规划 | 没有面向任意目标的持续规划、自动分解、重规划和完成保障 |
| 无人监督长期自主运行 | 没有长期稳定性、覆盖全部故障的恢复和自动完成保证 |
| 普通异步 Action 原地续跑 | Runtime 重启或断流后，不恢复原始回合的内存等待，也不自动重放中断的普通回合 |
| 完整 Memory 时间线分支 | 没有将所有记忆随游戏存档精确回退并永久隔离废弃分支的机制 |
| 长期语义记忆 | 没有 Semantic 抽取、纠错、向量存储或 embedding 检索 |
| 精确 Provider tokenizer | 上下文采用确定性估算 token 预算，没有覆盖各模型的精确分词器和自动窗口识别 |
| 同存档多实例并发写入保障 | 多连接是不同 EnvironmentSession，不代表可以安全并发操作同一游戏存档 |
| 完整存活检测与恢复产品机制 | 协议含 Heartbeat，尚无覆盖全部失联和恢复场景的产品化保证 |
| 完整网页执行详情 | 控制台展示近期回合摘要；完整模型请求、工具步骤和结果仍需读取 Trace |
| 跨平台发布覆盖 | 当前便携发布和两个 Adapter 的交付范围以 Windows 为主，未完成 macOS、Linux 同等发布验证 |
| 多 Agent 协作与自我改进 | 不在 MVP0 范围内 |

## 7. 已取得的验证与尚缺的证据

| 范围 | 已有证据 | 尚未覆盖或尚待确认 |
| --- | --- | --- |
| History、摘要和检索 | Phase8.2、8.3 均有 Accepted 记录；自动化、独立进程恢复、真实 DeepSeek 调用和时间可见性验证 | 技术验收使用受控世界；不能视为所有真实游戏存档和 NPC 场景均已验证 |
| Task 内核与 Runtime 接入 | SQLite、唤醒、世界绑定、执行证据、终态、恢复与检查点自动化 | 自动化中的 fake Adapter/model 不能替代真实游戏验证 |
| Stardew 预约赴约 | 有真实游戏、Runtime、SQLite 和模型的 `met`、`expired` 两条闭环 | 不代表全部 NPC、地标、路线和模型表现均可靠 |
| Task 保存与恢复 | Phase9.4 实现及相应自动化通过 | 保存后继续、同 run 断线与 Runtime 重启、新 run 重新读档、回退更早存档的完整实机验收仍有待办；Phase9.5 最终系统验收缺少完成记录 |
| Stardew 邮件 | 可选依赖、缺失依赖路径、投递、读取及模型自主选择有实机证据 | 人设与场景覆盖有限，完整采样和专门负向场景集未覆盖 |
| RimWorld 基础闭环 | 殖民者点击、真实模型选用对话工具、玩家回复、身份保存与加载有实机记录 | 非法角色范围和逐字段跨角色上下文对比没有完整实机记录 |
| 多游戏产品化与拆仓 | 10.4/10.5 自动化与独立交付验证通过，用户已报告对应实机基线通过 | 该基线不替代后续运行中切换功能的专项验收 |
| Runtime 0.2.0 Live Settings | 自动化、聚焦生命周期 race、浏览器配置与请求失败、便携包进程和会话保持验证 | 使用受控模型服务；运行中切换、Adapter 重连和模型替换的专项游戏实机验收仍待完成 |
| Phase11.1 开发验证 | 全量 Go 回归、跨进程恢复与精确回档、只读 API、Vue 类型/请求门控/构建、真实 HTTP 浏览器检查及便携包进程验证通过；Stardew TaskExecution 166 项、ProtocolMapper 186 项、RuntimeClient 26 项通过 | 使用隔离数据库与 fake model；四个核心包完整 race 检测通过，2 个符号链接权限相关测试跳过；实机组合恢复待验；见 [验收记录](phase11.1-acceptance.md) |

历史阶段中存在因 Windows C 工具链而未执行完整 race 的记录。Live Settings 的聚焦 race 通过只覆盖对应生命周期范围。

验收来源：[Phase8 记录](../phase08/GameAgent%20MVP0%20Phase8.2-8.3%20开发与验收记录.md)、[Phase9 记录](../phase09/GameAgent%20MVP0%20Phase9%20开发与验收记录.md)、[Phase10 总方案](../phase10/GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md)、[Live Settings 实机清单](../development/testing.md#live-settings-real-game-acceptance)。

## 8. 当前已知问题与使用限制

| 类别 | 当前事实 | 影响 |
| --- | --- | --- |
| 长期可靠性证据 | Task 恢复机制已有实现，完整实机恢复链路仍有验收缺口 | 目前不能作出长期无人值守稳定完成的承诺 |
| 模型执行 | 模型可能选错工具、生成无效参数或没有推进任务；连续失败会暂停 | 持久化保障任务记录，不保障模型每次完成任务 |
| 路线覆盖 | 五个通配点位缺少逐路线测量 | 跨地图移动可能无法寻路或在时间窗口、现实时间上限内到达 |
| 预约语义 | 约定时刻是出发时刻 | “两点见面”可能与玩家期望的“两点已抵达”产生差异 |
| 邮件能力描述 | `send_mail` 描述同时写有立即投递和玩家要求稍后收到回复的使用示例 | 描述存在歧义，实际执行始终立即投递；不能据此承诺延迟发送 |
| 交互距离 | 玩家离开有效对话范围会结束会话 | 待展示台词可能无法送达；任务结果与对话展示需要分别判断 |
| 历史与摘要 | 摘要可能超时或误述；Memory 缺少完整时间线分支隔离 | 历史可持久化和恢复不代表语义绝对正确，也不保证回档后的永久分支隔离 |
| 自动重连 | 会重复环境启动和世界绑定，不重放中断的普通回合 | 连接恢复不等于刚才的对话或动作自动续完 |
| 文件系统 | 首次准备缺失 Profile 资产依赖硬链接；同一数据根只允许一个 Runtime 进程使用 | 不支持硬链接的文件系统无法完成相关资产准备；同数据根不能并行启动多个写入实例 |
| 凭据保存 | API Key 保存在本地文件，网页状态不返回密钥；尚无系统密钥链接入 | 本地文件仍依赖用户环境的访问权限和管理 |

已实现机制、当前启用范围、自动化通过、真实模型验证和真实游戏验证分别成立；其中任一项都不自动扩大为其它任务、游戏、模型或运行环境的保证。
