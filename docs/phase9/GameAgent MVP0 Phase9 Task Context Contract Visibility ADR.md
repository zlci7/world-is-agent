# GameAgent MVP0 Phase9 Task Context Contract Visibility ADR

> **Status:** Accepted
> **Date:** 2026-09-15
> **Scope:** Task Context 对模型暴露的字段边界、Adapter 契约的可见性，以及"唤醒该做什么"的策略归属
> **Architecture Baseline:** GameAgent Runtime Architecture v0.9（`AGENTS.md` 架构边界）
> **Roadmap Baseline:** GameAgent 阶段规划 — Phase9.3
> **Protocol Baseline:** gameagent.protocol.v1alpha2（本 ADR 不改协议）

---

# 1. Decision

Runtime 在 Task Context 中新增三个由 Runtime 拥有、与游戏无关的字段，并把 Adapter 写在 `TaskProposal.payload` 里的契约**原样透传**给模型：

```text
wake_reason   本次唤醒的真实原因，取自 wake 记录本身
wake_due_at   本次唤醒的约定时刻，取自该 wake 的 due_tick
contract      任务创建时写入的 Spec.Contract，原样透传
```

Runtime 只负责传递，不负责解释：

```text
- Runtime 源码不出现任何游戏专名（landmark、community_center 等）；
- Runtime 不解析 contract 内部任何键，不按 contract 内容分支；
- Runtime 不因 contract 内容改变任务语义。
```

契约的可见性采用**单一生产者、单一消费者、不可变**模型：

```text
生产者   create_task（Runtime 工具），数据来自 Adapter 的 TaskProposal.payload
存储     task.Spec.Contract（json.RawMessage），创建后不再修改
消费者   Task Context 渲染，原样进入模型上下文
```

"唤醒之后该做什么"属于游戏策略，写在游戏 prompt profile，不进入 Runtime。

---

# 2. Rationale

Phase9.3 实机暴露了一个确定的失败：任务准时唤醒后，模型没有调用 `move_to_landmark`，而是调用 `face_player` 与两次参数为空的 `update_task`，用尽步数后 Turn 失败，任务停在 `paused / evidence_unconfirmed`，NPC 从未出发。

根因不是执行链路，而是**出发决策缺少事实依据**：

```text
1. HandleTaskWake 把 WakeReason 硬编码为 "task_wake"，wake 记录里的真实原因没有进入上下文；
2. Task Context 只有 instruction 文本，没有约定地点、没有见面窗口、没有契约；
3. 因此模型必须从一句中文里推断"此刻应该走到 community_center 并登记等待"。
```

同一条链路此前成功过一次，说明模型有能力推断，但推断不可靠。把地点与时间做成结构化字段，是把"靠推理"换成"给事实"。

同时必须回答一个架构问题：地点与时间是游戏语义，而 Task Context 由 Runtime 渲染。本 ADR 明确两者的边界，避免 Runtime 为了一个游戏字段而开始解析游戏内容。

---

# 3. Current Facts

本 ADR 基于以下已核对的事实：

```text
HandleTaskWake 硬编码 WakeReason="task_wake"
    runtime/internal/agent/task_wake.go

wake 记录本身带真实 reason
    task_wakeups.reason 列，取值如 created / evidence_wait / intent_wait

Spec.Contract 是不透明 JSON
    task.TaskSpec.Contract 为 json.RawMessage，创建时由 create_task 序列化 TaskProposal 写入，
    Runtime 仅校验它是合法 JSON

update_task 的 wait / cancel 不修改 Spec
    intent 校验包含 taskSpecsEqual(response.Spec, current.Spec)，
    只改 Record.State / Record.NextWakeAt，并插入新的 wake

Task Context 当前字段
    task_id / instruction / state / revision / next_wakeup_at / deadline_at
    / wake_reason / wake_id / reason
```

其中最后两条决定了字段来源：`Spec.WakeAt` 是创建时的原值，**不能用它表示"本次唤醒的时刻"**，后者必须取当前 wake 的 `due_tick`。

---

# 4. Field Semantics

Task Context 新增字段定义：

```text
wake_reason   string，仅在本 Turn 由 task wake 触发时出现
              取值 = 当前 wake 记录的 reason 原值

wake_due_at   int64，仅在本 Turn 由 task wake 触发时出现
              取值 = 当前 wake 记录的 due_tick

contract      object，任务存在 Spec.Contract 时出现
              取值 = Spec.Contract 原样反序列化后的 JSON
```

渲染示例（预约任务，出发唤醒）：

