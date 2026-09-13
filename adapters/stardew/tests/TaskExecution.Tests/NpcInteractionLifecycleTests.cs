using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class NpcInteractionLifecycleTests
{
    [Fact]
    public void AckControlsWhetherPendingArrivalBecomesInteractive()
    {
        FakeControl control = new();
        NpcInteractionLifecycle lifecycle = new(new TaskInteractionHandoffStore(() => 0), control);
        TaskOperationSource source = TaskSourceContextStoreTests.Source();

        Assert.True(lifecycle.TryBegin("event-a", source, out _));
        Assert.Null(lifecycle.FindCommitted("event-a"));
        Assert.Equal(HandoffAckResult.Accepted, lifecycle.Accept("event-a"));
        Assert.NotNull(lifecycle.FindCommitted("event-a"));
        Assert.Equal(1, control.Promoted);
        Assert.Equal(1, control.Committed);

        lifecycle.Complete("event-a", "turn_completed");
        Assert.Equal(1, control.Released);
    }

    [Fact]
    public void RejectionAndTimeoutReleaseOnlyTheirPendingOwnership()
    {
        long elapsed = 0;
        FakeControl control = new();
        NpcInteractionLifecycle lifecycle = new(new TaskInteractionHandoffStore(() => elapsed), control);
        lifecycle.TryBegin("rejected", TaskSourceContextStoreTests.Source("reject"), out _);
        lifecycle.Reject("rejected", "event_rejected");
        lifecycle.TryBegin("expired", TaskSourceContextStoreTests.Source("expire"), out _);
        elapsed = TaskInteractionHandoffStore.AckTimeoutMilliseconds;

        int expired = lifecycle.ExpirePending();

        Assert.Equal(1, expired);
        Assert.Equal(2, control.Released);
    }

    private sealed class FakeControl : ITaskInteractionControl
    {
        public int Promoted { get; private set; }
        public int Committed { get; private set; }
        public int Released { get; private set; }
        public bool PromoteWaitToInteraction(OperationKey operation) { this.Promoted++; return true; }
        public bool CommitInteraction(OperationKey operation) { this.Committed++; return true; }
        public bool ReleaseInteraction(OperationKey operation, string reason) { this.Released++; return true; }
        public bool BeginInteractionApproach(OperationKey interactionOperation, OperationKey approachOperation) => true;
        public bool FinishInteractionApproach(OperationKey approachOperation, bool returnToInteraction) => true;
        public bool IsHandedOff(string taskId, string operationId) => false;
    }
}
