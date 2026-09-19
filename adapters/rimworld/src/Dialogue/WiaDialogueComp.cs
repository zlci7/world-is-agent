using System.Collections.Generic;
using RimWorld;
using Verse;
using Wia.RimWorld.Identity;
using Wia.RimWorld.Runtime;

namespace Wia.RimWorld.Dialogue
{
    /// <summary>
    /// Properties for <see cref="WiaDialogueComp"/>. RimWorld resolves a ThingDef's comps through
    /// CompProperties, so the comp class alone in the XML would not be instantiated.
    /// </summary>
    public class WiaDialogueCompProperties : CompProperties
    {
        public WiaDialogueCompProperties()
        {
            this.compClass = typeof(WiaDialogueComp);
        }
    }

    /// <summary>
    /// The WIA entry point: a gizmo on a colonist that starts a conversation with the Runtime.
    ///
    /// It is attached to the vanilla Human def by an XML patch, and Human is the parent of every
    /// humanlike def, so raiders, prisoners and guests receive this comp too. That is why the
    /// eligibility check below is not an optimisation: without it the WIA entry point would appear
    /// on the enemy standing in the middle of the colony.
    ///
    /// This version does not touch RimWorld's AI at all - no job, no draft, no behaviour change. It
    /// only adds an entry point, which is the most conservative ownership boundary available.
    /// </summary>
    public class WiaDialogueComp : ThingComp
    {
        public override IEnumerable<Gizmo> CompGetGizmosExtra()
        {
            Pawn pawn = this.parent as Pawn;
            if (!EligibleColonist.Is(pawn))
            {
                yield break;
            }

            RuntimeConnection connection = AdapterStartup.Connection;
            DialogueCommand command = new DialogueCommand
            {
                defaultLabel = "WIA 对话",
                defaultDesc = "以这位殖民者的身份与 World Is Agent Runtime 交谈。",
                icon = TexCommand.GatherSpotActive,
                action = () => OpenDialogue(pawn, connection),
            };

            // Disabled rather than hidden, with the reason attached: a gizmo that silently does
            // nothing is indistinguishable from a broken mod.
            if (connection == null || !connection.IsSessionActive)
            {
                command.MarkUnavailable("WIA Runtime 未连接");
            }

            yield return command;
        }

        /// <summary>
        /// Command.disabled is protected, so making the gizmo visibly unavailable needs a subclass
        /// rather than a field assignment. Kept nested and private: this is not adapter surface.
        /// </summary>
        private sealed class DialogueCommand : Command_Action
        {
            public void MarkUnavailable(string reason)
            {
                this.disabled = true;
                this.disabledReason = reason;
            }
        }

        private static void OpenDialogue(Pawn pawn, RuntimeConnection connection)
        {
            if (connection == null)
            {
                Messages.Message("WIA 适配器未启动", MessageTypeDefOf.RejectInput, false);
                return;
            }

            string failure;
            if (!connection.Dialogue.BeginConversation(pawn, out failure))
            {
                Messages.Message(failure, MessageTypeDefOf.RejectInput, false);
            }
        }
    }
}
