namespace GameAgent.Stardew.Integrations.MailFramework;

/// <summary>
/// The narrow set of mail operations <c>send_mail</c> needs. Keeping it this small is what lets
/// the send decision logic be exercised without MFM or a running game.
/// </summary>
internal interface IMailDelivery
{
    /// <summary>
    /// Whether this Adapter run has already registered the mail id. Deliberately Adapter-owned
    /// bookkeeping rather than a query against MFM: MFM's GetLetter wraps a failed lookup in a
    /// non-null ApiLetter, so it cannot answer this, and its repository is its own detail.
    /// </summary>
    bool IsRegistered(string mailId);

    /// <summary>Register the letter. Returns <c>ok</c> on success, otherwise a failure code.</summary>
    string Register(string mailId, string? title, string body);

    /// <summary>Push registered letters into the mailbox. Returns false with a code on failure.</summary>
    bool RequestDelivery(out string code);
}
