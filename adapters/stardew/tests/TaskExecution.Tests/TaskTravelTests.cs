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
        Assert.Equal(0, npc.ReleaseCount);
        Assert.False(driver.IsActive(source.Operation));
        Assert.True(driver.HasArrivalHandoff(source.Operation));
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

    [Fact]
    public void WaitAtomicallyTakesTheArrivalHandoffAndReleasesAtTerminalEvidence()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);
        driver.BeginTravel(travel, World, "beach_meeting_spot", catalog);
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));
        driver.Poll(travel.Operation, World);
        TaskOperationSource wait = travel with { Operation = travel.Operation with { OperationId = "wait-a" } };

        TaskExecutionOutcome registered = driver.BeginWait(wait, World, catalog);
        bool released = driver.FinishWait(wait.Operation, "met");

        Assert.Equal("succeeded", registered.Result.Status);
        Assert.Equal("wait_registered", registered.Result.Code);
        Assert.Equal(1, npc.TransferCount);
        Assert.True(released);
        Assert.Equal(1, npc.ReleaseCount);
    }

    [Fact]
    public void TravelAt158SecondsCanRegisterWaitWithoutHoldingAnActiveAction()
    {
        long elapsed = 0;
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => elapsed);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);
        driver.BeginTravel(travel, World, "beach_meeting_spot", catalog);
        elapsed = 158_000;
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));

        Assert.Equal("arrived", driver.Poll(travel.Operation, World).Result.Code);
        TaskOperationSource wait = travel with { Operation = travel.Operation with { OperationId = "wait" } };
        Assert.Equal("wait_registered", driver.BeginWait(wait, World, catalog).Result.Code);
        Assert.False(driver.IsActive(travel.Operation));
        Assert.False(driver.IsActive(wait.Operation));
        Assert.Equal(0, npc.ReleaseCount);
        Assert.True(driver.FinishWait(wait.Operation, "met"));
        Assert.Equal(1, npc.ReleaseCount);
    }

    [Fact]
    public void WaitWithoutAnArrivalHandoffHasNoSideEffects()
    {
        FakeNpcDriver npc = new(new WorldPosition("Beach", 28, 36));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource wait = TaskSourceContextStoreTests.Source() with
        {
            Operation = TaskSourceContextStoreTests.Source().Operation with { OperationId = "wait-a" },
        };

        TaskExecutionOutcome result = driver.BeginWait(wait, World, LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));

        Assert.Equal("rejected", result.Result.Status);
        Assert.Equal("arrival_handoff_missing", result.Result.Code);
        Assert.Equal(0, npc.TransferCount);
    }

    [Fact]
    public void UnclaimedArrivalHandoffExpiresAfterTwoMinutes()
    {
        long elapsed = 0;
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => elapsed);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        driver.BeginTravel(travel, World, "beach_meeting_spot", LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));
        driver.Poll(travel.Operation, World);
        elapsed = TaskExecutionDriver.ArrivalHandoffTimeoutMilliseconds;

        int expired = driver.ExpireArrivalHandoffs();

        Assert.Equal(1, expired);
        Assert.False(driver.HasArrivalHandoff(travel.Operation));
        Assert.Equal(1, npc.ReleaseCount);
    }

    [Fact]
    public void AlreadyArrivedTravelKeepsControlForWaitHandoff()
    {
        FakeNpcDriver npc = new(new WorldPosition("Beach", 28, 36));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);

        TaskExecutionOutcome arrived = driver.BeginTravel(
            travel,
            World with { NowTick = travel.Contract.StartAt },
            "beach_meeting_spot",
            catalog);
        OperationKey waitOperation = travel.Operation with { OperationId = "wait-after-immediate-arrival" };
        TaskExecutionOutcome waiting = driver.BeginWait(
            travel with { Operation = waitOperation },
            World with { NowTick = travel.Contract.StartAt },
            catalog);

        Assert.Equal("arrived", arrived.Result.Code);
        Assert.False(driver.HasArrivalHandoff(travel.Operation));
        Assert.Equal("wait_registered", waiting.Result.Code);
        Assert.Equal(1, npc.HoldCount);
        Assert.Equal(1, npc.TransferCount);
    }

    [Fact]
    public void TaskControlReleasesOwnedStatesButPreservesCommittedInteraction()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);
        driver.BeginTravel(travel, World, "beach_meeting_spot", catalog);

        Assert.Equal("released", driver.ReleaseForTaskControl(travel.Operation.TaskId, travel.Operation.OperationId, "task_terminal").Status);
        Assert.Equal("released", driver.ReleaseForTaskControl(travel.Operation.TaskId, travel.Operation.OperationId, "duplicate").Status);

        TaskOperationSource secondTravel = TaskSourceContextStoreTests.Source("travel-for-interaction");
        npc.Position = new WorldPosition("Mountain", 29, 9);
        driver.BeginTravel(secondTravel, World, "beach_meeting_spot", catalog);
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));
        driver.Poll(secondTravel.Operation, World);
        OperationKey pending = secondTravel.Operation with { OperationId = "pending-interaction" };
        driver.BeginWait(secondTravel with { Operation = pending }, World, catalog);
        Assert.True(driver.PromoteWaitToInteraction(pending));
        Assert.Equal("released", driver.ReleaseForTaskControl(pending.TaskId, pending.OperationId, "before_ack").Status);

        TaskOperationSource thirdTravel = TaskSourceContextStoreTests.Source("travel-for-committed");
        npc.Position = new WorldPosition("Mountain", 29, 9);
        driver.BeginTravel(thirdTravel, World, "beach_meeting_spot", catalog);
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));
        driver.Poll(thirdTravel.Operation, World);
        OperationKey committed = thirdTravel.Operation with { OperationId = "committed-interaction" };
        driver.BeginWait(thirdTravel with { Operation = committed }, World, catalog);
        Assert.True(driver.PromoteWaitToInteraction(committed));
        Assert.True(driver.CommitInteraction(committed));

        Assert.Equal("handed_off", driver.ReleaseForTaskControl(committed.TaskId, committed.OperationId, "task_terminal").Status);
        Assert.True(driver.OwnsInteraction(committed));
    }

    [Fact]
    public void TaskControlDoesNotClaimAConfirmedReleaseAfterControlWasLost()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source("foreign-control");
        driver.BeginTravel(travel, World, "beach_meeting_spot", LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));
        npc.ControlOwned = false;

        TaskControlDecision decision = driver.ReleaseForTaskControl(travel.Operation.TaskId, travel.Operation.OperationId, "task_terminal");

        Assert.Equal("unconfirmed", decision.Status);
        Assert.Equal("control_lost", decision.Code);
        Assert.Equal(1, npc.ReleaseCount);
    }

    [Fact]
    public void AcceptedInteractionCanLendControlToApproachAndTakeItBack()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        TaskExecutionDriver driver = Driver(npc, () => 0);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);
        driver.BeginTravel(travel, World, "beach_meeting_spot", catalog);
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));
        driver.Poll(travel.Operation, World);
        OperationKey wait = travel.Operation with { OperationId = "wait-a" };
        driver.BeginWait(travel with { Operation = wait }, World, catalog);
        Assert.True(driver.PromoteWaitToInteraction(wait));
        Assert.True(driver.CommitInteraction(wait));
        OperationKey approach = wait with { OperationId = "approach-a" };

        Assert.True(driver.BeginInteractionApproach(wait, approach));
        Assert.True(driver.IsHandedOff(wait.TaskId, wait.OperationId));
        Assert.True(driver.FinishInteractionApproach(approach, returnToInteraction: true));
        Assert.True(driver.OwnsInteraction(wait));
        Assert.True(driver.ReleaseInteraction(wait, "turn_completed"));
        Assert.Equal(2, npc.ReleaseCount);
    }

    [Fact]
    public void ApproachAfterForeignTakeoverReleasesLeaseWithoutTakingControlBack()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        NpcControlLease leases = new();
        TaskExecutionDriver driver = new(new TaskSourceContextStore(), new TaskOperationReceipts(), leases, npc);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);
        driver.BeginTravel(travel, World, "beach_meeting_spot", catalog);
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));
        driver.Poll(travel.Operation, World);
        OperationKey wait = travel.Operation with { OperationId = "wait" };
        driver.BeginWait(travel with { Operation = wait }, World, catalog);
        driver.PromoteWaitToInteraction(wait);
        driver.CommitInteraction(wait);
        npc.ControlOwned = false;

        Assert.False(driver.BeginInteractionApproach(wait, wait with { OperationId = "approach" }));
        Assert.False(leases.HasOwner(wait.NpcEntityId));
        Assert.False(driver.IsHandedOff(wait.TaskId, wait.OperationId));
        Assert.False(npc.ControlOwned);
        Assert.Equal(0, npc.HoldCount);
    }

    private static TaskExecutionDriver Driver(FakeNpcDriver npc, Func<long> elapsed) =>
        new(new TaskSourceContextStore(), new TaskOperationReceipts(), new NpcControlLease(), npc, elapsed);

    [Fact]
    public void ExecutionEndedReleasesArrivalSoOrdinaryInteractionCanAcquireControl()
    {
        FakeNpcDriver npc = new(new WorldPosition("Mountain", 29, 9));
        NpcControlLease leases = new();
        TaskExecutionDriver driver = new(new TaskSourceContextStore(), new TaskOperationReceipts(), leases, npc, () => 0);
        TaskOperationSource travel = TaskSourceContextStoreTests.Source();
        driver.BeginTravel(travel, World, "beach_meeting_spot", LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));
        npc.Next = new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36));
        driver.Poll(travel.Operation, World);
        OrdinaryInteractionLifecycle ordinary = new(leases, npc, npc.Hold, () => 0);
        OperationKey click = travel.Operation with { TaskId = "interaction", OperationId = "click" };
        Assert.False(ordinary.Begin("conversation", "event", click));

        Assert.Equal("released", driver.ReleaseForTaskControl(travel.Operation.TaskId, travel.Operation.OperationId, "execution_ended").Status);
        Assert.Equal("released", driver.ReleaseForTaskControl(travel.Operation.TaskId, travel.Operation.OperationId, "execution_ended").Status);
        Assert.False(driver.HasArrivalHandoff(travel.Operation));
        Assert.Equal(1, npc.ReleaseCount);
        Assert.True(ordinary.Begin("conversation", "event", click));
    }

    private sealed class FakeNpcDriver : ITaskNpcDriver
    {
        public FakeNpcDriver(WorldPosition position) => this.Position = position;
        public WorldPosition Position { get; set; }
        public DriverResult Next { get; set; } = new("running", string.Empty, new WorldPosition("Mountain", 29, 9));
        public int StartCount { get; private set; }
        public int PollCount { get; private set; }
        public int ReleaseCount { get; private set; }
        public int TransferCount { get; private set; }
        public int HoldCount { get; private set; }
        public bool ControlOwned { get; set; } = true;

        public WorldPosition ReadPosition(string npcEntityId) => this.Position;
        public DriverResult StartTravel(OperationKey operation, Landmark landmark)
        {
            this.StartCount++;
            if (this.Position == landmark.Position)
                return new DriverResult("succeeded", "arrived", this.Position);
            return new DriverResult("running", string.Empty, this.Position);
        }
        public DriverResult Poll(OperationKey operation)
        {
            this.PollCount++;
            this.Position = this.Next.Position;
            return this.Next;
        }
        public void Release(OperationKey operation, string reason, bool restoreNative = true) => this.ReleaseCount++;
        public bool Transfer(OperationKey from, OperationKey to)
        {
            this.TransferCount++;
            return true;
        }
        public bool Owns(OperationKey operation) => this.ControlOwned;
        public bool Hold(OperationKey operation) { this.HoldCount++; return true; }
    }
}
