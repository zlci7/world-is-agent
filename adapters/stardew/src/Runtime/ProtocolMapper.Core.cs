using System;
using System.Threading;
using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Capabilities;
using GameAgent.Stardew.Dialogue;
using GameAgent.Stardew.State;
using Google.Protobuf.WellKnownTypes;

namespace GameAgent.Stardew.Runtime;

public static partial class ProtocolMapper
{
    public const int MaxDialogueTextChars = 240;
    public const int MaxReplyOptions = 3;
    public const int MaxReplyOptionChars = 80;

    private const string NpcEntityPrefix = "npc:";
    public const string PlayerEntityId = "player:local";
    private static long messageSequence;

    public static string ToNpcEntityId(string npcName)
    {
        return $"{NpcEntityPrefix}{npcName}";
    }

    public static bool TryParseNpcEntityId(string entityId, out string npcName)
    {
        if (entityId.StartsWith(NpcEntityPrefix, StringComparison.Ordinal) && entityId.Length > NpcEntityPrefix.Length)
        {
            npcName = entityId[NpcEntityPrefix.Length..];
            return true;
        }

        npcName = string.Empty;
        return false;
    }

    public static GameEvent BuildPlayerInteractedWithNpcEvent(
        string npcEntityId,
        string npcDisplayName,
        string playerEntityId,
        string playerDisplayName,
        string conversationId,
        string trigger,
        ulong sequence,
        string worldId,
        GameTime gameTime,
        string? eventId = null
    )
    {
        return new GameEvent
        {
            EventId = string.IsNullOrWhiteSpace(eventId) ? NewMessageId("event") : eventId,
            EventType = "player_interacted_with_npc",
            GameTime = gameTime,
            Payload = new Struct
            {
                Fields =
                {
                    ["conversation_id"] = Value.ForString(RequireNonEmpty(conversationId, "conversation_id")),
                    ["trigger"] = Value.ForString(trigger),
                    ["source"] = Value.ForString("stardew-smapi"),
                },
            },
            Sequence = sequence,
            WorldId = worldId,
            TargetEntityId = npcEntityId,
            Entities =
            {
                BuildEntity(playerEntityId, "player", playerDisplayName),
                BuildEntity(npcEntityId, "npc", npcDisplayName),
            },
        };
    }

    public static GameEvent BuildPlayerSaidToNpcEvent(
        string npcEntityId,
        string npcDisplayName,
        string playerEntityId,
        string playerDisplayName,
        string conversationId,
        string inputKind,
        string text,
        int? selectedOptionIndex,
        string trigger,
        ulong sequence,
        string worldId,
        GameTime gameTime,
        string? eventId = null
    )
    {
        string normalizedInputKind = RequireNonEmpty(inputKind, "input_kind");
        if (normalizedInputKind != "option" && normalizedInputKind != "free_text")
            throw new ArgumentException("input_kind must be option or free_text");

        if (normalizedInputKind == "option" && !selectedOptionIndex.HasValue)
            throw new ArgumentException("selected_option_index is required for option input");
        if (normalizedInputKind == "free_text" && selectedOptionIndex.HasValue)
            throw new ArgumentException("selected_option_index must be omitted for free_text input");

        string normalizedConversationId = RequireNonEmpty(conversationId, "conversation_id");
        string normalizedText = RequireBoundedText(text, "text", MaxDialogueTextChars);
        Struct payload = new()
        {
            Fields =
            {
                ["conversation_id"] = Value.ForString(normalizedConversationId),
                ["input_kind"] = Value.ForString(normalizedInputKind),
                ["text"] = Value.ForString(normalizedText),
                ["trigger"] = Value.ForString(trigger),
                ["source"] = Value.ForString("stardew-smapi"),
            },
        };

        if (selectedOptionIndex.HasValue)
            payload.Fields["selected_option_index"] = Value.ForNumber(selectedOptionIndex.Value);

        Struct attributes = new()
        {
            Fields =
            {
                ["input_kind"] = Value.ForString(normalizedInputKind),
                ["trigger"] = Value.ForString(trigger),
            },
        };
        if (selectedOptionIndex.HasValue)
            attributes.Fields["selected_option_index"] = Value.ForNumber(selectedOptionIndex.Value);

        return new GameEvent
        {
            EventId = string.IsNullOrWhiteSpace(eventId) ? NewMessageId("event") : eventId,
            EventType = "player_said_to_npc",
            GameTime = gameTime,
            Payload = payload,
            Sequence = sequence,
            WorldId = worldId,
            TargetEntityId = npcEntityId,
            Entities =
            {
                BuildEntity(playerEntityId, "player", playerDisplayName),
                BuildEntity(npcEntityId, "npc", npcDisplayName),
            },
            ContextFacts =
            {
                new ContextFact
                {
                    Kind = "utterance",
                    ActorEntityId = playerEntityId,
                    TargetEntityId = npcEntityId,
                    ScopeId = normalizedConversationId,
                    Text = normalizedText,
                    Attributes = attributes,
                },
            },
        };
    }

