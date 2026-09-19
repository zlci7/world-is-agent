using System;
using System.Collections.Generic;
using System.Threading;
using GameAgent.Protocol.V1Alpha2;
using Google.Protobuf.WellKnownTypes;

namespace Wia.RimWorld.Runtime
{
    /// <summary>
    /// Builds every protocol message this adapter sends.
    ///
    /// The event names, payload fields, ContextFact shape and the present_dialogue declaration are
    /// deliberately the same ones the Stardew adapter uses for the same semantics. That is not
    /// copy-paste convenience: two adapters for two unrelated games agreeing on a contract is what
    /// makes it a contract, and it is why the Runtime needs no game-specific branch to understand
    /// either one. What differs is only the source string and the entity types.
    /// </summary>
    internal static class ProtocolMapper
    {
        /// <summary>The player is a single local entity; there is no player pawn in RimWorld.</summary>
        public const string PlayerEntityId = "player:local";

        public const string PlayerEntityType = "player";
        public const string ColonistEntityType = "pawn";
        public const string ColonistDefinitionId = "archetype:colonist";

        /// <summary>Distinguishes this adapter's events from the Stardew adapter's.</summary>
        public const string Source = "rimworld-mod";

        public const string Trigger = "wia_gizmo";

        public const string EventPlayerInteractedWithNpc = "player_interacted_with_npc";
        public const string EventPlayerSaidToNpc = "player_said_to_npc";

        public const int MaxDialogueTextChars = 240;
        public const int MaxReplyOptions = 3;
        public const int MaxReplyOptionChars = 80;

        private const string PresentDialogueInputSchemaJson =
            "{\"type\":\"object\",\"properties\":{\"text\":{\"type\":\"string\",\"maxLength\":240},\"reply_options\":{\"type\":\"array\",\"maxItems\":3,\"items\":{\"type\":\"string\",\"maxLength\":80},\"description\":\"Exactly three distinct player replies for continuing dialogue; empty only for ending dialogue.\"},\"allow_free_text\":{\"type\":\"boolean\",\"default\":true,\"description\":\"True or omitted for continuing dialogue; explicit false only for ending dialogue.\"}},\"required\":[\"text\",\"reply_options\"],\"additionalProperties\":false}";

        private const string PresentDialogueDescription =
            "Displays NPC dialogue using exactly one of two forms. For continuing dialogue, provide exactly three distinct player-authored reply options and set allow_free_text=true or omit it because true is the default. For ending dialogue, provide reply_options=[] and allow_free_text=false. It must be the only tool call in its model response. After it succeeds, the current turn ends; wait for player_said_to_npc before continuing that conversation.";

        private static long messageSequence;

        public static string NewMessageId(string prefix)
        {
            long sequence = Interlocked.Increment(ref messageSequence);
            return prefix + "-" + sequence.ToString() + "-" + Guid.NewGuid().ToString("N");
        }

        /// <summary>
        /// Tick-only game time: <c>tick</c> and nothing else. The protocol's other GameTime fields
        /// are optional and this adapter does not invent a calendar mapping for them, so a reader can
        /// tell "tick-only" from "calendar with a missing tick" rather than guessing.
        /// </summary>
        public static GameTime TickOnly(long tick)
        {
            return new GameTime { Tick = tick };
        }

        public static EntityRef BuildPlayerEntity(string displayName)
        {
            return new EntityRef
            {
                EntityId = PlayerEntityId,
                EntityType = PlayerEntityType,
                DisplayName = displayName ?? string.Empty,
            };
        }

        public static EntityRef BuildColonistEntity(string entityId, string displayName)
        {
            return new EntityRef
            {
                EntityId = RequireNonEmpty(entityId, "entity_id"),
                EntityType = ColonistEntityType,
                DisplayName = displayName ?? string.Empty,
                DefinitionId = ColonistDefinitionId,
            };
        }

        public static Observation BuildObservation(
            string entityId,
            string worldId,
            long tick,
            ulong revision,
            Struct rimWorldState)
        {
            return new Observation
            {
                EntityId = RequireNonEmpty(entityId, "entity_id"),
                Revision = revision,
                GameTime = TickOnly(tick),
                WorldId = RequireNonEmpty(worldId, "world_id"),
                State = new Struct
                {
                    Fields = { ["rimworld"] = Value.ForStruct(rimWorldState) },
                },
            };
        }

