using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
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
}
