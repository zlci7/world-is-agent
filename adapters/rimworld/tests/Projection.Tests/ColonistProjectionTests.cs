using System.Collections.Generic;
using System.Linq;
using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Wia.RimWorld.Projection;
using Xunit;

namespace WiaRimWorld.Projection.Tests
{
    /// <summary>
    /// The projection is the one part of this adapter that can be checked without a running game, so
    /// these tests carry the properties the stage's exit conditions ask for: bounded content, a
    /// fixed schema, and a build that returns the same structured content for the same snapshot.
    /// </summary>
    public sealed class ColonistProjectionTests
    {
        [Fact]
        public void SameSnapshotProjectsToTheSameContent()
        {
            Struct first = ColonistProjection.Build(Snapshot());
            Struct second = ColonistProjection.Build(Snapshot());

            // Map semantics, not wire bytes: field order is not part of the contract, content is.
            Assert.True(first.Equals(second), "two builds of the same snapshot differ");
            Assert.Equal(JsonFormatter.Default.Format(first), JsonFormatter.Default.Format(second));
        }

        [Fact]
        public void PermutingTheInputDoesNotChangeTheContent()
        {
            RimWorldSnapshot ascending = Snapshot();
            RimWorldSnapshot descending = Snapshot();
            descending.Traits = descending.Traits.Reverse().ToArray();
            descending.Skills = descending.Skills.Reverse().ToArray();

            // Ordering is a property of the projection, not of the order the game happened to
            // enumerate. Without this the truncation prefix could differ between two reads of an
            // unchanged colonist.
            Assert.True(ColonistProjection.Build(ascending).Equals(ColonistProjection.Build(descending)));
        }

        [Fact]
        public void SchemaVersionIsReported()
        {
            Struct rimworld = ColonistProjection.Build(Snapshot());
            Assert.Equal("0.1", Str(rimworld, "schema_version"));
            Assert.Equal(ColonistProjection.SchemaVersion, Str(rimworld, "schema_version"));
        }

        [Fact]
        public void TraitsAreCutToTheCapInDefNameOrder()
        {
            RimWorldSnapshot snapshot = Snapshot();
            snapshot.Traits = new[]
            {
                Trait("zulu"), Trait("alpha"), Trait("mike"), Trait("bravo"), Trait("yankee"), Trait("charlie"),
            };

            IReadOnlyList<Value> traits = List(Pawn(ColonistProjection.Build(snapshot)), "traits");

            Assert.Equal(ColonistProjection.MaxTraits, traits.Count);
            Assert.Equal(
                new[] { "alpha", "bravo", "charlie", "mike" },
                traits.Select(value => Str(value.StructValue, "def_name")).ToArray());
        }

        [Fact]
        public void SkillsAreCutToTheCapByLevelThenDefName()
        {
            RimWorldSnapshot snapshot = Snapshot();
            snapshot.Skills = new[]
            {
                Skill("cooking", 3),
                Skill("shooting", 9),
                Skill("mining", 3),
                Skill("crafting", 12),
                Skill("plants", 9),
                Skill("medical", 3),
                Skill("social", 1),
            };

            IReadOnlyList<Value> skills = List(Pawn(ColonistProjection.Build(snapshot)), "skills");

            Assert.Equal(ColonistProjection.MaxSkills, skills.Count);
            Assert.Equal(
                new[] { "crafting", "plants", "shooting", "cooking", "medical" },
                skills.Select(value => Str(value.StructValue, "def_name")).ToArray());
            Assert.Equal(
                new double[] { 12, 9, 9, 3, 3 },
                skills.Select(value => Num(value.StructValue, "level")).ToArray());
        }

        /// <summary>
        /// Bounds what the projection is configured to bound. Several fields are deliberately not
        /// capped - the entity id, the map id, def names - because the adapter only ever fills them
        /// from the game's own identifiers, so a name that claimed every field were bounded would be
        /// claiming more than the code does.
        /// </summary>
        [Fact]
        public void ConfiguredTextFieldsAndCollectionsAreBounded()
        {
            RimWorldSnapshot snapshot = Snapshot();
            snapshot.Name = new string('n', 500);
            snapshot.Childhood = new string('c', 500);
            snapshot.Adulthood = new string('a', 500);
            snapshot.Traits = Enumerable.Range(0, 100).Select(index => Trait("trait" + index)).ToArray();
            snapshot.Skills = Enumerable.Range(0, 100).Select(index => Skill("skill" + index, index)).ToArray();

            Struct pawn = Pawn(ColonistProjection.Build(snapshot));

            Assert.Equal(ColonistProjection.MaxNameChars, Str(pawn, "name").Length);
            Assert.Equal(ColonistProjection.MaxBackgroundChars, Str(pawn, "childhood").Length);
            Assert.Equal(ColonistProjection.MaxBackgroundChars, Str(pawn, "adulthood").Length);
            Assert.Equal(ColonistProjection.MaxTraits, List(pawn, "traits").Count);
            Assert.Equal(ColonistProjection.MaxSkills, List(pawn, "skills").Count);
            Assert.All(
                List(pawn, "traits"),
                value => Assert.True(Str(value.StructValue, "label").Length <= ColonistProjection.MaxTraitLabelChars));
        }

