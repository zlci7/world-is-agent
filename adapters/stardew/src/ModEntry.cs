using System;
using System.IO;
using GameAgent.Stardew.Authoring;
using GameAgent.Stardew.Capabilities;
using GameAgent.Stardew.Dialogue;
using GameAgent.Stardew.Diagnostics;
using GameAgent.Stardew.Events;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.State;
using GameAgent.Stardew.Tasks;
using StardewModdingAPI;
using StardewModdingAPI.Events;
using StardewValley;

namespace GameAgent.Stardew;

/// <summary>SMAPI entry point for the Adapter Capability Spike.</summary>
public sealed class ModEntry : Mod
{
    private AdapterConfig? config;
    private IModHelper? helper;
    private MainThreadDispatcher? dispatcher;
    private ConversationStateStore? conversationStore;
    private DialogueInteractionController? dialogueController;
    private ObservationBuilder? observationBuilder;
    private EmoteCapability? emoteCapability;
    private PresentDialogueCapability? presentDialogueCapability;
    private FacePlayerCapability? facePlayerCapability;
    private MoveToCapability? moveToCapability;
    private ApproachPlayerCapability? approachPlayerCapability;
    private ResolveMeetingCapability? resolveMeetingCapability;
    private TaskExecutionDriver? taskExecutionDriver;
    private PlayerInteractProbe? playerInteractProbe;
    private RuntimeClient? runtimeClient;
    private StardewRouteProbe? phase9RouteProbe;
    private StardewSaveProbe? phase9SaveProbe;

