using GameAgent.Stardew.Integrations.MailFramework;
using Xunit;

namespace GameAgent.Stardew.Tests;

/// <summary>
/// Phase10.1 §3: the letter text boundary. MFM passes both fields to the game's TokenParser,
/// whose grammar is not known, so the contract under test is that nothing passes unless it is
/// explicitly recognised.
/// </summary>
public sealed class MailTextValidatorTests
{
    [Theory]
    [InlineData("line one\nline two", "line one^line two")]
    [InlineData("line one\r\nline two", "line one^line two")]
    [InlineData("line one\rline two", "line one^line two")]
    [InlineData("a\n\nb", "a^^b")]
    [InlineData("trailing\n", "trailing")]
    [InlineData("  padded  ", "padded")]
    public void BodyNormalisesNewlinesToTokens(string input, string expected)
    {
        Assert.True(MailTextValidator.TryValidateBody(input, out string normalized, out string code), code);
        Assert.Equal(expected, normalized);
    }

    [Theory]
    [InlineData("Dear diary\r\nsecond line", "Dear diary second line")]
    [InlineData("only one line", "only one line")]
    public void TitleCollapsesNewlinesToSpaces(string input, string expected)
    {
        Assert.True(MailTextValidator.TryValidateTitle(input, out string normalized, out string code), code);
        Assert.Equal(expected, normalized);
    }

    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData("   ")]
    public void TitleMayBeAbsent(string? title)
    {
        Assert.True(MailTextValidator.TryValidateTitle(title, out string normalized, out string code), code);
        Assert.Equal(string.Empty, normalized);
    }

    // Every ASCII punctuation character, so a future widening of the safe set has to be
    // deliberate rather than accidental.
    [Theory]
    [InlineData('!', true)]
    [InlineData('"', true)]
    [InlineData('#', false)]
    [InlineData('$', false)]
    [InlineData('%', false)]
    [InlineData('&', false)]
    [InlineData('\'', true)]
    [InlineData('(', true)]
    [InlineData(')', true)]
    [InlineData('*', false)]
    [InlineData('+', false)]
    [InlineData(',', true)]
    [InlineData('-', true)]
    [InlineData('.', true)]
    [InlineData('/', false)]
    [InlineData(':', true)]
    [InlineData(';', true)]
    [InlineData('<', false)]
    [InlineData('=', false)]
    [InlineData('>', false)]
    [InlineData('@', true)]
    [InlineData('[', false)]
    [InlineData('\\', false)]
    [InlineData(']', false)]
    [InlineData('^', true)]
    [InlineData('_', false)]
    [InlineData('`', false)]
    [InlineData('{', false)]
    [InlineData('|', false)]
    [InlineData('}', false)]
    [InlineData('~', false)]
    public void BodyAcceptsOnlyExplicitAsciiPunctuation(char character, bool expected)
    {
        string body = $"a{character}b";
        Assert.Equal(expected, MailTextValidator.TryValidateBody(body, out _, out _));
    }

    [Theory]
    [InlineData("\t")]
    [InlineData("\0")]
    [InlineData("\v")]
    [InlineData("\f")]
    public void BodyRejectsControlCharacters(string character)
    {
        Assert.False(MailTextValidator.TryValidateBody($"a{character}b", out _, out string code));
        Assert.Equal(MailTextValidator.BodyInvalidCode, code);
    }

    [Fact]
    public void BodyAcceptsLettersDigitsAndCjk()
    {
        const string body = "你好，农夫 Linus 第 3 次写信：请保重 (see you)。";
        Assert.True(MailTextValidator.TryValidateBody(body, out string normalized, out string code), code);
        Assert.Equal(body, normalized);
    }

    [Fact]
    public void BodyAcceptsComposedAndDecomposedAccents()
    {
        // "café" precomposed and as e + combining acute. Both must survive; the combining mark
        // is not itself a letter, which is why normalisation runs before the allowlist.
        Assert.True(MailTextValidator.TryValidateBody("caf\u00e9", out string precomposed, out _));
        Assert.True(MailTextValidator.TryValidateBody("cafe\u0301", out string decomposed, out _));
        Assert.Equal(precomposed, decomposed);
    }

    [Fact]
    public void BodyRejectsEmptyAndWhitespaceOnly()
    {
        Assert.False(MailTextValidator.TryValidateBody("", out _, out string emptyCode));
        Assert.Equal(MailTextValidator.BodyInvalidCode, emptyCode);
        Assert.False(MailTextValidator.TryValidateBody("   \n  ", out _, out string blankCode));
        Assert.Equal(MailTextValidator.BodyInvalidCode, blankCode);
    }

    [Fact]
    public void BodyRejectsOverlongText()
    {
        string body = new('a', MailTextValidator.MaxBodyLength + 1);
        Assert.False(MailTextValidator.TryValidateBody(body, out _, out string code));
        Assert.Equal(MailTextValidator.BodyInvalidCode, code);
    }

    [Fact]
    public void BodyAcceptsTextAtTheLengthLimit()
    {
        string body = new('a', MailTextValidator.MaxBodyLength);
        Assert.True(MailTextValidator.TryValidateBody(body, out _, out _));
    }

    [Fact]
    public void BodyRejectsTooManyLineBreaks()
    {
        string body = "a" + new string('^', MailTextValidator.MaxBodyLineBreaks + 1) + "b";
        Assert.False(MailTextValidator.TryValidateBody(body, out _, out string code));
        Assert.Equal(MailTextValidator.BodyInvalidCode, code);
    }

    [Fact]
    public void BodyAcceptsLineBreaksAtTheLimit()
    {
        string body = "a" + new string('^', MailTextValidator.MaxBodyLineBreaks) + "b";
        Assert.True(MailTextValidator.TryValidateBody(body, out _, out _));
    }

    [Fact]
    public void BodyRejectsTooManyPlayerNameTokens()
    {
        string body = new string('@', MailTextValidator.MaxPlayerNameTokens + 1);
        Assert.False(MailTextValidator.TryValidateBody(body, out _, out string code));
        Assert.Equal(MailTextValidator.BodyInvalidCode, code);
    }

    [Fact]
    public void TitleRejectsOverlongText()
    {
        string title = new('a', MailTextValidator.MaxTitleLength + 1);
        Assert.False(MailTextValidator.TryValidateTitle(title, out _, out string code));
        Assert.Equal(MailTextValidator.TitleInvalidCode, code);
    }
}
