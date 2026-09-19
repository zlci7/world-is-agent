# GameAgent MVP0 Phase10.3-1 实机验收记录

> 状态：**已验收**（含 §7 修正轮次）。方案见 [Phase10.3 技术方案](GameAgent%20MVP0%20Phase10.3%20RimWorld%20对话接入与世界实例实体技术方案.md)。
> 原始日志：[a1-handshake-player-log.txt](../../adapters/rimworld/tests/evidence/a1-handshake-player-log.txt)（首次验收）
> 修正轮次日志：[a1-correction-round-player-log.txt](../../adapters/rimworld/tests/evidence/a1-correction-round-player-log.txt)

## 1. 环境

```text
RimWorld        1.6.4871 rev591
Unity           2022.3.35f1 / Mono 6.13.0
游戏路径        D:\data\project\RimWorld
Runtime         --data-root <临时 dev root>，默认适配器端口 127.0.0.1:50051
Mod             <Game>\Mods\WiaRimWorld，ModsConfig activeMods 仅含原版 + 5 DLC + wia.rimworld
```

本次运行未涉及任何游戏存档操作：`-quicktest` 启动，仅在启动阶段完成握手。

## 2. 退出条件逐条结论

```text
1  Mod 无游戏安装也可构建
   ✅ adapters/rimworld/tests/check-standalone-build.ps1 三项全过：
      仓库外构建（显式 WIA_PROTOCOL_DIR）→ 产出 WiaRimWorld.dll
      构建产物不含任何 Ludeon / Unity 程序集
      负向探针：WIA_PROTOCOL_DIR 不可用时构建必须失败（证明未走仓库相对回退）

2  进程内 gRPC 双向流稳定
   ✅ 建流、握手、断线检测、退避重连、重连后重新握手全部在游戏进程内完成；
      见 §3.2。Runtime 停机期间游戏正常运行，无卡顿或崩溃。

3  Runtime Core 零 game-specific 改动
   ✅ git status -- runtime protocol 为空。本阶段未改动 Runtime 与协议任何一行。

4  不协商 task 扩展
   ✅ AdapterHello 的 supported_extensions 为空（日志 extensions=(none)）；
      EnvironmentReady 的 accepted extensions 为空；
      全程未发送 WorldBinding / WorldClock / CheckpointPrepare / CheckpointFinish。
      适配器把"收到非空 accepted extensions"当作握手失败处理，该断言由代码强制。
      注：该拒绝分支与 handshake_out_of_order 分支本次均未被触发。实机证据只行使了
      "正常顺序 → Active"与"断线 → 重连 → 新 session"两条路径；两条拒绝路径是代码强制，
      尚无证据行使。
```

## 3. 原始证据

### 3.1 首次连接与握手

适配器侧（Player.log）：

```text
[WIA] native library loaded from D:\data\project\RimWorld\Mods\WiaRimWorld\Native\grpc_csharp_ext.dll (handle 0x7FFDD7C00000); bare-name resolution returned 0x7FFDD7C00000 (win32 error 0)
[WIA] adapter 0.1.0 started on thread 1; runtime 127.0.0.1:50051
[WIA] AdapterHello sent to 127.0.0.1:50051 session=a8d3ec070daf4a8fa08e56ffcd3a85b6 extensions=(none)
[WIA] EnvironmentReady received; accepted extensions=(none)
[WIA] CapabilityList sent: (none) revision=1
[WIA] main-thread pump drained on thread 1, startup thread was 1, same=True
```

Runtime 侧：

```text
capability bootstrap session_id=a8d3ec070daf4a8fa08e56ffcd3a85b6 revision=1 accepted=0 catalog=0
  skipped_nil=0 invalid_names=[] invalid_schema=[] invalid_policy=[] duplicates=[]
  unsupported_entity_id="" invalid_entity_id=""
```

两侧 `session_id` 一致，空能力列表被正常接受且无任何告警。

最后一行同时确认了主线程边界：主线程泵在 thread 1 上排空，与 Mod 静态构造函数所在线程相同。

### 3.2 断线重连

停止 Runtime 后：

```text
[WIA] runtime session ended: RpcException: Status(StatusCode="Unknown", Detail="Stream removed", ...)
[WIA] reconnecting to the Runtime in 5s
[WIA] runtime session ended: RpcException: Status(StatusCode="Unavailable", Detail="failed to connect to all addresses", ...)
[WIA] reconnecting to the Runtime in 5s
```

重新启动 Runtime 后：

