using RimWorld;
using Verse;

namespace Wia.RimWorld.Identity
{
    /// <summary>
    /// Which pawns WIA treats as agents.
    ///
    /// This is not a tidiness filter. The WIA entry point is a ThingComp attached to the vanilla
    /// <c>Human</c> def, and <c>Human</c> is the base of every humanlike def, so enemy soldiers,
    /// prisoners and guests receive the same comp. Anything that acts on <c>Human</c> must therefore
    /// decide eligibility itself instead of trusting the def it was attached to.
    ///
    /// Scope for this version: vanilla Human, player faction, alive. Everything else - enemies,
    /// prisoners, guests, animals, mechanoids, the dead and modded races - has no WIA entry point.
    /// </summary>
    internal static class EligibleColonist
    {
        /// <summary>Reason a pawn is out of scope, for diagnostics. Empty when eligible.</summary>
        public static bool Is(Pawn pawn)
        {
            string reason;
            return Is(pawn, out reason);
        }

        public static bool Is(Pawn pawn, out string reason)
        {
            if (pawn == null)
            {
                reason = "nil";
                return false;
            }

            if (pawn.Destroyed)
            {
                reason = "destroyed";
                return false;
            }

            if (pawn.Dead)
            {
                reason = "dead";
                return false;
            }

            // Identity, not a category: modded humanlikes have their own defs and are out of scope
            // even when the player owns them.
            if (pawn.def != ThingDefOf.Human)
            {
                reason = "def";
                return false;
            }

            if (pawn.RaceProps == null || !pawn.RaceProps.Humanlike)
            {
                reason = "not_humanlike";
                return false;
            }

            if (pawn.Faction != Faction.OfPlayer)
            {
                reason = "faction";
                return false;
            }

            // Prisoners and guests sit in the player's faction but are not the player's colonists.
            if (pawn.HostFaction != null)
            {
                reason = "host_faction";
                return false;
            }

            if (pawn.IsSlave)
            {
                reason = "slave";
                return false;
            }

            reason = string.Empty;
            return true;
        }
    }
}
