using System;
using System.Collections.Generic;
using System.Linq;
using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Capabilities;
using GameAgent.Stardew.Tasks;
using Google.Protobuf.WellKnownTypes;

namespace GameAgent.Stardew.Runtime;

public static partial class ProtocolMapper
{
    public static void AttachPlayerInteractionSource(GameEvent gameEvent, RuntimeWorldSnapshot? readyWorld)
    {
        ArgumentNullException.ThrowIfNull(gameEvent);
        if (gameEvent.InteractionSource is not null)
            throw new ArgumentException("existing interaction source must be preserved");
        if (readyWorld is null)
            return;
        if (gameEvent.EventType is not ("player_interacted_with_npc" or "player_said_to_npc") ||
            string.IsNullOrWhiteSpace(gameEvent.EventId) ||
            !string.Equals(gameEvent.WorldId, readyWorld.WorldId, StringComparison.Ordinal) ||
            readyWorld.ExecutionGeneration == 0)
            throw new ArgumentException("player interaction does not match a ready world");
        EntityRef[] players = gameEvent.Entities.Where(entity => entity.EntityType == "player").ToArray();
        if (players.Length != 1 || string.IsNullOrWhiteSpace(players[0].EntityId))
            throw new ArgumentException("player interaction requires one player entity");
        gameEvent.InteractionSource = new InteractionSource
        {
            Kind = "player",
            SourceId = gameEvent.EventId,
            PlayerEntityId = players[0].EntityId,
            Scope = BuildTaskScope(readyWorld),
        };
    }

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
            Checkpoint = BuildCheckpointReference(snapshot),
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

    /// <summary>
    /// A save proves its task snapshot only through a confirmed reference. Every other on-disk
    /// state travels as an explicit marker so the runtime pauses instead of guessing.
    /// </summary>
    private static TaskCheckpointRef BuildCheckpointReference(RuntimeWorldSnapshot snapshot)
    {
        CheckpointMarkerRead read = snapshot.CheckpointMarker;
        CheckpointMarker? marker = read.Marker;
        if (marker is null)
        {
            return new TaskCheckpointRef
            {
                GameId = snapshot.GameId,
                WorldId = snapshot.WorldId,
                Status = read.State == CheckpointMarkerState.Invalid ? CheckpointMarker.StatusUnconfirmed : CheckpointMarker.StatusAbsent,
                SchemaVersion = read.State == CheckpointMarkerState.Invalid ? (uint)CheckpointMarker.CurrentSchemaVersion : 0u,
                Reason = read.State == CheckpointMarkerState.Invalid ? read.Reason : string.Empty,
            };
        }
        return new TaskCheckpointRef
        {
            GameId = snapshot.GameId,
            WorldId = snapshot.WorldId,
            Status = marker.Status,
            SchemaVersion = (uint)marker.SchemaVersion,
            CheckpointId = marker.CheckpointId ?? string.Empty,
            Checksum = marker.Checksum ?? string.Empty,
            Reason = marker.Reason ?? string.Empty,
        };
    }

    public static CheckpointPrepare BuildCheckpointPrepare(CheckpointPrepareRequest request)
    {
        ArgumentNullException.ThrowIfNull(request);
        return new CheckpointPrepare
        {
            Scope = new TaskScope
            {
                GameId = request.Scope.GameId,
                WorldId = request.Scope.WorldId,
                WorldRunId = request.Scope.WorldRunId,
                ExecutionGeneration = request.Scope.ExecutionGeneration,
            },
            Clock = new WorldClock
            {
                ClockId = GameClock.ClockId,
                NowTick = request.NowTick,
                Sequence = request.ClockSequence,
            },
            SaveRequestId = request.SaveRequestId,
        };
    }

