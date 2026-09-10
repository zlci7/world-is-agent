# GameAgent MVP0 Phase9.5 联调验收与交付技术方案

> **Status:** Implementation Plan Draft
> **Date:** 2026-09-10
> **执行方式:** 9.1–9.4 的每个独立审查单元均已完成增量本地提交与独立任务 CR 后，执行最终联调、整分支/系统 review、修复和复验；不省略开发过程中的测试或 CR。
> **Goal:** 交付真实游戏/真实模型中可复现的预约最小闭环，以及可核验的代码与验收记录。
> **Architecture:** Runtime Task、Environment Action、Interaction、Checkpoint 和 History 各自保持权威边界。
> **Tech Stack:** Go / SQLite / gRPC、C# / SMAPI、既有真实模型配置。
> **Spec:** [Phase9 总方案](./GameAgent%20MVP0%20Phase9%20技术开发与验收方案.md)
> **前置:** [9.1](./GameAgent%20MVP0%20Phase9.1%20Runtime%20Task%20内核技术开发方案.md)、[9.2](./GameAgent%20MVP0%20Phase9.2%20Runtime%20Tools%20与调度接入技术开发方案.md)、[9.3](./GameAgent%20MVP0%20Phase9.3%20Stardew%20行动与交互技术开发方案.md)、[9.4](./GameAgent%20MVP0%20Phase9.4%20检查点与结果记忆技术开发方案.md)

## 1. 增量开发与最终 review 的规则

- 9.1–9.4 每个独立审查单元先完成失败测试、实现、聚焦验证和阶段回归，运行 `git diff --check` 后创建一个包含实现及其测试的本地提交，并交由独立任务 CR。CR 修正以独立 `fix:` 本地提交提交；通过 CR 后自动继续，不等待用户逐项确认。
- 测试失败先定位原因并直接修正相关逻辑；不能跳过、删除断言、扩大超时掩盖问题。
- 跨地图、原生恢复和 Saving 交接的 9.0 可行性门必须有真实证据。缺少游戏环境时可以完成不依赖它的内核/协议工作，但不能把 fake 通过记为该门通过。
- 环境/权限/产品规则真的阻断时，说明具体缺失和已完成内容；不自行删减跨天、检查点或原生行为恢复需求。
- 本阶段在全部增量任务 CR 后执行最终整分支/系统 review；此前自动测试、开发者自查和实机探针仍持续进行。增量 CR 不替代最终 review 或真实游戏验收。
- 使用专用测试存档；不为了验收删除用户现有存档、Task DB 或 Memory DB。故障注入使用临时副本和测试实例。
- 每个提交保持可构建、可测试；协议生成文件与其协议变更必须处于同一个提交。允许按上述流程自动创建本地提交，但未获用户明确授权不得 `git push`；不提交模型密钥、真实存档、运行数据库、构建输出或含隐私的完整日志。

## 2. 交付文件与测试范围

| 文件 | 内容 |
| --- | --- |
| `runtime/internal/gateway/task_e2e_test.go` | 新增端到端 gRPC + SQLite + fake environment/model 闭环 |
| `runtime/internal/task/restart_process_test.go` | 新增独立进程崩溃、锁释放和 working head 恢复 |
| `adapters/stardew/tests/TaskExecution.Tests/` | 补齐跨能力租约、到达与截止竞争、保存和 UI 组合用例 |
| `scripts/check-architecture.ps1` | 保持 game-agnostic 检查，覆盖 Runtime Tool 执行分流 |
| `adapters/stardew/tests/check-context-static.ps1` | 新 schema、Mapper、Observation 与资源装配 |
| 专用演示/本地任务配置、`adapters/stardew/assets/landmarks.json` | 演示/本地配置显式启用任务功能，仓库默认配置保持 `enabled=false`；使用已验证点位且不携带凭据 |
| `docs/phase9/GameAgent MVP0 Phase9 开发与验收记录.md` | 执行时创建；基线、命令结果、实机证据、review 与最终限制 |

验收记录从真实执行结果生成，不复制技术方案的预期输出作为证据。9.1–9.5 的开发状态可记录 Completed；Phase9 Accepted 仍以最终通过条件和负责人确认决定。

## 3. 自动化验收单元

### 9.5-A：端到端与并发

- [ ] fake model 用不同于 Stardew 的校验/导航/等待工具名称，完整执行 create → due → travel → wait → evidence → result。
- [ ] 增加无模型确证收尾、有效到达对话、Task 终态与 UI 生命周期独立的断言。
- [ ] 当前 Turn 结束后推进假游戏时钟，确认新的 turn_id；不要通过延长原 Turn 实现等待。
- [ ] queue full、MarkEnqueued 失败、重复 Evidence、ActionResult/Event 竞争只产生一个业务终态。
- [ ] 第二 NPC 与第二 world 交错运行，验证任务、交互、History 和 controller 所有权隔离。
- [ ] 同 run 重启、新 run 读档、损坏引用、未确认保存、旧回调分别走对应路径。

