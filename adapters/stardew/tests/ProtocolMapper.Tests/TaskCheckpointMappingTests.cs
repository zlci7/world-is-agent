using System.Collections.Generic;
using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskCheckpointMappingTests
{
    private static RuntimeWorldSnapshot World(CheckpointMarkerRead marker)
    {
        RuntimeWorldContext context = new("stardew-valley", "clock");
        context.BeginWorld("Farm_1", 600, marker);
        return context.Current!;
    }

    [Fact]
    public void BindingCarriesTheConfirmedReferenceFromDisk()
    {
        RuntimeWorldSnapshot snapshot = World(CheckpointMarkerRead.Of(
            CheckpointMarker.Confirmed("stardew-valley", "Farm_1", "checkpoint_1", new string('a', 64))
        ));

        WorldBinding binding = ProtocolMapper.BuildWorldBinding(snapshot, new[] { "Linus" });

        Assert.Equal("confirmed", binding.Checkpoint.Status);
        Assert.Equal("checkpoint_1", binding.Checkpoint.CheckpointId);
        Assert.Equal(new string('a', 64), binding.Checkpoint.Checksum);
        Assert.Equal(1u, binding.Checkpoint.SchemaVersion);
        Assert.Equal("", binding.Checkpoint.Reason);
    }

    [Fact]
    public void BindingCarriesAnUnconfirmedMarkerAndItsReason()
    {
        RuntimeWorldSnapshot snapshot = World(CheckpointMarkerRead.Of(
            CheckpointMarker.Unconfirmed("stardew-valley", "Farm_1", "timeout")
        ));

        WorldBinding binding = ProtocolMapper.BuildWorldBinding(snapshot, System.Array.Empty<string>());

        Assert.Equal("unconfirmed", binding.Checkpoint.Status);
        Assert.Equal("timeout", binding.Checkpoint.Reason);
        Assert.Equal(1u, binding.Checkpoint.SchemaVersion);
        Assert.Equal("", binding.Checkpoint.CheckpointId);
        Assert.Equal("", binding.Checkpoint.Checksum);
    }

    [Fact]
    public void BindingKeepsAbsentAndReportsACorruptMarkerAsUnconfirmed()
    {
        RuntimeWorldSnapshot absent = World(CheckpointMarkerRead.Absent);
        RuntimeWorldSnapshot corrupt = World(CheckpointMarkerRead.Invalid("checksum"));

        WorldBinding absentBinding = ProtocolMapper.BuildWorldBinding(absent, System.Array.Empty<string>());
        WorldBinding corruptBinding = ProtocolMapper.BuildWorldBinding(corrupt, System.Array.Empty<string>());

        Assert.Equal("absent", absentBinding.Checkpoint.Status);
        Assert.Equal(0u, absentBinding.Checkpoint.SchemaVersion);
        Assert.Equal("unconfirmed", corruptBinding.Checkpoint.Status);
        Assert.Equal("invalid_checksum", corruptBinding.Checkpoint.Reason);
        Assert.Equal(1u, corruptBinding.Checkpoint.SchemaVersion);
    }

    [Fact]
    public void PrepareAndFinishMessagesCarryTheHandoffIdentity()
    {
        CheckpointScope scope = new("stardew-valley", "Farm_1", "run-a", 2);
        CheckpointPrepareRequest request = new(scope, 1800, 4, "save_1");

        CheckpointPrepare prepare = ProtocolMapper.BuildCheckpointPrepare(request);
        CheckpointFinish finish = ProtocolMapper.BuildCheckpointFinish(new CheckpointFinishRequest(scope, "save_1", Saved: true));

        Assert.Equal("stardew-valley", prepare.Scope.GameId);
        Assert.Equal("Farm_1", prepare.Scope.WorldId);
        Assert.Equal("run-a", prepare.Scope.WorldRunId);
        Assert.Equal(2UL, prepare.Scope.ExecutionGeneration);
        Assert.Equal(GameClock.ClockId, prepare.Clock.ClockId);
        Assert.Equal(1800, prepare.Clock.NowTick);
        Assert.Equal(4UL, prepare.Clock.Sequence);
        Assert.Equal("save_1", prepare.SaveRequestId);
        Assert.Empty(prepare.FinalEvidence);
        Assert.Equal("save_1", finish.SaveRequestId);
        Assert.True(finish.Saved);
    }

    [Fact]
    public void PreparedReplyMapsAConfirmedReferenceAndKeepsRuntimeErrors()
    {
        CheckpointPrepared success = new()
        {
            Scope = new TaskScope { GameId = "stardew-valley", WorldId = "Farm_1", WorldRunId = "run-a", ExecutionGeneration = 3 },
            SaveRequestId = "save_1",
            Checkpoint = new TaskCheckpointRef
            {
                GameId = "stardew-valley",
                WorldId = "Farm_1",
                Status = "confirmed",
                SchemaVersion = 1,
                CheckpointId = "checkpoint_1",
                Checksum = new string('b', 64),
            },
        };
        CheckpointPrepared refused = new()
        {
            Scope = new TaskScope { GameId = "stardew-valley", WorldId = "Farm_1", WorldRunId = "run-a", ExecutionGeneration = 2 },
            SaveRequestId = "save_2",
            Error = new Error { Code = "save_in_progress", Message = "save_in_progress" },
        };

        CheckpointPreparedReply accepted = ProtocolMapper.ReadCheckpointPrepared(success);
        CheckpointPreparedReply rejected = ProtocolMapper.ReadCheckpointPrepared(refused);

        Assert.Equal(string.Empty, accepted.ErrorCode);
        Assert.Equal(3UL, accepted.Scope.ExecutionGeneration);
        Assert.Equal("checkpoint_1", accepted.Reference?.CheckpointId);
        Assert.Equal("save_in_progress", rejected.ErrorCode);
        Assert.Null(rejected.Reference);
    }

    [Fact]
    public void SaveLoadedReadsTheMarkerIntoTheWorldSnapshot()
    {
        RuntimeWorldContext context = new("stardew-valley", "clock");
        Dictionary<string, string> data = CheckpointMarker.Confirmed("stardew-valley", "Farm_1", "checkpoint_1", new string('c', 64)).ToSaveData();

        context.BeginWorld("Farm_1", 600, CheckpointMarker.Read(data));
        RuntimeWorldSnapshot snapshot = context.Current!;

        Assert.Equal(CheckpointMarkerState.Confirmed, snapshot.CheckpointMarker.State);
        Assert.Equal("checkpoint_1", snapshot.CheckpointMarker.Marker?.CheckpointId);
        context.AdvanceClock(700);
        Assert.Equal(CheckpointMarkerState.Confirmed, context.Current!.CheckpointMarker.State);
    }
}