```json
{
  "task_id": "task_1789442859014940600_1974",
  "instruction": "11点前往社区中心与渝大师见面，听取他要说的事情。",
  "state": "running",
  "revision": 4,
  "next_wakeup_at": null,
  "deadline_at": 372820,
  "wake_reason": "created",
  "wake_due_at": 372000,
  "wake_id": "wake_1789442859014940600_1975",
  "contract": {
    "clock": {"id": "stardew.game_time.v1", "tick": 371910, "sequence": 5},
    "wake_at": 372000,
    "deadline_at": 372820,
    "participant_entity_ids": ["npc:Linus", "player:local"],
    "equivalence_key": "meeting-v1:...",
    "payload": {
      "landmark_id": "community_center",
      "start_at": 372090,
      "end_at": 372150,
      "clock_id": "stardew.game_time.v1",
      "schema_version": 1
    }
  }
}
```

`wake_reason` 与 `wake_due_at` 只在 wake Turn 出现；`contract` 只要任务带契约就出现，玩家对话 Turn 同样可见，因为模型需要它回答"我们约了几点"或判断是否取消。

---

# 5. Contract Bounds

`contract` 进入 Task Context 的**不可裁剪权威段**（已有 `TestTaskContextAuthorityCannotBeCroppedToFit` 守卫该性质）。因此它必须有界：

```text
- 创建期强制：Spec.Contract 序列化长度不得超过 4096 字节；
- 该上限同时适用于写入与读取校验，使"已持久化即必有界"成立；
- 超限输入在 create_task 阶段拒绝，返回 invalid_task_spec，不落库；
- Runtime 不对 contract 内部做任何字段级校验。
```

当前唯一生产者是 `create_task`，现有契约约 400 字节，远低于上限。

---

# 6. Policy Ownership

本 ADR 区分"事实"与"策略"：

```text
事实（Runtime 负责提供）
    wake_reason、wake_due_at、contract

策略（游戏负责提供）
    "出发唤醒时应 move_to_landmark，再 wait_for_player"
    → runtime/config/games/<game>/agent.json 的 prompt.tool_instruction
```

Runtime 的通用 authority instruction 允许补一句中性说明：task wake 表示该任务的约定时刻已到，模型应依据 Task Context 决定动作。该句不含任何游戏概念，也不替代 Runtime 已有的状态机约束——任务时序、窗口、过期判定仍由 Runtime 与 Adapter 强制，不依赖模型是否遵守提示。

---

# 7. Alternatives Rejected

```text
A. Runtime 解析 contract 里的 landmark_id 并写进上下文
   拒绝。Runtime 将出现游戏专名与游戏字段分支，破坏 game-agnostic 边界。

B. Adapter 把地点信息塞进 Observation.state.<game>
   拒绝。游戏私有字段进入模型上下文，Runtime core 又不能解析它，
   而且与任务契约形成双来源。

C. 只修改游戏 prompt profile，不增加字段
   拒绝。事实缺失，模型只能继续推理；提示词不能作为执行约束的唯一来源。

D. 新增协议消息或协议字段承载契约
   拒绝。契约已经在 task spec 中，属于 Runtime 内部状态，不需要经过协议。
```

---

# 8. Non-Goals

```text
- 不修改 Protocol；
- 不修改任务语义：唤醒时刻、见面窗口、过期与取消判定保持现状；
- 不修改 Adapter 的地标目录、路线白名单与轨迹模型；
- 不修改 create_task / update_task 的输入 schema；
- 不解决 present_dialogue 的 interaction_context_conversation_changed；
- 不解决 update_task 空参数只返回 tool_arguments_invalid 的问题；
- 不引入跨 run 的任务恢复（属于 Phase9.4）。
```

---

# 9. Acceptance

```text
1. Runtime tests 证明 task wake Turn 的 Task Context 携带 wake 记录的真实 reason。
2. Runtime tests 证明 wake_due_at 取当前 wake 的 due_tick，
   且在 update_task wait 之后与原始 Spec.WakeAt 不同。
3. Runtime tests 证明 contract 原样出现在 Task Context，Runtime 不解析其内部键。
4. Runtime tests 证明超过 4096 字节的 contract 在创建期被拒绝且不落库。
5. Runtime tests 证明 Task Context 缺少 contract 时其它字段与现有行为一致。
6. 架构检查证明 runtime 源码不出现游戏专名与 contract 字段名。
7. Stardew profile 的 tool_instruction 说明出发唤醒流程。
8. 实机验收：重新预约一次，出发唤醒调用 move_to_landmark，到达后登记 wait_for_player，
   并在窗口内产生 met 或过期证据。
```

验收命令：

```powershell
go test ./... -count=1
powershell -NoProfile -File protocol/tests/check-protocol-static.ps1
powershell -NoProfile -File scripts/check-architecture.ps1
dotnet test adapters/stardew/tests/TaskExecution.Tests/TaskExecution.Tests.csproj
git diff --check
```
