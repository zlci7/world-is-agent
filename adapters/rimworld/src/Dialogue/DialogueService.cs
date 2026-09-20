using System;
using System.Collections.Generic;
using GameAgent.Protocol.V1Alpha2;
using RimWorld;
using Verse;
using Wia.RimWorld.Identity;
using Wia.RimWorld.Runtime;
using Wia.RimWorld.Threading;

namespace Wia.RimWorld.Dialogue
{
    /// <summary>
    /// The player-facing dialogue entry point and the executor behind present_dialogue.
    ///
    /// Everything here runs on the main thread except <see cref="TrySend"/>'s callback into the
    /// transport, which must not block this thread.
    ///
    /// The one rule that shapes the whole class: showing the line is the action, waiting for the
    /// reply is not. So an action succeeds the moment the window is up, the player's reply is a new
    /// event that starts a new turn, and closing the window is a cleanup rather than a failure.
    /// </summary>
    internal sealed class DialogueService
    {
        public const string CapabilityName = "present_dialogue";

        private readonly ConversationStore conversations = new ConversationStore();
        private readonly Func<GameEvent, bool> send;
        private readonly MainThreadPump pump;
        private readonly Dictionary<string, WiaDialogueWindow> windows =
            new Dictionary<string, WiaDialogueWindow>(StringComparer.Ordinal);

        private long sequence;

        public DialogueService(MainThreadPump pump, Func<GameEvent, bool> send)
        {
            this.pump = pump ?? throw new ArgumentNullException(nameof(pump));
            this.send = send ?? throw new ArgumentNullException(nameof(send));
        }

        /// <summary>
        /// Starts a conversation from the gizmo. Returns false with a reason the caller can show the
        /// player: a click that does nothing is the worst possible answer, because the player cannot
        /// tell a disconnected Runtime from a broken mod.
        /// </summary>
        public bool BeginConversation(Pawn pawn, out string failure)
        {
            failure = string.Empty;

            if (pawn == null)
            {
                failure = "没有可对话的目标";
                return false;
            }

            string reason;
            if (!EligibleColonist.Is(pawn, out reason))
            {
                failure = "该单位不是 WIA 的对话对象";
                return false;
            }

            long tick;
            if (!GameClock.TryReadTick(out tick))
            {
                failure = "没有已加载的游戏";
                return false;
            }

            string worldId = WorldIdentityComponent.CurrentWorldId();
            if (string.IsNullOrEmpty(worldId))
            {
                failure = "当前世界还没有 WIA 身份";
                return false;
            }

            string entityId = EntityId.For(pawn);

            Conversation open;
            if (this.conversations.TryGetOpenForEntity(entityId, out open))
            {
                // One conversation per colonist. A second click would start a second turn on the same
                // entity lane, where the Runtime runs one turn at a time, so the extras queue or drop
                // while the player sees whichever window an earlier click produced and cannot tell
                // which click it belongs to.
                failure = "这位殖民者已经在对话中";
                return false;
            }

            string eventId = ProtocolMapper.NewMessageId("event");
            Conversation conversation = this.conversations.Begin(worldId, entityId, eventId);

            GameEvent gameEvent = ProtocolMapper.BuildPlayerInteractedWithNpcEvent(
                eventId,
                entityId,
                pawn.LabelShort,
                PlayerDisplayName(),
                conversation.ConversationId,
                this.NextSequence(),
                worldId,
                tick);

            if (!this.send(gameEvent))
            {
                // Nothing was sent, so nothing will ever quote this event back. Dropping the
                // conversation keeps a failed click from leaving state behind.
                this.conversations.Close(conversation.ConversationId);
                failure = "WIA Runtime 未连接";
                return false;
            }

            return true;
        }

