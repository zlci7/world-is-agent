using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ProductionLandmarkCatalogTests
{
    [Fact]
    public void ContainsOnlyRoutesBackedByRecordedLiveEvidence()
    {
        string json = File.ReadAllText(Path.Combine(AppContext.BaseDirectory, "assets", "landmarks.json"));
        LandmarkCatalog catalog = LandmarkCatalog.Parse(json);

        Landmark beach = Assert.Single(catalog.Landmarks);
        Assert.Equal("beach_meeting_spot", beach.LandmarkId);
        Assert.Equal(new WorldPosition("Beach", 28, 36), beach.Position);
        Assert.Equal(240, beach.DepartureLeadMinutes);
        Assert.Equal(new SupportedRoute("npc:Linus", "Mountain"), Assert.Single(beach.SupportedRoutes));
        Assert.Null(catalog.Find("saloon_meeting_spot"));
        Assert.Null(catalog.Find("town_square"));
    }
}
