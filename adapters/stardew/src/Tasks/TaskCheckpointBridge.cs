using System;
using System.Threading;
using System.Threading.Tasks;

namespace GameAgent.Stardew.Tasks;

/// <summary>
/// Bounded save handoff. The game blocks inside <see cref="PrepareForSaving"/> only for the
/// configured handoff wait, and the marker it returns is written into the save: a confirmed
/// reference when the runtime committed a snapshot, an unconfirmed marker for every other
/// outcome. A handoff that produced a reference stays available until the save ends so the caller
/// can send exactly one Finish, and a lost reply never rewrites an answered handoff.
/// </summary>
public sealed class TaskCheckpointBridge
{
    private readonly ICheckpointTransport transport;
    private readonly Action<string> log;
    private readonly object gate = new();
    private CheckpointFinishRequest? pendingFinish;

    public TaskCheckpointBridge(ICheckpointTransport transport, Action<string>? log = null)
    {
        this.transport = transport ?? throw new ArgumentNullException(nameof(transport));
        this.log = log ?? (_ => { });
    }

    public CheckpointMarker PrepareForSaving(CheckpointPrepareRequest request, TimeSpan timeout)
    {
        ArgumentNullException.ThrowIfNull(request);
        lock (this.gate)
            this.pendingFinish = null;
        try
        {
            Task<CheckpointPreparedReply> waiting = this.transport.PrepareAsync(request, CancellationToken.None);
            CheckpointPreparedReply reply = waiting.WaitAsync(timeout).GetAwaiter().GetResult();
            if (!string.IsNullOrEmpty(reply.ErrorCode))
                return this.Unconfirmed(request, reply.ErrorCode);
            if (reply.Reference is null || !reply.Reference.IsConfirmed)
                return this.Unconfirmed(request, "checkpoint_missing");
            if (reply.Reference.GameId != request.Scope.GameId || reply.Reference.WorldId != request.Scope.WorldId)
                return this.Unconfirmed(request, "checkpoint_mismatch");

            lock (this.gate)
                this.pendingFinish = new CheckpointFinishRequest(reply.Scope, request.SaveRequestId, Saved: false);
            return CheckpointMarker.Confirmed(
                request.Scope.GameId,
                request.Scope.WorldId,
                reply.Reference.CheckpointId!,
                reply.Reference.Checksum!
            );
        }
        catch (TimeoutException)
        {
            this.log($"task checkpoint prepare for save_request_id={request.SaveRequestId} timed out");
            return this.Unconfirmed(request, "timeout");
        }
        catch (Exception ex)
        {
            this.log($"task checkpoint prepare for save_request_id={request.SaveRequestId} failed: {ex.Message}");
            return this.Unconfirmed(request, "prepare_failed");
        }
    }

    public CheckpointFinishRequest? OnSaved() => this.TakePendingFinish(saved: true);

    /// <summary>True while a handoff holds a runtime reference the caller has not finished yet.</summary>
    public bool HasPendingFinish
    {
        get { lock (this.gate) return this.pendingFinish is not null; }
    }

    public CheckpointFinishRequest? OnSaveAborted(string reason)
    {
        this.log($"task checkpoint save aborted: {reason}");
        return this.TakePendingFinish(saved: false);
    }

    /// <summary>A lost stream leaves no finish to send; the runtime retires its own barrier.</summary>
    public void OnDisconnected(string reason)
    {
        lock (this.gate)
            this.pendingFinish = null;
        this.log($"task checkpoint handoff dropped: {reason}");
    }

    private CheckpointFinishRequest? TakePendingFinish(bool saved)
    {
        lock (this.gate)
        {
            CheckpointFinishRequest? pending = this.pendingFinish;
            this.pendingFinish = null;
            return pending is null ? null : pending with { Saved = saved };
        }
    }

    private CheckpointMarker Unconfirmed(CheckpointPrepareRequest request, string reason)
    {
        lock (this.gate)
            this.pendingFinish = null;
        return CheckpointMarker.Unconfirmed(request.Scope.GameId, request.Scope.WorldId, reason);
    }
}
