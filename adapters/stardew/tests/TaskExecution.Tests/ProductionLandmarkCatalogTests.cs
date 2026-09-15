using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ProductionLandmarkCatalogTests
{
    [Fact]
    public void KeepsTheLiveEvidenceRouteForTheBeachMeeting()
    {
        LandmarkCatalog catalog = Load();

        Landmark beach = Assert.IsType<Landmark>(catalog.Find("beach_meeting_spot"));
        Assert.Equal(new WorldPosition("Beach", 28, 36), beach.Position);
        Assert.Equal(240, beach.DepartureLeadMinutes);
        Assert.Equal(new SupportedRoute("npc:Linus", "Mountain"), Assert.Single(beach.SupportedRoutes));
        Assert.False(catalog.SupportsRoute("npc:Abigail", "Mountain", "beach_meeting_spot"));
    }

    [Fact]
    public void ContainsTheMarkedCommunityCenterPointForAnyNpc()
    {
        LandmarkCatalog catalog = Load();

        Landmark point = Assert.IsType<Landmark>(catalog.Find("community_center"));
        Assert.Equal(new WorldPosition("Town", 55, 22), point.Position);
        Assert.Equal(90, point.DepartureLeadMinutes);
        Assert.Equal(new SupportedRoute("*", "*"), Assert.Single(point.SupportedRoutes));
        Assert.True(catalog.SupportsRoute("npc:Abigail", "Beach", "community_center"));
    }

    [Fact]
    public void DoesNotShipUnmarkedPlaceholders()
    {
        LandmarkCatalog catalog = Load();

        Assert.Null(catalog.Find("saloon_meeting_spot"));
        Assert.Null(catalog.Find("town_square"));
    }

    private static LandmarkCatalog Load() =>
        LandmarkCatalog.Parse(File.ReadAllText(Path.Combine(AppContext.BaseDirectory, "assets", "landmarks.json")));
}
