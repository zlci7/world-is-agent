# GameAgent MVP0 Phase10.4 Game Profile 选择与多游戏产品形态技术方案

> 状态：待文档验收；代码开发未开始。
> 日期：2026-09-20。
> 前置：10.3 已正式 Accepted，条件 9 的负向部分未验证且经用户决定豁免，见 [10.3 验收记录](GameAgent%20MVP0%20Phase10.3-3与10.3-4%20实现与证据记录.md)。
> 上位文档：[Phase10 总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md)。
> 后续：[10.5 官方 Adapter 仓库拆分方案](GameAgent%20MVP0%20Phase10.5%20官方%20Adapter%20仓库拆分技术方案.md)。

## 1. 目标与边界

用户在本地客户端选择游戏，Runtime 准备对应的 Game Profile 并启动。首次配置完成后，当前游戏行为固定到进程结束；运行中保存的新选择在重启后生效。

Game Profile 包含 Agent 配置、prompt、工具调用预算、timeout、task policy、memory policy 和 definition catalog。切换必须使这些配置及其依赖组件一致生效。

| 工作 | 归属 |
| --- | --- |
| 游戏选择、Runtime 配置资产准备、旧配置迁移 | 10.4 |
| 首次初始化、重启切换、Selected / Next / Connected、连接拒绝 | 10.4 |
| 两个官方 Adapter 迁入 `world-is-agent-adapters` | 10.5 |
| Turn 详情、任务与记忆查看、能力可用性、依赖体检 | 10.2-5 |
| 第三方 Adapter 接入检查表 | 10.6 |

执行顺序为 10.4 → 10.5。文档验收后，开发按工作单元连续完成自动验证与内部审查，最终集中进行产品验收；内部编号不构成逐项人工验收关卡。

## 2. 配置布局与来源

### 2.1 所有权与发布树

`world-is-agent` 保留 Runtime、Console、Protocol、Game Profile、prompt、definitions 与发布默认值。`world-is-agent-adapters` 中每个游戏目录负责游戏 API、Observation、Capability、动作执行和游戏内 UI。

```text
world-is-agent/runtime/config/games/
├── stardew-valley/
│   ├── agent.json
│   └── definitions/
└── rimworld/
    ├── agent.json
    └── definitions/
```

RimWorld 的 `adapters/rimworld/profile/agent.json` 迁入上述发布树，内容沿用 10.3 基线。Stardew 的 `task.enabled=true`，RimWorld 的 `task.enabled=false`；本阶段保持两款游戏已经验证的 prompt 行为目标。

发布树支持 N 份 profile。游戏列表按包含 `agent.json` 的游戏目录发现，game_id 取目录标识，并与 `definitions/game.json` 校验一致；显示名称取其 `title`。发布验证必须证明两款游戏的完整资产都进入 Runtime 二进制。

游戏名称、默认值、定义和旧发布版本的归属映射属于配置数据。Runtime Core 通过 game_id 和结构化配置工作，不按具体游戏名称分支。

### 2.2 Data root

```text
<data root>/config/
├── model.json
├── active-game.json
└── games/
    ├── stardew-valley/
    │   ├── agent.json
    │   └── definitions/
    └── rimworld/
        ├── agent.json
        └── definitions/
```

`active-game.json` 的格式：

```json
{ "game_id": "rimworld" }
```

启动只读取游戏选择与已有资产。首次选择负责准备配置；已准备的游戏不会在启动时因 Runtime 升级被静默改写。模型、密钥、trace、memory 与 task 数据沿用既有 data root 路径。

### 2.3 配置解析与开发覆盖

1. 游戏身份始终由 `active-game.json` 决定；缺少选择时进入 `needs_configuration`。
2. Agent 配置优先读取 `GAMEAGENT_AGENT_CONFIG`，否则读取所选游戏的 `agent.json`。
3. 所有 Runtime 相对路径继续相对 data root 解析，迁移文件不改变相对路径语义。
4. 环境变量只覆盖内容，不推断或替代 game_id。开发启动脚本、示例和测试 fixture 必须提供游戏选择。
5. 显式选择仍按 §3 准备该游戏的发布资产。环境变量禁止隐式播种，不禁止用户主动准备资产。
6. 覆盖配置中的 `definition_catalog_root` 同样按 selected game 加载并校验；无效时报告错误，不回退到另一份 catalog。

