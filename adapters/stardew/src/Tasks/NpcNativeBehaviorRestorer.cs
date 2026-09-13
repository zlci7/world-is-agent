using StardewModdingAPI;
using StardewValley;
using StardewValley.Pathfinding;

namespace GameAgent.Stardew.Tasks;

public sealed record NativeRestoreResult(string Status, string Code);

public sealed class NpcNativeBehaviorRestorer
{
    public NativeRestoreResult Restore(NPC npc)
    {
        try
        {
            return RestoreCore(npc);
        }
        catch
        {
            return new NativeRestoreResult("failed", "native_restore_failed");
        }
    }

    private static NativeRestoreResult RestoreCore(NPC npc)
    {
        if (!Context.IsWorldReady || !Context.IsMainPlayer || Context.IsMultiplayer)
            return new NativeRestoreResult("skipped", "world_unavailable");
        if (npc.temporaryController is not null || npc.controller is not null)
            return new NativeRestoreResult("preserved", "foreign_control_preserved");
        if (!npc.followSchedule || npc.Schedule is null || npc.Schedule.Count == 0 || npc.currentLocation is null)
        {
            npc.Halt();
            return new NativeRestoreResult("skipped", "native_schedule_unavailable");
        }

        int? currentTime = NpcNativeSchedulePolicy.SelectCurrentTime(npc.Schedule.Keys, Game1.timeOfDay);
        if (!currentTime.HasValue || !npc.Schedule.TryGetValue(currentTime.Value, out SchedulePathDescription? current))
        {
            npc.Halt();
            return new NativeRestoreResult("skipped", "native_schedule_intent_missing");
        }
        if (string.Equals(npc.currentLocation.NameOrUniqueName, current.targetLocationName, StringComparison.Ordinal) &&
            npc.TilePoint == current.targetTile)
        {
            npc.Halt();
            return new NativeRestoreResult("restored", "native_stationary");
        }

        SchedulePathDescription fresh = npc.pathfindToNextScheduleLocation(
            npc.ScheduleKey ?? "gameagent_native_restore",
            npc.currentLocation.NameOrUniqueName,
            npc.TilePoint.X,
            npc.TilePoint.Y,
            current.targetLocationName,
            current.targetTile.X,
            current.targetTile.Y,
            current.facingDirection,
            current.endOfRouteBehavior,
            current.endOfRouteMessage);
        if (fresh.route is null || fresh.route.Count == 0)
        {
            npc.Halt();
            return new NativeRestoreResult("failed", "native_route_unreachable");
        }

        fresh.time = Game1.timeOfDay;
        npc.queuedSchedulePaths.Clear();
        npc.queuedSchedulePaths.Add(fresh);
        npc.lastAttemptedSchedule = Game1.timeOfDay;
        npc.checkSchedule(Game1.timeOfDay);
        if (npc.controller is null &&
            (!string.Equals(npc.currentLocation.NameOrUniqueName, current.targetLocationName, StringComparison.Ordinal) || npc.TilePoint != current.targetTile))
        {
            return new NativeRestoreResult("failed", "native_schedule_not_started");
        }
        return new NativeRestoreResult("restored", npc.controller is null ? "native_stationary" : "native_schedule_rejoined");
    }
}
