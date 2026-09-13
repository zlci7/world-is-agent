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

    public static TaskOperationSource RequireTaskOperationSource(ActionRequest request, RuntimeWorldSnapshot world)
    {
        TaskActionSource source = request.TaskSource ?? throw new ArgumentException("task action source is required");
        TaskScope scope = source.Scope ?? throw new ArgumentException("task action scope is required");
        if (!string.Equals(scope.GameId, world.GameId, StringComparison.Ordinal) ||
            !string.Equals(scope.WorldId, world.WorldId, StringComparison.Ordinal) ||
            !string.Equals(scope.WorldRunId, world.WorldRunId, StringComparison.Ordinal) ||
            scope.ExecutionGeneration != world.ExecutionGeneration ||
            !string.Equals(request.WorldId, world.WorldId, StringComparison.Ordinal))
        {
            throw new ArgumentException("task action scope does not match the active world");
        }
        if (source.StartRevision == 0)
            throw new ArgumentException("task action start_revision must be positive");

        TaskProposal proposal = source.TaskContract ?? throw new ArgumentException("task action contract is required");
        Struct payload = proposal.Payload ?? throw new ArgumentException("task action contract payload is required");
        if (payload.Fields.Count != 8)
            throw new ArgumentException("task action contract payload has an unsupported shape");
        int schemaVersion = RequireIntegerField(payload, "schema_version", "task contract schema_version");
        string landmarkId = RequireStringField(payload, "landmark_id", "task contract landmark_id");
        string participant = RequireStringField(payload, "participant_entity_id", "task contract participant_entity_id");
        string clockId = RequireStringField(payload, "clock_id", "task contract clock_id");
        long departureAt = RequireLongField(payload, "departure_at", "task contract departure_at");
        long startAt = RequireLongField(payload, "start_at", "task contract start_at");
        long endAt = RequireLongField(payload, "end_at", "task contract end_at");
        if (!payload.Fields.TryGetValue("target_date", out Value? dateValue) || dateValue.KindCase != Value.KindOneofCase.StructValue)
            throw new ArgumentException("task contract target_date must be an object");
        Struct date = dateValue.StructValue;
        if (date.Fields.Count != 3)
            throw new ArgumentException("task contract target_date has an unsupported shape");
        MeetingDate targetDate = new(
            RequireIntegerField(date, "year", "task contract target_date.year"),
            RequireStringField(date, "season", "task contract target_date.season"),
            RequireIntegerField(date, "day_of_month", "task contract target_date.day_of_month")
        );

        if (schemaVersion != MeetingContract.SchemaVersion ||
            !string.Equals(participant, request.EntityId, StringComparison.Ordinal) ||
            proposal.Clock is null ||
            !string.Equals(clockId, proposal.Clock.ClockId, StringComparison.Ordinal) ||
            proposal.WakeAt != departureAt ||
            proposal.DeadlineAt != endAt ||
            departureAt >= startAt ||
            startAt >= endAt ||
            !proposal.ParticipantEntityIds.Contains(participant))
        {
            throw new ArgumentException("task action contract is inconsistent");
        }

        OperationKey operation = new(
            scope.WorldId,
            scope.WorldRunId,
            scope.ExecutionGeneration,
            participant,
            RequireNonEmpty(source.TaskId, "task action task_id"),
            RequireNonEmpty(source.OperationId, "task action operation_id")
        );
        MeetingAgreement agreement = new(schemaVersion, landmarkId, participant, targetDate, clockId, departureAt, startAt, endAt);
        return new TaskOperationSource(operation, source.StartRevision, RequireNonEmpty(source.WakeId, "task action wake_id"), agreement);
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

    private static string RequireStringField(Struct structure, string fieldName, string name)
    {
        if (!structure.Fields.TryGetValue(fieldName, out Value? value) || value.KindCase != Value.KindOneofCase.StringValue)
            throw new ArgumentException($"{name} must be a string");
        return RequireNonEmpty(value.StringValue, name);
    }

    private static long RequireLongField(Struct structure, string fieldName, string name)
    {
        if (!structure.Fields.TryGetValue(fieldName, out Value? value) || value.KindCase != Value.KindOneofCase.NumberValue)
            throw new ArgumentException($"{name} must be a number");
        double number = value.NumberValue;
        if (double.IsNaN(number) || double.IsInfinity(number) || Math.Round(number) != number || number < 0 || number > long.MaxValue)
            throw new ArgumentException($"{name} must be a non-negative integer");
        return checked((long)number);
    }
}