    public static Observation BuildObservation(string entityId, StardewObservation stardewObservation, string worldId, GameTime gameTime)
    {
        return new Observation
        {
            EntityId = entityId,
            Revision = 1,
            GameTime = gameTime,
            WorldId = worldId,
            State = new Struct
            {
                Fields =
                {
                    ["stardew"] = ForStruct(BuildStardewState(stardewObservation)),
                },
            },
            NearbyEntities =
            {
                BuildEntity(PlayerEntityId, "player", stardewObservation.Player.Name),
                stardewObservation.Scene.NearbyNpcs.Select(npc => BuildEntity(npc.EntityId, "npc", npc.Name)),
            },
        };
    }

    private static Struct BuildStardewState(StardewObservation observation)
    {
        Struct stardew = new()
        {
            Fields =
            {
                ["schema_version"] = Value.ForString(observation.SchemaVersion),
                ["time"] = ForStruct(BuildTime(observation.Time)),
                ["weather"] = ForStruct(BuildWeather(observation.Weather)),
                ["agent"] = ForStruct(BuildStateEntity(observation.Agent)),
                ["player"] = ForStruct(BuildPlayer(observation.Player)),
                ["relationship"] = ForStruct(BuildRelationship(observation.Relationship)),
                ["scene"] = ForStruct(BuildScene(observation.Scene)),
            },
        };

        if (observation.Schedule is not null)
            stardew.Fields["schedule"] = ForStruct(BuildSchedule(observation.Schedule));

        if (observation.Conversation is not null)
            stardew.Fields["conversation"] = ForStruct(BuildConversation(observation.Conversation));

        return stardew;
    }

    private static Struct BuildTime(StardewTime time)
    {
        return new Struct
        {
            Fields =
            {
                ["year"] = Value.ForNumber(time.Year),
                ["season"] = Value.ForString(time.Season),
                ["day_of_month"] = Value.ForNumber(time.DayOfMonth),
                ["weekday"] = Value.ForString(time.Weekday),
                ["time_of_day"] = Value.ForNumber(time.TimeOfDay),
                ["time_bucket"] = Value.ForString(time.TimeBucket),
            },
        };
    }

    private static Struct BuildWeather(StardewWeather weather)
    {
        return new Struct
        {
            Fields =
            {
                ["rain"] = Value.ForBool(weather.Rain),
                ["snow"] = Value.ForBool(weather.Snow),
                ["lightning"] = Value.ForBool(weather.Lightning),
                ["green_rain"] = Value.ForBool(weather.GreenRain),
            },
        };
    }

    private static Struct BuildStateEntity(StardewEntity entity)
    {
        return new Struct
        {
            Fields =
            {
                ["entity_id"] = Value.ForString(entity.EntityId),
                ["name"] = Value.ForString(entity.Name),
                ["location"] = Value.ForString(entity.Location),
                ["tile"] = ForStruct(BuildTile(entity.Tile)),
            },
        };
    }

    private static Struct BuildPlayer(StardewPlayer player)
    {
        Struct playerStruct = BuildStateEntity(new StardewEntity(player.EntityId, player.Name, player.Location, player.Tile));
        playerStruct.Fields["gender"] = Value.ForString(player.Gender);
        return playerStruct;
    }

    private static Struct BuildTile(StardewTile tile)
    {
        return new Struct
        {
            Fields =
            {
                ["x"] = Value.ForNumber(tile.X),
                ["y"] = Value.ForNumber(tile.Y),
            },
        };
    }

