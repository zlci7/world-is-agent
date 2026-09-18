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

    private readonly IMailFrameworkModApi api;
    private readonly ITranslationHelper translation;
    private readonly MethodInfo updateMailBox;
    private readonly MethodInfo hasCustomMail;

    private MailFrameworkIntegration(
        IMailFrameworkModApi api,
        ITranslationHelper translation,
        MethodInfo updateMailBox,
        MethodInfo hasCustomMail)
    {
        this.api = api;
        this.translation = translation;
        this.updateMailBox = updateMailBox;
        this.hasCustomMail = hasCustomMail;
    }

    /// <summary>Last failure detail, for logging only. Never surfaced to the model.</summary>
    public string LastError { get; private set; } = string.Empty;

    /// <summary>
    /// Resolve MFM through SMAPI's interface mapping. Returns false with a specific code rather
    /// than throwing, so the Adapter loads normally when MFM is absent.
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

        // The API instance is MFM's own object, so its assembly is MFM's assembly. This is more
        // reliable than probing the Mods directory for a file path.
        Type? controller = api.GetType().Assembly.GetType(MailControllerTypeName, throwOnError: false);
        MethodInfo? update = controller?.GetMethod(
            UpdateMailBoxMethodName, BindingFlags.Public | BindingFlags.Static);
        MethodInfo? has = controller?.GetMethod(
            HasCustomMailMethodName, BindingFlags.Public | BindingFlags.Static);

        if (controller is null || update is null || has is null)
        {
            code = "mail_framework_layout_unexpected";
            monitor.Log(
                $"GameAgent MFM layout unexpected: controller={controller is not null} " +
                $"update={update is not null} hasCustomMail={has is not null}",
                LogLevel.Warn);
            return false;
        }

        integration = new MailFrameworkIntegration(api, helper.Translation, update, has);
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
        try
        {
            this.updateMailBox.Invoke(null, null);
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
        try
        {
            object? value = this.hasCustomMail.Invoke(null, null);
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

    /// <summary>Unwrap TargetInvocationException so the log shows MFM's real failure.</summary>
    private static string Describe(Exception ex) =>
        ex is TargetInvocationException { InnerException: not null } wrapped
            ? wrapped.InnerException!.ToString()
            : ex.ToString();
}
