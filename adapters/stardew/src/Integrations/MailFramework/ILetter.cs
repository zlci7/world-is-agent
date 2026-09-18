using System.Collections.Generic;
using Microsoft.Xna.Framework.Graphics;
using StardewModdingAPI;
using StardewValley;

namespace GameAgent.Stardew.Integrations.MailFramework;

/// <summary>
/// Local copy of Mail Framework Mod's letter contract. Members and order mirror
/// MailFrameworkMod/Api/ILetter.cs; see <see cref="IMailFrameworkModApi"/> for why this is
/// declared locally rather than referenced, and why it must stay public.
/// </summary>
public interface ILetter
{
    string Id { get; }

    string Text { get; }

    string? GroupId { get; }

    string? Title { get; }

    List<Item>? Items { get; }

    string? Recipe { get; }

    int WhichBG { get; }

    Texture2D? LetterTexture { get; }

    int? TextColor { get; }

    Texture2D? UpperRightCloseButtonTexture { get; }

    bool AutoOpen { get; }

    ITranslationHelper I18N { get; }
}
