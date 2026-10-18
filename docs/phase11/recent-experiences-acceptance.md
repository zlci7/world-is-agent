# 近期经历后台验收

日期：2026-10-18。

## 结论

近期经历功能的后台验收通过。真实赠送事实具有独立持久化来源、近期自动召回、长期文本检索和当前存档有效性过滤。Runtime 与 Stardew Adapter 的源码及开发构建已具备该链路。

实机送礼和 NPC 回答质量未验收。扩大范围的 race 检查存在容量与时间预算超时，不能记为全量 race 通过。

## 当前行为

- Stardew 单人模式在原生 `NPC.receiveGift` 完成后比较实际 NPC/物品赠送计数。计数增加才生成确认事实；赠送数量记录为一个，赠送成功与喜欢该礼物分别理解。
- 事实按 `game_id + world_id + entity_id` 隔离，存入既有 SQLite 的 `session_history`，类型为 `experience`。原文、文本检索索引和 `experience_index` 在同一事务提交。
- 赠送关注期为三个游戏日。每回合的 `Recent Experiences` 自动选取至多八条完整事实、768 个估算 tokens。原始经历独立于普通聊天扫描和摘要；当前输入与因果工具消息保留预算优先级。
- 关注期结束后，原文保留，可通过既有字面文本检索按话题召回。
- 游戏存档保存对应 NPC 的有效事实 ID 和待同步内容。断线期间保留待同步事实，连接恢复后补发；确认持久化后只移除待同步正文。旧存档恢复的有效 ID 集合限制近期召回及经历检索。
- 经历事件属于记录通路，不启动模型回合、不抢占 NPC 控制。扩展协商与 Task 独立；未协商的旧 Adapter 沿用已有行为。

## 功能证据

| 场景 | 结果与证据 |
| --- | --- |
| 昨日经历、今日只问候 | 两个 fake 游戏使用不同事件名称；SQLite 重开、Loop 重建后，Recording Provider 的实际请求仍含正确经历及来源，无需当前问题提及礼物 |
| 聊天增长与摘要 | 四十条后续聊天、仅八条的普通扫描上限及省略礼物的摘要不影响独立经历；另一个测试实际执行普通摘要生成，验证经历不进入摘要覆盖集合 |
| 过期与长期召回 | 到达关注截止 tick 后自动区块退出；提问蓝莓时文本检索仍返回原始来源 |
| 作用域隔离 | 其他游戏、世界和角色无法读取同一私有经历 |
| 回档与追平 | 有效 ID 集合外的记录在近期区块和检索中均被过滤；相同或更晚时间也不恢复其可见性 |
| 幂等与事务 | 重送保留同一来源和首次写入时间；同 ID 不同内容返回冲突；索引写入失败时原文与文本索引一起回滚 |
| 补发失败 | Runtime 写入失败返回明确拒绝，重发同一 ID 可以成功；游戏端待同步正文随存档往返，确认后保留有效 ID |
| 索引可靠性 | 删除派生索引表后可从原始经历重建；十条无效分支记录不消耗仅一条的检索候选容量 |
| 请求预算 | 数量、独立 token 上限及最终总请求上限通过；内部有效 ID 列表不进入模型 Observation，其他扩展正常保留 |
| 跨语言 | C# Mapper 输出与 Go 解析使用字节一致的事件及观察样例；已发布 proto 与生成代码无差异 |
| 游戏 API | 本机 Stardew 1.6.15 程序集确认成功路径更新赠送台账；后台测试实际注册/卸载 Harmony 前后钩子，核对真实方法签名 |
| 独立进程 | 新 Runtime 二进制通过真实 gRPC 接收事实；强制结束、重启、重送后，数据库仍只有一条来源，其 ID、sequence 和首次写入时间相同，近期查询有效 |

## 回归与审查

