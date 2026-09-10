using GameAgent.Protocol.V1Alpha2;
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

    public static CapabilityList BuildEnvironmentCapabilities()
    {
        return new CapabilityList
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
            },
        };
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
