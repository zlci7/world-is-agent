using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Tasks;

public sealed record TravelDecision(bool Accepted, string Code, string Message, Landmark? Landmark);

public static class TaskTravelPolicy
{
    public static TravelDecision Validate(
        TaskOperationSource source,
        RuntimeWorldSnapshot world,
        string landmarkId,
        WorldPosition current,
        LandmarkCatalog catalog)
    {
        if (!string.Equals(source.Contract.LandmarkId, landmarkId, StringComparison.Ordinal))
            return Reject("landmark_mismatch", "requested landmark does not match the task contract");
        if (!string.Equals(source.Contract.ClockId, world.ClockId, StringComparison.Ordinal))
            return Reject("clock_mismatch", "task clock does not match the active world clock");
        if (world.NowTick < source.Contract.DepartureAt)
            return Reject("departure_not_reached", "task departure time has not been reached");
        Landmark? landmark = catalog.Find(landmarkId);
        if (landmark is null)
            return Reject("landmark_not_found", "meeting landmark is not configured");
        if (world.NowTick >= source.Contract.StartAt)
        {
            bool arrived = string.Equals(current.Location, landmark.Position.Location, StringComparison.Ordinal) &&
                current.X == landmark.Position.X && current.Y == landmark.Position.Y;
            return arrived
                ? new TravelDecision(true, string.Empty, string.Empty, landmark)
                : Reject("arrival_deadline_missed", "task arrival deadline has been reached");
        }
        if (!catalog.SupportsRoute(source.Operation.NpcEntityId, current.Location, landmarkId))
            return Reject("route_not_supported", "NPC route has not been verified for this origin and landmark");

        return new TravelDecision(true, string.Empty, string.Empty, landmark);
    }

    private static TravelDecision Reject(string code, string message) => new(false, code, message, null);
}
