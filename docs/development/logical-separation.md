# Phase A：逻辑分离（Logical Separation）开发流程说明

> **Status:** In Progress — A1/A2 已完成并验证；A3/A4/A5 见 §3
> **Date:** 2026-09-18
> **Scope:** 解除 Stardew Adapter 对 monorepo 目录布局的依赖；不拆仓
> **Predecessor:** 拆仓讨论（`wia-adapter-*` 命名、Phase A / Phase B 划分）
> **Related:** [guide.md](guide.md)、[testing.md](testing.md)

## 0. 已确认的范围与决策（2026-09-18）

用户已锁定本轮范围，共 5 项，不引入 GitHub Actions：

```text
1. WIA_PROTOCOL_DIR 外部依赖点
2. protocol-v1alpha2.0 版本契约与文档
3. install-stardew-adapter.ps1 的 -ProjectPath / -GamePath 参数化
4. check-standalone-build.ps1 独立构建验收
5. 暂不引入 GitHub Actions
```

| 决策 | 结论 |
| --- | --- |
| CI | 选 (a)：只做本地 `check-standalone-build.ps1`，作为唯一验收入口；以后加 CI 直接调用同一脚本 |
| Protocol tag | 选 (a)：`protocol-v1alpha2.0`，文档同时写“协议族”（v1alpha2）与“已测发布版本”（protocol-v1alpha2.0） |
| `WIA_PROTOCOL_DIR` 默认值 | 选 (a)：保留本地便利默认值，但仅为 convenience fallback，不构成正式依赖；独立构建验证必须显式指定协议路径 |

依赖解析优先级：

```text
显式 MSBuild 参数 / 环境变量 WIA_PROTOCOL_DIR
        ↓
monorepo 相对路径 fallback（仅本地开发便利）
```

MSBuild 属性名大小写不敏感，`-p:WIAProtocolDir=...` 与 `-p:WIA_PROTOCOL_DIR=...` 等价；文档统一写作 `WIA_PROTOCOL_DIR`。

### 0.1 实施状态

| 模块 | 状态 | 验证证据 |
| --- | --- | --- |
| A1 Protocol 显式依赖点（主项目） | ✅ 完成 | 默认 fallback 构建 0 警告 0 错误；反证：`WIA_PROTOCOL_DIR` 指向不存在目录时构建失败 |
| A2 Protocol 显式依赖点（测试项目） | ✅ 完成 | `ProtocolMapper.Tests` 109 通过 / 0 失败；同样通过反证 |
| A3 协议版本契约 | ✅ 完成（tag 待用户创建） | `protocol/README.md` 新增；`check-protocol-static.ps1` 通过 |
| A4 安装脚本参数化 | ✅ 完成 | 以临时 `-ModsPath` 运行成功，安装产物落在临时目录 |
| A5 脱离仓库构建验证 | ✅ 完成 | `check-standalone-build.ps1` 通过：临时目录正向构建成功 + 负向探针失败 |

### 0.2 实施中发现并修复的问题

前两项由 A5 验证脚本在实施过程中暴露，属“默认值掩盖显式依赖”的同类缺陷：

| # | 问题 | 证据 | 修复 |
| --- | --- | --- | --- |
| 1 | `WIA_PROTOCOL_DIR` 环境变量未生效，构建落回仓库相对 fallback | 临时目录构建报 `adapter\..\..\protocol\proto ... directory does not exist` | A5 校验脚本改为在构建前于当前进程设置环境变量，并保留负向探针断言属性被真实使用 |
| 2 | `GamePath` 在 csproj 中是**无条件赋值**，会覆盖显式传入与环境变量 | 临时目录构建出现 204 个 `CS0246`（找不到 `NPC` / `IMonitor` / `GameTime` 等游戏类型） | `GamePath` 改为条件赋值，与 `WIA_PROTOCOL_DIR` 相同的分级顺序 |
| 3 | 测试宿主在本环境崩溃（`Win32Exception (5)` 于 `SetParentProcessExitCallback`） | 基线同样失败，非本次改动引入 | 属环境限制；`dotnet test` 需要放开文件沙箱，否则如实记为未执行 |