```text
[WIA] AdapterHello sent to 127.0.0.1:50051 session=550af439b4174fd1941b886162f0214a extensions=(none)
[WIA] EnvironmentReady received; accepted extensions=(none)
[WIA] CapabilityList sent: (none) revision=1
[WIA] main-thread pump drained on thread 1, startup thread was 1, same=True
```

Runtime 侧同样记录到新会话 `session_id=550af439b4174fd1941b886162f0214a`。两次会话 id 不同，说明是重新建流而不是复用旧状态。

## 4. 实现过程中发现并修正的两个问题

```text
1  原生库预加载漏实现
   首轮运行失败：DllNotFoundException: grpc_csharp_ext。
   方案与 README 都已写明"以绝对路径预加载"，但代码里没有这一步。
   修正：新增 src/Runtime/NativeLibraryLoader.cs，在 AdapterStartup 中于任何 gRPC 调用之前执行，
   并同时验证裸名解析返回同一模块句柄——这正是后续 DllImport 能命中的前提。

2  ModsConfig.xml 的 packageId 大小写
   手工写入混合大小写（Ludeon.RimWorld 等）后，RimWorld 并未按大小写不敏感去重，
   而是把全小写的规范形式当作新条目追加，造成同一 DLC 出现两次。
   修正：activeMods / knownExpansions 一律使用全小写 packageId。
```

问题 1 说明"文档写了"不等于"代码做了"，已固化到实现：原生库预加载前置于任何 gRPC 调用，且预加载失败时
不再启动传输（见 §7）。问题 2 是本次手工维护 ModsConfig 时的真实陷阱，**未固化到安装脚本**——安装脚本
不接触 `ModsConfig.xml`，也不启用 Mod；`activeMods` 由用户在 RimWorld 的 Mod UI 中自行启用。

## 5. 交付物

```text
adapters/rimworld/WiaRimWorld.csproj         net472，Krafs.Rimworld.Ref，编译期零游戏依赖
adapters/rimworld/About/About.xml            packageId = wia.rimworld
adapters/rimworld/src/AdapterStartup.cs      入口：原生库预加载 + 进程级泵 + 传输启动
adapters/rimworld/src/AdapterLog.cs          带 [WIA] 前缀的日志
adapters/rimworld/src/Threading/MainThreadPump.cs
adapters/rimworld/src/Runtime/AdapterIdentity.cs
adapters/rimworld/src/Runtime/GameVersionSnapshot.cs
adapters/rimworld/src/Runtime/NativeLibraryLoader.cs
adapters/rimworld/src/Runtime/RuntimeSessionState.cs
adapters/rimworld/src/Runtime/RuntimeConnection.cs
adapters/rimworld/README.md
adapters/rimworld/tests/check-standalone-build.ps1
scripts/install-rimworld-adapter.ps1
scripts/check-architecture.ps1               遍历 adapters/*；$runtimeForbiddenTerms 增加 RimWorld
```

## 6. 本阶段未做的事

```text
能力列表为空：present_dialogue 在 10.3-4 才发布
没有 identity、没有 Observation、没有事件——10.3-2 与 10.3-3
未验证长时间运行下的连接稳定性（仅验证了建立、断线、重连）
```

## 7. 验收后修正轮次

首次验收记录发布后的一轮评审指出四处"实现与声明不一致"，均已修正。§3 的原始日志保持不变，作为修正前的证据。

