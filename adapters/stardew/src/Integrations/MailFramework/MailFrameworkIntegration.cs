using System;
using System.Collections.Generic;
using System.Reflection;
using StardewModdingAPI;
using StardewValley;

namespace GameAgent.Stardew.Integrations.MailFramework;

/// <summary>
/// Optional integration with Mail Framework Mod.
/// <para>
/// MFM is never a hard dependency. Registration goes through the locally declared contract, so
/// it needs no reflection at all; only the two static <c>MailController</c> entry points are
/// resolved reflectively, because static methods have no interface for SMAPI to map.
/// </para>
/// <para>
/// Every method returns a code instead of throwing, so callers can turn a failure into an
/// ActionResult rather than a crashed main thread.
/// </para>
/// </summary>
internal sealed class MailFrameworkIntegration : IMailDelivery
{
    public const string UniqueId = "DIGUS.MailFrameworkMod";

    private const string MailControllerTypeName = "MailFrameworkMod.MailController";
    private const string UpdateMailBoxMethodName = "UpdateMailBox";
    private const string HasCustomMailMethodName = "HasCustomMail";

    private readonly IModHelper helper;
    private readonly IMailFrameworkModApi api;

    // Registration and delivery are tracked separately because they fail independently: a retry
    // after a failed delivery must not be short-circuited by an earlier successful registration.
    private readonly HashSet<string> registeredIds = new(StringComparer.Ordinal);

    private MethodInfo? updateMailBox;
    private MethodInfo? hasCustomMail;
    private bool controllerProbed;

    private MailFrameworkIntegration(IModHelper helper, IMailFrameworkModApi api)
    {
        this.helper = helper;
        this.api = api;
    }

    /// <summary>Last failure detail, for logging only. Never surfaced to the model.</summary>
    public string LastError { get; private set; } = string.Empty;

    /// <summary>Which assembly the MailController lookup settled on, for diagnostics.</summary>
    public string ControllerSource { get; private set; } = string.Empty;

    /// <summary>
    /// Resolve MFM through SMAPI's interface mapping. This deliberately checks the API only:
    /// whether the API maps is what the bridge probe answers first, and a failure to locate
    /// MailController must not mask it.
    /// </summary>
    public static bool TryResolve(
        IModHelper helper,
        IMonitor monitor,
        out MailFrameworkIntegration? integration,
        out string code)
    {
        integration = null;

        IMailFrameworkModApi? api;
        try
        {
            api = helper.ModRegistry.GetApi<IMailFrameworkModApi>(UniqueId);
        }
        catch (Exception ex)
        {
            code = MailDeliveryCodes.ApiFailed;
            monitor.Log($"GameAgent MFM API mapping failed: {ex}", LogLevel.Warn);
            return false;
        }

        if (api is null)
        {
            code = MailDeliveryCodes.Unavailable;
            return false;
        }

        integration = new MailFrameworkIntegration(helper, api);
        code = "ok";
        return true;
    }

    /// <inheritdoc />
    public bool IsRegistered(string mailId) => this.registeredIds.Contains(mailId);

    /// <inheritdoc />
    public string Register(string mailId, string? title, string body) =>
        this.Register(mailId, title, body, onRead: null);

    /// <summary>
    /// Register a letter, recording the id only when registration succeeds.
    /// <paramref name="onRead"/> is an observation hook for diagnostics; the state write below
    /// always happens.
    /// </summary>
    public string Register(string mailId, string? title, string body, Action<string>? onRead)
    {
        this.LastError = string.Empty;
        // The letter belongs to the save that is loaded now. This is captured per registration and
        // closed over by the condition below, so each letter keeps its own save identity even after
        // the player loads a different save.
        ulong registeredSaveId = Game1.uniqueIDForThisGame;
        try
        {
            this.api.RegisterLetter(
                new MailLetter
                {
                    Id = mailId,
                    Title = title,
                    Text = body,
                    // No attachments, no recipe, no auto-open: the letter must travel the normal
                    // UI path for the delivery evidence to mean anything.
                    Items = null,
                    Recipe = null,
                    AutoOpen = false,
                    WhichBG = 0,
                },
                // The condition is the only re-delivery guard on this path. Letter has no
                // Repeatable property, so MFM never checks "already delivered" itself; the
                // content-pack path gets that check from MailItem, the API path does not. With a
                // constant true the letter stays in the repository and every DayStarted ->
                // UpdateMailBox hands the player the same letter again. The callback below writes
                // the final id, and that is what turns this condition false.
                //
                // The save identity covers the other half: MFM's repository outlives the save, so
                // without it a letter written in one save would be delivered into the next one.
                condition: letter => MailDeliveryGuard.ShouldDeliver(
                    registeredSaveId,
                    Game1.uniqueIDForThisGame,
                    Game1.player.mailReceived.Contains(letter.Id)),
                callback: letter =>
                {
                    // The Adapter owns this write: MFM removes its temporary "id + suffix" marker
                    // on close and does not record the final id itself (Phase10.1 §4.2.1).
                    if (!Game1.player.mailReceived.Contains(letter.Id))
                        Game1.player.mailReceived.Add(letter.Id);
                    onRead?.Invoke(letter.Id);
                },
                dynamicItems: _ => new List<Item>());

            this.registeredIds.Add(mailId);
            return MailDeliveryCodes.Ok;
        }
        catch (Exception ex)
        {
            this.LastError = Describe(ex);
            return MailDeliveryCodes.RegisterFailed;
        }
    }

