using GameAgent.Protocol.V1Alpha2;

namespace GameAgent.Stardew.Runtime;

public sealed record InteractionContextSnapshot(
    string EventId,
    string WorldId,
    string NpcEntityId,
    string PlayerEntityId,
    string ConversationId,
    string NpcLocation,
    int NpcTileX,
    int NpcTileY,
    string PlayerLocation,
    int PlayerTileX,
    int PlayerTileY,
    int MaxInteractionDistance
);

public sealed record InteractionContextCurrentState(
    string WorldId,
    string NpcEntityId,
    string PlayerEntityId,
    string ConversationId,
    string NpcLocation,
    int NpcTileX,
    int NpcTileY,
    string PlayerLocation,
    int PlayerTileX,
    int PlayerTileY
);

public sealed class InteractionContextStore
{
    private readonly object gate = new();
    private readonly Dictionary<string, PendingInteractionContext> contexts = new(StringComparer.Ordinal);
    private readonly Dictionary<InteractionKey, string> inFlightByKey = new();

    public void Reserve(InteractionContextSnapshot snapshot)
    {
        this.TryReserve(snapshot, out _);
    }

    public bool TryReserve(InteractionContextSnapshot snapshot, out string reason)
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        string eventId = RequireNonEmpty(snapshot.EventId, nameof(snapshot.EventId));
        InteractionKey key = BuildKey(snapshot.WorldId, snapshot.NpcEntityId, snapshot.PlayerEntityId);
        reason = string.Empty;

