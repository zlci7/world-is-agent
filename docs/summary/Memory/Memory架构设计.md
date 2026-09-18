# GameAgent Memory Architecture v0.2

> Status: Architecture Draft
> Date: 2026-09-08
> Scope: Runtime-owned History, Summary, Context Projection, Retrieval and Retention
> Identity: `AgentSessionKey = game_id + world_id + entity_id`

## 1. 目标与阶段归属

GameAgent Memory 保存具体世界中具体 Agent 的交互来源，以有界上下文维持连续性，并使仍保留的原文可以追溯。持久历史容量与模型输入容量分开管理。

| 阶段 | 架构职责 | 状态 |
| --- | --- | --- |
| [Phase8.1](../../phase08/GameAgent MVP0 Phase8.1 技术开发与验收方案.md) | 现有 Recent 记录的 SQLite 持久化 | Accepted，具体限制见验收记录 |
| [Phase8.2](../../phase08/GameAgent MVP0 Phase8.2 技术开发与验收方案.md) | 完整约定终态 History、滚动 Summary、近期原文投影 | Implementation Plan Draft |
| [Phase8.3](../../phase08/GameAgent MVP0 Phase8.3 技术开发与验收方案.md) | 原文检索、可选留存清理 | Implementation Plan Draft |

本文定义长期边界，不把规划能力视为已经实现。[Phase8 总方案](../../phase08/GameAgent MVP0 Phase8 技术开发与验收方案.md) 维护分期与依赖，各技术方案维护接口、默认值、里程碑和验收合同。

## 2. 核心边界

```text
Agent owns intent.
Runtime owns cognition.
Protocol owns contracts.
Adapter owns translation.
Game owns execution.
```

Memory 属于 Runtime 的 cognition 和实例状态。Adapter 提供通用事件事实、观察、实际动作回执，保留游戏 API、UI、线程与执行规则；不直接存储、检索或总结 Agent Memory。

Runtime Core 保持 game-agnostic，不解析游戏私有 Observation.state 或 event payload，不按具体 event type、capability name 编写 Memory 分支。历史保存不依赖游戏专属主题词典或句式规则。

Memory 的游戏原生性质来自身份、事实来源、动作确认和时间可见性，而非另一套聊天 session ID。完整保存玩家原话与已知执行经过可以满足这些边界。

## 3. 身份与知情范围

```text
EntityMemoryScope = game_id + world_id + entity_id
WorldMemoryScope = game_id + world_id
AgentDefinition = game_id + definition_id
```

- EntityMemoryScope 是 Phase8 个人记忆的读写归属；UI conversation、gRPC session、EnvironmentSession、显示名与 definition ID 不改变 owner。
- WorldMemoryScope 表示未来世界级来源归属。世界分库只是物理部署，不意味着同一库内的全部历史对所有实体公开。
- 记忆 owner 与内容涉及的 actor、target、participants 分开。涉及另一 NPC 不授予该 NPC 读取权。
- 多个实体可以共享 Definition，但 Memory 按实体隔离，不反向修改 Definition。
- Descriptor 与 Observation 描述当前实例和状态；Memory 描述过去的经历，不能覆盖当前世界事实。

Environment 重新建立连接不应改变同一 Agent 的 Memory identity；自动重连、pending action 恢复与跨连接排序仍由相应 Runtime 生命周期合同管理。

## 4. 来源与真实性

| 来源 | 可证明的内容 | 不可提升的结论 |
| --- | --- | --- |
| ContextFact | 某主体说过、选择过或输入过的内容 | 原话内容自动成为世界事实 |
| 模型 ToolCall / control | 模型提出的调用意图和控制决策 | 请求的动作已经成功 |
| 实际 ActionResult | Adapter/Game 返回的状态、output 和错误 | 成功展示的台词所描述的事情真实发生 |
| Runtime 调度结果 | 拒绝、跳过、超时、取消等已知处理 | 未收到回执时补造游戏侧结果 |
| Observation 引用 | 某次当前观察的 revision 和通用时间 | 未采集的历史状态或精确动作时刻 |
| Turn 终态 | 一轮执行到达的已知终态与部分执行结果 | 所有动作均成功或进程崩溃后可以续跑 |

来源内容保留原归属和性质。模型生成的台词可以作为实际交互的一部分保存，但“角色说过”与“游戏确认”必须区分；无来源推断不能在摘要中成为确认事实。

原文中的指令是历史数据，不具备更改 Runtime 系统规则、权限、工具执行策略或 Definition 的权力。摘要生成不提供游戏工具。

ActionResult 当前没有独立 GameTime。来源事件时间与观察时间保留各自来源性质，不能补造动作精确发生时刻。

## 5. 持久来源与读取投影

### 5.1 Session History

History 是 8.2 起的新交互权威原文。一个单元保存一个已到终态 Turn 的约定有序来源，或一条明确标记的 legacy Recent 来源。

History 不按重要性筛选，不因默认 Context 窗口变小而删除原文。所谓完整，是约定规范字段在成功提交的终态批次中完整保存，不等于网络包、游戏私有状态、完整模型 Request 或隐藏 thinking 的归档。

