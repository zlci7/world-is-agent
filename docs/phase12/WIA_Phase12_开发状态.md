# World Is Agent Phase12 开发状态

当前目标与执行合同：[开发执行指南](WIA_Phase12_开发执行指南.md)。

## 当前状态

M1「核心游玩与可靠存档」的实现和工程验证已完成，交付物可独立于游戏运行时启动。用户体验验收可直接从本地 Runtime 开始；真实模型连续游玩需要在工作台提供可用的 Provider API Key。

当前成果包含结构化回合意图解析、明确目标与私聊隔离、在场人物过滤、NPC 行动意图与个人记忆闭环、阶段二公开刺激、场景主上下文与场景版本提交、回合基线校验、失败重试边界、可靠另存、单数据根进程互斥、运行状态恢复和本地 Origin 校验。

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

最近一次验证已通过：`go test ./... -count=1`、`go test -race ./runtime/internal/storyapp ./runtime/internal/storyapi -count=1`、`go vet ./runtime/internal/storyapp ./runtime/internal/storyapi ./runtime/cmd/server`、`npm run type-check`、`npm run build`、`git diff --check`，并完成 Runtime 本地启动探测。当前开发环境没有预置 DeepSeek 或 OpenAI API Key，因此真实模型的普通对话、私聊、插话等待和连续存读档数据仍保持“待执行”，不以测试注入的 Fake 冒充真实模型体验结论。