`assets_ready` 仅描述 data root 中该游戏的发布资产完整且可解析；它不证明外部覆盖配置有效，也不表示游戏、Adapter 或模型已经就绪。

## 3. 选择与资产准备

### 3.1 一个选择操作

`POST /api/setup/game` 同时承担选择和缺失资产准备：

```text
校验请求与 game_id
    ↓
准备旧配置迁移与所选游戏的候选文件
    ↓
校验 profile、definitions 与实际配置来源
    ↓
提交缺失资产
    ↓
原子保存 active-game.json
    ↓
尚未初始化：尝试统一初始化
已经初始化：保持本进程配置，计算 restart_required
```

game_id 必须命中发布游戏集合，路径只能由已验证的目录项生成。未知标识和路径穿越输入返回 `invalid_game`，不创建目录。客户端保持单一选择动作，无单独 Install 按钮。

### 3.2 完整性与用户文件

- 完整性检查覆盖游戏 `agent.json`、`definitions/game.json`、发布清单中的必要定义文件，以及本地额外定义的解析与身份校验。
- `assets_ready=true` 要求所需文件齐全且可解析；目录存在本身不构成准备完成。
- 缺失文件从内嵌发布资产补齐；已存在且有效的文件保留用户内容，额外定义保留。
- 已存在但损坏或身份冲突的文件返回 `profile_invalid`，指出具体资产和原因，原文件保持不变。
- 所选 `agent.json` 必须存在；不能使用通用加载器的“文件不存在则返回默认值”通过选择校验。

### 3.3 提交、失败与重试

游戏选择、模型保存与首次初始化共用一个配置写入协调边界。校验状态、准备文件、提交选择与初始化不得被另一次提交交错穿插。状态读取使用一致快照，不持有覆盖整个文件操作过程的写锁。

提交顺序为资产在前、选择在后。文件通过临时文件和安全提交写入；提交前重新检查目标，已有用户文件不被覆盖。保存 `active-game.json` 前再次确认本次所需资产完整。

资产准备、磁盘写入或选择文件替换失败时，原选择保持有效，当前运行组件保持原行为。已成功写入的新文件可供重试复用，完整性按文件实际状态重算。返回明确错误，不把失败选择报告为成功，不以删除用户目录作为回滚手段。

重复选择已保存的游戏是幂等操作。进程中断后，下次显式选择可以继续完成准备；启动读取到缺失资产时报告原因，不自行补齐文件。

## 4. v0.1.0 配置迁移

迁移属于第一次显式游戏选择，覆盖根级 `config/agent.json` 向游戏目录配置的过渡。已经存在可解析的游戏选择时，后续启动与选择不再触发根级旧配置识别；保留旧文件不构成重复迁移的触发条件。

### 4.1 来源识别

普通 v0.1.0 发布版的单一 profile 归属 Stardew Valley。兼容性验证使用该发布版的固定 fixture，检查旧布局、原配置目录和游戏定义身份。旧版本归属映射作为 Runtime 发布配置数据维护，核心代码不按游戏名称分支。

标准路径要求旧 `definition_catalog_root` 相对 data root 解析到 `config/games`，且其中原发布游戏的定义可解析、身份与发行基线一致。不能用新版本目录数量或本次选择值推断旧配置归属。

用户对标准 profile 的 prompt、预算、timeout、task 和 memory 等参数修改完整保留。指向外部 catalog、身份冲突或无法确定归属的根级配置返回 `migration_conflict`，保留原文件并说明冲突路径及需要明确的配置归属。

### 4.2 文件处理

| 条件 | 行为 |
| --- | --- |
| 没有根级旧配置 | 按 §3 准备所选游戏 |
| 已识别旧配置，原游戏目标 `agent.json` 缺失 | 原文件内容完整复制到原游戏目录，原文件保留 |
| 原游戏目标配置已存在且有效 | 目标文件为权威，根级旧文件保留 |
| 原游戏目标配置损坏 | 返回 `profile_invalid`，保留两个文件 |
| 旧配置来源无法确认 | 返回 `migration_conflict`，保存选择前停止 |