    public override void Entry(IModHelper helper)
    {
        this.helper = helper;
        this.config = helper.ReadConfig<AdapterConfig>();
        this.phase9SaveProbe = StardewSaveProbe.Create(helper, this.Monitor, this.config);
        if (this.config.EnablePhase9RouteProbe)
        {
            this.phase9RouteProbe = new StardewRouteProbe(helper, this.Monitor, this.config);
            helper.Events.GameLoop.UpdateTicked += (_, _) => this.phase9RouteProbe.Update();
            helper.Events.GameLoop.SaveLoaded += (_, _) => this.phase9RouteProbe.WorldChanged("save_loaded");
            helper.Events.GameLoop.ReturnedToTitle += (_, _) => this.phase9RouteProbe.WorldChanged("returned_to_title");
            helper.Events.GameLoop.DayStarted += (_, _) => this.phase9RouteProbe.WorldChanged("day_started");
        }
        this.dispatcher = new MainThreadDispatcher(this.Monitor);
        this.conversationStore = new ConversationStateStore(new ConversationIdGenerator());
        this.dialogueController = new DialogueInteractionController();
        this.observationBuilder = new ObservationBuilder(this.conversationStore);
        this.emoteCapability = new EmoteCapability();
        this.presentDialogueCapability = new PresentDialogueCapability(this.conversationStore, this.dialogueController);
        this.facePlayerCapability = new FacePlayerCapability();
        this.moveToCapability = new MoveToCapability();
        this.approachPlayerCapability = new ApproachPlayerCapability(this.moveToCapability);
        string landmarkAssetPath = Path.Combine(helper.DirectoryPath, "assets", "landmarks.json");
        LandmarkCatalogStore landmarkCatalogStore = new(LandmarkCatalog.Parse(File.ReadAllText(landmarkAssetPath)));
        this.resolveMeetingCapability = new ResolveMeetingCapability(landmarkCatalogStore);
        NpcControlLease npcControlLease = new();
        this.taskExecutionDriver = new TaskExecutionDriver(
            new TaskSourceContextStore(),
            new TaskOperationReceipts(),
            npcControlLease,
            new GameNpcDriver());
        MeetingWaitMonitor meetingWaitMonitor = new();
        NpcInteractionLifecycle interactionLifecycle = new(new TaskInteractionHandoffStore(), this.taskExecutionDriver);
        this.runtimeClient = new RuntimeClient(
            this.config,
            this.dispatcher,
            this.observationBuilder,
            this.conversationStore,
            this.emoteCapability,
            this.presentDialogueCapability,
            this.facePlayerCapability,
            this.moveToCapability,
            this.approachPlayerCapability,
            this.resolveMeetingCapability,
            landmarkCatalogStore,
            this.taskExecutionDriver,
            npcControlLease,
            meetingWaitMonitor,
            interactionLifecycle,
            this.Monitor
        );
        this.playerInteractProbe = new PlayerInteractProbe(
            this.config.AgentTargets,
            this.runtimeClient,
            this.Monitor,
            helper.Input
        );

        helper.Events.GameLoop.GameLaunched += this.OnGameLaunched;
        helper.Events.GameLoop.SaveLoaded += this.OnSaveLoaded;
        helper.Events.GameLoop.Saving += this.OnSaving;
        helper.Events.GameLoop.Saved += this.OnSaved;
        helper.Events.GameLoop.DayStarted += this.OnDayStarted;
        helper.Events.GameLoop.ReturnedToTitle += this.OnReturnedToTitle;
        helper.Events.GameLoop.TimeChanged += this.OnTimeChanged;
        helper.Events.GameLoop.UpdateTicked += this.OnUpdateTicked;
        helper.Events.Input.ButtonPressed += this.OnButtonPressed;
        helper.ConsoleCommands.Add(
            "gameagent_probe_npc",
            "Run the GameAgent NPC probe without clicking. Usage: gameagent_probe_npc [NPC name]",
            this.RunProbeCommand
        );
        helper.ConsoleCommands.Add(
            "gameagent_runtime_reconnect",
            "Reconnect the GameAgent Runtime stream and repeat capability/world binding.",
            (_, _) => this.ReconnectRuntimeClient()
        );
        LandmarkMarker.RegisterCommand(
            helper,
            landmarkAssetPath,
            landmarkCatalogStore,
            this.Monitor
        );

        this.Monitor.Log("GameAgent Stardew Adapter Probe loaded.", LogLevel.Info);
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            this.phase9RouteProbe?.Dispose();
            this.phase9SaveProbe?.Dispose();
            this.runtimeClient?.Dispose();
        }
    }

    private void OnGameLaunched(object? sender, GameLaunchedEventArgs e)
    {
        this.StartRuntimeClient();
    }

    private void StartRuntimeClient()
    {
        try
        {
            this.runtimeClient?.Start();
        }
        catch (Exception ex)
        {
            this.Monitor.Log($"Failed to start GameAgent Runtime client: {ex}", LogLevel.Error);
        }
    }

    private void ReconnectRuntimeClient()
    {
        try
        {
            this.runtimeClient?.Reconnect();
        }
        catch (Exception ex)
        {
            this.Monitor.Log($"Failed to reconnect GameAgent Runtime client: {ex}", LogLevel.Error);
        }
    }

    private void OnSaveLoaded(object? sender, SaveLoadedEventArgs e)
    {
        this.runtimeClient?.AbortPendingCheckpointSave("save_reloaded");
        this.runtimeClient?.BeginWorldContext(this.ReadCheckpointMarker());
    }

    private CheckpointMarkerRead ReadCheckpointMarker()
    {
        if (this.helper is null)
            return CheckpointMarkerRead.Absent;
        try
        {
            Dictionary<string, string>? data = this.helper.Data.ReadSaveData<Dictionary<string, string>>(CheckpointMarker.SaveDataKey);
            return CheckpointMarker.Read(data);
        }
        catch (Exception ex)
        {
            this.Monitor.Log($"GameAgent could not read the task checkpoint marker: {ex}", LogLevel.Warn);
            return CheckpointMarkerRead.Invalid("save_data");
        }
    }

    /// <summary>
    /// Runs while the game saves: the marker written here is what the next load refers to, so an
    /// unconfirmed outcome is written rather than leaving a stale confirmed reference behind.
    /// </summary>
    private void OnSaving(object? sender, SavingEventArgs e)
    {
        CheckpointMarker? marker;
        try
        {
            marker = this.runtimeClient?.PrepareTaskCheckpointSave();
        }
        catch (Exception ex)
        {
            this.Monitor.Log($"GameAgent task checkpoint prepare failed: {ex}", LogLevel.Error);
            return;
        }
        if (marker is null || this.helper is null)
            return;
        try
        {
            this.helper.Data.WriteSaveData(CheckpointMarker.SaveDataKey, marker.ToSaveData());
        }
        catch (Exception ex)
        {
            this.Monitor.Log($"GameAgent could not write the task checkpoint marker: {ex}", LogLevel.Error);
        }
    }

    private void OnSaved(object? sender, SavedEventArgs e)
    {
        try
        {
            this.runtimeClient?.OnWorldSaved();
        }
        catch (Exception ex)
        {
            this.Monitor.Log($"GameAgent task checkpoint finish failed: {ex}", LogLevel.Error);
        }
    }

    private void OnDayStarted(object? sender, DayStartedEventArgs e)
    {
        this.runtimeClient?.AbortPendingCheckpointSave("day_started_before_saved");
        this.runtimeClient?.RefreshWorldClock();
    }

    private void OnTimeChanged(object? sender, TimeChangedEventArgs e)
    {
        this.runtimeClient?.RefreshWorldClock();
    }

    private void OnReturnedToTitle(object? sender, ReturnedToTitleEventArgs e)
    {
        this.runtimeClient?.AbortPendingCheckpointSave("returned_to_title");
        this.runtimeClient?.ClearWorldContext();
    }

    private void OnUpdateTicked(object? sender, UpdateTickedEventArgs e)
    {
        this.dispatcher?.Drain();
        this.dialogueController?.Update();
        this.moveToCapability?.Update();
        this.runtimeClient?.UpdateTaskActions();
    }

    private void OnButtonPressed(object? sender, ButtonPressedEventArgs e)
    {
        try
        {
            this.playerInteractProbe?.HandleButtonPressed(e);
        }
        catch (Exception ex)
        {
            this.Monitor.Log($"GameAgent probe failed: {ex}", LogLevel.Error);
        }
    }

    private void RunProbeCommand(string command, string[] args)
    {
        if (!Context.IsWorldReady)
        {
            this.Monitor.Log("Load a save before running the GameAgent probe.", LogLevel.Warn);
            return;
        }

        string targetAgentName = args.Length > 0
            ? string.Join(" ", args).Trim()
            : this.config?.AgentTargets?.FirstOrDefault(name => !string.IsNullOrWhiteSpace(name)) ?? "Linus";
        NPC? target = Game1.getCharacterFromName(targetAgentName, mustBeVillager: true);
        if (target is null)
        {
            this.Monitor.Log($"Could not find {targetAgentName} in this save.", LogLevel.Warn);
            return;
        }

        if (this.runtimeClient is null || !this.runtimeClient.IsReady)
        {
            this.Monitor.Log("GameAgent Runtime is not ready; command ignored.", LogLevel.Warn);
            return;
        }

        this.runtimeClient.SendPlayerInteracted(target, Game1.player, PlayerInteractTrigger.ConsoleProbe);
    }
}
