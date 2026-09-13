using System;
using System.Collections.Generic;
using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Tasks;

public sealed class TaskExecutionDriver : ITaskInteractionControl
{
    public const long TravelTimeoutMilliseconds = 180_000;
    public const long ArrivalHandoffTimeoutMilliseconds = 120_000;

    private readonly TaskSourceContextStore sources;
    private readonly TaskOperationReceipts receipts;
    private readonly NpcControlLease leases;
    private readonly ITaskNpcDriver? npcDriver;
    private readonly Func<long> elapsedMilliseconds;
    private readonly Dictionary<OperationKey, ActiveOperation> active = new();
    private readonly Dictionary<OperationKey, HeldOperation> arrivalHandoffs = new();
    private readonly Dictionary<OperationKey, LeaseToken> waiting = new();
    private readonly Dictionary<OperationKey, InteractionLease> interactions = new();
    private readonly Dictionary<OperationKey, InteractionApproach> interactionApproaches = new();

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
            return this.Complete(
                operation,
                result,
                holdForArrivalHandoff: string.Equals(result.Status, "succeeded", StringComparison.Ordinal) &&
                    string.Equals(result.Code, "arrived", StringComparison.Ordinal));

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

    public TaskExecutionOutcome Complete(OperationKey operation, DriverResult result, bool holdForArrivalHandoff = false)
    {
        if (!this.active.Remove(operation, out ActiveOperation? active))
            return Rejected("operation_not_active", "operation is not active");

        TaskOperationSource source = this.sources.Find(operation) ?? throw new InvalidOperationException("active operation has no task source");
        TaskOperationReceipt receipt = this.receipts.Record(operation, active.Fingerprint, source.StartRevision, result);
        if (receipt.Result.IsTerminal && holdForArrivalHandoff)
        {
            this.arrivalHandoffs.Add(operation, new HeldOperation(active.Lease, this.elapsedMilliseconds()));
        }
        else if (receipt.Result.IsTerminal)
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
        return new TaskExecutionOutcome(receipt.Result, receipt.Result.IsTerminal && !holdForArrivalHandoff ? null : active.Lease, false);
    }

    public TaskExecutionOutcome BeginWait(
        TaskOperationSource source,
        RuntimeWorldSnapshot world,
        LandmarkCatalog catalog)
    {
        ITaskNpcDriver driver = this.npcDriver ?? throw new InvalidOperationException("task NPC driver is not configured");
        const string Fingerprint = "wait_for_player:v1";
        if (!ScopeMatches(source.Operation, world))
            return Rejected("world_mismatch", "operation scope does not match the active world");
        if (!this.sources.TryRegister(source, out _, out string sourceError))
            return Rejected(sourceError, "operation task source changed");
        ReceiptLookup lookup = this.receipts.Lookup(source.Operation, Fingerprint);
        if (lookup.Status == ReceiptLookupStatus.Replay)
        {
            this.waiting.TryGetValue(source.Operation, out LeaseToken? replayLease);
            return new TaskExecutionOutcome(lookup.Receipt!.Result, replayLease, true);
        }
        if (lookup.Status == ReceiptLookupStatus.Conflict)
            return Rejected("idempotency_conflict", "operation was already used with different arguments");
        if (world.NowTick >= source.Contract.EndAt)
            return Rejected("meeting_window_expired", "meeting window has ended");

        Landmark? landmark = catalog.Find(source.Contract.LandmarkId);
        if (landmark is null)
            return Rejected("landmark_not_found", "meeting landmark is not configured");
        WorldPosition current = driver.ReadPosition(source.Operation.NpcEntityId);
        if (current != landmark.Position)
            return Rejected("npc_not_at_landmark", "NPC is not at the task landmark", current);

        KeyValuePair<OperationKey, HeldOperation>? candidate = this.arrivalHandoffs
            .Where(pair => SameTaskActor(pair.Key, source.Operation))
            .Select(pair => (KeyValuePair<OperationKey, HeldOperation>?)pair)
            .SingleOrDefault();
        if (candidate is null)
            return Rejected("arrival_handoff_missing", "no completed task travel is available for handoff", current);

        OperationKey from = candidate.Value.Key;
        HeldOperation held = candidate.Value.Value;
        TaskOperationSource travelSource = this.sources.Find(from)
            ?? throw new InvalidOperationException("arrival handoff has no task source");
        if (travelSource.Contract != source.Contract)
            return Rejected("task_source_conflict", "wait contract does not match the completed travel", current);
        if (!driver.Transfer(from, source.Operation))
            return Rejected("control_lost", "travel controller is no longer owned", current);
        LeaseAttempt transfer = this.leases.Transfer(held.Lease, source.Operation, "waiting");
        if (!transfer.Acquired)
        {
            driver.Transfer(source.Operation, from);
            return Rejected(transfer.Code, "arrival lease could not be transferred", current);
        }

        this.arrivalHandoffs.Remove(from);
        this.waiting.Add(source.Operation, transfer.Token!);
        DriverResult result = new("succeeded", "wait_registered", current);
        this.receipts.Record(source.Operation, Fingerprint, source.StartRevision, result);
        return new TaskExecutionOutcome(result, transfer.Token, false);
    }

