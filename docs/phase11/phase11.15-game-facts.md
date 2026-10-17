# Phase11.15 游戏事实与角色覆盖

核验版本：Stardew Valley `1.6.15`、SMAPI `4.3.2`、Stardew Adapter 源码 `0.1.2`。静态内容版本为 `stardew-content-v1`，definition 格式保持 `v1alpha1`。

## 角色模板

34 名普通可建立好感关系的角色均有独立中文模板。共享背景与当前角色合计按现有估算器执行 4096 token 预算检查；当前存档的好感、关系和历史分别来自 Observation 与 Memory。

| 角色组 | 实际内部名称 |
| --- | --- |
| 行业与日常 | Robin、Marnie、Pierre、Gus、Clint、Willy、Harvey |
| 家庭与照顾 | Demetrius、Caroline、Jodi、Kent、Pam、Penny |
| 兴趣与个人发展 | Abigail、Alex、Elliott、Emily、Haley、Leah、Maru、Sam、Sebastian、Shane |
| 社区与长者 | Evelyn、George、Lewis、Linus |
| 儿童与成长 | Jas、Vincent、Leo |
| 远方与特殊生活 | Sandy、Wizard、Dwarf、Krobus |

身份与家庭关系核对 `Data/Characters` 的 `FriendsAndFamily`、`Home`、`UnlockConditions` 等数据。家庭背景不固定当前住处。Kent 的原版出现条件为第二年；Leo 的出现与住处有独立解锁条件；Sandy 的社交资格包含已见事件条件；Dwarf 的中文交互保留游戏的语言前置。

