using GameAgent.Stardew.Capabilities;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ResolveMeetingCapabilityTests
{
    private static readonly RuntimeWorldSnapshot World = new(
        "stardew-valley", "Farm_1", "run-a", 1, GameClock.ClockId,
        GameClock.ToTick(1, 0, 1, 600), 1);

    private static readonly MeetingRequest TavernMeeting =
        new("tavern_door", new MeetingDate(1, "spring", 2), 1000, 1100);

    [Fact]
    public void ResolvesAgainstTheCatalogThatIsCurrentAtCallTime()
    {
        LandmarkCatalogStore store = new(LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));
        ResolveMeetingCapability capability = new(store);

        MeetingResolution before = capability.Resolve(World, "npc:Linus", "player:local", "Town", TavernMeeting, FestivalKnowledge.NotFestival);
        Assert.False(before.Accepted);
        Assert.Equal("landmark_not_found", before.Code);

        store.Replace(store.Current.WithLandmark(new Landmark(
            "tavern_door", "Tavern door", new WorldPosition("Town", 12, 20), 600, 2200, 10,
            new[] { new SupportedRoute("npc:Linus", "Town") })));

        MeetingResolution after = capability.Resolve(World, "npc:Linus", "player:local", "Town", TavernMeeting, FestivalKnowledge.NotFestival);
        Assert.True(after.Accepted);
        Assert.Equal("tavern_door", after.Agreement!.LandmarkId);
    }
}
