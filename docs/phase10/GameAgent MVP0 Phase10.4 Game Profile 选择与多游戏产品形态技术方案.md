# GameAgent MVP0 Phase10.4 Game Profile 选择与多游戏产品形态技术方案

> 状态：**Accepted for Implementation**。前置条件已满足：10.3 已于 2026-09-20 正式 Accepted，
> 其中条件 9 的负向部分**未验证、经用户决定豁免**，已在 10.3 验收记录中明确记录。
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

本阶段排在 10.5（拆仓）之前。理由是拆仓需要先知道一件事：

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
  prompt.npc_style 与 tool_instruction 针对殖民者重写，max_speak_chars 60 → 120
```

两份配置**目前的差异只有 `task.enabled` 与 `prompt` 两项**：所有 timeout、`max_steps`、
`max_tool_calls_*`、`definition_catalog_root`、`prompt.language` 完全相同。差距小并不削弱
下面的结论——`task.enabled` 一项就已经决定了能力集与事件形状——但它意味着**本阶段不能靠
"两份 profile 差很多"来论证**，论证要落在"这两个字段决定了不同的行为契约"上。

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

**本阶段要把 RimWorld 的 profile 搬进发布树。** 10.3 §13.1 把它放在
`adapters/rimworld/profile/agent.json`，理由是当时 `shippedProfile()` 要求
`games/*/agent.json` **恰好一份**，放进 `games/` 会让每一个全新 data root 直接 blocked。
10.4-1 移除那条约束之后，它应当迁到 `runtime/config/games/rimworld/agent.json`，也就是
上面目标结构里的位置。

两点必须写进实施顺序：

```text
顺序硬约束   10.4-1 先落地。在 shippedProfile() 还要求"恰好一份"时把
             games/rimworld/agent.json 放进去，会让所有全新 data root blocked

顺带修好     AGENTS.md 规定"不把 Runtime prompt 配置放入 Adapter 目录"，
             10.3 §13.1 为绕开"恰好一份"把 profile 放进了 adapter 目录——那是一次例外。
             搬到 games/rimworld/ 之后这条冲突自然消失，10.3 §13.1 中"位置冻结"的表述
             也随之失效，应在该文档补一句指向本文。
```

### 3.3 shipped profile 从"恰好一份"变为"N 份 + 用户选择"

```text
旧规则   games/*/agent.json 必须恰好一份，它就是这份 Runtime 的 profile
新规则   可以存在 N 份；由 active-game.json 选择其中一份
```

`shippedProfile()` 的"恰好一份"约束随之删除，替换为"选择必须是显式的"。
没有选择时不是猜一个，而是进入 §4 的未配置状态。

### 3.4 游戏身份与 agent config 是两件事

选择游戏与选择 agent config 不是同一个决定，解析顺序必须把两者分开：

```text
游戏身份 由 active-game.json 决定，环境变量不参与
    active-game.json → game_id
    （无）           → 尚未选择游戏，属 §4.1 的未配置状态

agent config 的内容
    1  GAMEAGENT_AGENT_CONFIG 存在 → 它覆盖 <selected game> 的 agent.json
    2  否则                        → games/<selected game>/agent.json
```

**环境变量覆盖配置内容，不覆盖游戏身份。** `GAMEAGENT_AGENT_CONFIG` 只回答"用哪份 agent
config"，它回答不了"当前选的是哪个游戏"——而后者是 Selected Game、mismatch 判定与
definitions 定位都要用的。若让它同时决定身份，就会出现"选着 RimWorld、却挂着一份不知道属于
哪个游戏的 profile"这种组合。开发模式若确实需要完全跳过 `active-game.json`，那需要另一套
game-id 来源，MVP0 不做。

注意一个既有副作用（10.3 §13.1 已记录）：`bootstrap.go` 把"设置了 `GAMEAGENT_AGENT_CONFIG`"
直接当作 `Seed()` 的 `skip=true`，于是整棵发布树都不播种，**包括所有 definitions**
（`runtime/internal/bootstrap/bootstrap.go:221`）。用了这个覆盖的 data root 必须自己保证
定义齐全。本阶段不改变这条行为。

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

**决策：A。** 理由有两层：

```text
1  它保留 `Seed()` "不做静默升级"的既定原则，同时把分发问题变成用户可理解的动作；
   B 会把 Runtime 的行为配置绑到适配器仓库的发布节奏上；C 引入版本 reconcile，是三者中最重的。

