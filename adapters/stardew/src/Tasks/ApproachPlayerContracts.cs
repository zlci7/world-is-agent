namespace GameAgent.Stardew.Tasks;

public sealed record ApproachPlayerStart(
    WorldPosition PlayerPositionAtStart,
    WorldPosition Target,
    WorldPosition CurrentPosition,
    bool AlreadyAtTarget);

public sealed record ApproachPlayerResult(
    WorldPosition PlayerPositionAtStart,
    WorldPosition Target,
    WorldPosition FinalPosition,
    bool PlayerIsAdjacent);
