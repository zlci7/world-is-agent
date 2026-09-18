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
internal sealed class MailFrameworkIntegration
{
    public const string UniqueId = "DIGUS.MailFrameworkMod";

    private const string MailControllerTypeName = "MailFrameworkMod.MailController";
    private const string UpdateMailBoxMethodName = "UpdateMailBox";
    private const string HasCustomMailMethodName = "HasCustomMail";

    private readonly IModHelper helper;
    private readonly IMailFrameworkModApi api;
    private readonly ITranslationHelper translation;

    private MethodInfo? updateMailBox;
    private MethodInfo? hasCustomMail;
    private bool controllerProbed;

    private MailFrameworkIntegration(IModHelper helper, IMailFrameworkModApi api, ITranslationHelper translation)
    {
        this.helper = helper;
        this.api = api;
        this.translation = translation;
    }

    /// <summary>Last failure detail, for logging only. Never surfaced to the model.</summary>
    public string LastError { get; private set; } = string.Empty;

    /// <summary>Which assembly the MailController lookup settled on, for diagnostics.</summary>
    public string ControllerSource { get; private set; } = string.Empty;

    /// <summary>
    /// Resolve MFM through SMAPI's interface mapping. This deliberately checks the API only:
    /// whether the API maps is the separate question the bridge probe answers first, and a
    /// failure to locate MailController must not mask it.
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
            code = "mail_framework_api_failed";
            monitor.Log($"GameAgent MFM API mapping failed: {ex}", LogLevel.Warn);
            return false;
        }

        if (api is null)
        {
            code = "mail_framework_unavailable";
            return false;
        }

        integration = new MailFrameworkIntegration(helper, api, helper.Translation);
        code = "ok";
        return true;
    }

    /// <summary>
    /// Register a letter. <paramref name="onRead"/> runs when the player closes the letter.
    /// </summary>
    public string RegisterLetter(string id, string? title, string text, Action<ILetter> onRead)
    {
        this.LastError = string.Empty;
        try
        {
            this.api.RegisterLetter(
                new MailLetter(this.translation)
                {
                    Id = id,
                    Title = title,
                    Text = text,
                    // No attachments, no recipe, no auto-open: the letter must travel the normal
                    // UI path for the delivery evidence to mean anything.
                    Items = null,
                    Recipe = null,
                    AutoOpen = false,
                    WhichBG = 0,
                },
                condition: _ => true,
                callback: letter => onRead(letter),
                dynamicItems: _ => new List<Item>());
            return "ok";
        }
        catch (Exception ex)
        {
            this.LastError = Describe(ex);
            return "mail_register_failed";
        }
    }

    /// <summary>Push registered letters into the player's mailbox immediately.</summary>
    public bool RequestDelivery(out string code)
    {
        this.LastError = string.Empty;
        if (!this.TryResolveController(out code))
            return false;
        try
        {
            this.updateMailBox!.Invoke(null, null);
            code = "ok";
            return true;
        }
        catch (Exception ex)
        {
            this.LastError = Describe(ex);
            code = "mail_delivery_failed";
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
            code = "ok";
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
                ? "mail_framework_layout_unexpected"
                : "ok";
            return code == "ok";
        }

        this.controllerProbed = true;

        Type? controller = FindMailController();
        this.updateMailBox = controller?.GetMethod(UpdateMailBoxMethodName, BindingFlags.Public | BindingFlags.Static);
        this.hasCustomMail = controller?.GetMethod(HasCustomMailMethodName, BindingFlags.Public | BindingFlags.Static);

        if (this.updateMailBox is null || this.hasCustomMail is null)
        {
            code = "mail_framework_layout_unexpected";
            return false;
        }

        code = "ok";
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