    private static Struct BuildRelationship(StardewRelationship relationship)
    {
        Struct relationshipStruct = new()
        {
            Fields =
            {
                ["known"] = Value.ForBool(relationship.Known),
                ["is_spouse"] = Value.ForBool(relationship.IsSpouse),
                ["is_roommate"] = Value.ForBool(relationship.IsRoommate),
            },
        };

        if (relationship.Known)
        {
            if (relationship.FriendshipPoints.HasValue)
                relationshipStruct.Fields["friendship_points"] = Value.ForNumber(relationship.FriendshipPoints.Value);

            if (relationship.Hearts.HasValue)
                relationshipStruct.Fields["hearts"] = Value.ForNumber(relationship.Hearts.Value);
        }

        return relationshipStruct;
    }

    private static Struct BuildScene(StardewScene scene)
    {
        return new Struct
        {
            Fields =
            {
                ["trigger"] = Value.ForString(scene.Trigger),
                ["nearby_npcs_total"] = Value.ForNumber(scene.NearbyNpcsTotal),
                ["nearby_npcs_omitted_count"] = Value.ForNumber(scene.NearbyNpcsOmittedCount),
                ["nearby_npcs"] = ForList(scene.NearbyNpcs.Select(npc => ForStruct(BuildStateEntity(npc)))),
            },
        };
    }

    private static Struct BuildSchedule(StardewSchedule schedule)
    {
        Struct scheduleStruct = new()
        {
            Fields =
            {
                ["future_locations"] = ForList(schedule.FutureLocations.Select(Value.ForString)),
            },
        };

        if (!string.IsNullOrWhiteSpace(schedule.Destination))
            scheduleStruct.Fields["destination"] = Value.ForString(schedule.Destination);

        return scheduleStruct;
    }

    private static Struct BuildConversation(StardewConversation conversation)
    {
        return new Struct
        {
            Fields =
            {
                ["conversation_id"] = Value.ForString(conversation.ConversationId),
                ["active"] = Value.ForBool(conversation.Active),
                ["recent_lines_omitted_count"] = Value.ForNumber(conversation.RecentLinesOmittedCount),
                ["recent_lines"] = ForList(conversation.RecentLines.Select(line => ForStruct(BuildConversationLine(line)))),
            },
        };
    }

    private static Struct BuildConversationLine(StardewConversationLine line)
    {
        return new Struct
        {
            Fields =
            {
                ["role"] = Value.ForString(line.Role),
                ["speaker_entity_id"] = Value.ForString(line.SpeakerEntityId),
                ["speaker_name"] = Value.ForString(line.SpeakerName),
                ["text"] = Value.ForString(line.Text),
                ["time_of_day"] = Value.ForNumber(line.TimeOfDay),
            },
        };
    }

    private static Value ForStruct(Struct structure)
    {
        return new Value { StructValue = structure };
    }

    private static Value ForList(IEnumerable<Value> values)
    {
        ListValue list = new();
        list.Values.AddRange(values);
        return new Value { ListValue = list };
    }

    public static string RequireTextArgument(ActionRequest request)
    {
        // Parser for the adapter-local one-line dialogue helper; production capabilities do not expose speak.
        if (request.Arguments is null || !request.Arguments.Fields.TryGetValue("text", out Value? value))
            throw new ArgumentException("missing required speak argument: text");

        return RequireBoundedText(value.StringValue, "speak argument text", MaxDialogueTextChars);
    }

    public static string RequireEmoteArgument(ActionRequest request)
    {
        if (request.Arguments is null || !request.Arguments.Fields.TryGetValue("emote", out Value? value))
            throw new ArgumentException("missing required emote argument: emote");

        string emote = value.StringValue;
        if (string.IsNullOrWhiteSpace(emote))
            throw new ArgumentException("emote argument must not be empty");

        return emote;
    }

