# Stardew NPC 与世界设定实施方案

全量内容、目标协作与委托试点的范围及验收按 [Phase11.15 技术方案](phase11.15-npc-experience-and-contextual-objectives.md) 执行；本文件维护角色内容、身份与进度字段规范。

状态：共用 Game Definition 与 34 名普通 NPC 内容使用 `stardew-content-v1`。静态加载、4096 估算 tokens 下的完整投影与不同世界的状态输入检查通过。Adapter 源码已接入精确关系身份、首批世界进度和选定角色事件；真实模型与实机对话待验。范围与证据见 [验收记录](phase11.15-acceptance.md)。

范围：按当前支持游戏版本的原版 NPC 名单完成全量内容覆盖。所有可交互角色使用一致的内容质量要求，并核对晚期角色与特殊角色的实际入口。目标为角色身份和中文表达稳定、亲疏符合当前关系、世界反馈符合当前存档、共同经历有实际依据。当前版本实机闭环与本方案分别验收；服务器接入仍按 [11.2 方案](phase11.2-hosted-runtime-and-launch.md) 执行。

## 1. 当前实现与参考依据

WIA 有 34 名普通 NPC 的独立中文 definition，包含身份、性格、语气示例、偏好及分情境的行为要求。Runtime 按 `game_id + definition_id` 查找角色，将其与 Game Definition、当前 Observation、历史和 Task Context 一起提供给模型。特殊角色入口与事实来源见 [游戏事实与角色覆盖](phase11.15-game-facts.md)。

`game.json` 包含季节与日常生活、公共地点、设施用途、公共人物关系，以及当前事实、历史与未知状态的使用规则。Adapter 提供时间、天气、位置、好感、精确关系、日程、当前对话、首批世界设施状态与当前角色的选定事件。

| 参考材料 | 已有结构或机制 | 本方案采用的内容 |
| --- | --- | --- |
| ValleyTalk 的 [Abigail bio](../../../references/ValleyTalk-master/ContentPack/assets/bio/Abigail.json) 与其他 bio | `Biography`、`Relationships`、`Traits`、`Preoccupations`、`Dialogue` | 稳定背景、人物关系、关心的话题和角色表达的内容组织 |
| ValleyTalk 的 [GameSummary](../../../references/ValleyTalk-master/ContentPack/assets/GameSummary.json) | 季节、地点、节日、村民概览 | 精简的公共世界知识，保留与实际对话相关的内容 |
| ValleyTalk 的 [Character](../../../references/ValleyTalk-master/src/Character.cs) 与 [Prompts](../../../references/ValleyTalk-master/src/Prompts.cs) | 按季节、日期、好感选择对话样本；结合关系状态、近期事件和世界进度组织上下文 | 关系与剧情分别判断，静态设定与当前事实分开 |
| StarDojo 的 [GetProgressionData](../../../references/stardojo-main/StardojoMod/actions/Actions.cs) | 建设、修复、社区中心、Joja、矿层和任务等游戏状态读取 | 从当前游戏读取少量可验证进度事实 |
| StarDojo 的 [任务样本](../../../references/stardojo-main/env/tasks/task_suite/social_lite.yaml) 与 [游戏数据目录](../../../references/stardojo-main/env/game_data) | 带前置条件的场景；物品、商店、配方数据 | 构造前后状态不同的验收场景；按需核对知识 |

参考项目属于内容与实现参考。具体年龄、早年经历、家庭细节和剧情结论须核对当前支持版本的游戏资料后采用。ValleyTalk 传记中的扩写不能直接视为游戏事实，其近期对话事件表也不能直接作为长期建设状态的权威来源。StarDojo 的未来邮件判断和部分近似进度值，不能直接作为设施当前可用的依据。

