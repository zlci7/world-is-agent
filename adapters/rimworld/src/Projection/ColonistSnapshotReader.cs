using System.Collections.Generic;
using RimWorld;
using RimWorld.Planet;
using Verse;
using Wia.RimWorld.Identity;

namespace Wia.RimWorld.Projection
{
    /// <summary>
    /// Reads one colonist out of a running game into a game-free snapshot.
    ///
    /// Main thread only: every field below is live game state. This is the whole reason the
    /// projection is split in two - reading is inherently unverifiable off-line, so it is kept as
    /// thin and as free of decisions as possible, and every decision that could be wrong in a
    /// reviewable way lives in <see cref="ColonistProjection"/> instead.
    ///
    /// Nothing here is omitted to make the output smaller. Values are read in full and clamped by
    /// the projector, so the caps are a property of the projection rather than of this reader.
    /// </summary>
    internal static class ColonistSnapshotReader
    {
        public static RimWorldSnapshot Read(Pawn pawn)
        {
            RimWorldSnapshot snapshot = new RimWorldSnapshot();
            snapshot.EntityId = EntityId.For(pawn);
            snapshot.Name = pawn.LabelShort;
            snapshot.Colonist = EligibleColonist.Is(pawn);
            snapshot.Drafted = pawn.Drafted;
            snapshot.Traits = ReadTraits(pawn);
            snapshot.Skills = ReadSkills(pawn);
            snapshot.Childhood = Backstory(pawn.story == null ? null : pawn.story.Childhood);
            snapshot.Adulthood = Backstory(pawn.story == null ? null : pawn.story.Adulthood);

            Map map = pawn.Map;
            if (map != null)
            {
                snapshot.MapId = map.uniqueID.ToString();
                snapshot.HasPosition = true;
                snapshot.PositionX = pawn.Position.x;
                snapshot.PositionZ = pawn.Position.z;
            }

            snapshot.InCaravan = pawn.ParentHolder is Caravan;
            snapshot.Food = Need(pawn, NeedKind.Food);
            snapshot.Rest = Need(pawn, NeedKind.Rest);
            snapshot.Recreation = Need(pawn, NeedKind.Recreation);
            snapshot.Mood = Need(pawn, NeedKind.Mood);
            snapshot.Health = ReadHealth(pawn);
            return snapshot;
        }

        private static TraitFact[] ReadTraits(Pawn pawn)
        {
            List<Trait> traits = pawn.story == null || pawn.story.traits == null
                ? null
                : pawn.story.traits.allTraits;
            if (traits == null || traits.Count == 0)
            {
                return new TraitFact[0];
            }

            List<TraitFact> facts = new List<TraitFact>(traits.Count);
            foreach (Trait trait in traits)
            {
                if (trait == null || trait.def == null)
                {
                    continue;
                }

                facts.Add(new TraitFact
                {
                    DefName = trait.def.defName,
                    Degree = trait.Degree,
                    Label = trait.Label,
                });
            }

            return facts.ToArray();
        }

        private static SkillFact[] ReadSkills(Pawn pawn)
        {
            List<SkillRecord> skills = pawn.skills == null ? null : pawn.skills.skills;
            if (skills == null || skills.Count == 0)
            {
                return new SkillFact[0];
            }

            List<SkillFact> facts = new List<SkillFact>(skills.Count);
            foreach (SkillRecord skill in skills)
            {
                if (skill == null || skill.def == null)
                {
                    continue;
                }

                facts.Add(new SkillFact
                {
                    DefName = skill.def.defName,
                    Level = skill.Level,
                });
            }

            return facts.ToArray();
        }

        /// <summary>
        /// The background title, falling back to the def label when a backstory carries no title of
        /// its own. Both are game text; which one is used is a fact about the def, not a choice made
        /// here, and it is the same on every read.
        /// </summary>
        private static string Backstory(BackstoryDef backstory)
        {
            if (backstory == null)
            {
                return string.Empty;
            }

            return string.IsNullOrEmpty(backstory.title) ? backstory.label : backstory.title;
        }

        private enum NeedKind
        {
            Food,
            Rest,
            Recreation,
            Mood,
        }

        private static float Need(Pawn pawn, NeedKind kind)
        {
            if (pawn.needs == null)
            {
                return 0f;
            }

            Need need;
            switch (kind)
            {
                case NeedKind.Food:
                    need = pawn.needs.food;
                    break;
                case NeedKind.Rest:
                    need = pawn.needs.rest;
                    break;
                case NeedKind.Recreation:
                    // RimWorld calls this need "joy" internally and labels it recreation in game.
                    // The observation uses the player-facing name; this is where the two meet.
                    need = pawn.needs.joy;
                    break;
                default:
                    need = pawn.needs.mood;
                    break;
            }

            return need == null ? 0f : need.CurLevelPercentage;
        }

        /// <summary>
        /// Downed is the game's own state and wins over injured. Injured means there is at least one
        /// injury hediff: permanent conditions such as a missing part or an addiction are part of
        /// who the colonist now is, not a current injury.
        /// </summary>
        private static string ReadHealth(Pawn pawn)
        {
            if (pawn.Downed)
            {
                return RimWorldHealth.Downed;
            }

            List<Hediff> hediffs = pawn.health == null || pawn.health.hediffSet == null
                ? null
                : pawn.health.hediffSet.hediffs;
            if (hediffs != null)
            {
                foreach (Hediff hediff in hediffs)
                {
                    if (hediff is Hediff_Injury)
                    {
                        return RimWorldHealth.Injured;
                    }
                }
            }

            return RimWorldHealth.Normal;
        }
    }
}