        /// <summary>
        /// The one capability this adapter publishes. The declaration is identical to the Stardew
        /// adapter's, down to the description and the two tool-policy keys: the same policy key
        /// expressing the same tool semantics in two unrelated games is the evidence that those keys
        /// are not specific to one adapter.
        /// </summary>
        public static CapabilityList BuildCapabilityList()
        {
            Struct toolPolicy = new Struct();
            toolPolicy.Fields.Add("exclusive_per_step", Value.ForBool(true));
            toolPolicy.Fields.Add("settle_after_success", Value.ForBool(true));

            Struct gameagent = new Struct();
            gameagent.Fields.Add("tool_policy", Value.ForStruct(toolPolicy));

            Struct extensions = new Struct();
            extensions.Fields.Add("gameagent", Value.ForStruct(gameagent));

            CapabilityList list = new CapabilityList { Revision = 1 };
            list.Capabilities.Add(new Capability
            {
                Name = "present_dialogue",
                Version = "0.1.0",
                Description = PresentDialogueDescription,
                InputSchemaJson = PresentDialogueInputSchemaJson,
                ExecutionMode = ExecutionMode.Sync,
                ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                Extensions = extensions,
            });
            return list;
        }

        public static AdapterMessage BuildCapabilityListMessage(string correlationId, CapabilityList capabilities)
        {
            return new AdapterMessage
            {
                MessageId = NewMessageId("capabilities_msg"),
                CorrelationId = correlationId,
                Capabilities = capabilities,
            };
        }

        public static AdapterMessage BuildObservationMessage(string correlationId, Observation observation)
        {
            return new AdapterMessage
            {
                MessageId = NewMessageId("observation"),
                CorrelationId = correlationId,
                Observation = observation,
            };
        }

        public static AdapterMessage BuildEventMessage(GameEvent gameEvent)
        {
            return new AdapterMessage
            {
                MessageId = NewMessageId("event"),
                Event = gameEvent,
            };
        }

        public static AdapterMessage BuildActionResultMessage(ActionResult result)
        {
            return new AdapterMessage
            {
                MessageId = NewMessageId("action_result"),
                ActionResult = result,
            };
        }

        public static AdapterMessage BuildErrorMessage(string correlationId, string code, string message)
        {
            return new AdapterMessage
            {
                MessageId = NewMessageId("error"),
                CorrelationId = correlationId,
                Error = new Error { Code = code, Message = message },
            };
        }

        public static GameEvent BuildPlayerInteractedWithNpcEvent(
            string eventId,
            string colonistEntityId,
            string colonistDisplayName,
            string playerDisplayName,
            string conversationId,
            ulong sequence,
            string worldId,
            long tick)
        {
            GameEvent gameEvent = new GameEvent
            {
                EventId = RequireNonEmpty(eventId, "event_id"),
                EventType = EventPlayerInteractedWithNpc,
                GameTime = TickOnly(tick),
                Sequence = sequence,
                WorldId = RequireNonEmpty(worldId, "world_id"),
                TargetEntityId = colonistEntityId,
                Payload = new Struct
                {
                    Fields =
                    {
                        ["conversation_id"] = Value.ForString(RequireNonEmpty(conversationId, "conversation_id")),
                        ["trigger"] = Value.ForString(Trigger),
                        ["source"] = Value.ForString(Source),
                    },
                },
            };

            gameEvent.Entities.Add(BuildPlayerEntity(playerDisplayName));
            gameEvent.Entities.Add(BuildColonistEntity(colonistEntityId, colonistDisplayName));
            return gameEvent;
        }

        public static GameEvent BuildPlayerSaidToNpcEvent(
            string eventId,
            string colonistEntityId,
            string colonistDisplayName,
            string playerDisplayName,
            string conversationId,
            string inputKind,
            string text,
            int? selectedOptionIndex,
            ulong sequence,
            string worldId,
            long tick)
        {
            string normalizedKind = RequireNonEmpty(inputKind, "input_kind");
            if (normalizedKind != "option" && normalizedKind != "free_text")
            {
                throw new ArgumentException("input_kind must be option or free_text", nameof(inputKind));
            }

            if (normalizedKind == "option" && !selectedOptionIndex.HasValue)
            {
                throw new ArgumentException("selected_option_index is required for option input", nameof(selectedOptionIndex));
            }

            if (normalizedKind == "free_text" && selectedOptionIndex.HasValue)
            {
                throw new ArgumentException("selected_option_index must be omitted for free_text input", nameof(selectedOptionIndex));
            }

            string conversation = RequireNonEmpty(conversationId, "conversation_id");
            string normalizedText = RequireBoundedText(text, "text", MaxDialogueTextChars);

            Struct payload = new Struct
            {
                Fields =
                {
                    ["conversation_id"] = Value.ForString(conversation),
                    ["input_kind"] = Value.ForString(normalizedKind),
                    ["text"] = Value.ForString(normalizedText),
                    ["trigger"] = Value.ForString(Trigger),
                    ["source"] = Value.ForString(Source),
                },
            };

            Struct attributes = new Struct
            {
                Fields =
                {
                    ["input_kind"] = Value.ForString(normalizedKind),
                    ["trigger"] = Value.ForString(Trigger),
                },
            };

            if (selectedOptionIndex.HasValue)
            {
                payload.Fields["selected_option_index"] = Value.ForNumber(selectedOptionIndex.Value);
                attributes.Fields["selected_option_index"] = Value.ForNumber(selectedOptionIndex.Value);
            }

            GameEvent gameEvent = new GameEvent
            {
                EventId = RequireNonEmpty(eventId, "event_id"),
                EventType = EventPlayerSaidToNpc,
                GameTime = TickOnly(tick),
                Sequence = sequence,
                WorldId = RequireNonEmpty(worldId, "world_id"),
                TargetEntityId = colonistEntityId,
                Payload = payload,
            };

            gameEvent.Entities.Add(BuildPlayerEntity(playerDisplayName));
            gameEvent.Entities.Add(BuildColonistEntity(colonistEntityId, colonistDisplayName));
            gameEvent.ContextFacts.Add(new ContextFact
            {
                Kind = "utterance",
                ActorEntityId = PlayerEntityId,
                TargetEntityId = colonistEntityId,
                ScopeId = conversation,
                Text = normalizedText,
                Attributes = attributes,
            });

            return gameEvent;
        }