迁移原游戏与选择另一个游戏可以发生在同一次操作中，两者归属独立。旧 Stardew 用户首次选择 RimWorld 时，旧配置仍进入 `stardew-valley`，RimWorld 使用自己的 profile。

迁移成功后，默认解析只使用 `games/<game>/agent.json`。根级旧文件不再作为隐式后备来源；通过 `GAMEAGENT_AGENT_CONFIG` 显式引用时仍遵循覆盖规则。

模型配置、密钥、历史与任务数据保持原位置和内容。自定义 store 路径保留其 data root 相对语义。迁移冲突修复后可重新提交选择，无需重新填写有效模型配置。

## 5. 初始化与配置生效

### 5.1 统一首次初始化

进程先建立 data root、日志、trace、本地 HTTP 与 gRPC 入口。游戏或模型未配置时控制面继续可用，`loaded_game=null`，游戏事件尚不准入。

游戏和模型配置均可加载后，统一初始化入口完成：

1. 固定候选 game_id，解析有效 Agent 配置并加载该游戏 catalog。
2. 按该配置创建 memory/history 存储和 Agent Core。
3. 按 task policy 创建 task store、service 与 dispatcher；`task.enabled=false` 时不启动任务组件。
4. 将完整组件挂入 Gateway，统一发布 `loaded_game` 与 Ready 状态。

任务调度和游戏事件只在整体 Ready 后准入。初始化失败时关闭本次未发布资源，`loaded_game` 保持为空，不留下部分启动的任务服务。HTTP 与 gRPC 监听器在同一进程只创建一次。

实施必须协调 `bootstrap.Open / Configure`、`cmd/server/task_runtime.go` 与 Gateway 的生命周期。`AgentConfig`、history store、TaskService、dispatcher 和 loop 都来自同一次候选配置。

### 5.2 三状态与原因

保留 `needs_configuration`、`blocked`、`ready`。增加机器可读的 `reason_code`；`reason` 继续提供面向用户的说明。

| 场景 | 状态 / code | 恢复方式 |
| --- | --- | --- |
| 缺少选择 | `needs_configuration / game_not_selected` | 选择游戏 |
| 保存的 game_id 不在发布集合 | `needs_configuration / invalid_game` | 选择可用游戏 |
| 所选资产缺失 | `needs_configuration / profile_assets_missing` | 再次选择，显式准备资产 |
| 模型配置缺失或不可加载 | `needs_configuration / model_configuration_required` | 配置模型 |
| 所选 profile 或 catalog 损坏 | `blocked / profile_invalid` | 修复所指文件后重启 |
| 选择文件无法解析 | `blocked / game_selection_invalid` | 修复文件后重启 |
| 依赖资源初始化失败 | `blocked / initialization_failed` | 按原因修复后重启 |
| 整体初始化成功 | `ready`，空 reason | 服务游戏事件 |

检查顺序为选择 → 所选资产与配置 → 模型 → 初始化资源。已经 Ready 时，失败的换游戏请求仅返回请求错误，不把当前可用 Runtime 改成 `blocked`。

### 5.3 冻结点与重启

冻结点是首次完整初始化成功。此前 `loaded_game=null`，允许调整选择；其后当前行为固定到进程结束。

```text
configured_game = active-game.json 中已提交的选择
loaded_game     = 已完成初始化、当前实际使用的游戏；初始化前为 null
restart_required = loaded_game != null 且 loaded_game.id != configured_game.id
```

Ready 后选择另一游戏，先准备资产、保存选择，再返回 `restart_required=true`。当前 prompt、catalog、store 配置、task policy 和在途工作使用原 profile。

选回当前游戏时清除重启提示；重复选择当前游戏保持幂等。重启读取 `configured_game`，从同一 profile 初始化全部组件，成功后两个身份一致。

模型与凭据仍为一份；换游戏不换模型。模型修改沿用 Ready 状态下的 `already_configured` 约束。

