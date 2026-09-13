using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskInteractionConversationIndexTests
{
    [Fact]
    public void TurnWithoutDialogueReleasesButDisplayedDialogueDefersToUiLifecycle()
    {
        TaskInteractionConversationIndex index = new();
        index.Register("conversation", "arrival-event");

        Assert.True(index.ShouldReleaseAtTurnCompletion("turn-without-ui", "conversation"));

        index.MarkPresentation("turn-with-ui");
        Assert.False(index.ShouldReleaseAtTurnCompletion("turn-with-ui", "conversation"));
        Assert.Equal("arrival-event", index.FindTaskEvent("conversation"));
    }

    [Fact]
    public void RemovingTaskEventDropsItsConversationBinding()
    {
        TaskInteractionConversationIndex index = new();
        index.Register("conversation", "arrival-event");

        index.RemoveTaskEvent("arrival-event");

        Assert.Null(index.FindTaskEvent("conversation"));
    }
}
