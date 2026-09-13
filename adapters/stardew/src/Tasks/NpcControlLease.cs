using System;
using System.Collections.Generic;

namespace GameAgent.Stardew.Tasks;

public sealed record LeaseToken(string Value, OperationKey Scope, string Mode);

public sealed record LeaseAttempt(bool Acquired, string Code, LeaseToken? Token);

public sealed class NpcControlLease
{
    private readonly Dictionary<string, LeaseToken> owners = new(StringComparer.Ordinal);

    public LeaseAttempt Acquire(OperationKey scope, string mode)
    {
        if (this.owners.TryGetValue(scope.NpcEntityId, out LeaseToken? current))
        {
            if (current.Scope == scope && string.Equals(current.Mode, mode, StringComparison.Ordinal))
                return new LeaseAttempt(true, string.Empty, current);
            return new LeaseAttempt(false, "npc_control_busy", null);
        }

        LeaseToken token = new(Guid.NewGuid().ToString("N"), scope, mode);
        this.owners.Add(scope.NpcEntityId, token);
        return new LeaseAttempt(true, string.Empty, token);
    }

    public LeaseAttempt Transfer(LeaseToken from, OperationKey to, string mode)
    {
        if (!this.IsOwned(from) ||
            !string.Equals(from.Scope.WorldId, to.WorldId, StringComparison.Ordinal) ||
            !string.Equals(from.Scope.WorldRunId, to.WorldRunId, StringComparison.Ordinal) ||
            from.Scope.ExecutionGeneration != to.ExecutionGeneration ||
            !string.Equals(from.Scope.NpcEntityId, to.NpcEntityId, StringComparison.Ordinal) ||
            !string.Equals(from.Scope.TaskId, to.TaskId, StringComparison.Ordinal))
        {
            return new LeaseAttempt(false, "lease_transfer_mismatch", null);
        }

        LeaseToken next = new(Guid.NewGuid().ToString("N"), to, mode);
        this.owners[to.NpcEntityId] = next;
        return new LeaseAttempt(true, string.Empty, next);
    }

    public bool Release(LeaseToken token, string reason)
    {
        if (!this.IsOwned(token))
            return false;
        return this.owners.Remove(token.Scope.NpcEntityId);
    }

    public bool IsOwned(LeaseToken token)
    {
        return this.owners.TryGetValue(token.Scope.NpcEntityId, out LeaseToken? current) && current == token;
    }

    public bool HasOwner(string npcEntityId)
    {
        return this.owners.ContainsKey(npcEntityId);
    }

    public void Clear()
    {
        this.owners.Clear();
    }
}
