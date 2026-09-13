using System;
using System.Collections.Generic;
using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Tasks;

public sealed class TaskExecutionDriver
{
    private readonly TaskSourceContextStore sources;
    private readonly TaskOperationReceipts receipts;
    private readonly NpcControlLease leases;
    private readonly Dictionary<OperationKey, ActiveOperation> active = new();

    public TaskExecutionDriver(TaskSourceContextStore sources, TaskOperationReceipts receipts, NpcControlLease leases)
    {
        this.sources = sources;
        this.receipts = receipts;
        this.leases = leases;
    }

    public TaskExecutionOutcome Begin(
        TaskOperationSource source,
        RuntimeWorldSnapshot world,
        string mode,
        string fingerprint,
        Func<DriverResult> start)
    {
        if (!ScopeMatches(source.Operation, world))
            return Rejected("world_mismatch", "operation scope does not match the active world");
        if (!this.sources.TryRegister(source, out TaskOperationSource? registered, out string sourceError))
            return Rejected(sourceError, "operation task source changed");

        ReceiptLookup lookup = this.receipts.Lookup(source.Operation, fingerprint);
        if (lookup.Status == ReceiptLookupStatus.Replay)
        {
            LeaseToken? lease = this.active.TryGetValue(source.Operation, out ActiveOperation? operation) ? operation.Lease : null;
            return new TaskExecutionOutcome(lookup.Receipt!.Result, lease, true);
        }
        if (lookup.Status == ReceiptLookupStatus.Conflict)
            return Rejected("idempotency_conflict", "operation was already used with different arguments");

        LeaseAttempt attempt = this.leases.Acquire(source.Operation, mode);
        if (!attempt.Acquired)
        {
            DriverResult rejected = new("rejected", attempt.Code, EmptyPosition(), "NPC is controlled by another operation");
            this.receipts.Record(source.Operation, fingerprint, registered!.StartRevision, rejected);
            return new TaskExecutionOutcome(rejected, null, false);
        }

        DriverResult result;
        try
        {
            result = start();
        }
        catch
        {
            this.leases.Release(attempt.Token!, "start_failed");
            throw;
        }

        this.receipts.Record(source.Operation, fingerprint, registered!.StartRevision, result);
        if (result.IsTerminal)
            this.leases.Release(attempt.Token!, result.Code);
        else
            this.active.Add(source.Operation, new ActiveOperation(attempt.Token!, fingerprint));

        return new TaskExecutionOutcome(result, result.IsTerminal ? null : attempt.Token, false);
    }

    public TaskExecutionOutcome Complete(OperationKey operation, DriverResult result)
    {
        if (!this.active.Remove(operation, out ActiveOperation? active))
            return Rejected("operation_not_active", "operation is not active");

        TaskOperationSource source = this.sources.Find(operation) ?? throw new InvalidOperationException("active operation has no task source");
        TaskOperationReceipt receipt = this.receipts.Record(operation, active.Fingerprint, source.StartRevision, result);
        if (receipt.Result.IsTerminal)
            this.leases.Release(active.Lease, receipt.Result.Code);
        else
            this.active.Add(operation, active);
        return new TaskExecutionOutcome(receipt.Result, receipt.Result.IsTerminal ? null : active.Lease, false);
    }

    public bool IsActive(OperationKey operation)
    {
        return this.active.ContainsKey(operation);
    }

    public void Clear()
    {
        foreach (ActiveOperation operation in this.active.Values)
            this.leases.Release(operation.Lease, "world_cleared");
        this.active.Clear();
        this.sources.Clear();
        this.receipts.Clear();
    }

    private static bool ScopeMatches(OperationKey operation, RuntimeWorldSnapshot world)
    {
        return string.Equals(operation.WorldId, world.WorldId, StringComparison.Ordinal) &&
            string.Equals(operation.WorldRunId, world.WorldRunId, StringComparison.Ordinal) &&
            operation.ExecutionGeneration == world.ExecutionGeneration;
    }

    private static TaskExecutionOutcome Rejected(string code, string message)
    {
        return new TaskExecutionOutcome(new DriverResult("rejected", code, EmptyPosition(), message), null, false);
    }

    private static WorldPosition EmptyPosition()
    {
        return new WorldPosition(string.Empty, 0, 0);
    }

    private sealed record ActiveOperation(LeaseToken Lease, string Fingerprint);
}
