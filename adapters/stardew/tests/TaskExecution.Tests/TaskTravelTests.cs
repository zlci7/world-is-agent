using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskTravelTests
{
    private static readonly RuntimeWorldSnapshot World = new("stardew-valley", "Farm_1", "run-a", 1, GameClock.ClockId, 1800, 2);

    [Fact]
    public void StartsVerifiedTravelAndPollsOnlyItsActiveOperation()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource source = TaskSourceContextStoreTests.Source();
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);

        TaskExecutionOutcome started = driver.BeginTravel(source, World, "beach_meeting_spot", catalog);
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));
        TaskExecutionOutcome completed = driver.Poll(source.Operation, World);

        Assert.Equal("running", started.Result.Status);
        Assert.Equal("succeeded", completed.Result.Status);
        Assert.Equal(1, npc.StartCount);
        Assert.Equal(1, npc.PollCount);
        Assert.Equal(1, npc.ReleaseCount);
        Assert.False(driver.IsActive(source.Operation));
    }

    [Fact]
    public void RejectsUnverifiedOriginWithoutStartingNativeRoute()
    {
        FakeNpcDriver npc = new(new WorldPosition("Town", 10, 10));
        TaskExecutionDriver driver = Driver(npc, () => 0);

        TaskExecutionOutcome result = driver.BeginTravel(
            TaskSourceContextStoreTests.Source(),
            World,
            "beach_meeting_spot",
            LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));

        Assert.Equal("rejected", result.Result.Status);
        Assert.Equal("route_not_supported", result.Result.Code);
        Assert.Equal(0, npc.StartCount);
    }

    [Fact]
    public void ActiveTravelRetryReplaysBeforeRevalidatingChangedPosition()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource source = TaskSourceContextStoreTests.Source();
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);
        driver.BeginTravel(source, World, "beach_meeting_spot", catalog);
        npc.Position = new WorldPosition("Town", 5, 5);

        TaskExecutionOutcome replay = driver.BeginTravel(source, World, "beach_meeting_spot", catalog);

        Assert.True(replay.Replayed);
        Assert.Equal("running", replay.Result.Status);
        Assert.Equal(1, npc.StartCount);
    }

    [Fact]
    public void DoesNotInstallANewRouteAtTheArrivalBoundary()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource source = TaskSourceContextStoreTests.Source();

        TaskExecutionOutcome result = driver.BeginTravel(
            source,
            World with { NowTick = source.Contract.StartAt },
            "beach_meeting_spot",
            LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));

        Assert.Equal("rejected", result.Result.Status);
        Assert.Equal("arrival_deadline_missed", result.Result.Code);
        Assert.Equal(0, npc.StartCount);
    }

    [Fact]
    public void SamplesArrivalAtStartBoundaryBeforeDeclaringMissedDeadline()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource source = TaskSourceContextStoreTests.Source();
        driver.BeginTravel(source, World, "beach_meeting_spot", LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));

        TaskExecutionOutcome result = driver.Poll(source.Operation, World with { NowTick = source.Contract.StartAt });

        Assert.Equal("succeeded", result.Result.Status);
        Assert.Equal("arrived", result.Result.Code);
    }

    [Fact]
    public void StopsAtStartBoundaryWhenNpcHasNotArrived()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource source = TaskSourceContextStoreTests.Source();
        driver.BeginTravel(source, World, "beach_meeting_spot", LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));

        TaskExecutionOutcome result = driver.Poll(source.Operation, World with { NowTick = source.Contract.StartAt });

        Assert.Equal("interrupted", result.Result.Status);
        Assert.Equal("arrival_deadline_missed", result.Result.Code);
        Assert.Equal(1, npc.ReleaseCount);
    }

    [Fact]
    public void StopsAfterRealTimeTravelBudgetWithoutRepollingWorldDriver()
    {
        long elapsed = 0;
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => elapsed);
        TaskOperationSource source = TaskSourceContextStoreTests.Source();
        driver.BeginTravel(source, World, "beach_meeting_spot", LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));
        elapsed = TaskExecutionDriver.TravelTimeoutMilliseconds;

        TaskExecutionOutcome result = driver.Poll(source.Operation, World);

        Assert.Equal("interrupted", result.Result.Status);
        Assert.Equal("travel_timeout", result.Result.Code);
        Assert.Equal(0, npc.PollCount);
        Assert.Equal(1, npc.ReleaseCount);
    }

    private static TaskExecutionDriver Driver(FakeNpcDriver npc, Func<long> elapsed) =>
        new(new TaskSourceContextStore(), new TaskOperationReceipts(), new NpcControlLease(), npc, elapsed);

    private sealed class FakeNpcDriver : ITaskNpcDriver
    {
        public FakeNpcDriver(WorldPosition position) => this.Position = position;
        public WorldPosition Position { get; set; }
        public DriverResult Next { get; set; } = new("running", string.Empty, new WorldPosition("Mountain", 29, 9));
        public int StartCount { get; private set; }
        public int PollCount { get; private set; }
        public int ReleaseCount { get; private set; }

        public WorldPosition ReadPosition(string npcEntityId) => this.Position;
        public DriverResult StartTravel(OperationKey operation, Landmark landmark)
        {
            this.StartCount++;
            return new DriverResult("running", string.Empty, this.Position);
        }
        public DriverResult Poll(OperationKey operation)
        {
            this.PollCount++;
            this.Position = this.Next.Position;
            return this.Next;
        }
        public void Release(OperationKey operation, string reason) => this.ReleaseCount++;
    }
}