        /// <summary>
        /// Executes one present_dialogue request. Main thread only.
        ///
        /// Every rejection here is a REJECTED ActionResult rather than an exception, so the model
        /// gets the reason in its next step instead of the turn dying.
        /// </summary>
        public ActionResult Execute(ActionRequest request)
        {
            if (request == null)
            {
                throw new ArgumentNullException(nameof(request));
            }

            if (!string.Equals(request.Capability, CapabilityName, StringComparison.Ordinal))
            {
                return ProtocolMapper.BuildRejected(
                    request, "unknown_capability", "this adapter does not implement " + request.Capability);
            }

            Conversation conversation;
            if (!this.conversations.TryResolve(request.SourceEventId, out conversation))
            {
                // The conversation id is correlation state the model never sees, so the only way back
                // to it is the event that started this turn. Without that link there is nothing to
                // attach the window to.
                return ProtocolMapper.BuildRejected(
                    request, "unknown_source_event", "no conversation is associated with the source event");
            }

            // The event binding deliberately outlives the conversation so that a late or duplicated
            // ActionRequest gets an explicit rejection instead of leaving the Runtime waiting. That
            // only works if the answer is checked here - resolving the event is not the same as the
            // conversation still being live.
            if (!this.conversations.IsOpen(conversation.ConversationId))
            {
                return ProtocolMapper.BuildRejected(
                    request, "conversation_closed", "the conversation has already ended");
            }

            if (!string.Equals(conversation.EntityId, request.EntityId, StringComparison.Ordinal))
            {
                return ProtocolMapper.BuildRejected(
                    request, "entity_mismatch", "the source event belongs to a different entity");
            }

            string worldId = WorldIdentityComponent.CurrentWorldId();
            if (string.IsNullOrEmpty(worldId) ||
                !string.Equals(worldId, conversation.WorldId, StringComparison.Ordinal) ||
                !string.Equals(worldId, request.WorldId, StringComparison.Ordinal))
            {
                return ProtocolMapper.BuildRejected(
                    request, "world_mismatch", "the conversation does not belong to the loaded world");
            }

            Pawn pawn = FindPawn(conversation.EntityId);
            if (pawn == null)
            {
                return ProtocolMapper.BuildRejected(
                    request, "unknown_entity", "no living pawn matches " + conversation.EntityId);
            }

            PresentDialogueRequest input;
            string message;
            if (!PresentDialogueParser.TryParse(request.Arguments, out input, out message))
            {
                return ProtocolMapper.BuildRejected(request, "invalid_action_arguments", message);
            }

            this.Present(conversation, pawn, input);

            // The player has now been shown something, so a completed turn has to leave this
            // conversation open instead of settling it as though nothing had happened.
            this.conversations.MarkPresented(conversation.ConversationId);

            if (input.EndsConversation)
            {
                // The model chose the ending form, so this conversation is over before the window is
                // even closed. Closing it here rather than on window close keeps the two forms
                // distinguishable in the store.
                this.conversations.Close(conversation.ConversationId);
            }

            return ProtocolMapper.BuildPresentDialogueSucceeded(
                request,
                conversation.ConversationId,
                input.Text,
                input.ReplyOptions.Length,
                input.AllowFreeText);
        }

        /// <summary>
        /// Forgets a conversation whose event did not survive, and tells the player.
        ///
        /// Called from the stream thread when the Runtime rejects an event, when a turn ends without
        /// completing, or when a detached write fails. Closing the store entry is thread-safe on its
        /// own; the window and the message touch the game, so they go through the pump. Without this
        /// a rejected click would leave the player talking into a conversation the Runtime never
        /// accepted, which is the silent failure the send path already refuses to produce.
        /// </summary>
        public void AbandonEvent(string eventId, string reason)
        {
            Conversation conversation;
            if (!this.conversations.TryResolve(eventId, out conversation))
            {
                return;
            }

            this.conversations.Close(conversation.ConversationId);

            this.pump.Enqueue(() =>
            {
                WiaDialogueWindow window;
                if (this.windows.TryGetValue(conversation.ConversationId, out window))
                {
                    this.windows.Remove(conversation.ConversationId);
                    window.Close();
                }

                Messages.Message("WIA 对话没有开始：" + reason, MessageTypeDefOf.RejectInput, false);
            });
        }