首批内容的人物关系与兴趣同时参考 Stardew Valley Wiki 的 [Abigail](https://stardewvalleywiki.com/Abigail)、[Robin](https://stardewvalleywiki.com/Robin) 和 [Linus](https://stardewvalleywiki.com/Linus) 条目。中文语气示例为原创表达，不是原作台词或存档历史。

全量覆盖以 [Villagers 名单](https://stardewvalleywiki.com/Villagers) 与实际支持版本的游戏实体核对。普通可建立好感关系的 34 名角色包括 Leo，当前 33 份 NPC 文件不能作为完整名单。Birdie、Gunther、Marlon、Mr. Qi 等特殊角色同时登记内容与入口状态；独立 definition 的 ID 按实际游戏实体核验，已有资料不等于已经开放交互。扩展 Mod 角色按对应 Mod 的资料和入口单独纳入。

## 2. 数据与职责

| 数据 | 所属位置 | 内容边界 |
| --- | --- | --- |
| Game Definition | Runtime 的 `definitions/game.json` | 稳定的公共世界知识、知识使用规则 |
| NPC Definition | Runtime 的各角色 `npc-*.json` | 身份、稳定关系、性格、中文语气、基于当前事实的表达规则 |
| 当前进度 | Adapter → `Observation.state.stardew` | 当前玩家与当前 NPC 的关系、选定事件状态、当前世界设施状态 |
| 历史与预约 | 现有 Memory、Task Context | 已记录对话、预约事实和实际执行结果 |

Game Profile、prompt 和 definitions 继续由 Runtime 发布。Adapter 负责读取和解释游戏事实，不组装最终模型 prompt。Runtime Core 继续通用投影 Observation，不读取 Stardew 私有字段、不判断剧情阶段。

沿用 definition 的现有字段与 `v1alpha1` 格式，保留已有角色标识；新增角色的 `definition_id` 与实际 Adapter 上报核对。进度采用 Stardew namespaced Observation 的可选字段扩充，不修改 Protobuf 契约。

### 2.1 模板与存档实例的标识

| 对象 | 标识范围 | 当前实现 |
| --- | --- | --- |
| Game Definition | `game_id` | 所有存档共用的游戏背景 |
| NPC Definition | `game_id + definition_id` | 所有存档可共用的角色模板 |
| NPC 实例与 Agent Instance Descriptor | `game_id + world_id + entity_id` | 某个世界里的具体 NPC，引用其模板 |
| 当前 NPC 状态 | 当前世界、当前实体的 Observation | Adapter 从当前游戏读取位置、日程和当前玩家的好感与关系 |
| NPC 历史与 Memory | `game_id + world_id + entity_id` | 该世界里该 NPC 已记录的历史 |

例如，世界 A 的 `npc:Abigail` 可以与当前玩家为 0 心，世界 B 的同名实体可以为 8 心。两者使用相同的 `npc:Abigail` 模板，各自获得当前世界的 Observation 与 Memory。好感由游戏保存，Runtime 在每轮读取；不会复制为一份按世界维护的 definition，也不会由模板保存当前心数。

当前 Stardew Adapter 使用存档目录名作为 `world_id`。同一 Runtime 数据根下，世界标识相同会进入相同的世界命名空间；该标识不是托管用户身份。同存档多玩家的独立记忆也不由上述三元键保证，托管服务的数据归属与隔离按 11.2 单独设计。

## 3. 进度按独立维度判断

年份、关系、剧情和世界解锁分别表达。测试时可用“初识”“熟悉”“世界建设后”等场景名称，运行时以实际事实为准。

| 维度 | 事实依据 | 对话要求 |
| --- | --- | --- |
| 时间 | 已有年份、季节、日期、天气 | 使用当前季节与地点；年份不推导建设或剧情完成 |
| 关系亲疏 | 已有 `known`、好感点数与 hearts | 影响称呼、开放程度和话题深度；未读取到关系时保持自然、克制 |
| 关系身份 | 游戏实际友情、恋爱、订婚、婚姻、离婚或室友状态 | 高好感不推导恋爱；婚姻和离婚按当前事实承接 |
| 角色事件 | 当前玩家对当前 NPC 的少量已见事件标记 | 未见事件不作为共同经历；已见标记不推导玩家当时的选项 |
| 世界变化 | 社区中心、Joja 会员、公交、矿车、温室 | 区分尚未完成、等待生效、当前可用和未知 |
| 角色可见与交互条件 | 当前角色是否存在、位置、相关地区和特殊交互条件 | 角色实际出现与交互资格由游戏和 Adapter 决定，人设文件不开放原本未满足条件的内容 |

同一存档可以同时具有高好感、未完成建设、未见某个角色事件。NPC 的回答须兼容这类组合。

### 3.1 首批进度字段

首批 Adapter 数据约定如下，既有字段保持兼容：

| 字段 | 取值与来源要求 |
| --- | --- |
| `relationship.status` | 从当前 `Friendship` 对象读取精确状态；读取不到为 `unknown`。与既有 `is_spouse`、`is_roommate` 从同一次快照投影并校验一致性 |
| `world_progress.community_center` | `incomplete / complete / unknown`，完成依据须在当前支持版本核验 |
| `world_progress.joja_membership` | `yes / no / unknown`，与社区中心完成状态分别报告 |
| `world_progress.bus` | `locked / pending / available / unknown` |
| `world_progress.minecarts` | `locked / pending / available / unknown` |
| `world_progress.greenhouse` | `locked / pending / available / unknown` |
| `npc_progress.key_events` | 最多三个当前 NPC 的选定事件，每项包含核验后的 `event_id` 与 `seen / not_seen / unknown` 状态 |

世界设施字段读取实际可用状态；确认已经安排修复而尚未生效时为 `pending`。单独一条待收邮件不构成 `available`。准确的 API、标记与生效时点需在 Adapter 实现前形成小型映射表，并覆盖“安排当日、次日生效、读档”三个边界。

各角色只选择确会改变对话承接的关键事件；没有适用事件的角色不填充虚构阶段。映射表记录游戏版本、事件 ID、判定来源和允许承接的范围；只投影当前角色的有限事实，不上传完整事件、邮件、任务或存档数据。没有经过核验的映射保持未知。

关系和事件使用当前交互玩家的状态，世界建设使用实际世界状态。事实在游戏主线程构建，读取失败独立降为未知，不把某一字段失败变成整次对话失败。

### 3.2 新鲜度与知识边界

- 每个新 Turn 使用现有 Observe 链路读取当前事实；异步动作恢复后继续使用现有重新 Observe 机制。
- 读档、回档、切换世界后重新读取进度，不保留跨存档的阶段缓存。
- 当前 Observation 是当前事实来源；Memory 保留历史语境，不覆盖当前婚姻、设施或事件状态。
- 缺字段、旧 Adapter、读取失败或上下文被裁剪时均按未知处理，未知不解释为未完成。
- 已解锁地点不代表 NPC 已去过；玩家已见事件不代表所有 NPC 知道；高好感不代表共享了未记录的经历。
- 当前事件标记只能支持确认对应事件已见。涉及选项、当时对话或玩家承诺时，须有明确记录才能引用。

首批静态内容只写公共事实和表达规则，不收录完整未见剧情脚本。剧情阶段主要影响开放程度，以及能否承接已确认的共同经历。模型表达约束需要真实模型验收，不能作为绝对的防剧透保证。

## 4. Game Definition 内容

`game.json` 按以下内容重写，保留现有字段结构：

| 字段 | 首批内容 |
| --- | --- |
| `summary` | 星露谷与鹈鹕镇的简洁背景，玩家农场主身份的公共前提 |
| `world_rules` | 季节与日期、日常生活、世界变化由进度确认、游戏角色行动受当前 Capability 和结果约束 |
| `lore` | 全镇地点、职业与公共关系；季节生活、常见节日和建设设施的一般用途；未发现地区与人物的知识边界 |
| `narrative_constraints` | 当前事实优先；未知时保留判断；不把未发生的剧情、未成功的动作或未记录的承诺说成事实 |
| `source_version` | 独立的内容版本标识，供发布与加载核对 |

设施的一般用途属于静态知识；该存档现在是否修好属于 Observation。节日的一般信息属于静态知识；玩家是否参加、是否获胜属于已确认的游戏事实或历史。

世界知识覆盖普通生活、地区变化及不同角色参与的进度线。与具体解锁相关的内容按当前事实承接，涉及晚期角色时核对出现、居住和关系变化。完整物品百科、价格表与全部配方不整份注入每个角色的上下文。

## 5. NPC Definition 内容

全体角色使用一致的编写要求，具体表达保持各自特点：

| 现有字段 | 编写要求 |
| --- | --- |
| `identity` | 清楚交代职业、生活背景和经核验的稳定人物关系；居住与婚姻等可变状态采用条件表达 |
| `personality` | 写性格如何影响反应，兼顾兴趣、顾虑、价值取向与表达分寸 |
| `speech_style` | 中文句式、语气、节奏，以及少量原创中文表达示例；示例只示范声音，不构成对话历史 |
| `preferences` | 经核验的喜好和自然话题，不把个人偏好当作物品赠送指令 |
| `behavior_guidelines` | 对初识、熟悉、已确认关系和事件的表达要求；角色擅长讨论与提供帮助的领域；未知事实的处理；实际能力与结果的承接 |

### 5.1 角色覆盖与差异

| 角色组 | 普通角色 | 内容重点 |
| --- | --- | --- |
| 行业与日常生活 | Robin、Marnie、Pierre、Gus、Clint、Willy、Harvey | 专业视角、日常服务、工作与生活的区别，不凭职业身份宣称已执行游戏动作 |
| 家庭与照顾 | Demetrius、Caroline、Jodi、Kent、Pam、Penny | 家庭关系、照顾方式、个人顾虑、角色出现与生活变化 |
| 兴趣与个人发展 | Abigail、Alex、Elliott、Emily、Haley、Leah、Maru、Sam、Sebastian、Shane | 价值取向、不同中文声音、亲疏变化、个人剧情与恋爱身份分别判断 |
| 社区与长者 | Evelyn、George、Lewis、Linus | 社区视角、生活经验、表达界限，避免全知或统一说教 |
| 儿童与成长 | Jas、Vincent、Leo | 年龄相称的表达、监护与朋友关系、当前居住和熟悉程度 |
| 远方与特殊生活 | Sandy、Wizard、Dwarf、Krobus | 地区、文化与身份差异，尊重发现顺序、交互条件和知识边界 |

特殊角色维护可核查的内容清单，分别记录实际实体 ID、出现条件和现有交互入口。未具备入口的角色不能以添加 JSON 文件作为“已经可对话”的验收依据；需要新增入口时单独界定游戏侧改动。

### 5.2 阶段表达要求

- 初识时保持角色原有的热情、戒备或疏离，不统一写成友善接待员；熟悉后增加与角色一致的信任与话题深度。
- 成年可恋爱角色、不可恋爱角色与儿童分别使用适当的关系表达。高好感不自动转换成恋爱或无条件服从。
- 角色出现、搬迁、职业或家庭生活变化有当前事实才承接。年份与心数不代替关键事件和分支选择。
- 专业知识贴合角色本身；同一件建设或生活事件可以有不同立场，玩家的私事只由有记录依据的角色承接。
- 中文语气示例体现角色表达，不伪造既往对话，也不规定所有情境必须套用同一句台词。
- 人设中的善意、兴趣与帮助意愿使用实际可用能力表达，不创造未经实现的委托、奖励或游戏副作用。


## 6. 上下文体积与兼容

每轮只加载当前 NPC 的 definition 与共用 Game Definition，角色文件数量不增加单轮加载角色的数量。任一角色与 Game Definition 合计不超过 4096 个现有估算 tokens；新增进度事实目标不超过 512 个估算 tokens。实际以现有 Context Engine 的报告和整轮预算为准，默认 Definition 与 Observation 上限均为 8192。

现有定义裁剪顺序中，行为要求和世界约束可能晚于其他内容保留。因此内容验收必须检查完整模型请求，在支持的配置下确认角色边界与当前进度仍可见；不通过单纯提高全局预算解决内容膨胀。

旧 Adapter 缺少新字段时，角色使用公共设定与现有关系事实进行对话，对缺失进度保持未知。namespaced 字段扩充不改变 Runtime 的跨游戏行为。

新数据根由现有机制准备内容。已有数据根会保留已存在的 definition；首次验收使用隔离数据根。已有用户更新时按内容清单备份并替换官方 definition 后重启 Runtime，保留模型配置、自定义文件、Memory 和 Task 数据。本轮不增加内容热更新或自动覆盖流程。

纯 definition 更新需要重新构建 Runtime 发布包；进度读取更新还需要构建并安装 Stardew Adapter。两项分别验证，不能把现有 Mod 自动视为已经具备新增进度能力。

## 7. 工作单元与验收

| 顺序 | 工作单元 | 完成标准 |
| --- | --- | --- |
| 1 | 全量名单、内容事实与进度来源核对 | 当前支持版本的原版角色覆盖可核查，含 Leo 与特殊角色入口状态；人物关系、背景和进度判定有来源 |
| 2 | Game Definition 与全量 NPC 内容 | 所有可交互角色沿用现有格式加载；中文表达有辨识度；公共事实与可变状态分开；每个角色均在内容预算内 |
| 3 | Adapter 当前进度事实 | 游戏线程读取；关系、事件、设施前后状态正确；缺失与失败为未知；重载不沿用旧存档进度 |
| 4 | 模型场景与实机体验 | 同题不同角色、关系变化、世界变化、剧情承接、记忆与预约结果均满足下表；自动测试与真实模型、实机结果分别记录 |

实现涉及的主仓库文件为 `runtime/config/games/stardew-valley/definitions/game.json` 与全量角色 JSON，以及受影响的 definition、Context 和配置准备测试。Adapter 涉及 `State/ObservationBuilder.cs`、Observation 模型与 Factory、`Runtime/ProtocolMapper.Core.cs` 及对应测试。确有需要时增加一个自足的进度读取辅助文件，不建立通用阶段框架。新增交互入口与玩法能力分别明确范围，不以内容更新代替其实现。

### 7.1 自动验证

- definition 的格式、ID、内容版本和加载链路正确；名单逐角色核对，所有角色保持独立内容，新增文件与实际上报 ID 一致。
- 使用通用 fake Observation 验证 namespaced 新字段可进入模型请求，Runtime 不依赖 Stardew 字段语义。
- 覆盖关系身份与既有布尔值的一致性、未知数据、设施待生效与可用状态、事件未见与已见状态。
- 覆盖不同玩家、不同世界和回档前后的状态读取；保留游戏版本对应的映射依据。
- 覆盖新数据根内容、已有内容保留、旧 Adapter 缺字段，以及预算下的完整请求。
- 运行受影响的 Runtime / Adapter 测试、架构静态检查和 `git diff --check`。

### 7.2 模型与实机场景

每名可交互 NPC 覆盖适用的下列八类试聊场景，并保留逐角色结果。需要比较前后状态的场景分别保留两份输入与回答；不适用的婚姻、事件和预约场景明确标注，不能用其它角色的通过结果代替。固定模型和配置，保存当前事实、实际回答和判定。

| 场景 | 验收要求 |
| --- | --- |
| 初识与熟悉 | 态度随关系变化，身份与性格稳定，不凭高好感宣称恋爱 |
| 家庭、朋友与职业 | 人物关系准确；不补写未核验的生平细节 |
| 季节、天气与地点 | 当前事实正确，表达体现角色关心的事情 |
| 设施尚未修复、待生效与可用 | 同题回答匹配当前状态，不把已安排当作已可用 |
| 角色事件未见与已见 | 不提前声称共同经历；不从已见标记推导玩家选项 |
| 玩家诱导错误事实 | 不因玩家暗示就确认不存在的婚姻、设施解锁或共同回忆 |
| 记忆、预约成功与失败 | 能承接有记录的信息，措辞符合实际 Task 与 Action 结果 |
| 字段未知、重启或回档 | 承接当前事实，缺信息时自然保留判断，不把旧进度当作当前状态 |

连续交流按上述角色类型选代表覆盖约 20 轮，并跨一个游戏日、一次保存加载和一次 Runtime 重启检查重复、语气漂移与上下文承接。普通角色的基础试聊保持全量覆盖，晚期与特殊角色分别核对实际出现和入口。实机使用测试存档副本。

人物关系错误、当前设施状态说反、虚构共同经历、把失败任务说成完成属于阻断问题。表达自然度与角色辨识度由真实回答评审；每名角色的适用场景均需满足预期，所有阻断问题修正后再进入连续游玩验收。自动测试不替代真实模型和实机结论。