---


## 1. 目标与不做什么

### 1.1 目标

让 Stardew Adapter 能在**不知道 `world-is-agent` 仓库目录结构**的前提下独立构建。仓库拆分只是组织形式，真正的边界是：

> Runtime 与 Adapter 能否在不知道彼此源码目录结构的情况下独立构建、发布和运行。

本阶段结束时，Adapter 在物理上仍在同一仓库，但**逻辑上已经可以独立存在**。

### 1.2 本阶段不做

```text
不拆仓                    不创建 wia-adapter-stardew-valley 仓库
不改协议内容              proto 字段、包名、csharp_namespace 全部不动
不改 Mod 运行时标识       UniqueID / EntryDll / AssemblyName / AssemblyTitle 不动
不重命名目录              adapters/stardew 路径保持
不新增 CI 系统            只做本地验证脚本，不引入 GitHub Actions（见 §0）
不重构 Runtime            runtime/ 与 protocol/ 的 Go 侧不属于本阶段
```

`UniqueID` 与 `EntryDll` 是已安装 Mod 的运行时身份，改动会影响用户现有安装，必须作为独立授权项处理，不随本阶段顺带执行。

---

## 2. 当前事实基线

本方案基于以下已核实事实，不依赖假设：

| 项目 | 事实 |
| --- | --- |
| CI | 仓库**没有任何 CI**（无 `.github`，无其它 CI 配置） |
| Adapter 规模 | `adapters/stardew` 跟踪 143 个文件（另有 `bin/`、`obj/` 构建产物） |
| 真实耦合点 | 只有 2 处 `.csproj` 用相对路径引用 proto |
| Runtime 耦合 | `runtime/` 的 Go 代码**零处**引用 `adapters/`（`check-architecture.ps1:63` 强制禁止） |
| 协议生成 | Stardew 侧用 `Grpc.Tools` **构建期生成**，不提交生成物；Go 侧生成物在 `protocol/gen/go/` |
| 版本标签 | `git tag` **为空**，协议从未有版本化的发布物 |
| 工具链 | 本机 `dotnet 6.0.428` 可用 |
| 游戏路径 | 实际位于 `E:\SteamLibrary\steamapps\common\Stardew Valley`；`.csproj` 默认路径在本机**不存在**，构建必须显式传 `-p:GamePath` |

因此本阶段的实际工作量很小：**2 个 csproj 的引用方式 + 1 个安装脚本 + 1 个验证脚本 + 1 份版本契约**。

---

## 3. 工作模块

每个模块独立提交，提交信息使用 `chore(adapter):` / `docs(protocol):` 等前缀。模块内完成测试、直接受影响回归、`git diff --check` 后提交。

### A1 — Protocol 显式依赖点（主项目）

**改动**：`adapters/stardew/GameAgent.Stardew.csproj`

- 新增可覆盖的 MSBuild 属性 `WIA_PROTOCOL_DIR`，默认值仅用于本地开发便利：

```xml
<PropertyGroup>
  <WIA_PROTOCOL_DIR Condition="'$(WIA_PROTOCOL_DIR)' == ''">$(MSBuildThisFileDirectory)..\..\protocol</WIA_PROTOCOL_DIR>
</PropertyGroup>
<ItemGroup>
  <Protobuf Include="$(WIA_PROTOCOL_DIR)\proto\gameagent.proto" GrpcServices="Client" />
</ItemGroup>
```

- 文件内不再出现硬写的 `..\..\protocol` 相对路径（仅保留默认值中的一处，且可被覆盖）。

**验收**：

```powershell
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug `
  -p:GamePath="E:\SteamLibrary\steamapps\common\Stardew Valley"
