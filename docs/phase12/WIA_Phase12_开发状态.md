# World Is Agent Phase12 开发状态

当前目标与执行合同：[开发执行指南](WIA_Phase12_开发执行指南.md)。

## 当前状态

M1「核心游玩与可靠存档」的实现和工程验证已完成，交付物可独立于游戏运行时启动。用户体验验收可直接从本地 Runtime 开始；真实模型连续游玩需要在工作台提供可用的 Provider API Key。

## 阶段状态

| 阶段 | 实现 | 工程验证 | 用户体验验收 | 证据 |
| --- | --- | --- | --- | --- |
| M1 核心游玩与可靠存档 | 已完成 | 已完成 | 待用户体验 | [M1 验收记录](acceptance/M1.md) |
| M2 世界剧情与长期记忆 | 未开始 | 未执行 | 待阶段交付 | — |
| M3 完整玩家工作台 | 未开始 | 未执行 | 待阶段交付 | — |
| M4 桌面、服务器与完整交付 | 未开始 | 未执行 | 待阶段交付 | — |

## M1 交付入口

```powershell
cd D:\data\project\game-agent\world-is-agent
.\scripts\start-phase12.ps1 -Rebuild
```

日常启动可使用 `.\scripts\start-phase12.ps1`；需要重新构建工作台时使用 `-Rebuild` 或 `-True`。也可以构建 `go build -o wia-runtime.exe ./runtime/cmd/server` 后直接运行。Runtime 日志会打印本地工作台地址；首次进入由工作台完成真实 Provider 连接验证。

## 已知验证边界

自动化、race、静态检查、前端构建和本地二进制启动均已完成。当前开发环境没有预置 DeepSeek 或 OpenAI API Key，因此真实模型的普通对话、私聊、插话等待和连续存读档数据仍保持“待执行”，不以测试注入的 Fake 冒充真实模型体验结论。
