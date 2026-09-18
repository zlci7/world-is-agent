using System.Collections.Generic;
using Microsoft.Xna.Framework.Graphics;
using StardewModdingAPI;
using StardewValley;

namespace GameAgent.Stardew.Integrations.MailFramework;

/// <summary>
/// The Adapter's own letter object, handed to MFM through the locally declared
/// <see cref="ILetter"/>. SMAPI bridges it to MFM's own interface when the call crosses the
/// boundary, which is exactly what the mail bridge probe verifies.
/// </summary>
internal sealed class MailLetter : ILetter
{
    private readonly ITranslationHelper translation;

    public MailLetter(ITranslationHelper translation) => this.translation = translation;

    public string Id { get; set; } = string.Empty;

    public string Text { get; set; } = string.Empty;

    public string? GroupId { get; set; }

    public string? Title { get; set; }

    public List<Item>? Items { get; set; }

    public string? Recipe { get; set; }

    public int WhichBG { get; set; }

    public Texture2D? LetterTexture { get; set; }

    public int? TextColor { get; set; }

    public Texture2D? UpperRightCloseButtonTexture { get; set; }

    public bool AutoOpen { get; set; }

    // MFM reads I18N when resolving translated title/text. Supplying this mod's helper keeps
    // the probe honest: a null here would fail for a reason unrelated to interface bridging.
    public ITranslationHelper I18N => this.translation;
}