        [Fact]
        public void TruncationDoesNotLeaveHalfOfASurrogatePair()
        {
            RimWorldSnapshot snapshot = Snapshot();
            // The 80th UTF-16 unit would be the high half of the emoji.
            snapshot.Name = new string('a', ColonistProjection.MaxNameChars - 1) + "\U0001F600" + "tail";

            string name = Str(Pawn(ColonistProjection.Build(snapshot)), "name");

            Assert.Equal(ColonistProjection.MaxNameChars - 1, name.Length);
            Assert.False(char.IsHighSurrogate(name[name.Length - 1]));
        }

        [Fact]
        public void WithoutAMapThereIsNeitherAMapIdNorAPosition()
        {
            RimWorldSnapshot snapshot = Snapshot();
            snapshot.MapId = null;
            snapshot.HasPosition = true;
            snapshot.PositionX = 12;
            snapshot.PositionZ = 34;
            snapshot.InCaravan = true;

            Struct location = Location(ColonistProjection.Build(snapshot));

            // Pawn.Position is not a fact about a pawn in a caravan, so a stale one must not be
            // reported as the current location.
            Assert.True(IsNull(location, "map_id"));
            Assert.True(IsNull(location, "position"));
            Assert.True(location.Fields["in_caravan"].BoolValue);
        }

        [Fact]
        public void WithAMapBothTheMapIdAndThePositionArePresent()
        {
            Struct location = Location(ColonistProjection.Build(Snapshot()));

            Assert.Equal("0", Str(location, "map_id"));
            Assert.Equal(12d, Num(location.Fields["position"].StructValue, "x"));
            Assert.Equal(34d, Num(location.Fields["position"].StructValue, "z"));
            Assert.False(location.Fields["in_caravan"].BoolValue);
        }

        [Fact]
        public void NonFiniteReadingsBecomeZero()
        {
            RimWorldSnapshot snapshot = Snapshot();
            snapshot.Food = float.NaN;
            snapshot.Rest = float.PositiveInfinity;
            snapshot.Recreation = float.NegativeInfinity;
            snapshot.Mood = float.NaN;

            Struct rimworld = ColonistProjection.Build(snapshot);
            Struct needs = Needs(rimworld);

            // A Struct carrying NaN serialises to JSON that is not JSON. The model would read the
            // literal "NaN" and have no way to tell it from a real reading.
            Assert.Equal(0d, Num(needs, "food"));
            Assert.Equal(0d, Num(needs, "rest"));
            Assert.Equal(0d, Num(needs, "recreation"));
            Assert.Equal(0d, Num(rimworld, "mood"));
        }

        [Fact]
        public void UnitReadingsAreClamped()
        {
            RimWorldSnapshot snapshot = Snapshot();
            snapshot.Food = 1.5f;
            snapshot.Rest = -0.5f;
            snapshot.Recreation = 0.42f;
            snapshot.Mood = 2f;

            Struct rimworld = ColonistProjection.Build(snapshot);
            Struct needs = Needs(rimworld);

            Assert.Equal(1d, Num(needs, "food"));
            Assert.Equal(0d, Num(needs, "rest"));
            Assert.Equal(0.41999998688697815d, Num(needs, "recreation"), 6);
            Assert.Equal(1d, Num(rimworld, "mood"));
        }

        [Fact]
        public void HealthIsCarriedThroughAsTheClosedSetTheSnapshotUses()
        {
            RimWorldSnapshot snapshot = Snapshot();
            snapshot.Health = RimWorldHealth.Downed;

            Assert.Equal("downed", Str(ColonistProjection.Build(snapshot), "health"));
        }

        private static RimWorldSnapshot Snapshot()
        {
            return new RimWorldSnapshot
            {
                EntityId = "pawn:Thing_Human89",
                Name = "塔斯坎姆",
                Colonist = true,
                Drafted = false,
                Traits = new[] { Trait("industriousness"), Trait("kind") },
                Childhood = "vatgrown soldier",
                Adulthood = "colonist",
                Skills = new[] { Skill("crafting", 7), Skill("plants", 4), Skill("medical", 2) },
                MapId = "0",
                HasPosition = true,
                PositionX = 12,
                PositionZ = 34,
                InCaravan = false,
                Food = 0.75f,
                Rest = 0.5f,
                Recreation = 0.25f,
                Mood = 0.6f,
                Health = RimWorldHealth.Normal,
            };
        }

        private static TraitFact Trait(string defName)
        {
            return new TraitFact { DefName = defName, Degree = 0, Label = defName + " label" };
        }

        private static SkillFact Skill(string defName, int level)
        {
            return new SkillFact { DefName = defName, Level = level };
        }

        private static Struct Pawn(Struct rimworld)
        {
            return rimworld.Fields["pawn"].StructValue;
        }

        private static Struct Location(Struct rimworld)
        {
            return rimworld.Fields["location"].StructValue;
        }

        private static Struct Needs(Struct rimworld)
        {
            return rimworld.Fields["needs"].StructValue;
        }

        private static IReadOnlyList<Value> List(Struct owner, string key)
        {
            return owner.Fields[key].ListValue.Values;
        }

        private static string Str(Struct owner, string key)
        {
            return owner.Fields[key].StringValue;
        }

        private static double Num(Struct owner, string key)
        {
            return owner.Fields[key].NumberValue;
        }

        private static bool IsNull(Struct owner, string key)
        {
            return owner.Fields[key].KindCase == Value.KindOneofCase.NullValue;
        }
    }
}
