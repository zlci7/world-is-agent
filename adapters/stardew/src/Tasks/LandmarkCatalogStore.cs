namespace GameAgent.Stardew.Tasks;

// LandmarkCatalog is immutable. The store publishes the catalog that meeting
// resolution and capability building read, so a revalidated catalog can replace
// the running one without restarting the mod.
public sealed class LandmarkCatalogStore
{
    private readonly object gate = new();
    private LandmarkCatalog current;

    public LandmarkCatalogStore(LandmarkCatalog initial)
    {
        this.current = initial ?? throw new ArgumentNullException(nameof(initial));
    }

    public LandmarkCatalog Current
    {
        get { lock (this.gate) return this.current; }
    }

    public void Replace(LandmarkCatalog catalog)
    {
        ArgumentNullException.ThrowIfNull(catalog);
        lock (this.gate) this.current = catalog;
    }
}
