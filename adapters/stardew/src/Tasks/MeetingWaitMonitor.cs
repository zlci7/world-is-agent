using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Tasks;

public sealed record NpcWaitSample(WorldPosition Position, bool ControlOwned);

public sealed record WaitEvidence(
    TaskOperationSource Source,
    long OccurredAt,
    string Outcome,
    string Code,
    WorldPosition NpcPosition,
    long? WaitUntil = null);

public sealed class MeetingWaitMonitor
{
    private readonly Dictionary<OperationKey, ActiveWait> active = new();
    private readonly HashSet<OperationKey> completed = new();

    public IEnumerable<OperationKey> Operations => this.active.Keys;

    public bool Register(TaskOperationSource source, WorldPosition target, long observedAt)
    {
        if (this.completed.Contains(source.Operation))
            return false;
        if (this.active.TryGetValue(source.Operation, out ActiveWait? current))
            return current.Source == source && current.Target == target;

        this.active.Add(source.Operation, new ActiveWait(
            source,
            target,
            observedAt,
            observedAt <= source.Contract.StartAt,
            observedAt >= source.Contract.StartAt && observedAt < source.Contract.EndAt));
        return true;
    }

    public WaitEvidence? Observe(
        OperationKey operation,
        RuntimeWorldSnapshot world,
        NpcWaitSample npc,
        WorldPosition? player)
    {
        if (!this.active.TryGetValue(operation, out ActiveWait? wait))
            return null;

        if (!Matches(operation, world))
            return this.Finish(wait, world.NowTick, "interrupted", "world_changed", npc.Position);
        if (!npc.ControlOwned)
            return this.Finish(wait, world.NowTick, "interrupted", "control_lost", npc.Position);
        if (npc.Position != wait.Target)
            return this.Finish(wait, world.NowTick, "interrupted", "npc_left_landmark", npc.Position);
        if (world.NowTick < wait.LastObservedAt)
            return this.Finish(wait, world.NowTick, "interrupted", "time_reversed", npc.Position);

        if (world.NowTick >= wait.Source.Contract.EndAt)
        {
            bool canProveExpired = wait.RegisteredByStart && wait.ObservedInsideWindow;
            return this.Finish(
                wait,
                world.NowTick,
                canProveExpired ? "unsatisfied" : "interrupted",
                canProveExpired ? "expired" : wait.RegisteredByStart ? "time_jump_unknown" : "wait_started_late",
                npc.Position);
        }

        if (world.NowTick >= wait.Source.Contract.StartAt)
        {
            wait = wait with { LastObservedAt = world.NowTick, ObservedInsideWindow = true };
            this.active[operation] = wait;
            if (player is not null && IsPlayerArrival(npc.Position, player))
                return this.Finish(wait, world.NowTick, "satisfied", "met", npc.Position);
            return null;
        }

        this.active[operation] = wait with { LastObservedAt = world.NowTick };
        return null;
    }

    public void Clear()
    {
        this.active.Clear();
        this.completed.Clear();
    }

    public bool Cancel(OperationKey operation)
    {
        if (!this.active.Remove(operation))
            return false;
        this.completed.Add(operation);
        return true;
    }

    public bool Cancel(string taskId, string operationId)
    {
        OperationKey? operation = this.active.Keys.SingleOrDefault(candidate =>
            string.Equals(candidate.TaskId, taskId, StringComparison.Ordinal) &&
            string.Equals(candidate.OperationId, operationId, StringComparison.Ordinal));
        return operation is not null && this.Cancel(operation);
    }

    private WaitEvidence Finish(ActiveWait wait, long occurredAt, string outcome, string code, WorldPosition position)
    {
        this.active.Remove(wait.Source.Operation);
        this.completed.Add(wait.Source.Operation);
        return new WaitEvidence(wait.Source, occurredAt, outcome, code, position);
    }

    private static bool Matches(OperationKey operation, RuntimeWorldSnapshot world) =>
        string.Equals(operation.WorldId, world.WorldId, StringComparison.Ordinal) &&
        string.Equals(operation.WorldRunId, world.WorldRunId, StringComparison.Ordinal) &&
        operation.ExecutionGeneration == world.ExecutionGeneration;

    private static bool IsPlayerArrival(WorldPosition npc, WorldPosition player) =>
        string.Equals(npc.Location, player.Location, StringComparison.Ordinal) &&
        Math.Abs(npc.X - player.X) + Math.Abs(npc.Y - player.Y) <= 2;

    private sealed record ActiveWait(
        TaskOperationSource Source,
        WorldPosition Target,
        long LastObservedAt,
        bool RegisteredByStart,
        bool ObservedInsideWindow);
}
