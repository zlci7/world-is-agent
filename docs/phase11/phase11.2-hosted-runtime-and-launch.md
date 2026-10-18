# World Is Agent Phase11.2 云端 Runtime 托管与账号技术方案

日期：2026-10-18（Asia/Shanghai）

状态：邀请内测源码与后台验证具备；真实 Linux Docker 部署、公网游戏连接、完整卷恢复及容量验收待完成。验证范围见 [验收记录](phase11.2-acceptance.md)，部署操作见 [Hosted invitation beta](../../deploy/hosted/README.md)。

## 1. 产品范围

首版面向 Windows 单人 Stardew Valley。玩家安装 SMAPI 与 WIA Mod，在网站凭邀请码、邮箱和密码注册，取得个人 WIA Key，在游戏中按 F8 配置连接。平台提供模型凭据，玩家电脑负责游戏模拟与动作执行，服务端保存角色历史、预约与显式玩家偏好。

- 账号模块独立于 Runtime，以服务端生成的稳定 `user_id` 作为数据所有者。
- 每个账号分配一个独立 Runtime 容器和持久卷，沿用卷内的 game/world/NPC 数据边界。
- 同一账号同一时刻连接一个安装实例。相同安装重连先退役旧流；不同安装的并发连接被拒绝。
- 网页提供注册登录、安装指引、密钥管理、连接状态、只读预约、近期回合、偏好和额度。
- 本地 Runtime、控制台、模型配置与两款游戏的本地接入保持独立使用。

首版采用单节点、固定 DeepSeek Provider 和平台指定模型。公开自由注册、邮箱所有权验证、SMTP、支付、多节点调度、多设备并发、RimWorld 托管和离线游戏模拟均在范围外。现有 Action continuation 仍为进程内状态。

## 2. 系统与仓库边界

```text
浏览器 ── HTTPS ──► Caddy ──► wia-hosted
                              ├─ accounts：身份、邀请、会话、Key、偏好、额度
Stardew + SMAPI + WIA Mod      ├─ TLS 接入后的 gRPC 流路由
       │ TLS gRPC             ├─ 单写入者 Docker worker 分配
       └──────────► Caddy ────┤
                              └─ 私有模型代理 ──► 平台模型
                                      ▲
                              账号 Runtime worker
                              ├─ 私有 gRPC / 只读 HTTP
                              ├─ 原有 Core、Game Profile、Task、Memory
                              └─ 账号持久卷
```

| 位置 | 职责 |
| --- | --- |
| `runtime/internal/accounts/` | 独立账号与管理库、凭据验证、偏好版本、额度账本 |
| `runtime/cmd/hosted/`、`runtime/internal/hosted/` | 公共 HTTP、认证流路由、模型代理、Docker 所有权、部署锁与管理命令 |
| `runtime/cmd/worker/`、`runtime/internal/workerapi/` | headless Runtime、私有认证、白名单查询和偏好缓存 |
| `runtime/internal/context/`、Agent 装配 | 可选、通用且有界的 PlayerPreferences 数据投影 |
| `console/web/src/hosted/` | 中文账号网站；复用现有预约组件 |
| `deploy/hosted/` | Linux 镜像、Compose、Caddy、配置示例和备份入口 |
| Adapter 仓库 `stardew-valley/` | HTTPS、WIA Key、安装 ID、游戏内连接设置、重连及发行包地址 |

Core 不查询账号、计费或容器库，不解释游戏私有状态。Protocol 消息保持现有版本，认证通过调用 metadata 完成。Phase11.1 的 Task 恢复、查询和只读组件作为基线，账号身份属于托管层。

## 3. 账号与身份

### 注册与网页登录

运维使用私有 CLI 发放有期限的一次性邀请码。玩家用邀请码、邮箱和 12–128 字符密码注册；邮箱仅作为登录标识。密码按 `argon2id-v1` 加盐保存，当前参数为 64 MiB、3 次、1 lane、32 字节输出；同时最多执行 2 次密码哈希。正式服务器仍需测量其开销。

账号主秘密由环境提供，至少 32 字节并在部署生命周期内保留。已有管理库会校验其验证值，秘密不匹配时拒绝打开。邀请、会话和 Key 保存 HMAC 验证摘要。

网页登录使用 Secure、HttpOnly、SameSite=Strict Cookie。修改请求校验 Host、Origin 和 CSRF。认证入口具有持久化的分钟预算：同邮箱每分钟 6 次、入口来源每分钟 30 次；示例 Caddy 下来源为代理连接，不声称提供真实终端 IP 限流。密码恢复与停用仅由运维 CLI 执行，撤销该账号会话与设备 Key，并写入审计。

### 三类凭据

