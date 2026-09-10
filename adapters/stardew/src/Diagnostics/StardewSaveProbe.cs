using System.Diagnostics;
using System.Globalization;
using System.Text.Json;
using GameAgent.Stardew.Runtime;
using StardewModdingAPI;
using StardewModdingAPI.Events;
using StardewValley;

namespace GameAgent.Stardew.Diagnostics;

internal sealed class StardewSaveProbe : ISaveProbeHost, IDisposable
{
    private readonly IModHelper helper;
    private readonly IMonitor monitor;
    private readonly SaveProbeTransport transport = new();
    private readonly Stopwatch clock = Stopwatch.StartNew();
    private SaveProbe? probe;
    private bool disposed;
    private static readonly JsonSerializerOptions Json = new() { PropertyNamingPolicy = JsonNamingPolicy.CamelCase };

    private StardewSaveProbe(IModHelper helper, IMonitor monitor)
    { this.helper = helper; this.monitor = monitor; }

    public static StardewSaveProbe? Create(IModHelper helper, IMonitor monitor, AdapterConfig config)
    {
        StardewSaveProbe? live = null;
        SaveProbe.Install(config, () =>
        {
            live = new(helper, monitor);
            return new(config, live, live.transport.Start, (task, timeout) => task.Wait(timeout),
                () => live.clock.ElapsedMilliseconds, live.Emit);
        }, (name, help, callback) => helper.ConsoleCommands.Add(name, help, (_, args) => callback(args)),
            value => live!.Attach(value));
        return live;
    }

    private void Attach(SaveProbe value)
    {
        probe = value;
        helper.Events.GameLoop.Saving += OnSaving;
        helper.Events.GameLoop.Saved += OnSaved;
        helper.Events.GameLoop.SaveLoaded += OnSaveLoaded;
        helper.Events.GameLoop.ReturnedToTitle += OnReturnedToTitle;
    }

    public bool WorldReady => Context.IsWorldReady;
    public bool HasAuthority => Context.IsMainPlayer && !Context.IsMultiplayer;
    public SaveProbeIdentity Capture() => new(
        $"{Constants.SaveFolderName}:{Game1.uniqueIDForThisGame.ToString(CultureInfo.InvariantCulture)}",
        $"{Game1.year}-{Game1.currentSeason}-{Game1.dayOfMonth}", Game1.timeOfDay);
    public void Write(Dictionary<string, string> marker) => helper.Data.WriteSaveData(SaveProbe.DataKey, marker);
    public Dictionary<string, string>? Read() => helper.Data.ReadSaveData<Dictionary<string, string>>(SaveProbe.DataKey);

    private void OnSaving(object? sender, SavingEventArgs e) => probe!.Saving();
    private void OnSaved(object? sender, SavedEventArgs e) => probe!.Saved();
    private void OnSaveLoaded(object? sender, SaveLoadedEventArgs e) => probe!.SaveLoaded();
    private void OnReturnedToTitle(object? sender, ReturnedToTitleEventArgs e) => probe!.WorldChanged();
    private void Emit(object evidence) => monitor.Log(
        "phase9_save_feasibility " + JsonSerializer.Serialize(evidence, Json), LogLevel.Info);

    public void Dispose()
    {
        if (disposed) return;
        disposed = true;
        probe?.Dispose();
        helper.Events.GameLoop.Saving -= OnSaving;
        helper.Events.GameLoop.Saved -= OnSaved;
        helper.Events.GameLoop.SaveLoaded -= OnSaveLoaded;
        helper.Events.GameLoop.ReturnedToTitle -= OnReturnedToTitle;
        transport.Dispose();
    }
}
