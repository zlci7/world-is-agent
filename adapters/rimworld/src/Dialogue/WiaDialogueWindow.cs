using System;
using UnityEngine;
using Verse;

namespace Wia.RimWorld.Dialogue
{
    /// <summary>
    /// The window that shows a colonist's line and collects the player's reply.
    ///
    /// The window outlives the action that opened it. present_dialogue succeeds as soon as the line
    /// is on screen - waiting for the player is not that action's job - so the reply is a separate
    /// event that starts a new turn, and closing the window without replying is not a failure and
    /// leaves nothing hanging.
    /// </summary>
    internal sealed class WiaDialogueWindow : Window
    {
        private const float Padding = 12f;
        private const float ButtonHeight = 30f;
        private const float ButtonGap = 6f;

        private readonly string colonistName;
        private readonly string line;
        private readonly string[] replyOptions;
        private readonly bool allowFreeText;
        private readonly Action<string, string, int?> onSubmit;
        private readonly Action onAbandoned;

        private string freeText = string.Empty;
        private bool resolved;

        public WiaDialogueWindow(
            string colonistName,
            string line,
            string[] replyOptions,
            bool allowFreeText,
            Action<string, string, int?> onSubmit,
            Action onAbandoned)
        {
            this.colonistName = colonistName ?? string.Empty;
            this.line = line ?? string.Empty;
            this.replyOptions = replyOptions ?? new string[0];
            this.allowFreeText = allowFreeText;
            this.onSubmit = onSubmit;
            this.onAbandoned = onAbandoned;

            this.doCloseX = true;
            this.absorbInputAroundWindow = true;
            this.closeOnClickedOutside = false;
            this.closeOnCancel = true;
        }

        public override Vector2 InitialSize
        {
            get { return new Vector2(680f, 360f); }
        }

        /// <summary>
        /// Every close goes through here - the X button, Escape, or the code below - so it is the one
        /// place that can tell the adapter the conversation ended without a reply. The guard makes it
        /// fire once: submitting a reply closes the window too, and that is not an abandonment.
        /// </summary>
        public override void PreClose()
        {
            base.PreClose();

            if (this.resolved)
            {
                return;
            }

            this.resolved = true;
            this.onAbandoned();
        }

        public override void DoWindowContents(Rect inRect)
        {
            Rect header = new Rect(inRect.x, inRect.y, inRect.width, 28f);
            Text.Font = GameFont.Medium;
            Widgets.Label(header, this.colonistName);
            Text.Font = GameFont.Small;

            Rect body = new Rect(inRect.x, header.yMax + Padding, inRect.width, inRect.height - header.height - Padding * 2f);
            float optionsHeight = (ButtonHeight + ButtonGap) * (this.replyOptions.Length + (this.HasFreeTextRow ? 1 : 0));
            Rect lineRect = new Rect(body.x, body.y, body.width, Mathf.Max(40f, body.height - optionsHeight - Padding));
            Widgets.Label(lineRect, this.line);

            float y = lineRect.yMax + Padding;
            for (int index = 0; index < this.replyOptions.Length; index++)
            {
                int captured = index;
                Rect button = new Rect(body.x, y, body.width, ButtonHeight);
                if (Widgets.ButtonText(button, this.replyOptions[index]))
                {
                    this.Submit("option", this.replyOptions[index], captured);
                    return;
                }

                y += ButtonHeight + ButtonGap;
            }

            if (this.HasFreeTextRow)
            {
                float sendWidth = 90f;
                Rect field = new Rect(body.x, y, body.width - sendWidth - ButtonGap, ButtonHeight);
                Rect send = new Rect(field.xMax + ButtonGap, y, sendWidth, ButtonHeight);

                this.freeText = Widgets.TextField(field, this.freeText, PresentDialogueParser.MaxTextChars);

                bool canSend = !string.IsNullOrWhiteSpace(this.freeText);
                if (Widgets.ButtonText(send, "说", active: canSend) && canSend)
                {
                    this.Submit("free_text", this.freeText, null);
                }
            }
        }

        /// <summary>
        /// Free text is offered exactly when the model allowed it. An ending line offers neither
        /// options nor input, which is what makes the two forms visibly different to the player.
        /// </summary>
        private bool HasFreeTextRow
        {
            get { return this.replyOptions.Length > 0 && this.allowFreeText; }
        }

        private void Submit(string inputKind, string text, int? optionIndex)
        {
            if (this.resolved)
            {
                return;
            }

            this.resolved = true;

            // Sent before the window closes so the reply cannot be lost to a close handler that runs
            // first.
            this.onSubmit(inputKind, text, optionIndex);
            this.Close();
        }
    }
}
