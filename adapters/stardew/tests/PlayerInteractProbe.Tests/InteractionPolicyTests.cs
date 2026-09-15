using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class InteractionPolicyTests
{
    [Fact]
    public void AllowsNearbyNpcInteractions()
    {
        Assert.True(InteractionPolicy.IsWithinMaxInteractionDistance(
            npcTileX: 10,
            npcTileY: 10,
            playerTileX: 11,
            playerTileY: 11
        ));
    }

    [Fact]
    public void RejectsTooFarNpcInteractions()
    {
        Assert.False(InteractionPolicy.IsWithinMaxInteractionDistance(
            npcTileX: 10,
            npcTileY: 10,
            playerTileX: 13,
            playerTileY: 10
        ));
    }

    [Theory]
    [InlineData("interaction_in_flight", true)]
    [InlineData("npc_control_busy", true)]
    [InlineData("world_not_ready", false)]
    [InlineData("", false)]
    public void SuppressesOnlyRejectionsThatWouldOpenAGameOwnedDialogue(string reason, bool suppresses)
    {
        Assert.Equal(suppresses, InteractionPolicy.SuppressesInput(reason));
    }
}