    public static PresentDialogueInput RequirePresentDialogueArgument(ActionRequest request)
    {
        if (request.Arguments is null || !request.Arguments.Fields.TryGetValue("text", out Value? textValue))
            throw new ArgumentException("missing required present_dialogue argument: text");

        string text = RequireBoundedText(textValue.StringValue, "present_dialogue text", MaxDialogueTextChars);
        if (!request.Arguments.Fields.TryGetValue("reply_options", out Value? optionsValue))
            throw new ArgumentException("missing required present_dialogue argument: reply_options");
        if (optionsValue.KindCase != Value.KindOneofCase.ListValue)
            throw new ArgumentException("present_dialogue reply_options must be a list");

        if (optionsValue.ListValue.Values.Count > MaxReplyOptions)
            throw new ArgumentException($"present_dialogue reply_options must include {MaxReplyOptions} options or fewer");

        string[] replyOptions = optionsValue.ListValue.Values
            .Select(option => RequireBoundedText(option.StringValue, "present_dialogue reply option", MaxReplyOptionChars))
            .ToArray();

        bool allowFreeText = true;
        if (request.Arguments.Fields.TryGetValue("allow_free_text", out Value? allowFreeTextValue))
        {
            if (allowFreeTextValue.KindCase != Value.KindOneofCase.BoolValue)
                throw new ArgumentException("present_dialogue allow_free_text must be a boolean");

            allowFreeText = allowFreeTextValue.BoolValue;
        }
        if (allowFreeText && replyOptions.Length != MaxReplyOptions)
            throw new ArgumentException($"continuing present_dialogue must include exactly {MaxReplyOptions} reply options");
        if (!allowFreeText && replyOptions.Length != 0)
            throw new ArgumentException("ending dialogue must not include reply options");

        return new PresentDialogueInput(text, replyOptions, allowFreeText);
    }

    public static SendMailInput RequireSendMailArgument(ActionRequest request)
    {
        if (request.Arguments is null)
            throw new ArgumentException("missing required send_mail arguments");

        if (!request.Arguments.Fields.TryGetValue("body", out Value? bodyValue))
            throw new ArgumentException("missing required send_mail argument: body");
        if (bodyValue.KindCase != Value.KindOneofCase.StringValue)
            throw new ArgumentException("send_mail body must be a string");

        string? title = null;
        if (request.Arguments.Fields.TryGetValue("title", out Value? titleValue))
        {
            if (titleValue.KindCase != Value.KindOneofCase.StringValue)
                throw new ArgumentException("send_mail title must be a string");
            title = titleValue.StringValue;
        }

        // Shape only. Length, characters and newline normalisation belong to the capability, so
        // a malformed call reports invalid_action_arguments while bad text reports mail_*_invalid.
        return new SendMailInput(title, bodyValue.StringValue);
    }

    /// <summary>
    /// Read the required schedule_mail intent. Shape only: length belongs to the capability, so a
    /// malformed call reports invalid_action_arguments while a bad intent reports
    /// schedule_mail_intent_invalid.
    /// </summary>
    public static string RequireScheduleMailIntent(ActionRequest request)
    {
        if (request.Arguments is null)
            throw new ArgumentException("missing required schedule_mail arguments");

        if (!request.Arguments.Fields.TryGetValue("intent", out Value? intentValue))
            throw new ArgumentException("missing required schedule_mail argument: intent");
        if (intentValue.KindCase != Value.KindOneofCase.StringValue)
            throw new ArgumentException("schedule_mail intent must be a string");

        return intentValue.StringValue;
    }

    public static MoveToInput RequireMoveToArgument(ActionRequest request)
    {
        if (request.Arguments is null)
            throw new ArgumentException("missing required move_to arguments");

        if (!request.Arguments.Fields.TryGetValue("location", out Value? locationValue))
            throw new ArgumentException("missing required move_to argument: location");
        if (locationValue.KindCase != Value.KindOneofCase.StringValue)
            throw new ArgumentException("move_to location must be a string");

        if (!request.Arguments.Fields.TryGetValue("tile", out Value? tileValue))
            throw new ArgumentException("missing required move_to argument: tile");
        if (tileValue.KindCase != Value.KindOneofCase.StructValue)
            throw new ArgumentException("move_to tile must be an object");

        Struct tile = tileValue.StructValue;
        int x = RequireIntegerField(tile, "x", "move_to tile.x");
        int y = RequireIntegerField(tile, "y", "move_to tile.y");
        return new MoveToInput(RequireNonEmpty(locationValue.StringValue, "move_to location"), x, y);
    }

    public static ActionResult BuildSucceededActionResult(ActionRequest request, string displayedText)
    {
        return BuildSucceededActionResult(request, "displayed_text", displayedText);
    }

