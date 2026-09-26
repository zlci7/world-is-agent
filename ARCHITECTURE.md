# World Is Agent 架构

Phase12 将 World Is Agent 定义为一个本机叙事游戏 Runtime。玩家通过本地浏览器进入故事，Runtime 负责世界身份、人物感知、模型协调、原子回合和独立存档。

```text
本地浏览器工作台
        │  loopback HTTP + session cookie
        ▼
Story API
        │
        ├── AppStore：用户作用域、活动世界、模型配置、另存任务
        ├── Story Coordinator：回合、NPC 阶段、取消、幂等、版本
        ├── Text Provider：DeepSeek / OpenAI
        └── WorldStore：每个 world_id 一个 SQLite 数据库
                         ├── 正文与事件
                         ├── 人物感知与记忆
                         └── 世界时间与回合状态
```

## 运行边界

Runtime 绑定一个本机 `local` owner 和一个预置故事 `lantern-dusk`。每个新世界分配新的 `world_id`，路径位于：

```text
<data-root>/story-app/
  app.db
  config/model.json
  secrets/model.key
  worlds/<user>/<game>/<world>/world.db
```

应用库保存目录与活动选择；世界库保存本世界采用的故事版本、主角、人物实例、正文、事件、感知、记忆、回合和世界时间。模型凭据只保存在 secrets 文件中，不复制到世界库。

## 一轮故事

```text
玩家输入
  → 固定世界快照与版本检查
  → 两名重要 NPC 并行读取各自上下文
  → 公开对白形成下一阶段刺激
  → 沉默人物按需追加一次回应
  → 场景主组织玩家可见正文
  → 一个 SQLite 事务提交事件、感知、记忆、正文、时钟和 completed
```

NPC 只收到自己的角色资料、个人感知、个人经历和本阶段允许的刺激。私下输入对授权人物保留原文，对旁观人物只生成交谈迹象；场景主收到私聊的公开投影，不收到耳语原文。失败、取消或中断的输入保留在 run 表供界面恢复，不进入正式历史。

## 可靠性合同

- 同一世界单写者；不同世界可以独立运行。
- `request_key` 和请求哈希保证重复提交返回原 run，负载变化返回冲突。
- 活动世界使用单调 `active_revision`；切换、回合和另存携带预期版本。
- 回合完成使用同一个事务，防止正文、事件、感知、记忆和完成态分裂。
- 取消通过上下文和数据库 `cancel_requested` 共同竞争提交门。
- 进程重启会将未终结回合标为 `interrupted`；不会自动重放模型调用。
- 另存使用 SQLite `VACUUM INTO` 创建独立世界，生成中等待指定回合的终态，失败时目标不可游玩且源世界保持完整。
- HTTP 只监听 loopback，浏览器通过启动 URL 的一次性令牌换取 HttpOnly session cookie；普通游玩接口只返回公开人物投影。

## 模型边界

模型只负责候选对白、意图和场景正文。程序负责人物身份、可见范围、阶段顺序、输出 JSON 的重复键和未知字段拒绝、活动版本、取消竞争以及落盘事务。正式 Runtime 不自动使用 Fake；Fake 仅由测试注入。

## 当前范围

M1 提供一个可直接游玩的调查冒险、流程型/开放型开局、人物信息差、自动保存、另存读取、生成取消和失败重试。长期摘要与语义检索、行动建议、内容编辑、统一纠正、多用户服务端和发布包属于后续阶段。
