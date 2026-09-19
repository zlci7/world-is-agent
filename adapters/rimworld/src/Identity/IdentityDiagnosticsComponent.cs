using System.Collections.Generic;
using System.Text;
using RimWorld;
using RimWorld.Planet;
using Verse;

namespace Wia.RimWorld.Identity
{
    /// <summary>
    /// Writes the world identity, the colonist entity ids and the clock to the game log, so this
    /// stage's claims can be checked against an actual running game.
    ///
    /// It exists because nothing on the wire carries these values yet. The Runtime only learns a
    /// world id and an entity id from GameEvents and Observations, and both arrive with the
    /// projection and dialogue stages, so until then the log is the only place a save/load, a
    /// caravan trip or a clock reading can be observed. This is the stage's evidence output, not
    /// permanent product behaviour: once real protocol traffic carries identity, this component
    /// should shrink to whatever diagnostics are still worth keeping.
    ///
    /// It reports on save, on load, and once every <see cref="ClockReportIntervalTicks"/> ticks.
    /// </summary>
    public class IdentityDiagnosticsComponent : GameComponent
    {
        /// <summary>Roughly half a game hour, so a normal session stays readable.</summary>
        private const int ClockReportIntervalTicks = 2000;

        private long lastClockReportTick = long.MinValue;
        private string lastRosterSignature;

        /// <summary>
        /// The game argument is required by RimWorld's component discovery, which constructs every
        /// GameComponent subclass with <c>Activator.CreateInstance(type, game)</c>. GameComponent
        /// itself declares only a parameterless constructor - unlike WorldComponent, which takes the
        /// world - so the argument is deliberately dropped rather than chained to base. Omitting this
        /// constructor does not fail the build: the component is simply never created, and the game
        /// writes one "Could not instantiate a GameComponent" line to the log.
        /// </summary>
        public IdentityDiagnosticsComponent(Game game)
        {
        }

        public override void ExposeData()
        {
            base.ExposeData();

            // PostLoadInit rather than LoadingVars: the pawns and the world are only fully
            // reconstructed by then, and this component must report on the finished state.
            if (Scribe.mode == LoadSaveMode.PostLoadInit)
            {
                this.Report("load");
            }
            else if (Scribe.mode == LoadSaveMode.Saving)
            {
                this.Report("save");
            }
        }

        public override void GameComponentTick()
        {
            long tick;
            if (!GameClock.TryReadTick(out tick))
            {
                return;
            }

            // Reports on the interval, and also when the tick moves backwards: RimWorld lets the
            // player load an earlier save, and a rewind is precisely what must stay visible instead
            // of being silently swallowed by a monotonicity check.
            if (this.lastClockReportTick != long.MinValue &&
                tick >= this.lastClockReportTick &&
                tick - this.lastClockReportTick < ClockReportIntervalTicks)
            {
                return;
            }

            this.lastClockReportTick = tick;
            AdapterLog.Info("clock tick=" + tick);
            this.ReportRoster("tick");
        }

        private void Report(string slot)
        {
            AdapterLog.Info(
                "identity " + slot +
                " world=" + this.WorldIdOrNone() +
                " tick=" + this.TickOrNone());
            this.ReportRoster(slot);
        }

        private void ReportRoster(string slot)
        {
            List<Pawn> colonists = new List<Pawn>();
            foreach (Pawn pawn in PawnsFinder.AllMapsCaravansAndTravellingTransporters_Alive_FreeColonists)
            {
                if (EligibleColonist.Is(pawn))
                {
                    colonists.Add(pawn);
                }
            }

            StringBuilder signature = new StringBuilder();
            foreach (Pawn pawn in colonists)
            {
                signature.Append(EntityId.For(pawn)).Append('@')
                    .Append(MapText(pawn)).Append(',').Append(PositionText(pawn)).Append(';');
            }

            string current = signature.ToString();
            bool periodic = slot == "tick";
            if (periodic && current == this.lastRosterSignature)
            {
                return;
            }

            this.lastRosterSignature = current;

            if (colonists.Count == 0)
            {
                AdapterLog.Info("identity " + slot + " colonists=0");
                return;
            }

            foreach (Pawn pawn in colonists)
            {
                AdapterLog.Info(
                    "identity " + slot +
                    " entity=" + EntityId.For(pawn) +
                    " name=" + pawn.LabelShort +
                    " def=" + pawn.def.defName +
                    " map=" + MapText(pawn) +
                    " position=" + PositionText(pawn) +
                    " in_caravan=" + (pawn.ParentHolder is Caravan));
            }
        }

        private string WorldIdOrNone()
        {
            World world = Find.World;
            if (world == null)
            {
                return "none";
            }

            WorldIdentityComponent identity = world.GetComponent<WorldIdentityComponent>();
            if (identity == null || string.IsNullOrEmpty(identity.WorldId))
            {
                return "none";
            }

            return identity.WorldId;
        }

        private string TickOrNone()
        {
            long tick;
            return GameClock.TryReadTick(out tick) ? tick.ToString() : "none";
        }

        private static string MapText(Pawn pawn)
        {
            return pawn.Map == null ? "none" : pawn.Map.uniqueID.ToString();
        }

        /// <summary>
        /// Empty when the pawn has no map. <c>Pawn.Position</c> is not a fact about a pawn in a
        /// caravan or in an unsettled world state, so it is not reported as one.
        /// </summary>
        private static string PositionText(Pawn pawn)
        {
            return pawn.Map == null ? "none" : pawn.Position.ToString();
        }
    }
}