| 检查 | 结果 |
| --- | --- |
| Go 全量 `go test -p 1 -json ./... -count=1 -timeout 8m` | 25 个测试包、1,222 项顶层测试和 1,712 项子测试通过；7 项跳过 |
| 经历专项 race | 五个包、15 项顶层测试和 12 项子测试通过，包含经历持久化、请求投影、网关确认及串行任务预算复测 |
| Stardew 官方测试入口 | 六个项目，共 536 项测试通过；安装路径安全检查中的文件符号链接用例受普通 Windows 账户权限限制跳过 |
| 中文控制台 | `type-check`、`test:ordering`、`build` 通过；独立进程返回中文网页 |
| Protocol | static check、Go generation check 通过，固定 tag 仍为 `protocol-v1alpha2.0`，commit 为 `950d417fcc64517d8d59c334a4cf96ec224bd61a` |
| 架构与差异 | Runtime game-agnostic 架构检查、两个仓库的 `git diff --check` 通过 |
| 内部 CR | 检查持久化确认、原生交互、任务独立性、作用域、回档、预算和旧协议兼容；完成自查，未进行独立上下文 CR |

Go 的七项跳过包括五个需显式启动的历史/真实模型验收辅助或诊断测试，以及两个需文件符号链接权限的 Task 存储测试。本轮功能使用 Recording Provider 和 fake provider，不调用玩家配置的 LLM Key。

扩大 race 检查的六个顶层失败项为 `TestTaskTravelBudget`、`TestRetrievalMigrationStreamsAllOwners`、`TestSQLiteSearchDefaultIndexCapacityBoundaries`、`TestSQLiteSearchDefaultScanCapacityBoundaries`、`TestSQLiteSearchSharedPathReadBudget`、`TestSummaryCapacitySourceBoundaries`。日志显示动作/查询/写入时间预算超时，未出现数据竞争报告。任务预算串行复测通过；索引容量用例在当前代码及改动前 HEAD 的隔离副本中串行检查均通过。其余容量超时未完成归因，生产预算保持原值。

## 构建与复核材料

Runtime 开发构建：`runtime/.local/experiences-20261018/wia-runtime.exe`，版本 `0.2.0-dev.recent-experiences`。Stardew 开发构建：独立 Adapter 仓库的 `stardew-valley/bin/Release/GameAgent.Stardew.dll`，版本 `0.1.2`。

Runtime 仓库的 `runtime/.local/experiences-20261018/` 保留 `go-tests-final.jsonl`、`experience-race-final.jsonl`、`race-tests.jsonl`、串行容量比较记录及 `binary-smoke.json`。构建与测试材料位于忽略目录，不属于发布包。

本轮未安装至正式游戏目录，未改写玩家存档、正式 Memory/Task、模型配置或 Key。

## 实机边界与失败行为

实机检查需要同时运行本次 Runtime 与 Adapter 开发构建，并使用独立测试存档：

1. 向普通 NPC 赠送蓝莓，核对原生扣物与赠送反馈，随后只打招呼，检查实际请求包含正确角色、物品与时间。
2. 跨日、增加聊天、保存读档并重启 Runtime，核对经历继续进入关注区块；跨越关注期后提问蓝莓，核对按话题召回。
3. 暂停 Runtime 后赠送，保存并重新连接，检查补发；加载赠送前的备份，核对该事实在近期区块和经历检索中均不可见。
4. 核对拒收、原生物品交付和其他 NPC 不产生错误的赠送记忆。

采集失败保留原生交互并记录错误。Runtime 拒绝或写入失败时，游戏保留待同步正文；读取经历失败时，本轮对话继续，Trace 记录缺失诊断。容量不足时完整省略部分事实并记录预算诊断。

普通对话及其摘要的既有回档分支隔离限制仍然存在：它们可能包含对失效经历的谈论。新经历契约只保证自身来源的有效性过滤。已确认正文保存在 Runtime，删除数据库后，游戏中的 ID 无法重建这些正文。多人赠送、主动发信、电话及新委托均不在本次范围内。
