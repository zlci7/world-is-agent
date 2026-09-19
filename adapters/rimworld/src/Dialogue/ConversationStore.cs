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
