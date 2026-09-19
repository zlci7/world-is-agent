using Verse;

namespace Wia.RimWorld.Identity
{
    /// <summary>
    /// The single clock the adapter reads game time from.
    ///
    /// WIA's protocol supports calendar, calendar-with-tick and tick-only game time. This adapter
    /// freezes tick-only and does not invent a calendar mapping: <c>TicksGame</c> is absolute,
    /// monotonic within a loaded run, and persisted with the save, which is everything the Runtime
    /// needs and nothing it would have to interpret.
    ///
    /// Every GameEvent and every Observation this adapter builds must take its tick from here. There
    /// is deliberately no second source, so "Event and Observation share one clock" is a property of
    /// the code rather than something to keep in sync.
    ///
    /// Monotonicity is scoped to one loaded run. RimWorld lets the player load an earlier save while
    /// the world lineage keeps the same <c>world_id</c>, so the tick genuinely moves backwards across
    /// a rewind; the Runtime's existing game-time filtering handles that and this adapter adds no
    /// rollback mechanism of its own.
    ///
    /// Main thread only: <c>Find.TickManager</c> is game state.
    /// </summary>
    internal static class GameClock
    {
        /// <summary>
        /// Reads the current tick. Returns false when no game is loaded, in which case there is no
        /// game time to report; callers must not substitute a wall-clock value, because a fabricated
        /// tick would be indistinguishable from a real one on the wire.
        /// </summary>
        public static bool TryReadTick(out long tick)
        {
            TickManager ticks = Find.TickManager;
            if (ticks == null)
            {
                tick = 0;
                return false;
            }

            tick = ticks.TicksGame;
            return true;
        }
    }
}