```text
1  安装脚本不是真正的白名单 stage
   原脚本只清 Assemblies/ 下的 grpc_csharp_ext*，托管 DLL 无清理。白名单一旦收缩，旧 DLL 会留在
   Assemblies/ 里被 RimWorld 照常加载（该目录下所有 *.dll 都进 Assembly.LoadFrom，且按文件名序），
   所以这不是普通残留，而是会改变实际运行的程序集集合。
   修正：Assemblies/ 与 Native/ 先整体删除再重建；mod 目录下的其它文件不动。
   同时删掉 `..\RimWorld` 的相对路径猜测——它在本机指向不存在的目录，且与脚本自身
   "makes no assumption about the surrounding repository layout" 的声明矛盾——并补上 GamePath
   为空的显式报错。少了这句判断，为空时抛的是 PowerShell 参数绑定错误，而不是这条提示。

2  AdapterHello.game_version 硬编码
   原实现固定发送 1.6.4871（构建目标），而 About.xml 声明支持整个 1.6，属协议事实错误；
   10.4 会把 Adapter 身份展示到前端，该错误届时会直接暴露。
   修正：新增 src/Runtime/GameVersionSnapshot.cs，在 [StaticConstructorOnStartup]（主线程）读取
   global::RimWorld.VersionControl.CurrentVersionString 一次并冻结，BuildHello 只读快照；
   构建目标另存为 AdapterIdentity.BuildTargetGameVersion，两个概念不再混用。
   不在 BuildHello 里现读的原因：它运行在线程池线程上，而游戏 API 属主线程。
   global:: 限定是必需的——本程序集 RootNamespace 为 Wia.RimWorld，裸写 RimWorld.VersionControl
   会绑定到适配器自身命名空间，首轮构建即因此报 CS0234。

3  原生库预加载失败后仍启动传输
   原实现只记 error 就继续建连接，形成"注定失败 → 5 秒 → 重试"的循环，把安装错误伪装成
   运行时不可用。修正：预加载失败即记 `adapter disabled, no connection will be attempted: ...`
   并 return，不创建泵、不建连接。

4  文档与代码不一致
   README 开头以现在时声称"殖民者可以对话"，与自身 Status 段矛盾（当前能力列表为空）；
   Requirements 未标注 Windows x64（实际是 net472 + PlatformTarget x64 + LoadLibraryW +
   仅含 grpc_csharp_ext.x64.dll）；未说明两类失败的区分。以上均已修正。
   本节 §4 原先还声称 ModsConfig 大小写问题"已固化到安装脚本"，实际脚本从不接触
   ModsConfig.xml，该句已改正。
```

另：`RuntimeConnection.Dispose()` 当前无任何调用点（连接是进程级生命周期），因此它"取消后不等 worker
即释放 sendGate"的竞态不可达。本次未改动，记在此处以免被误认为已处理。

## 8. 修正轮次实机复验

原始日志见 [a1-correction-round-player-log.txt](../../adapters/rimworld/tests/evidence/a1-correction-round-player-log.txt)。

```text
正向：握手仍完整，且版本来自运行中的游戏
  [WIA] native library loaded from ...\Native\grpc_csharp_ext.dll (handle 0x7FFDD55C0000);
        bare-name resolution returned 0x7FFDD55C0000 (win32 error 0)
  [WIA] adapter 0.1.0 started on thread 1; game rimworld/1.6.4871 rev591; runtime 127.0.0.1:50051
  [WIA] AdapterHello sent to 127.0.0.1:50051 session=531620ca1ef34b53b5eadabdac067469
        adapter=wia-rimworld game=rimworld/1.6.4871 extensions=(none)
  [WIA] EnvironmentReady received; accepted extensions=(none)
  [WIA] CapabilityList sent: (none) revision=1
  [WIA] main-thread pump drained on thread 1, startup thread was 1, same=True

  Runtime 侧同会话：capability bootstrap session_id=531620ca1ef34b53b5eadabdac067469
                    revision=1 accepted=0 catalog=0，无任何告警

  "rev591" 不可能由 BuildTargetGameVersion（裸 "1.6.4871"）产生，所以它的出现证明该值读自
  运行中的游戏，而不是编译期常量。这一点必须可见：本机安装的就是 1.6.4871，只看版本号本身
  无法区分"读了游戏"和"写死了常量"。

负向：原生库缺失时不启动传输
  移除 Native/grpc_csharp_ext.dll 后启动游戏，整个 Player.log 中 [WIA] 只有一行：
  [WIA] adapter disabled, no connection will be attempted: native library not found at
        ...\Native\grpc_csharp_ext.dll; run scripts/install-rimworld-adapter.ps1 to stage it
        (restart RimWorld after installing it)
  匹配 "AdapterHello sent" 的行数：0；同一时间段 Runtime 无任何新输出。
  两侧都确认没有发起连接，且游戏本身正常运行。
```

安装脚本的两个新行为以不依赖实机的方式单独验证：

```text
白名单 stage   预置 Assemblies/Grpc.Core.Old.dll 与 Assemblies/grpc_csharp_ext.dll 后运行安装脚本，
              两者均被清除，mod 目录下的非程序集文件保留，产物恰为
              8 个托管 DLL + Native/grpc_csharp_ext.dll。
报错路径       既不传 -GamePath 也不设 WIA_RIMWORLD_GAME_PATH：
              "Game path not specified. Pass -GamePath or set WIA_RIMWORLD_GAME_PATH."
              -GamePath 指向非 RimWorld 目录：
              "Not a RimWorld install: <path>"
```

其余自动化检查：`check-standalone-build.ps1` 三项全过、`check-architecture.ps1` 通过、
`git diff --check` 干净。
