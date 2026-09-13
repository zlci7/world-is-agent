using System;
using System.Collections.Generic;
using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Tasks;

public sealed class TaskExecutionDriver
{
    public const long TravelTimeoutMilliseconds = 180_000;

    private readonly TaskSourceContextStore sources;
    private readonly TaskOperationReceipts receipts;
    private readonly NpcControlLease leases;
    private readonly ITaskNpcDriver? npcDriver;
    private readonly Func<long> elapsedMilliseconds;
    private readonly Dictionary<OperationKey, ActiveOperation> active = new();

    public TaskExecutionDriver(
        TaskSourceContextStore sources,
        TaskOperationReceipts receipts,
        NpcControlLease leases,
        ITaskNpcDriver? npcDriver = null,
        Func<long>? elapsedMilliseconds = null)
    {
        this.sources = sources;
        this.receipts = receipts;
        this.leases = leases;
        this.npcDriver = npcDriver;
        this.elapsedMilliseconds = elapsedMilliseconds ?? (() => Environment.TickCount64);
    }

    public TaskExecutionOutcome Begin(
        TaskOperationSource source,
        RuntimeWorldSnapshot world,
        string mode,
        string fingerprint,
        Func<DriverResult> start)
    {
        return this.BeginCore(source, world, mode, fingerprint, start, null, null);
    }

    public TaskExecutionOutcome BeginTravel(
        TaskOperationSource source,
        RuntimeWorldSnapshot world,
        string landmarkId,
        LandmarkCatalog catalog)
    {
        ITaskNpcDriver driver = this.npcDriver ?? throw new InvalidOperationException("task NPC driver is not configured");
        string fingerprint = $"move_to_landmark:v1:{landmarkId}";
        if (!ScopeMatches(source.Operation, world))
            return Rejected("world_mismatch", "operation scope does not match the active world");
        if (!this.sources.TryRegister(source, out _, out string sourceError))
            return Rejected(sourceError, "operation task source changed");
        ReceiptLookup lookup = this.receipts.Lookup(source.Operation, fingerprint);
        if (lookup.Status == ReceiptLookupStatus.Replay)
        {
            LeaseToken? replayLease = this.active.TryGetValue(source.Operation, out ActiveOperation? active) ? active.Lease : null;
            return new TaskExecutionOutcome(lookup.Receipt!.Result, replayLease, true);
        }
        if (lookup.Status == ReceiptLookupStatus.Conflict)
            return Rejected("idempotency_conflict", "operation was already used with different arguments");

        WorldPosition current = driver.ReadPosition(source.Operation.NpcEntityId);
        TravelDecision decision = TaskTravelPolicy.Validate(source, world, landmarkId, current, catalog);
        if (!decision.Accepted)
            return Rejected(decision.Code, decision.Message, current);

        return this.BeginCore(
            source,
            world,
            "travelling",
            fingerprint,
            () => driver.StartTravel(source.Operation, decision.Landmark!),
            () => driver.Poll(source.Operation),
            reason => driver.Release(source.Operation, reason));
    }

    private TaskExecutionOutcome BeginCore(
        TaskOperationSource source,
        RuntimeWorldSnapshot world,
        string mode,
        string fingerprint,
        Func<DriverResult> start,
        Func<DriverResult>? poll,
        Action<string>? release)
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
            this.active.Add(source.Operation, new ActiveOperation(
                attempt.Token!,
                fingerprint,
                poll,
                release,
                this.elapsedMilliseconds()));

        return new TaskExecutionOutcome(result, result.IsTerminal ? null : attempt.Token, false);
    }

    public TaskExecutionOutcome Poll(OperationKey operation, RuntimeWorldSnapshot world)
    {
        if (!this.active.TryGetValue(operation, out ActiveOperation? active) || active.Poll is null)
            return Rejected("operation_not_active", "operation is not active");
        if (!ScopeMatches(operation, world))
            return this.Complete(operation, new DriverResult("interrupted", "world_changed", EmptyPosition()));
        if (this.elapsedMilliseconds() - active.StartedAtMilliseconds >= TravelTimeoutMilliseconds)
            return this.Complete(operation, new DriverResult("interrupted", "travel_timeout", this.npcDriver!.ReadPosition(operation.NpcEntityId)));

        DriverResult result = active.Poll();
        if (result.IsTerminal)
            return this.Complete(operation, result);

        TaskOperationSource source = this.sources.Find(operation) ?? throw new InvalidOperationException("active operation has no task source");
        if (world.NowTick >= source.Contract.StartAt)
            return this.Complete(operation, new DriverResult("interrupted", "arrival_deadline_missed", result.Position));
        return new TaskExecutionOutcome(result, active.Lease, false);
    }

    public TaskExecutionOutcome Cancel(OperationKey operation, string reason)
    {
        if (!this.active.ContainsKey(operation))
            return Rejected("operation_not_active", "operation is not active");
        return this.Complete(operation, new DriverResult("cancelled", "cancelled", this.npcDriver?.ReadPosition(operation.NpcEntityId) ?? EmptyPosition(), reason));
    }

    public TaskExecutionOutcome Complete(OperationKey operation, DriverResult result)
    {
        if (!this.active.Remove(operation, out ActiveOperation? active))
            return Rejected("operation_not_active", "operation is not active");

        TaskOperationSource source = this.sources.Find(operation) ?? throw new InvalidOperationException("active operation has no task source");
        TaskOperationReceipt receipt = this.receipts.Record(operation, active.Fingerprint, source.StartRevision, result);
        if (receipt.Result.IsTerminal)
        {
            try
            {
                active.Release?.Invoke(receipt.Result.Code);
            }
            finally
            {
                this.leases.Release(active.Lease, receipt.Result.Code);
            }
        }
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
        {
            try
            {
                operation.Release?.Invoke("world_cleared");
            }
            finally
            {
                this.leases.Release(operation.Lease, "world_cleared");
            }
        }
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

    private static TaskExecutionOutcome Rejected(string code, string message, WorldPosition? position = null)
    {
        return new TaskExecutionOutcome(new DriverResult("rejected", code, position ?? EmptyPosition(), message), null, false);
    }

    private static WorldPosition EmptyPosition()
    {
        return new WorldPosition(string.Empty, 0, 0);
    }

    private sealed record ActiveOperation(
        LeaseToken Lease,
        string Fingerprint,
        Func<DriverResult>? Poll,
        Action<string>? Release,
        long StartedAtMilliseconds);
}