```text
TestTaskEndToEndSuccess
  Turn A：校验 → Runtime create_task → 返回确认 → A 完成
  Clock 到期：Turn B 新观察 → Environment 导航 → 当前等待回执
  B 完成，数据库保留 deadline wake；无等待中的 LLM/Action
  接纳 satisfied：Result R 成功；有效到达来源开启 Turn C
  C 对话完成不代表 UI 关闭；UI 关闭后原生恢复
  后续 Turn D 的请求读取实际 R，不将 A 的承诺当见面事实

TestTaskEndToEndNoShow
  同样创建和出发；玩家未满足条件
  等待截止回执 unsatisfied；Task failed，cleanup 幂等
  截止结果提交的模型调用次数为 0
  后续对话事实为真实未见面，不是寻路失败
```

### 9.5-B：独立进程与异常

- [ ] 测试子进程持有临时任务库锁，第二进程拒绝；终止该测试子进程后可以重新打开。
- [ ] 分别在 claim、入队提交、operation 意图落库、世界执行后回执前、快照提交后制造中断。
- [ ] 不确定世界效果必须先 Observe/回执协调，不能盲目重发；不可确认时暂停。
- [ ] 实现所有清理函数幂等测试；旧 owner token 不能停止新 controller。
- [ ] 跑 race 检查；工具链缺失属于未执行，不能写成通过。

任何测试进程必须由测试自己创建并按 PID 管理，只终止该测试实例；不得按进程名批量关闭用户 Runtime 或游戏。

### 9.5-C：全量检查

从仓库根目录执行，各命令检查退出码。任何新建测试必须确实被运行，不能用“匹配到 0 个测试”的成功退出码作为证据。

```powershell
powershell -ExecutionPolicy Bypass -File protocol/scripts/gen-go.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
go test ./runtime/internal/task ./runtime/internal/session ./runtime/internal/tool ./runtime/internal/agent ./runtime/internal/gateway ./runtime/internal/context ./runtime/internal/memory -count=1
go test ./... -count=1
go test -race ./runtime/internal/task ./runtime/internal/session ./runtime/internal/gateway ./runtime/internal/agent -count=1
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj --configuration Debug
dotnet test adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj --configuration Debug
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug
git diff --check
```

全量失败不能只给出“既有问题”结论；需要对照开发前基线确认，并写明是否影响本次通过标准。

## 4. 真实环境与演示

### 4.1 部署

