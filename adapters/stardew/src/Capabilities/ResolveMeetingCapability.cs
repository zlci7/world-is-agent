using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;

namespace GameAgent.Stardew.Capabilities;

public sealed class ResolveMeetingCapability
{
    private readonly LandmarkCatalogStore catalogStore;

    public ResolveMeetingCapability(LandmarkCatalogStore catalogStore)
    {
        this.catalogStore = catalogStore;
    }

    public MeetingResolution Resolve(
        RuntimeWorldSnapshot world,
        string npcEntityId,
        string playerEntityId,
        string originLocation,
        MeetingRequest request,
        FestivalKnowledge festival)
    {
        return MeetingContract.Resolve(world, npcEntityId, playerEntityId, originLocation, request, festival, this.catalogStore.Current);
    }
}
