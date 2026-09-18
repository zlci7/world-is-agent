# GameAgent MVP0 Phase10.2-2 本地控制面技术开发方案

> **Status:** 首个切片已实现并实机验证（状态与 turn 列表）；向导与可视化仍属后续子阶段
> **Date:** 2026-09-18
> **Phase:** Phase10 Ecosystem & Productization / 10.2 客户端与产品化
> **总方案:** [GameAgent MVP0 Phase10 技术开发与验收总方案](GameAgent%20MVP0%20Phase10%20技术开发与验收总方案.md)
> **上游约束:** 总方案 §3.3（客户端形态与选型已冻结）、§3.4（本地访问与密钥边界）、§3.7（退出条件）

---

## 1. 本切片的目标与范围

一句话目标：

> **启动 Runtime → 浏览器自动打开 → 看到 agent 的当前状态与最近的 AgentTurn。**

客户端**形态**（本地 Web UI、Vue 3 + TypeScript + Vite、`//go:embed`、不做桌面打包）与**发行形态**（Portable ZIP）已在总方案冻结，不在本文件讨论。本文件只记录该形态内部这一切片的设计与结果。

做：

```text
本地 HTTP 控制面       只监听 loopback，随 Bootstrap 启动，与 Agent Core 是否 Ready 无关
凭证自动交接           启动即打开浏览器并自动完成会话凭证交换，用户不复制任何东西
/api/status            状态、数据根、模型摘要、trace 路径
/api/turns             最近 AgentTurn 摘要（数据源是既有 JSONL trace）
资产内嵌               前端构建产物 embed 进 Runtime 二进制
```

不做（各自子阶段或明确排除）：

```text
首次运行向导、依赖体检、写配置与 key      10.2-3
secret 文件权限收紧、OS 密钥库            10.2-3
turn 详情与时间线、对话记录、任务与记忆视图  10.2-4
Release 打包、tag、CI                    10.2 的发行步骤
多用户 / 远程访问 / 云端托管 / 端口转发     总方案 §3.4 明确不做
```

## 2. 目录与 embed 约定

`go.mod` 位于仓库根，module 为 `gameagent`；`//go:embed` 不接受 `..`，因此 embed 包必须位于资产目录或其上层。据此确定：

```text
console/                   Go 包 gameagent/console，内嵌 dist；Runtime 从这里取前端
console/web/               Vite 工程，构建输出到 ../dist
console/dist/              构建产物，不入库，仓库只保留 .gitkeep
runtime/internal/httpapi/  控制面：路由、Host 校验、会话、静态资源
runtime/internal/traceview/ trace → turn 摘要投影
runtime/internal/browser/  调起系统浏览器
```

`dist/` 不入库的理由：入库的构建产物会与源码漂移，而且漂移是静默的。未构建时控制面返回 `503 assets_not_built` 并给出构建命令，比服务一份过期前端更可信。

`console/dist/.gitkeep` 必须存在，否则 embed 模式匹配不到任何文件、`go build` 直接失败。

前端构建**故意不清空** `dist/`（`emptyOutDir: false`）：Vite 默认会清空输出目录，那样每次构建都会删掉这个被跟踪的占位文件——工作区出现一个"被删除的已跟踪文件"，而从清空后的目录构建会直接失败。代价是历史哈希资源会留下，它们不再被引用，只是磁盘噪音。

## 3. 接口

