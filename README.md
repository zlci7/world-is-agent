# World Is Agent

**World Is Agent（WIA）** 是一个面向游戏世界的 **Game-native Agent Runtime**。

它通过 Runtime / Adapter 架构连接真实游戏世界与 LLM Agent，让 NPC 不再只是“对话机器人”，而是能够基于游戏状态进行感知、决策、记忆、工具调用和持续行动。

目前以 **Stardew Valley** 作为第一个真实游戏 Adapter。

![World Is Agent](docs/images/world-is-agent.jpg)

## 核心能力

* **Runtime / Adapter 解耦**：通过 gRPC 双向流连接游戏与 Agent Runtime
* **统一 Agent 生命周期**：支持 AgentTurn、多步决策与执行
* **Context & Memory**：管理 Agent 上下文与持久化记忆
* **Capability → Tool**：由游戏动态声明能力并注册为模型工具
* **本地控制台**：启动 Runtime 自动打开浏览器，查看 agent 状态与最近 AgentTurn
* **Agent 隔离**：基于 `game_id + world_id + entity_id` 管理独立 Agent
* **同步 / 异步执行**：支持游戏动作及长生命周期任务

## 技术栈

`Golang` · `gRPC` · `Protobuf` · `SQLite` · `C#` · `SMAPI` · `LLM Tool Calling` · `JSON Schema`

## 项目结构

```text
runtime/      Agent Runtime
protocol/     Protobuf 协议与生成代码
adapters/     游戏 Adapter
docs/         架构、状态与开发文档
```

## 文档

* [架构设计](ARCHITECTURE.md)
* [当前状态](docs/STATUS.md)
* [开发指南](docs/development/guide.md)
* [测试与验证](docs/development/testing.md)
* [Stardew Valley Adapter](adapters/stardew/README.md)

## License

[MIT License](LICENSE)