## 6. 按游戏加载 definitions

catalog 加载入口显式接收确定的 game_id，仅解析 `definition_catalog_root/<game>/definitions`。root 仍表示各游戏目录的父目录，不通过将 root 指向单个游戏目录绕过加载契约。

所选游戏的 `game.json`、必要 archetype 和发布定义通过身份、schema 与既有唯一性校验。缺少必要定义时不能以空 catalog 到达 Ready。

未选游戏损坏不影响当前游戏初始化。HTTP 游戏列表逐项报告资产状态，一项损坏不阻止列出其它游戏。状态轮询不反复加载全游戏 catalog；资产校验发生在列表读取、选择及初始化的必要边界。

## 7. Gateway 准入与连接状态

### 7.1 Hello 判定顺序

```text
收到 AdapterHello
    ↓
Runtime 尚未 Ready → runtime_not_ready，结束 stream
    ↓
hello.game_id != loaded_game.id → game_mismatch，结束 stream
    ↓
已有连接或正在握手的占位 → adapter_already_connected，结束 stream
    ↓
为本次连接取得唯一占位
    ↓
EnvironmentReady → Capability discovery
    ↓
成功后发布 connected=true
```

拒绝使用现有 `RuntimeMessage.error` 携带明确 code 和说明，随后结束 stream。mismatch 在任何 `EnvironmentReady`、能力发现和事件准入之前判定；不使用 `configured_game` 校验当前连接。

### 7.2 单一状态来源

Gateway 提供只读连接快照，由 HTTP 展示。握手和能力发现完成后才发布连接身份；正常断开、握手失败和取消都释放本连接占位。占位与清理按内部 connection identity 匹配，旧连接延迟清理不得清除新连接。

第二条连接明确拒绝，当前连接保持可用；它结束后允许新连接。尚未 Ready 或 mismatch 都不建立有效会话，也不接收 GameEvent。

`last_connection_error` 单独保存最近一次拒绝的 code、expected_game_id、received_game_id 和说明；错误连接结束后页面仍可解释原因。它仅保留一条进程内记录，下一次成功连接时清除。另一次拒绝不改变已有有效连接的身份。

Adapter 沿用既有连接、重连和错误处理机制；本阶段不把自动重连提升为跨 Adapter 的协议保证。实机验证可使用已有重连入口或重新启动对应游戏。

## 8. HTTP 契约

沿用既有 session cookie、Host 与 Origin 校验。修改请求先检查 Runtime 状态，再解析请求体；在配置写入锁内重新检查状态。`blocked` 请求返回 `setup_blocked`，不写入资产、模型或密钥。

### 8.1 路由

| 路由 | 行为 |
| --- | --- |
| `GET /api/setup/games` | 内嵌游戏列表，每项含 `id`、`title`、`assets_ready`、可选 `assets_error` |
| `POST /api/setup/game` | body 为 `{ "game_id": "rimworld" }`；执行 §3，成功返回完整 status |
| `GET /api/status` | 既有状态字段加 §8.2 的新增字段 |
| `POST /api/setup/model` | 复用现有模型保存流程，保存后进入统一初始化入口 |

未选择游戏时，模型提交返回 `game_not_selected`，在写入密钥前停止。模型保存后不得使用旧的启动默认 profile 构建核心。有效模型的用户通过游戏选择直接触发初始化。

### 8.2 状态字段

| 字段 | 语义 |
| --- | --- |
| `reason_code` | 稳定原因码；Ready 时为 null，`reason` 保留可读说明 |
| `loaded_game` | `{ id, title }` 或 null；实际初始化成功的游戏 |
| `configured_game` | `{ id, title }` 或 null；已保存选择；未知 id 保留标识，title 为 null |
| `restart_required` | 按 §5.3 计算 |
| `adapter` | 未连接时 `{ connected: false }`；连接后含 `connected`、`game_id`、`adapter_id`、`adapter_version`、`game_version`、`session_id` |
| `last_connection_error` | §7.2 的拒绝记录或 null |

