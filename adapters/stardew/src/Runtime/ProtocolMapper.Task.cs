using System;
using System.Collections.Generic;
using System.Linq;
using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Tasks;
using Google.Protobuf.WellKnownTypes;

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

    public static MeetingRequest RequireResolveMeetingArgument(ActionRequest request)
    {
        Struct arguments = request.Arguments ?? throw new ArgumentException("missing required resolve_meeting arguments");
        if (arguments.Fields.Count != 4)
            throw new ArgumentException("resolve_meeting accepts only landmark_id, target_date, start_time, and end_time");
        if (!arguments.Fields.TryGetValue("landmark_id", out Value? landmarkValue) || landmarkValue.KindCase != Value.KindOneofCase.StringValue)
            throw new ArgumentException("resolve_meeting landmark_id must be a string");
        if (!arguments.Fields.TryGetValue("target_date", out Value? dateValue) || dateValue.KindCase != Value.KindOneofCase.StructValue)
            throw new ArgumentException("resolve_meeting target_date must be an object");

        Struct date = dateValue.StructValue;
        if (date.Fields.Count != 3)
            throw new ArgumentException("resolve_meeting target_date accepts only year, season, and day_of_month");
        int year = RequireIntegerField(date, "year", "resolve_meeting target_date.year");
        int day = RequireIntegerField(date, "day_of_month", "resolve_meeting target_date.day_of_month");
        if (!date.Fields.TryGetValue("season", out Value? seasonValue) || seasonValue.KindCase != Value.KindOneofCase.StringValue)
            throw new ArgumentException("resolve_meeting target_date.season must be a string");
        int start = RequireIntegerField(arguments, "start_time", "resolve_meeting start_time");
        int end = RequireIntegerField(arguments, "end_time", "resolve_meeting end_time");
        return new MeetingRequest(
            RequireNonEmpty(landmarkValue.StringValue, "resolve_meeting landmark_id"),
            new MeetingDate(year, RequireNonEmpty(seasonValue.StringValue, "resolve_meeting target_date.season"), day),
            start,
            end
        );
    }

    public static ActionResult BuildMeetingResolutionResult(
        ActionRequest request,
        RuntimeWorldSnapshot snapshot,
        MeetingResolution resolution,
        string playerEntityId)
    {
        if (!resolution.Accepted)
            return BuildRejectedActionResult(request, resolution.Code, resolution.Message);

        MeetingAgreement agreement = resolution.Agreement ?? throw new InvalidOperationException("accepted meeting resolution is missing agreement");
        Struct date = new()
        {
            Fields =
            {
                ["year"] = Value.ForNumber(agreement.TargetDate.Year),
                ["season"] = Value.ForString(agreement.TargetDate.Season),
                ["day_of_month"] = Value.ForNumber(agreement.TargetDate.DayOfMonth),
            },
        };
        Struct payload = new()
        {
            Fields =
            {
                ["schema_version"] = Value.ForNumber(agreement.SchemaVersion),
                ["landmark_id"] = Value.ForString(agreement.LandmarkId),
                ["participant_entity_id"] = Value.ForString(agreement.ParticipantEntityId),
                ["target_date"] = Value.ForStruct(date),
                ["clock_id"] = Value.ForString(agreement.ClockId),
                ["departure_at"] = Value.ForNumber(agreement.DepartureAt),
                ["start_at"] = Value.ForNumber(agreement.StartAt),
                ["end_at"] = Value.ForNumber(agreement.EndAt),
            },
        };
        TaskProposal proposal = new()
        {
            Clock = BuildWorldClock(snapshot),
            WakeAt = agreement.DepartureAt,
            DeadlineAt = agreement.EndAt,
            EquivalenceKey = resolution.EquivalenceKey,
            Payload = payload,
        };
        proposal.ParticipantEntityIds.Add(agreement.ParticipantEntityId);
        proposal.ParticipantEntityIds.Add(playerEntityId);
        return new ActionResult
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Succeeded,
            Output = new Struct
            {
                Fields =
                {
                    ["landmark_id"] = Value.ForString(agreement.LandmarkId),
                    ["departure_at"] = Value.ForNumber(agreement.DepartureAt),
                    ["start_at"] = Value.ForNumber(agreement.StartAt),
                    ["end_at"] = Value.ForNumber(agreement.EndAt),
                },
            },
            TaskProposal = proposal,
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
