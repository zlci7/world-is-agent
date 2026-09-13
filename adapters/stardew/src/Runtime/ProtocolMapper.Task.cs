using System;
using System.Collections.Generic;
using System.Linq;
using GameAgent.Protocol.V1Alpha2;

namespace GameAgent.Stardew.Runtime;

public static partial class ProtocolMapper
{
    public static AdapterHello BuildAdapterHello(
        string adapterId,
        string adapterVersion,
        string protocolVersion,
        string gameId,
        string gameVersion,
        string sessionId)
    {
        AdapterHello hello = new()
        {
            AdapterId = adapterId,
            AdapterVersion = adapterVersion,
            ProtocolVersion = protocolVersion,
            GameId = gameId,
            GameVersion = gameVersion,
            SessionId = sessionId,
        };
        hello.SupportedExtensions.Add(RuntimeSessionState.TaskExtension);
        return hello;
    }

    public static WorldBinding BuildWorldBinding(RuntimeWorldSnapshot snapshot, IEnumerable<string> npcNames)
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        ArgumentNullException.ThrowIfNull(npcNames);

        WorldBinding binding = new()
        {
            Scope = BuildTaskScope(snapshot),
            Clock = BuildWorldClock(snapshot),
            Checkpoint = new TaskCheckpointRef
            {
                GameId = snapshot.GameId,
                WorldId = snapshot.WorldId,
                Status = "absent",
            },
        };
        binding.Entities.Add(BuildTaskEntity(PlayerEntityId, "player", "Player"));
        foreach (string npcName in npcNames
            .Where(name => !string.IsNullOrWhiteSpace(name))
            .Select(name => name.Trim())
            .Distinct(StringComparer.Ordinal)
            .OrderBy(name => name, StringComparer.Ordinal))
        {
            binding.Entities.Add(BuildTaskEntity(ToNpcEntityId(npcName), "npc", npcName));
        }
        return binding;
    }

    public static WorldClockUpdate BuildWorldClockUpdate(RuntimeWorldSnapshot snapshot)
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        return new WorldClockUpdate
        {
            Scope = BuildTaskScope(snapshot),
            Clock = BuildWorldClock(snapshot),
        };
    }

    private static TaskScope BuildTaskScope(RuntimeWorldSnapshot snapshot)
    {
        return new TaskScope
        {
            GameId = snapshot.GameId,
            WorldId = snapshot.WorldId,
            WorldRunId = snapshot.WorldRunId,
            ExecutionGeneration = snapshot.ExecutionGeneration,
        };
    }

    private static WorldClock BuildWorldClock(RuntimeWorldSnapshot snapshot)
    {
        return new WorldClock
        {
            ClockId = snapshot.ClockId,
            NowTick = snapshot.NowTick,
            Sequence = snapshot.ClockSequence,
        };
    }

    private static EntityRef BuildTaskEntity(string entityId, string entityType, string displayName)
    {
        return new EntityRef
        {
            EntityId = entityId,
            EntityType = entityType,
            DisplayName = displayName,
            DefinitionId = entityId,
        };
    }
}