    public static ActionResult BuildSucceededActionResult(ActionRequest request, string outputFieldName, string outputValue)
    {
        return new ActionResult
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Succeeded,
            Output = new Struct
            {
                Fields =
                {
                    [outputFieldName] = Value.ForString(outputValue),
                },
            },
        };
    }

    public static ActionResult BuildPresentDialogueSucceededActionResult(ActionRequest request, string conversationId, PresentDialogueInput input)
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
                    ["displayed_text"] = Value.ForString(input.Text),
                    ["reply_options_count"] = Value.ForNumber(input.ReplyOptions.Count),
                    ["allow_free_text"] = Value.ForBool(input.AllowFreeText),
                },
            },
        };
    }

    public static ActionResult BuildFacePlayerSucceededActionResult(ActionRequest request, string facing)
    {
        return BuildSucceededActionResult(request, "facing", facing);
    }

    public static ActionResult BuildMoveToSucceededActionResult(ActionRequest request, MoveToProgress progress)
    {
        return new ActionResult
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Succeeded,
            Output = BuildMoveToLocationOutput(progress),
        };
    }

    public static Struct BuildMoveToStatusMetadata(MoveToProgress progress)
    {
        return BuildMoveToLocationOutput(progress);
    }

    public static ActionResult BuildFailedActionResult(ActionRequest request, string code, Exception ex) =>
        BuildFailedActionResult(request, code, ex.Message);

    public static ActionResult BuildFailedActionResult(ActionRequest request, string code, string message)
    {
        return new ActionResult
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Failed,
            Error = new Error
            {
                Code = code,
                Message = message,
            },
        };
    }

    public static ActionResult BuildRejectedActionResult(ActionRequest request, string code, string message)
    {
        return new ActionResult
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Rejected,
            Error = new Error
            {
                Code = code,
                Message = message,
            },
        };
    }

    public static ActionResult BuildCancelledActionResult(ActionRequest request, string reason)
    {
        return new ActionResult
        {
            ActionId = request.ActionId,
            Status = ActionStatus.Cancelled,
            Error = new Error
            {
                Code = "action_cancelled",
                Message = reason,
            },
        };
    }

    public static AdapterMessage BuildErrorMessage(string correlationId, string code, Exception ex)
    {
        return BuildErrorMessage(correlationId, code, ex.Message);
    }

    public static AdapterMessage BuildErrorMessage(string correlationId, string code, string message)
    {
        return new AdapterMessage
        {
            MessageId = NewMessageId("error"),
            CorrelationId = correlationId,
            Error = new Error
            {
                Code = code,
                Message = message,
            },
        };
    }

    public static string NewMessageId(string prefix)
    {
        long sequence = Interlocked.Increment(ref messageSequence);
        return $"{prefix}_{DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()}_{sequence}";
    }

    private static string RequireBoundedText(string value, string name, int maxChars)
    {
        string text = RequireNonEmpty(value, name);
        if (text.Length > maxChars)
            throw new ArgumentException($"{name} must be {maxChars} chars or fewer");

        return text;
    }

    private static string RequireNonEmpty(string value, string name)
    {
        if (string.IsNullOrWhiteSpace(value))
            throw new ArgumentException($"{name} must not be empty");

        return value.Trim();
    }

    private static int RequireIntegerField(Struct structure, string fieldName, string name)
    {
        if (!structure.Fields.TryGetValue(fieldName, out Value? value))
            throw new ArgumentException($"missing required {name}");
        if (value.KindCase != Value.KindOneofCase.NumberValue)
            throw new ArgumentException($"{name} must be a number");

        double number = value.NumberValue;
        if (double.IsNaN(number) || double.IsInfinity(number) || Math.Round(number) != number)
            throw new ArgumentException($"{name} must be an integer");
        if (number < int.MinValue || number > int.MaxValue)
            throw new ArgumentException($"{name} is outside supported integer range");

        return (int)number;
    }

    private static Struct BuildMoveToLocationOutput(MoveToProgress progress)
    {
        return new Struct
        {
            Fields =
            {
                ["location"] = Value.ForString(progress.Location),
                ["tile"] = ForStruct(new Struct
                {
                    Fields =
                    {
                        ["x"] = Value.ForNumber(progress.TileX),
                        ["y"] = Value.ForNumber(progress.TileY),
                    },
                }),
            },
        };
    }

    private static EntityRef BuildEntity(string entityId, string entityType, string displayName)
    {
        return new EntityRef
        {
            EntityId = entityId,
            EntityType = entityType,
            DisplayName = displayName,
            // Stardew MVP0 uses stable game entity IDs as definition aliases.
            DefinitionId = entityId,
        };
    }
}