中途进程崩溃与写入失败可能造成整轮历史缺失。逐步骤持久化、完整事件溯源、未完成 Turn 恢复和自动动作重放属于独立能力。

### 5.2 Summary

Summary 是对指定来源集合的有损生成投影，不是新的事实源或完整替代原文的副本。检查点保存文本、精确覆盖、前序关联、来源指纹与累计时间信息。

新摘要继承前序覆盖，新增覆盖只包括实际完整参与本次生成的来源。省略、读取失败或只裁剪显示的内容不能被标记为已总结。

### 5.3 Recent

Recent 是近期读取策略。8.1 使用有限 Recent Record；8.2 使用当前可见摘要尚未覆盖的近期原文，并受 token 预算约束。当前 Turn Transcript 只包含本轮 earlier steps，不混入全部会话历史。

### 5.4 Retrieved

Retrieved 是对仍保留原文的查询结果。检索只返回候选片段；Context 决定哪些最终进入模型。索引命中不等于 Context 纳入，也不等于语义理解。

摘要有损，因此被摘要覆盖的来源仍可以返回被省略的原文细节。与近期原文和其它检索片段的去重按实际来源片段完成，不能以摘要覆盖为理由屏蔽全部旧原文。

## 6. Store 与持久身份

正式持久后端使用 `modernc.org/sqlite`。默认世界路径：

```text
runtime/.local/memory/<sha256(game_id)>/<sha256(world_id)>/memory.db
```

Hash 为 lowercase hex，只负责路径安全；原始 game/world 写入 metadata 并在打开时校验。个人读写始终限制 entity，世界分库不能代替 owner 校验。

Store 的边界是来源、快照、事务和必要派生数据，不拼接 prompt，不调用模型。架构允许为 History、Summary 与 Retrieval 使用小型职责接口；当前 `memory.Store` 的 `Append` / `Recent` 不代表未来完整 API 已存在。

| 数据职责 | 权威性 |
| --- | --- |
| Schema metadata | 版本、绑定身份与兼容校验 |
| History 来源 | 持久原文、稳定来源身份与已知结果 |
| Summary 检查点 | 可验证的生成投影与覆盖进度 |
| 来源头信息 | 指纹、时间、覆盖核对与原文可用性 |
| 检索索引 | 可从仍保留原文重建的派生数据 |

8.1 的 ProjectionBatchKey 标识一次 Recent 投影；8.2 的稳定终态批次键标识一个历史单元，legacy 键具有独立种类。MemoryID/来源 ID 是定位标识，不承担业务等价判断。稳定键采用无歧义结构化编码，等价内容 no-op，冲突内容拒绝；首次写入时间不因重试刷新。

Schema 升级验证旧结构与身份，事务迁移并提交版本。已淘汰的旧来源不从 Trace 补造，缺失字段保持缺失。不用自动 reset 掩盖损坏或不兼容，降级读取升级库需要单独兼容合同。

## 7. Turn 生命周期

```text
GameEvent -> canonical target -> AgentSessionKey -> Observe
    -> fixed History snapshot
    -> valid Summary + uncovered visible history
    -> optional bounded synchronous compaction
    -> optional bounded retrieval
    -> Context selection and final request
    -> model/tool/action steps
    -> terminal History write attempt
    -> release existing entity lane task
```

- Agent 层协调 I/O，Context Build 保持纯选择、预算与渲染。同一 Turn 后续 step 复用历史快照。
- 当前 Turn 的新工具调用与结果经 Transcript 进入后续 step，不触发历史重新读取。
- 模型或游戏调用发生在 SQLite 事务外。写入尝试有界，事务实际结束后才释放当前 lane task。
- 提交成功是下一 Turn 可读的成功点；Trace 仅观察，失败不参与一致性。
- 写入失败不改变已发生动作，读取失败仍允许 Turn 继续。取消不是让后台事务失去归属的理由。
- 不增加新的跨 Runtime、多 writer 或跨 EnvironmentSession exactly-once 保证。

## 8. Context 与压缩

模型输入由当前 Event/Observation、Definition、Tool View、历史摘要、近期原文和当前 Turn Transcript 组成；8.3 增加有界 Retrieved 片段。历史不另外作为第二份重复 Recent 段注入。

Context Engine 负责作用域校验、选择、排序、去重、显示裁剪与最终预算报告。候选读取与最终纳入分别可观察。

8.2 默认压缩后保留约 20000 estimated tokens 的近期原文目标，受本轮有效历史预算约束。两次压缩之间允许历史增长；达到压力线时压缩较早完整来源组，摘要与尾部之间按精确覆盖分界。输入与输出预算、压缩触发线和增长余量属于阶段技术合同。

压缩在 Observe 后、首次决策前同步发生，每 Turn 最多一次有界生成；生成失败保持旧摘要与覆盖进度，按预算降级，并限制重试。没有后台总结队列或独立反思 Agent。