    public static CheckpointFinish BuildCheckpointFinish(CheckpointFinishRequest request)
    {
        ArgumentNullException.ThrowIfNull(request);
        return new CheckpointFinish
        {
            Scope = new TaskScope
            {
                GameId = request.Scope.GameId,
                WorldId = request.Scope.WorldId,
                WorldRunId = request.Scope.WorldRunId,
                ExecutionGeneration = request.Scope.ExecutionGeneration,
            },
            SaveRequestId = request.SaveRequestId,
            Saved = request.Saved,
        };
    }

    public static CheckpointPreparedReply ReadCheckpointPrepared(CheckpointPrepared message)
    {
        ArgumentNullException.ThrowIfNull(message);
        TaskScope scope = message.Scope ?? throw new ArgumentException("checkpoint prepared scope is required");
        CheckpointScope answered = new(scope.GameId, scope.WorldId, scope.WorldRunId, scope.ExecutionGeneration);
        CheckpointMarker? reference = null;
        TaskCheckpointRef? checkpoint = message.Checkpoint;
        if (checkpoint is not null && string.Equals(checkpoint.Status, CheckpointMarker.StatusConfirmed, StringComparison.Ordinal))
        {
            reference = CheckpointMarker.Confirmed(
                checkpoint.GameId,
                checkpoint.WorldId,
                checkpoint.CheckpointId,
                checkpoint.Checksum
            );
        }
        return new CheckpointPreparedReply(answered, message.SaveRequestId, reference, message.Error?.Code ?? string.Empty);
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

    /// <summary>
    /// Build the schedule_mail result. The proposal carries the game-clock window the Adapter
    /// computed; the intent travels in the payload, which the Runtime stores opaquely and
    /// publishes back verbatim in the wake turn's Task Context.
    /// </summary>
    public static ActionResult BuildScheduleMailResult(
        ActionRequest request,
        RuntimeWorldSnapshot snapshot,
        string intent,
        ScheduleMailWindow window,
        string npcEntityId,
        string playerEntityId)
    {
        Struct payload = new()
        {
            Fields =
            {
                ["schema_version"] = Value.ForNumber(ScheduleMailContract.SchemaVersion),
                ["intent"] = Value.ForString(intent),
            },
        };
        TaskProposal proposal = new()
        {
            Clock = BuildWorldClock(snapshot),
            WakeAt = window.WakeAt,
            DeadlineAt = window.DeadlineAt,
            Payload = payload,
        };
        proposal.ParticipantEntityIds.Add(npcEntityId);
        proposal.ParticipantEntityIds.Add(playerEntityId);
        return new ActionResult
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Succeeded,
            Output = new Struct
            {
                Fields =
                {
                    ["delivery"] = Value.ForString("next_morning"),
                    ["wake_at"] = Value.ForNumber(window.WakeAt),
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
            departureAt != startAt ||
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

    public static string RequireMoveToLandmarkArgument(ActionRequest request)
    {
        Struct arguments = request.Arguments ?? throw new ArgumentException("missing required move_to_landmark arguments");
        if (arguments.Fields.Count != 1)
            throw new ArgumentException("move_to_landmark accepts only landmark_id");
        return RequireStringField(arguments, "landmark_id", "move_to_landmark landmark_id");
    }

    public static void RequireWaitForPlayerArgument(ActionRequest request)
    {
        Struct arguments = request.Arguments ?? throw new ArgumentException("missing required wait_for_player arguments");
        if (arguments.Fields.Count != 0)
            throw new ArgumentException("wait_for_player does not accept arguments");
    }

    public static void RequireApproachPlayerArgument(ActionRequest request)
    {
        Struct arguments = request.Arguments ?? throw new ArgumentException("missing required approach_player arguments");
        if (arguments.Fields.Count != 0)
            throw new ArgumentException("approach_player does not accept arguments");
    }

    public static ActionResult BuildApproachPlayerSucceededActionResult(ActionRequest request, ApproachPlayerResult result)
    {
        return new ActionResult
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Succeeded,
            Output = new Struct
            {
                Fields =
                {
                    ["player_position_at_start"] = Value.ForStruct(BuildPosition(result.PlayerPositionAtStart, includeLocation: true)),
                    ["target_tile"] = Value.ForStruct(BuildPosition(result.Target, includeLocation: false)),
                    ["final_position"] = Value.ForStruct(BuildPosition(result.FinalPosition, includeLocation: true)),
                    ["player_is_adjacent"] = Value.ForBool(result.PlayerIsAdjacent),
                },
            },
        };
    }

    public static Struct BuildApproachPlayerStatusMetadata(ApproachPlayerStart start)
    {
        return new Struct
        {
            Fields =
            {
                ["player_position_at_start"] = Value.ForStruct(BuildPosition(start.PlayerPositionAtStart, includeLocation: true)),
                ["target_tile"] = Value.ForStruct(BuildPosition(start.Target, includeLocation: false)),
                ["current_position"] = Value.ForStruct(BuildPosition(start.CurrentPosition, includeLocation: true)),
            },
        };
    }

    public static ActionResult BuildWaitRegisteredActionResult(
        ActionRequest request,
        TaskOperationSource source,
        DriverResult driver,
        RuntimeWorldSnapshot world)
    {
        if (!string.Equals(driver.Status, "succeeded", StringComparison.Ordinal) ||
            !string.Equals(driver.Code, "wait_registered", StringComparison.Ordinal))
        {
            return BuildTaskDriverActionResult(request, source, driver, world);
        }

        TaskEvidence evidence = BuildEvidence(
            source,
            world,
            $"{source.Operation.TaskId}:{source.Operation.OperationId}:wait_registered",
            "progress",
            driver.Code,
            driver.Position,
            source.Contract.EndAt);
        ActionResult result = new()
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Succeeded,
            Output = BuildDriverDetails(driver),
        };
        result.TaskEvidence.Add(evidence);
        return result;
    }

    public static GameEvent BuildWaitEvidenceEvent(WaitEvidence evidence, RuntimeWorldSnapshot world, ulong sequence)
    {
        TaskEvidence fact = BuildWaitEvidenceFact(evidence, world);
        GameEvent result = new()
        {
            EventId = "task_fact:" + fact.FactId,
            EventType = "task_evidence",
            WorldId = world.WorldId,
            TargetEntityId = evidence.Source.Operation.NpcEntityId,
            Sequence = sequence,
        };
        result.Entities.Add(BuildTaskEntity(evidence.Source.Operation.NpcEntityId, "npc", evidence.Source.Operation.NpcEntityId["npc:".Length..]));
        result.Entities.Add(BuildTaskEntity(PlayerEntityId, "player", "Player"));
        result.TaskEvidence.Add(fact);
        if (string.Equals(evidence.Outcome, "satisfied", StringComparison.Ordinal) &&
            string.Equals(evidence.Code, "met", StringComparison.Ordinal))
        {
            result.InteractionSource = new InteractionSource
            {
                SourceId = result.EventId,
                Scope = BuildTaskScope(world),
                PlayerEntityId = PlayerEntityId,
                TaskId = evidence.Source.Operation.TaskId,
                OperationId = evidence.Source.Operation.OperationId,
                Kind = "task_arrival",
            };
        }
        return result;
    }

    /// <summary>
    /// Builds one durable fact for the operation that produced it. A save moves the world to a new
    /// generation while the operation keeps its original binding, so the fact names the operation's
    /// own run and generation; the runtime then decides how that binding is revalidated.
    /// </summary>
    public static TaskEvidence BuildWaitEvidenceFact(WaitEvidence evidence, RuntimeWorldSnapshot world)
    {
        ArgumentNullException.ThrowIfNull(evidence);
        ArgumentNullException.ThrowIfNull(world);
        if (!string.Equals(evidence.Source.Operation.WorldId, world.WorldId, StringComparison.Ordinal) ||
            !string.Equals(evidence.Source.Operation.WorldRunId, world.WorldRunId, StringComparison.Ordinal) ||
            evidence.Source.Operation.ExecutionGeneration > world.ExecutionGeneration)
        {
            throw new ArgumentException("wait evidence scope does not match the active world");
        }

        string factId = $"{evidence.Source.Operation.TaskId}:{evidence.Source.Operation.OperationId}:{evidence.Code}";
        return BuildEvidence(
            evidence.Source,
            world with
            {
                WorldId = evidence.Source.Operation.WorldId,
                WorldRunId = evidence.Source.Operation.WorldRunId,
                ExecutionGeneration = evidence.Source.Operation.ExecutionGeneration,
                NowTick = evidence.OccurredAt,
            },
            factId,
            evidence.Outcome,
            evidence.Code,
            evidence.NpcPosition,
            evidence.WaitUntil);
    }

    public static ActionResult BuildTaskDriverActionResult(
        ActionRequest request,
        TaskOperationSource source,
        DriverResult driver,
        RuntimeWorldSnapshot world)
    {
        ActionStatus status = driver.Status switch
        {
            "succeeded" => ActionStatus.Succeeded,
            "failed" => ActionStatus.Failed,
            "interrupted" => ActionStatus.Interrupted,
            "cancelled" => ActionStatus.Cancelled,
            "rejected" => ActionStatus.Rejected,
            _ => throw new ArgumentException($"unsupported terminal driver status: {driver.Status}"),
        };
        Struct details = BuildDriverDetails(driver);
        ActionResult result = new()
        {
            ActionId = request.ActionId,
            Status = status,
            Output = details,
        };
        if (status != ActionStatus.Succeeded)
        {
            result.Error = new Error
            {
                Code = RequireNonEmpty(driver.Code, "driver result code"),
                Message = string.IsNullOrWhiteSpace(driver.Message) ? driver.Code : driver.Message,
            };
        }
        if (status == ActionStatus.Succeeded && string.Equals(driver.Code, "arrived", StringComparison.Ordinal))
        {
            result.TaskEvidence.Add(BuildEvidence(
                source,
                world,
                $"{source.Operation.TaskId}:{source.Operation.OperationId}:arrived",
                "progress",
                driver.Code,
                driver.Position,
                null));
        }
        return result;
    }

    public static Struct BuildTaskDriverStatusMetadata(DriverResult driver)
    {
        return BuildDriverDetails(driver);
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

    private static Struct BuildDriverDetails(DriverResult driver)
    {
        return new Struct
        {
            Fields =
            {
                ["code"] = Value.ForString(driver.Code ?? string.Empty),
                ["location"] = Value.ForString(driver.Position.Location),
                ["tile"] = Value.ForStruct(new Struct
                {
                    Fields =
                    {
                        ["x"] = Value.ForNumber(driver.Position.X),
                        ["y"] = Value.ForNumber(driver.Position.Y),
                    },
                }),
            },
        };
    }

    private static Struct BuildPosition(WorldPosition position, bool includeLocation)
    {
        Struct result = new()
        {
            Fields =
            {
                ["x"] = Value.ForNumber(position.X),
                ["y"] = Value.ForNumber(position.Y),
            },
        };
        if (includeLocation)
            result.Fields.Add("location", Value.ForString(position.Location));
        return result;
    }

    private static TaskEvidence BuildEvidence(
        TaskOperationSource source,
        RuntimeWorldSnapshot world,
        string factId,
        string outcome,
        string code,
        WorldPosition position,
        long? waitUntil)
    {
        TaskEvidence result = new()
        {
            FactId = factId,
            TaskId = source.Operation.TaskId,
            OperationId = source.Operation.OperationId,
            Scope = BuildTaskScope(world),
            StartRevision = source.StartRevision,
            OccurredAt = world.NowTick,
            Outcome = outcome,
            Details = BuildDriverDetails(new DriverResult("succeeded", code, position)),
        };
        if (waitUntil.HasValue)
            result.WaitUntil = waitUntil.Value;
        return result;
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
