using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class OrdinaryInteractionLifecycleTests
{
    private static OperationKey Scope => new("world", "run", 1, "npc:Linus", "interaction:conv", "event");
    private readonly NpcControlLease leases = new();
    private readonly FakeDriver driver = new();
    private long now;
    private OrdinaryInteractionLifecycle Create() => new(this.leases, this.driver, this.driver.Hold, () => this.now);

    [Fact]
    public void ThinkingAndVisibleDialogueRetainControlAcrossTurnCompletion()
    {
        var lifecycle = this.Create();
        Assert.True(lifecycle.Begin("conv", "event", Scope));
        Assert.Empty(lifecycle.Expire(_ => true));
        Assert.True(this.driver.Owns(Scope));
        lifecycle.Presented("event");
        lifecycle.TurnEnded("event");
        this.now = 600_000;
        Assert.Empty(lifecycle.Expire(_ => true));
        Assert.True(this.leases.HasOwner("npc:Linus"));
        lifecycle.End("conv");
        lifecycle.End("conv");
        Assert.Equal(1, this.driver.Restores);
        Assert.False(this.leases.HasOwner("npc:Linus"));
    }

    [Fact]
    public void ReplyKeepsHoldAndOldTurnCannotEndNewTurn()
    {
        var lifecycle = this.Create();
        lifecycle.Begin("conv", "event", Scope);
        lifecycle.Presented("event");
        lifecycle.BindEvent("conv", "reply");
        lifecycle.TurnEnded("event");
        Assert.True(this.driver.Owns(Scope));
        lifecycle.TurnEnded("reply");
        Assert.Equal(1, this.driver.Restores);
    }

    [Fact]
    public void MovementReleasesHoldAndResumesBeforeNextModelStep()
    {
        var lifecycle = this.Create();
        lifecycle.Begin("conv", "event", Scope);
        lifecycle.SuspendForMove("event", "move");
        Assert.False(this.driver.Owns(Scope));
        Assert.False(this.leases.HasOwner("npc:Linus"));
        Assert.Equal(0, this.driver.Restores);
        lifecycle.MoveFinished("move");
        lifecycle.MoveFinished("move");
        Assert.True(this.driver.Owns(Scope));
        Assert.Equal(2, this.driver.Holds);
        lifecycle.Clear();
        Assert.Equal(1, this.driver.Restores);
    }

    [Fact]
    public void UiFailureAfterDisplayReleasesEvenAfterTurnEnded()
    {
        var lifecycle = this.Create();
        lifecycle.Begin("conv", "event", Scope);
        lifecycle.Presented("event");
        lifecycle.TurnEnded("event");
        lifecycle.End("conv");
        lifecycle.TurnEnded("event");
        lifecycle.Clear();
        Assert.Equal(1, this.driver.Restores);
        Assert.False(this.leases.HasOwner("npc:Linus"));
    }

    [Fact]
    public void ControlLossRejectsMovementHandoff()
    {
        var lifecycle = this.Create();
        lifecycle.Begin("conv", "event", Scope);
        this.driver.Owned = false;
        Assert.False(lifecycle.SuspendForMove("event", "move"));
        lifecycle.MoveFinished("move");
        Assert.Equal(1, this.driver.Holds);
        Assert.Equal(0, this.driver.Restores);
    }

    [Fact]
    public void DisconnectDoesNotReacquireFromLateMovementCallback()
    {
        var lifecycle = this.Create();
        lifecycle.Begin("conv", "event", Scope);
        lifecycle.SuspendForMove("event", "move");
        lifecycle.Clear();
        lifecycle.MoveFinished("move");
        Assert.False(this.leases.HasOwner("npc:Linus"));
        Assert.Equal(1, this.driver.Holds);
    }

    [Theory]
    [InlineData("timeout")]
    [InlineData("player_left")]
    [InlineData("control_lost")]
    public void InvalidInteractionEndsOnce(string reason)
    {
        var lifecycle = this.Create();
        lifecycle.Begin("conv", "event", Scope);
        if (reason == "timeout") this.now = 300_000;
        if (reason == "control_lost") this.driver.Owned = false;
        Assert.Single(lifecycle.Expire(_ => reason != "player_left"));
        Assert.Empty(lifecycle.Expire(_ => false));
        Assert.Equal(reason == "control_lost" ? 0 : 1, this.driver.Restores);
        Assert.False(this.leases.HasOwner("npc:Linus"));
    }

    [Fact]
    public void ForeignOwnerAndFailedHoldDoNotLeakLease()
    {
        var lifecycle = this.Create();
        var foreign = this.leases.Acquire(Scope with { TaskId = "task" }, "waiting");
        Assert.False(lifecycle.Begin("conv", "event", Scope));
        Assert.Equal(0, this.driver.Holds);
        this.leases.Release(foreign.Token!, "done");
        this.driver.AllowHold = false;
        Assert.False(lifecycle.Begin("conv", "event", Scope));
        Assert.False(this.leases.HasOwner("npc:Linus"));
    }

    private sealed class FakeDriver : ITaskNpcDriver
    {
        public bool Owned;
        public bool AllowHold = true;
        public int Holds;
        public int Restores;
        public bool Hold(OperationKey operation) { this.Holds++; return this.Owned = this.AllowHold; }
        public bool Owns(OperationKey operation) => this.Owned;
        public void Release(OperationKey operation, string reason, bool restoreNative = true)
        {
            if (this.Owned && restoreNative) this.Restores++;
            this.Owned = false;
        }
        public WorldPosition ReadPosition(string npc) => new("Mountain", 1, 1);
        public DriverResult StartTravel(OperationKey operation, Landmark landmark) => throw new NotSupportedException();
        public DriverResult Poll(OperationKey operation) => throw new NotSupportedException();
        public bool Transfer(OperationKey from, OperationKey to) => throw new NotSupportedException();
    }
}