```

通过标准：0 警告 0 错误；且显式传入 `-p:WIA_PROTOCOL_DIR=<临时目录>` 时，构建读取的是传入目录而非默认路径。

**回归**：Stardew Adapter Debug 构建。

### A2 — Protocol 显式依赖点（测试项目）

**改动**：`adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj`

- 同样改用 `$(WIA_PROTOCOL_DIR)\proto\gameagent.proto`。

**验收**：

```powershell
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj `
  --configuration Debug -p:GamePath="E:\SteamLibrary\steamapps\common\Stardew Valley"
```

通过标准：全部通过（当前基线 104 个用例）。

**风险**：这是本阶段**唯一高风险项**——协议类型是 Adapter 与 Runtime 的契约面，此项失败即阻断后续。若失败，先定位是属性解析问题还是 proto 生成问题，不通过扩大超时或跳过断言掩盖。

### A3 — 协议版本契约

**改动**：

- 新增 `protocol/README.md`（当前不存在）：写明协议版本 `v1alpha2`、兼容性规则、变更须 additive、以及生成物范围（Go 提交、C# 构建期生成）。
- `adapters/stardew/README.md` 增加一行依赖声明，表达“依赖协议”而非“依赖仓库目录”：

```text
Requires WIA Protocol v1alpha2 (tag: protocol-v1alpha2.0)
```

**验收**：

- `protocol/tests/check-protocol-static.ps1` 通过（不改断言，仅确认未被破坏）。
- README 声明的版本与 `gameagent.proto` 中 `package gameagent.protocol.v1alpha2;` 一致。

**不做**：不创建 git tag、不推送 tag（见 §0 决策与 §7 待办）。

### A4 — 安装脚本去仓库布局依赖

**改动**：`scripts/install-stardew-adapter.ps1`

- 新增 `-ProjectPath` 参数，默认指向当前 `adapters\stardew\GameAgent.Stardew.csproj` 以保持现有调用不变。
- `-OutputPath` 由 `-ProjectPath` 所在目录推导，不再硬编码 `adapters\stardew\bin\$Configuration`。
- `-ModsPath` 可显式传入；`-GamePath` 保留现有默认行为。
- 脚本内不再出现 `world-is-agent/adapters/stardew` 这类结构假设。

**验收**：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-stardew-adapter.ps1 `
  -ProjectPath adapters/stardew/GameAgent.Stardew.csproj `
  -GamePath "E:\SteamLibrary\steamapps\common\Stardew Valley"
```

通过标准：能正确定位项目、构建产物与 Mods 目标目录；`-ProjectPath` 指向另一目录时脚本同样工作（可用临时副本验证）。

**不做**：不改 Mod 安装目录名 `GameAgentStardew`（属运行时标识）。

### A5 — “脱离仓库构建”验证（评审意见第 4 条）

**交付**：`adapters/stardew/tests/check-standalone-build.ps1`

**验证方法**——关键在于让构建**无法**回退到仓库：

```text
1. 建临时根目录 $TempRoot
2. 复制 protocol  → $TempRoot\protocol
3. 复制 adapters\stardew → $TempRoot\adapter
4. Set-Location $TempRoot\adapter        # 关键：不能在仓库内构建
5. dotnet build GameAgent.Stardew.csproj -p:WIA_PROTOCOL_DIR=$TempRoot\protocol
6. 断言：临时 adapter 树内不存在指向原仓库 protocol 的引用
7. 清理临时目录
```

**通过标准**：

- 在临时目录中构建成功；
- 显式传入的 `WIA_PROTOCOL_DIR` 确实被使用（若属性解析被忽略，本检查必须失败，不能因“默认值恰好可用”而假通过）；
- 临时目录与原仓库之间无相对路径可达关系。

**环境缺失语义**：缺少 dotnet 或游戏 DLL 时，本检查记为**未执行**，不记为通过；如实说明缺失项。

---

## 4. 开发顺序

```text
A1 ──► A2 ──► A5 ──► A3
                ▲
A4 ─────────────┘   （A4 与 A1/A2 无依赖，可并行或最后做）
```

先 A1/A2 建立依赖点（A2 是高风险项，尽早暴露），再 A5 证明结果，然后 A3 把版本契约写进文档。A4 无依赖。

---

## 5. 验收与回归清单

开工前记录基线，收口时逐条执行：

```powershell
# Protocol
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1

