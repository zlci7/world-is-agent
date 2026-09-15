using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class LandmarkCatalogStoreTests
{
    [Fact]
    public void ReplaceSwapsTheCatalogVisibleToReaders()
    {
        LandmarkCatalog initial = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);
        LandmarkCatalogStore store = new(initial);
        Assert.Same(initial, store.Current);
        Assert.Null(store.Current.Find("tavern_door"));

        LandmarkCatalog updated = initial.WithLandmark(new Landmark(
            "tavern_door", "Tavern door", new WorldPosition("Town", 12, 20), 600, 2200, 10,
            new[] { new SupportedRoute("npc:Linus", "Town") }));
        store.Replace(updated);

        Assert.Same(updated, store.Current);
        Assert.NotNull(store.Current.Find("tavern_door"));
    }
}
