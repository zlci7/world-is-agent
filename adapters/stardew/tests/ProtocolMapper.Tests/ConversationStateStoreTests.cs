using GameAgent.Stardew.Dialogue;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ConversationStateStoreTests
{
    [Fact]
    public void CommitsAndReusesActiveConversation()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1", "conv_2"));

        string conversationId = store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        Assert.Equal("conv_1", conversationId);
        Assert.Null(store.GetActiveConversation("Farm_123456", "npc:Abigail", "player:local"));

        store.CommitPending("event_interact_1");
        ConversationSnapshot? activeConversation = store.GetActiveConversation("Farm_123456", "npc:Abigail", "player:local");
        Assert.Equal("conv_1", activeConversation?.ConversationId);

        string reusedConversationId = store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_2");
        Assert.Equal("conv_1", reusedConversationId);
    }

    [Fact]
    public void AcceptedPlayerLineEntersConversationState()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1"));
        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        store.CommitPending("event_interact_1");

        store.PreparePlayerLine("Farm_123456", "npc:Abigail", "player:local", "conv_1", "event_player_1", "player:local", "Local Farmer", "Let's go fishing.", 1820);
        store.CommitPending("event_player_1");

        ConversationSnapshot? activeConversation = store.GetActiveConversation("Farm_123456", "npc:Abigail", "player:local");
        ConversationSnapshot conversation = Assert.IsType<ConversationSnapshot>(activeConversation);
        Assert.Equal("Let's go fishing.", Assert.Single(conversation.RecentLines).Text);
    }

    [Fact]
    public void DiscardingTransientRejectedPlayerLineKeepsActiveConversation()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1"));
        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        store.CommitPending("event_interact_1");

        store.PreparePlayerLine("Farm_123456", "npc:Abigail", "player:local", "conv_1", "event_player_transient", "player:local", "Local Farmer", "Are you still there?", 1820);
        store.DiscardPending("event_player_transient");

        Assert.NotNull(store.GetActiveConversation("Farm_123456", "npc:Abigail", "player:local"));
    }

    [Fact]
    public void DialogueDisplayReusesAndClosesMatchingConversation()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1"));
        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        store.CommitPending("event_interact_1");

        string activeDialogueId = store.EnsureConversationId("Farm_123456", "npc:Abigail", "player:local");
        Assert.Equal("conv_1", activeDialogueId);

        store.CloseIfConversation("Farm_123456", "npc:Abigail", "player:local", "conv_missing");
        Assert.NotNull(store.GetActiveConversation("Farm_123456", "npc:Abigail", "player:local"));

        store.CloseIfConversation("Farm_123456", "npc:Abigail", "player:local", "conv_1");
        Assert.Null(store.GetActiveConversation("Farm_123456", "npc:Abigail", "player:local"));
    }

    [Fact]
    public void DisplayedNpcLineActivatesReservedConversation()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_2"));

        string reservedDialogueId = store.EnsureConversationId("Farm_123456", "npc:Leah", "player:local");
        Assert.Equal("conv_2", reservedDialogueId);
        Assert.Null(store.GetActiveConversation("Farm_123456", "npc:Leah", "player:local"));

        store.AppendNpcLineToConversation("Farm_123456", "npc:Leah", "player:local", reservedDialogueId, "Leah", "Hi there.", 930);

        ConversationSnapshot displayedConversation = Assert.IsType<ConversationSnapshot>(
            store.GetActiveConversation("Farm_123456", "npc:Leah", "player:local")
        );
        Assert.Equal("conv_2", displayedConversation.ConversationId);
        Assert.Equal("Hi there.", Assert.Single(displayedConversation.RecentLines).Text);
    }

    [Fact]
    public void RejectsOverlongPlayerLine()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1"));

        TestSupport.ExpectArgumentException(
            () => store.PreparePlayerLine("Farm_123456", "npc:Abigail", "player:local", "conv_1", "event_player_2", "player:local", "Local Farmer", new string('x', 241), 1820),
            "240"
        );
    }

    [Fact]
    public void PinnedInteractionKeepsIdentityWhileTheNativeDialogueCloses()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1", "conv_2"));
        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        store.CommitPending("event_interact_1");

        store.CloseIfConversation("Farm_123456", "npc:Abigail", "player:local", "conv_1");

        ConversationSnapshot? current = store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local");
        Assert.Equal("conv_1", current?.ConversationId);
        Assert.Null(store.GetActiveConversation("Farm_123456", "npc:Abigail", "player:local"));

        string reusedConversationId = store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_2");
        Assert.Equal("conv_1", reusedConversationId);
    }

    [Fact]
    public void PinnedPlayerLineKeepsIdentityWhileTheNativeDialogueCloses()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1", "conv_2"));
        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        store.CommitPending("event_interact_1");

        store.PreparePlayerLine("Farm_123456", "npc:Abigail", "player:local", "conv_1", "event_player_1", "player:local", "Local Farmer", "Let's go fishing.", 1820);
        store.CloseIfConversation("Farm_123456", "npc:Abigail", "player:local", "conv_1");

        Assert.Equal("conv_1", store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local")?.ConversationId);
    }

    [Fact]
    public void DiscardingTheInteractionEventReleasesItsPin()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1", "conv_2"));
        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        store.CloseIfConversation("Farm_123456", "npc:Abigail", "player:local", "conv_1");

        store.DiscardPending("event_interact_1");

        Assert.Null(store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local"));
        Assert.Equal("conv_2", store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_2"));
    }

    [Fact]
    public void ReleasingTheTurnPinEndsTheConversationIdentity()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1", "conv_2"));
        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        store.CommitPending("event_interact_1");
        store.CloseIfConversation("Farm_123456", "npc:Abigail", "player:local", "conv_1");

        store.ReleaseInteractionPin("event_interact_other");
        Assert.Equal("conv_1", store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local")?.ConversationId);

        store.ReleaseInteractionPin("event_interact_1");
        Assert.Null(store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local"));

        string nextConversationId = store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_2");
        Assert.Equal("conv_2", nextConversationId);
    }

    [Fact]
    public void CurrentConversationRequiresActiveOrPinnedState()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1", "conv_2"));
        Assert.Null(store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local"));

        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        Assert.Equal("conv_1", store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local")?.ConversationId);

        store.CommitPending("event_interact_1");
        store.ReleaseInteractionPin("event_interact_1");
        Assert.Equal("conv_1", store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local")?.ConversationId);

        store.CloseIfConversation("Farm_123456", "npc:Abigail", "player:local", "conv_1");
        Assert.Null(store.GetCurrentConversation("Farm_123456", "npc:Abigail", "player:local"));
    }

    [Fact]
    public void ClosedAndClearedStoreStartNewConversationIds()
    {
        ConversationStateStore store = new(new FixedConversationIdGenerator("conv_1", "conv_3", "conv_4"));
        store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_1");
        store.CommitPending("event_interact_1");
        store.CloseIfConversation("Farm_123456", "npc:Abigail", "player:local", "conv_1");
        store.ReleaseInteractionPin("event_interact_1");

        string conversationId = store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_3");
        Assert.Equal("conv_3", conversationId);
        store.DiscardPending("event_interact_3");

        store.Clear();
        conversationId = store.PrepareInteraction("Farm_123456", "npc:Abigail", "player:local", "event_interact_4");
        Assert.Equal("conv_4", conversationId);
    }
}
