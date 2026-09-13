using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class LandmarkCatalogTests
{
    [Fact]
    public void LoadsVerifiedRouteByNpcOriginAndLandmark()
    {
        LandmarkCatalog catalog = LandmarkCatalog.Parse(ValidCatalogJson);

        Landmark landmark = Assert.IsType<Landmark>(catalog.Find("beach_meeting_spot"));
        Assert.Equal(new WorldPosition("Beach", 28, 36), landmark.Position);
        Assert.True(catalog.SupportsRoute("npc:Linus", "Mountain", "beach_meeting_spot"));
        Assert.False(catalog.SupportsRoute("npc:Abigail", "Mountain", "beach_meeting_spot"));
        Assert.False(catalog.SupportsRoute("npc:Linus", "Town", "beach_meeting_spot"));
    }

    [Theory]
    [InlineData("{\"landmarks\":[]}", "landmark_catalog_empty")]
    [InlineData("{\"landmarks\":[{\"landmark_id\":\"beach_meeting_spot\",\"display_name\":\"Beach\",\"location\":\"Beach\",\"tile\":{\"x\":28,\"y\":36},\"open_start\":600,\"open_end\":2200,\"departure_lead_minutes\":235,\"supported_routes\":[]}]}", "invalid_departure_lead")]
    public void RejectsInvalidCatalogs(string json, string code)
    {
        LandmarkCatalogException error = Assert.Throws<LandmarkCatalogException>(() => LandmarkCatalog.Parse(json));
        Assert.Equal(code, error.Code);
    }

    [Fact]
    public void RejectsDuplicateRouteKeys()
    {
        string json = ValidCatalogJson.Replace(
            "{\"npc_id\":\"npc:Linus\",\"origin_location\":\"Mountain\"}",
            "{\"npc_id\":\"npc:Linus\",\"origin_location\":\"Mountain\"},{\"npc_id\":\"npc:Linus\",\"origin_location\":\"Mountain\"}");

        LandmarkCatalogException error = Assert.Throws<LandmarkCatalogException>(() => LandmarkCatalog.Parse(json));
        Assert.Equal("duplicate_supported_route", error.Code);
    }

    public const string ValidCatalogJson =
        "{\"landmarks\":[{\"landmark_id\":\"beach_meeting_spot\",\"display_name\":\"Beach meeting spot\",\"location\":\"Beach\",\"tile\":{\"x\":28,\"y\":36},\"open_start\":600,\"open_end\":2200,\"departure_lead_minutes\":240,\"supported_routes\":[{\"npc_id\":\"npc:Linus\",\"origin_location\":\"Mountain\"}]}]}";
}
