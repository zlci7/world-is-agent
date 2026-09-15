using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Google.Protobuf.WellKnownTypes;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ProtocolMapperTaskTests
{
    private static readonly RuntimeWorldSnapshot Snapshot = new(
        "stardew-valley", "Farm_1", "run-a", 2, "stardew.game_time.v1", 1234, 9);

    [Fact]
    public void HelloAdvertisesDurableTasks()
    {
        AdapterHello hello = ProtocolMapper.BuildAdapterHello("adapter", "1.0", "v1alpha2", "stardew-valley", "1.6", "session");

        Assert.Equal(RuntimeSessionState.TaskExtension, Assert.Single(hello.SupportedExtensions));
    }

    [Fact]
    public void WorldBindingUsesSnapshotAndDeterministicEntityDirectory()
    {
        WorldBinding binding = ProtocolMapper.BuildWorldBinding(Snapshot, new[] { "Linus", "Abigail", "Linus", " " });

        Assert.Equal("stardew-valley", binding.Scope.GameId);
        Assert.Equal("Farm_1", binding.Scope.WorldId);
        Assert.Equal("run-a", binding.Scope.WorldRunId);
        Assert.Equal((ulong)2, binding.Scope.ExecutionGeneration);
        Assert.Equal(1234, binding.Clock.NowTick);
        Assert.Equal((ulong)9, binding.Clock.Sequence);
        Assert.Equal("absent", binding.Checkpoint.Status);
        Assert.Equal(new[] { "player:local", "npc:Abigail", "npc:Linus" }, binding.Entities.Select(entity => entity.EntityId));
        Assert.All(binding.Entities, entity => Assert.Equal(entity.EntityId, entity.DefinitionId));
    }

    [Fact]
    public void ClockUpdateCarriesTheSameAuthorityScope()
    {
        WorldClockUpdate update = ProtocolMapper.BuildWorldClockUpdate(Snapshot);

        Assert.Equal(Snapshot.WorldRunId, update.Scope.WorldRunId);
        Assert.Equal(Snapshot.ExecutionGeneration, update.Scope.ExecutionGeneration);
        Assert.Equal(Snapshot.NowTick, update.Clock.NowTick);
        Assert.Equal(Snapshot.ClockSequence, update.Clock.Sequence);
    }

    [Fact]
    public void ParsesResolveMeetingArgumentsAndRejectsExtraFields()
    {
        ActionRequest request = ResolveRequest();

        MeetingRequest input = ProtocolMapper.RequireResolveMeetingArgument(request);

        Assert.Equal("beach_meeting_spot", input.LandmarkId);
        Assert.Equal(new MeetingDate(2, "summer", 3), input.TargetDate);
        Assert.Equal(1000, input.StartTime);
        Assert.Equal(1100, input.EndTime);

        request.Arguments.Fields.Add("task_id", Value.ForString("injected"));
        Assert.Throws<ArgumentException>(() => ProtocolMapper.RequireResolveMeetingArgument(request));
    }

    [Fact]
    public void BuildsSuccessfulProposalFromResolvedAgreement()
    {
        MeetingAgreement agreement = new(1, "beach_meeting_spot", "npc:Linus", new MeetingDate(2, "summer", 3), Snapshot.ClockId, 2040, 2040, 2100);
        MeetingResolution resolution = new(true, string.Empty, string.Empty, agreement, "meeting-key");
        ActionRequest request = ResolveRequest();

        ActionResult result = ProtocolMapper.BuildMeetingResolutionResult(request, Snapshot, resolution, "player:local");

        Assert.Equal(ActionStatus.Succeeded, result.Status);
        Assert.Equal(2040, result.TaskProposal.WakeAt);
        Assert.Equal(2100, result.TaskProposal.DeadlineAt);
        Assert.Equal(new[] { "npc:Linus", "player:local" }, result.TaskProposal.ParticipantEntityIds);
        Assert.Equal("beach_meeting_spot", result.TaskProposal.Payload.Fields["landmark_id"].StringValue);
        Assert.Equal(1, result.TaskProposal.Payload.Fields["schema_version"].NumberValue);
    }

    [Fact]
    public void BuildsRejectedResultWithoutAProposal()
    {
        ActionResult result = ProtocolMapper.BuildMeetingResolutionResult(
            ResolveRequest(),
            Snapshot,
            new MeetingResolution(false, "route_not_supported", "route is not verified", null, string.Empty),
            "player:local");

        Assert.Equal(ActionStatus.Rejected, result.Status);
        Assert.Equal("route_not_supported", result.Error.Code);
        Assert.Null(result.TaskProposal);
    }

    [Fact]
    public void MapsTaskActionSourceAndPreservesItsStartRevision()
    {
        ActionRequest request = TaskActionRequest();

        TaskOperationSource source = ProtocolMapper.RequireTaskOperationSource(request, Snapshot);

        Assert.Equal("operation-a", source.Operation.OperationId);
        Assert.Equal((ulong)4, source.StartRevision);
        Assert.Equal("wake-a", source.WakeId);
        Assert.Equal(2040, source.Contract.DepartureAt);
    }

    [Fact]
    public void RejectsTaskActionSourceFromAnotherGeneration()
    {
        ActionRequest request = TaskActionRequest();
        request.TaskSource.Scope.ExecutionGeneration++;

        Assert.Throws<ArgumentException>(() => ProtocolMapper.RequireTaskOperationSource(request, Snapshot));
    }

    [Fact]
    public void MapsMoveToLandmarkArgumentsAndArrivalProgressEvidence()
    {
        ActionRequest request = TaskActionRequest();
        TaskOperationSource source = ProtocolMapper.RequireTaskOperationSource(request, Snapshot);

        string landmarkId = ProtocolMapper.RequireMoveToLandmarkArgument(request);
        ActionResult result = ProtocolMapper.BuildTaskDriverActionResult(
            request,
            source,
            new DriverResult("succeeded", "arrived", new WorldPosition("Beach", 28, 36)),
            Snapshot);

        Assert.Equal("beach_meeting_spot", landmarkId);
        Assert.Equal(ActionStatus.Succeeded, result.Status);
        TaskEvidence evidence = Assert.Single(result.TaskEvidence);
        Assert.Equal("progress", evidence.Outcome);
        Assert.Equal((ulong)4, evidence.StartRevision);
        Assert.Equal("Beach", evidence.Details.Fields["location"].StringValue);
        Assert.False(evidence.Details.Fields.ContainsKey("satisfied"));
    }

    [Fact]
    public void MoveToLandmarkRejectsExtraModelArguments()
    {
        ActionRequest request = TaskActionRequest();
        request.Arguments.Fields.Add("location", Value.ForString("Beach"));

        Assert.Throws<ArgumentException>(() => ProtocolMapper.RequireMoveToLandmarkArgument(request));
    }

    [Fact]
    public void BuildsWaitRegistrationProgressAndTerminalEvidenceEvent()
    {
        ActionRequest request = TaskActionRequest();
        request.Capability = "wait_for_player";
        request.Arguments = new Struct();
        request.TaskSource.OperationId = "wait-a";
        TaskOperationSource source = ProtocolMapper.RequireTaskOperationSource(request, Snapshot);
        DriverResult registered = new("succeeded", "wait_registered", new WorldPosition("Beach", 28, 36));

        ProtocolMapper.RequireWaitForPlayerArgument(request);
        ActionResult action = ProtocolMapper.BuildWaitRegisteredActionResult(request, source, registered, Snapshot);
        WaitEvidence terminal = new(source, source.Contract.StartAt, "satisfied", "met", registered.Position);
        GameEvent gameEvent = ProtocolMapper.BuildWaitEvidenceEvent(terminal, Snapshot, 42);

        TaskEvidence progress = Assert.Single(action.TaskEvidence);
        Assert.Equal("progress", progress.Outcome);
        Assert.Equal(source.Contract.EndAt, progress.WaitUntil);
        TaskEvidence fact = Assert.Single(gameEvent.TaskEvidence);
        Assert.Equal("satisfied", fact.Outcome);
        Assert.Equal((ulong)4, fact.StartRevision);
        Assert.False(fact.HasWaitUntil);
        Assert.Equal("npc:Linus", gameEvent.TargetEntityId);
        Assert.Equal((ulong)42, gameEvent.Sequence);
        Assert.Equal("task_arrival", gameEvent.InteractionSource.Kind);
        Assert.Equal(source.Operation.TaskId, gameEvent.InteractionSource.TaskId);
        Assert.Equal(source.Operation.OperationId, gameEvent.InteractionSource.OperationId);
    }

    [Fact]
    public void ApproachResultKeepsStartTargetAndReportsCurrentAdjacency()
    {
        ActionRequest request = TaskActionRequest();
        request.Capability = "approach_player";
        request.Arguments = new Struct();
        ApproachPlayerStart start = new(
            new WorldPosition("Beach", 30, 36),
            new WorldPosition("Beach", 29, 36),
            new WorldPosition("Beach", 28, 36),
            false);
        ApproachPlayerResult completed = new(
            start.PlayerPositionAtStart,
            start.Target,
            new WorldPosition("Beach", 29, 36),
            PlayerIsAdjacent: false);

        ProtocolMapper.RequireApproachPlayerArgument(request);
        ActionResult result = ProtocolMapper.BuildApproachPlayerSucceededActionResult(request, completed);

        Assert.Equal(ActionStatus.Succeeded, result.Status);
        Assert.Equal(30, result.Output.Fields["player_position_at_start"].StructValue.Fields["x"].NumberValue);
        Assert.Equal(29, result.Output.Fields["target_tile"].StructValue.Fields["x"].NumberValue);
        Assert.False(result.Output.Fields["player_is_adjacent"].BoolValue);
    }

    private static ActionRequest ResolveRequest()
    {
        return new ActionRequest
        {
            ActionId = "action",
            EntityId = "npc:Linus",
            Capability = "resolve_meeting",
            WorldId = Snapshot.WorldId,
            Arguments = new Struct
            {
                Fields =
                {
                    ["landmark_id"] = Value.ForString("beach_meeting_spot"),
                    ["target_date"] = Value.ForStruct(new Struct
                    {
                        Fields =
                        {
                            ["year"] = Value.ForNumber(2),
                            ["season"] = Value.ForString("summer"),
                            ["day_of_month"] = Value.ForNumber(3),
                        },
                    }),
                    ["start_time"] = Value.ForNumber(1000),
                    ["end_time"] = Value.ForNumber(1100),
                },
            },
        };
    }

    private static ActionRequest TaskActionRequest()
    {
        MeetingAgreement agreement = new(1, "beach_meeting_spot", "npc:Linus", new MeetingDate(2, "summer", 3), Snapshot.ClockId, 2040, 2040, 2100);
        ActionRequest resolve = ResolveRequest();
        TaskProposal proposal = ProtocolMapper.BuildMeetingResolutionResult(
            resolve,
            Snapshot,
            new MeetingResolution(true, string.Empty, string.Empty, agreement, "key"),
            "player:local").TaskProposal;
        return new ActionRequest
        {
            ActionId = "action-task",
            EntityId = "npc:Linus",
            Capability = "move_to_landmark",
            WorldId = Snapshot.WorldId,
            Arguments = new Struct { Fields = { ["landmark_id"] = Value.ForString("beach_meeting_spot") } },
            TaskSource = new TaskActionSource
            {
                TaskId = "task-a",
                StartRevision = 4,
                WakeId = "wake-a",
                OperationId = "operation-a",
                Scope = new TaskScope
                {
                    GameId = Snapshot.GameId,
                    WorldId = Snapshot.WorldId,
                    WorldRunId = Snapshot.WorldRunId,
                    ExecutionGeneration = Snapshot.ExecutionGeneration,
                },
                TaskContract = proposal,
            },
        };
    }
}
