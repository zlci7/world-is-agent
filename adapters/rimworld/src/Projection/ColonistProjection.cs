using System;
using System.Collections.Generic;
using System.Linq;
using Google.Protobuf.WellKnownTypes;

namespace Wia.RimWorld.Projection
{
    /// <summary>
    /// Turns a frozen <see cref="RimWorldSnapshot"/> into <c>Observation.state.rimworld</c>.
    ///
    /// This is a pure function over game-free input on purpose: the same snapshot must always
    /// produce the same structured content, and that is checkable without a running game. Nothing
    /// here reads RimWorld, the main thread, or time.
    ///
    /// Bounded by construction. Every list is ordered by an explicit total order before it is cut
    /// to size, so truncation keeps a deterministic prefix rather than whatever the game happened to
    /// enumerate first, and every string and number is clamped.
    /// </summary>
    internal static class ColonistProjection
    {
        public const string SchemaVersion = "0.1";
        public const int MaxNameChars = 80;
        public const int MaxTraitLabelChars = 80;
        public const int MaxBackgroundChars = 120;
        public const int MaxTraits = 4;
        public const int MaxSkills = 5;

        public static Struct Build(RimWorldSnapshot snapshot)
        {
            if (snapshot == null)
            {
                throw new ArgumentNullException(nameof(snapshot));
            }

            Struct rimworld = new Struct();
            rimworld.Fields["schema_version"] = Value.ForString(SchemaVersion);
            rimworld.Fields["pawn"] = Value.ForStruct(BuildPawn(snapshot));
            rimworld.Fields["location"] = Value.ForStruct(BuildLocation(snapshot));
            rimworld.Fields["needs"] = Value.ForStruct(BuildNeeds(snapshot));
            rimworld.Fields["mood"] = Value.ForNumber(Unit(snapshot.Mood));
            rimworld.Fields["health"] = Value.ForString(snapshot.Health);
            return rimworld;
        }

        private static Struct BuildPawn(RimWorldSnapshot snapshot)
        {
            Struct pawn = new Struct();
            pawn.Fields["id"] = Value.ForString(snapshot.EntityId);
            pawn.Fields["name"] = Value.ForString(Clamp(snapshot.Name, MaxNameChars));
            pawn.Fields["colonist"] = Value.ForBool(snapshot.Colonist);
            pawn.Fields["drafted"] = Value.ForBool(snapshot.Drafted);
            pawn.Fields["traits"] = Value.ForList(BuildTraits(snapshot.Traits));
            pawn.Fields["childhood"] = Value.ForString(Clamp(snapshot.Childhood, MaxBackgroundChars));
            pawn.Fields["adulthood"] = Value.ForString(Clamp(snapshot.Adulthood, MaxBackgroundChars));
            pawn.Fields["skills"] = Value.ForList(BuildSkills(snapshot.Skills));
            return pawn;
        }

        private static Value[] BuildTraits(TraitFact[] traits)
        {
            if (traits == null || traits.Length == 0)
            {
                return new Value[0];
            }

            // def_name is the stable machine identity and the ordering key. The extra tie-breaks are
            // not decoration: without them the order of two entries with the same def_name would
            // depend on the sort implementation, and this function must not depend on that.
            IEnumerable<TraitFact> ordered = traits
                .OrderBy(trait => trait.DefName, StringComparer.Ordinal)
                .ThenBy(trait => trait.Degree)
                .ThenBy(trait => trait.Label, StringComparer.Ordinal)
                .Take(MaxTraits);

            List<Value> result = new List<Value>();
            foreach (TraitFact trait in ordered)
            {
                Struct entry = new Struct();
                entry.Fields["def_name"] = Value.ForString(trait.DefName);
                entry.Fields["degree"] = Value.ForNumber(trait.Degree);
                entry.Fields["label"] = Value.ForString(Clamp(trait.Label, MaxTraitLabelChars));
                result.Add(Value.ForStruct(entry));
            }

            return result.ToArray();
        }

        private static Value[] BuildSkills(SkillFact[] skills)
        {
            if (skills == null || skills.Length == 0)
            {
                return new Value[0];
            }

            IEnumerable<SkillFact> ordered = skills
                .OrderByDescending(skill => skill.Level)
                .ThenBy(skill => skill.DefName, StringComparer.Ordinal)
                .Take(MaxSkills);

            List<Value> result = new List<Value>();
            foreach (SkillFact skill in ordered)
            {
                Struct entry = new Struct();
                entry.Fields["def_name"] = Value.ForString(skill.DefName);
                entry.Fields["level"] = Value.ForNumber(skill.Level);
                result.Add(Value.ForStruct(entry));
            }

            return result.ToArray();
        }

        private static Struct BuildLocation(RimWorldSnapshot snapshot)
        {
            Struct location = new Struct();

            // The two fields are present or absent together. Without a map, Pawn.Position is not a
            // fact about where the pawn is, so reporting one would be inventing a location.
            bool hasMap = !string.IsNullOrEmpty(snapshot.MapId);

            location.Fields["map_id"] = hasMap ? Value.ForString(snapshot.MapId) : Value.ForNull();
            location.Fields["position"] = hasMap && snapshot.HasPosition
                ? Value.ForStruct(Position(snapshot))
                : Value.ForNull();
            location.Fields["in_caravan"] = Value.ForBool(snapshot.InCaravan);
            return location;
        }

        private static Struct Position(RimWorldSnapshot snapshot)
        {
            Struct position = new Struct();
            position.Fields["x"] = Value.ForNumber(snapshot.PositionX);
            position.Fields["z"] = Value.ForNumber(snapshot.PositionZ);
            return position;
        }

        private static Struct BuildNeeds(RimWorldSnapshot snapshot)
        {
            Struct needs = new Struct();
            needs.Fields["food"] = Value.ForNumber(Unit(snapshot.Food));
            needs.Fields["rest"] = Value.ForNumber(Unit(snapshot.Rest));
            needs.Fields["recreation"] = Value.ForNumber(Unit(snapshot.Recreation));
            return needs;
        }

        /// <summary>
        /// Clamps a 0..1 reading. NaN and infinity become 0 rather than travelling on: a Struct
        /// carrying them serialises to JSON that is not valid JSON, and the model would see a
        /// literal "NaN" with no way to tell it apart from a real reading.
        /// </summary>
        private static double Unit(float value)
        {
            if (float.IsNaN(value) || float.IsInfinity(value))
            {
                return 0d;
            }

            if (value < 0f)
            {
                return 0d;
            }

            return value > 1f ? 1d : value;
        }

        /// <summary>
        /// Truncates to a character budget without splitting a surrogate pair: half of one is not
        /// text, and leaving one in the observation would put an unpaired surrogate on the wire.
        /// </summary>
        private static string Clamp(string value, int maxChars)
        {
            if (string.IsNullOrEmpty(value))
            {
                return string.Empty;
            }

            if (value.Length <= maxChars)
            {
                return value;
            }

            int end = maxChars;
            if (end > 0 && char.IsHighSurrogate(value[end - 1]))
            {
                end--;
            }

            return value.Substring(0, end);
        }
    }
}
