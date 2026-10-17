# World Is Agent MVP0 Phase11.1 预约任务恢复可靠性与 Web 状态展示技术方案

日期：2026-10-17（Asia/Shanghai）

状态：生产代码与自动化验证完成；正式 Vue 控制台采用中文像素风、分层透明窗口和按游戏区分的背景。实机保存、重启、断线及回档组合验收待完成，验证范围见 [开发与验收记录](phase11.1-acceptance.md)。

代码基线：Runtime `c14a75c`，官方 Adapter `ebc295485d6655a6bb5dab830633ca2aa6a69e41`。

## 1. 交付目标与范围

玩家在 Stardew Valley 与 NPC 建立预约后，任务能够在保存、重新加载、Runtime 重启和连接恢复后按明确规则继续；任务无法安全继续时，Web 明确展示原因。玩家可以在本地控制台查看任务、执行状态和实际结果，后续对话使用相同的权威结果。

Phase11.1 的最小产品闭环为：

```text
预约创建成功 → 保存与恢复 → 游戏时间到期 → 执行赴约或明确暂停
→ 见面 / 过期 / 中断等真实结果 → 控制权释放或合法交接 → Web 与后续对话读取一致结果
```

### 1.1 本次包含

- 对已有恢复链路补充组合测试、独立进程测试、实机验收，以及由这些验证发现的根因修复。
- 增加当前加载游戏下的世界选择、任务列表、任务详情、状态筛选和明确的离线、暂停及失败展示。
- 将现有运行概览、游戏选择、模型配置、近期回合和高级详情一并中文化，使用同一套布局、字体、间距和状态语义。
- 将已确认的像素风、中文文案、分层透明窗口与响应式布局落地到正式 Vue 控制台。

### 1.2 边界

- Task SQLite 是任务权威来源。Web 查询并展示数据，组件内缓存不参与调度；刷新、关闭页面或清空浏览器存储不影响任务。
- 任务页面首版为只读：查看、筛选、选择世界和刷新。创建、取消等意图仍通过现有游戏交互和 Runtime Tool 链路处理；页面没有重试、强制完成、清库或重置世界按钮。
- 维持现有“约定时刻开始出发”的语义，以及已有控制权、generation、世界所有权与检查点规则。
- 沿用现有 Protocol、任务合同和存档检查点格式；本次不为 UI 增加游戏私有协议字段，不从网页生成游戏命令。
- 任务阅读模型保持通用；Stardew 是可靠性实机验收对象。RimWorld 保留 `task.enabled=false`，页面明确展示该游戏尚未启用任务。
- 延迟邮件、自主目标生成、任意任务平台、多人同存档并发写入和 Memory 分支隔离不在本次范围内。

### 1.3 与 Phase11.2 的交付边界

| 工作 | Phase11.1 | Phase11.2 |
| --- | --- | --- |
| 运行方式 | 现有本地 Runtime、便携包与本机控制台 | 服务器托管 Runtime、远程接入与部署运维 |
| 身份与权限 | 现有 loopback Host 校验与本地网页会话 | 账号、网页登录、WIA 连接密钥与账号路由 |
| 数据 | 现有数据根内的 Task、Memory 与存档检查点 | 每账号独立持久目录、云端偏好、备份与数据生命周期 |
| 连接恢复 | 同 run 重连、Runtime 重启、保存读档与回档 | 公网延迟、断网、凭证撤销、托管实例重启与网关重启 |
| 前端 | 中文像素布局、只读任务页、现有本地配置功能 | 账号入口、Mod 接入指引、密钥、额度与托管页面权限 |
| 模型配置 | 延续本地用户配置模型的既有行为 | 平台提供模型，模型凭据由服务端管理 |
| 游戏范围 | Stardew 实机预约验收；RimWorld 展示任务未启用 | 首版仅向玩家开放 Stardew，单账号同时连接一个游戏世界 |

Phase11.1 可在没有服务器、域名、账号、WIA Key 和公网连接的条件下独立开发、验证和发布。托管接入方案见 [Phase11.2 云端托管与上线方案](phase11.2-hosted-runtime-and-launch.md)。

### 1.4 交接给 Phase11.2 的成果

- 已验证的 Task 保存恢复语义、故障复现和实机验收记录。
- 稳定的只读 DTO、分页和预算，以及 `instance_id`、工作集身份和陈旧响应门控。
- 可复用的中文任务组件、状态映射、像素主题 tokens 和本地背景资源。
- 本地使用说明与匹配版本的 Runtime / Adapter 包。

