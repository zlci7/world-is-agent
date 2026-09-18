using System.Text.Json;
using GameAgent.Stardew.Integrations.MailFramework;
using GameAgent.Stardew.Runtime;
using StardewModdingAPI;
using StardewValley;

namespace GameAgent.Stardew.Diagnostics;

/// <summary>
/// Opt-in probe for the Mail Framework Mod integration boundary (Phase10.1 §4.2.2).
/// <para>
/// It answers two questions separately. First, can SMAPI map MFM's API onto the Adapter's
/// locally declared contract, and can the Adapter's own letter object and delegates cross that
/// boundary? Second, can the two static MailController entry points be found reflectively?
/// They fail for unrelated reasons, so the probe reports them apart rather than stopping at the
/// first one.
/// </para>
/// <para>
/// The probe deliberately uses fixed text and no model input: it exercises the mechanism, not
/// the validation rules, and must not be confused with a real capability call.
/// </para>
/// </summary>
internal static class StardewMailProbe
{
    public const string CommandName = "gameagent_mail_probe";

    // Stable id so repeated probe runs replace the previous letter instead of accumulating in
    // MFM's repository. MFM strips spaces from ids, so this must contain none.
    private const string ProbeMailId = "wia.probe.bridge";

    private const string ProbeTitle = "GameAgent bridge probe";
    private const string ProbeBody = "GameAgent mail bridge probe. If you can read this, the Adapter reached Mail Framework Mod.";

    private static readonly JsonSerializerOptions Json = new() { PropertyNamingPolicy = JsonNamingPolicy.CamelCase };

    public static void Install(IModHelper helper, IMonitor monitor, AdapterConfig config)
    {
        if (!config.EnableMailBridgeProbe)
            return;

        helper.ConsoleCommands.Add(
            CommandName,
            "Probe the Mail Framework Mod integration boundary. Requires MailFrameworkMod and a loaded save. " +
            "Usage: gameagent_mail_probe",
            (_, args) => Run(helper, monitor, args));

        helper.Events.GameLoop.GameLaunched += (_, _) => LogResolution(helper, monitor);

        monitor.Log(
            "GameAgent mail bridge probe armed. Load a save and run " + CommandName + ".",
            LogLevel.Info);
    }

    /// <summary>Resolve only: proves API mapping without touching the world.</summary>
    private static void LogResolution(IModHelper helper, IMonitor monitor)
    {
        bool resolved = MailFrameworkIntegration.TryResolve(helper, monitor, out _, out string code);
        Emit(monitor, "resolve", code, detail: resolved ? "mapped" : "not mapped");
    }

    private static void Run(IModHelper helper, IMonitor monitor, string[] args)
    {
        if (args.Length != 0)
        {
            Emit(monitor, "aborted", "invalid_arguments");
            return;
        }

        if (!Context.IsWorldReady)
        {
            Emit(monitor, "aborted", "world_not_ready");
            return;
        }

        // Step 1: interface mapping. Reaching this point at all means the contract matched.
        if (!MailFrameworkIntegration.TryResolve(helper, monitor, out MailFrameworkIntegration? integration, out string resolveCode))
        {
            Emit(monitor, "resolve", resolveCode);
            Emit(monitor, "verdict", "bridge_failed", detail: "API contract did not map");
            return;
        }

        Emit(monitor, "resolve", resolveCode, detail: "mapped");

        // Step 2: bridging. Our letter object and our delegates crossing the mapped boundary.
        string registerCode = integration!.RegisterLetter(
            ProbeMailId,
            ProbeTitle,
            ProbeBody,
            letter =>
            {
                // The Adapter owns this write: MFM removes its temporary "id + suffix" marker on
                // close and does not record the final id itself (Phase10.1 §4.2.1).
                if (!Game1.player.mailReceived.Contains(letter.Id))
                    Game1.player.mailReceived.Add(letter.Id);
                Emit(monitor, "callback_fired", "ok", detail: letter.Id);
            });

        Emit(monitor, "register", registerCode, detail: integration.LastError);

        // Step 3: delivery. Reflection over static methods, a separate concern from bridging.
        bool delivered = integration.RequestDelivery(out string deliveryCode);
        Emit(monitor, "deliver", deliveryCode, detail: $"{integration.LastError} controller={integration.ControllerSource}");

        bool read = integration.TryHasCustomMail(out bool hasCustomMail, out string hasCode);
        Emit(monitor, "has_custom_mail", hasCode, detail: read ? hasCustomMail.ToString() : integration.LastError);

        // Nothing pending is the correct resting state once the letter has been read: the
        // condition tests mailReceived, which only the callback writes. So an already-read id
        // proves both the callback and the re-delivery guard, and must not be reported as a
        // delivery failure.
        bool alreadyRead = Game1.player.mailReceived.Contains(ProbeMailId);

        string verdict = registerCode != "ok"
            ? "bridge_failed"
            : !delivered || !read
                ? "bridge_ok_delivery_incomplete"
                : hasCustomMail
                    ? "bridge_ok"
                    : alreadyRead
                        ? "bridge_ok_already_read"
                        : "bridge_ok_delivery_incomplete";

        Emit(
            monitor,
            "verdict",
            verdict,
            detail: $"mail_id={ProbeMailId} register={registerCode} deliver={deliveryCode} " +
                $"mail_received={alreadyRead} controller={integration.ControllerSource}");
    }

    private static void Emit(IMonitor monitor, string step, string code, string detail = "")
    {
        bool good = code is "ok" or "mapped" or "bridge_ok" or "bridge_ok_already_read" or "bridge_ok_delivery_incomplete";
        monitor.Log(
            "wia_mail_bridge_probe " + JsonSerializer.Serialize(new { step, code, detail }, Json),
            good ? LogLevel.Info : LogLevel.Warn);
    }
}
