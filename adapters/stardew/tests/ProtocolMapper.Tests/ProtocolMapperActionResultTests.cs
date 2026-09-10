using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Capabilities;
using GameAgent.Stardew.Dialogue;
using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ProtocolMapperActionResultTests
{
    [Fact]
    public void BuildsPresentDialogueSucceededActionResult()
    {
        ActionRequest request = TestSupport.CreatePresentDialogueRequest();
        PresentDialogueInput input = ProtocolMapper.RequirePresentDialogueArgument(request);

        ActionResult result = ProtocolMapper.BuildPresentDialogueSucceededActionResult(request, "conv_1", input);

        Assert.Equal(ActionStatus.Succeeded, result.Status);
        Assert.Equal("conv_1", result.Output.Fields["conversation_id"].StringValue);
        Assert.Equal(3, result.Output.Fields["reply_options_count"].NumberValue);
        Assert.True(result.Output.Fields["allow_free_text"].BoolValue);
        Assert.False(result.Output.Fields.ContainsKey("free_text_enabled"));
    }

    [Fact]
    public void BuildsFacePlayerSucceededActionResult()
    {
        ActionRequest request = TestSupport.CreatePresentDialogueRequest();

        ActionResult result = ProtocolMapper.BuildFacePlayerSucceededActionResult(request, "down");

        Assert.Equal("down", result.Output.Fields["facing"].StringValue);
        Assert.False(result.Output.Fields.ContainsKey("facing_direction"));
    }

    [Fact]
    public void BuildsMoveToSucceededActionResultAndStatusMetadata()
    {
        ActionRequest request = TestSupport.CreateMoveToRequest();

        ActionResult result = ProtocolMapper.BuildMoveToSucceededActionResult(request, new MoveToProgress("Town", 12, 20));

        Assert.Equal(ActionStatus.Succeeded, result.Status);
        Assert.Equal("Town", result.Output.Fields["location"].StringValue);
        Assert.Equal(12, TestSupport.RequireNumber(result.Output.Fields["tile"].StructValue, "x"));
        Assert.Equal(20, TestSupport.RequireNumber(ProtocolMapper.BuildMoveToStatusMetadata(new MoveToProgress("Town", 12, 20)).Fields["tile"].StructValue, "y"));
    }

    [Fact]
    public void BuildsRejectedActionResult()
    {
        ActionRequest request = new()
        {
            ActionId = "act_1",
            EntityId = "npc:Abigail",
            WorldId = "Farm_123456",
        };

        ActionResult rejected = ProtocolMapper.BuildRejectedActionResult(request, "world_mismatch", "current world changed");

        Assert.Equal("act_1", rejected.ActionId);
        Assert.Equal(ActionStatus.Rejected, rejected.Status);
        Assert.Equal("world_mismatch", rejected.Error.Code);
    }

    [Fact]
    public void ResolvesFacePlayerDirection()
    {
        Assert.Equal("right", FacePlayerDirection.Resolve(npcTileX: 10, npcTileY: 10, playerTileX: 12, playerTileY: 10));
        Assert.Equal("down", FacePlayerDirection.Resolve(npcTileX: 10, npcTileY: 10, playerTileX: 10, playerTileY: 12));
        Assert.Equal("left", FacePlayerDirection.Resolve(npcTileX: 10, npcTileY: 10, playerTileX: 10, playerTileY: 10, currentDirection: FacePlayerDirection.Left));
    }
}