账号身份由 Phase11.2 的外层鉴权和独立实例提供。Phase11.1 的 DTO、Task 主键、协议字段和存储路径不增加账号占位字段；本地 HTTP 安全边界保持现有规则。

## 2. 当前实现与增量缺口

| 部位 | 已有实现 | Phase11.1 增量 |
| --- | --- | --- |
| Task 内核 | SQLite、任务状态、wake、operation、证据、终态、暂停与清理记录 | 增加面向控制台的有界只读快照，覆盖查询与恢复同时发生的情况 |
| 保存恢复 | 保存屏障、精确 checkpoint、同 run working head、新 run 检查点恢复 | 完成独立进程与实机组合验证；修复发现的生命周期或证据协调缺陷 |
| 游戏执行 | `resolve_meeting`、`move_to_landmark`、`wait_for_player`、控制权交接和日程恢复 | 验证保存、断线、重启、读档对这些路径的影响 |
| Runtime 装配 | Coordinator 统一发布、退役与关闭运行实例 | 为只读查询绑定实例身份，避免旧查询响应与新游戏或新模型实例混合 |
| HTTP | 会话认证、回环 Host 校验、状态、游戏和模型配置、近期回合 | 增加世界、任务列表与详情的只读 API |
| Web | Vue 3、2 秒轮询、游戏上下文响应门控、配置表单 | 增加任务视图；完善实例、世界、筛选和请求序号门控；统一中文和视觉 |

现有 `TaskService.List / ListActive / ListRecentResults` 主要按角色读取。Web 需要按当前游戏和一个选定世界跨角色读取，不能逐个调用这些接口后在页面拼接不同时刻的数据。

## 3. 权威来源与状态模型

### 3.1 数据分工

| 数据 | 权威来源 | Web 行为 |
| --- | --- | --- |
| 任务身份、说明、状态、revision、结果 | 当前 Task 工作集 | 查询、展示，不写入副本作为权威 |
| 任务集合属于哪个存档状态 | 当前 world head 与精确 checkpoint | 回档后替换列表，清理已不在该工作集中的选择和详情 |
| 世界是否在线、绑定、task-ready | Gateway 的当前世界目录与有效绑定 | 与任务状态分开展示 |
| 角色展示名 | 当前有效 `EntityRef.display_name` | 缺失时回退角色标识；不根据游戏名硬编码 NPC 名单 |
| 页面读取是否仍有效 | HTTP 实例身份、世界上下文与客户端请求门控 | 丢弃旧实例、旧世界及旧请求响应 |

SQLite 中持久化的 `running` 不代表 NPC 此刻正在移动；连接数不代表世界 Ready；`waiting` 也不能一律解释为尚未出发。页面不得据此推断游戏物理状态。

### 3.2 任务状态中文映射

| 原始状态 | 中文 | 展示规则 |
| --- | --- | --- |
| `waiting` | 等待中 | 任务等待未来唤醒或环境事实；保留约定说明 |
| `running` | 执行中 | 当前记录处于执行状态，仍需同时展示世界可用性 |
| `paused` | 已暂停 | 必须同时展示暂停原因；不能显示成失败或已取消 |
| `succeeded` | 已完成 | 只有权威终态结果成立时显示 |
| `failed` | 未完成 | 展示真实原因，例如条件未满足或执行中断 |
| `cancelled` | 已取消 | 展示已提交的取消结果 |

页面分为“进行中”和“已结束”两个集合；进行中包含 `waiting / running / paused`，已结束包含其余三个终态。状态颜色同时配有文字和图标，不能只靠颜色传达。

记录的 `needs_reconcile=true` 作为任务级提示“正在确认执行结果”，保持原始 state；它不代表整个世界不可用，也不影响该世界其它任务的状态展示。

### 3.3 世界可用性独立展示

`world.availability` 为控制台阅读模型，不写回 Task 状态：

| 值 | 中文 | 任务展示 |
| --- | --- | --- |
| `ready` | 世界已就绪 | 展示任务原始状态 |
| `waiting_for_world` | 等待游戏连接 | 展示持久记录，注明当前尚未确认执行条件 |
| `saving` | 游戏正在保存 | 保留任务，提示保存结束后重新确认 |
| `recovery_blocked` | 恢复需要处理 | 展示通用原因，原始 code 放入高级详情 |

这些值只能从现有 head、保存屏障和有效绑定推导；无法确认时采用 `waiting_for_world`，不依靠收到连接后的固定等待时间改成 Ready。

## 4. 恢复可靠性

### 4.1 保留现有恢复语义

