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

    [Fact]
    public void SerializesCanonicalJsonThatRoundTrips()
    {
        LandmarkCatalog catalog = LandmarkCatalog.Parse(ValidCatalogJson);

        string json = catalog.ToJson();
        LandmarkCatalog reparsed = LandmarkCatalog.Parse(json);

        Landmark landmark = Assert.IsType<Landmark>(reparsed.Find("beach_meeting_spot"));
        Assert.Equal(new WorldPosition("Beach", 28, 36), landmark.Position);
        Assert.Equal(600, landmark.OpenStart);
        Assert.Equal(2200, landmark.OpenEnd);
        Assert.Equal(240, landmark.DepartureLeadMinutes);
        Assert.True(reparsed.SupportsRoute("npc:Linus", "Mountain", "beach_meeting_spot"));
    }

    [Fact]
    public void WithLandmarkAddsThenReplacesWithoutDroppingOtherEntries()
    {
        LandmarkCatalog catalog = LandmarkCatalog.Parse(ValidCatalogJson);
        Landmark tavern = new(
            "tavern_door", "Tavern door", new WorldPosition("Town", 12, 20), 600, 2200, 10,
            new[] { new SupportedRoute("npc:Linus", "Town") });

        LandmarkCatalog added = catalog.WithLandmark(tavern);

        Assert.Equal(new WorldPosition("Town", 12, 20), Assert.IsType<Landmark>(added.Find("tavern_door")).Position);
        Assert.True(added.SupportsRoute("npc:Linus", "Town", "tavern_door"));
        Assert.Equal(new WorldPosition("Beach", 28, 36), Assert.IsType<Landmark>(added.Find("beach_meeting_spot")).Position);
        Assert.Equal(2, added.Landmarks.Count());

        LandmarkCatalog replaced = added.WithLandmark(tavern with { Position = new WorldPosition("Town", 30, 40) });

        Assert.Equal(new WorldPosition("Town", 30, 40), Assert.IsType<Landmark>(replaced.Find("tavern_door")).Position);
        Assert.Equal(2, replaced.Landmarks.Count());
    }

    [Fact]
    public void WithLandmarkRejectsAnEntryThatWouldProduceAnUnreadableCatalog()
    {
        LandmarkCatalog catalog = LandmarkCatalog.Parse(ValidCatalogJson);
        Landmark invalid = new(
            "tavern_door", "Tavern door", new WorldPosition("Town", 12, 20), 600, 2200, 235,
            new[] { new SupportedRoute("npc:Linus", "Town") });

        LandmarkCatalogException error = Assert.Throws<LandmarkCatalogException>(() => catalog.WithLandmark(invalid));

        Assert.Equal("invalid_departure_lead", error.Code);
        Assert.Null(catalog.Find("tavern_door"));
    }

    public const string ValidCatalogJson =
        "{\"landmarks\":[{\"landmark_id\":\"beach_meeting_spot\",\"display_name\":\"Beach meeting spot\",\"location\":\"Beach\",\"tile\":{\"x\":28,\"y\":36},\"open_start\":600,\"open_end\":2200,\"departure_lead_minutes\":240,\"supported_routes\":[{\"npc_id\":\"npc:Linus\",\"origin_location\":\"Mountain\"}]}]}";
}
