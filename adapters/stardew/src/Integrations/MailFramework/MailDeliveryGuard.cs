namespace GameAgent.Stardew.Integrations.MailFramework;

/// <summary>
/// Decides whether a letter that was registered through the MailFrameworkMod API may be
/// delivered into the save that is loaded right now.
/// </summary>
/// <remarks>
/// MailFrameworkMod keeps API-registered letters in <c>MailRepository.Letters</c>, which is a
/// static list that is never cleared: returning to the title screen only calls
/// <c>MailController.UnloadMailBox()</c>, and that clears the mailbox queue, not the repository.
/// Loading another save re-adds the content-pack letters but leaves the registered ones in place.
/// The letter condition is the only thing standing between the repository and the player's
/// mailbox, so the condition has to carry the identity of the save that created the letter.
/// Without it, a letter written in one save is delivered into the next save the player loads,
/// because that save's <c>mailReceived</c> cannot contain an id it never saw.
/// </remarks>
internal static class MailDeliveryGuard
{
    public static bool ShouldDeliver(ulong registeredSaveId, ulong currentSaveId, bool alreadyRead)
    {
        return registeredSaveId == currentSaveId && !alreadyRead;
    }
}