1. 同一游戏 run 中 Runtime 重启：恢复最新 working head，旧执行资格失效，不确定 operation 进入协调。
2. `SaveLoaded` 开启新 run：按该存档携带的精确 checkpoint 恢复，不用“最新检查点”代替。
3. 回退旧存档：恢复该 checkpoint 的任务集合与版本，未来创建的任务不出现在当前列表中。
4. 引用缺失、损坏或未确认：保留恢复失败原因，暂停相关世界；不初始化空任务集合来绕过失败。
5. 普通异步 Action 的内存等待不跨进程续接。Task 恢复依赖证据与重新观察，不盲目重发动作。

Task checkpoint 不会整体回退 Memory 数据库。实机记录分别核对当前 Task 工作集和模型最终请求中的任务结果；发现旧任务结果被当作当前结果时，作为 Task 结果投影的一致性缺陷处理。通用 Memory 分支隔离仍属于已有边界，本次不据此承诺废弃时间线中的全部记忆均已永久隔离。

### 4.2 必须覆盖的恢复矩阵

| 场景 | 检查点与任务断言 | 游戏与用户侧断言 |
| --- | --- | --- |
| 创建预约后正常保存，保存完成后继续 | checkpoint 确认；任务身份保持；保存屏障解除后重新绑定 | 到期能继续；页面没有重复任务 |
| 保存退出后重新进游戏 | 新 run 使用存档中的精确引用；恢复任务与结果 | 游戏就绪前显示等待；就绪后按游戏时间继续 |
| 预约等待期间重启 Runtime，游戏保持打开 | 同 run 使用最新 working head，旧 wake claim 不再执行 | 重连后恢复；不要求玩家重新预约 |
| 移动过程中断线或重启 Runtime | 不确定 operation 先协调；旧回调不能覆盖新代次 | NPC 控制权不会永久占用；未确认时明确暂停 |
| 已到达并登记等待后保存、重启或重连 | 等待证据、截止 wake 和 operation 关系保持一致 | 只能得到一次见面或过期结果；恢复后不会再从头移动 |
| 保存已 prepared、Finish 丢失 | 有界屏障和重绑定规则成立 | 实际游戏保存不会因无限等待卡死；页面不无限显示保存中 |
| 回退至预约创建之前 | 当前工作集没有该任务 | 列表与详情移除该任务，不能显示为另一个任务的终态 |
| 回退至任务执行之前 | 恢复该引用对应版本与 wake | 后续按恢复后的游戏时间处理，不借用未来结果 |
| 错误 world、checksum 损坏、unconfirmed 引用 | 恢复失败关闭，不选择其它快照 | 中文原因可见；不自动清库、重置或改写存档 |
| 重复证据、重复保存回执、旧代次回调 | 幂等；终态唯一；不增加重复有效 wake | 页面计数与结果稳定，NPC 日程恢复不被旧清理回调破坏 |
| 游戏或模型切换时旧查询仍在进行 | 旧实例响应不能成为新实例页面状态 | 页面清理旧数据并重新拉取；历史保留规则不变 |

### 4.3 失败时的行为

- 保存准备超时或失败：遵守现有有界等待，游戏实际保存继续；写入真实的未确认引用并展示原因。
- 结果不确定：保持协调或暂停，不用模型台词补造成功结果。
- Task 终态与控制权清理分别展示。`succeeded` 但 cleanup 尚未确认时，同时提示“任务已完成，控制权交接待确认”。
- game run 变化、世界不再 Ready、generation 变化时，旧动作、回调和页面响应均不得获得当前执行权威。
- 任何修复先建立失败复现，再直接修正归属模块的逻辑；不通过放宽校验、增加超时或清空状态掩盖问题。

## 5. 控制台只读模型与查询

### 5.1 模块与依赖

新增 Task 只读查询及 DTO，使用当前 bundle 已打开的 TaskStore，不由 HTTP 再打开任务库，不创建独立写入连接或另一份调度服务。

```text
HTTP（认证、参数和响应）
  → bootstrap.Runtime.ReadTaskWorlds / ReadTaskPage（实例绑定与可用性）
    → TaskStore 只读快照（world head、计数、任务）
    → Gateway 只读世界目录（连接与角色展示名）
  → 通用 TaskConsole DTO
  → Vue 状态与中文映射
```

Task 结果以同一 SQLite 只读事务里的工作集为准。Gateway 的在线目录是独立的即时快照，不能宣称与数据库构成跨系统原子事务；目录无法与读取 head 的绑定对齐时，世界可用性降为 `waiting_for_world`，任务记录保持原始状态。

### 5.2 实例生命周期

