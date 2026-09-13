using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class MeetingWaitMonitorTests
{
    private static readonly RuntimeWorldSnapshot World = new("stardew-valley", "Farm_1", "run-a", 1, GameClock.ClockId, 1800, 2);
    private static readonly WorldPosition Target = new("Beach", 28, 36);

    [Fact]
    public void PlayerArrivalInsideHalfOpenWindowProducesSatisfiedOnce()
    {
        MeetingWaitMonitor monitor = Registered();
        TaskOperationSource source = WaitSource();
        RuntimeWorldSnapshot inside = World with { NowTick = source.Contract.StartAt };

        WaitEvidence? first = monitor.Observe(source.Operation, inside, new NpcWaitSample(Target, true), new WorldPosition("Beach", 29, 36));
        WaitEvidence? duplicate = monitor.Observe(source.Operation, inside, new NpcWaitSample(Target, true), new WorldPosition("Beach", 29, 36));

        Assert.Equal("satisfied", first!.Outcome);
        Assert.Equal("met", first.Code);
        Assert.Null(duplicate);
    }

    [Fact]
    public void PlayerAtExactEndDoesNotCountAsMet()
    {
        MeetingWaitMonitor monitor = Registered(observedAt: WaitSource().Contract.StartAt);
        TaskOperationSource source = WaitSource();

        WaitEvidence? result = monitor.Observe(
            source.Operation,
            World with { NowTick = source.Contract.EndAt },
            new NpcWaitSample(Target, true),
            new WorldPosition("Beach", 29, 36));

        Assert.Equal("unsatisfied", result!.Outcome);
        Assert.Equal("expired", result.Code);
    }

    [Fact]
    public void JumpAcrossEntireWindowIsInterruptedNotUnsatisfied()
    {
        MeetingWaitMonitor monitor = Registered(observedAt: WaitSource().Contract.StartAt - 1);
        TaskOperationSource source = WaitSource();

        WaitEvidence? result = monitor.Observe(
            source.Operation,
            World with { NowTick = source.Contract.EndAt + 10 },
            new NpcWaitSample(Target, true),
            null);

        Assert.Equal("interrupted", result!.Outcome);
        Assert.Equal("time_jump_unknown", result.Code);
    }

    [Fact]
    public void ControlLossTerminatesWithoutWaitingForDeadline()
    {
        MeetingWaitMonitor monitor = Registered();
        TaskOperationSource source = WaitSource();

        WaitEvidence? result = monitor.Observe(
            source.Operation,
            World,
            new NpcWaitSample(Target, false),
            null);

        Assert.Equal("interrupted", result!.Outcome);
        Assert.Equal("control_lost", result.Code);
    }

    [Fact]
    public void PlayerMustBeInSameLocationAndWithinTwoTiles()
    {
        MeetingWaitMonitor monitor = Registered();
        TaskOperationSource source = WaitSource();
        RuntimeWorldSnapshot inside = World with { NowTick = source.Contract.StartAt };

        Assert.Null(monitor.Observe(source.Operation, inside, new NpcWaitSample(Target, true), new WorldPosition("Town", 28, 36)));
        Assert.Null(monitor.Observe(source.Operation, inside, new NpcWaitSample(Target, true), new WorldPosition("Beach", 31, 36)));
        WaitEvidence? met = monitor.Observe(source.Operation, inside, new NpcWaitSample(Target, true), new WorldPosition("Beach", 28, 38));

        Assert.Equal("satisfied", met!.Outcome);
    }

    private static MeetingWaitMonitor Registered(long? observedAt = null)
    {
        MeetingWaitMonitor monitor = new();
        TaskOperationSource source = WaitSource();
        Assert.True(monitor.Register(source, Target, observedAt ?? World.NowTick));
        return monitor;
    }

    private static TaskOperationSource WaitSource()
    {
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        return travel with { Operation = travel.Operation with { OperationId = "wait-a" } };
    }
}
