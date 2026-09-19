# GameAgent MVP0 Phase10.3-1 实机验收记录

> 状态：**已验收**。方案见 [Phase10.3 技术方案](GameAgent%20MVP0%20Phase10.3%20RimWorld%20对话接入与世界实例实体技术方案.md)。
> 原始日志：[a1-handshake-player-log.txt](../../adapters/rimworld/tests/evidence/a1-handshake-player-log.txt)

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

问题 1 说明"文档写了"不等于"代码做了"；问题 2 是手工维护 ModsConfig 时的真实陷阱，两者都已固化到安装脚本与实现中。

## 5. 交付物

```text
adapters/rimworld/WiaRimWorld.csproj         net472，Krafs.Rimworld.Ref，编译期零游戏依赖
adapters/rimworld/About/About.xml            packageId = wia.rimworld
adapters/rimworld/src/AdapterStartup.cs      入口：原生库预加载 + 进程级泵 + 传输启动
adapters/rimworld/src/AdapterLog.cs          带 [WIA] 前缀的日志
adapters/rimworld/src/Threading/MainThreadPump.cs
adapters/rimworld/src/Runtime/AdapterIdentity.cs
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
能力列表为空：present_dialogue 在 A4 才发布
没有 identity、没有 Observation、没有事件——A2 与 A3
未验证长时间运行下的连接稳定性（仅验证了建立、断线、重连）
```