- 为每个成功发布的 bundle 增加不可猜语义的 `instance_id`，在 `/api/status` 中以可选新增字段返回；同游戏更换模型也产生新身份。
- 查询开始时在短锁内取得 bundle、实例 ID 和实例读取 context，随后释放 Coordinator 锁再执行有截止时间的数据库读取。
- 实例退役先取消其读取 context；`database/sql` 的关闭与在途读取按既有关闭流程处理。查询期间发生关闭按可恢复的实例变化或不可用错误返回。
- 响应前再次确认发布实例、Ready 状态与所请求游戏；已变化时丢弃查询结果，返回 `runtime_changed`。
- HTTP 不暴露 TaskStore 裸指针，不在 Coordinator 锁中等待数据库 IO，也不从已退役实例读数据后标成新实例。

### 5.3 查询范围和预算

- 每次请求绑定一个加载游戏；任务列表绑定一个世界。世界列表来自持久 world heads，因此游戏未连接时仍可找到已保存记录。
- 世界列表默认 20、最大 50 条；任务列表默认 20、最大 50 条。默认选择唯一在线世界；只有一个历史世界时可以自动选择；多个候选世界且无有效选择时明确要求用户选择。
- 世界选择只改变读取范围，不切换游戏、不激活存档，也不产生 TaskService mutation。
- 单次查询截止 500ms；超时、锁冲突或记录校验失败返回明确错误，不能作为零任务成功响应。
- 列表按 `(entity_id, task_id)` 稳定排序，复用现有键范围；不把 Task ID 字符串顺序解释为业务时间。
- 下一页使用版本化 opaque cursor，绑定 `instance_id / game_id / world_id / 集合与筛选 / 工作集身份 / 最后一个键`。工作集在恢复、保存或换 run 后变化时，分页失效并回到第一页。
- 分页属于实时工作集查询，不承诺跨请求固定快照。任务在翻页期间转换状态时，下一次完整刷新回到第一页重新读取计数与列表，不能把已加载分页当作完整离线档案。
- 列表与计数在同一只读事务里读取；计数包含当前选定世界的整个工作集，不是当前页条数。
- 页面读取不调用 `ActivateWorld`、`ClaimDue`、`Reconcile` 或 `UpdateClock`。终态和清理记录查询不推进 revision。
- 先有界选择记录键，再读取和校验本页记录；累计载荷读取上限 16 MiB，列表响应上限 256 KiB。超过预算返回 `task_query_budget_exceeded`，不返回伪完整页面。
- 游标为分页定位数据，不是权限凭据；服务器完整校验其范围，不接受客户端指定数据库路径。

### 5.4 安全投影与游戏边界

- HTTP 返回明确 DTO，不直接序列化 `task.Record`。
- 默认字段为身份、约定说明、通用状态、revision、创建时间、暂停原因、通用结果和清理状态。
- 不返回原始 contract、operation receipt、source facts、存档正文、完整 prompt 或任意路径；自然语言字段作为纯文本显示，不使用 `v-html`。
- 角色名来自有效 EntityRef；世界缺少名称时显示缩短世界 ID，并在高级详情保留完整标识。
- 约定正文直接使用持久化 `TaskSpec.Instruction`，作为“约定说明”展示。不得用模型再生成一份被当作权威的新摘要。
- Phase11.1 不从 Stardew 私有 contract 推导日期、地标、赴约阶段，也不从通用 tick 猜测游戏分钟。`wake_tick / deadline_tick` 仅放在高级详情；自然语言约定可以包含原有日期和地点。
- 时间线只显示确实存在的通用事实，例如创建、当前执行状态、进展证据条数、权威终态和清理。没有可信时间戳的阶段不补造时刻，当前 `running` 不自动生成“NPC 已出发”。
- 标识、revision、generation、tick 等 64 位整数在 JSON 中使用十进制字符串，避免浏览器精度丢失。

## 6. HTTP 契约

新增接口继续经过现有 loopback Host 检查和网页会话认证，使用 GET，响应设置 `Cache-Control: no-store`。

| 接口 | 参数 | 返回 |
| --- | --- | --- |
| `GET /api/task-worlds` | `game_id`、`instance_id`、`limit`、可选 `cursor` | 功能是否启用、可选世界、工作集身份和世界可用性 |
| `GET /api/tasks` | 上述游戏和实例，加 `world_id`、`collection=active\|terminal`、可选 `state`、`limit`、`cursor` | 筛选计数、当前页、下一页游标及快照范围 |
| `GET /api/tasks/detail` | `game_id`、`instance_id`、`world_id`、`entity_id`、`task_id` | 当前工作集中的详情、结果、通用证据和清理摘要 |