| 方法 | 路径 | 凭证 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/session` | bootstrap token | 交换会话凭证，写入 HttpOnly cookie，返回 204 |
| GET | `/api/status` | 会话 | 状态、数据根、模型摘要、trace 路径、adapter 端点 |
| GET | `/api/turns?limit=` | 会话 | 最近 turn，最新在前；默认 50，上限 200 |
| GET | `/`、`/assets/*`、其它路径 | 无 | 前端资源；未知路径回落 `index.html`（客户端路由） |

前端资源不要求凭证：入口页必须先加载，才能用它交换 bootstrap token。

### 3.1 访问边界（总方案 §3.4 的落实）

```text
监听地址     host 必须是 loopback，否则拒绝启动（"0.0.0.0:8080"、":8080"、"192.168.x.x:8080" 均被拒）
Host 头      必须命中 127.0.0.1:<port> / localhost:<port> / [::1]:<port>，否则 403
             —— 这是本地 Web 服务最真实的攻击面：攻击者控制的域名可以解析到 127.0.0.1
凭证         /api 全部要求会话，唯一例外是 /api/session
cookie       HttpOnly + SameSite=Strict + Path=/；不设 Secure（本面是明文 http loopback，设了浏览器就不会存）
Origin       POST 校验 Origin，与外来源不匹配即 403（第二道锁，本面只有这一个改状态的路由）
```

### 3.2 凭证不让用户手工搬运

```text
启动 → 生成 32 字节随机 bootstrap token
     → 打开 http://127.0.0.1:<port>/#token=<token>
     → 前端把它 POST 给 /api/session，换到会话 cookie
     → 前端立即清掉 URL fragment
```

token 放在 **fragment**：浏览器不会把 fragment 发给服务器，因此它不进访问日志、不进代理、不进 Referer。页面的 `Referrer-Policy` 另外设为 `no-referrer`。

**bootstrap token 在进程生命周期内有效，且可多次交换。** 单次使用会让第二个浏览器、或清掉 cookie 后的刷新把用户逼到重启 Runtime；而能读到这个 URL 的人本来就能读到日志里的同一个值。重启即失效；需要立刻作废就重启进程。

### 3.3 密钥不回传

`/api/status` 的模型信息来自 `llm.DescribeConfig`，它只产出 `provider` / `model` / `base_url` / `api_key_env_name` / `api_key_configured`：

```text
env:VAR 形式      只回传变量名，不回传变量的值
直接写入的 key    不回传任何内容（没有可以安全显示的名字），只报告"未配置"
```

这个不变量由测试固定（`describe_test.go`），因为 10.2-3 的向导要建立在它上面。

### 3.4 端口

默认 `127.0.0.1:0`（临时端口）：gRPC 端口是发布契约必须固定，控制面不是，而临时端口没有"端口被占用"这一类用户可见故障。需要固定时用 `--http-addr`。

## 4. 自动打开浏览器

- 打开失败**不是**致命错误：日志同时打印带 token 的完整 URL，用户仍可手工打开。
- 控制面启动失败也**不是**致命错误：gRPC 适配器链路与它无关，因为一个起不来的 UI 让游戏也玩不了，是把局部问题升级成全局故障。
- `--no-open` 关闭自动打开。自动化测试必须使用它，否则跑测试会弹浏览器。

## 5. turn 摘要的边界

`traceview` 只投影"发生了哪些 turn、涉及谁、怎么结束"，**不重放** turn 内容；细节回到 trace 本身。

```text
状态         completed（turn_completed） / failed（turn_failed，带 reason 与 error） /
             unfinished（两者都没有：进程在 turn 结束前停住了）
顺序         最新在前，因为唯一消费者是"最近活动"视图
工具         tool_call_selected 的选择顺序，连续重复折叠
读上限       尾部 32MB；缓存按 (size, mtime) 失效；默认保留 200 个 turn
容错         trace 是 append-only 且可能正被写入进程追加，尾部半行必须跳过；
             单行解析失败不能带垮整个投影（否则模型换 schema 会让界面全空）
```

## 6. 验收结果

| 项 | 结果 | 证据 |
| --- | --- | --- |
| 只监听 loopback，非 loopback 地址拒绝启动 | 通过 | `TestRequireLoopbackRejectsReachableAddresses` |
| 伪造 Host 被拒、loopback Host 放行 | 通过 | `TestControlPlaneRejectsANonLoopbackHostHeader`、`TestControlPlaneAcceptsLoopbackHostNames` |
| 无凭证访问 `/api` 被拒 | 通过 | `TestAPIRequiresASessionCredential`（含伪造 cookie） |
| bootstrap token 错误/为空被拒且不下发 cookie | 通过 | `TestSessionExchangeRejectsAWrongBootstrapToken` |
| 外来源 POST 被拒 | 通过 | `TestSessionExchangeRejectsAForeignOrigin` |
| cookie 属性 | 通过 | `TestSessionCookieIsStrictAndHttpOnly` |
| 密钥不出现在 status 响应 | 通过 | `TestStatusReportsConfigurationWithoutTheCredential`、`TestDescribeConfigNeverEchoesAnInlineCredential` |
| 缺少模型配置是可显示的状态而非失败请求 | 通过 | `TestStatusReportsAMissingModelConfiguration` |
| turn 投影正确性 | 通过 | `TestTurnsProjectsACompletedTurn`、`TestTurnsProjectsAFailedTurn` 等 |
| 尾部半行 / 坏行 / 读上限不产生假 turn | 通过 | `TestTurnsSkipsATornTrailingLine`、`TestTurnsSkipsAMalformedLine`、`TestTurnsReadsOnlyTheTailWhenBounded` |
| 资产缺失时给出可操作提示而不是空白页 | 通过 | `TestAssetsReportAMissingBuildInsteadOfServingNothing` |
| 真实 trace 的端到端读取 | 通过 | 对本机 `runtime/data/traces.jsonl` 实机请求 `/api/turns`，返回真实 turn（含 10.1 的 C1 证据回合：`npc:Penny`、2 步、`send_mail` + `present_dialogue`、completed） |
| 浏览器自动打开并渲染状态卡与 turn 列表 | 通过 | 实机确认 |
| 非 Windows 启动器（darwin `open` / linux `xdg-open`） | **未验证** | 只在 Windows 实机跑过 |

自动化测试数：`traceview` 10 项、`httpapi` 17 项、`llm` 配置描述 3 项。

## 7. 已知限制

```text
模型配置仍需手工写入    <root>/config/model.json；向导属于 10.2-3
api_key 仍只接受 env:VAR  llm 包有测试显式拒绝内联 key。要在 UI 里填 key，必须由 10.2-3
                        单独决定落盘形态（明文 + 权限收紧，还是 OS 密钥库绑定）
secret 权限收紧未做      POSIX 0600 / Windows ACL；属 10.2-3
前端需自行构建           仓库不存 dist，也不含 Release 产物
无 CI                    控制面测试是本仓库当前的自动化边界
```

## 8. 与总方案的关系

本切片满足总方案 §3.7 的退出条件 2（浏览器自动打开、用户不复制 token、不需要前端工具链才能用）、5（游戏内触发一次交互后能在 Web UI 看到该 Turn）与 7（§3.4 约束全部生效）；条件 1、3、4、6 仍待 10.2-3 与发行步骤。
