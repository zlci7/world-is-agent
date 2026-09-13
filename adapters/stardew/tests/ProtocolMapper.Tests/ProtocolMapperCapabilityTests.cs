using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Google.Protobuf.WellKnownTypes;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ProtocolMapperCapabilityTests
{
    [Fact]
    public void BuildsEnvironmentCapabilities()
    {
        CapabilityList capabilities = CapabilityCatalog.BuildEnvironmentCapabilities();
        Capability emote = capabilities.Capabilities.Single(capability => capability.Name == "emote");
        Capability presentDialogue = capabilities.Capabilities.Single(capability => capability.Name == "present_dialogue");
        Capability facePlayer = capabilities.Capabilities.Single(capability => capability.Name == "face_player");
        Capability moveTo = capabilities.Capabilities.Single(capability => capability.Name == "move_to");

        Assert.DoesNotContain(capabilities.Capabilities, capability => capability.Name == "speak");
        Assert.Contains("emote bubble", emote.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("reply options", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("exactly three distinct player-authored reply options", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.DoesNotContain("up to four reply options", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("continuing", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("ending", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("wait for player_said_to_npc", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("only tool call", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("allow_free_text=false", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("reply_options=[]", presentDialogue.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("face the player", facePlayer.Description, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("reachable tile", moveTo.Description, StringComparison.OrdinalIgnoreCase);

        Assert.Equal(CapabilityConcurrencyMode.Sequential, emote.ConcurrencyMode);
        Assert.Equal(CapabilityConcurrencyMode.Sequential, presentDialogue.ConcurrencyMode);
        Assert.Equal(CapabilityConcurrencyMode.Sequential, facePlayer.ConcurrencyMode);
        Assert.Equal(CapabilityConcurrencyMode.Sequential, moveTo.ConcurrencyMode);
        Assert.Equal(ExecutionMode.Async, moveTo.ExecutionMode);

        Assert.Contains("\"location\"", moveTo.InputSchemaJson, StringComparison.Ordinal);
        Assert.Contains("\"tile\"", moveTo.InputSchemaJson, StringComparison.Ordinal);
        Assert.Contains("\"x\":{\"type\":\"integer\"}", moveTo.InputSchemaJson, StringComparison.Ordinal);
        Assert.Contains("\"y\":{\"type\":\"integer\"}", moveTo.InputSchemaJson, StringComparison.Ordinal);
        Assert.Contains("\"maxItems\":3", presentDialogue.InputSchemaJson, StringComparison.Ordinal);
        Assert.Contains("\"required\":[\"text\",\"reply_options\"]", presentDialogue.InputSchemaJson, StringComparison.Ordinal);
        Assert.Contains("\"allow_free_text\":{\"type\":\"boolean\",\"default\":true", presentDialogue.InputSchemaJson, StringComparison.Ordinal);

        Struct gameAgentExtensions = TestSupport.RequireStruct(presentDialogue.Extensions, "gameagent");
        Struct toolPolicy = TestSupport.RequireStruct(gameAgentExtensions, "tool_policy");
        Assert.True(toolPolicy.Fields["exclusive_per_step"].BoolValue);
        Assert.True(toolPolicy.Fields["settle_after_success"].BoolValue);
    }

    [Fact]
    public void AddsResolveMeetingWithConfiguredLandmarkEnum()
    {
        const string json = "{\"landmarks\":[{\"landmark_id\":\"beach_meeting_spot\",\"display_name\":\"Beach meeting spot\",\"location\":\"Beach\",\"tile\":{\"x\":28,\"y\":36},\"open_start\":600,\"open_end\":2200,\"departure_lead_minutes\":240,\"supported_routes\":[{\"npc_id\":\"npc:Linus\",\"origin_location\":\"Mountain\"}]}]}";

        Capability resolve = CapabilityCatalog.BuildEnvironmentCapabilities(LandmarkCatalog.Parse(json).Landmarks)
            .Capabilities.Single(capability => capability.Name == "resolve_meeting");
        Capability moveToLandmark = CapabilityCatalog.BuildEnvironmentCapabilities(LandmarkCatalog.Parse(json).Landmarks)
            .Capabilities.Single(capability => capability.Name == "move_to_landmark");
        Capability waitForPlayer = CapabilityCatalog.BuildEnvironmentCapabilities(LandmarkCatalog.Parse(json).Landmarks)
            .Capabilities.Single(capability => capability.Name == "wait_for_player");
        Capability approachPlayer = CapabilityCatalog.BuildEnvironmentCapabilities(LandmarkCatalog.Parse(json).Landmarks)
            .Capabilities.Single(capability => capability.Name == "approach_player");

        Assert.Equal(ExecutionMode.Sync, resolve.ExecutionMode);
        Assert.Equal(CapabilityConcurrencyMode.Sequential, resolve.ConcurrencyMode);
        Assert.Contains("\"enum\":[\"beach_meeting_spot\"]", resolve.InputSchemaJson, StringComparison.Ordinal);
        Assert.Contains("\"additionalProperties\":false", resolve.InputSchemaJson, StringComparison.Ordinal);
        Assert.Equal(ExecutionMode.Async, moveToLandmark.ExecutionMode);
        Assert.Equal(CapabilityConcurrencyMode.Sequential, moveToLandmark.ConcurrencyMode);
        Assert.Contains("\"enum\":[\"beach_meeting_spot\"]", moveToLandmark.InputSchemaJson, StringComparison.Ordinal);
        Assert.Equal(ExecutionMode.Sync, waitForPlayer.ExecutionMode);
        Assert.Equal("{\"type\":\"object\",\"properties\":{},\"additionalProperties\":false}", waitForPlayer.InputSchemaJson);
        Assert.Equal(ExecutionMode.Async, approachPlayer.ExecutionMode);
        Assert.Equal(CapabilityConcurrencyMode.Sequential, approachPlayer.ConcurrencyMode);
    }
}