详情查找同时绑定 owner 和 task ID。回档后不在工作集的任务返回 `task_not_found`，不能从不可变历史快照中悄悄拼回当前详情。

列表响应示例：

```json
{
  "instance_id": "instance_a7e2",
  "game_id": "stardew-valley",
  "world_id": "world_7c3a",
  "supported": true,
  "observed_at": "2026-10-17T08:32:00Z",
  "working_set": {
    "run_id": "run_31",
    "generation": "9",
    "checkpoint_id": "checkpoint_12"
  },
  "world": {
    "world_id": "world_7c3a",
    "working_set": { "run_id": "run_31", "generation": "9", "checkpoint_id": "checkpoint_12" },
    "availability": "ready"
  },
  "counts": { "waiting": 1, "running": 1, "paused": 1, "succeeded": 1, "failed": 0, "cancelled": 0 },
  "items": [
    {
      "task_id": "task_01",
      "entity_id": "npc:Linus",
      "entity_display_name": "莱纳斯",
      "instruction": "春季第19日10:00出发去海滩，等待玩家到16:00。",
      "state": "running",
      "needs_reconcile": false,
      "revision": "4",
      "created_at": "2026-10-17T08:20:00Z",
      "cleanup_status": "not_required"
    }
  ]
}
```

`cleanup_status` 为只读聚合值：没有已登记 operation 时为 `not_required`，尚无明确清理证据为 `pending`，所有关联 operation 均 released 或合法 handed_off 时为 `confirmed`，存在明确未确认项为 `unconfirmed`。合法对话交接不要求先结束 UI 才将交接本身认定为成功。

### 6.1 错误与降级

| HTTP / code | 页面行为 |
| --- | --- |
| `400 invalid_task_query` | 显示查询参数问题，重新选择筛选或世界 |
| `401 unauthorized` | 清理可信运行态，提示重新从 Runtime 打开控制台 |
| `409 runtime_changed` / `game_mismatch` | 丢弃旧响应，刷新状态，按新实例重新请求 |
| `409 task_snapshot_changed` | 清理分页和详情，刷新当前世界第一页 |
| `404 task_not_found` | 提示该任务已不在当前存档任务集合，关闭详情并刷新列表 |
| `503 runtime_not_ready` | 显示初始化或配置状态，任务区域不宣称正在执行 |
| `503 task_query_failed` / `task_query_budget_exceeded` | 显示读取失败；旧数据只可作为带时间标记的未同步记录，不能显示为实时状态 |

当前游戏关闭 Task 时返回 `supported=false` 和空任务集，HTTP 为 200。它与“已启用但当前没有任务”、读取失败和游戏离线是四种独立页面状态。

### 6.2 详情补充字段

详情沿用列表的范围与单个任务字段，新增以下只读投影：

| 字段 | 来源与语义 |
| --- | --- |
| `facts.operation_count` | 当前记录的 operation 数量；大于零可以显示“已有执行记录”，不推导当前游戏位置 |
| `facts.progress_evidence_count` | 已应用的通用 progress 证据数量；不等同任务完成 |
| `facts.outcome` | 权威结果对应的 satisfied、unsatisfied 或 interrupted；没有对应终态证据时省略 |
| `facts.outcome_game_time` | 存在时使用已有规范化 GameTime；缺少部分字段时不补造 |
| `cleanup_status` / `cleanup_reasons` | 关联 operation 的清理聚合与已记录原因；不返回 receipt |
| `advanced` | 当前 clock ID 与 wake/deadline/next-wake tick；revision 和原因 code 位于任务字段，run、generation、checkpoint 位于 `working_set` |

详情标题由“与角色展示名的约定”组成，正文使用已有 instruction，不增加由模型推测的地点标题。创建使用真实 `created_at`，执行阶段没有时间戳时显示事实而非补造时间。截图中的日期与时刻属于示例约定正文。

## 7. 中文 Web 与交互

### 7.1 页面信息结构

| 页面 | 首要内容 | 延续的现有功能 |
| --- | --- | --- |
| 运行概览 | Runtime 状态、当前游戏、模型、任务摘要与近期回合 | 模型设置入口、连接详情、刷新 |
| 预约任务 | 世界选择、进行中与已结束、状态筛选、任务列表与详情 | 新增只读信息，不增加游戏操作按钮 |
| 游戏与模型 | 游戏选择、切换影响、服务商、模型、密钥与可选接口地址 | 首次配置与运行中修改保持既有提交和探测语义 |
| 运行记录 | 当前游戏近期回合、状态、步骤、工具摘要 | 高级详情保留 Trace 路径，首版不新增完整 Trace 浏览器 |

