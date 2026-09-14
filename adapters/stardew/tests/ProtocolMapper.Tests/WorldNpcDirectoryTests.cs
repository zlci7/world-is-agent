using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class WorldNpcDirectoryTests
{
    [Fact]
    public void BindingContainsOnlyKnownNpcsAndRepeatedInteractionsAreIdempotent()
    {
        var world = new RuntimeWorldContext("stardew-valley", "clock");
        world.BeginWorld("world", 600);
        Assert.True(world.RememberNpc("world", "Linus"));
        Assert.True(world.RememberNpc("world", "Linus"));
        Assert.False(world.RememberNpc("other-world", "Abigail"));
        Assert.False(world.RememberNpc("world", ""));
        var snapshot = world.Current!;
        var binding = ProtocolMapper.BuildWorldBinding(snapshot, world.KnownNpcNames(snapshot));
        Assert.Equal(new[] { "player:local", "npc:Linus" }, binding.Entities.Select(entity => entity.EntityId));
        Assert.Equal("npc:Linus", binding.Entities[1].DefinitionId);
    }

    [Fact]
    public void NewRunAndClearDropKnownNpcsButClockAndRebindingPreserveThem()
    {
        var world = new RuntimeWorldContext("stardew-valley", "clock");
        world.BeginWorld("world", 600);
        world.RememberNpc("world", "Linus");
        var first = world.Current!;
        world.AdvanceClock(610);
        Assert.True(world.TryApplyBinding(first.GameId, first.WorldId, first.WorldRunId, 2, out _));
        Assert.Equal(new[] { "Linus" }, world.KnownNpcNames(world.Current!));
        world.BeginWorld("world", 600);
        Assert.Empty(world.KnownNpcNames(world.Current!));
        Assert.Empty(world.KnownNpcNames(first));
        world.RememberNpc("world", "Abigail");
        var second = world.Current!;
        world.Clear();
        Assert.Empty(world.KnownNpcNames(second));
        Assert.False(world.RememberNpc("world", "Linus"));
    }
}
