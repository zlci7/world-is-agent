using GameAgent.Protocol.V1Alpha2;
using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ProtocolMapperDurableTaskCompatibilityTests
{
    [Fact]
    public void DecodesLegacyAdapterHelloFieldNumbersAndRoundTripsExtensions()
    {
        byte[] legacyPayload = { 0x0A, 0x07, (byte)'a', (byte)'d', (byte)'a', (byte)'p', (byte)'t', (byte)'e', (byte)'r', 0x12, 0x03, (byte)'1', (byte)'.', (byte)'0', 0x1A, 0x07, (byte)'v', (byte)'1', (byte)'a', (byte)'l', (byte)'p', (byte)'h', (byte)'a', 0x22, 0x04, (byte)'g', (byte)'a', (byte)'m', (byte)'e', 0x2A, 0x03, (byte)'1', (byte)'.', (byte)'6', 0x32, 0x07, (byte)'s', (byte)'e', (byte)'s', (byte)'s', (byte)'i', (byte)'o', (byte)'n' };

        AdapterHello legacy = AdapterHello.Parser.ParseFrom(legacyPayload);

        Assert.Equal("adapter", legacy.AdapterId);
        Assert.Equal("session", legacy.SessionId);
        Assert.Empty(legacy.SupportedExtensions);

        legacy.SupportedExtensions.Add("gameagent.tasks.v1");
        AdapterHello roundTripped = AdapterHello.Parser.ParseFrom(legacy.ToByteArray());

        Assert.Equal("gameagent.tasks.v1", Assert.Single(roundTripped.SupportedExtensions));
    }

    [Fact]
    public void RoundTripsDurableTaskNestedValuesWithOptionalWaitUntilAndStartRevision()
    {
        TaskScope scope = new()
        {
            GameId = "stardew-smapi",
            WorldId = "Farm_1",
            WorldRunId = "run_9",
            ExecutionGeneration = 42,
        };
        WorldClock clock = new() { ClockId = "main", NowTick = 1234, Sequence = 99 };
        Struct details = new() { Fields = { ["reason"] = Value.ForString("meeting") } };
        TaskEvidence evidence = new()
        {
            FactId = "fact_1",
            TaskId = "task_1",
            OperationId = "op_1",
            Scope = scope,
            StartRevision = ulong.MaxValue,
            OccurredAt = 1234,
            Outcome = "progress",
            WaitUntil = 1400,
            Details = details,
            GameTime = TestSupport.FixedGameTime(),
            ContextFacts = { new ContextFact { Kind = "task_progress", ScopeId = "task_1", Text = "waiting" } },
        };
        TaskProposal proposal = new()
        {
            Clock = clock,
            WakeAt = 1400,
            DeadlineAt = 2000,
            ParticipantEntityIds = { "npc:Abigail", "player:local" },
            EquivalenceKey = "meet:abigail",
            Payload = details,
        };
        AdapterMessage bindingMessage = new()
        {
            MessageId = "binding_1",
            CorrelationId = "corr_1",
            WorldBinding = new WorldBinding
            {
                Scope = scope,
                Clock = clock,
                Entities = { new EntityRef { EntityId = "npc:Abigail", EntityType = "npc", DefinitionId = "npc:Abigail" } },
                Checkpoint = new TaskCheckpointRef { SchemaVersion = 1, GameId = "stardew-smapi", WorldId = "Farm_1", Status = "confirmed", CheckpointId = "checkpoint_1", Checksum = "sha256", Reason = "load" },
            },
        };
        AdapterMessage bindingRoundTrip = AdapterMessage.Parser.ParseFrom(bindingMessage.ToByteArray());

        Assert.Equal((ulong)42, bindingRoundTrip.WorldBinding.Scope.ExecutionGeneration);
        Assert.Equal((ulong)99, bindingRoundTrip.WorldBinding.Clock.Sequence);
        Assert.Equal("confirmed", bindingRoundTrip.WorldBinding.Checkpoint.Status);

        ActionRequest action = new()
        {
            ActionId = "action_1",
            EntityId = "npc:Abigail",
            Capability = "move_to",
            WorldId = "Farm_1",
            TaskSource = new TaskActionSource
            {
                TaskId = "task_1",
                StartRevision = ulong.MaxValue,
                WakeId = "wake_1",
                OperationId = "op_1",
                Scope = scope,
                TaskContract = proposal,
            },
        };
        ActionResult result = new() { ActionId = "action_1", Status = ActionStatus.Succeeded, TaskProposal = proposal, TaskEvidence = { evidence } };
        GameEvent gameEvent = new() { EventId = "event_1", EventType = "task_progress", TaskEvidence = { evidence }, InteractionSource = new InteractionSource { SourceId = "source_1", Scope = scope, PlayerEntityId = "player:local", TaskId = "task_1", OperationId = "op_1", Kind = "task_arrival" } };
        Observation observation = new() { EntityId = "npc:Abigail", TaskEvidence = { evidence } };

        ActionRequest actionRoundTrip = ActionRequest.Parser.ParseFrom(action.ToByteArray());
        ActionResult resultRoundTrip = ActionResult.Parser.ParseFrom(result.ToByteArray());
        GameEvent eventRoundTrip = GameEvent.Parser.ParseFrom(gameEvent.ToByteArray());
        Observation observationRoundTrip = Observation.Parser.ParseFrom(observation.ToByteArray());

        Assert.Equal(ulong.MaxValue, actionRoundTrip.TaskSource.StartRevision);
        Assert.Equal(ulong.MaxValue, resultRoundTrip.TaskEvidence[0].StartRevision);
        Assert.True(resultRoundTrip.TaskEvidence[0].HasWaitUntil);
        Assert.Equal(1400, resultRoundTrip.TaskEvidence[0].WaitUntil);
        Assert.Equal("task_arrival", eventRoundTrip.InteractionSource.Kind);
        Assert.Equal("fact_1", Assert.Single(observationRoundTrip.TaskEvidence).FactId);
    }

    [Fact]
    public void RoundTripsDurableTaskControlEnvelopeVariants()
    {
        TaskScope scope = new() { GameId = "game", WorldId = "world", WorldRunId = "run", ExecutionGeneration = 7 };
        AdapterMessage[] adapterMessages =
        {
            new() { WorldClock = new WorldClockUpdate { Scope = scope, Clock = new WorldClock { ClockId = "clock", NowTick = 8, Sequence = 9 } } },
            new() { CheckpointPrepare = new CheckpointPrepare { Scope = scope, Clock = new WorldClock { ClockId = "clock" }, SaveRequestId = "save_1" } },
            new() { CheckpointFinish = new CheckpointFinish { Scope = scope, SaveRequestId = "save_1", Saved = true } },
            new() { TaskControlResult = new TaskControlResult { Scope = scope, TaskId = "task_1", OperationId = "op_1", RequestId = "request_1", Status = "released" } },
        };
        RuntimeMessage[] runtimeMessages =
        {
            new() { WorldBindingReady = new WorldBindingReady { Scope = scope, Status = "ready" } },
            new() { CheckpointPrepared = new CheckpointPrepared { Scope = scope, SaveRequestId = "save_1", Checkpoint = new TaskCheckpointRef { Status = "confirmed" } } },
            new() { TaskControl = new TaskControlRequest { Scope = scope, TaskId = "task_1", OperationId = "op_1", RequestId = "request_1", Reason = "player_interaction" } },
        };

        Assert.All(adapterMessages, message => Assert.NotEmpty(message.ToByteArray()));
        Assert.All(runtimeMessages, message => Assert.NotEmpty(message.ToByteArray()));
    }
}
