using System;
using RimWorld.Planet;
using Verse;

namespace Wia.RimWorld.Identity
{
    /// <summary>
    /// The WIA identity of one world, stored in the save.
    ///
    /// Nothing the game already keeps can serve as a world identity: <c>World.info.name</c> is
    /// player-editable, <c>World.info.seedString</c> does not tell two saves of the same seed apart,
    /// and the file name changes on Save As. So the adapter generates a GUID and scribes it, which
    /// also keeps the save readable if the mod is later removed.
    ///
    /// Known semantics, accepted and recorded rather than solved: Save As and copying the file
    /// inherit the same GUID, so this identifies a <em>world lineage</em>, not a file. A save written
    /// before the mod was installed carries no value and starts a new lineage the first time the mod
    /// saves it.
    /// </summary>
    public class WorldIdentityComponent : WorldComponent
    {
        private string worldId;

        public WorldIdentityComponent(global::RimWorld.Planet.World world)
            : base(world)
        {
            // Covers a world that has never been saved. Loading overwrites this in ExposeData with
            // the value from the save, so the constructor value only survives for genuinely new worlds.
            this.worldId = NewWorldId();
        }

        /// <summary>Stable for the lifetime of this world lineage. Never null or empty.</summary>
        public string WorldId
        {
            get { return this.worldId; }
        }

        public override void ExposeData()
        {
            base.ExposeData();

            Scribe_Values.Look(ref this.worldId, "wiaWorldId");

            // A save written before the mod was installed has nothing to look up, and Scribe leaves
            // the constructor value in place. Regenerating here keeps the invariant "never empty"
            // explicit instead of relying on that side effect.
            if (Scribe.mode == LoadSaveMode.PostLoadInit && string.IsNullOrEmpty(this.worldId))
            {
                this.worldId = NewWorldId();
            }
        }

        /// <summary>
        /// The identity of the loaded world, or null when there is no world or the component that
        /// owns the identity is missing. The two are told apart by the caller rather than collapsed
        /// here, because "no world yet" and "the component did not load" are different faults.
        ///
        /// Main thread only.
        /// </summary>
        public static string CurrentWorldId()
        {
            global::RimWorld.Planet.World world = Find.World;
            if (world == null)
            {
                return null;
            }

            WorldIdentityComponent identity = world.GetComponent<WorldIdentityComponent>();
            return identity == null ? null : identity.WorldId;
        }

        private static string NewWorldId()
        {
            return Guid.NewGuid().ToString("N");
        }
    }
}
