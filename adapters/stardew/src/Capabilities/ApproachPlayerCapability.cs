using GameAgent.Stardew.Tasks;
using Microsoft.Xna.Framework;
using StardewValley;
using StardewValley.Pathfinding;

namespace GameAgent.Stardew.Capabilities;

public sealed class ApproachPlayerException : Exception
{
    public ApproachPlayerException(string code, string message) : base(message) => this.Code = code;
    public string Code { get; }
}

public sealed class ApproachPlayerCapability
{
    private readonly MoveToCapability moveTo;

    public ApproachPlayerCapability(MoveToCapability moveTo) => this.moveTo = moveTo;

    public ApproachPlayerStart Start(
        string actionId,
        NPC npc,
        Farmer player,
        Func<bool> isCancelled,
        Action<ApproachPlayerResult> onSucceeded,
        Action<string> onCancelled,
        Action<string, Exception> onFailed)
    {
        if (npc.currentLocation is null || player.currentLocation is null ||
            !ReferenceEquals(npc.currentLocation, player.currentLocation))
        {
            throw new ApproachPlayerException("different_location", "NPC and player must be in the same location");
        }

        WorldPosition playerAtStart = Position(player);
        Tile npcTile = new(npc.TilePoint.X, npc.TilePoint.Y);
        Tile playerTile = new(player.TilePoint.X, player.TilePoint.Y);
        AdjacentSelection? selection = AdjacentTileSelector.Select(
            npcTile,
            playerTile,
            BuildCandidates(npc, playerTile));
        if (selection is null)
            throw new ApproachPlayerException("no_reachable_adjacent_tile", "player has no reachable adjacent tile");

        WorldPosition target = new(npc.currentLocation.NameOrUniqueName, selection.Target.X, selection.Target.Y);
        MoveToStart move = this.moveTo.Start(
            actionId,
            npc,
            new MoveToInput(target.Location, target.X, target.Y),
            isCancelled,
            progress => onSucceeded(BuildResult(playerAtStart, target, progress, player)),
            onCancelled,
            onFailed);
        return new ApproachPlayerStart(playerAtStart, target, FromProgress(move.Progress), move.AlreadyAtTarget);
    }

    public static ApproachPlayerResult CompleteAlreadyAtTarget(ApproachPlayerStart start, NPC npc, Farmer player)
    {
        return BuildResult(
            start.PlayerPositionAtStart,
            start.Target,
            new MoveToProgress(npc.currentLocation?.NameOrUniqueName ?? "unavailable", npc.TilePoint.X, npc.TilePoint.Y),
            player);
    }

    private static IReadOnlyList<AdjacentCandidate> BuildCandidates(NPC npc, Tile player)
    {
        GameLocation location = npc.currentLocation;
        Tile[] tiles =
        {
            new(player.X, player.Y - 1),
            new(player.X + 1, player.Y),
            new(player.X, player.Y + 1),
            new(player.X - 1, player.Y),
        };
        List<AdjacentCandidate> result = new(4);
        foreach (Tile tile in tiles)
        {
            Vector2 vector = new(tile.X, tile.Y);
            bool passable = location.isTileOnMap(vector) &&
                location.isTilePassable(vector) &&
                ApproachPathInspector.IsPassable(location, tile.X, tile.Y, npc);
            bool occupied = location.characters.Any(character =>
                !ReferenceEquals(character, npc) && character.TilePoint == new Point(tile.X, tile.Y));
            IReadOnlyList<Tile>? path = passable && !occupied ? FindPath(npc, location, tile) : null;
            result.Add(new AdjacentCandidate(tile, passable, occupied, path));
        }
        return result;
    }

    private static IReadOnlyList<Tile>? FindPath(NPC npc, GameLocation location, Tile target)
    {
        Tile start = new(npc.TilePoint.X, npc.TilePoint.Y);
        if (start == target)
            return new[] { start };
        PathFindController candidate = new(npc, location, new Point(target.X, target.Y), finalFacingDirection: -1);
        if (candidate.pathToEndPoint is null || candidate.pathToEndPoint.Count == 0)
            return null;
        List<Tile> path = new() { start };
        path.AddRange(candidate.pathToEndPoint.Select(point => new Tile(point.X, point.Y)));
        if (path[^1] != target)
            return null;
        return path;
    }

    private static ApproachPlayerResult BuildResult(
        WorldPosition playerAtStart,
        WorldPosition target,
        MoveToProgress progress,
        Farmer player)
    {
        WorldPosition final = FromProgress(progress);
        WorldPosition currentPlayer = Position(player);
        bool adjacent = string.Equals(final.Location, currentPlayer.Location, StringComparison.Ordinal) &&
            Math.Abs(final.X - currentPlayer.X) + Math.Abs(final.Y - currentPlayer.Y) == 1;
        return new ApproachPlayerResult(playerAtStart, target, final, adjacent);
    }

    private static WorldPosition Position(Character actor) => new(
        actor.currentLocation?.NameOrUniqueName ?? "unavailable",
        actor.TilePoint.X,
        actor.TilePoint.Y);

    private static WorldPosition FromProgress(MoveToProgress progress) => new(progress.Location, progress.TileX, progress.TileY);

    private sealed class ApproachPathInspector : PathFindController
    {
        private ApproachPathInspector(Character character, GameLocation location, Point target)
            : base(character, location, target, -1) { }

        public static bool IsPassable(GameLocation location, int x, int y, NPC npc) =>
            !isPositionImpassableForNPCSchedule(location, x, y, npc);
    }
}