- [ ] 确认真实模型配置、凭据可用，保持现有模型/token 基线；不输出密钥。
- [ ] 确认 task.enabled=true、AgentTargets、已验证 landmarks 和游戏/SMAPI 版本。
- [ ] 按仓库 install-stardew-adapter 流程构建并覆盖 Mod；若执行环境提供同名安装技能，先读取并使用。
- [ ] 对照实际 Mod DLL/资源的路径、构建时间或 hash，确认游戏加载的是本次产物。
- [ ] 使用专用存档，并记录运行入口、任务库和证据位置。

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-stardew-adapter.ps1
go run ./runtime/cmd/server
```

Runtime 启动是持续进程，应保存它的进程/会话身份以便测试结束后准确停止。安装前若游戏占用 DLL，则请用户关闭游戏后重试，不绕过文件占用或关闭其他实例。

### 4.2 成功赴约：连续三次

1. 在普通日使用受控 NPC，输入“后天 15:00 在沙滩见面，等我到 16:00”。
2. 核对 resolve_meeting 成功、create_task 持久成功后才显示确认台词。
3. 真正关闭对话，观察 NPC 按当前原生日程离开；创建 Turn 已终态，任务仍 waiting。
4. 正常跨日保存；三次演示中至少一次退出并加载游戏，核对精确 checkpoint 和任务状态。
5. 约定日玩家不跟随 NPC，验证其真实穿过地图到达；当前移动 Action 与等待登记 Turn 均能结束。
6. 玩家在窗口内到场；核对 satisfied → succeeded 和独立 task_result 来源。
7. 使用有效到达来源执行一次 approach_player，再对话；思考时不走开，不插入原版台词。
8. 最后台词/回复关闭后恢复当前原生日程。再次对话，检查最终模型请求包含实际见面证据。

三次必须是独立有效预约流程，不以重放同一日志、同一 result_id 或重复同一回执计数。

### 4.3 爽约：连续三次

1. 创建同样的未来预约，按正常游戏时间与保存流程推进。
2. NPC 按时到达并等待；玩家不满足到达条件。
3. 截止后 Adapter 自动释放并恢复原生日程；Runtime 接纳 unsatisfied，提交 failed。
4. 检查截止收尾未调用模型；后续对话读取真实爽约结果。

若 NPC 路线失败、晚到或等待被打断，该次不是爽约通过案例，单独进入异常记录。

### 4.4 必测异常与回归

| 场景 | 实机 / 自动化证据 |
| --- | --- |
| 酒馆、广场 | 同一 Runtime 合同可复用语义点位，地点开放规则有效 |
| 节日或信息未知 | 创建明确拒绝；已有任务执行时重新观察条件 |
| 完整窗口跳过、玩家恰好截止到达 | 不虚构实际等待或 met |
| approach 已相邻、目标被占、玩家途中离开 | 合法选点；固定终点；当前相邻关系结果准确 |
| 等待中断线、日终、返回标题 | 本地临时占用收尾，未来 Task 不被任意删除 |
| 同 run 重启 / 新 run 读档 | 分别恢复 working head / 精确快照 |
| 保存后完成、再读旧保存 | Task 回到保存时等待；旧结果不能重新推进 |
| 三选项、自由输入、普通对话、旧 move_to | 原输入合同与交互行为保持 |

正常主路径不使用控制台瞬移、伪造完成事件或改数据库状态。受控跳时只用于异常用例并明确标记。仅日志“arrived/restored”不能替代 NPC 实际移动证据。

## 5. 集中独立 review

### 5.1 输入包

review 输入包含：开发前基线 commit、当前 diff、总方案及五份子方案、实际测试结果、实际部署版本、实机记录和已知限制。review 对照完整改动，不只检查最后修改的文件。

由独立上下文开展一次完整 review；如果没有独立审查环境，则交付可直接审查的输入包并标记待 review，不把开发者自查称为独立 review。

### 5.2 必查项目

1. 分层：TaskService、Runtime Tools、Environment Capabilities 的执行所有权；Core 无游戏专属分支。
2. 事务：Create / Evidence / Result / wake 原子性；queue admission、claim、generation、revision 分工。
3. 生命周期：无数小时 Turn/Action waiter；确定性结案无需模型；不存在 lane 等自己或保存线程互等。
4. 世界执行：跨地图真实行走、固定目标接近、无重复 controller、原生日程恢复。
5. 来源：背景 Task 不获得远程 UI 权限；met 到达交互独立于 Task 终态；旧来源无法越代。
6. 保存：先持久后引用，所有故障窗口有覆盖；unconfirmed 不误恢复；完整状态回退。
7. 认知：Task Result 入 History 不捏造 Turn/台词；Memory 不成为任务执行权威。
8. 回归与交付：测试未删除/跳过，部署产物正确，真实路径证据与声称的能力一致。

### 5.3 修复与复验

- [ ] 对每条发现记录影响、具体位置、复现条件和严重性。
- [ ] 有阻断问题时直接修复相关根因，再跑针对性测试与受影响的全量检查。
- [ ] 修改控制权/时钟/存档/来源逻辑后，重新执行相关实机路径。
- [ ] review 修复后的实际产物重新构建、安装和 smoke；不保留旧 DLL 冒充最终版本。
- [ ] 阻断问题清零后生成最终验收记录；不因为开发时间长或测试困难降低通过标准。

## 6. 最终记录与通过标准

记录字段至少包含：

- 基线、工作区状态、模型/Runtime/游戏/SMAPI/Adapter 版本与关键配置。
- 每个 9.x 的自动化命令、实际退出码、用例数、未执行原因和对应产物。
- 每次实机的 task_id/wake_id/operation_id/result_id、原始游戏时间与位置、checkpoint_id、交互结束和恢复证据。
- 后续模型请求中的实际结果来源；密钥和无关个人信息脱敏。
- review 发现、修复位置、复验结果、最终未解决限制。

代码完成、自动化通过、实机通过、独立 review 通过四项分别记录。全部达到总方案通过标准后，才提交 Phase9 Accepted 的确认材料。

最终交付至少满足：

- [ ] 9.1–9.4 合同及总方案验收矩阵均覆盖，真实成功/爽约各连续三次。
- [ ] 跨天持久任务、到期认知、真实游戏效果、结果记忆和存档回退完整成立。
- [ ] NPC 不因对话、断线、任务截止或保存而永久停在原地。
- [ ] 现有 Phase6/Phase8 行为回归通过，无阻断 review 问题。
- [ ] 未执行项和已知限制明确；没有把 Phase10 的自动恢复能力写入本阶段已实现清单。
