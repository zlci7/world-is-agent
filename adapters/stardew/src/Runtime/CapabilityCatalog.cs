using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Integrations.MailFramework;
using GameAgent.Stardew.Tasks;
using Google.Protobuf.WellKnownTypes;

namespace GameAgent.Stardew.Runtime;

public static class CapabilityCatalog
{
    private const string EmoteInputSchemaJson =
        "{\"type\":\"object\",\"properties\":{\"emote\":{\"type\":\"string\",\"enum\":[\"happy\",\"sad\",\"surprised\",\"neutral\"]}},\"required\":[\"emote\"],\"additionalProperties\":false}";

    private const string PresentDialogueInputSchemaJson =
        "{\"type\":\"object\",\"properties\":{\"text\":{\"type\":\"string\",\"maxLength\":240},\"reply_options\":{\"type\":\"array\",\"maxItems\":3,\"items\":{\"type\":\"string\",\"maxLength\":80},\"description\":\"Exactly three distinct player replies for continuing dialogue; empty only for ending dialogue.\"},\"allow_free_text\":{\"type\":\"boolean\",\"default\":true,\"description\":\"True or omitted for continuing dialogue; explicit false only for ending dialogue.\"}},\"required\":[\"text\",\"reply_options\"],\"additionalProperties\":false}";

    private const string FacePlayerInputSchemaJson =
        "{\"type\":\"object\",\"properties\":{},\"additionalProperties\":false}";

    private const string MoveToInputSchemaJson =
        "{\"type\":\"object\",\"properties\":{\"location\":{\"type\":\"string\"},\"tile\":{\"type\":\"object\",\"properties\":{\"x\":{\"type\":\"integer\"},\"y\":{\"type\":\"integer\"}},\"required\":[\"x\",\"y\"],\"additionalProperties\":false}},\"required\":[\"location\",\"tile\"],\"additionalProperties\":false}";

    // Length limits come from the validator so the advertised schema cannot drift from what the
    // Adapter actually accepts.
    private static readonly string SendMailInputSchemaJson =
        "{\"type\":\"object\",\"properties\":{" +
        "\"body\":{\"type\":\"string\",\"maxLength\":" + MailTextValidator.MaxBodyLength + "}," +
        "\"title\":{\"type\":\"string\",\"maxLength\":" + MailTextValidator.MaxTitleLength + "}}," +
        "\"required\":[\"body\"],\"additionalProperties\":false}";

    private const string SendMailDescription =
        "Writes a letter from this NPC to the player and puts it in the farm mailbox right away. " +
        "Use it when the NPC has something to tell the player that cannot be said face to face in " +
        "this conversation, such as a farewell, or a reply the player asked to receive later. " +
        "Do not use it while the player is standing here and the answer can simply be spoken.";

    public static bool RequiresTaskReady(string capability) =>
        capability is "resolve_meeting" or "move_to_landmark" or "wait_for_player";

