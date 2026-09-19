# GameAgent MVP0 Phase10.4 Game Profile 选择与多游戏产品形态技术方案

> 状态：**方案（未开工）**。前置条件是 10.3 闭环完成——在那之前第二个 profile 只有"未来可能需要"，
> 没有实现依据。
> 上位文档：[Phase10 技术开发与验收总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md) §4、§3.5。

## 1. 目标与范围

### 1.1 做什么

让用户**选择游戏**，并让 Runtime 按所选游戏加载整套行为配置。完成后，用户不需要知道
`agent.json`、`GAMEAGENT_AGENT_CONFIG`、`definition_catalog_root` 的存在。

### 1.2 为什么必须等 10.3

10.3 在 §18 明确把"game profile 重构"列为不做，在 §13.1 记录了这么做的技术原因：
发布树中 `games/*/agent.json` 只允许存在一份，多一份会让 `Seed()` 报错、
`bootstrap.Open()` 置 `StateBlocked`，**所有全新 data root 首次启动直接 blocked**。

那个约束在当时是正确取舍：只有一份真实 profile 时，引入"多份 + 选择规则"是在为不存在的需求设计。
**当第二个 Adapter 真实闭环之后，理由才成立**——此时不再是"未来可能需要"，
而是"两个真实游戏已经需要"。

因此本阶段的第一条前置是：**10.3 的退出条件全部达成。**

### 1.3 为什么排在 10.5（拆仓）之前

文档编号是 10.4，但**执行位置在 10.5 之前**。理由是拆仓需要先知道一件事：

> 游戏的 definitions 随 Adapter 仓库走，还是留在 Runtime 发布树？

10.3 §17 已把这个"definitions distribution"记为**发布前必须解决**的问题，
而 profile 选择器恰恰是那个必须"按游戏找到定义"的机制——两件事是同一个问题。
先做 10.4，拆仓才有非任意的依据；反过来先拆仓，只会在拆完之后再决定定义放哪。

若确实要先做 10.5，则 §3.5 必须单独提前完成。

### 1.4 与其它子阶段的边界

```text
10.2-5 运行状态可视化
    拥有：AgentTurn 时间线细化、对话记录、任务与记忆查看、能力可用性、依赖体检
    本阶段不重复这些

10.4（本文）
    拥有：Game Profile 选择、Selected / Connected 的身份语义、mismatch 处理、
          以及缺失游戏 profile 的安装路径

10.5 / 10.6
    本阶段为 10.5 提供"definitions 归属"的结论；10.6 的检查表由 10.4
    的接入经验补充，但检查表本身仍属 10.6
```

"游戏是否已连接"这一条同时出现在 10.2-5 的清单里。**本阶段只定义它的语义与 mismatch 判定**，
更宽的查看面仍归 10.2-5。两者谁先落地都可以，但不得各建一套。

## 2. 核心概念：切换的是 Game Profile，不是 Prompt

### 2.1 Runtime 行为配置是一个整体

```text
Game Profile =
    Agent config（prompt / tool instruction / max_steps / timeout）
  + task policy（task.enabled 及由此决定的能力集）
  + definition catalog（该游戏的定义与 archetype）
  + seed 与默认值
```

### 2.2 两个 Adapter 的实际差异已经存在

这不是假设，而是已经写在两份配置里的事实：

```text
Stardew Valley
  task.enabled = true
  max_steps = 5
  有 durable task、有移动能力
  prompt.tool_instruction 指向 move_to_landmark / wait_for_player

RimWorld（10.3）
  task.enabled = false
  只有 present_dialogue
  Observe 需要主线程读取游戏状态，timeout 取值依据不同
```

### 2.3 因此明确不是

```text
选择 RimWorld
→ 只替换一段 system prompt
```

只换 prompt 会立刻产生错误组合：RimWorld 的 profile 配 Stardew 的能力集、
或 Stardew 的 profile 收到 RimWorld 的事件。**选择单位必须是整份 profile。**

## 3. 配置结构变更

### 3.1 现状与约束来源

```text
<data root>/config/
├── agent.json                   ← 唯一激活的 agent 配置
└── games/
    └── stardew-valley/
        ├── agent.json
        └── definitions/
```

```text
runtime/config/defaults.go
    shippedProfile()   要求 games/*/agent.json 恰好一份，否则 Seed() 报错
    seedDefinitions()  只复制各游戏的 definitions，不复制游戏目录下的其它文件
    Seed()             锚点是 config/agent.json；锚点存在即直接返回
```

其中"锚点存在即返回"的规则是刻意的，包注释写明：逐文件补齐会 "silently become an upgrade mechanism"。
**本阶段保留这条原则**，见 §3.5。

### 3.2 目标结构

```text
<data root>/config/
├── active-game.json             ← 新增：用户选择的游戏
└── games/
    ├── stardew-valley/
    │   ├── agent.json
    │   └── definitions/
    └── rimworld/
        ├── agent.json
        └── definitions/
```

```json
// active-game.json
{ "game_id": "rimworld" }
```

### 3.3 shipped profile 从"恰好一份"变为"N 份 + 用户选择"