        /// <summary>
        /// SUCCEEDED means the line was shown, not that the player answered. Waiting for a reply is
        /// not this action's job, which is why closing the window is not a failure and leaves no
        /// action hanging.
        /// </summary>
        public static ActionResult BuildPresentDialogueSucceeded(
            ActionRequest request,
            string conversationId,
            string displayedText,
            int replyOptionsCount,
            bool allowFreeText)
        {
            return new ActionResult
            {
                ActionId = request.ActionId,
                Status = ActionStatus.Succeeded,
                Output = new Struct
                {
                    Fields =
                    {
                        ["conversation_id"] = Value.ForString(conversationId),
                        ["displayed_text"] = Value.ForString(displayedText),
                        ["reply_options_count"] = Value.ForNumber(replyOptionsCount),
                        ["allow_free_text"] = Value.ForBool(allowFreeText),
                    },
                },
            };
        }

        public static ActionResult BuildFailed(ActionRequest request, string code, string message)
        {
            return new ActionResult
            {
                ActionId = request.ActionId,
                Status = ActionStatus.Failed,
                Error = new Error { Code = code, Message = message },
            };
        }

        public static ActionResult BuildRejected(ActionRequest request, string code, string message)
        {
            return new ActionResult
            {
                ActionId = request.ActionId,
                Status = ActionStatus.Rejected,
                Error = new Error { Code = code, Message = message },
            };
        }

        public static string RequireNonEmpty(string value, string field)
        {
            if (string.IsNullOrWhiteSpace(value))
            {
                throw new ArgumentException(field + " is required");
            }

            return value;
        }

        public static string RequireBoundedText(string value, string field, int maxChars)
        {
            if (string.IsNullOrWhiteSpace(value))
            {
                throw new ArgumentException(field + " is required");
            }

            if (value.Length > maxChars)
            {
                throw new ArgumentException(field + " exceeds " + maxChars.ToString() + " characters");
            }

            return value;
        }

        /// <summary>
        /// Reads a required string field out of a model-supplied Struct. Missing and wrong-typed
        /// both fail here rather than reaching the game as an empty value.
        /// </summary>
        public static string ReadString(Struct arguments, string field, bool required)
        {
            Value value;
            if (arguments == null || !arguments.Fields.TryGetValue(field, out value))
            {
                if (required)
                {
                    throw new ArgumentException(field + " is required");
                }

                return null;
            }

            if (value.KindCase != Value.KindOneofCase.StringValue)
            {
                throw new ArgumentException(field + " must be a string");
            }

            return value.StringValue;
        }

        public static bool ReadBool(Struct arguments, string field, bool fallback)
        {
            Value value;
            if (arguments == null || !arguments.Fields.TryGetValue(field, out value))
            {
                return fallback;
            }

            if (value.KindCase != Value.KindOneofCase.BoolValue)
            {
                throw new ArgumentException(field + " must be a boolean");
            }

            return value.BoolValue;
        }

        public static IReadOnlyList<string> ReadStringList(Struct arguments, string field, bool required)
        {
            Value value;
            if (arguments == null || !arguments.Fields.TryGetValue(field, out value))
            {
                if (required)
                {
                    throw new ArgumentException(field + " is required");
                }

                return new string[0];
            }

            if (value.KindCase != Value.KindOneofCase.ListValue)
            {
                throw new ArgumentException(field + " must be an array");
            }

            List<string> values = new List<string>();
            foreach (Value item in value.ListValue.Values)
            {
                if (item.KindCase != Value.KindOneofCase.StringValue)
                {
                    throw new ArgumentException(field + " must contain only strings");
                }

                values.Add(item.StringValue);
            }

            return values;
        }
    }
}