    public static CapabilityList BuildEnvironmentCapabilities(
        IEnumerable<Landmark>? landmarks = null,
        bool includeTaskCapabilities = true,
        bool includeMailCapability = false)
    {
        CapabilityList result = new()
        {
            Revision = 1,
            Capabilities =
            {
                new Capability
                {
                    Name = "emote",
                    Version = "0.1.0",
                    Description = "Displays one emote bubble above the NPC.",
                    InputSchemaJson = EmoteInputSchemaJson,
                    ExecutionMode = ExecutionMode.Sync,
                    ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                },
                new Capability
                {
                    Name = "present_dialogue",
                    Version = "0.1.0",
                    Description = "Displays NPC dialogue using exactly one of two forms. For continuing dialogue, provide exactly three distinct player-authored reply options and set allow_free_text=true or omit it because true is the default. For ending dialogue, provide reply_options=[] and allow_free_text=false. It must be the only tool call in its model response. After it succeeds, the current turn ends; wait for player_said_to_npc before continuing that conversation.",
                    InputSchemaJson = PresentDialogueInputSchemaJson,
                    ExecutionMode = ExecutionMode.Sync,
                    ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                    Extensions = PresentDialogueExtensions(),
                },
                new Capability
                {
                    Name = "face_player",
                    Version = "0.1.0",
                    Description = "Turns the NPC to face the player when both are in the same location.",
                    InputSchemaJson = FacePlayerInputSchemaJson,
                    ExecutionMode = ExecutionMode.Sync,
                    ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                },
                new Capability
                {
                    Name = "move_to",
                    Version = "0.1.0",
                    Description = "Moves the NPC toward a reachable tile in the current location. The action is asynchronous; wait for the terminal result before deciding the next step.",
                    InputSchemaJson = MoveToInputSchemaJson,
                    ExecutionMode = ExecutionMode.Async,
                    ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                },
                new Capability
                {
                    Name = "approach_player",
                    Version = "0.1.0",
                    Description = "Moves the NPC to one fixed reachable tile adjacent to the player position observed when the action starts. The target is not recomputed if the player moves.",
                    InputSchemaJson = FacePlayerInputSchemaJson,
                    ExecutionMode = ExecutionMode.Async,
                    ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                },
            },
        };

        // Published only when Mail Framework Mod actually resolved at GameLaunched. The model
        // should not see a tool this environment cannot execute; a rejection path still exists in
        // the handler as a defensive branch.
        if (includeMailCapability)
        {
            result.Capabilities.Add(new Capability
            {
                Name = "send_mail",
                Version = "0.1.0",
                Description = SendMailDescription,
                InputSchemaJson = SendMailInputSchemaJson,
                ExecutionMode = ExecutionMode.Sync,
                ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
            });
        }

        if (includeTaskCapabilities && landmarks is not null)
        {
            string[] ids = landmarks.Select(landmark => landmark.LandmarkId).Distinct().OrderBy(id => id).ToArray();
            if (ids.Length != 0)
            {
                string landmarkEnum = JsonSerializer.Serialize(ids);
                result.Capabilities.Add(new Capability
                {
                    Name = "resolve_meeting",
                    Version = "0.1.0",
                    Description = "Validates a player-agreed future meeting. start_time is the moment the NPC departs, so it must be later than the current in-game time, and end_time must stay open long enough to cover the NPC's trip. On success, use the returned proposal_ref with create_task in the next step. Only confirm the appointment after create_task succeeds; validation alone does not save or schedule a task.",
                    InputSchemaJson = $"{{\"type\":\"object\",\"properties\":{{\"landmark_id\":{{\"type\":\"string\",\"enum\":{landmarkEnum}}},\"target_date\":{{\"type\":\"object\",\"properties\":{{\"year\":{{\"type\":\"integer\",\"minimum\":1}},\"season\":{{\"type\":\"string\",\"enum\":[\"spring\",\"summer\",\"fall\",\"winter\"]}},\"day_of_month\":{{\"type\":\"integer\",\"minimum\":1,\"maximum\":28}}}},\"required\":[\"year\",\"season\",\"day_of_month\"],\"additionalProperties\":false}},\"start_time\":{{\"type\":\"integer\"}},\"end_time\":{{\"type\":\"integer\"}}}},\"required\":[\"landmark_id\",\"target_date\",\"start_time\",\"end_time\"],\"additionalProperties\":false}}",
                    ExecutionMode = ExecutionMode.Sync,
                    ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                });
                result.Capabilities.Add(new Capability
                {
                    Name = "move_to_landmark",
                    Version = "0.1.0",
                    Description = "Moves the task NPC to a verified meeting landmark using the native cross-location schedule pathfinder. Arrival produces progress evidence, not task satisfaction.",
                    InputSchemaJson = $"{{\"type\":\"object\",\"properties\":{{\"landmark_id\":{{\"type\":\"string\",\"enum\":{landmarkEnum}}}}},\"required\":[\"landmark_id\"],\"additionalProperties\":false}}",
                    ExecutionMode = ExecutionMode.Async,
                    ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                });
                result.Capabilities.Add(new Capability
                {
                    Name = "wait_for_player",
                    Version = "0.1.0",
                    Description = "Registers an authoritative game-clock wait at the task landmark and returns immediately. The adapter reports met or expired independently of the model.",
                    InputSchemaJson = "{\"type\":\"object\",\"properties\":{},\"additionalProperties\":false}",
                    ExecutionMode = ExecutionMode.Sync,
                    ConcurrencyMode = CapabilityConcurrencyMode.Sequential,
                });
            }
        }

        return result;
    }

    private static Struct PresentDialogueExtensions()
    {
        Struct toolPolicy = new();
        toolPolicy.Fields.Add("exclusive_per_step", Value.ForBool(true));
        toolPolicy.Fields.Add("settle_after_success", Value.ForBool(true));

        Struct gameagent = new();
        gameagent.Fields.Add("tool_policy", Value.ForStruct(toolPolicy));

        Struct extensions = new();
        extensions.Fields.Add("gameagent", Value.ForStruct(gameagent));
        return extensions;
    }
}
