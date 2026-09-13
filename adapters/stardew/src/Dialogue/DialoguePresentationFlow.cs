using System;

namespace GameAgent.Stardew.Dialogue;

public sealed class DialoguePresentationFlow
{
    private readonly bool shouldShowReplyMenu;
    private readonly Action onDisplayed;
    private readonly Action onAbandoned;
    private readonly Action onFinished;
    private PresentationStage stage = PresentationStage.NotStarted;
    private bool observedNpcDialogue;
    private bool displayed;
    private bool suppressAbandon;

    public DialoguePresentationFlow(
        bool shouldShowReplyMenu,
        Action onDisplayed,
        Action onAbandoned,
        Action? onFinished = null
    )
    {
        this.shouldShowReplyMenu = shouldShowReplyMenu;
        this.onDisplayed = onDisplayed;
        this.onAbandoned = onAbandoned;
        this.onFinished = onFinished ?? (() => { });
    }

    public bool IsFinished { get; private set; }

    public void Start(Action showNpcLine)
    {
        if (this.stage != PresentationStage.NotStarted || this.IsFinished)
            return;

        showNpcLine();
        this.MarkDisplayed();
        this.stage = PresentationStage.ShowingNpcLine;
    }

    public void Update(bool isDialogueUiBusy, bool isReplyMenuActive, Action showReplyMenu)
    {
        if (this.IsFinished)
            return;

        switch (this.stage)
        {
            case PresentationStage.ShowingNpcLine:
                this.UpdateShowingNpcLine(isDialogueUiBusy, showReplyMenu);
                break;
            case PresentationStage.ShowingReplyMenu:
                this.UpdateShowingReplyMenu(isReplyMenuActive);
                break;
        }
    }

    public void MarkSubmitted()
    {
        this.Finish(abandoned: false);
    }

    public void FinishWithoutAbandon()
    {
        this.Finish(abandoned: false);
    }

    public void CloseWithoutSubmission(Action closeVisibleUi)
    {
        if (this.IsFinished)
            return;

        this.suppressAbandon = true;
        closeVisibleUi();
        this.Finish(abandoned: false);
    }

    public void Abandon()
    {
        if (this.IsFinished)
            return;

        this.Finish(abandoned: !this.suppressAbandon);
    }

    private void UpdateShowingNpcLine(bool isDialogueUiBusy, Action showReplyMenu)
    {
        if (isDialogueUiBusy)
        {
            this.observedNpcDialogue = true;
            return;
        }

        if (!this.observedNpcDialogue)
            return;

        if (!this.shouldShowReplyMenu)
        {
            this.Finish(abandoned: false);
            return;
        }

        showReplyMenu();
        this.stage = PresentationStage.ShowingReplyMenu;
    }

    private void UpdateShowingReplyMenu(bool isReplyMenuActive)
    {
        if (isReplyMenuActive)
            return;

        this.Abandon();
    }

    private void MarkDisplayed()
    {
        if (this.displayed)
            return;

        this.displayed = true;
        this.onDisplayed();
    }

    private void Finish(bool abandoned)
    {
        if (this.IsFinished)
            return;
        this.IsFinished = true;
        if (abandoned)
            this.onAbandoned();
        this.onFinished();
    }

    private enum PresentationStage
    {
        NotStarted,
        ShowingNpcLine,
        ShowingReplyMenu,
    }
}