无有效连接时不携带过期连接身份。Adapter 身份字段直接来自 `AdapterHello`。既有路径、版本和模型描述字段保留。

快照必须一致，不能出现 `ready=true` 而 `loaded_game=null`，或跨初始化批次的模型与游戏状态。客户端使用 `reason_code` 分类，不解析自然语言说明。

### 8.3 游戏选择响应

| 结果 | HTTP / code |
| --- | --- |
| 选择已保存 | `200`，完整 status；缺模型时为 `needs_configuration` |
| 请求体非法或未知游戏 | `400 / invalid_request` 或 `invalid_game` |
| Runtime blocked | `400 / setup_blocked` |
| 迁移归属冲突 | `409 / migration_conflict` |
| 候选 profile 无效 | `409 / profile_invalid` |
| 资产或选择提交失败 | `500 / game_setup_failed` |

选择成功保存后，初始化失败通过返回的 status 表达 `blocked / initialization_failed`，保留选择，不把初始化失败误称为选择未写入。响应和日志不回传密钥。

## 9. 本地客户端

```text
全新 data root
Choose Game → 准备资产 → Configure Model → Ready

已有可用模型配置、缺少选择
Choose Game → 准备资产 → Ready

已经 Ready
Choose Game → 保存下一次选择 → Restart required
重启 → 新游戏 Ready
```

未初始化时展示已保存选择和下一项可修复配置；Ready 后展示 Current Game 与 Adapter。保存选择与当前游戏不一致时才展示 Next Game 和重启提示。提供“关闭并重新运行 Runtime”的说明，不增加自动重启服务。

选择不要求用户编辑文件或环境变量。`assets_ready` 用于配置准备状态，不显示成游戏或 Mod 已安装。

请求期间禁用重复提交并显示进度；失败保留有效状态、显示原因，重试重新读取服务器。过期 session 沿用现有凭证恢复流程。重新打开页面、重复与多页面提交均以服务器快照为准；迟到响应不能覆盖较新的选择结果。

## 10. 实施工作单元与修改清单

| 工作单元 | 修改范围 | 验证命题 |
| --- | --- | --- |
| 10.4-1 配置与迁移 | `runtime/config/defaults.go`、`runtime/config/games/`、`runtime/internal/definition/` 及测试；迁移 RimWorld profile | 两款游戏可发现，准备可恢复，旧配置保留，catalog 按游戏加载 |
| 10.4-2 首次初始化 | `runtime/internal/bootstrap/`、`runtime/cmd/server/main.go`、`task_runtime.go`、Gateway 组件装配及测试 | 首次选择可启动，全部组件使用同一 profile，失败不留下活动组件 |
| 10.4-3 控制面与准入 | `runtime/internal/httpapi/`、`runtime/internal/gateway/` 及测试 | 写入串行、状态一致、Hello 拒绝与连接清理正确 |
| 10.4-4 用户流程与分发 | `console/web/src/`、启动与发布脚本、测试 fixture、受影响文档 | 新旧用户流程与便携包脱离源码运行成立 |

取消唯一 profile 约束、加入第二份发布 profile 与适配初始化入口作为协调变更落地。内部单元先验证模块行为；对外包来自端到端路径已经通过的完整变更集。

每个单元完成相关测试、直接回归、内部 CR 和 `git diff --check` 后继续。获得用户提交授权时创建本地 commit；GitHub 推送由用户执行。

功能实现并验证后同步 `README.md`、`docs/STATUS.md`、开发与测试指南、Adapter 使用说明和发布 README。公开事实源只描述已验证能力；历史 10.3 证据保留原验收口径。

## 11. 验证矩阵

以下为开发后的通过条件，当前尚无 10.4 实现结果。