        lock (this.gate)
        {
            if (this.contexts.TryGetValue(eventId, out PendingInteractionContext? existing) && existing.Committed)
                return true;

            if (this.inFlightByKey.TryGetValue(key, out string? existingEventId) && !string.Equals(existingEventId, eventId, StringComparison.Ordinal))
            {
                reason = "interaction_in_flight";
                return false;
            }

            if (this.contexts.TryGetValue(eventId, out existing))
                this.RemoveInFlightIfMatches(existing.Snapshot, eventId);

            this.contexts[eventId] = new PendingInteractionContext(snapshot, Committed: false);
            this.inFlightByKey[key] = eventId;
            return true;
        }
    }

    public bool IsInFlight(string worldId, string npcEntityId, string playerEntityId)
    {
        InteractionKey key = BuildKey(worldId, npcEntityId, playerEntityId);

        lock (this.gate)
        {
            return this.inFlightByKey.ContainsKey(key);
        }
    }

    public bool TryReserveHandoff(InteractionContextSnapshot snapshot, out string reason)
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        string eventId = RequireNonEmpty(snapshot.EventId, nameof(snapshot.EventId));
        InteractionKey key = BuildKey(snapshot.WorldId, snapshot.NpcEntityId, snapshot.PlayerEntityId);
        reason = string.Empty;

        lock (this.gate)
        {
            if (!this.inFlightByKey.TryGetValue(key, out string? existingEventId))
            {
                this.contexts[eventId] = new PendingInteractionContext(snapshot, Committed: false);
                this.inFlightByKey[key] = eventId;
                return true;
            }

            if (string.Equals(existingEventId, eventId, StringComparison.Ordinal))
            {
                this.contexts[eventId] = new PendingInteractionContext(snapshot, Committed: false);
                return true;
            }

            if (!this.contexts.TryGetValue(existingEventId, out PendingInteractionContext? existing) ||
                !existing.Committed ||
                !string.Equals(existing.Snapshot.ConversationId, snapshot.ConversationId, StringComparison.Ordinal))
            {
                reason = "interaction_in_flight";
                return false;
            }

            this.contexts.Remove(existingEventId);
            this.contexts[eventId] = new PendingInteractionContext(snapshot, Committed: false);
            this.inFlightByKey[key] = eventId;
            return true;
        }
    }

    public void Commit(string eventId)
    {
        string normalizedEventId = RequireNonEmpty(eventId, nameof(eventId));

        lock (this.gate)
        {
            if (!this.contexts.TryGetValue(normalizedEventId, out PendingInteractionContext? pending))
                return;

            this.contexts[normalizedEventId] = pending with { Committed = true };
        }
    }

    public InteractionContextSnapshot? DiscardPending(string eventId)
    {
        string normalizedEventId = RequireNonEmpty(eventId, nameof(eventId));

        lock (this.gate)
        {
            if (!this.contexts.TryGetValue(normalizedEventId, out PendingInteractionContext? pending) || pending.Committed)
                return null;

            this.contexts.Remove(normalizedEventId);
            this.RemoveInFlightIfMatches(pending.Snapshot, normalizedEventId);
            return pending.Snapshot;
        }
    }

    public void Release(TurnCompletion? completion)
    {
        if (completion?.EventId is null || string.IsNullOrWhiteSpace(completion.EventId))
            return;

        this.Release(completion.EventId);
    }

    public InteractionContextSnapshot? Release(string eventId)
    {
        string normalizedEventId = RequireNonEmpty(eventId, nameof(eventId));

        lock (this.gate)
        {
            if (!this.contexts.Remove(normalizedEventId, out PendingInteractionContext? pending))
                return null;

            this.RemoveInFlightIfMatches(pending.Snapshot, normalizedEventId);
            return pending.Snapshot;
        }
    }

    public InteractionContextSnapshot? TryGet(string eventId)
    {
        string normalizedEventId = RequireNonEmpty(eventId, nameof(eventId));

        lock (this.gate)
        {
            if (!this.contexts.TryGetValue(normalizedEventId, out PendingInteractionContext? pending) || !pending.Committed)
                return null;

            return pending.Snapshot;
        }
    }

    public InteractionContextSnapshot? Find(string eventId)
    {
        string normalizedEventId = RequireNonEmpty(eventId, nameof(eventId));
        lock (this.gate)
            return this.contexts.TryGetValue(normalizedEventId, out PendingInteractionContext? pending)
                ? pending.Snapshot
                : null;
    }

    public bool TryResolve(ActionRequest request, out InteractionContextSnapshot? snapshot, out string errorCode, out string message)
    {
        snapshot = null;
        errorCode = string.Empty;
        message = string.Empty;

        if (string.IsNullOrWhiteSpace(request.SourceEventId))
        {
            errorCode = "interaction_context_missing";
            message = "ActionRequest.source_event_id is required for interaction-bound actions";
            return false;
        }

        snapshot = this.TryGet(request.SourceEventId);
        if (snapshot is null)
        {
            errorCode = "interaction_context_missing";
            message = $"interaction context not found for source_event_id {request.SourceEventId}";
            return false;
        }

        if (!string.Equals(snapshot.WorldId, request.WorldId, StringComparison.Ordinal))
            return Changed("interaction_context_world_changed", "ActionRequest source context does not match requested world", out errorCode, out message);

        if (!string.Equals(snapshot.NpcEntityId, request.EntityId, StringComparison.Ordinal))
            return Changed("interaction_context_entity_changed", "ActionRequest source context does not match requested entity", out errorCode, out message);

        return true;
    }

    public bool TryValidateCurrentState(
        InteractionContextSnapshot snapshot,
        InteractionContextCurrentState current,
        bool requireProximity,
        out string errorCode,
        out string message
    )
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        ArgumentNullException.ThrowIfNull(current);
        errorCode = string.Empty;
        message = string.Empty;

        if (!string.Equals(snapshot.WorldId, current.WorldId, StringComparison.Ordinal))
            return Changed("interaction_context_world_changed", "world changed before action execution", out errorCode, out message);

        if (!string.Equals(snapshot.NpcEntityId, current.NpcEntityId, StringComparison.Ordinal))
            return Changed("interaction_context_entity_changed", "npc entity changed before action execution", out errorCode, out message);

        if (!string.Equals(snapshot.PlayerEntityId, current.PlayerEntityId, StringComparison.Ordinal))
            return Changed("interaction_context_player_changed", "player entity changed before action execution", out errorCode, out message);

        if (!string.Equals(snapshot.ConversationId, current.ConversationId, StringComparison.Ordinal))
            return Changed("interaction_context_conversation_changed", "conversation changed before action execution", out errorCode, out message);

        if (!string.Equals(snapshot.NpcLocation, current.NpcLocation, StringComparison.Ordinal))
            return Changed("interaction_context_npc_location_changed", "npc location changed before action execution", out errorCode, out message);

        if (!string.Equals(snapshot.PlayerLocation, current.PlayerLocation, StringComparison.Ordinal))
            return Changed("interaction_context_player_location_changed", "player location changed before action execution", out errorCode, out message);

        if (requireProximity &&
            InteractionPolicy.ManhattanDistance(current.NpcTileX, current.NpcTileY, current.PlayerTileX, current.PlayerTileY) > snapshot.MaxInteractionDistance)
            return Changed("interaction_context_distance_changed", "player and npc distance changed before action execution", out errorCode, out message);

        return true;
    }

    public void Clear()
    {
        lock (this.gate)
        {
            this.contexts.Clear();
            this.inFlightByKey.Clear();
        }
    }

    private static string RequireNonEmpty(string value, string name)
    {
        if (string.IsNullOrWhiteSpace(value))
            throw new ArgumentException($"{name} must not be empty");

        return value.Trim();
    }

    private static InteractionKey BuildKey(string worldId, string npcEntityId, string playerEntityId)
    {
        return new InteractionKey(
            RequireNonEmpty(worldId, nameof(worldId)),
            RequireNonEmpty(npcEntityId, nameof(npcEntityId)),
            RequireNonEmpty(playerEntityId, nameof(playerEntityId))
        );
    }

    private static bool Changed(string code, string reason, out string errorCode, out string message)
    {
        errorCode = code;
        message = reason;
        return false;
    }

    private void RemoveInFlightIfMatches(InteractionContextSnapshot snapshot, string eventId)
    {
        InteractionKey key = BuildKey(snapshot.WorldId, snapshot.NpcEntityId, snapshot.PlayerEntityId);
        if (this.inFlightByKey.TryGetValue(key, out string? inFlightEventId) && string.Equals(inFlightEventId, eventId, StringComparison.Ordinal))
            this.inFlightByKey.Remove(key);
    }

    private sealed record PendingInteractionContext(InteractionContextSnapshot Snapshot, bool Committed);
    private sealed record InteractionKey(string WorldId, string NpcEntityId, string PlayerEntityId);
}
