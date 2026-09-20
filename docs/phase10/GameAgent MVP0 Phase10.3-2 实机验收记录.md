# GameAgent MVP0 Phase10.3-2 实机验收记录

> 状态：**身份与时钟部分已实机验收；商队部分未验证**（原因见 §4）。
> 方案见 [Phase10.3 技术方案](GameAgent%20MVP0%20Phase10.3%20RimWorld%20对话接入与世界实例实体技术方案.md)。
> 10.3-1 的证据见 [10.3-1 实机验收记录](GameAgent%20MVP0%20Phase10.3-1%20实机验收记录.md)，
> 10.3-3 与 10.3-4 的证据见 [10.3-3/10.3-4 实现与证据记录](GameAgent%20MVP0%20Phase10.3-3与10.3-4%20实现与证据记录.md)。

## 1. 环境

```text
RimWorld        1.6.4871 rev591（Windows x64，Unity 2022.3.35f1 / Mono 6.13）
游戏路径        D:\data\project\RimWorld
Runtime         全新 dev data root，默认适配器端口 127.0.0.1:50051
本次运行构建    含 cda69c7 的构建
```

## 2. 退出条件

```text
5    world_id 跨 save/load 稳定
     ✅ 见 §3：一次运行内 world_id 始终为 2246980492de438d85d92a0f2b927270，
        存档两次、读档一次，四处记录全部相同

6    Pawn entity_id 跨 save/load 与商队稳定，且其确切形式已在实机记录并冻结
     ✅ save/load：5 名殖民者在存档与读档两侧取值完全相同（§3）
     ✅ 确切形式已冻结：GetUniqueLoadID() 返回 "Thing_" + defName + 数字，
        因此 entity_id 形如 pawn:Thing_Human89。适配器不重建这个格式，只加一次 pawn: 前缀
     ⬜ 商队：未验证，见 §4

7    同一 world_id 内两个不同 eligible colonist 的 entity_id 必须不同
     ✅ 同一殖民地 5 名殖民者得到 5 个互不相同的 entity_id（§3）

8    Event 与 Observation 使用同一个 tick-only GameTime；同一 loaded run 内单调递增，
     save → reload 同一保存点时间一致
     ✅ 单调：同一次运行内 tick 依次为 1 → 2001 → 3065 → 5065，没有回退
     ✅ save → reload 一致：存档时 tick=3064，读档时 tick=3064，完全相同
     ✅ 单一来源：适配器只有 GameClock 一处读 tick，Event 与 Observation 都走它
     ⬜ Event ↔ Observation 的配对：需要 GameEvent 与 ObserveRequest 同时存在，
        已在 10.3-4 收口，见 10.3-3/10.3-4 记录的 §3
```

## 3. 原始摘录

以下是本次运行 `Player.log` 中 `[WIA]` 行的摘录，按时间顺序，未做删改（省略号处是被
截断的重复位置刷新行）。

```text
[WIA] clock tick=1 world=2246980492de438d85d92a0f2b927270
[WIA] identity tick entity=pawn:Thing_Human89  name=塔斯坎姆      def=Human map=0 position=(130, 0, 145) in_caravan=False
[WIA] identity tick entity=pawn:Thing_Human91  name=银色          def=Human map=0 position=(129, 0, 145) in_caravan=False
[WIA] identity tick entity=pawn:Thing_Human93  name=卡梅尼亚特拉  def=Human map=0 position=(122, 0, 145) in_caravan=False
[WIA] identity tick entity=pawn:Thing_Human95  name=亚斯拜        def=Human map=0 position=(128, 0, 147) in_caravan=False
[WIA] identity tick entity=pawn:Thing_Human101 name=桦树皮        def=Human map=0 position=(125, 0, 141) in_caravan=False

[WIA] identity save world=2246980492de438d85d92a0f2b927270 tick=217
[WIA] identity save entity=pawn:Thing_Human89  ... position=(129, 0, 149) in_caravan=False
[WIA] identity save entity=pawn:Thing_Human91  ... position=(128, 0, 141) in_caravan=False
[WIA] identity save entity=pawn:Thing_Human93  ... position=(126, 0, 146) in_caravan=False
[WIA] identity save entity=pawn:Thing_Human95  ... position=(125, 0, 149) in_caravan=False
[WIA] identity save entity=pawn:Thing_Human101 ... position=(125, 0, 141) in_caravan=False
[WIA] identity save world=2246980492de438d85d92a0f2b927270 tick=217   ← 同一次存档被序列化两遍，内容一致
（...）

[WIA] clock tick=2001 world=2246980492de438d85d92a0f2b927270
（...位置刷新...）

[WIA] identity save world=2246980492de438d85d92a0f2b927270 tick=3064
[WIA] identity save entity=pawn:Thing_Human89  ...   entity=pawn:Thing_Human91  ...
[WIA] identity save entity=pawn:Thing_Human93  ...   entity=pawn:Thing_Human95  ...
[WIA] identity save entity=pawn:Thing_Human101 ...

[WIA] identity load world=2246980492de438d85d92a0f2b927270 tick=3064   ← 与存档时完全相同
[WIA] identity load colonists=0                                        ← 见 §5 限制 1
[WIA] clock tick=3065 world=2246980492de438d85d92a0f2b927270
[WIA] identity tick entity=pawn:Thing_Human89  ... in_caravan=False    ← 与存档前同一批 entity_id
[WIA] identity tick entity=pawn:Thing_Human91  ... in_caravan=False
[WIA] identity tick entity=pawn:Thing_Human93  ... in_caravan=False
[WIA] identity tick entity=pawn:Thing_Human95  ... in_caravan=False
[WIA] identity tick entity=pawn:Thing_Human101 ... in_caravan=False

[WIA] clock tick=5065 world=2246980492de438d85d92a0f2b927270
[WIA] identity save world=2246980492de438d85d92a0f2b927270 tick=5423
```

