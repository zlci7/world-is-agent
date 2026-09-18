using GameAgent.Stardew.Integrations.MailFramework;

namespace GameAgent.Stardew.Capabilities;

/// <summary>How send_mail ended, which decides the ActionResult status the caller sends.</summary>
internal enum SendMailDisposition
{
    Succeeded,
    Rejected,
    Failed,
}

/// <summary>Outcome of one send_mail call.</summary>
internal readonly record struct SendMailOutcome(SendMailDisposition Disposition, string Code, string Message)
{
    public static SendMailOutcome Succeeded() => new(SendMailDisposition.Succeeded, string.Empty, string.Empty);

    public static SendMailOutcome Rejected(string code, string message) =>
        new(SendMailDisposition.Rejected, code, message);

    public static SendMailOutcome Failed(string code, string message) =>
        new(SendMailDisposition.Failed, code, message);
}

/// <summary>
/// The send_mail decision: validate, then register once, then always attempt delivery.
/// <para>
/// Registration and delivery are separate facts on purpose. Collapsing them into one "done" flag
/// would make a retry after a failed delivery return success because the letter is already in
/// the repository, leaving the action permanently undelivered. Idempotency here removes
/// duplicate registration only; it must never remove the delivery attempt.
/// </para>
/// </summary>
internal static class SendMailCapability
{
    /// <summary>
    /// Run one send_mail action. <paramref name="delivery"/> is null when MFM was not available
    /// at GameLaunched, in which case the capability is not published and this is a defensive
    /// path rather than the normal one.
    /// </summary>
    public static SendMailOutcome Send(string mailId, string? title, string body, IMailDelivery? delivery)
    {
        if (delivery is null)
            return SendMailOutcome.Rejected(MailDeliveryCodes.Unavailable, "Mail Framework Mod is not available");

        // Validation runs before any MFM call, so a rejected text never leaves partial state.
        if (!MailTextValidator.TryValidateTitle(title, out string normalisedTitle, out string titleCode))
            return SendMailOutcome.Rejected(titleCode, "mail title did not pass validation");

        if (!MailTextValidator.TryValidateBody(body, out string normalisedBody, out string bodyCode))
            return SendMailOutcome.Rejected(bodyCode, "mail body did not pass validation");

        if (!delivery.IsRegistered(mailId))
        {
            string registerCode = delivery.Register(
                mailId,
                normalisedTitle.Length == 0 ? null : normalisedTitle,
                normalisedBody);
            if (registerCode != MailDeliveryCodes.Ok)
                return SendMailOutcome.Failed(registerCode, "registering the letter failed");
        }

        // Reached even when the letter was already registered, so a previous delivery failure can
        // actually recover.
        if (!delivery.RequestDelivery(out string deliveryCode))
            return SendMailOutcome.Failed(deliveryCode, "delivering the letter failed");

        return SendMailOutcome.Succeeded();
    }
}