| 用例 | 必须断言 |
| --- | --- |
| 两份内嵌 profile | 两款游戏的 agent.json 与完整 definitions 进入发布包；启动不隐式修改资产 |
| 全新目录，经真实 HTTP 配置 | 选择、准备资产、配置模型后整体 Ready，无需重启或编辑文件 |
| 已有有效模型目录 | 只选择游戏即可 Ready，模型和密钥内容保持一致 |
| 初始选择与重复选择 | 初始化前 loaded 为空，完整初始化才冻结，重复选择幂等 |
| 旧目录与自定义参数 | 按原游戏迁移，原文件及自定义值保留，相对路径语义一致 |
| 旧 Stardew 用户先选 RimWorld | 旧配置归 Stardew，RimWorld 的 task policy 与 prompt 来自自己 profile |
| 迁移冲突与目标已存在 | 归属不明时无选择提交；已有有效目标配置保留 |
| 中断与写入失败 | 资产写入和选择提交边界注入失败，旧选择有效，重试完成，用户文件不被覆盖 |
| 并发提交 | 游戏与模型并发、两个游戏并发、重复请求不产生组件或文件错配 |
| definitions 隔离 | 当前游戏缺必要定义会阻止 Ready，其它游戏损坏不影响当前初始化 |
| 环境变量覆盖 | active game 决定身份，覆盖只影响内容，缺选择不能绕过，外部 catalog 校验目标游戏 |
| 初始化故障 | 任一组件准备失败时无 loaded_game、无活动 dispatcher，错误可读 |
| Ready 后切换 | 本进程行为不变，选回当前游戏清除提示，两个方向重启后整套配置一致 |
| Hello mismatch | 返回 game_mismatch，未发送 EnvironmentReady、未请求能力、无事件或任务准入 |
| 未 Ready 与第二连接 | 分别返回 runtime_not_ready、adapter_already_connected，已有连接可用 |
| 断开、失败与重连 | 占位释放、身份清理，旧清理不影响新连接，拒绝记录可见且成功后清除 |
| API 保护 | session、Host、Origin、blocked body 前置拒绝和错误输入按契约处理且无副作用 |
| 浏览器完整流程 | 新旧用户、失败重试、迟到响应、重启提示和凭证恢复正确 |
| 发布包 | 从仓库外任意工作目录运行，内嵌客户端与两份配置可用，不依赖源码路径 |

后端复用 Go 测试框架，Gateway 使用真实 gRPC 流与受控 Adapter fixture 验证时序。模型使用现有受控 provider 验证配置装配，fake 结果不记为实机结论。

开发回归入口：

```powershell
go test ./runtime/config ./runtime/internal/bootstrap ./runtime/internal/definition ./runtime/internal/httpapi ./runtime/internal/gateway ./runtime/cmd/server
go test ./... -p 1 -count=1
npm --prefix console/web run type-check
npm --prefix console/web run build
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
powershell -ExecutionPolicy Bypass -File scripts/release-runtime.ps1
git diff --check
```

新旧目录、重启与便携包使用实际进程验证；用户流程通过真实浏览器验证。HTTP 单元测试不替代进程装配与浏览器验证。本阶段不引入新的测试服务或云 CI。

## 12. 统一人工验收与退出条件

10.4 自动验证完成后可继续 10.5，最终在完整交付物上集中验收：

1. 全新目录与已有模型目录均能通过页面完成所需配置。
2. 选择 Stardew Valley，真实 Adapter 连接并完成一次对话。
3. 保存 RimWorld 选择，当前游戏身份保持不变，页面显示重启提示。
4. 重启 Runtime，真实 RimWorld Adapter 连接并完成一次对话。
5. 按反方向验证 RimWorld → Stardew Valley，profile 与连接身份一致。
6. 故意连接与当前游戏不符的 Adapter，页面可见 `game_mismatch`，错误会话不执行游戏动作。

自动验证与上述实机记录全部完成后，10.4 才可标记 Accepted。开发中不要求重复 10.3 的 Pawn、存读档和 caravan 完整验收；历史豁免保持原口径。

## 13. 范围限制

- 一个 Runtime 进程使用一个 profile、接受一个 Adapter 会话。
- 游戏由用户显式选择，运行后的切换在重启时生效。
- 模型与凭据跨游戏共用。
- 游戏与 Adapter 的下载、安装检测、依赖体检和自动安装不属于本阶段。
- 自动资产版本 reconcile、游戏行为扩展、共享 Adapter 业务框架、第三款游戏、自动重启服务、桌面壳及新平台发行不属于本阶段。
