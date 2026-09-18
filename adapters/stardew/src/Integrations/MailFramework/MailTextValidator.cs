using System.Collections.Generic;
using System.Text;

namespace GameAgent.Stardew.Integrations.MailFramework;

/// <summary>
/// Closed-set validation for letter title and body (Phase10.1 §3).
/// <para>
/// MFM hands both fields to the game's <c>TokenParser</c>, so text is a command channel. The
/// parser's full grammar is not known, which is why this is an allowlist: a character passes
/// only if it is explicitly recognised, never merely because it was not on a list of suspects.
/// </para>
/// <para>
/// Order matters and is fixed: normalise the representation, trim, translate newlines, then
/// apply limits and the allowlist. Normalising newlines rather than rejecting them is required
/// for this to be usable at all — the model writes real newlines and has no way to know MFM
/// spells a line break as <c>^</c>.
/// </para>
/// </summary>
internal static class MailTextValidator
{
    public const int MaxTitleLength = 80;
    public const int MaxBodyLength = 600;
    public const int MaxBodyLineBreaks = 20;
    public const int MaxPlayerNameTokens = 3;

    public const string TitleInvalidCode = "mail_title_invalid";
    public const string BodyInvalidCode = "mail_body_invalid";

    private const char PlayerNameToken = '@';
    private const char LineBreakToken = '^';

    /// <summary>
    /// Characters that pass in addition to letters, digits and the plain space. Kept explicit
    /// and deliberately small: a wider set buys nothing and every addition has to be justified
    /// against an unknown parser.
    /// </summary>
    private const string SafePunctuation =
        ",.!?;:()-'\"" +
        "，。！？、；：（）《》〈〉「」『』…—“”‘’";

    /// <summary>
    /// Validate the optional title. A null, empty or whitespace-only title means "no title" and
    /// is valid; it normalises to the empty string.
    /// </summary>
    public static bool TryValidateTitle(string? title, out string normalized, out string code)
    {
        normalized = string.Empty;
        code = string.Empty;

        if (string.IsNullOrWhiteSpace(title))
            return true;

        // A title is single line, so a newline becomes a space rather than a line-break token.
        string value = Normalize(title).Replace('\n', ' ').Trim();

        if (value.Length == 0)
            return true;

        if (value.Length > MaxTitleLength || !AllAllowed(value))
        {
            code = TitleInvalidCode;
            return false;
        }

        normalized = value;
        return true;
    }

    /// <summary>Validate the required body.</summary>
    public static bool TryValidateBody(string? body, out string normalized, out string code)
    {
        normalized = string.Empty;
        code = string.Empty;

        if (string.IsNullOrWhiteSpace(body))
        {
            code = BodyInvalidCode;
            return false;
        }

        string value = Normalize(body);
        value = value.Replace("\n", LineBreakToken.ToString());

        if (value.Length > MaxBodyLength ||
            CountOf(value, LineBreakToken) > MaxBodyLineBreaks ||
            CountOf(value, PlayerNameToken) > MaxPlayerNameTokens ||
            !AllAllowed(value))
        {
            code = BodyInvalidCode;
            return false;
        }

        normalized = value;
        return true;
    }

    /// <summary>
    /// Fold to a composed form, unify line endings and trim. Composing first keeps accented
    /// letters written as a base plus a combining mark from failing the allowlist, since a
    /// combining mark is neither a letter nor a whitelisted punctuation character.
    /// </summary>
    private static string Normalize(string value) =>
        value.Normalize(NormalizationForm.FormC).Replace("\r\n", "\n").Replace('\r', '\n').Trim();

    /// <summary>
    /// A character passes only when it is explicitly recognised. This is the whole point of the
    /// method: the failure mode to avoid is releasing anything merely because nobody listed it.
    /// </summary>
    private static bool AllAllowed(string value)
    {
        foreach (char character in value)
        {
            if (character == ' ' || character == PlayerNameToken || character == LineBreakToken)
                continue;
            if (char.IsLetterOrDigit(character))
                continue;
            if (SafePunctuation.IndexOf(character) >= 0)
                continue;
            return false;
        }

        return true;
    }

    private static int CountOf(string value, char character)
    {
        int count = 0;
        foreach (char current in value)
        {
            if (current == character)
                count++;
        }

        return count;
    }
}
