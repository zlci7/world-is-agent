using System;
using System.Collections.Generic;
using GameAgent.Protocol.V1Alpha2;

namespace GameAgent.Stardew.Runtime;

/// <summary>
/// Holds durable facts that a save moved to an older generation. Those facts may only travel in an
/// observation reply, so they wait here per entity until the runtime asks for that entity's state.
/// </summary>
public sealed class TaskEvidenceInbox
{
    private readonly object gate = new();
    private readonly Dictionary<string, List<TaskEvidence>> pending = new(StringComparer.Ordinal);

    public void Add(string npcEntityId, TaskEvidence evidence)
    {
        ArgumentNullException.ThrowIfNull(evidence);
        if (string.IsNullOrWhiteSpace(npcEntityId) || npcEntityId != npcEntityId.Trim())
            throw new ArgumentException("npc entity id must be a canonical non-empty identity", nameof(npcEntityId));
        lock (this.gate)
        {
            if (!this.pending.TryGetValue(npcEntityId, out List<TaskEvidence>? facts))
            {
                facts = new List<TaskEvidence>();
                this.pending.Add(npcEntityId, facts);
            }
            facts.Add(evidence);
        }
    }

    public IReadOnlyList<TaskEvidence> Take(string npcEntityId)
    {
        lock (this.gate)
        {
            if (npcEntityId is null || !this.pending.Remove(npcEntityId, out List<TaskEvidence>? facts))
                return Array.Empty<TaskEvidence>();
            return facts;
        }
    }

    public void Clear()
    {
        lock (this.gate)
            this.pending.Clear();
    }

    public int Count
    {
        get { lock (this.gate) return this.pending.Count; }
    }
}
