using System;

namespace GameAgent.Stardew.Tasks;

public sealed record CheckpointScope(string GameId, string WorldId, string WorldRunId, ulong ExecutionGeneration)
{
    public bool SameRun(CheckpointScope other) =>
        other is not null &&
        string.Equals(this.GameId, other.GameId, StringComparison.Ordinal) &&
        string.Equals(this.WorldId, other.WorldId, StringComparison.Ordinal) &&
        string.Equals(this.WorldRunId, other.WorldRunId, StringComparison.Ordinal);
}

public sealed record CheckpointPrepareRequest(
    CheckpointScope Scope,
    long NowTick,
    ulong ClockSequence,
    string SaveRequestId
);

public sealed record CheckpointPreparedReply(
    CheckpointScope Scope,
    string SaveRequestId,
    CheckpointMarker? Reference,
    string ErrorCode
);

public sealed record CheckpointFinishRequest(CheckpointScope Scope, string SaveRequestId, bool Saved);

/// <summary>The Adapter side of the save handoff: send a request, learn the runtime's answer.</summary>
public interface ICheckpointTransport
{
    Task<CheckpointPreparedReply> PrepareAsync(CheckpointPrepareRequest request, CancellationToken token);
}

public static class CheckpointBridgeOptions
{
    public const int DefaultTimeoutMilliseconds = 5_000;
    public const int MinTimeoutMilliseconds = 1_000;
    public const int MaxTimeoutMilliseconds = 8_000;

    /// <summary>
    /// The handoff wait stays below the runtime's own barrier bound so a slow save cannot hold the
    /// game's save thread while the runtime has already retired its barrier.
    /// </summary>
    public static TimeSpan RequireTimeout(int configuredMilliseconds)
    {
        if (configuredMilliseconds < MinTimeoutMilliseconds || configuredMilliseconds > MaxTimeoutMilliseconds)
        {
            throw new ArgumentOutOfRangeException(
                nameof(configuredMilliseconds),
                configuredMilliseconds,
                $"checkpoint prepare timeout must be between {MinTimeoutMilliseconds} and {MaxTimeoutMilliseconds} milliseconds"
            );
        }
        return TimeSpan.FromMilliseconds(configuredMilliseconds);
    }
}