    public bool FinishWait(OperationKey operation, string reason)
    {
        if (!this.waiting.Remove(operation, out LeaseToken? lease))
            return false;
        try
        {
            this.npcDriver?.Release(operation, reason);
        }
        finally
        {
            this.leases.Release(lease, reason);
        }
        return true;
    }

    public bool PromoteWaitToInteraction(OperationKey operation)
    {
        if (!this.waiting.Remove(operation, out LeaseToken? waitingLease))
            return false;
        LeaseAttempt transfer = this.leases.Transfer(waitingLease, operation, "interaction");
        if (!transfer.Acquired)
        {
            this.waiting.Add(operation, waitingLease);
            return false;
        }
        this.interactions.Add(operation, new InteractionLease(transfer.Token!, Committed: false));
        return true;
    }

    public bool CommitInteraction(OperationKey operation)
    {
        if (!this.interactions.TryGetValue(operation, out InteractionLease? interaction))
            return false;
        if (interaction.Committed)
            return true;
        this.interactions[operation] = interaction with { Committed = true };
        return true;
    }

    public bool ReleaseInteraction(OperationKey operation, string reason)
    {
        if (!this.interactions.Remove(operation, out InteractionLease? interaction))
        {
            KeyValuePair<OperationKey, InteractionApproach>? borrowed = this.interactionApproaches
                .Where(pair => pair.Value.InteractionOperation == operation)
                .Select(pair => (KeyValuePair<OperationKey, InteractionApproach>?)pair)
                .SingleOrDefault();
            if (borrowed is null)
                return false;
            this.interactionApproaches.Remove(borrowed.Value.Key);
            try
            {
                this.npcDriver?.Release(borrowed.Value.Key, reason);
            }
            finally
            {
                this.leases.Release(borrowed.Value.Value.Lease, reason);
            }
            return true;
        }
        try
        {
            this.npcDriver?.Release(operation, reason);
        }
        finally
        {
            this.leases.Release(interaction.Lease, reason);
        }
        return true;
    }

    public bool BeginInteractionApproach(OperationKey interactionOperation, OperationKey approachOperation)
    {
        if (!this.interactions.TryGetValue(interactionOperation, out InteractionLease? interaction) || !interaction.Committed)
            return false;
        this.npcDriver?.Release(interactionOperation, "approach_start", restoreNative: false);
        LeaseAttempt transfer = this.leases.Transfer(interaction.Lease, approachOperation, "approach");
        if (!transfer.Acquired)
        {
            this.npcDriver?.Hold(interactionOperation);
            return false;
        }

        this.interactions.Remove(interactionOperation);
        this.interactionApproaches.Add(approachOperation, new InteractionApproach(transfer.Token!, interactionOperation));
        return true;
    }

    public bool FinishInteractionApproach(OperationKey approachOperation, bool returnToInteraction)
    {
        if (!this.interactionApproaches.Remove(approachOperation, out InteractionApproach? approach))
            return false;
        if (returnToInteraction)
        {
            LeaseAttempt transfer = this.leases.Transfer(approach.Lease, approach.InteractionOperation, "interaction");
            if (transfer.Acquired && (this.npcDriver?.Hold(approach.InteractionOperation) ?? false))
            {
                this.interactions.Add(approach.InteractionOperation, new InteractionLease(transfer.Token!, Committed: true));
                return true;
            }
            if (transfer.Acquired)
                this.leases.Release(transfer.Token!, "interaction_hold_failed");
        }

        try
        {
            this.npcDriver?.Release(approachOperation, "approach_finished");
        }
        finally
        {
            this.leases.Release(approach.Lease, "approach_finished");
        }
        return !returnToInteraction;
    }

