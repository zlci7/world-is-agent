using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;

namespace GameAgent.Stardew.Capabilities;

public sealed class ResolveMeetingCapability
{
    private readonly LandmarkCatalog catalog;

    public ResolveMeetingCapability(LandmarkCatalog catalog)
    {
        this.catalog = catalog;
    }

    public MeetingResolution Resolve(
        RuntimeWorldSnapshot world,
        string npcEntityId,
        string playerEntityId,
        string originLocation,
        MeetingRequest request,
        FestivalKnowledge festival)
    {
        return MeetingContract.Resolve(world, npcEntityId, playerEntityId, originLocation, request, festival, this.catalog);
    }
}
