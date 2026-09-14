namespace GameAgent.Stardew.Tasks;

// Main-thread ownership spans turns; visible dialogue ends through its UI callback.
public sealed class OrdinaryInteractionLifecycle
{
    private readonly NpcControlLease leases;
    private readonly ITaskNpcDriver driver;
    private readonly Func<OperationKey, bool> hold;
    private readonly Func<long> now;
    private readonly Dictionary<string, Entry> entries = new();
    private readonly Dictionary<string, string> events = new();
    private readonly Dictionary<string, string> moves = new();

    public OrdinaryInteractionLifecycle(NpcControlLease leases, ITaskNpcDriver driver,
        Func<OperationKey, bool> hold, Func<long> now)
    {
        this.leases = leases;
        this.driver = driver;
        this.hold = hold;
        this.now = now;
    }

    public bool Begin(string conversation, string eventId, OperationKey scope)
    {
        if (!this.entries.ContainsKey(conversation))
        {
            LeaseAttempt attempt = this.leases.Acquire(scope, "interaction");
            if (!attempt.Acquired) return false;
            try
            {
                if (!this.hold(scope))
                {
                    this.leases.Release(attempt.Token!, "hold_failed");
                    return false;
                }
            }
            catch
            {
                this.leases.Release(attempt.Token!, "hold_failed");
                throw;
            }
            this.entries.Add(conversation, new Entry(scope, attempt.Token!));
        }
        this.BindEvent(conversation, eventId);
        return true;
    }

    public void BindEvent(string conversation, string eventId)
    {
        if (!this.entries.TryGetValue(conversation, out Entry? entry)) return;
        this.events[eventId] = conversation;
        entry.EventId = eventId;
        entry.Visible = false;
        entry.Deadline = this.now() + 300_000;
    }

    public void Presented(string eventId)
    {
        if (this.events.TryGetValue(eventId, out string? conversation) &&
            this.entries.TryGetValue(conversation, out Entry? entry) && entry.EventId == eventId)
            entry.Visible = true;
    }

    public void TurnEnded(string eventId)
    {
        if (this.events.TryGetValue(eventId, out string? conversation) &&
            this.entries.TryGetValue(conversation, out Entry? entry) &&
            entry.EventId == eventId && !entry.Visible)
            this.End(conversation);
    }

    public bool SuspendForMove(string eventId, string actionId)
    {
        if (!this.events.TryGetValue(eventId, out string? conversation) ||
            !this.entries.TryGetValue(conversation, out Entry? entry)) return true;
        if (entry.Suspended) return false;
        if (!this.driver.Owns(entry.Scope)) { this.End(conversation); return false; }
        this.driver.Release(entry.Scope, "interaction_move", restoreNative: false);
        this.leases.Release(entry.Token, "interaction_move");
        entry.Suspended = true;
        this.moves[actionId] = conversation;
        return true;
    }

    public void MoveFinished(string actionId)
    {
        if (!this.moves.Remove(actionId, out string? conversation) ||
            !this.entries.TryGetValue(conversation, out Entry? entry)) return;
        LeaseAttempt attempt = this.leases.Acquire(entry.Scope, "interaction");
        if (!attempt.Acquired) { this.End(conversation); return; }
        entry.Token = attempt.Token!;
        try
        {
            if (!this.hold(entry.Scope)) { this.End(conversation); return; }
            entry.Suspended = false;
        }
        catch { this.End(conversation); throw; }
    }

    public void End(string conversation)
    {
        if (!this.entries.Remove(conversation, out Entry? entry)) return;
        if (!entry.Suspended) this.driver.Release(entry.Scope, "interaction_finished");
        this.leases.Release(entry.Token, "interaction_finished");
        foreach (string id in this.events.Where(pair => pair.Value == conversation).Select(pair => pair.Key).ToArray())
            this.events.Remove(id);
        foreach (string id in this.moves.Where(pair => pair.Value == conversation).Select(pair => pair.Key).ToArray())
            this.moves.Remove(id);
    }

    public IReadOnlyList<(string Conversation, string Npc)> Expire(Func<string, bool> isPlayerPresent)
    {
        var ended = new List<(string Conversation, string Npc)>();
        foreach (var pair in this.entries.ToArray())
        {
            Entry entry = pair.Value;
            if (entry.Suspended) continue;
            if (!this.driver.Owns(entry.Scope) || !isPlayerPresent(entry.Scope.NpcEntityId) ||
                (!entry.Visible && this.now() >= entry.Deadline))
            {
                ended.Add((pair.Key, entry.Scope.NpcEntityId));
                this.End(pair.Key);
            }
        }
        return ended;
    }

    public void Clear()
    {
        foreach (string conversation in this.entries.Keys.ToArray()) this.End(conversation);
    }

    private sealed class Entry
    {
        public Entry(OperationKey scope, LeaseToken token) { this.Scope = scope; this.Token = token; }
        public OperationKey Scope { get; }
        public LeaseToken Token { get; set; }
        public string EventId { get; set; } = string.Empty;
        public bool Visible { get; set; }
        public bool Suspended { get; set; }
        public long Deadline { get; set; }
    }
}