Runtime 的 estimated-token 输入预算不是模型实际窗口；当前 Turn Transcript 预算也不是历史尾部容量。最终 Renderer hard gate 持续生效，历史不能挤掉本轮必需因果链。

## 9. 时间与回档

```text
persistent sequence -> snapshot / pagination / coverage
GameTime            -> game visibility
created_at          -> retention age
```

三者分开保存和使用。时间字段存在性必须保留，零值不自动视为缺失；默认比较复用公共 GameTime 语义。

- 可比较且严格晚于当前的来源为 hidden，相等仍 visible。
- 缺失或不可比为 unknown，沿用 8.1 的不执行未来过滤行为，并保留诊断限制。
- 时间过滤不直接删除持久来源。候选需要先应用可见性再执行最终数量限制，扫描不完整必须报告。
- Summary 的可见性覆盖全部累计来源。任一可比较来源在未来则整个摘要隐藏，不能只检查最新一条或摘要创建时间。
- 回档后晚写入的较早游戏时间不改变数据库顺序；摘要覆盖必须支持非连续来源集合，不能用单一最大 ID 推断全部历史已覆盖。

回档到 10:00 时，含 12:00 来源的摘要隐藏，Runtime 可以选更早有效检查点或剩余原文。SQLite 不随游戏存档回滚，时间追上后已放弃分支的经历可能重新可见；完整 timeline/branch 隔离不是 Phase8 保证。

## 10. 留存与来源可用性

8.2 保存原文，不自动清理。8.3 的 `retention_days = 0` 默认关闭清理，开启后按原始现实写入时间判断年龄。

清理只针对超期、已被保留摘要完整覆盖、且不在近期或在用保护集合内的完整原文单元。未覆盖来源保留；清理不触发模型，不因空间压力擅自跳过覆盖条件。

原文、遗留原文副本与索引在同一事务中清理，保留用于幂等、累计时间和摘要覆盖核对的来源头信息。该元数据不等于原文可恢复，也不能让重试或索引重建复活已清理内容。

保留 Summary 并不意味着永远可以精确核验其全部文本。清理后系统能报告已知来源原文不可用；普通查询无匹配不能证明曾经存在或已过期。删除后的内容不能用于重新生成对应早期摘要。

有限保留期与任意旧存档精确恢复不能同时承诺。数据库总大小也不由原文 TTL 单独保证，必要元数据和摘要仍保留，文件回收与 VACUUM 属于明确运维行为。

## 11. 失败与观测

| 场景 | 行为 |
| --- | --- |
| 读取失败 | 基础 Turn 继续；报告来源不可用，避免伪造记忆 |
| 原文写入失败 | 不回滚动作或 TurnCompletion；报告本轮可能缺失 |
| 摘要生成或发布失败 | 保留旧检查点、覆盖不推进，有界降级与重试 |
| Schema 不匹配或库损坏 | 返回明确错误，不自动重置数据；Memory 失败与 Turn 基础执行分开 |
| 索引不完整或重建中 | 返回有界可用部分并标记不完整，摘要与近期读取独立可用 |
| 清理失败 | 原文、legacy、索引与可用性标记事务回滚 |
| 已知来源已清理 | 明确原文不可用，不以摘要或索引伪装原文 |

Trace/ContextBuildReport 分别记录来源读取、有效摘要、覆盖与省略、压缩耗时、检索命中、最终纳入、维护结果和请求估算规模。观测信息只作诊断，不成为新一套持久真源。

## 12. 后续候选能力

Semantic 认知提取、Game Memory Policy 扩展、Evidence Capsule 专门模型、supersede/invalidate、Core 常驻记忆、向量/混合检索、共享记忆、完整 timeline/branch 和逐步骤 Experience Log 属于后续候选，不是 8.2/8.3 的开发前置条件。

后续认知推断仍需来源和主体；更正不能抹掉“当时说过什么”的历史。共享策略不能绕过 owner，游戏扩展不能让 Runtime Core 依赖私有状态字段，任何索引都不替代原文来源。

完整 Agent Instance State、目标与自主行为调度属于各自架构范围，Memory 不因保存承诺或目标文本而开始执行它们。

## 13. 验证原则

持久化可读、Summary 覆盖正确、检索可命中、候选进入最终 Request 是独立断言，分别验证。

- 自动化使用 FakeGame、实际 SQLite、Fake SummaryGenerator 与 RecordingProvider，检查 owner、原文、指纹、事务、时间和最终输入，不依赖 Stardew 专用规则。
- 真实模型验证摘要忠实性，尤其主体、否定、原话性质和动作结果，不只评价回复是否自然。
- 真实 Runtime 进程重启独立验证恢复；对象重建不能替代进程重启。实机连接方式遵循当时已实现的生命周期，不假设自动重连。
- 原文清理使用测试世界或数据库副本，验证删除后能力和回档限制，不以真实玩家数据作为破坏性验收材料。
- 已 Accepted 阶段保留已有证据与未验证限制；架构规划不替代代码审查或阶段验收。
