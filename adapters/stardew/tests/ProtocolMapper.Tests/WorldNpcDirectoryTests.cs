using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class WorldNpcDirectoryTests
{
    [Theory]
    [InlineData(false)]
    [InlineData(true)]
    public void BindingRegistersAvailableVillagersAllowedByInteractionConfiguration(bool explicitTargets)
    {
        var world = new RuntimeWorldContext("stardew-valley", "clock");
        world.BeginWorld("world", 600);
        string[] configured = explicitTargets ? new[] { " Linus ", "Missing", "Linus" } : Array.Empty<string>();
        var binding = ProtocolMapper.BuildWorldBinding(world.Current!, configured,
            new[] { "Linus", "Abigail", "Linus" });

        Assert.Equal(explicitTargets ? new[] { "player:local", "npc:Linus" }
            : new[] { "player:local", "npc:Abigail", "npc:Linus" },
            binding.Entities.Select(entity => entity.EntityId));
        Assert.All(binding.Entities.Where(entity => entity.EntityType == "npc"),
            entity => Assert.Equal(entity.EntityId, entity.DefinitionId));
    }

    [Fact]
    public void BindingDoesNotCarryNpcNamesBetweenWorldSnapshots()
    {
        var world = new RuntimeWorldContext("stardew-valley", "clock");
        world.BeginWorld("first", 600);
        var first = ProtocolMapper.BuildWorldBinding(world.Current!, Array.Empty<string>(), new[] { "Linus" });
        world.BeginWorld("second", 600);
        var second = ProtocolMapper.BuildWorldBinding(world.Current!, Array.Empty<string>(), new[] { "Abigail" });
        Assert.Contains(first.Entities, entity => entity.EntityId == "npc:Linus");
        Assert.DoesNotContain(second.Entities, entity => entity.EntityId == "npc:Linus");
        Assert.Contains(second.Entities, entity => entity.EntityId == "npc:Abigail");
        Assert.NotEqual(first.Scope.WorldRunId, second.Scope.WorldRunId);
    }
}