| 凭据 | 权限与生命周期 |
| --- | --- |
| 网页会话 | 网站账号操作；配置有效期，退出撤销当前会话 |
| WIA Key | Mod 接入；每账号最多 5 个有效 Key，明文创建时显示一次，有效期由配置决定 |
| worker 内部凭据 | 私有查询、模型与偏好读取；绑定账号及分配 generation，重建容器时轮换 |

`device_id` 在 Mod 安装配置中生成，表示安装而非用户授权。支持的 Hello 校验成功后，Key 首次绑定该设备。Key 不进入游戏消息体、模型上下文或 URL。

## 4. 账号存储与 worker 所有权

管理库位于控制进程的数据卷，保存账号、邀请、会话、Key、偏好、额度、generation、已分配卷标记和审计。每个账号的 Runtime 卷包含原有配置、Memory、经历、Task、checkpoint 与 Trace。游戏存档继续留在玩家电脑。

容器名称为 `wia-usr_<id>`，卷为 `wia-usr_<id>-data`。容器与卷具有部署归属和用户标签。部署 ID 从保留的账号主秘密派生，用于限定容量统计、关闭和备份范围。

- 控制进程持有数据根部署锁，分配操作由单一管理锁串行执行。
- 容器使用固定镜像标签、独立命名卷、私有网络、CPU/内存/PID 预算及只读根文件系统；worker 不挂载 Docker socket。
- 重启同一容器使用原卷和 generation。确认原容器不存在后，重建容器并轮换内部凭据。
- 重用前检查镜像、账号、部署与 generation；已有卷检查账号与部署标签。
- 已分配卷缺失或归属不符时返回恢复受阻，不创建替代空历史。Docker 清单读取失败按不可用处理。
- 私有状态读取确认 Runtime Ready 后发布分配。Runtime Ready、游戏连接数量和任务世界 Ready 分别表达。
- 空闲 worker 保留并占用容量；首版由运维在维护窗口处理，不提供自动空闲淘汰。

数据权限由服务端认证路由确定。浏览器与 Mod 不能提交另一个 `user_id`、目录或后端地址来选取数据。不同账号即使声明同样的游戏、存档、角色和经历 ID，也使用不同卷。

## 5. gRPC 网关与 Mod

Caddy 提供网站与游戏网关两个域名的证书及 HTTP/2 接入。网关只转发既有 Connect 双向流，worker 端口和模型代理不公开。

接入先验证 WIA Key 与设备，再在 10 秒内校验首条 Hello、`v1alpha2`、已配置 game ID 和最低 Adapter 版本。服务器重新建立私有 metadata，客户端身份 header 不直接转发。长流生命周期内固定路由至一个账号 worker。

同设备重连取消旧流并等待退出，再建立新转发；不同设备返回 `AlreadyExists`。世界绑定、run、generation 和 Task 事实协调继续由原 Runtime / Adapter 执行。首版不额外建立跨重连的世界抢占协议，也不保证识别同账号复制存档形成的独立时间线。

Key 撤销主动断开对应流并取消模型请求；到期、停用与存储错误通过约一秒的有效性检查收敛。网关重启后重新认证，不重放结果未知的游戏动作。

Stardew Adapter 0.1.3 提供：

- `ConnectionMode=Local|Hosted`，保留本地 RuntimeAddress；Hosted 必须使用 HTTPS。
- F8 或 `gameagent_connection` 打开连接设置，Key 遮盖显示，保存配置后重连。
- gRPC metadata 携带 WIA Key 和设备 ID，沿用标准证书验证。
- 网络重试采用 1、2、4、8、16、30 秒上限及抖动，EnvironmentReady 后重置。
- 鉴权、版本、恢复受阻和其它活动设备错误停止自动重试，等待配置或管理员处理。
- 额度和繁忙结果以中文提示。连接故障保留原生游戏交互。
- 打包参数 `-HostedAddress` 附带正式服务地址及空 Key，不分发共享密钥。

## 6. 模型预算与故障语义

worker 使用内部凭据访问私有 `/v1/chat/completions`。控制进程持有真实上游 Key，固定 Provider、模型、地址与超时；不接受浏览器或游戏指定任意上游。

- 每账号最多 1 个模型请求，全局并发使用显式配置，超过容量立即返回有类型的繁忙结果。
- 请求模型与最大输出遵守平台配置，关闭 streaming；序列化请求字节数受上下文与输出预算的保守上界约束。
- 调用前原子预留整个配置的 context window，UTC 日界计入账号每日额度。
- 可靠 usage 到达后结算并释放未使用预留；失败、取消或结果未知保留预留。
- 请求 ID 具有唯一性，已预留请求不再次提交上游。账本跨重启保留。
- 凭据失效或连接关闭会取消活动请求；不能把模型服务错误转换为 Task 成功。

