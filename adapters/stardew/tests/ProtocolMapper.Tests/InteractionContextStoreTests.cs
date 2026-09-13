using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class InteractionContextStoreTests
{
    [Fact]
    public void ReservesCommitsValidatesAndReleasesInteractionContext()
    {
        InteractionContextStore interactionContexts = new();
        InteractionContextSnapshot interactionSnapshot = CreateInteractionSnapshot();

        Assert.True(interactionContexts.TryReserve(interactionSnapshot, out string inFlightReason));
        Assert.Equal("", inFlightReason);
        Assert.Equal(interactionSnapshot, interactionContexts.Find("event_guard_1"));
        Assert.Null(interactionContexts.TryGet("event_guard_1"));
        Assert.False(interactionContexts.TryReserve(interactionSnapshot with { EventId = "event_guard_pending_2", ConversationId = "conv_guard_pending_2" }, out inFlightReason));
        Assert.Equal("interaction_in_flight", inFlightReason);

        interactionContexts.Commit("event_guard_1");
        InteractionContextSnapshot committedInteraction = Assert.IsType<InteractionContextSnapshot>(interactionContexts.TryGet("event_guard_1"));
        Assert.Equal("conv_guard", committedInteraction.ConversationId);
        Assert.Equal("Town", committedInteraction.NpcLocation);

        ActionRequest guardedAction = new()
        {
            ActionId = "act_guard",
            EntityId = "npc:Abigail",
            WorldId = "Farm_123456",
            SourceEventId = "event_guard_1",
            SourceTurnId = "turn_guard",
        };
        Assert.True(interactionContexts.TryResolve(guardedAction, out InteractionContextSnapshot? resolvedInteraction, out string guardErrorCode, out _));
        committedInteraction = Assert.IsType<InteractionContextSnapshot>(resolvedInteraction);
        Assert.Equal("npc:Abigail", committedInteraction.NpcEntityId);

        InteractionContextCurrentState currentInteraction = CreateCurrentState();
        Assert.True(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction, requireProximity: true, out guardErrorCode, out _));

        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { ConversationId = "conv_changed" }, requireProximity: true, out guardErrorCode, out _));
        Assert.Equal("interaction_context_conversation_changed", guardErrorCode);
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { PlayerLocation = "Farm" }, requireProximity: true, out guardErrorCode, out _));
        Assert.Equal("interaction_context_player_location_changed", guardErrorCode);
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { PlayerTileX = 20 }, requireProximity: true, out guardErrorCode, out string guardMessage));
        Assert.Equal("interaction_context_distance_changed", guardErrorCode);
        Assert.Contains("distance", guardMessage, StringComparison.OrdinalIgnoreCase);

        Assert.True(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { PlayerTileX = 20 }, requireProximity: false, out guardErrorCode, out _));
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { WorldId = "Farm_999999" }, requireProximity: false, out guardErrorCode, out _));
        Assert.Equal("interaction_context_world_changed", guardErrorCode);
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { NpcEntityId = "npc:Robin" }, requireProximity: false, out guardErrorCode, out _));
        Assert.Equal("interaction_context_entity_changed", guardErrorCode);
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { PlayerEntityId = "player:remote" }, requireProximity: false, out guardErrorCode, out _));
        Assert.Equal("interaction_context_player_changed", guardErrorCode);
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { ConversationId = "conv_changed" }, requireProximity: false, out guardErrorCode, out _));
        Assert.Equal("interaction_context_conversation_changed", guardErrorCode);
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { NpcLocation = "Farm" }, requireProximity: false, out guardErrorCode, out _));
        Assert.Equal("interaction_context_npc_location_changed", guardErrorCode);
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { PlayerLocation = "Farm" }, requireProximity: false, out guardErrorCode, out _));
        Assert.Equal("interaction_context_player_location_changed", guardErrorCode);
        Assert.False(interactionContexts.TryValidateCurrentState(committedInteraction, currentInteraction with { PlayerTileX = 20 }, requireProximity: true, out guardErrorCode, out _));
        Assert.Equal("interaction_context_distance_changed", guardErrorCode);

        InteractionContextSnapshot dialogueContinuation = committedInteraction with { EventId = "event_player_reply" };
        Assert.True(interactionContexts.TryValidateCurrentState(dialogueContinuation, currentInteraction with { PlayerTileX = 20 }, requireProximity: false, out guardErrorCode, out _));
        Assert.False(interactionContexts.TryValidateCurrentState(dialogueContinuation, currentInteraction with { PlayerLocation = "Farm" }, requireProximity: false, out guardErrorCode, out _));
        Assert.Equal("interaction_context_player_location_changed", guardErrorCode);

        Assert.False(interactionContexts.TryReserve(interactionSnapshot with { EventId = "event_guard_committed_2", ConversationId = "conv_guard_committed_2" }, out inFlightReason));
        Assert.Equal("interaction_in_flight", inFlightReason);

        ActionRequest missingSourceAction = guardedAction.Clone();
        missingSourceAction.SourceEventId = "";
        Assert.False(interactionContexts.TryResolve(missingSourceAction, out _, out guardErrorCode, out _));
        Assert.Equal("interaction_context_missing", guardErrorCode);

        ActionRequest wrongEntityAction = guardedAction.Clone();
        wrongEntityAction.EntityId = "npc:Leah";
        Assert.False(interactionContexts.TryResolve(wrongEntityAction, out _, out guardErrorCode, out _));
        Assert.Equal("interaction_context_entity_changed", guardErrorCode);

        ActionRequest wrongWorldAction = guardedAction.Clone();
        wrongWorldAction.WorldId = "Farm_654321";
        Assert.False(interactionContexts.TryResolve(wrongWorldAction, out _, out guardErrorCode, out _));
        Assert.Equal("interaction_context_world_changed", guardErrorCode);

        interactionContexts.Reserve(interactionSnapshot with { ConversationId = "conv_overwritten" });
        interactionContexts.DiscardPending("event_guard_1");
        committedInteraction = Assert.IsType<InteractionContextSnapshot>(interactionContexts.TryGet("event_guard_1"));
        Assert.Equal("conv_guard", committedInteraction.ConversationId);

        interactionContexts.Release(new TurnCompletion { EventId = "event_guard_1", Status = TurnCompletionStatus.Completed });
        Assert.Null(interactionContexts.TryGet("event_guard_1"));
        Assert.True(interactionContexts.TryReserve(interactionSnapshot with { EventId = "event_guard_after_completion", ConversationId = "conv_guard_after_completion" }, out inFlightReason));
        interactionContexts.DiscardPending("event_guard_after_completion");

        interactionContexts.Reserve(interactionSnapshot with { EventId = "event_guard_2" });
        interactionContexts.DiscardPending("event_guard_2");
        Assert.Null(interactionContexts.TryGet("event_guard_2"));
    }

    [Fact]
    public void DialogueSubmissionHandoffKeepsNewInteractionUntilItsTurnCompletes()
    {
        InteractionContextStore handoffContexts = new();
        InteractionContextSnapshot interactionSnapshot = CreateInteractionSnapshot();

        Assert.True(handoffContexts.TryReserve(interactionSnapshot, out string inFlightReason));
        handoffContexts.Commit("event_guard_1");

        InteractionContextSnapshot handoffSnapshot = interactionSnapshot with { EventId = "event_player_handoff", ConversationId = "conv_guard" };
        Assert.True(handoffContexts.TryReserveHandoff(handoffSnapshot, out inFlightReason));
        Assert.Null(handoffContexts.TryGet("event_guard_1"));

        handoffContexts.Release(new TurnCompletion { EventId = "event_guard_1", Status = TurnCompletionStatus.Completed });
        Assert.False(handoffContexts.TryReserve(interactionSnapshot with { EventId = "event_click_before_reply_done" }, out inFlightReason));

        handoffContexts.Commit("event_player_handoff");
        handoffContexts.Release(new TurnCompletion { EventId = "event_player_handoff", Status = TurnCompletionStatus.Completed });
        Assert.True(handoffContexts.TryReserve(interactionSnapshot with { EventId = "event_after_handoff_done", ConversationId = "conv_after_handoff_done" }, out inFlightReason));
        handoffContexts.DiscardPending("event_after_handoff_done");
    }

    private static InteractionContextSnapshot CreateInteractionSnapshot() => new(
        EventId: "event_guard_1",
        WorldId: "Farm_123456",
        NpcEntityId: "npc:Abigail",
        PlayerEntityId: "player:local",
        ConversationId: "conv_guard",
        NpcLocation: "Town",
        NpcTileX: 10,
        NpcTileY: 12,
        PlayerLocation: "Town",
        PlayerTileX: 11,
        PlayerTileY: 12,
        MaxInteractionDistance: 2
    );

    private static InteractionContextCurrentState CreateCurrentState() => new(
        WorldId: "Farm_123456",
        NpcEntityId: "npc:Abigail",
        PlayerEntityId: "player:local",
        ConversationId: "conv_guard",
        NpcLocation: "Town",
        NpcTileX: 10,
        NpcTileY: 12,
        PlayerLocation: "Town",
        PlayerTileX: 11,
        PlayerTileY: 12
    );
}
