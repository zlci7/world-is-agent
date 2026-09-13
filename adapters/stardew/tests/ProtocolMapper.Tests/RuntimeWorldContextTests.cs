using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class RuntimeWorldContextTests
{
    [Fact]
    public void SaveLoadCreatesANewRunEvenForTheSameWorld()
    {
        Queue<string> runIds = new(new[] { "run-a", "run-b" });
        RuntimeWorldContext context = new("stardew-valley", "stardew.game_time.v1", () => runIds.Dequeue());

        context.BeginWorld("Farm_1", 100);
        RuntimeWorldSnapshot first = Assert.IsType<RuntimeWorldSnapshot>(context.Current);
        context.AdvanceClock(110);
        RuntimeWorldSnapshot advanced = Assert.IsType<RuntimeWorldSnapshot>(context.Current);
        context.BeginWorld("Farm_1", 200);
        RuntimeWorldSnapshot second = Assert.IsType<RuntimeWorldSnapshot>(context.Current);

        Assert.Equal("run-a", first.WorldRunId);
        Assert.Equal((ulong)2, advanced.ClockSequence);
        Assert.Equal("run-b", second.WorldRunId);
        Assert.Equal((ulong)1, second.ClockSequence);
        Assert.Equal((ulong)0, second.ExecutionGeneration);
    }

    [Fact]
    public void DayAndClockUpdatesKeepTheRunAndAdvanceSequence()
    {
        RuntimeWorldContext context = new("stardew-valley", "stardew.game_time.v1", () => "run-a");
        context.BeginWorld("Farm_1", 100);

        context.AdvanceClock(1540);
        RuntimeWorldSnapshot snapshot = Assert.IsType<RuntimeWorldSnapshot>(context.Current);

        Assert.Equal("run-a", snapshot.WorldRunId);
        Assert.Equal(1540, snapshot.NowTick);
        Assert.Equal((ulong)2, snapshot.ClockSequence);
    }

    [Fact]
    public void BindingReadyCanOnlyAdvanceTheMatchingRun()
    {
        RuntimeWorldContext context = new("stardew-valley", "stardew.game_time.v1", () => "run-a");
        context.BeginWorld("Farm_1", 100);

        Assert.False(context.TryApplyBinding("stardew-valley", "Farm_1", "other-run", 1, out string mismatch));
        Assert.Equal("world_binding_mismatch", mismatch);
        Assert.True(context.TryApplyBinding("stardew-valley", "Farm_1", "run-a", 1, out _));
        Assert.Equal((ulong)1, context.Current!.ExecutionGeneration);
    }

    [Fact]
    public void ClearRemovesAdmissionButKeepsNoSyntheticWorld()
    {
        RuntimeWorldContext context = new("stardew-valley", "stardew.game_time.v1", () => "run-a");
        context.BeginWorld("Farm_1", 100);

        context.Clear();

        Assert.Null(context.Current);
    }
}
