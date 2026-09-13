using Microsoft.Xna.Framework;
using StardewValley;
using StardewValley.Pathfinding;

namespace GameAgent.Stardew.Tasks;

public sealed class GameNpcDriver : ITaskNpcDriver
{
    private readonly Dictionary<OperationKey, ActiveTravel> active = new();

    public WorldPosition ReadPosition(string npcEntityId)
    {
        NPC npc = RequireNpc(npcEntityId);
        return Position(npc);
    }

    public DriverResult StartTravel(OperationKey operation, Landmark landmark)
    {
        if (this.active.ContainsKey(operation))
            throw new InvalidOperationException("task travel operation is already active");

        NPC npc = RequireNpc(operation.NpcEntityId);
        if (npc.currentLocation is null)
            return Failed("npc_location_missing", npc);
        if (npc.temporaryController is not null || (npc.controller is not null && !npc.controller.NPCSchedule))
            return Rejected("npc_control_busy", npc);

        GameLocation? targetLocation = Game1.getLocationFromName(landmark.Position.Location);
        Vector2 targetTile = new(landmark.Position.X, landmark.Position.Y);
        if (targetLocation is null ||
            !string.Equals(targetLocation.NameOrUniqueName, landmark.Position.Location, StringComparison.Ordinal) ||
            !targetLocation.isTileOnMap(targetTile) ||
            !targetLocation.isTilePassable(targetTile) ||
            !TaskPathController.IsPassable(targetLocation, landmark.Position.X, landmark.Position.Y, npc))
        {
            return Rejected("invalid_landmark_target", npc);
        }

        if (At(npc, landmark.Position))
            return new DriverResult("succeeded", "arrived", Position(npc));

        SchedulePathDescription route = npc.pathfindToNextScheduleLocation(
            npc.ScheduleKey ?? "gameagent_task_travel",
            npc.currentLocation.NameOrUniqueName,
            npc.TilePoint.X,
            npc.TilePoint.Y,
            landmark.Position.Location,
            landmark.Position.X,
            landmark.Position.Y,
            -1,
            null,
            null);
        if (route.route is null || route.route.Count == 0 || route.route.Last() != new Point(landmark.Position.X, landmark.Position.Y))
            return Rejected("native_route_unreachable", npc);

        TaskPathController controller = new(route.route, npc, landmark.Position);
        ActiveTravel travel = new(npc, controller, landmark.Position, npc.ignoreScheduleToday, npc.endOfRouteMessage.Value);
        npc.ignoreScheduleToday = true;
        npc.controller = controller;
        this.active.Add(operation, travel);
        return new DriverResult("running", string.Empty, Position(npc));
    }

    public DriverResult Poll(OperationKey operation)
    {
        if (!this.active.TryGetValue(operation, out ActiveTravel? travel))
            return new DriverResult("interrupted", "operation_not_active", new WorldPosition(string.Empty, 0, 0));
        if (At(travel.Npc, travel.Target))
            return new DriverResult("succeeded", "arrived", Position(travel.Npc));
        if (travel.Npc.temporaryController is not null || !ReferenceEquals(travel.Npc.controller, travel.Controller))
            return new DriverResult("interrupted", "control_lost", Position(travel.Npc));
        return new DriverResult("running", string.Empty, Position(travel.Npc));
    }

    public void Release(OperationKey operation, string reason)
    {
        if (!this.active.Remove(operation, out ActiveTravel? travel))
            return;

        travel.Npc.ignoreScheduleToday = travel.OriginalIgnoreSchedule;
        travel.Npc.endOfRouteMessage.Value = travel.OriginalEndMessage;
        if (!ReferenceEquals(travel.Npc.controller, travel.Controller))
            return;

        travel.Npc.controller = null;
        if (travel.Npc.temporaryController is null)
            travel.Npc.Halt();
    }

    private static NPC RequireNpc(string entityId)
    {
        const string Prefix = "npc:";
        if (!entityId.StartsWith(Prefix, StringComparison.Ordinal) || entityId.Length == Prefix.Length)
            throw new ArgumentException("task NPC entity_id is invalid", nameof(entityId));
        string name = entityId[Prefix.Length..];
        return Game1.getCharacterFromName(name, mustBeVillager: true)
            ?? throw new InvalidOperationException($"task NPC was not found: {name}");
    }

    private static bool At(NPC npc, WorldPosition target) =>
        string.Equals(npc.currentLocation?.NameOrUniqueName, target.Location, StringComparison.Ordinal) &&
        npc.TilePoint == new Point(target.X, target.Y);

    private static WorldPosition Position(NPC npc) => new(
        npc.currentLocation?.NameOrUniqueName ?? "unavailable",
        npc.TilePoint.X,
        npc.TilePoint.Y);

    private static DriverResult Rejected(string code, NPC npc) => new("rejected", code, Position(npc));
    private static DriverResult Failed(string code, NPC npc) => new("failed", code, Position(npc));

    private sealed record ActiveTravel(
        NPC Npc,
        TaskPathController Controller,
        WorldPosition Target,
        bool OriginalIgnoreSchedule,
        string? OriginalEndMessage);

    private sealed class TaskPathController : PathFindController
    {
        private readonly NPC npc;
        private readonly WorldPosition target;

        public TaskPathController(Stack<Point> path, NPC npc, WorldPosition target)
            : base(path, npc, npc.currentLocation)
        {
            this.npc = npc;
            this.target = target;
            this.finalFacingDirection = -1;
        }

        public static bool IsPassable(GameLocation location, int x, int y, NPC npc) =>
            !isPositionImpassableForNPCSchedule(location, x, y, npc);

        public override bool update(GameTime time)
        {
            if (At(this.npc, this.target))
            {
                this.npc.Halt();
                return false;
            }
            return base.update(time);
        }
    }
}