# Runtime（确认未被波及）
go test ./... -count=1

# Adapter 构建与测试（本机游戏路径必须显式传入）
$game = "E:\SteamLibrary\steamapps\common\Stardew Valley"
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/Phase9Feasibility.Tests/Phase9Feasibility.Tests.csproj --configuration Debug -p:GamePath="$game"
dotnet test adapters/stardew/tests/RuntimeClient.Tests/RuntimeClient.Tests.csproj --configuration Debug -p:GamePath="$game"

# 静态检查
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1

# 本阶段新增
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-standalone-build.ps1 `
  -GamePath "$game"

# 通用
git diff --check
```

**通过标准**：全部通过，且 `check-architecture.ps1` 的 game-agnostic 断言未被削弱。任一无法执行的检查如实标注原因，不记为通过。

---

## 6. 环境前置条件

| 项 | 要求 | 本机状态 |
| --- | --- | --- |
| .NET SDK | 6.0.x（`net6.0` 目标） | ✅ `6.0.428` |
| Stardew DLL | `Stardew Valley.dll`、`StardewModdingAPI.dll`、`MonoGame.Framework.dll`、`StardewValley.GameData.dll`、`xTile.dll` | ✅ `E:\SteamLibrary\steamapps\common\Stardew Valley` |
| 游戏路径传参 | 通过 `-p:GamePath=...` 或 `GamePath` 环境变量指定；仓库相对默认值在本机不存在 | ⚠️ 必须显式传入 |
| `dotnet test` 运行环境 | 测试宿主需要打开父进程句柄（`OpenProcess`）；文件沙箱拒绝时会以 `Win32Exception (5)` 崩溃 | ⚠️ 需放开文件沙箱后执行 |
| Go | 用于 Runtime 回归与协议生成检查 | 需确认 |
| 实机验证 | 本阶段不要求；A5 为构建级验证，非运行级 | — |

---

## 7. 决策记录

三项决策均已由用户确认，结论见 §0，此处不再重复。仍待用户执行的一项：

| 项 | 状态 |
| --- | --- |
| 创建并推送 tag `protocol-v1alpha2.0` | 待用户执行（tag 属推送类操作，按仓库规则由用户完成） |

协议文档已按“协议族 / 已测发布版本”区分表述；tag 创建后该版本号才真正成为可依赖的发布物。

---

## 8. 风险与回退

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| MSBuild 属性在 `Grpc.Tools` 中解析异常 | A1/A2 直接失败 | A2 提前执行暴露问题；必要时改为在 csproj 顶层规范化绝对路径 |
| 属性被忽略造成 A5 假通过 | 验证失去意义 | A5 必须断言传入值被实际使用 |
| `RuntimeClient.Tests` 自带 `GamePath` 属性 | 全局 `-p:GamePath` 可能被覆盖 | 构建时确认其 `ProjectReference` 解析到传入路径 |
| 改动触及已发布契约 | 破坏兼容 | 本阶段不动协议内容与 Mod 标识，仅改依赖方式 |

**回退**：每个模块独立提交，单独 `git revert` 即可回退；本阶段不改变运行时行为，回退无状态影响。

---

## 9. 退出条件

同时满足即 Phase A 完成：

1. 全仓 Markdown / 脚本 / csproj 中不再存在指向 `protocol/` 的**隐式目录耦合**（除 `WIA_PROTOCOL_DIR` 默认值一处）。
2. `check-standalone-build.ps1` 在临时目录（无仓库回退路径）中构建成功。
3. `adapters/stardew/README.md` 明确声明所依赖的协议版本。
4. §5 回归清单全部通过，或对未执行项给出具体原因。
5. 仓库结构、Mod 运行时标识、协议内容三者均未改变。

Phase B（物理拆仓）仍按触发条件推迟：**第二个真实 Adapter 出现时优先拆**；“Runtime 开始正式 Release”是强信号，但不是必须拆仓的条件。
