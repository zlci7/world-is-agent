using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;

namespace GameAgent.Stardew.Tasks;

/// <summary>
/// Pending save handoffs keyed by the request message they were sent with. A reply resolves a
/// caller only when its key, save request and run identity all name the same handoff, so a late or
/// foreign reply leaves the waiting save untouched and a timed-out wait is dropped, not rewritten.
/// </summary>
public sealed class CheckpointReplyInbox
{
    private readonly object gate = new();
    private readonly Dictionary<string, Pending> pending = new(StringComparer.Ordinal);

    private sealed record Pending(
        string SaveRequestId,
        CheckpointScope Scope,
        TaskCompletionSource<CheckpointPreparedReply> Completion
    );

    public Task<CheckpointPreparedReply> Register(string messageId, string saveRequestId, CheckpointScope scope)
    {
        RequireIdentity(messageId, nameof(messageId));
        RequireIdentity(saveRequestId, nameof(saveRequestId));
        ArgumentNullException.ThrowIfNull(scope);
        Pending entry = new(saveRequestId, scope, new TaskCompletionSource<CheckpointPreparedReply>(TaskCreationOptions.RunContinuationsAsynchronously));
        lock (this.gate)
        {
            if (this.pending.ContainsKey(messageId))
                throw new InvalidOperationException("checkpoint request id is already pending");
            this.pending.Add(messageId, entry);
        }
        return entry.Completion.Task;
    }

    public bool TryResolve(string correlationId, string saveRequestId, CheckpointScope scope, CheckpointMarker? reference, string errorCode)
    {
        Pending entry;
        lock (this.gate)
        {
            if (correlationId is null || !this.pending.TryGetValue(correlationId, out entry!))
                return false;
            if (!string.Equals(entry.SaveRequestId, saveRequestId, StringComparison.Ordinal) ||
                scope is null ||
                !entry.Scope.SameRun(scope) ||
                (string.IsNullOrEmpty(errorCode) && (reference is null || !reference.IsConfirmed || scope.ExecutionGeneration < entry.Scope.ExecutionGeneration)))
            {
                return false;
            }
            this.pending.Remove(correlationId);
        }
        entry.Completion.TrySetResult(new CheckpointPreparedReply(scope, saveRequestId, reference, errorCode));
        return true;
    }

    /// <summary>Drops one waiting handoff. The caller's bounded wait already produced its answer.</summary>
    public void Complete(string messageId)
    {
        lock (this.gate)
            this.pending.Remove(messageId ?? string.Empty);
    }

    /// <summary>Answers one waiting handoff whose request never reached the runtime.</summary>
    public bool TryFail(string messageId, string errorCode)
    {
        Pending entry;
        lock (this.gate)
        {
            if (messageId is null || !this.pending.TryGetValue(messageId, out entry!))
                return false;
            this.pending.Remove(messageId);
        }
        return entry.Completion.TrySetResult(new CheckpointPreparedReply(entry.Scope, entry.SaveRequestId, null, errorCode));
    }

    /// <summary>Resolves every waiting handoff, which is what a lost stream means for a save.</summary>
    public void FailAll(string errorCode)
    {
        List<Pending> entries;
        lock (this.gate)
        {
            entries = new List<Pending>(this.pending.Values);
            this.pending.Clear();
        }
        foreach (Pending entry in entries)
            entry.Completion.TrySetResult(new CheckpointPreparedReply(entry.Scope, entry.SaveRequestId, null, errorCode));
    }

    public int Count
    {
        get { lock (this.gate) return this.pending.Count; }
    }

    private static void RequireIdentity(string value, string name)
    {
        if (string.IsNullOrWhiteSpace(value) || value != value.Trim())
            throw new ArgumentException($"{name} must be a canonical non-empty identity", name);
    }
}
