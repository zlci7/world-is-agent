using System;
using System.Collections.Generic;
using StardewModdingAPI;
using StardewValley;

namespace GameAgent.Stardew.Integrations.MailFramework;

/// <summary>
/// Local copy of Mail Framework Mod's public API contract.
/// <para>
/// MailFrameworkMod.dll is deliberately not referenced: MFM is an optional dependency, and a
/// hard reference would break loading for players who do not have it installed. SMAPI maps the
/// real API onto this interface, so the members below must stay in sync with
/// MailFrameworkMod/Api/IMailFrameworkModApi.cs. A mismatch makes GetApi return null rather
/// than fail loudly, which is why the mail bridge probe exists.
/// </para>
/// <para>
/// This must stay public. SMAPI generates the mapping proxy at runtime, and the proxy type
/// lives in a dynamic assembly that cannot implement an internal interface.
/// </para>
/// </summary>
public interface IMailFrameworkModApi
{
    void RegisterContentPack(IContentPack contentPack);

    void RegisterLetter(
        ILetter iLetter,
        Func<ILetter, bool> condition,
        Action<ILetter> callback,
        Func<ILetter, List<Item>> dynamicItems);

    ILetter GetLetter(string id);

    string GetMailDataString(string id);
}
