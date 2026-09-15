using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ProductionLandmarkCatalogTests
{
    [Theory]
    [InlineData("beach_meeting_spot", "Beach", 28, 36, 240)]
    [InlineData("community_center", "Town", 55, 22, 90)]
    [InlineData("pierre_store", "Town", 46, 59, 150)]
    [InlineData("trailer", "Town", 79, 67, 180)]
    [InlineData("mine_entrance", "Mountain", 53, 6, 180)]
    public void ShipsEachMarkedMeetingPoint(string landmarkId, string location, int x, int y, int departureLeadMinutes)
    {
        LandmarkCatalog catalog = Load();

        Landmark landmark = Assert.IsType<Landmark>(catalog.Find(landmarkId));
        Assert.Equal(new WorldPosition(location, x, y), landmark.Position);
        Assert.Equal(departureLeadMinutes, landmark.DepartureLeadMinutes);
        Assert.NotEmpty(landmark.SupportedRoutes);
    }

    [Fact]
    public void KeepsTheLiveEvidenceRouteForTheBeachMeeting()
    {
        LandmarkCatalog catalog = Load();

        Landmark beach = Assert.IsType<Landmark>(catalog.Find("beach_meeting_spot"));
        Assert.Equal(new SupportedRoute("npc:Linus", "Mountain"), Assert.Single(beach.SupportedRoutes));
        Assert.Equal(220, beach.MeasuredTravelMinutes);
        Assert.False(catalog.SupportsRoute("npc:Abigail", "Mountain", "beach_meeting_spot"));
    }

    [Fact]
    public void AuthoringMarkedPointsAcceptAnyNpcFromAnyLocation()
    {
        LandmarkCatalog catalog = Load();

        Landmark point = Assert.IsType<Landmark>(catalog.Find("community_center"));
        Assert.Equal(new SupportedRoute("*", "*"), Assert.Single(point.SupportedRoutes));
        Assert.True(catalog.SupportsRoute("npc:Abigail", "Beach", "community_center"));
        Assert.True(catalog.SupportsRoute("npc:Shane", "Forest", "pierre_store"));
        Assert.True(catalog.SupportsRoute("npc:Pam", "Mountain", "trailer"));
        Assert.True(catalog.SupportsRoute("npc:Linus", "Beach", "mine_entrance"));
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