        /// <summary>
        /// Settles a turn the Runtime reported as completed.
        ///
        /// A completed turn is normally the one that put a window on screen, and that conversation has
        /// to stay open because the player's reply is what starts the next turn. A model may also
        /// settle with no tool call at all, though, and that turn completes just as successfully; there
        /// is no window then and no reply is coming, so the conversation is closed. Without this the
        /// colonist would answer "already talking" for the rest of the session with nothing to close.
        ///
        /// Called from the stream thread.
        /// </summary>
        public void CompleteTurn(string eventId)
        {
            if (!this.conversations.CompleteTurn(eventId))
            {
                return;
            }

            this.pump.Enqueue(() => Messages.Message(
                "WIA：这一回合没有产生对话，已结束",
                MessageTypeDefOf.RejectInput,
                false));
        }

        private void Present(Conversation conversation, Pawn pawn, PresentDialogueRequest input)
        {
            WiaDialogueWindow existing;
            if (this.windows.TryGetValue(conversation.ConversationId, out existing))
            {
                existing.Close();
                this.windows.Remove(conversation.ConversationId);
            }

            WiaDialogueWindow window = new WiaDialogueWindow(
                pawn.LabelShort,
                input.Text,
                input.ReplyOptions,
                input.AllowFreeText,
                onSubmit: (kind, text, optionIndex) => this.SubmitReply(conversation, kind, text, optionIndex),
                onAbandoned: () =>
                {
                    this.windows.Remove(conversation.ConversationId);
                    this.conversations.Close(conversation.ConversationId);
                });

            this.windows[conversation.ConversationId] = window;
            Find.WindowStack.Add(window);
        }

        private void SubmitReply(Conversation conversation, string inputKind, string text, int? optionIndex)
        {
            this.windows.Remove(conversation.ConversationId);

            long tick;
            if (!GameClock.TryReadTick(out tick))
            {
                AdapterLog.Warn("dropping a dialogue reply: no game is loaded");
                return;
            }

            string worldId = WorldIdentityComponent.CurrentWorldId();
            if (string.IsNullOrEmpty(worldId))
            {
                AdapterLog.Warn("dropping a dialogue reply: the world has no WIA identity");
                return;
            }

            Pawn pawn = FindPawn(conversation.EntityId);
            if (pawn == null)
            {
                AdapterLog.Warn("dropping a dialogue reply: " + conversation.EntityId + " is gone");
                return;
            }

            string eventId = ProtocolMapper.NewMessageId("event");
            GameEvent gameEvent = ProtocolMapper.BuildPlayerSaidToNpcEvent(
                eventId,
                conversation.EntityId,
                pawn.LabelShort,
                PlayerDisplayName(),
                conversation.ConversationId,
                inputKind,
                text,
                optionIndex,
                this.NextSequence(),
                worldId,
                tick);

            // Bound before sending: the turn this starts can come back with a present_dialogue
            // before this method returns.
            this.conversations.Bind(eventId, conversation);

            if (!this.send(gameEvent))
            {
                // The reply was never handed to a live session, so the conversation it belonged to is
                // over as far as the Runtime is concerned. Leaving it open would make the next click
                // on this colonist answer "already talking".
                this.conversations.Close(conversation.ConversationId);
                this.windows.Remove(conversation.ConversationId);
                AdapterLog.Warn("dialogue reply could not be sent: the Runtime is not connected");
                Messages.Message("WIA Runtime 未连接，回复没有发送", MessageTypeDefOf.RejectInput, false);
            }
        }

        /// <summary>
        /// The player side of the conversation. RimWorld has no player pawn, so the colony's own
        /// name is the most truthful display name available.
        /// </summary>
        private static string PlayerDisplayName()
        {
            Faction player = Faction.OfPlayer;
            return player == null ? "player" : player.Name;
        }

        private static Pawn FindPawn(string entityId)
        {
            foreach (Pawn pawn in PawnsFinder.AllMapsCaravansAndTravellingTransporters_Alive)
            {
                if (pawn != null && string.Equals(EntityId.For(pawn), entityId, StringComparison.Ordinal))
                {
                    return pawn;
                }
            }

            return null;
        }

        private ulong NextSequence()
        {
            this.sequence++;
            return (ulong)this.sequence;
        }
    }
}
