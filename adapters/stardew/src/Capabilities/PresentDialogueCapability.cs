using System;
using GameAgent.Stardew.Dialogue;
using StardewValley;

namespace GameAgent.Stardew.Capabilities;

public sealed class PresentDialogueCapability
{
    private const string NpcEntityPrefix = "npc:";
    private const string PlayerEntityId = "player:local";

    private readonly ConversationStateStore conversationStore;
    private readonly DialogueInteractionController dialogueController;

    public PresentDialogueCapability(ConversationStateStore conversationStore, DialogueInteractionController dialogueController)
    {
        this.conversationStore = conversationStore;
        this.dialogueController = dialogueController;
    }

    public void CloseForNpc(string npcEntityId)
    {
        this.dialogueController.CloseForNpc(npcEntityId);
    }

    public void QueueWaitingForNpc(string npcEntityId)
    {
        this.dialogueController.QueueWaitingForNpc(npcEntityId);
    }

    public void CloseWaitingForNpc(string npcEntityId)
    {
        this.dialogueController.CloseWaitingForNpc(npcEntityId);
    }

    public void CloseAll()
    {
        this.dialogueController.CloseAll();
    }

    public void Present(
        NPC npc,
        Farmer player,
        string worldId,
        PresentDialogueInput input,
        Func<bool> isCancelled,
        Action onCancelled,
        Action<string> onDisplayed,
        Action<Exception> onFailed,
        Action<PlayerDialogueSubmission> onSubmitted,
        Action onAbandoned,
        Action onFinished
    )
    {
        string npcEntityId = ToNpcEntityId(npc.Name);
        string? conversationId = null;
        this.dialogueController.CloseForNpc(npcEntityId);
        this.dialogueController.QueueOrShow(
            npcEntityId,
            createPresentation: () =>
            {
                if (isCancelled())
                {
                    onCancelled();
                    return null;
                }

                conversationId = this.conversationStore.EnsureConversationId(worldId, npcEntityId, PlayerEntityId);
                return new DialoguePresentation(
                    npc,
                    npcEntityId,
                    conversationId,
                    input,
                    onSubmitted,
                    OnAbandoned: () =>
                    {
                        this.conversationStore.CloseIfConversation(worldId, npcEntityId, PlayerEntityId, conversationId);
                        onAbandoned();
                    },
                    OnFinished: onFinished
                );
            },
            onDisplayed: () =>
            {
                string displayedConversationId = conversationId ?? throw new InvalidOperationException("present_dialogue displayed without a conversation id");
                this.conversationStore.AppendNpcLineToConversation(worldId, npcEntityId, PlayerEntityId, displayedConversationId, npc.displayName, input.Text, Game1.timeOfDay);
                if (input.ReplyOptions.Count == 0 && !input.AllowFreeText)
                    this.conversationStore.CloseIfConversation(worldId, npcEntityId, PlayerEntityId, displayedConversationId);
                onDisplayed(displayedConversationId);
            },
            onFailed: onFailed
        );
    }

    private static string ToNpcEntityId(string npcName)
    {
        return $"{NpcEntityPrefix}{npcName}";
    }
}
