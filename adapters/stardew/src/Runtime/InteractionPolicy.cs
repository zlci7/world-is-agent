namespace GameAgent.Stardew.Runtime;

public static class InteractionPolicy
{
    public const int MaxInteractionDistance = 2;

    public static bool IsWithinMaxInteractionDistance(int npcTileX, int npcTileY, int playerTileX, int playerTileY)
    {
        return ManhattanDistance(npcTileX, npcTileY, playerTileX, playerTileY) <= MaxInteractionDistance;
    }

    // These rejections must not fall through to the game's own NPC interaction,
    // which would open a dialogue the model cannot continue.
    public static bool SuppressesInput(string reason) =>
        reason == "interaction_in_flight" || reason == "npc_control_busy";

    public static int ManhattanDistance(int leftX, int leftY, int rightX, int rightY)
    {
        return Math.Abs(leftX - rightX) + Math.Abs(leftY - rightY);
    }
}