2  它符合既有的 ownership 边界：Runtime owns Definition / Context / Memory，
   Adapter owns game translation / execution。Game Profile 与 definitions 属于前者，
   因此随 Runtime 发布。10.5 拆仓之后 wia-adapter-<game> 不需要负责给 Runtime 注册
   人格与配置资产，否则那个仓库会同时拥有通信实现、游戏 Mod、Runtime 行为配置与
   Definition catalog，边界反而变混。
```

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

### 4.2 profile 只在 Agent Core 启动时生效

选择动作本身随时可以做（见 §4.3），受限的是**生效时机**：

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

### 4.3 切换 = 写"下次启动的选择"，本进程不改行为

游戏 profile 比模型配置更重（prompt、task、memory policy、timeout、definitions、tool 行为全在内），
而模型配置在 `ready` 之后已经不允许直接改写（`/api/setup/model` 返回 `already_configured`）。
本阶段沿用"不热切换"，但 **不热切换 ≠ 不写磁盘**：

```text
本次 Runtime 生命周期内 profile 固定：
    当前加载的 profile = 进程启动时读取 active-game.json 后冻结

ready 状态下选择另一个游戏
    → 校验 game_id
    → 原子写 active-game.json        ← 写的是"下次启动用哪个"
    → 不修改当前 Agent Core
    → 返回 restart_required

重启
    → 读新的 active-game.json
    → 加载新 profile
```

若 ready 状态下只返回 `restart_required` 而不写盘，重启后读到的仍是旧值，"重启后加载新
profile"就不成立——用户换了游戏、重启、发现毫无变化，而且没有任何机制解释原因。

状态上必须区分两个值，否则 Selected Game 的语义不清：

```text
loaded_game      当前 Runtime 真正在用的（启动时读取并冻结）
configured_game  active-game.json 里的当前值
两者不同          → restart_required = true
```

客户端据此显示：

```text
Current: Stardew Valley
Next:    RimWorld
Restart required
```

### 4.4 为什么不做热切换

热切换要同时处理：正在进行的 Turn、in-memory continuation、memory projection 的 game 边界、
能力集替换、以及 prompt 变更。任何一项没做对，都会出现"半新半旧"的会话，
而这正是最难诊断的一类问题。MVP0 的边界里也没有多游戏并发。

## 5. Selected Game 与 Connected Game

### 5.1 两个概念必须分开

```text
Selected Game    Runtime 加载的是哪份 Game Profile：即 loaded_game（§4.3）
Configured Game  active-game.json 里的当前值，即下次启动会加载哪个
Connected Game   当前哪个 Adapter 真的连上来了（来自 AdapterHello.game_id）
```

UI 上必须都显示，否则用户无法判断"为什么没反应"：

```text
Selected:   RimWorld
Adapter:    RimWorld 1.6   Connected ✓
```

mismatch 判定用 **loaded_game**——那是本进程真正在跑的 profile，与"下次启动的选择"无关。

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
3  ready 状态下 POST /api/setup/game 仍然写 active-game.json，并返回 restart_required：
   写入的是下次启动的选择，当前进程的 Agent Core 不受影响（§4.3）
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
        games/<game>/agent.json 支持 N 份；把 RimWorld profile 从
        adapters/rimworld/profile/ 迁入 games/rimworld/（§3.2）；
        active-game.json；游戏身份与 agent config 的分离解析（§3.4）；
        "恰好一份"约束移除

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
4   切换游戏在 ready 状态下写入 active-game.json 并返回 restart_required；
    当前进程的 loaded_game 不变，重启后加载 configured_game
5   重启后确实加载了新 profile：prompt、task policy、definitions 三者同时生效
6   既有 v0.1.0 data root 有一条明确路径获得新游戏 profile，且该路径经过验证
7   /api/status 同时暴露 loaded_game（Selected）、configured_game 与 Connected
8   Adapter 的 game_id 与所选 profile 不一致时被拒绝，且错误可读
9   首次启动流程为"选游戏 → 选模型 → Ready"，全程不需要用户编辑任何文件或环境变量
10  Runtime Core 未新增任何 game-specific 分支（games/ 目录内容属数据，不属核心逻辑）
```

第 7 条的 Selected / Connected 以 Real 实机为准：一个真实 Stardew Adapter 与一个真实 RimWorld Adapter
分别连接，两种顺序都验证过。

## 11. 已知债务与风险

```text
若决定先做 10.5（拆仓），§3.5 必须提前单独完成。

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
