using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskExecutionDriverTests
{
    private static readonly RuntimeWorldSnapshot World = new("stardew-valley", "Farm_1", "run-a", 1, GameClock.ClockId, 1800, 2);

    [Fact]
    public void SameOperationRetryStartsOnlyOnce()
    {
        TaskExecutionDriver driver = new(new TaskSourceContextStore(), new TaskOperationReceipts(), new NpcControlLease());
        int starts = 0;
        TaskOperationSource source = TaskSourceContextStoreTests.Source();

        TaskExecutionOutcome first = driver.Begin(source, World, "travelling", "move:beach", () =>
        {
            starts++;
            return new DriverResult("running", string.Empty, new WorldPosition("Mountain", 29, 9));
        });
        TaskExecutionOutcome replay = driver.Begin(source, World, "travelling", "move:beach", () =>
        {
            starts++;
            return new DriverResult("running", string.Empty, new WorldPosition("Mountain", 29, 9));
        });

        Assert.Equal(1, starts);
        Assert.False(first.Replayed);
        Assert.True(replay.Replayed);
        Assert.Equal("running", replay.Result.Status);
    }

    [Fact]
    public void ScopeMismatchNeverCallsTheWorldDriver()
    {
        TaskExecutionDriver driver = new(new TaskSourceContextStore(), new TaskOperationReceipts(), new NpcControlLease());
        int starts = 0;

        TaskExecutionOutcome result = driver.Begin(TaskSourceContextStoreTests.Source(), World with { WorldRunId = "other" }, "travelling", "move:beach", () =>
        {
            starts++;
            return new DriverResult("running", string.Empty, new WorldPosition("Mountain", 29, 9));
        });

        Assert.Equal(0, starts);
        Assert.Equal("rejected", result.Result.Status);
        Assert.Equal("world_mismatch", result.Result.Code);
    }

    [Fact]
    public void TerminalStartReleasesOnlyItsOwnLeaseAndIsReplayed()
    {
        NpcControlLease leases = new();
        TaskExecutionDriver driver = new(new TaskSourceContextStore(), new TaskOperationReceipts(), leases);
        TaskOperationSource source = TaskSourceContextStoreTests.Source();

        TaskExecutionOutcome first = driver.Begin(source, World, "travelling", "move:beach", () =>
            new DriverResult("failed", "native_route_unreachable", new WorldPosition("Mountain", 29, 9)));
        TaskExecutionOutcome replay = driver.Begin(source, World, "travelling", "move:beach", () => throw new InvalidOperationException());

        Assert.Equal("failed", first.Result.Status);
        Assert.False(leases.HasOwner(source.Operation.NpcEntityId));
        Assert.True(replay.Replayed);
        Assert.Equal("native_route_unreachable", replay.Result.Code);
    }
}
