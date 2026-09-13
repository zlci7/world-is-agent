using System;
using System.Collections.Generic;

namespace GameAgent.Stardew.Tasks;

public enum ReceiptLookupStatus
{
    Missing,
    Replay,
    Conflict,
}

public sealed record TaskOperationReceipt(
    OperationKey Operation,
    string Fingerprint,
    ulong StartRevision,
    DriverResult Result
);

public sealed record ReceiptLookup(ReceiptLookupStatus Status, TaskOperationReceipt? Receipt);

public sealed class TaskOperationReceipts
{
    private readonly Dictionary<OperationKey, TaskOperationReceipt> receipts = new();

    public ReceiptLookup Lookup(OperationKey operation, string fingerprint)
    {
        if (!this.receipts.TryGetValue(operation, out TaskOperationReceipt? receipt))
            return new ReceiptLookup(ReceiptLookupStatus.Missing, null);
        return string.Equals(receipt.Fingerprint, fingerprint, StringComparison.Ordinal)
            ? new ReceiptLookup(ReceiptLookupStatus.Replay, receipt)
            : new ReceiptLookup(ReceiptLookupStatus.Conflict, receipt);
    }

    public TaskOperationReceipt Record(OperationKey operation, string fingerprint, ulong startRevision, DriverResult result)
    {
        ArgumentNullException.ThrowIfNull(result);
        if (this.receipts.TryGetValue(operation, out TaskOperationReceipt? current))
        {
            if (!string.Equals(current.Fingerprint, fingerprint, StringComparison.Ordinal) || current.StartRevision != startRevision)
                throw new InvalidOperationException("operation receipt identity changed");
            if (current.Result.IsTerminal)
                return current;

            TaskOperationReceipt updated = current with { Result = result };
            this.receipts[operation] = updated;
            return updated;
        }

        TaskOperationReceipt created = new(operation, fingerprint, startRevision, result);
        this.receipts.Add(operation, created);
        return created;
    }

    public void Clear()
    {
        this.receipts.Clear();
    }
}