人物声音、帮助方式和语气示例为原创中文内容。参考项目用于内容结构对照，不将其中未经核验的年龄、生平与身世当作事实。贾斯与玛妮、谢恩的关系另核对 [Jas 资料](https://stardewvalleywiki.com/Jas)。

## 特殊角色入口

`Data/Characters` 还含以下特殊实体；模板文件和社交资格都不能单独证明当前场景存在可点击入口。现有探测器检查 `NPC.IsVillager`，该属性不等于可建立好感关系。

| 内部名称 | 数据中的位置或条件 | 本轮支持状态 |
| --- | --- | --- |
| Marlon、Gil | AdventureGuild | 保留现有入口，独立角色内容及实机入口未验收 |
| Gunther | ArchaeologyHouse | 同上 |
| Morris | JojaMart | 同上；世界路线影响实际出现 |
| Birdie | IslandWest | 同上；原生故事交付仍由游戏处理 |
| Bouncer | SandyHouse；有消失条件 | 同上 |
| Henchman | WitchSwamp；有消失条件 | 同上 |
| Mister Qi | Club / QiNutRoom | 同上；内部名称包含空格 |
| Old Mariner | Beach | 同上；出现和售卖条件须实机核对 |
| Governor、Grandpa、Bear、???、Welwick | 活动、脚本或特殊场景 | 未建立通用 AI 交互支持结论 |

活动演员、其它专用类型与扩展 Mod 角色不以这份资产名单推导完整入口覆盖。当前交付的内容覆盖结论限定为上表之前的 34 名普通角色。

## 当前进度来源

| Observation 字段 | 1.6.15 来源与语义 |
| --- | --- |
| `relationship.status` | 当前交互玩家的 `Friendship.Status`；室友优先使用 `IsRoommate()`。取值 friendly、dating、engaged、married、divorced、roommate、unknown |
| `world_progress.community_center` | `Game1.MasterPlayer.hasCompletedCommunityCenter()`；判定六个房间的原生完成标记，不代表庆典已见 |
| `world_progress.joja_membership` | 房主 `mailReceived` 中的 `JojaMember`；尚未生效的会员安排不当作已生效 |
| `world_progress.bus` | 原生 BusStop 使用房主已收到的 `ccVault` 判断解锁；已安排相关修复而未生效为 pending |
| `world_progress.minecarts` | 房主已收到的 `ccBoilerRoom` 判断解锁；相关待生效修复为 pending |
| `world_progress.greenhouse` | `Game1.getFarm().greenhouseUnlocked` 判断可用；相关修复安排为 pending |
| `npc_progress.language_understood` | 仅 Dwarf 使用当前玩家 `canUnderstandDwarves`；其它角色为 null |

设施取值为 locked、pending、available、unknown。待生效判断使用相关 `hasOrWillReceiveMail`；该方法的结果不单独证明设施可用。公交解锁不等于潘姆当前在岗或玩家能够立即乘车。单字段读取失败为 unknown。

## 选定角色事件

事件 ID 核对游戏 `Data/Events/<地点>`。每轮只投影当前 NPC 的最多三个选定标记，读取当前玩家 `eventsSeen`。标记只支持 seen、not_seen、unknown，不包含玩家选项、完整事件内容或其它 NPC 的任务。

| 当前 NPC | 事件 ID | 资产地点 |
| --- | --- | --- |
| Abigail | 1, 2, 4 | SeedShop, Mountain, Town |
| Alex | 20, 2481135, 21 | Beach, Town, JoshHouse |
| Caroline | 17 | SeedShop |
| Clint | 97, 101 | Saloon, Town |
| Demetrius | 25 | ScienceHouse |
| Dwarf | 691039 | Sewer |
| Elliott | 40, 43 | Saloon, Beach |
| Emily | 471942, 463391, 917409 | HaleyHouse, Town |
| Evelyn | 19 | JoshHouse |
| George | 18 | JoshHouse |
| Gus | 96 | Saloon |
| Haley | 11, 12, 13 | HaleyHouse, Beach |
| Harvey | 56, 57, 58 | JoshHouse, Hospital, SeedShop |
| Jas | 3910979 | Forest |
| Kent | 100 | SamHouse |
| Leah | 50, 51, 52 | LeahHouse, Forest |
| Leo | 6497423, 6497428, 8959199 | IslandWest, IslandSouth, Mountain |
| Lewis | 639373 | Town |
| Linus | 502969, 26, 371652 | Town, Mountain |
| Marnie | 639373 | Town |
| Maru | 6, 7, 8 | ScienceHouse, Hospital, Mountain |
| Penny | 34, 35, 36 | Town, Trailer |
| Pierre | 16 | SeedShop |
| Robin | 33 | ScienceHouse |
| Sam | 44, 733330, 46 | SamHouse, Beach |
| Sebastian | 2794460, 384883, 27 | SebastianRoom, Mountain |
| Shane | 611944, 3910975, 831125 | Forest, Town |
| Vincent | 3910979 | Forest |
| Willy | 711130 | Beach |

Jodi、Krobus、Pam、Sandy、Wizard 本轮没有配置选定事件。空列表只表示没有提供这些事实，不表示该角色没有事件或玩家从未经历过其它事件。

## 原生交互与目标结果

手持物品时由游戏处理任务交付、送礼、数量不足和拒收。空手交互若原生 `OnNpcSocialized(..., probe: true)` 表明任务可推进，同样放行原生处理；探测异常保留原生交互。其余符合条件的交互进入现有 AI 对话。

Willy 钓鱼试点限定单人、当前玩家、一个已接原生目标。任务 `modData` 保存实例 GUID，玩家 `modData` 保存当前目标或最近观察到的结果。两者与游戏同一存档保存，Runtime 不建立另一份原生任务进度权威。

| 状态 | 依据 |
| --- | --- |
| active | 原生任务仍在日志，未完成且未达到数量 |
| ready_to_return | 已达到原生数量，尚未完成向 NPC 报告 |
| completed | 实际观察到原生 `completed` 标记 |
| unavailable | 当前不能确认该任务；不推断取消、过期或成功，当前 caught 为 null |

关联按世界和玩家校验，读档清空进程内对象引用。Native Quest 的序列化代理保存实例 metadata；后台测试仅验证该 metadata 的 XML 往返，游戏展示属性依赖的完整保存加载仍须实机验证。

当前只给 Willy 提供相关目标上下文。鱼名与一般季节、时段、天气来自游戏物品数据和 `Data/Fish`；未提供鱼池与地点可达性证据时不保证能在某处钓到。观察完成不推断奖励已经领取。

基础游戏 `Data/Locations` 中，太阳鱼 `(O)145` 存在于 `Town` 的鱼池与 `Forest` 的 `River` 鱼区，`Mountain` 的基础鱼池不含该鱼；`Data/Fish[145]` 给出的普通条件为春夏、晴天、6:00 至 19:00。Willy definition 使用这些原版背景知识，当前 Mod 改动和实际观察仍有优先权。

原生日常任务由 `Billboard.receiveLeftClick` 接取时设置 `daysLeft = 2`；`Farmer.dayupdate` 每次换日减一，降至零且未完成时移除，因此 `days_left = 1` 是最后有效日。原生 `FishingQuest` 按累计钓获数量报告，与 `ItemDeliveryQuest` 的持物交付不同，玩家不用保留或递交所钓的鱼。

开发接口参考：[原生任务数据](https://stardewvalleywiki.com/Modding:Quest_data)、[SMAPI 内容编辑](https://stardewvalleywiki.com/Modding:Modder_Guide/APIs/Content)、[SMAPI 存档数据](https://stardewvalleywiki.com/Modding:Modder_Guide/APIs/Data)。
