using System.Diagnostics;
using System.Text.Json;
using System.Text.Json.Serialization;
using GameAgent.Stardew.Runtime;
using Microsoft.Xna.Framework;
using StardewModdingAPI;
using StardewValley;
using StardewValley.Pathfinding;

namespace GameAgent.Stardew.Diagnostics;

/// <summary>Console-only, opt-in Phase9 feasibility instrument.</summary>
internal sealed class StardewRouteProbe : IRouteProbeDriver, IDisposable
{
    private static readonly JsonSerializerOptions Json = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        Converters = { new JsonStringEnumConverter() }
    };
    private readonly IModHelper helper;
    private readonly IMonitor monitor;
    private readonly AdapterConfig config;
    private readonly RouteProbe probe;
    private readonly Stopwatch clock = Stopwatch.StartNew();
    private NPC? npc;
    private ProbeRequest? request;
    private SchedulePathDescription? route;
    private ProbePathController? owned;
    private PathFindController? native;
    private bool changedFlags;
    private bool originalIgnoreSchedule;
    private string? originalEndMessage;
    private string? world;
    private string? date;
    private long worldGeneration;

    public StardewRouteProbe(IModHelper helper, IMonitor monitor, AdapterConfig config)
    {
        this.helper = helper;
        this.monitor = monitor;
        this.config = config;
        probe = new(config, this, () => clock.ElapsedMilliseconds, Emit);
        RouteProbe.RegisterCommands(config, (name, help, callback) =>
            helper.ConsoleCommands.Add(name, help, (_, args) => callback(args)),
            args => LogCode(probe.Start(args)), _ => Emit(probe.Status), _ => probe.Cancel());
        ProbeTestSaveLoader.RegisterCommand(config, (name, help, callback) =>
            helper.ConsoleCommands.Add(name, help, (_, args) => callback(args)), LoadTestSave);
    }

    public bool WorldReady => Context.IsWorldReady;
    public bool HasAuthority => Context.IsMainPlayer && !Context.IsMultiplayer;
    private string World => $"{worldGeneration}:{Game1.uniqueIDForThisGame}";
    private static string Date => $"{Game1.year}-{Game1.currentSeason}-{Game1.dayOfMonth}";

    public void Update() => probe.Update();
    public void WorldChanged(string reason)
    {
        probe.Cancel(reason, rejoinSchedule: false);
        worldGeneration++;
    }
    public void Dispose() => probe.Cancel("adapter_disposed");

    private void LoadTestSave(string[] args)
    {
        try
        {
            LogCode(ProbeTestSaveLoader.Load(config, args, Context.IsWorldReady,
                Game1.gameMode == 6, slot => { SaveGame.Load(slot); Game1.exitActiveMenu(); }));
        }
        catch (Exception ex) { LogCode("save_load_failed", ex.GetType().Name); }
    }

    public ProbePrepared Prepare(ProbeRequest input)
    {
        npc = Game1.getCharacterFromName(input.Npc, mustBeVillager: true)
            ?? throw new InvalidOperationException("npc_not_found");
        request = input;
        owned = null;
        native = null;
        changedFlags = false;
        if (npc.currentLocation == null) throw new InvalidOperationException("npc_location_missing");
        if (npc.temporaryController != null || (npc.controller != null && !npc.controller.NPCSchedule))
            throw new InvalidOperationException("npc_control_busy");
        if (npc.isMarried() || npc.IsWalkingInSquare || npc.doingEndOfRouteAnimation.Value ||
            helper.Reflection.GetField<bool>(npc, "freezeMotion").GetValue() || npc.ignoreScheduleToday || !npc.followSchedule ||
            npc.Schedule == null || npc.Schedule.Count == 0 || npc.currentScheduleDelay > 0 || npc.scheduleDelaySeconds > 0)
            throw new InvalidOperationException("native_schedule_state_unsupported");
        if (!TargetValid()) throw new InvalidOperationException("target_invalid");
        world = World;
        date = Date;
        originalIgnoreSchedule = npc.ignoreScheduleToday;
        originalEndMessage = npc.endOfRouteMessage.Value;
        string nativeState = JsonSerializer.Serialize(new
        {
            npc.ScheduleKey, npc.followSchedule, npc.ignoreScheduleToday, npc.lastAttemptedSchedule,
            queuedPaths = npc.queuedSchedulePaths.Count, controller = npc.controller == null ? "null" : "native_schedule",
            controllerRouteLength = npc.controller?.pathToEndPoint?.Count,
            directionsTarget = npc.DirectionsToNewLocation?.targetLocationName,
            previousEndPoint = new { npc.previousEndPoint.X, npc.previousEndPoint.Y }
        }, Json);
        var sample = Sample();
        route = BuildRoute(input.Target, -1, null, null);
        if (route.route == null || route.route.Count == 0 || route.route.Last() != new Point(input.Target.X, input.Target.Y))
            throw new InvalidOperationException("target_unreachable");
        return new(sample, route.route.Count, nativeState);
    }

    public void Install()
    {
        if (!WorldReady || !HasAuthority || World != world || Date != date)
            throw new InvalidOperationException("world_changed");
        owned = new ProbePathController(route!.route, npc!, request!.Target);
        changedFlags = true;
        npc!.ignoreScheduleToday = true;
        npc.controller = owned;
    }

    public ProbeSample Sample()
    {
        if (npc == null) throw new InvalidOperationException("npc_missing");
        var control = npc.controller == null ? ProbeControl.None : ReferenceEquals(npc.controller, owned) ? ProbeControl.Owned : ProbeControl.Foreign;
        bool sameWorld = WorldReady && World == world && Date == date && ReferenceEquals(Game1.getCharacterFromName(npc.Name), npc);
        bool nativeMovement = sameWorld && !changedFlags && npc.temporaryController == null && npc.controller != null &&
            (ReferenceEquals(npc.controller, native) ||
             (npc.controller.NPCSchedule && npc.DirectionsToNewLocation?.route == npc.controller.pathToEndPoint));
        var currentSchedule = sameWorld ? CurrentSchedule() : null;
        bool stationary = sameWorld && !changedFlags && npc.controller == null && npc.temporaryController == null &&
            currentSchedule != null && npc.currentLocation?.NameOrUniqueName == currentSchedule.targetLocationName &&
            npc.TilePoint == currentSchedule.targetTile && !npc.ignoreScheduleToday && npc.followSchedule;
        return new(sameWorld ? World : "world_unavailable", Date, Game1.timeOfDay, Position(npc), control,
            sameWorld && TargetValid(), nativeMovement, stationary, TemporaryControl: npc.temporaryController != null);
    }

    public void Hold()
    {
        if (ReferenceEquals(npc!.controller, owned) && npc.temporaryController == null)
            npc.Halt();
    }

    private void RestoreFlags()
    {
        if (!changedFlags || npc == null) return;
        changedFlags = false;
        npc.ignoreScheduleToday = originalIgnoreSchedule;
        npc.endOfRouteMessage.Value = originalEndMessage;
    }

    public string Release(bool rejoinSchedule)
    {
        if (npc == null) return "released";
        RestoreFlags();
        bool foreign = ProbeControllerOwnership.Release(npc.controller, owned, npc.temporaryController,
            () => npc.controller = null, npc.Halt);
        if (foreign) return "foreign_control_preserved";
        if (!rejoinSchedule || !WorldReady || !HasAuthority || World != world || Date != date)
            return "released_world_unavailable";
        SchedulePathDescription current = CurrentSchedule() ?? throw new InvalidOperationException("native_schedule_missing");
        // The loaded current-day schedule supplies intent; its previously consumed route is never replayed.
        SchedulePathDescription fresh = BuildRoute(new(current.targetLocationName, current.targetTile.X, current.targetTile.Y),
            current.facingDirection, current.endOfRouteBehavior, current.endOfRouteMessage);
        if (fresh.route == null || fresh.route.Count == 0)
            throw new InvalidOperationException("native_route_unreachable");
        fresh.time = Game1.timeOfDay;
        npc.queuedSchedulePaths.Clear();
        npc.queuedSchedulePaths.Add(fresh);
        npc.lastAttemptedSchedule = Game1.timeOfDay;
        npc.checkSchedule(Game1.timeOfDay);
        native = npc.controller;
        if (native == null && (npc.currentLocation?.NameOrUniqueName != current.targetLocationName || npc.TilePoint != current.targetTile))
            throw new InvalidOperationException("native_schedule_not_started");
        return native == null ? "native_stationary_candidate" : "native_schedule_rejoined";
    }

    private SchedulePathDescription? CurrentSchedule() => npc!.Schedule?
        .Where(pair => pair.Key <= Game1.timeOfDay).OrderByDescending(pair => pair.Key).Select(pair => pair.Value).FirstOrDefault();

    private SchedulePathDescription BuildRoute(ProbePosition destination, int facing, string? behavior, string? message) =>
        npc!.pathfindToNextScheduleLocation(npc.ScheduleKey ?? "phase9_route_probe",
            npc.currentLocation.NameOrUniqueName, npc.TilePoint.X, npc.TilePoint.Y,
            destination.Location, destination.X, destination.Y, facing, behavior, message);

    private bool TargetValid()
    {
        GameLocation? location = Game1.getLocationFromName(request!.Target.Location);
        Vector2 tile = new(request.Target.X, request.Target.Y);
        return location != null && location.NameOrUniqueName == request.Target.Location &&
            location.isTileOnMap(tile) && location.isTilePassable(tile) &&
            ProbePathController.IsPassable(location, request.Target.X, request.Target.Y, npc!);
    }

    private static ProbePosition Position(NPC actor) => new(actor.currentLocation?.NameOrUniqueName ?? "unavailable",
        actor.TilePoint.X, actor.TilePoint.Y, actor.Position.X, actor.Position.Y);
    private void Emit(ProbeStatus status) => monitor.Log("[Phase9RouteProbe] " + JsonSerializer.Serialize(status, Json), LogLevel.Info);
    private void LogCode(string code, string? errorType = null) => monitor.Log("[Phase9RouteProbe] " +
        JsonSerializer.Serialize(new { code, runId = probe.Status.RunId, errorType }, Json), LogLevel.Info);

    private sealed class ProbePathController : PathFindController
    {
        private readonly NPC npc;
        private readonly ProbePosition target;
        public ProbePathController(Stack<Point> path, NPC npc, ProbePosition target) : base(path, npc, npc.currentLocation)
        {
            this.npc = npc;
            this.target = target;
            finalFacingDirection = -1;
        }

        public static bool IsPassable(GameLocation location, int x, int y, NPC npc) =>
            !isPositionImpassableForNPCSchedule(location, x, y, npc);

        // Character.update is the sole caller. Retain ownership at the verified tile through dwell.
        public override bool update(GameTime time)
        {
            if (npc.currentLocation?.NameOrUniqueName == target.Location && npc.TilePoint == new Point(target.X, target.Y))
            {
                npc.Halt();
                return false;
            }
            return base.update(time);
        }
    }
}