示例配置为 131072 context、4096 output、每日 1000000 tokens、全局并发 2、模型代理超时 45 秒。它们是启动示例，不代表容量或费用验收。已交付 Stardew Profile 仍使用 5 步、每步最多 4 次工具调用、每回合最多 6 次调用、回合 270 秒等现有预算。

## 7. 显式玩家偏好

账号偏好只有称呼（40 字符）、语言（中文/英语）与回复长度（自然/简短/详细）。按版本条件保存，冲突要求重新读取。留空称呼并选择中文、自然表达即为默认值。

worker 约每 5 秒刷新，读取故障保留最近确认值。偏好以单独的通用 PlayerPreferences 区块进入模型上下文；没有该输入时本地行为不变。偏好不改写 NPC definition、系统规则或工具权限。NPC 经历和关系仍属于游戏世界，账号偏好跨该账号世界复用。

## 8. 网站与 API

沿用已确认的中文像素农场视觉、透明窗口、间距和对齐。四个账号栏目为连接游戏、预约任务、运行记录、账号偏好；网站不提供模型密钥或服务器管理参数。

| HTTP 接口 | 语义 |
| --- | --- |
| `POST /api/invitations/redeem` | 邀请注册并建立会话 |
| `POST /api/session`、`DELETE /api/session` | 登录、退出 |
| `GET /api/me` | 账号信息与 CSRF |
| `GET /api/me/connection` | 正式网关、游戏及最低版本 |
| `GET/POST /api/me/keys`、`DELETE /api/me/keys/{key_id}` | 元信息、一次性创建与撤销 |
| `GET/PUT /api/me/preferences` | 有限偏好及版本校验 |
| `GET /api/me/usage` | UTC 周期、额度、已结算、预留与剩余 |
| `GET /api/me/status`、`GET /api/me/turns` | 脱敏 Runtime 状态与有界回合列表 |
| `GET /api/me/task-worlds`、`/api/me/tasks`、`/api/me/tasks/detail` | 账号路由后的现有只读 Task 契约 |

Task 查询继续校验 instance/game/world，游标不改变账号归属。公共 DTO 排除服务器路径、上游凭据和原始 Trace。退出或会话失效清空视图、取消请求，并丢弃旧账号响应。读取失败明确提示；保留旧值时标明最后成功时间。离线任务表明记录保存在账号下，执行条件仍需游戏确认。

私有 worker HTTP 仅开放认证后的状态、任务和回合查询。偏好通过私有模型入口读取，不开放本地设置或任意路径代理。

## 9. 恢复、备份与部署

部署材料包括 Dockerfile、Compose、Caddy、环境与 JSON 示例、`backup.sh` 和运维说明。固定运行于 Linux amd64 单机，公网仅发布 80/443；控制进程独占 Docker 管理权限。

备份使用全服务维护窗口：停止控制进程、取消准入及模型请求、排空并关闭本部署 worker，确认停止后归档管理卷及本部署所有账号卷，生成校验和，再恢复服务。备份包括尚未挂接容器但仍保留的账号卷。恢复需保留部署主秘密、账号卷名称、标签与归档内所有者，并在隔离环境验证。

已分配卷缺失、数据库不可读或存档引用的 checkpoint 缺失时，保留故障事实并阻断恢复。普通 Memory 的历史分支限制仍存在，普通 Action continuation 不跨进程恢复。游戏关闭后服务器保留记录，游戏时间与真实动作暂停。

容器日志有大小及文件数量限制。Runtime Trace 与账号数据仍需运维制定容量告警、保留、备份频率和恢复目标；首版没有每账号磁盘硬配额、自动清理或内置监控平台。

## 10. 验收边界

源码完成条件包括相关自动化测试、本地 TLS 流与账号隔离测试、前端构建及后台浏览器验证、代码审查、架构和 Protocol 检查。

真实开放邀请前还需在目标部署环境完成：

1. 两个正式域名、证书与公网 gRPC 长流，干净玩家电脑只安装 SMAPI + Mod + WIA Key 完成对话。
2. 相同游戏/存档/角色标识的双账号隔离，密钥轮换、撤销、停用与过期反馈。
3. 保存读档、预约成功/过期、网络中断、worker 与控制进程重启后的事实一致性。
4. 完整卷备份在隔离主机恢复；缺失 checkpoint 时明确阻断。
5. 指定模型与服务器的容量、认证哈希开销、费用预算、磁盘告警、证书续期和发布回退。

[验收记录](phase11.2-acceptance.md) 区分已通过的本地证据和待完成的部署/游戏结论，源码验证不替代上线验收。