导航属于同一个控制台，不新增独立服务或第二套网页会话。桌面使用已确认的像素侧栏，窄屏使用紧凑导航。

### 7.2 任务列表与详情

- 默认打开进行中集合；暂停任务数量在入口可见。用户可以切换已结束和状态筛选。
- 列表展示角色、约定说明、状态和创建时间；详情展示完整说明、读取范围、暂停或结果原因、通用事实及控制权清理。
- 桌面列表与详情并排；窄屏点击后进入详情并提供返回列表，不把多个窄列挤在一个屏幕内。
- 选择由 `(instance_id, game_id, world_id, entity_id, task_id)` 标识。刷新保留仍存在的选择，回档或切换后清理无效选择。
- 暂停详情给出真实原因及可行指引，例如等待当前存档连接或在游戏内重新沟通；没有明确恢复动作时不提供“恢复成功”承诺。
- 不把浏览器刷新解释为重新执行任务。

### 7.3 轮询与陈旧响应

- 复用现有 2 秒状态轮询；任务页面激活且文档可见时拉取列表和当前详情。请求未完成时不叠加下一次同类轮询。
- 请求上下文包含实例、游戏、世界、集合、筛选和请求序号；任一维度变化立即取消旧请求并使旧响应失效。
- 复用并扩展 `createContextResponseGate`，不得只检查请求序号或游戏 ID；同游戏更换模型同样会换实例。
- 失去 Runtime 连接时清除 Ready 和实时计数。若保留最后一次任务内容，整个任务区域必须展示“尚未同步”和最后成功读取时间。
- 游戏离线但 Runtime 在线时可以继续读取持久记录，显示“等待游戏连接”；不能把它混同 Runtime 失联。
- 世界恢复切换工作集时，列表、计数、分页和详情作为一个范围更新，避免未来任务短暂残留。

### 7.4 全量中文化

- 增加统一的 `zh-CN` 文案与 code 映射，覆盖 App、GameSetup、Setup、新任务组件、空状态、加载、错误、表单校验、导航和无障碍名称。
- 游戏名采用“星露谷物语”“环世界”。World Is Agent、DeepSeek、OpenAI、模型 ID、实体 ID 和协议 code 保持原始标识。
- API 状态枚举和现有字段保持兼容，不通过改后端 code 来翻译页面。
- 已知错误 code 对应中文标题和恢复说明；未知 code 显示中文通用提示，安全原始详情可在高级详情查看。
- API Key 输入不预填、不持久化到浏览器；取消、提交成功和关闭表单时清理输入。失败保持可编辑；候选探测失败保留当前运行模型。
- 现有游戏与模型切换仍取消当前回合、保留已记录历史。配置按钮、忙碌状态和请求门控沿用已有正确行为。

## 8. 已确认视觉与简单切换

正式控制台采用 [像素世界样稿](previews/pixel.html) 的视觉：中文界面、像素场景背景、方形窗口、分层透明面板与同一套导航和内容布局。

- 星露谷采用春日田园背景与暖色窗口；环世界采用殖民地背景与钢蓝窗口。
- 中文正文使用清晰的常规字体，像素感体现在场景、边框、窗口与图标。
- 正文与交互文字对比度至少 4.5:1；状态以文字和图标共同表达；键盘焦点清晰可见，主要交互目标至少 40px。
- 初次配置、运行中配置、空列表、读取失败、游戏离线、Runtime 失联、恢复受阻和 RimWorld 未启用状态使用同一套组件语义。
- 桌面验证 1440px，中等宽度验证 1024px，手机验证 390px；所有页面没有横向溢出。

### 8.1 主题与透明度

- 游戏主题映射位于前端 `App.vue`，样式 tokens 位于 `style.css`，以已接受状态快照中的 `loaded_game.id` 选择本地背景与 tokens。Runtime Core 不判断视觉，不解析游戏私有 Observation。
- 星露谷背景色不透明度：外壳 82%、侧栏 88%、信息面板 96%；环世界分别为 84%、90%、97%。文字和图标本身保持完全不透明。
- 手机端外壳提高到 90% / 92%，侧栏提高到 94% / 96%；正文和输入区域维持高遮盖率。
- 图片加载失败使用对应主题底色，无加载游戏或未知游戏使用中性主题；图片故障不阻断任务查询和配置。
- 背景随网页构建发布，离线可用。背景保持静态，中文阅读区不使用背景模糊、持续视差或闪烁。

### 8.2 本地游戏切换