    /// <inheritdoc />
    public bool RequestDelivery(out string code)
    {
        this.LastError = string.Empty;
        if (!this.TryResolveController(out code))
            return false;
        try
        {
            this.updateMailBox!.Invoke(null, null);
            code = MailDeliveryCodes.Ok;
            return true;
        }
        catch (Exception ex)
        {
            this.LastError = Describe(ex);
            code = MailDeliveryCodes.DeliveryFailed;
            return false;
        }
    }

    /// <summary>Whether MFM currently has a custom letter queued for the mailbox.</summary>
    public bool TryHasCustomMail(out bool hasCustomMail, out string code)
    {
        this.LastError = string.Empty;
        hasCustomMail = false;
        if (!this.TryResolveController(out code))
            return false;
        try
        {
            object? value = this.hasCustomMail!.Invoke(null, null);
            hasCustomMail = value is true;
            code = MailDeliveryCodes.Ok;
            return true;
        }
        catch (Exception ex)
        {
            this.LastError = Describe(ex);
            code = "mail_probe_failed";
            return false;
        }
    }

    /// <summary>
    /// Locate MFM's assembly and pull out the two static entry points.
    /// <para>
    /// The mapped API instance cannot be used for this: <c>api.GetType()</c> is SMAPI's generated
    /// proxy, which lives in a dynamic assembly rather than MFM's. The non-generic registry call
    /// returns MFM's own object, and scanning loaded assemblies covers the case where it does not.
    /// </para>
    /// </summary>
    private bool TryResolveController(out string code)
    {
        if (this.controllerProbed)
        {
            code = this.updateMailBox is null || this.hasCustomMail is null
                ? MailDeliveryCodes.LayoutUnexpected
                : MailDeliveryCodes.Ok;
            return code == MailDeliveryCodes.Ok;
        }

        this.controllerProbed = true;

        Type? controller = FindMailController();
        this.updateMailBox = controller?.GetMethod(UpdateMailBoxMethodName, BindingFlags.Public | BindingFlags.Static);
        this.hasCustomMail = controller?.GetMethod(HasCustomMailMethodName, BindingFlags.Public | BindingFlags.Static);

        if (this.updateMailBox is null || this.hasCustomMail is null)
        {
            code = MailDeliveryCodes.LayoutUnexpected;
            return false;
        }

        code = MailDeliveryCodes.Ok;
        return true;
    }

    private Type? FindMailController()
    {
        // Preferred: the unproxied API object, whose assembly is MFM's.
        try
        {
            object? raw = this.helper.ModRegistry.GetApi(UniqueId);
            Type? direct = raw?.GetType().Assembly.GetType(MailControllerTypeName, throwOnError: false);
            if (direct is not null)
            {
                this.ControllerSource = raw!.GetType().Assembly.GetName().Name ?? "unknown";
                return direct;
            }
        }
        catch (Exception ex)
        {
            this.LastError = Describe(ex);
        }

        // Fallback: MFM's assembly is loaded (its API resolved), so scan for the type.
        foreach (Assembly assembly in AppDomain.CurrentDomain.GetAssemblies())
        {
            Type? found = assembly.GetType(MailControllerTypeName, throwOnError: false);
            if (found is not null)
            {
                this.ControllerSource = assembly.GetName().Name ?? "unknown";
                return found;
            }
        }

        return null;
    }

    /// <summary>Unwrap TargetInvocationException so the log shows MFM's real failure.</summary>
    private static string Describe(Exception ex) =>
        ex is TargetInvocationException { InnerException: not null } wrapped
            ? wrapped.InnerException!.ToString()
            : ex.ToString();
}