    public bool OwnsInteraction(OperationKey operation) =>
        this.interactions.TryGetValue(operation, out InteractionLease? interaction) &&
        this.leases.IsOwned(interaction.Lease) &&
        (this.npcDriver?.Owns(operation) ?? false);

    public bool IsHandedOff(string taskId, string operationId) =>
        this.interactions.Keys.Any(operation =>
            string.Equals(operation.TaskId, taskId, StringComparison.Ordinal) &&
            string.Equals(operation.OperationId, operationId, StringComparison.Ordinal)) ||
        this.interactionApproaches.Values.Any(approach =>
            string.Equals(approach.InteractionOperation.TaskId, taskId, StringComparison.Ordinal) &&
            string.Equals(approach.InteractionOperation.OperationId, operationId, StringComparison.Ordinal));

    public bool OwnsWait(OperationKey operation) =>
        this.waiting.TryGetValue(operation, out LeaseToken? lease) &&
        this.leases.IsOwned(lease) &&
        (this.npcDriver?.Owns(operation) ?? false);

    public WorldPosition ReadPosition(string npcEntityId) =>
        this.npcDriver?.ReadPosition(npcEntityId) ?? EmptyPosition();

    public bool HasArrivalHandoff(OperationKey operation) => this.arrivalHandoffs.ContainsKey(operation);

    public int ExpireArrivalHandoffs()
    {
        OperationKey[] expired = this.arrivalHandoffs
            .Where(pair => this.elapsedMilliseconds() - pair.Value.HeldAtMilliseconds >= ArrivalHandoffTimeoutMilliseconds)
            .Select(pair => pair.Key)
            .ToArray();
        foreach (OperationKey operation in expired)
        {
            HeldOperation held = this.arrivalHandoffs[operation];
            this.arrivalHandoffs.Remove(operation);
            try
            {
                this.npcDriver?.Release(operation, "arrival_handoff_timeout");
            }
            finally
            {
                this.leases.Release(held.Lease, "arrival_handoff_timeout");
            }
        }
        return expired.Length;
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
        foreach ((OperationKey operation, HeldOperation held) in this.arrivalHandoffs)
        {
            try
            {
                this.npcDriver?.Release(operation, "world_cleared");
            }
            finally
            {
                this.leases.Release(held.Lease, "world_cleared");
            }
        }
        foreach ((OperationKey operation, LeaseToken lease) in this.waiting)
        {
            try
            {
                this.npcDriver?.Release(operation, "world_cleared");
            }
            finally
            {
                this.leases.Release(lease, "world_cleared");
            }
        }
        foreach ((OperationKey operation, InteractionLease interaction) in this.interactions)
        {
            try
            {
                this.npcDriver?.Release(operation, "world_cleared");
            }
            finally
            {
                this.leases.Release(interaction.Lease, "world_cleared");
            }
        }
        foreach ((OperationKey operation, InteractionApproach approach) in this.interactionApproaches)
        {
            try
            {
                this.npcDriver?.Release(operation, "world_cleared");
            }
            finally
            {
                this.leases.Release(approach.Lease, "world_cleared");
            }
        }
        this.active.Clear();
        this.arrivalHandoffs.Clear();
        this.waiting.Clear();
        this.interactions.Clear();
        this.interactionApproaches.Clear();
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

    private static bool SameTaskActor(OperationKey left, OperationKey right) =>
        string.Equals(left.WorldId, right.WorldId, StringComparison.Ordinal) &&
        string.Equals(left.WorldRunId, right.WorldRunId, StringComparison.Ordinal) &&
        left.ExecutionGeneration == right.ExecutionGeneration &&
        string.Equals(left.NpcEntityId, right.NpcEntityId, StringComparison.Ordinal) &&
        string.Equals(left.TaskId, right.TaskId, StringComparison.Ordinal);

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

    private sealed record HeldOperation(LeaseToken Lease, long HeldAtMilliseconds);
    private sealed record InteractionLease(LeaseToken Lease, bool Committed);
    private sealed record InteractionApproach(LeaseToken Lease, OperationKey InteractionOperation);
}
