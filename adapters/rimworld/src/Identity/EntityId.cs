using Verse;

namespace Wia.RimWorld.Identity
{
    /// <summary>
    /// Builds the WIA entity id for a pawn.
    ///
    /// The value is derived from the game's own stable id rather than from the pawn's name, which the
    /// player can change, or from its position, which changes constantly. The exact result is
    /// whatever <c>Pawn.GetUniqueLoadID()</c> returns - the adapter does not reconstruct it, because
    /// the format belongs to the game. What WIA freezes is the shape: a single <c>pawn:</c> prefix,
    /// added exactly once, plus <c>entity_type = "pawn"</c> on every EntityRef built from it.
    /// </summary>
    internal static class EntityId
    {
        /// <summary>entity_type carried by every EntityRef built from a pawn entity id.</summary>
        public const string EntityType = "pawn";

        private const string Prefix = "pawn:";

        /// <summary>Identity of a pawn within its world. Stable across save/load and Map/Caravan.</summary>
        public static string For(Pawn pawn)
        {
            return Prefix + pawn.GetUniqueLoadID();
        }
    }
}