判据：

```text
world_id       四处记录（两次 save、一次 load、四条 clock）取值完全相同
entity_id      5 个取值互不相同；save 与 load 两侧同名殖民者取值相同
tick           1 → 2001 → 3065 → 5065 单调；save=3064 与 load=3064 相等
```

## 4. 未验证的部分

```text
条件 6 的远行队部分：Map → 远行队 → Map 期间 Pawn.Map == null 且 entity_id 不变。
```

原因是**可观测的，不是推测**：验收用的殖民地来自 `-quicktest`，在该殖民地上

```text
选中殖民者后命令栏只有「WIA 对话」与「征召」两个 gizmo，没有组建远行队入口；
切到世界地图单选自己的定居点，gizmo 栏也只有 dev 模式的几个世界工具与视图切换。
```

也就是说这张测试地图没有提供组建远行队的入口，需要一局通过正常新开档流程建立的殖民地。
补验步骤：

> 术语：Caravan 在游戏**官方简体中文**里译作「远行队」，不是社区常说的「商队」。
> 对应英文键 `CommandFormCaravan`（组建远行队）与 `CommandSendCaravan`（派出远行队）。

```text
1  正常新开一局（含 ≥2 名殖民者）或载入一局正常存档，等待 [WIA] identity tick 行出现
2  选中至少一名殖民者 → 命令栏「组建远行队」→ 对话框里确认 → 世界地图上让它真的出发
   预期：出现 map=none position=none in_caravan=True，且 entity= 与出发前相同
3  让远行队返回定居点
   预期：map= 与 position= 恢复数值，entity= 仍然相同
```

## 5. 已知限制

```text
1  §3 中的 identity load colonists=0 是当时构建的行为，不是身份问题：
   适配器在 LoadSaveMode.PostLoadInit 报告名册，而那一刻 pawn 列表尚未重建。
   该行已在 c10c47a 修掉——读档现在只报 world 与 tick（这两项在该时机是正确的），
   名册由读档后的第一个 tick 报出。身份本身在两侧都是对的，见紧随其后的 tick=3065 行。

2  §3 的摘录来自 cda69c7 构建。此后与身份相关的改动只有诊断输出本身：
   WorldIdentityComponent 增加了 CurrentWorldId() 访问器，IdentityDiagnostics 不再在
   读档时报名册。GameClock、EntityId、WorldComponent 的持久化逻辑未改动。

3  本记录不覆盖 world lineage 的语义：Save As 与复制存档会继承同一个 GUID。
   这是方案 §6.2 已接受并记录的语义，不是缺陷。
```

## 6. 本阶段不做的事

```text
不映射 calendar（year / quadrum / day / hour / minute）：本集成冻结为 tick-only
不把 Map 放进 Agent identity：商队与部分世界状态下 Pawn.Map 就是 null
不为显式载入更早存档新增回滚机制：沿用 Runtime 既有的 game-time filtering
```
