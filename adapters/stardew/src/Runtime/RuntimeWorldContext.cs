using System;

namespace GameAgent.Stardew.Runtime;

public sealed record RuntimeWorldSnapshot(
    string GameId,
    string WorldId,
    string WorldRunId,
    ulong ExecutionGeneration,
    string ClockId,
    long NowTick,
    ulong ClockSequence
);

public sealed class RuntimeWorldContext
{
    private readonly object gate = new();
    private readonly string gameId;
    private readonly string clockId;
    private readonly Func<string> runIdFactory;
    private readonly HashSet<string> knownNpcNames = new(StringComparer.Ordinal);

    public RuntimeWorldContext(string gameId, string clockId, Func<string>? runIdFactory = null)
    {
        this.gameId = RequireIdentity(gameId, nameof(gameId));
        this.clockId = RequireIdentity(clockId, nameof(clockId));
        this.runIdFactory = runIdFactory ?? (() => Guid.NewGuid().ToString("N"));
    }

    private RuntimeWorldSnapshot? current;

    public RuntimeWorldSnapshot? Current
    {
        get { lock (this.gate) return this.current; }
    }

    public bool RememberNpc(string worldId, string npcName)
    {
        lock (this.gate)
        {
            if (this.current?.WorldId != worldId || string.IsNullOrWhiteSpace(npcName) || npcName != npcName.Trim())
                return false;
            this.knownNpcNames.Add(npcName);
            return true;
        }
    }

    public string[] KnownNpcNames(RuntimeWorldSnapshot snapshot)
    {
        lock (this.gate)
            return this.current?.WorldRunId == snapshot.WorldRunId && this.current.WorldId == snapshot.WorldId
                ? this.knownNpcNames.OrderBy(name => name, StringComparer.Ordinal).ToArray()
                : Array.Empty<string>();
    }

    public void BeginWorld(string worldId, long nowTick)
    {
        string runId = RequireIdentity(this.runIdFactory(), "worldRunId");
        lock (this.gate)
        {
            this.knownNpcNames.Clear();
            this.current = new RuntimeWorldSnapshot(
                this.gameId,
                RequireIdentity(worldId, nameof(worldId)),
                runId,
                0,
                this.clockId,
                RequireTick(nowTick),
                1
            );
        }
    }

    public void AdvanceClock(long nowTick)
    {
        lock (this.gate)
        {
            RuntimeWorldSnapshot snapshot = this.current ?? throw new InvalidOperationException("world context is unavailable");
            long nextTick = RequireTick(nowTick);
            if (nextTick < snapshot.NowTick)
                throw new ArgumentOutOfRangeException(nameof(nowTick), "world clock cannot move backwards within a run");

            this.current = snapshot with
            {
                NowTick = nextTick,
                ClockSequence = checked(snapshot.ClockSequence + 1),
            };
        }
    }

    public bool TryApplyBinding(string gameId, string worldId, string worldRunId, ulong generation, out string errorCode)
    {
        lock (this.gate)
        {
            RuntimeWorldSnapshot? snapshot = this.current;
            if (snapshot is null ||
                !string.Equals(snapshot.GameId, gameId, StringComparison.Ordinal) ||
                !string.Equals(snapshot.WorldId, worldId, StringComparison.Ordinal) ||
                !string.Equals(snapshot.WorldRunId, worldRunId, StringComparison.Ordinal) ||
                generation == 0)
            {
                errorCode = "world_binding_mismatch";
                return false;
            }

            this.current = snapshot with { ExecutionGeneration = generation };
            errorCode = string.Empty;
            return true;
        }
    }

    public void Clear()
    {
        lock (this.gate)
        {
            this.current = null;
            this.knownNpcNames.Clear();
        }
    }

    private static string RequireIdentity(string value, string name)
    {
        if (string.IsNullOrWhiteSpace(value) || !string.Equals(value, value.Trim(), StringComparison.Ordinal))
            throw new ArgumentException($"{name} must be a canonical non-empty identity", name);
        return value;
    }

    private static long RequireTick(long value)
    {
        if (value < 0)
            throw new ArgumentOutOfRangeException(nameof(value), "world clock tick must be non-negative");
        return value;
    }
}
