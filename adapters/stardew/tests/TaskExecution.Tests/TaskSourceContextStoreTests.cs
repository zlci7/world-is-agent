using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskSourceContextStoreTests
{
    [Fact]
    public void PreservesStartRevisionPerOperation()
    {
        TaskSourceContextStore store = new();
        TaskOperationSource first = Source("operation-a", 4);
        TaskOperationSource later = Source("operation-b", 5);

        Assert.True(store.TryRegister(first, out TaskOperationSource? registered, out _));
        Assert.True(store.TryRegister(later, out _, out _));

        Assert.Equal((ulong)4, registered!.StartRevision);
        Assert.Equal((ulong)4, store.Find(first.Operation)!.StartRevision);
        Assert.Equal((ulong)5, store.Find(later.Operation)!.StartRevision);
    }

    [Fact]
    public void RejectsAChangedSourceForTheSameOperation()
    {
        TaskSourceContextStore store = new();
        TaskOperationSource original = Source("operation-a", 4);
        store.TryRegister(original, out _, out _);

        Assert.False(store.TryRegister(original with { StartRevision = 5 }, out _, out string code));
        Assert.Equal("task_source_conflict", code);
        Assert.Equal((ulong)4, store.Find(original.Operation)!.StartRevision);
    }

    internal static TaskOperationSource Source(string operationId = "operation-a", ulong revision = 4)
    {
        OperationKey operation = new("Farm_1", "run-a", 1, "npc:Linus", "task-a", operationId);
        MeetingAgreement agreement = new(1, "beach_meeting_spot", "npc:Linus", new MeetingDate(1, "spring", 2), GameClock.ClockId, 1800, 2040, 2100);
        return new TaskOperationSource(operation, revision, "wake-a", agreement);
    }
}
