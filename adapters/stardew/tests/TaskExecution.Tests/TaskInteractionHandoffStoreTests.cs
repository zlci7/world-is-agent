using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskInteractionHandoffStoreTests
{
    [Fact]
    public void SourceBecomesUsableOnlyAfterAcceptedAck()
    {
        long elapsed = 0;
        TaskInteractionHandoffStore store = new(() => elapsed);
        TaskOperationSource source = Source();

        Assert.True(store.TryReserve("event-a", source, out string reason));
        Assert.Null(store.FindCommitted("event-a"));
        Assert.Equal(HandoffAckResult.Accepted, store.Accept("event-a"));
        Assert.Equal(source, store.FindCommitted("event-a")!.Source);
        Assert.Equal(HandoffAckResult.Duplicate, store.Accept("event-a"));
        Assert.Null(store.Reject("event-a"));
        Assert.Equal(string.Empty, reason);
    }

    [Fact]
    public void RejectReturnsPendingOwnershipExactlyOnce()
    {
        TaskInteractionHandoffStore store = new(() => 0);
        TaskOperationSource source = Source();
        store.TryReserve("event-a", source, out _);

        TaskInteractionHandoff? released = store.Reject("event-a");
        TaskInteractionHandoff? duplicate = store.Reject("event-a");

        Assert.Equal(source.Operation, released!.Source.Operation);
        Assert.Null(duplicate);
    }

    [Fact]
    public void PendingAckExpiresButCommittedInteractionDoesNot()
    {
        long elapsed = 0;
        TaskInteractionHandoffStore store = new(() => elapsed);
        store.TryReserve("pending", Source("wait-pending"), out _);
        store.TryReserve("committed", Source("wait-committed"), out _);
        store.Accept("committed");
        elapsed = TaskInteractionHandoffStore.AckTimeoutMilliseconds;

        IReadOnlyList<TaskInteractionHandoff> expired = store.ExpirePending();

        Assert.Equal("pending", Assert.Single(expired).EventId);
        Assert.NotNull(store.FindCommitted("committed"));
    }

    [Fact]
    public void ConflictingEventOrOperationCannotReplacePendingHandoff()
    {
        TaskInteractionHandoffStore store = new(() => 0);
        Assert.True(store.TryReserve("event-a", Source(), out _));

        Assert.False(store.TryReserve("event-a", Source("other"), out string eventReason));
        Assert.False(store.TryReserve("event-b", Source(), out string operationReason));

        Assert.Equal("handoff_event_conflict", eventReason);
        Assert.Equal("handoff_operation_conflict", operationReason);
    }

    private static TaskOperationSource Source(string operationId = "wait-a")
    {
        TaskOperationSource source = TaskSourceContextStoreTests.Source();
        return source with { Operation = source.Operation with { OperationId = operationId } };
    }
}
