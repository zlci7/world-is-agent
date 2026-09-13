using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskOperationReceiptsTests
{
    [Fact]
    public void ReplaysTheSameFingerprintAndRejectsADifferentOne()
    {
        TaskOperationReceipts receipts = new();
        OperationKey operation = TaskSourceContextStoreTests.Source().Operation;
        DriverResult running = new("running", string.Empty, new WorldPosition("Mountain", 29, 9));
        receipts.Record(operation, "move:beach", 4, running);

        ReceiptLookup replay = receipts.Lookup(operation, "move:beach");
        ReceiptLookup conflict = receipts.Lookup(operation, "move:town");

        Assert.Equal(ReceiptLookupStatus.Replay, replay.Status);
        Assert.Equal((ulong)4, replay.Receipt!.StartRevision);
        Assert.Equal(ReceiptLookupStatus.Conflict, conflict.Status);
    }

    [Fact]
    public void TerminalReceiptCannotBeReplacedByLateProgress()
    {
        TaskOperationReceipts receipts = new();
        OperationKey operation = TaskSourceContextStoreTests.Source().Operation;
        receipts.Record(operation, "move:beach", 4, new DriverResult("succeeded", string.Empty, new WorldPosition("Beach", 28, 36)));

        receipts.Record(operation, "move:beach", 4, new DriverResult("running", string.Empty, new WorldPosition("Town", 10, 10)));

        Assert.Equal("succeeded", receipts.Lookup(operation, "move:beach").Receipt!.Result.Status);
    }
}