| 状态 | 行为 |
| --- | --- |
| 选择候选 | 只更新表单选中态，主题和任务读取范围保持不变 |
| 提交 | 按钮显示“切换中”并禁止重复提交；保留当前主题，任务区退出实时可信状态 |
| 实例发布成功 | 接纳新 `instance_id` 与 `loaded_game.id`，清理旧任务范围并直接更新背景、配色与内容 |
| 等待游戏连接 | 显示“等待游戏连接”，按真实世界绑定状态更新任务可执行性 |
| 失败 | 在表单附近显示原因，恢复可编辑状态；主题取最后已确认游戏，可用性取当前服务端快照 |

- 使用现有配置提交和发布机制，保留取消当前回合与保留已记录历史的语义。
- 主题直接跟随生效快照更新，前端不加入转场对话框、倒计时、强制等待或切换动画。
- 主题显示与任务权威分别管理，仍按第 7.3 节丢弃旧实例和旧世界响应。
- 本节只处理当前本地进程的配置。托管首版的当前游戏由已鉴权连接与实例配置确定，属于 Phase11.2。

### 8.3 对齐与版式

- 桌面外窗宽 1248px，大屏扩展至 1328px；侧栏宽 192px，中等宽度为 168px。
- 页面标题、当前世界、列表和详情采用同一内容边界。主内容内边距、分区间距和双栏间距统一为 24px；中等宽度双栏间距为 16px。
- 顶部游戏标识与外窗共用水平容器，表单标题与内容使用 24px 内边距，字段间距 16px。
- 任务列表与详情上边缘一致，同级头像、状态与按钮尺寸一致。详情与列表可以依内容保留各自高度。
- 手机内容内边距 16px，面板内边距 20px，分区间距 24px；从列表进入详情并提供返回入口。

## 9. 代码变更落点

| 仓库与文件 | 职责 |
| --- | --- |
| Runtime `runtime/internal/task/console_query.go`、对应测试 | 单世界只读快照、计数、分页、详情与载荷预算；不改变执行状态 |
| Runtime `runtime/internal/task/console_model.go` | 通用阅读 DTO 与查询类型 |
| Runtime `runtime/internal/bootstrap/task_console.go` | 实例身份、查询生命周期、持久快照与在线目录对齐 |
| Runtime `runtime/internal/bootstrap/bootstrap.go` | bundle 实例身份和读取 context 的发布、取消与关闭 |
| Runtime `runtime/internal/gateway/world_registry.go` 或独立只读文件 | 安全复制当前世界目录，不调用执行入口 |
| Runtime `runtime/internal/httpapi/tasks.go`、对应测试 | 三个 GET 接口、认证继承、范围和错误处理 |
| Runtime `runtime/internal/httpapi/server.go` | 路由和 status 新增实例标识 |
| Web `console/web/src/tasks/` | TaskList、TaskDetail、TaskStatus、请求与类型 |
| Web `console/web/src/i18n/zh-CN.ts` | 全页面中文和原因码映射 |
| Web `App.vue / GameSetup.vue / Setup.vue / api.ts / types.ts / latest-response.ts` | 统一视觉、中文化、实例和范围门控 |
| Adapter `stardew-valley/src/Tasks/`、`src/Runtime/` 与对应测试 | 仅修改恢复验证中确定的根因所属代码，保持既有协议和 Mod 标识 |
| 文档 `README.md / docs/STATUS.md / docs/development/testing.md` | 实现与验收完成时同步已成立事实，不把方案写成已交付 |

现有数据库 schema 先保持不变。列表使用已有范围键和只读事务；只有实测证明查询预算无法满足时，才在同一工作单元论证必要索引及升级、恢复兼容测试。

## 10. 实施与验收单元

### 单元 A：恢复基线与故障复现

- 为第 4 节每个场景确认已有测试、补齐组合缺口，使用真实 SQLite 与正式 gRPC 链路。
- 独立子进程仅启动测试自己拥有的 Runtime；验证数据库锁释放、重启恢复和异常退出窗口。
- fake capability 使用通用工具名称，避免 Runtime 正确性依赖 Stardew 名称。
- 发现缺陷时提交对应复现和根因修复；不预先改写已通过的整个 Task 内核。

完成条件：所有自动化场景有明确断言，任务、wake、operation、结果及控制权清理关系保持一致。

### 单元 B：只读快照与 HTTP

- 实现持久世界目录、单世界快照、详情、预算和实例绑定。
- 验证新旧实例、同游戏模型切换、回档和并发清理时响应不混合。
- 覆盖未认证、错误 Host、越界查询、游标错范围、Task 关闭、空世界、离线世界、损坏记录、查询超时和关闭竞争。
- 读操作前后对比任务状态、revision、wake 与数据库内容，证明查询无执行副作用。

