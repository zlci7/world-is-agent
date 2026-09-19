namespace Wia.RimWorld.Projection
{
    /// <summary>
    /// Everything the projection is allowed to see about one colonist, with no game types in it.
    ///
    /// The split matters for more than tidiness. Reading a pawn is a main-thread operation against a
    /// running game and cannot be exercised without one; turning a snapshot into structured content
    /// is a pure function, so its determinism can be tested off-line against frozen input. Condition
    /// 13 of this stage - the same frozen snapshot must project to the same content twice - is
    /// therefore a test rather than an in-game observation.
    /// </summary>
    internal sealed class RimWorldSnapshot
    {
        public string EntityId = string.Empty;
        public string Name = string.Empty;
        public bool Colonist;
        public bool Drafted;
        public TraitFact[] Traits = new TraitFact[0];
        public string Childhood = string.Empty;
        public string Adulthood = string.Empty;
        public SkillFact[] Skills = new SkillFact[0];

        /// <summary>Null when the pawn has no map, which is the case in a caravan.</summary>
        public string MapId;

        /// <summary>Only meaningful when <see cref="MapId"/> is set; see the projector.</summary>
        public bool HasPosition;
        public int PositionX;
        public int PositionZ;

        public bool InCaravan;
        public float Food;
        public float Rest;
        public float Recreation;
        public float Mood;

        /// <summary>One of "normal", "injured", "downed".</summary>
        public string Health = RimWorldHealth.Normal;
    }

    internal sealed class TraitFact
    {
        public string DefName = string.Empty;
        public int Degree;
        public string Label = string.Empty;
    }

    internal sealed class SkillFact
    {
        public string DefName = string.Empty;
        public int Level;
    }

    /// <summary>The three health values this version reports. A closed set, not free text.</summary>
    internal static class RimWorldHealth
    {
        public const string Normal = "normal";
        public const string Injured = "injured";
        public const string Downed = "downed";
    }
}
