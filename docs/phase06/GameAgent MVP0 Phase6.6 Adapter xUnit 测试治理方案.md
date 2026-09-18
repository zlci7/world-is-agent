# GameAgent MVP0 Phase6.6 Adapter xUnit 测试治理方案

## 1. Phase6.6 目标

Phase6.6 只负责 Stardew Adapter 测试框架治理。

核心目标：

- Stardew Adapter 侧测试从 `Program.cs + 手写 Assert + dotnet run` 迁移到 xUnit.net。
- Adapter 测试统一使用 `dotnet test` 运行。
- 测试失败时可以定位到具体 test method。
- Runtime Go 侧继续使用 Go 标准库 `testing` 与 `go test`。
- 不引入 SMAPI 真机自动化。

Phase6.6 是测试治理阶段，不承载 Phase6.5 对话功能修复。

## 2. 当前测试现状

当前 Stardew Adapter 有三个独立测试项目：

```text
adapters/stardew/tests/ActionCancellationRegistry.Tests
adapters/stardew/tests/PlayerInteractProbe.Tests
adapters/stardew/tests/ProtocolMapper.Tests
```

迁移前形态：

```text
OutputType=Exe
Program.cs
static Assert(...)
dotnet run --project ...
```

这套方式可以验证纯逻辑，但缺少正式测试框架能力：

- 没有 test discovery；
- 没有具体 test case 名称；
- IDE 测试面板无法自然识别；
- 单个用例过滤不方便；
- 大型 `Program.cs` 失败定位成本高。

## 3. 框架选择

Adapter C# 侧采用 xUnit.net。

选择原则：

- 使用 .NET 社区常见测试框架；
- 与 `dotnet test`、IDE test explorer 兼容；
- 保持测试代码轻量；
- 不引入额外 assertion 风格依赖；
- 不要求本地 Stardew 游戏安装路径。

Phase6.6 默认依赖：

```xml
<PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.1" />
<PackageReference Include="xunit" Version="2.9.2" />
<PackageReference Include="xunit.runner.visualstudio" Version="2.8.2" PrivateAssets="All" />
```

测试项目继续使用：

```xml
<TargetFramework>net6.0</TargetFramework>
<ImplicitUsings>enable</ImplicitUsings>
<Nullable>enable</Nullable>
```

暂不引入：

```text
FluentAssertions
Shouldly
NUnit
MSTest
SMAPI test harness
Stardew DLL runtime fixture
```

## 4. 迁移范围

Phase6.6 修改范围限定为 Adapter 测试治理：

```text
adapters/stardew/tests/ActionCancellationRegistry.Tests
adapters/stardew/tests/PlayerInteractProbe.Tests
adapters/stardew/tests/ProtocolMapper.Tests
adapters/stardew/tests/check-context-static.ps1
docs/phase6.6
```

迁移内容：

- 三个 Adapter test project 从 console app 改为 xUnit test project。
- `.csproj` 移除 `OutputType=Exe`。
- `.csproj` 增加 xUnit 相关 package。
- `Program.cs` 改为命名清楚的 test class 文件。
- 保留现有 `<Compile Include="..\..\src\...">` 生产源码 link 方式。
- 保留 protobuf codegen 测试依赖。
- 静态检查脚本和开发命令从 `dotnet run` 更新为 `dotnet test`。

建议测试类拆分：

```text
ActionCancellationRegistryTests
PlayerInteractTargetSelectorTests
PlayerInteractTriggerTests
InteractionPolicyTests
ProtocolMapperEventTests
ProtocolMapperObservationTests
ProtocolMapperCapabilityTests
ProtocolMapperActionArgumentTests
ProtocolMapperActionResultTests
ConversationStateStoreTests
InteractionContextStoreTests
DialoguePresentationFlowTests
DialogueResponseMenuLayoutTests
DialogueReplyChoiceTests
DialogueSingleLineTextTests
TestSupport
```

## 5. 迁移顺序

### 5.1 ActionCancellationRegistry.Tests

优先迁移最小测试项目。

验收点：

- xUnit 包能 restore；
- `dotnet test` 能发现测试；
- 并发取消语义保持不变。

建议拆分：

```text
unknown action is not cancelled
cancel marker is consumed once
empty action id is ignored
clear removes cancellation marker
parallel cancellation markers are thread-safe
```

### 5.2 PlayerInteractProbe.Tests

第二步迁移纯 selector / trigger / policy 测试。

验收点：

- NPC 命中选择语义保持不变；
- left-click 不选择 adjacent tile；
- trigger mapping 保持稳定；
- interaction distance policy 保持稳定。

建议拆分：

```text
empty allow-list accepts any villager candidate
allow-list rejects candidates outside list
bounding-box hit beats adjacent-tile hit
equal adjacent distance tie-breaks by NPC name
left-click ignores adjacent-tile hits
left-click accepts direct bounding-box hits
button trigger maps to protocol trigger string
interaction distance policy accepts nearby targets
interaction distance policy rejects far targets
```

### 5.3 ProtocolMapper.Tests

最后迁移最大测试项目。

验收点：

- 先保持当前断言语义完整迁移；
- 拆分 test class，不重构生产代码；
- helper 只留在测试项目；
- protobuf、conversation、interaction context、dialogue layout、capability schema、action result 等测试都可单独定位。

建议拆分：

```text
ProtocolMapperEventTests
ProtocolMapperObservationTests
ProtocolMapperCapabilityTests
ProtocolMapperActionArgumentTests
ProtocolMapperActionResultTests
ConversationStateStoreTests
InteractionContextStoreTests
DialoguePresentationFlowTests
DialogueResponseMenuLayoutTests
DialogueReplyChoiceTests
DialogueSingleLineTextTests
TestSupport
```

## 6. 验收命令

Phase6.6 完成后运行：

```powershell
dotnet test adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet test adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
dotnet test adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
dotnet build adapters/stardew/GameAgent.Stardew.csproj --configuration Debug
go test ./...
git diff --check
```

首次迁移可能触发 NuGet restore，需要网络或本地 NuGet 缓存。

## 7. 验收标准

Phase6.6 验收条件：

- 三个 Adapter 测试项目都能被 `dotnet test` 发现并运行。
- 当前 console 测试覆盖的断言语义全部保留。
- 测试失败时能定位到具体 test method。
- 测试项目不要求本地 Stardew DLL。
- `check-context-static.ps1` 使用新的 `dotnet test` 命令口径。
- Runtime Go 测试方式不变。
- Adapter build 不受测试项目迁移影响。

## 8. 明确边界

Phase6.6 不做以下工作：

- 不修改 Phase6.5 对话功能逻辑；
- 不改 protobuf；
- 不改 Runtime AgentLoop / Gateway / Scheduler；
- 不引入持久化；
- 不引入 CI 服务；
- 不引入 SMAPI 真机自动化；
- 不合并三个测试项目；
- 不引入 FluentAssertions / Shouldly。

## 9. 后续演进

Phase6.6 之后可以继续评估：

- 是否把 Adapter 测试项目统一纳入 solution；
- 是否新增共享测试 helper project；
- 是否建立 CI；
- 是否补充 SMAPI 真机 smoke checklist；
- 是否对 ProtocolMapper 大测试文件继续做领域拆分。

这些不是 Phase6.6 的验收条件。
