using System.Globalization;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using StardewModdingAPI;
using StardewValley;

namespace GameAgent.Stardew.Authoring;

/// <summary>Console-only helper that captures the player's tile as a meeting landmark.</summary>
internal static class LandmarkMarker
{
    private const int DefaultOpenStart = 600;
    private const int DefaultOpenEnd = 2200;
    private const int DefaultDepartureLeadMinutes = 10;

    public static void RegisterCommand(
        IModHelper helper,
        string assetPath,
        LandmarkCatalogStore catalogStore,
        IMonitor monitor)
    {
        helper.ConsoleCommands.Add(
            "gameagent_mark_landmark",
            "Capture the player's current tile as a meeting landmark that any NPC may travel to. " +
            "Usage: gameagent_mark_landmark <landmark_id> [departure_lead_minutes]",
            (_, args) => Run(assetPath, catalogStore, monitor, args));
    }

    public static void Run(
        string assetPath,
        LandmarkCatalogStore catalogStore,
        IMonitor monitor,
        string[] args)
    {
        if (!Context.IsWorldReady)
        {
            monitor.Log("Load a save before marking a GameAgent landmark.", LogLevel.Warn);
            return;
        }

        string landmarkId = args.Length > 0 ? args[0].Trim() : string.Empty;
        if (landmarkId.Length == 0)
        {
            monitor.Log("Usage: gameagent_mark_landmark <landmark_id> [departure_lead_minutes]", LogLevel.Warn);
            return;
        }

        string playerLocation = Game1.player.currentLocation?.NameOrUniqueName ?? string.Empty;
        if (playerLocation.Length == 0)
        {
            monitor.Log("Player location is unavailable; landmark not written.", LogLevel.Warn);
            return;
        }

        if (!TryReadDepartureLead(args, out int departureLeadMinutes))
        {
            monitor.Log("departure_lead_minutes must be a positive multiple of 10.", LogLevel.Warn);
            return;
        }

        Landmark landmark = new(
            landmarkId,
            landmarkId,
            new WorldPosition(playerLocation, Game1.player.TilePoint.X, Game1.player.TilePoint.Y),
            DefaultOpenStart,
            DefaultOpenEnd,
            departureLeadMinutes,
            new[] { new SupportedRoute(LandmarkCatalog.AnyRoute, LandmarkCatalog.AnyRoute) });

        LandmarkCatalog updated;
        try
        {
            updated = catalogStore.Current.WithLandmark(landmark);
        }
        catch (LandmarkCatalogException ex)
        {
            monitor.Log($"Landmark {landmarkId} was rejected: {ex.Code} {ex.Message}", LogLevel.Warn);
            return;
        }

        try
        {
            LandmarkAssetWriter.Write(assetPath, updated.ToJson() + Environment.NewLine);
        }
        catch (Exception ex)
        {
            monitor.Log($"Failed to write the landmarks file: {ex.Message}", LogLevel.Error);
            return;
        }

        catalogStore.Replace(updated);
        monitor.Log(
            $"GameAgent landmark {landmarkId} at {playerLocation} ({landmark.Position.X},{landmark.Position.Y}), " +
            $"any NPC from any location, departure lead {departureLeadMinutes} minutes. " +
            "Run gameagent_runtime_reconnect to advertise it to the Runtime.",
            LogLevel.Info);
    }

    private static bool TryReadDepartureLead(string[] args, out int departureLeadMinutes)
    {
        departureLeadMinutes = DefaultDepartureLeadMinutes;
        if (args.Length <= 1 || args[1].Trim().Length == 0)
            return true;
        return int.TryParse(args[1].Trim(), NumberStyles.Integer, CultureInfo.InvariantCulture, out departureLeadMinutes) &&
            departureLeadMinutes > 0 && departureLeadMinutes % 10 == 0;
    }
}
