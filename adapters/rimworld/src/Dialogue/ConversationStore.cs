using System;
using System.Collections.Generic;

namespace Wia.RimWorld.Dialogue
{
    /// <summary>One conversation between the player and one colonist.</summary>
    internal sealed class Conversation
    {
        public string ConversationId;
        public string WorldId;
        public string EntityId;

        /// <summary>
        /// True while the turn started by this conversation's latest event has not presented a line.
        ///
        /// The Runtime lets a model settle a turn with no tool call at all, and that turn still
        /// completes. Whether the conversation should outlive a completed turn depends on whether the
        /// player was actually shown anything, so that fact is tracked per turn rather than inferred
        /// from the conversation merely being open.
        /// </summary>
        public bool AwaitingPresentation;
    }

    /// <summary>
    /// The adapter's private conversation bookkeeping.
    ///
    /// Two directions have to meet here. Outbound, the adapter invents a conversation id when the
    /// player opens the dialogue and keeps it for as long as the exchange lasts. Inbound, an
    /// ActionRequest carries only the source event id - the model never sees the conversation id,
    /// which is why this component exists at all - so every outbound event is indexed by the event
    /// id the Runtime will quote back.
    ///
    /// Touched from the main thread (the gizmo and the window) and from the stream thread (the
    /// ActionRequest handler), so every entry point takes the lock.
    /// </summary>
    internal sealed class ConversationStore
    {
        /// <summary>
        /// Cap on unresolved event bindings. A turn that fails before it reaches present_dialogue
        /// never quotes its event id back, so without a bound this map would grow for as long as the
        /// process runs. Oldest first is the right eviction: the Runtime resolves a fresh event.
        /// </summary>
        public const int MaxEventBindings = 64;

        private readonly object gate = new object();
        private readonly Dictionary<string, Conversation> byEventId = new Dictionary<string, Conversation>(StringComparer.Ordinal);
        private readonly Queue<string> eventOrder = new Queue<string>();
        private readonly Dictionary<string, Conversation> open = new Dictionary<string, Conversation>(StringComparer.Ordinal);

        /// <summary>
        /// Starts a conversation and binds the event that opened it. The id is generated here rather
        /// than by the model: it is correlation state, not something the model should hold or choose.
        /// </summary>
        public Conversation Begin(string worldId, string entityId, string eventId)
        {
            Conversation conversation = new Conversation
            {
                ConversationId = "conv-" + Guid.NewGuid().ToString("N"),
                WorldId = worldId,
                EntityId = entityId,
                AwaitingPresentation = true,
            };

            lock (this.gate)
            {
                this.open[conversation.ConversationId] = conversation;
                this.BindLocked(conversation, eventId);
            }

            return conversation;
        }

        /// <summary>
        /// Binds a follow-up event to the conversation it belongs to. The player's reply keeps the
        /// same conversation id, so the whole exchange stays one conversation on the Runtime side.
        /// </summary>
        public void Bind(string eventId, Conversation conversation)
        {
            if (conversation == null)
            {
                throw new ArgumentNullException(nameof(conversation));
            }

            lock (this.gate)
            {
                // A follow-up event starts a new turn for this conversation, so it is again waiting to
                // see whether that turn presents anything.
                conversation.AwaitingPresentation = true;
                this.open[conversation.ConversationId] = conversation;
                this.BindLocked(conversation, eventId);
            }
        }

        /// <summary>Resolves the event id an ActionRequest quoted back to the conversation it opened.</summary>
        public bool TryResolve(string eventId, out Conversation conversation)
        {
            conversation = null;
            if (string.IsNullOrEmpty(eventId))
            {
                return false;
            }

            lock (this.gate)
            {
                return this.byEventId.TryGetValue(eventId, out conversation);
            }
        }

        public bool IsOpen(string conversationId)
        {
            if (string.IsNullOrEmpty(conversationId))
            {
                return false;
            }

            lock (this.gate)
            {
                return this.open.ContainsKey(conversationId);
            }
        }

        /// <summary>
        /// The open conversation for one entity, if there is one.
        ///
        /// Used to keep a colonist from stacking up conversations: each one starts a turn, the
        /// Runtime runs one turn per entity lane at a time, and a burst of clicks would queue or drop
        /// the extras while the player sees windows appear from earlier clicks. The open map is
        /// bounded by this check, since at most one entry per entity can exist.
        /// </summary>
        public bool TryGetOpenForEntity(string entityId, out Conversation conversation)
        {
            conversation = null;
            if (string.IsNullOrEmpty(entityId))
            {
                return false;
            }

            lock (this.gate)
            {
                foreach (Conversation candidate in this.open.Values)
                {
                    if (string.Equals(candidate.EntityId, entityId, StringComparison.Ordinal))
                    {
                        conversation = candidate;
                        return true;
                    }
                }
            }

            return false;
        }

        /// <summary>
        /// Forgets a conversation. Called when the model ends it and when the player closes the
        /// window without replying; neither case produces another event.
        /// </summary>
        public void Close(string conversationId)
        {
            if (string.IsNullOrEmpty(conversationId))
            {
                return;
            }

            lock (this.gate)
            {
                this.open.Remove(conversationId);
            }
        }

        /// <summary>
        /// Records that the conversation's current turn has shown a line. Called when
        /// present_dialogue succeeds.
        /// </summary>
        public void MarkPresented(string conversationId)
        {
            if (string.IsNullOrEmpty(conversationId))
            {
                return;
            }

            lock (this.gate)
            {
                Conversation conversation;
                if (this.open.TryGetValue(conversationId, out conversation))
                {
                    conversation.AwaitingPresentation = false;
                }
            }
        }

        /// <summary>
        /// Settles a turn that the Runtime reported as completed, and reports whether that ended the
        /// conversation.
        ///
        /// A turn can complete successfully without the model calling any tool. When that happens
        /// there is no window and no reply is coming, so keeping the conversation open would leave the
        /// colonist answering "already talking" forever with nothing for the player to close. A turn
        /// that did present something is left open: the window is up and the reply is what starts the
        /// next turn.
        /// </summary>
        public bool CompleteTurn(string eventId)
        {
            Conversation conversation;
            if (!this.TryResolve(eventId, out conversation))
            {
                return false;
            }

            lock (this.gate)
            {
                Conversation tracked;
                if (!this.open.TryGetValue(conversation.ConversationId, out tracked) || !tracked.AwaitingPresentation)
                {
                    return false;
                }

                this.open.Remove(conversation.ConversationId);
                return true;
            }
        }

        private void BindLocked(Conversation conversation, string eventId)
        {
            if (string.IsNullOrEmpty(eventId))
            {
                return;
            }

            if (!this.byEventId.ContainsKey(eventId))
            {
                this.eventOrder.Enqueue(eventId);
            }

            this.byEventId[eventId] = conversation;

            while (this.eventOrder.Count > MaxEventBindings)
            {
                this.byEventId.Remove(this.eventOrder.Dequeue());
            }
        }
    }
}