```text
旧规则   games/*/agent.json 必须恰好一份，它就是这份 Runtime 的 profile
新规则   可以存在 N 份；由 active-game.json 选择其中一份
```

`shippedProfile()` 的"恰好一份"约束随之删除，替换为"选择必须是显式的"。
没有选择时不是猜一个，而是进入 §4 的未配置状态。

### 3.4 agent config 的解析顺序

```text
1  GAMEAGENT_AGENT_CONFIG          开发覆盖，保持现状，优先级最高
2  active-game.json → games/<game>/agent.json
3  （无）
```

第 3 种情况不是错误配置，而是"尚未选择游戏"，属于 §4.1 的未配置状态。

### 3.5 顺带解决 definitions distribution（10.3 §17 的待决项）

已确认的事实：`Seed()` 不是升级机制。`config/agent.json` 一旦存在，新版本新增的
游戏 definitions 不会补进既有 data root。因此：

```text
v0.1.0 老用户
  已有 data root
      ↓
升级到带第二个游戏 profile 的新版 Runtime
      ↓
既不会得到 rimworld/agent.json，也不会得到 rimworld/definitions/
```

**本阶段必须解决它，因为选择器必须能按游戏找到定义。** 三个候选：

```text
A  随 Runtime 发布并显式安装
   发布树本来就带 games/**；把"把某个游戏装进这个 data root"变成用户可见的显式动作
   （选择器里显示为未安装 + Install），而不是启动时的静默补齐

B  随 Adapter 仓库发布
   profile 与 definitions 跟着 adapter 走，注册到 Runtime

C  Runtime 引入定义升级机制
   带版本比较的 reconcile
```

**推荐 A**：它保留 `Seed()` "不做静默升级"的既定原则，同时把分发问题变成用户可理解的动作；
B 会把 Runtime 的行为配置绑到适配器仓库的发布节奏上；C 引入版本 reconcile，是三者中最重的。

无论选哪个，本阶段的产出必须包含：**既有 data root 如何获得新游戏 profile 的明确路径**，
以及一条针对"v0.1.0 data root 升级"的验证。

## 4. 状态与生命周期

### 4.1 三个状态不变，`reason` 区分缺什么

Runtime 已有的状态语义（`needs_configuration` ＝ 模型可以修好，`blocked` ＝ 模型修不好，`ready`）
不需要第四个状态。新增的只是"还没选游戏"也属于 `needs_configuration`，由 `reason` 区分：

```text
needs_configuration
  reason = game_not_selected      尚未选择游戏
  reason = model configuration not found at …   已有游戏，缺模型配置

ready
blocked
```

### 4.2 只在 Agent Core 启动前选择

```text
启动 WIA
   ↓
Local Web 打开
   ↓
选择游戏
   ↓
Runtime 选择对应 Game Profile
   ↓
加载该游戏：agent config / prompt / max_steps / timeout / task policy / definitions
   ↓
Agent Core Ready
```

### 4.3 切换 = restart required

游戏 profile 比模型配置更重（prompt、task、memory policy、timeout、definitions、tool 行为全在内），
而模型配置在 `ready` 之后已经不允许直接改写（`/api/setup/model` 返回 `already_configured`）。
因此第一版沿用同一模式并且更严格：

```text
Runtime ready
  → 本地客户端显示当前游戏
  → 选择另一个游戏
  → 返回 restart_required
  → 客户端提示"需要重启 Runtime"
  → 重启后加载新 profile
```

**本次 Runtime 生命周期内 profile 固定。**

### 4.4 为什么不做热切换

热切换要同时处理：正在进行的 Turn、in-memory continuation、memory projection 的 game 边界、
能力集替换、以及 prompt 变更。任何一项没做对，都会出现"半新半旧"的会话，
而这正是最难诊断的一类问题。MVP0 的边界里也没有多游戏并发。

## 5. Selected Game 与 Connected Game

### 5.1 两个概念必须分开

```text
Selected Game    Runtime 加载的是哪份 Game Profile
Connected Game   当前哪个 Adapter 真的连上来了（来自 AdapterHello.game_id）
```

UI 上必须两个都显示，否则用户无法判断"为什么没反应"：

```text
Selected:   RimWorld
Adapter:    RimWorld 1.6   Connected ✓
```

### 5.2 mismatch 必须显式报告，不能悄悄工作

```text
Selected:   RimWorld
Adapter:    Stardew Valley
→ Game mismatch
```

**推荐 fail-closed：拒绝该会话并给出明确 code。** 理由不是 memory 串扰——
`AgentSessionKey` 含 game_id，记忆本来就是隔离的——而是 **profile 用错了**：
RimWorld 的 profile（`task.enabled=false`、只有对话能力）收到 Stardew 的事件，
会产生一组谁也不理解的失败。

这一点需要在实现时确认一个细节：拒绝发生在 Hello 阶段还是首次事件阶段。
按现状，`game_id` 在 `AdapterHello` 中已知，因此应在 Hello 阶段即可判定。

### 5.3 与 10.2-5 的分工

本阶段只交付：`/api/status` 中的 Selected / Connected 身份、mismatch 判定与提示。
完整的可观察面（turn 明细、任务与记忆查看、能力可用性、依赖体检）仍属 10.2-5。

