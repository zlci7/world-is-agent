using System;
using System.IO;
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
    private MainThreadDispatcher? dispatcher;
    private ConversationStateStore? conversationStore;
    private DialogueInteractionController? dialogueController;
    private ObservationBuilder? observationBuilder;
    private EmoteCapability? emoteCapability;
    private PresentDialogueCapability? presentDialogueCapability;
    private FacePlayerCapability? facePlayerCapability;
    private MoveToCapability? moveToCapability;
    private ResolveMeetingCapability? resolveMeetingCapability;
    private PlayerInteractProbe? playerInteractProbe;
    private RuntimeClient? runtimeClient;
    private StardewRouteProbe? phase9RouteProbe;
    private StardewSaveProbe? phase9SaveProbe;

    public override void Entry(IModHelper helper)
    {
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
        string landmarkJson = File.ReadAllText(Path.Combine(helper.DirectoryPath, "assets", "landmarks.json"));
        LandmarkCatalog landmarkCatalog = LandmarkCatalog.Parse(landmarkJson);
        this.resolveMeetingCapability = new ResolveMeetingCapability(landmarkCatalog);
        this.runtimeClient = new RuntimeClient(
            this.config,
            this.dispatcher,
            this.observationBuilder,
            this.conversationStore,
            this.emoteCapability,
            this.presentDialogueCapability,
            this.facePlayerCapability,
            this.moveToCapability,
            this.resolveMeetingCapability,
            landmarkCatalog,
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
            (_, _) => this.StartRuntimeClient()
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

    private void OnSaveLoaded(object? sender, SaveLoadedEventArgs e)
    {
        this.runtimeClient?.BeginWorldContext();
    }

    private void OnDayStarted(object? sender, DayStartedEventArgs e)
    {
        this.runtimeClient?.RefreshWorldClock();
    }

    private void OnTimeChanged(object? sender, TimeChangedEventArgs e)
    {
        this.runtimeClient?.RefreshWorldClock();
    }

    private void OnReturnedToTitle(object? sender, ReturnedToTitleEventArgs e)
    {
        this.runtimeClient?.ClearWorldContext();
    }

    private void OnUpdateTicked(object? sender, UpdateTickedEventArgs e)
    {
        this.dispatcher?.Drain();
        this.dialogueController?.Update();
        this.moveToCapability?.Update();
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
