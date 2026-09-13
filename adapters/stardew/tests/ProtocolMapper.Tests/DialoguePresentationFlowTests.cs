using GameAgent.Stardew.Dialogue;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class DialoguePresentationFlowTests
{
    [Fact]
    public void FinalDialogueReportsUiFinishedOnlyAfterItCloses()
    {
        int finished = 0;
        DialoguePresentationFlow flow = new(
            shouldShowReplyMenu: false,
            onDisplayed: () => { },
            onAbandoned: () => { },
            onFinished: () => finished++);
        flow.Start(() => { });

        flow.Update(isDialogueUiBusy: true, isReplyMenuActive: false, showReplyMenu: () => { });
        Assert.Equal(0, finished);
        flow.Update(isDialogueUiBusy: false, isReplyMenuActive: false, showReplyMenu: () => { });

        Assert.Equal(1, finished);
    }

    [Fact]
    public void ShowsNpcLineThenReplyMenuAfterNativeDialogueCloses()
    {
        int npcLineShows = 0;
        int displayedCallbacks = 0;
        int replyMenuShows = 0;
        int abandonCallbacks = 0;
        DialoguePresentationFlow flow = new(
            shouldShowReplyMenu: true,
            onDisplayed: () => displayedCallbacks++,
            onAbandoned: () => abandonCallbacks++
        );

        flow.Start(() => npcLineShows++);
        flow.Update(isDialogueUiBusy: true, isReplyMenuActive: false, showReplyMenu: () => replyMenuShows++);
        flow.Update(isDialogueUiBusy: false, isReplyMenuActive: false, showReplyMenu: () => replyMenuShows++);
        flow.MarkSubmitted();
        flow.Update(isDialogueUiBusy: false, isReplyMenuActive: false, showReplyMenu: () => replyMenuShows++);

        Assert.Equal(1, npcLineShows);
        Assert.Equal(1, displayedCallbacks);
        Assert.Equal(1, replyMenuShows);
        Assert.True(flow.IsFinished);
        Assert.Equal(0, abandonCallbacks);
    }

    [Fact]
    public void PlainDialogueFinishesAfterNativeDialogueCloses()
    {
        int plainDialogueShows = 0;
        DialoguePresentationFlow flow = new(
            shouldShowReplyMenu: false,
            onDisplayed: () => { },
            onAbandoned: () => throw new InvalidOperationException("plain dialogue should not abandon")
        );

        flow.Start(() => plainDialogueShows++);
        flow.Update(isDialogueUiBusy: true, isReplyMenuActive: false, showReplyMenu: () => throw new InvalidOperationException("plain dialogue should not show reply menu"));
        flow.Update(isDialogueUiBusy: false, isReplyMenuActive: false, showReplyMenu: () => throw new InvalidOperationException("plain dialogue should not show reply menu"));

        Assert.Equal(1, plainDialogueShows);
        Assert.True(flow.IsFinished);
    }

    [Fact]
    public void PreemptedDialogueClosesVisibleUiWithoutAbandoningConversation()
    {
        int closedVisibleUi = 0;
        int abandonCallbacks = 0;
        DialoguePresentationFlow flow = new(
            shouldShowReplyMenu: true,
            onDisplayed: () => { },
            onAbandoned: () => abandonCallbacks++
        );

        flow.Start(() => { });
        flow.CloseWithoutSubmission(() => closedVisibleUi++);

        Assert.True(flow.IsFinished);
        Assert.Equal(1, closedVisibleUi);
        Assert.Equal(0, abandonCallbacks);
    }

    [Fact]
    public void ClosedReplyMenuAbandonsActiveConversationOnce()
    {
        int abandonCallbacks = 0;
        DialoguePresentationFlow flow = new(
            shouldShowReplyMenu: true,
            onDisplayed: () => { },
            onAbandoned: () => abandonCallbacks++
        );

        flow.Start(() => { });
        flow.Update(isDialogueUiBusy: true, isReplyMenuActive: false, showReplyMenu: () => { });
        flow.Update(isDialogueUiBusy: false, isReplyMenuActive: false, showReplyMenu: () => { });
        flow.Update(isDialogueUiBusy: false, isReplyMenuActive: false, showReplyMenu: () => throw new InvalidOperationException("reply menu should only show once"));

        Assert.True(flow.IsFinished);
        Assert.Equal(1, abandonCallbacks);
    }
}