## 6. HTTP 面

沿用既有的本地控制面，不新开接口层。既有路由为
`/api/session`、`/api/status`、`/api/turns`、`/api/setup/options`、`/api/setup/model`。

```text
GET  /api/status
     + game    { id, title, source, installed }
     + adapter { connected, game_id, adapter_id, version }

GET  /api/setup/games
     返回发布树中可用的游戏，以及每个游戏在当前 data root 是否已安装

POST /api/setup/game
     body { game_id }
     写入 active-game.json
```

约束：

```text
1  沿用既有 session cookie 与 origin 校验，不新增鉴权路径
2  沿用 10.2-3 已确立的顺序：先读 Runtime 状态，再解析请求体——
   blocked 的 Runtime 必须在读 body 之前就被拒绝（对应既有 setup_blocked）
3  ready 状态下 POST /api/setup/game 返回 restart_required，不改写磁盘
4  未知 game_id 返回 invalid_game，不隐式创建目录
```

## 7. 本地客户端

首次启动从一步变两步：

```text
World Is Agent

Choose a game
┌────────────────────────┬────────────────────────┐
│ Stardew Valley         │ RimWorld               │
│ Game-native NPC Agent  │ Colonist Agent         │
└────────────────────────┴────────────────────────┘
        ↓
Model  /  Model name  /  API Key
        ↓
[ Start Runtime ]  → Agent Core Ready
```

Runtime 页面新增：

```text
Selected game     RimWorld
Adapter           RimWorld 1.6 · Connected ✓        （或 Not connected）
```

切换游戏时：

```text
Restart required
本次运行使用的是 <当前游戏>。重启 Runtime 后才会加载 <新游戏>。
```

用户始终不需要接触 `agent.json`、环境变量或 `definition_catalog_root`；
这些继续只是开发接口。

## 8. 最终产品形态

```text
下载 WIA
   ↓
双击运行
   ↓
浏览器自动打开本地客户端
   ↓
选择游戏
   ↓
选择模型、填写 Key
   ↓
Ready
   ↓
启动对应游戏
   ↓
Adapter 自动连接
```

## 9. 阶段拆分

```text
10.4-1  配置结构
        games/<game>/agent.json 支持 N 份；active-game.json；
        agent config 解析顺序；"恰好一份"约束移除

10.4-2  分发与迁移
        缺失游戏 profile 的显式安装路径（§3.5 选定的方案）；
        既有 v0.1.0 data root 的升级验证

10.4-3  Runtime 与 HTTP
        三状态语义不变、reason 区分；/api/status 扩展；
        /api/setup/games 与 /api/setup/game；restart_required；mismatch 拒绝

10.4-4  本地客户端
        游戏选择步骤；Selected / Connected 展示；restart 提示
```

每一步只证明一个命题，完成即 commit。

## 10. 退出条件

```text
1   存在两份真实 shipped profile（Stardew Valley 与 RimWorld），Runtime 不再要求"恰好一份"
2   active-game.json 决定加载哪份 profile；未选择时进入 needs_configuration 且 reason 可区分
3   GAMEAGENT_AGENT_CONFIG 仍可作为开发覆盖，优先级最高
4   切换游戏在 ready 状态下返回 restart_required，且不改写磁盘
5   重启后确实加载了新 profile：prompt、task policy、definitions 三者同时生效
6   既有 v0.1.0 data root 有一条明确路径获得新游戏 profile，且该路径经过验证
7   /api/status 同时暴露 Selected 与 Connected
8   Adapter 的 game_id 与所选 profile 不一致时被拒绝，且错误可读
9   首次启动流程为"选游戏 → 选模型 → Ready"，全程不需要用户编辑任何文件或环境变量
10  Runtime Core 未新增任何 game-specific 分支（games/ 目录内容属数据，不属核心逻辑）
```

第 7 条的 Selected / Connected 以 Real 实机为准：一个真实 Stardew Adapter 与一个真实 RimWorld Adapter
分别连接，两种顺序都验证过。

## 11. 已知债务与风险

```text
编号与顺序不一致：本文编号 10.4，执行位置在 10.5 之前（理由见 §1.3）。
    若决定先拆仓，§3.5 必须提前单独完成。

一次只能服务一个游戏：本阶段不做多游戏并发，也没有"同一 Runtime 同时承载两个 Adapter"的目标。

不自动识别正在运行的游戏：用户显式选择。自动探测属于后续评估，不在本阶段。

每游戏独立的模型/凭据：本阶段仍是一份模型配置，切换游戏不切换模型。

10.2-5 与本阶段在"是否已连接"上有重叠，实现时以 §5.3 的分工为准，不重复建面。
```

## 12. 明确不做

```text
ready 状态下的 profile 热切换
同一 Runtime 同时服务多个游戏
自动探测当前运行的游戏
每游戏独立的模型与凭据
Adapter 的自动下载与安装
Installer / 代码签名 / macOS 与 Linux 包（仍属 10.2 已明确的非目标）
10.2-5 的其余可观察面
10.6 的 Adapter 接入检查表
```
