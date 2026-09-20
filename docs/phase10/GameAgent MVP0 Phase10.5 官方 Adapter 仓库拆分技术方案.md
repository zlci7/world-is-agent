# GameAgent MVP0 Phase10.5 官方 Adapter 仓库拆分技术方案

> 方案状态：实现与自动验证已完成，源码已推送 GitHub；用户报告 10.4/10.5 游戏实机基线测试通过。运行中切换与模型设置按 [Live Settings 规格](../superpowers/specs/2026-09-20-runtime-live-settings.md) 另行验收。
> 日期：2026-09-20。
> 上位文档：[Phase10 总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md)。
> 前置：[10.4 Game Profile 选择方案](GameAgent%20MVP0%20Phase10.4%20Game%20Profile%20选择与多游戏产品形态技术方案.md) 的实现与自动验证完成。
> 产品验收：10.4 与 10.5 在完整交付物上集中验收，GitHub 上传由用户执行。

## 1. 目标与仓库结构

两个官方 Adapter 位于本地 `world-is-agent-adapters`，GitHub 远端为 [world-is-agent-adapters](https://github.com/zlci7/world-is-agent-adapters)。Runtime、客户端、Protocol 和游戏行为配置保留在 `world-is-agent`。

```text
world-is-agent/
├── runtime/
│   └── config/games/
│       ├── stardew-valley/
│       │   ├── agent.json
│       │   └── definitions/
│       └── rimworld/
│           ├── agent.json
│           └── definitions/
├── console/
├── protocol/
├── scripts/
└── docs/

world-is-agent-adapters/
├── AGENTS.md
├── README.md
├── LICENSE
├── stardew-valley/
│   ├── src/
│   ├── assets/
│   ├── tests/
│   ├── scripts/
│   ├── GameAgent.Stardew.csproj
│   ├── manifest.json
│   ├── VERSION
│   ├── protocol.version
│   └── README.md
└── rimworld/
    ├── src/
    ├── About/
    ├── Patches/
    ├── tests/
    ├── scripts/
    ├── WiaRimWorld.csproj
    ├── VERSION
    ├── protocol.version
    └── README.md
```

未来官方 Adapter 按 game_id 增加独立目录，实际接入时才创建。社区 Adapter 可以使用自己的仓库，并遵守版本化 Protocol 契约。

## 2. 所有权与兼容边界

| 内容 | 归属 |
| --- | --- |
| Agent、Context、Memory、Task、Tool、Gateway | 主仓库 |
| 本地客户端、Runtime 构建与发布 | 主仓库 |
| Protocol 定义、Go 生成代码、版本契约与协议检查 | 主仓库 |
| Game Profile、prompt、definitions、旧配置迁移数据 | 主仓库 |
| 游戏 API、Observation、Capability、游戏内 UI 与动作执行 | Adapter 对应游戏目录 |
| Adapter 依赖、单元测试、游戏资产、安装与打包 | Adapter 对应游戏目录 |

每款游戏保留独立工程、依赖版本、构建入口、测试与安装包。Stardew 保留 `net6.0 + Grpc.Net.Client`，RimWorld 保留 `net472 + Grpc.Core` 及其 native 库布局。共享仓库不引入 Adapter 业务框架，也不合并游戏实现或 gRPC transport。

每游戏目录内的构建、测试、打包和安装入口保持自足。仓库根部 `scripts/` 可在实际需要时提供可选的统一调用或仓库级检查；各游戏工程和脚本不反向依赖它。仅取某个游戏目录并提供已声明的协议、工具链与游戏构建依赖，即可完成独立验证。

共享基础设施的边界限于构建、发布和仓库检查。Observation、Capability、transport、游戏执行和 UI 保持各 Adapter 独立实现；本阶段不预建尚无实际用途的共享工具层。

Mod 的 `UniqueID`、`EntryDll`、`AssemblyName`、安装目录和游戏命令保持兼容；现有 C# 工程名、namespace、Go module、proto package 与生成命名空间不变。10.5 不修改协议内容与游戏行为。

## 3. 迁移清单

| 当前路径 | 目标或处理方式 |
| --- | --- |
| `adapters/stardew/` | Adapter 仓库 `stardew-valley/`，包括源码、资产、测试与 Mod manifest |
| `adapters/rimworld/` | Adapter 仓库 `rimworld/`，包括源码、About、Patches、测试和 native 装载相关实现 |
| `scripts/install-stardew-adapter.ps1` | Adapter 仓库 `stardew-valley/scripts/install-stardew-adapter.ps1` |
| `scripts/install-rimworld-adapter.ps1` | Adapter 仓库 `rimworld/scripts/install-rimworld-adapter.ps1` |
| Adapter 专属检查与安装 fixture | 对应游戏 `tests/` 或 `scripts/` |
| `runtime/config/games/` | 保留主仓库，由 10.4 先完成 RimWorld profile 归位 |
| `protocol/` 与协议生成脚本 | 保留主仓库，通过版本化依赖提供给 Adapter |
| Runtime 与客户端的启动、构建和发布脚本 | 保留主仓库 |
| Runtime 集成测试、通用协议 fixture | 保留主仓库；Adapter 测试只依赖自身源码、声明的协议与游戏构建依赖 |
| 已有许可证及必要第三方声明 | 随所属内容保留，Adapter 仓库提供清晰的许可证入口 |
| 构建缓存、临时输出、机器配置与密钥 | 不进入新仓库或安装包 |

迁移覆盖已跟踪的 Adapter 文件，保留适用的 dotfiles、fixture 和既有测试证据。涉及仓库外依赖的测试必须明确依赖入口；不能通过隐藏的父目录回退使验证假通过。

主仓库的历史阶段与验收记录保留原始路径上下文。当前开发指南、安装说明和公开入口在迁移验证通过后更新为新位置。

## 4. Protocol 依赖

Protocol 定义的唯一权威来源是 `world-is-agent/protocol/`。每个 Adapter 声明协议族和已测发布版本，当前基线为：

```text
Requires WIA Protocol v1alpha2
Tested against protocol-v1alpha2.0
```

每游戏目录的 `protocol.version` 是已测协议版本的机器可读来源，内容为一行 tag：

```text
protocol-v1alpha2.0
```

各游戏独立验证脚本读取本目录的 pin，README 的获取与构建示例保持一致；脚本不能另行写死一个不同 tag。两个 Adapter 可分别升级已测协议版本，升级需完成该 Adapter 的兼容验证。`protocol.version` 描述协议依赖，`VERSION` 描述 Adapter 自身发行版本。

构建通过显式 `-p:WIA_PROTOCOL_DIR=<path>` 或同名环境变量读取固定版本的 proto。相对路径便利值若保留，只能表示当前 Adapter 的明确本地配置，不能要求另一个仓库位于某个兄弟目录。

独立构建验证从 pin 指定的 tag 导出协议源码，记录解析后的 commit，并校验显式协议目录中 `proto/gameagent.proto` 的内容与该版本一致。验证不能仅比较 tag 名称；若该协议引入其它本地 proto，内容校验覆盖它们。C# 类型继续由各自的 Grpc.Tools 在构建时生成；不维护可独立修改的第二份协议。

本地现有 tag 可用于离线验证。面向外部用户的说明给出明确的版本获取步骤；tag 和 GitHub 远端的发布由用户完成。尚未上传的远端结果不得记为已发布。

## 5. 构建、安装与打包

### 5.1 每个游戏独立运行

每款 Adapter 的脚本从自身位置定位工程，接受显式协议目录和所需游戏安装路径。调用者工作目录不影响依赖解析、输出目录或安装目标。

- Stardew 继续通过 `GamePath` 提供游戏及 SMAPI 的构建依赖；本机 DLL 不进入公开分发。
- RimWorld 继续使用已固定版本的引用程序集构建，构建不依赖游戏安装目录；真实安装仍需明确指定游戏或 Mods 目录。
- 安装脚本保留目标检查、产物预检、游戏运行状态检查和必要 native/patch 文件的完整装配。
- 测试工程、静态检查、独立构建探针和 README 使用迁移后的路径。

### 5.2 Adapter 版本与发布命名

每游戏目录的 `VERSION` 为该 Adapter 发行版本的唯一权威来源，内容为不带 `v` 的版本号。迁移基线两款均为 `0.1.0`；各自独立升级，不因迁仓自动提高版本或要求另一款同步发布。

| Adapter | 版本文件 | Git tag | 安装包 |
| --- | --- | --- | --- |
| Stardew Valley | `stardew-valley/VERSION` | `stardew-valley-v<version>` | `wia-adapter-stardew-valley-v<version>.zip` |
| RimWorld | `rimworld/VERSION` | `rimworld-v<version>` | `wia-adapter-rimworld-v<version>.zip` |

GitHub Release 按对应游戏 tag 发布该游戏的安装包。tag 虽指向整个仓库的一次提交，其名称和发布资产只声明对应 Adapter 的版本；另一游戏不因此产生新发行版。主仓库的 Runtime 与 Protocol 版本继续使用自己的版本契约。

版本从 `VERSION` 进入构建和发布产物：

- 构建注入程序集信息版本 `AssemblyInformationalVersion`；`AdapterHello.adapter_version` 使用该构建生成值，不由用户配置中的版本字符串决定。
- Stardew 安装包内 `manifest.json` 的 `Version` 由该版本生成，并与程序集信息版本及 Hello 上报值一致。
- RimWorld 使用程序集信息版本及 Hello 上报值表达 Adapter 版本。`About.xml` 的 `supportedVersions` 表示支持的游戏版本，保持原有游戏兼容含义。
- 打包验证检查 `VERSION`、生成的版本信息、适用的 Mod 版本字段和包名；为发行 tag 构建时，还需验证 tag 中的游戏标识和版本。任一不一致时停止打包，不产生标为成功的发布产物。

版本来源统一属于构建与身份信息装配，不改变 Mod 标识、游戏行为或协议字段。

### 5.3 安装包

每款游戏独立产出 §5.2 规定的安装包及清晰的本地输出路径。发布产物记录 Adapter 版本、源码 commit、Protocol pin 与解析后的协议 commit，验证记录包含协议内容校验结果。

已有安装脚本的装配规则作为打包依据，产物验证至少覆盖：

| Adapter | 必要产物 |
| --- | --- |
| Stardew | 现有 Mod DLL、manifest、assets 与声明的运行依赖 |
| RimWorld | About、Patches、managed Assemblies、正确命名和位置的 native DLL |

包内不含游戏程序集、源码构建缓存、机器专属配置、密钥或 Runtime 用户数据。Runtime 便携包独立构建，内嵌客户端与两套 Game Profile；不依赖 Adapter 源码目录。

沿用本地脚本作为验收入口，不引入 GitHub Actions、自动上传或签名服务。

## 6. 本地迁移顺序

1. 固定通过 10.4 自动验证的源 commit，确认待迁移文件、许可证和依赖版本。
2. 在独立目录建立 `world-is-agent-adapters`；已有非空目录保留并报告冲突，不能覆盖现有项目。
3. 导入两个游戏目录和专属脚本，补齐各自 `VERSION` 与 `protocol.version`，调整路径、版本装配、规则、README、构建与打包入口。
4. 两个 Adapter 在没有主仓库源码、另一款游戏目录和仓库根部辅助脚本的环境中完成独立构建、测试与打包验证；所需协议通过显式路径提供。
5. 保存 Adapter 仓库的本地迁移提交，记录源 commit 和路径映射，保留迁移追溯信息。
6. 从主仓库移出对应文件，更新当前引用和检查入口，完成 Runtime 与客户端回归及便携包验证。
7. 保存主仓库的本地迁移提交，集中进行两款游戏的最终实机验收。

主仓库保留完整既有 Git 历史，迁移不改写或强制覆盖历史。原文件退出主仓库前，必须有已验证并保存的 Adapter 仓库副本。两个仓库的本地 commit 不要求逐项人工验收；创建 commit 仍须处于用户授予的提交范围内。

GitHub 建仓、推送、tag 发布和 Release 上传由用户执行。本阶段自动工作交付可供验收的本地仓库与发布产物。

## 7. 文档与检查入口

| 文件或范围 | 修改内容 |
| --- | --- |
| 主仓库 `AGENTS.md` | 固定两个仓库名称、所有权、独立版本与协议 pin、统一验收方式 |
| Adapter 仓库 `AGENTS.md` | 每游戏入口自足、共享基础设施边界、版本和 tag 规则、Runtime 内部依赖禁令、兼容标识、验证与提交规则 |
| 两个仓库 README | 各自职责、安装与开发入口、已验证范围 |
| 每游戏 README | VERSION、Protocol pin、tag 与包名规则、游戏依赖、构建、测试、打包和安装命令；Runtime Ready 后启动游戏及现有重连方式 |
| 主仓库 `protocol/README.md` | 版本依赖获取与 Adapter 文档入口 |
| 主仓库开发与测试指南、发布说明 | 迁移后的工作流、单独安装 Adapter 和本地 Runtime 包 |
| 主仓库架构检查 | 保留 Runtime game-agnostic 和协议检查；Adapter 内部依赖检查在新仓库有实际执行入口 |
| Phase10 总方案 | 状态、执行顺序、完成条件与两仓职责 |

公开文件在对应实现验证后更新；不得提前把规划目录、未运行的安装包或未上传的 GitHub 仓库描述为当前已发布能力。

## 8. 验证矩阵

以下为实现后的验证矩阵。Stardew 独立验证通过 494 项测试及静态检查；RimWorld 独立验证通过 42 项测试、standalone build、打包、临时安装与负向探针。主仓库 `go test ./... -p 1 -count=1`、客户端 type-check/order、`npm ci` 与 build、Protocol static 与 generation、架构与 launcher 检查均通过。Runtime 便携包在独立临时目录启动并验证内嵌 UI、两套 profile、Stardew → RimWorld → Stardew 重启切换、Ready 状态下当前 profile 冻结与 pending selection。文件 symlink fixture 因当前主机权限无法创建，junction 覆盖通过。用户已报告游戏实机基线测试通过；本文未逐项补记用户未提供的日志或负向探针证据。

| 验证 | 通过条件 |
| --- | --- |
| 文件清单 | 两个 Adapter 的必要源码、资源、测试与许可证完整；原 Mod 标识保持一致 |
| 固定协议版本 | 从每游戏 protocol.version 读取 pin，记录解析 commit 并验证 proto 内容；当前基线为 protocol-v1alpha2.0，不依赖主仓库 HEAD |
| Stardew 独立构建 | 仅该目录、固定协议与显式 GamePath 即可构建；不需要 RimWorld 或 Runtime 源码 |
| RimWorld 独立构建 | 仅该目录与固定协议即可构建；不需要游戏安装、Stardew 或 Runtime 源码 |
| 入口自足 | 仅保留对应游戏目录和显式外部依赖时，构建、测试、打包入口均可运行；不依赖仓库根部辅助脚本 |
| 协议负向探针 | 无效 WIA_PROTOCOL_DIR 按该路径失败；缺失或无效 pin、无法解析 tag、协议内容与 pin 对应版本不符时验证失败 |
| Adapter 回归 | 两个游戏各自完整测试套件与静态检查通过 |
| 版本一致性 | VERSION、程序集信息版本、Hello 上报版本、Stardew manifest.Version 与包名一致；RimWorld 支持的游戏版本保持独立含义 |
| 版本负向探针 | 产物版本不一致或提供了错误游戏/版本的发行 tag 时打包失败；临时 fixture 升级一款 Adapter 不改变另一款的版本与包名 |
| 脚本路径 | 从仓库外工作目录构建、测试、准备安装包并安装到临时目标成功 |
| 安装包完整性 | 必需文件和 native 布局正确，不含游戏 DLL、密钥或机器数据 |
| 主仓库独立性 | Adapter 目录退出后 Go 测试、客户端构建、协议与架构检查、Runtime 发布全部通过 |
| 公开入口 | 当前 README、开发指南、安装说明的命令与链接可用，规划和已发布事实区分明确 |
| 实机连接与对话 | 两个迁移后构建的 Adapter 分别在 Runtime Ready 后连接所选游戏并完成一次对话；Hello 版本与对应包一致 |
| 切换与拒绝 | 两个方向的重启切换正确；开错游戏返回可读 `game_mismatch` |

本地验证复用各游戏 `tests/check-standalone-build.ps1` 与现有测试工程；脚本迁移后同时验证生产工程和测试工程的依赖路径。Go 全量回归、客户端 type-check/build、架构检查与发布脚本在主仓库独立执行。

实机检查与 10.4 共用一次最终验收，不重复整套 10.3 游戏机制验收。自动化、内部自查与实机结果分别记录；未执行项保留原因，不能按通过记账。

## 9. 退出条件

1. 本地存在两个独立 Git 仓库，名称和职责符合 §1、§2。
2. 两个 Adapter 都已迁出主仓库，源码、测试、安装与打包入口完整。
3. 主仓库和两个 Adapter 的独立构建与回归全部通过，单游戏入口自足，机器可读的协议依赖可复现。
4. 两款游戏的安装包经完整性和版本一致性检查，tag 命名与独立发行规则成立，Runtime 便携包可脱离源码运行。
5. 10.4 与 10.5 的统一实机验收完成，连接、对话、切换和 mismatch 结论有记录。
6. 当前文档与本地事实一致，10.3 历史验收口径和兼容标识保持不变。
7. 本地迁移提交和发布产物可交付用户验收；GitHub 上传状态单独记录，不作为已自动完成事项。

实现与自动验证完成不等于 Accepted。用户已报告实机基线通过；逐项验收证据以实际提供的记录为准。两个仓库源码均已推送 GitHub；10.5 安装包仍为本地验收产物，未创建对应远端 Release。