完成条件：API 只读、范围隔离、结果完整或明确失败，不能泄露 raw Record 或凭据。

### 单元 C：中文像素控制台

- 将已确认像素样稿的 tokens、背景、透明度与布局落地到正式 Vue，保留本地配置行为。
- 完成所有旧界面的中文化、任务视图与响应门控。
- 验证桌面、窄屏、键盘、空状态、暂停、失败、断开和配置失败；游戏切换验证按钮忙碌态、真实结果和直接主题更新。

完成条件：页面视觉一致，所有主要操作中文可理解，静态示例替换成真实 API，任何失联或旧响应都不会显示为实时成功。

### 单元 D：正式包与实机闭环

- 构建便携 Runtime 和匹配 Adapter，记录版本、commit 和文件校验值。
- 使用测试存档副本、独立数据根和已验证路线，执行第 4 节恢复矩阵。
- 至少覆盖一次见面成功和一次玩家未出现的过期结果，并核对 Web、Task DB、游戏位置、日程恢复与后续对话。
- 实机恢复失败保留独立测试数据和日志，不改玩家正式存档、Task DB、Memory DB 或目录权限。

完成条件：正常路径与故障路径均有真实证据；未确认场景明确列为限制。自动化或静态页面通过不替代实机结论。

## 11. 验证命令与检查对象

```powershell
go test ./runtime/internal/task ./runtime/internal/gateway ./runtime/internal/bootstrap ./runtime/internal/httpapi ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/memory -count=1
go test ./... -count=1
go test -race ./runtime/internal/task ./runtime/internal/gateway ./runtime/internal/bootstrap ./runtime/internal/httpapi -count=1
npm --prefix console/web run type-check
npm --prefix console/web run test:ordering
npm --prefix console/web run build
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
git diff --check
```

Adapter 在独立仓库运行 TaskExecution、ProtocolMapper、RuntimeClient、控制权相关测试和静态检查，并通过该游戏自足的构建入口显式指定 Protocol。命令和协议 pin 以该仓库当前脚本为准。

race 需要可用的 64 位 C 工具链；缺失时记录未执行并在具备条件的环境补验。浏览器检查使用 gstack `/browse`，覆盖真实后端、配置失败、任务只读和旧响应竞争，不能以组件快照代替。

## 12. 最终通过标准

1. 已约定预约在保存、同 run 重连和 Runtime 重启后按既定恢复规则继续，或明确报告无法安全继续的原因。
2. 新 run 和回档使用精确 checkpoint；列表、详情、计数及后续执行属于同一当前工作集。
3. 重复证据、回执和旧代次回调不会造成重复终态、重复有效 wake 或错误释放新控制权。
4. 见面与过期两条实机链路成立，终态与 NPC 控制权清理能够分别核对。
5. 任务状态、世界可用性、网页连接和最后同步时间分别准确展示。
6. 浏览器关闭或刷新不影响任务；所有任务查询无调度和游戏副作用。
7. 页面和后续 Task Context 展示的当前任务结果与 Task 权威结果一致；验收对话不将旧任务结果误作当前完成事实，Memory 分支边界独立记录。
8. 原有配置、运行状态和近期回合均完成中文化，已确认的像素视觉在桌面与窄屏对齐且可读。
9. API、前端类型、相应回归、浏览器验证、适用 race 和正式包检查通过，实机范围独立记录。
10. 公开事实源与实机记录同步；尚未通过的能力保留明确限制。
11. 本地包在没有任何托管服务的条件下完成全部验收；Phase11.1 的生产变更不包含账号、WIA Key、公网服务、云端部署或额度系统。

## 13. 依据

- [当前实现与验证状态](current-state.md)
- [Phase9 开发与验收记录](../phase09/GameAgent%20MVP0%20Phase9%20开发与验收记录.md)
- [Phase9.4 检查点与结果记忆方案](../phase09/GameAgent%20MVP0%20Phase9.4%20检查点与结果记忆技术开发方案.md)
- [Live Settings 规格](../superpowers/specs/2026-09-20-runtime-live-settings.md)
- [现有实机验收入口](../development/testing.md#live-settings-real-game-acceptance)
- [Task 内核](../../runtime/internal/task/)、[Runtime 装配](../../runtime/internal/bootstrap/bootstrap.go)、[HTTP 边界](../../runtime/internal/httpapi/server.go)
- [现有 Vue 控制台](../../console/web/src/App.vue)、[请求门控](../../console/web/src/latest-response.ts)
