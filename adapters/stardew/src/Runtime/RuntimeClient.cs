using System;
using System.Globalization;
using System.Threading;
using System.Threading.Tasks;
using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Capabilities;
using GameAgent.Stardew.Dialogue;
using GameAgent.Stardew.State;
using GameAgent.Stardew.Tasks;
using Google.Protobuf.WellKnownTypes;
using Grpc.Core;
using Grpc.Net.Client;
using StardewModdingAPI;
using StardewValley;

namespace GameAgent.Stardew.Runtime;

public sealed class RuntimeClient : IDisposable, ICheckpointTransport
{
    private const string ProtocolVersion = "v1alpha2";
    // The runtime retires a save barrier ten seconds after a handoff it never finished, so the
    // Adapter keeps retrying the post-save rebind for a bounded window above that bound.
    private const int SaveRecoveryWindowMs = 12_000;
    private const int SaveRecoveryRetryIntervalMs = 1_000;

    private readonly AdapterConfig config;
    private readonly MainThreadDispatcher dispatcher;
    private readonly ObservationBuilder observationBuilder;
    private readonly ConversationStateStore conversationStore;
    private readonly InteractionContextStore interactionContextStore = new();
    private readonly EmoteCapability emoteCapability;
    private readonly PresentDialogueCapability presentDialogueCapability;
    private readonly FacePlayerCapability facePlayerCapability;
    private readonly MoveToCapability moveToCapability;
    private readonly ApproachPlayerCapability approachPlayerCapability;
    private readonly ResolveMeetingCapability resolveMeetingCapability;
    private readonly LandmarkCatalogStore landmarkCatalogStore;
    private readonly TaskExecutionDriver taskExecutionDriver;
    private readonly NpcControlLease npcControlLease;
    private readonly OrdinaryInteractionLifecycle ordinaryInteractions;
    private readonly MeetingWaitMonitor meetingWaitMonitor;
    private readonly NpcInteractionLifecycle taskInteractionLifecycle;
    private readonly Dictionary<OperationKey, ActiveTaskOperation> activeTaskActions = new();
    private readonly TaskInteractionConversationIndex taskInteractionConversations = new();
    private readonly TaskEvidenceInbox deferredTaskEvidence = new();
    private readonly ActionCancellationRegistry actionCancellationRegistry = new();
    private readonly IMonitor monitor;
    private readonly SemaphoreSlim sendMu = new(1, 1);
    private readonly object connectionGate = new();
    private readonly RuntimeSessionState sessionState = new();
    private readonly RuntimeWorldContext worldContext;
    private readonly WorldBindingExchange worldBindingExchange = new();
    private readonly CheckpointReplyInbox checkpointReplies = new();
    private readonly TaskCheckpointBridge checkpointBridge;
    private readonly TimeSpan checkpointTimeout;

    private GrpcChannel? channel;
    private AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage>? stream;
    private CancellationTokenSource? cancellation;
    private Task? receiveTask;
    private long connectionEpoch;
    private readonly string sessionId = Guid.NewGuid().ToString("N");
    // currentWorldId is maintained from SMAPI main-thread lifecycle events.
    // Background gRPC threads must not resolve Stardew world state directly.
    private volatile string currentWorldId = string.Empty;
    private long eventSequence;
    private int worldBindingSent;
    private long worldBindingClockSequence;
    private long saveRecoveryDeadlineTicks;
    private long saveRecoveryNextAttemptTicks;
    private bool taskWorldBoundThisRun;
    private bool disposed;

    public RuntimeClient(
        AdapterConfig config,
        MainThreadDispatcher dispatcher,
        ObservationBuilder observationBuilder,
        ConversationStateStore conversationStore,
        EmoteCapability emoteCapability,
        PresentDialogueCapability presentDialogueCapability,
        FacePlayerCapability facePlayerCapability,
        MoveToCapability moveToCapability,
        ApproachPlayerCapability approachPlayerCapability,
        ResolveMeetingCapability resolveMeetingCapability,
        LandmarkCatalogStore landmarkCatalogStore,
        TaskExecutionDriver taskExecutionDriver,
        NpcControlLease npcControlLease,
        MeetingWaitMonitor meetingWaitMonitor,
        NpcInteractionLifecycle taskInteractionLifecycle,
        IMonitor monitor
    )
    {
        this.config = config;
        this.dispatcher = dispatcher;
        this.observationBuilder = observationBuilder;
        this.conversationStore = conversationStore;
        this.emoteCapability = emoteCapability;
        this.presentDialogueCapability = presentDialogueCapability;
        this.facePlayerCapability = facePlayerCapability;
        this.moveToCapability = moveToCapability;
        this.approachPlayerCapability = approachPlayerCapability;
        this.resolveMeetingCapability = resolveMeetingCapability;
        this.landmarkCatalogStore = landmarkCatalogStore;
        this.taskExecutionDriver = taskExecutionDriver;
        this.npcControlLease = npcControlLease;
        GameNpcDriver interactionDriver = new();
        this.ordinaryInteractions = new OrdinaryInteractionLifecycle(
            npcControlLease, interactionDriver, interactionDriver.HoldInteraction, () => Environment.TickCount64);
        this.meetingWaitMonitor = meetingWaitMonitor;
        this.taskInteractionLifecycle = taskInteractionLifecycle;
        this.monitor = monitor;
        this.worldContext = new RuntimeWorldContext(this.config.GameId, GameClock.ClockId);
        this.checkpointTimeout = CheckpointBridgeOptions.RequireTimeout(this.config.CheckpointPrepareTimeoutMilliseconds);
        this.checkpointBridge = new TaskCheckpointBridge(this, message => this.monitor.Log(message, LogLevel.Debug));
    }

    public bool IsReady => this.sessionState.CanUseRuntime && this.stream is not null;

    public bool IsTaskReady => this.sessionState.CanUseTasks && this.stream is not null;

    public void Start()
    {
        lock (this.connectionGate)
        {
            if (this.disposed)
                throw new ObjectDisposedException(nameof(RuntimeClient));
            if (this.receiveTask is { IsCompleted: false })
                return;

            this.stream?.Dispose();
            this.channel?.Dispose();
            this.cancellation?.Dispose();

            AppContext.SetSwitch("System.Net.Http.SocketsHttpHandler.Http2UnencryptedSupport", true);
            this.cancellation = new CancellationTokenSource();
            this.channel = GrpcChannel.ForAddress(this.config.RuntimeAddress);
            GameAgentGateway.GameAgentGatewayClient client = new(this.channel);
            this.stream = client.Connect(cancellationToken: this.cancellation.Token);
            this.sessionState.BeginConnection();
            Interlocked.Exchange(ref this.worldBindingSent, 0);
            Interlocked.Exchange(ref this.worldBindingClockSequence, 0);
            this.worldBindingExchange.Reset();
            CancellationToken token = this.cancellation.Token;
            this.receiveTask = Task.Run(() => this.RunAsync(token));
        }
    }

    public void Reconnect()
    {
        this.ResetTaskRuntimeState("manual_rebind");
        this.RestartConnection();
    }

    private long StopConnection()
    {
        lock (this.connectionGate)
        {
            this.connectionEpoch++;
            this.sessionState.Disconnect();
            this.cancellation?.Cancel();
            this.stream?.Dispose();
            this.stream = null;
            this.worldBindingExchange.Reset();
            return this.connectionEpoch;
        }
    }

    private void RestartConnection()
    {
        long epoch = this.StopConnection();
        Task previous = this.receiveTask ?? Task.CompletedTask;
        this.SendFireAndForget(this.StartAfterDisconnectAsync(previous, epoch), "Runtime reconnect");
    }

    private async Task StartAfterDisconnectAsync(Task previous, long epoch)
    {
        await previous;
        this.dispatcher.Enqueue(() =>
        {
            lock (this.connectionGate)
            {
                if (!this.disposed && this.connectionEpoch == epoch)
                    this.Start();
            }
        });
    }

    public void SendPlayerInteracted(NPC npc, Farmer player, string trigger)
    {
        if (!this.TrySendPlayerInteracted(npc, player, trigger, out string reason))
            this.monitor.Log($"GameAgent interaction event suppressed: {reason}.", LogLevel.Debug);
    }

    public bool TrySendPlayerInteracted(NPC npc, Farmer player, string trigger, out string reason)
    {
        ArgumentNullException.ThrowIfNull(npc);
        ArgumentNullException.ThrowIfNull(player);

        if (!this.IsReady)
        {
            reason = "runtime_not_ready";
            return false;
        }

        string worldId = this.currentWorldId;
        if (!RuntimeWorldScope.IsAvailable(worldId))
        {
            reason = "world_unavailable";
            return false;
        }

        string npcEntityId = ProtocolMapper.ToNpcEntityId(npc);
        if (!InteractionPolicy.IsWithinMaxInteractionDistance(npc.TilePoint.X, npc.TilePoint.Y, player.TilePoint.X, player.TilePoint.Y))
        {
            reason = "too_far";
            return false;
        }

        if (this.interactionContextStore.IsInFlight(worldId, npcEntityId, ProtocolMapper.PlayerEntityId))
        {
            reason = "interaction_in_flight";
            return false;
        }

        ulong sequence = unchecked((ulong)Interlocked.Increment(ref this.eventSequence));
        string eventId = ProtocolMapper.NewMessageId("event");
        try
        {
            this.presentDialogueCapability.CloseForNpc(npcEntityId);
            string conversationId = this.conversationStore.PrepareInteraction(worldId, npcEntityId, ProtocolMapper.PlayerEntityId, eventId);
            RuntimeWorldSnapshot world = this.worldContext.Current ?? throw new InvalidOperationException("world context is unavailable");
            OperationKey interactionScope = new(world.WorldId, world.WorldRunId, world.ExecutionGeneration,
                npcEntityId, "interaction:" + conversationId, eventId);
            if (!this.ordinaryInteractions.Begin(conversationId, eventId, interactionScope))
            {
                this.conversationStore.DiscardPending(eventId);
                reason = "npc_control_busy";
                return false;
            }
            InteractionContextSnapshot snapshot = this.BuildInteractionContextSnapshot(eventId, worldId, npc, player, conversationId);
            if (!this.interactionContextStore.TryReserve(snapshot, out reason))
            {
                this.conversationStore.DiscardPending(eventId);
                this.ordinaryInteractions.End(conversationId);
                return false;
            }

            GameEvent gameEvent = ProtocolMapper.BuildPlayerInteractedWithNpcEvent(npc, player, conversationId, trigger, sequence, worldId, eventId);
            ProtocolMapper.AttachPlayerInteractionSource(gameEvent, this.IsTaskReady ? this.worldContext.Current : null);
            this.presentDialogueCapability.QueueWaitingForNpc(npcEntityId);
            this.SendFireAndForget(this.SendPreparedGameEventAsync(gameEvent, eventId), "GameEvent");
            reason = string.Empty;
            return true;
        }
        catch (Exception ex)
        {
            this.ordinaryInteractions.TurnEnded(eventId);
            this.conversationStore.DiscardPending(eventId);
            this.interactionContextStore.DiscardPending(eventId);
            this.presentDialogueCapability.CloseWaitingForNpc(npcEntityId);
            reason = "interaction_queue_failed";
            this.monitor.Log($"GameAgent interaction event queue failed: {ex.Message}", LogLevel.Warn);
            return false;
        }
    }

    private async Task SendPreparedGameEventAsync(GameEvent gameEvent, string eventId)
    {
        try
        {
            await this.SendAsync(
                new AdapterMessage
                {
                    MessageId = ProtocolMapper.NewMessageId("event_msg"),
                    Event = gameEvent,
                },
                this.cancellation?.Token ?? CancellationToken.None
            );
        }
        catch
        {
            this.dispatcher.Enqueue(() => this.ordinaryInteractions.TurnEnded(eventId));
            this.conversationStore.DiscardPending(eventId);
            this.interactionContextStore.DiscardPending(eventId);
            this.CloseWaitingForNpcOnMainThread(gameEvent.TargetEntityId);
            throw;
        }

        this.monitor.Log($"GameAgent GameEvent sent: {gameEvent.EventId}", LogLevel.Debug);
    }

    private void SendPlayerDialogueSubmission(NPC npc, Farmer player, PlayerDialogueSubmission submission)
    {
        this.SendFireAndForget(this.SendPlayerDialogueSubmissionAsync(npc, player, submission), "Player dialogue GameEvent");
    }

    private async Task SendPlayerDialogueSubmissionAsync(NPC npc, Farmer player, PlayerDialogueSubmission submission)
    {
        string worldId = this.currentWorldId;
        string taskInteractionEventId = this.taskInteractionConversations.FindTaskEvent(submission.ConversationId) ?? string.Empty;
        if (!this.IsReady || !RuntimeWorldScope.IsAvailable(worldId))
        {
            this.AbortTaskConversation(worldId, ProtocolMapper.ToNpcEntityId(npc), submission.ConversationId, taskInteractionEventId, "runtime_unavailable");
            return;
        }

        ulong sequence = unchecked((ulong)Interlocked.Increment(ref this.eventSequence));
        string npcEntityId = ProtocolMapper.ToNpcEntityId(npc);
        string eventId = ProtocolMapper.NewMessageId("event");
        int timeOfDay = Game1.timeOfDay;
        GameEvent gameEvent;
        try
        {
            gameEvent = ProtocolMapper.BuildPlayerSaidToNpcEvent(
                npc,
                player,
                submission.ConversationId,
                submission.InputKind,
                submission.Text,
                submission.SelectedOptionIndex,
                submission.Trigger,
                sequence,
                worldId,
                eventId
            );
            InteractionContextSnapshot snapshot = this.BuildInteractionContextSnapshot(eventId, worldId, npc, player, submission.ConversationId);
            this.conversationStore.PreparePlayerLine(
                worldId,
                npcEntityId,
                ProtocolMapper.PlayerEntityId,
                submission.ConversationId,
                eventId,
                ProtocolMapper.PlayerEntityId,
                player.Name,
                submission.Text,
                timeOfDay
            );
            if (!this.interactionContextStore.TryReserveHandoff(snapshot, out string reason))
            {
                this.conversationStore.DiscardPending(eventId);
                this.AbortTaskConversation(worldId, npcEntityId, submission.ConversationId, taskInteractionEventId, reason);
                this.monitor.Log($"GameAgent player dialogue event suppressed: {reason}.", LogLevel.Debug);
                return;
            }

            this.ordinaryInteractions.BindEvent(submission.ConversationId, eventId);
            if (string.IsNullOrWhiteSpace(taskInteractionEventId))
                ProtocolMapper.AttachPlayerInteractionSource(gameEvent, this.IsTaskReady ? this.worldContext.Current : null);
            this.presentDialogueCapability.QueueWaitingForNpc(npcEntityId);
            await this.SendAsync(
                new AdapterMessage
                {
                    MessageId = ProtocolMapper.NewMessageId("event_msg"),
                    Event = gameEvent,
                },
                this.cancellation?.Token ?? CancellationToken.None
            );
        }
        catch
        {
            this.conversationStore.DiscardPending(eventId);
            this.interactionContextStore.DiscardPending(eventId);
            this.dispatcher.Enqueue(() =>
            {
                this.AbortTaskConversation(worldId, npcEntityId, submission.ConversationId, taskInteractionEventId, "event_send_failed");
                this.presentDialogueCapability.CloseWaitingForNpc(npcEntityId);
            });
            throw;
        }

        this.monitor.Log($"GameAgent player dialogue GameEvent sent: {gameEvent.EventId}", LogLevel.Debug);
    }

    public void Dispose()
    {
        lock (this.connectionGate)
            this.disposed = true;
        this.sessionState.Disconnect();

        try
        {
            this.cancellation?.Cancel();
            if (this.stream is not null)
                this.stream.RequestStream.CompleteAsync().GetAwaiter().GetResult();
        }
        catch
        {
            // Dispose best-effort only; SMAPI may unload while the stream is already broken.
        }

        this.stream?.Dispose();
        this.channel?.Dispose();
        this.cancellation?.Dispose();
        this.ClearRuntimeStreamStateOnMainThread();
        this.sendMu.Dispose();
    }

    private async Task RunAsync(CancellationToken cancellationToken)
    {
        try
        {
            var receivingStream = this.stream ?? throw new OperationCanceledException(cancellationToken);
            await this.SendHelloAsync(cancellationToken);

            while (await receivingStream.ResponseStream.MoveNext(cancellationToken))
            {
                cancellationToken.ThrowIfCancellationRequested();
                RuntimeMessage message = receivingStream.ResponseStream.Current;
                this.TraceRecv(message);
                await this.HandleRuntimeMessageAsync(message, cancellationToken);
            }
        }
        catch (OperationCanceledException)
        {
            this.monitor.Log("GameAgent Runtime stream cancelled.", LogLevel.Debug);
        }
        catch (Exception ex)
        {
            this.monitor.Log($"GameAgent Runtime stream failed: {ex}", LogLevel.Error);
        }
        finally
        {
            this.sessionState.Disconnect();
            Interlocked.Exchange(ref this.worldBindingSent, 0);
            Interlocked.Exchange(ref this.worldBindingClockSequence, 0);
            this.checkpointReplies.FailAll("disconnected");
            this.checkpointBridge.OnDisconnected("runtime_disconnected");
            this.dispatcher.Enqueue(() => this.ClearRuntimeStreamStateOnMainThread());
        }
    }

    private async Task SendHelloAsync(CancellationToken cancellationToken)
    {
        AdapterHello hello = ProtocolMapper.BuildAdapterHello(
            this.config.AdapterId,
            this.config.AdapterVersion,
            ProtocolVersion,
            this.config.GameId,
            "unknown",
            this.sessionId
        );

        await this.SendAsync(
            new AdapterMessage
            {
                MessageId = ProtocolMapper.NewMessageId("hello"),
                Hello = hello,
            },
            cancellationToken
        );

        this.monitor.Log($"GameAgent AdapterHello sent to {this.config.RuntimeAddress}.", LogLevel.Info);
    }

    private async Task HandleRuntimeMessageAsync(RuntimeMessage message, CancellationToken cancellationToken)
    {
        switch (message.PayloadCase)
        {
            case RuntimeMessage.PayloadOneofCase.EnvironmentReady:
                if (!this.sessionState.AcceptEnvironmentReady(message.EnvironmentReady.AcceptedExtensions, out string environmentError))
                    throw new InvalidOperationException(environmentError);
                this.monitor.Log("GameAgent Runtime EnvironmentReady received.", LogLevel.Info);
                break;

            case RuntimeMessage.PayloadOneofCase.CapabilityRequest:
                if (!this.sessionState.AcceptCapabilityRequest(out string capabilityError))
                    throw new InvalidOperationException(capabilityError);
                await this.SendCapabilitiesAsync(message.MessageId, cancellationToken);
                break;

            case RuntimeMessage.PayloadOneofCase.WorldBindingReady:
                await this.HandleWorldBindingReadyAsync(message.CorrelationId, message.WorldBindingReady, cancellationToken);
                break;

            case RuntimeMessage.PayloadOneofCase.CheckpointPrepared:
                // The save thread is blocked inside the game's own save here, so the reply must
                // release the waiting handoff on this thread instead of the main-thread queue.
                this.HandleCheckpointPrepared(message.CorrelationId, message.CheckpointPrepared);
                break;

            case RuntimeMessage.PayloadOneofCase.TaskControl:
                await this.HandleTaskControlAsync(message.TaskControl, cancellationToken);
                break;

            case RuntimeMessage.PayloadOneofCase.EventAck:
                this.dispatcher.Enqueue(() => { if (!cancellationToken.IsCancellationRequested) this.HandleEventAck(message.EventAck); });
                break;

            case RuntimeMessage.PayloadOneofCase.TurnCompletion:
                this.dispatcher.Enqueue(() => { if (!cancellationToken.IsCancellationRequested) this.HandleTurnCompletion(message.TurnCompletion); });
                break;

            case RuntimeMessage.PayloadOneofCase.Observe:
                if (message.Observe is not null)
                    this.dispatcher.Enqueue(() => { if (!cancellationToken.IsCancellationRequested) this.HandleObserveOnMainThread(message.MessageId, message.Observe); });
                break;

            case RuntimeMessage.PayloadOneofCase.Action:
                if (message.Action is not null)
                    this.dispatcher.Enqueue(() => { if (!cancellationToken.IsCancellationRequested) this.HandleActionOnMainThread(message.Action); });
                break;

            case RuntimeMessage.PayloadOneofCase.CancelAction:
                if (message.CancelAction is not null)
                    this.HandleCancelAction(message.CancelAction);
                break;

            case RuntimeMessage.PayloadOneofCase.Error:
                this.monitor.Log($"GameAgent Runtime error: {message.Error?.Code} {message.Error?.Message}", LogLevel.Warn);
                break;

            default:
                this.monitor.Log($"Ignoring unsupported RuntimeMessage payload: {message.PayloadCase}", LogLevel.Debug);
                break;
        }
    }

    private async Task SendCapabilitiesAsync(string correlationId, CancellationToken cancellationToken)
    {
        CapabilityList capabilities = CapabilityCatalog.BuildEnvironmentCapabilities(
            this.landmarkCatalogStore.Current.Landmarks,
            includeTaskCapabilities: this.sessionState.TaskExtensionAccepted);

        await this.SendAsync(
            new AdapterMessage
            {
                MessageId = ProtocolMapper.NewMessageId("capabilities_msg"),
                CorrelationId = correlationId,
                Capabilities = capabilities,
            },
            cancellationToken
        );

        this.sessionState.MarkCapabilitiesSent();
        this.monitor.Log($"GameAgent CapabilityList sent: {string.Join(", ", capabilities.Capabilities.Select(capability => capability.Name))}.", LogLevel.Info);
        await this.TrySendWorldBindingAsync(cancellationToken);
    }

    private async Task TrySendWorldBindingAsync(CancellationToken cancellationToken)
    {
        if (!this.TryTakeWorldBinding(out string requestId, out AdapterMessage message, out RuntimeWorldSnapshot snapshot))
            return;
        try
        {
            await this.SendAsync(message, cancellationToken);
            this.monitor.Log($"GameAgent WorldBinding sent: world_id={snapshot.WorldId} run_id={snapshot.WorldRunId} sequence={snapshot.ClockSequence} entities=[{string.Join(",", message.WorldBinding.Entities.Select(entity => entity.EntityId))}].", LogLevel.Info);
        }
        catch
        {
            this.CancelWorldBinding(requestId);
            throw;
        }
    }

    private bool TryTakeWorldBinding(out string requestId, out AdapterMessage message, out RuntimeWorldSnapshot snapshot)
    {
        requestId = string.Empty;
        message = new AdapterMessage();
        snapshot = null!;
        RuntimeWorldSnapshot? current = this.worldContext.Current;
        if (current is null ||
            this.sessionState.Phase != RuntimeSessionPhase.AwaitingWorldBindingReady ||
            Interlocked.CompareExchange(ref this.worldBindingSent, 1, 0) != 0)
        {
            return false;
        }

        requestId = ProtocolMapper.NewMessageId("world_binding");
        this.worldBindingExchange.Begin(requestId);
        Interlocked.Exchange(ref this.worldBindingClockSequence, checked((long)current.ClockSequence));
        WorldBinding binding = ProtocolMapper.BuildWorldBinding(current, this.worldContext.KnownNpcNames(current));
        message = new AdapterMessage { MessageId = requestId, WorldBinding = binding };
        snapshot = current;
        return true;
    }

    private void CancelWorldBinding(string requestId)
    {
        this.worldBindingExchange.Cancel(requestId);
        Interlocked.Exchange(ref this.worldBindingSent, 0);
        Interlocked.Exchange(ref this.worldBindingClockSequence, 0);
    }

    private async Task HandleWorldBindingReadyAsync(string correlationId, WorldBindingReady? ready, CancellationToken cancellationToken)
    {
        TaskCompletionSource<Task> dispatched = new(TaskCreationOptions.RunContinuationsAsynchronously);
        using CancellationTokenRegistration registration = cancellationToken.Register(() => dispatched.TrySetCanceled(cancellationToken));
        this.dispatcher.Enqueue(() =>
        {
            if (cancellationToken.IsCancellationRequested)
                return;
            dispatched.TrySetResult(this.ApplyWorldBindingReadyOnMainThreadAsync(correlationId, ready, cancellationToken));
        });
        await await dispatched.Task;
    }

    private async Task ApplyWorldBindingReadyOnMainThreadAsync(string correlationId, WorldBindingReady? ready, CancellationToken cancellationToken)
    {
        if (ready is null)
            throw new InvalidOperationException("world_binding_ready_missing");

        WorldBindingReplyStatus replyStatus = this.worldBindingExchange.Classify(correlationId);
        if (replyStatus == WorldBindingReplyStatus.Duplicate)
            return;
        if (replyStatus == WorldBindingReplyStatus.Stale)
        {
            this.monitor.Log($"Ignoring stale WorldBindingReady correlation_id={correlationId}.", LogLevel.Debug);
            return;
        }
        if (this.sessionState.Phase != RuntimeSessionPhase.AwaitingWorldBindingReady)
        {
            throw new InvalidOperationException("handshake_out_of_order");
        }

        if (string.Equals(ready.Status, "ready", StringComparison.Ordinal))
        {
            TaskScope scope = ready.Scope ?? throw new InvalidOperationException("world_binding_scope_missing");
            if (!this.worldContext.TryApplyBinding(scope.GameId, scope.WorldId, scope.WorldRunId, scope.ExecutionGeneration, out string bindingError))
            {
                this.sessionState.PauseTasks();
                throw new InvalidOperationException(bindingError);
            }
            this.taskWorldBoundThisRun = true;
            this.saveRecoveryDeadlineTicks = 0;
        }

        if (!this.sessionState.AcceptWorldBindingReady(ready.Status, out string stateError))
            throw new InvalidOperationException(stateError);
        this.worldBindingExchange.Complete(correlationId);

        this.monitor.Log(
            $"GameAgent WorldBindingReady received: status={ready.Status} code={ready.Error?.Code ?? string.Empty}.",
            string.Equals(ready.Status, "ready", StringComparison.Ordinal) ? LogLevel.Info : LogLevel.Warn
        );

        if (this.sessionState.CanUseTasks &&
            this.worldContext.Current is RuntimeWorldSnapshot current &&
            current.ClockSequence > unchecked((ulong)Interlocked.Read(ref this.worldBindingClockSequence)))
        {
            await this.SendWorldClockAsync(current, cancellationToken);
        }
    }

    /// <summary>
    /// Runs on the SMAPI save thread while the game saves. The marker this returns is written into
    /// the save; a runtime that cannot confirm the snapshot yields an unconfirmed marker instead.
    /// </summary>
    public CheckpointMarker? PrepareTaskCheckpointSave()
    {
        RuntimeWorldSnapshot? snapshot = this.worldContext.Current;
        bool holdsTaskWork = this.taskWorldBoundThisRun ||
            (snapshot?.CheckpointMarker.State is CheckpointMarkerState.Confirmed or CheckpointMarkerState.Unconfirmed or CheckpointMarkerState.Invalid);
        if (snapshot is null || !holdsTaskWork)
            return null;
        if (!this.sessionState.TaskExtensionAccepted || !this.IsTaskReady)
            return CheckpointMarker.Unconfirmed(snapshot.GameId, snapshot.WorldId, "task_runtime_unavailable");

        CheckpointPrepareRequest request = new(
            new CheckpointScope(snapshot.GameId, snapshot.WorldId, snapshot.WorldRunId, snapshot.ExecutionGeneration),
            snapshot.NowTick,
            snapshot.ClockSequence,
            ProtocolMapper.NewMessageId("checkpoint_save")
        );
        CheckpointMarker marker = this.checkpointBridge.PrepareForSaving(request, this.checkpointTimeout);
        this.monitor.Log(
            $"GameAgent task checkpoint prepared: save_request_id={request.SaveRequestId} status={marker.Status} reference={marker.CheckpointId ?? string.Empty} reason={marker.Reason ?? string.Empty}.",
            marker.IsConfirmed ? LogLevel.Info : LogLevel.Warn
        );
        return marker;
    }

    /// <summary>Runs on the SMAPI save thread once the save finished and its marker is on disk.</summary>
    public void OnWorldSaved() => this.CompleteTaskCheckpointSave(aborted: false, endsWorld: false, reason: "saved");

    public void OnSaveAborted(string reason) => this.CompleteTaskCheckpointSave(aborted: true, endsWorld: false, reason: reason);

    /// <summary>
    /// A save that never reached Saved is aborted by the next Saving, SaveLoaded, ReturnedToTitle or
    /// DayStarted. The runtime reference is released without rebinding because the world is going.
    /// </summary>
    public void AbortPendingCheckpointSave(string reason)
    {
        if (!this.checkpointBridge.HasPendingFinish)
            return;
        this.CompleteTaskCheckpointSave(aborted: true, endsWorld: true, reason: reason);
    }

    private void CompleteTaskCheckpointSave(bool aborted, bool endsWorld, string reason)
    {
        CheckpointFinishRequest? finish = aborted
            ? this.checkpointBridge.OnSaveAborted(reason)
            : this.checkpointBridge.OnSaved();
        RuntimeWorldSnapshot? snapshot = this.worldContext.Current;
        if (endsWorld || snapshot is null)
        {
            if (finish is not null)
                this.SendCheckpointFinish(finish, reason);
            return;
        }
        if (finish is not null &&
            finish.Scope.ExecutionGeneration != 0 &&
            finish.Scope.SameRun(new CheckpointScope(snapshot.GameId, snapshot.WorldId, snapshot.WorldRunId, snapshot.ExecutionGeneration)))
        {
            this.worldContext.TryApplyBinding(finish.Scope.GameId, finish.Scope.WorldId, finish.Scope.WorldRunId, finish.Scope.ExecutionGeneration, out _);
        }
        this.RebindAfterSave(finish, reason);
    }

    // A save fenced the world: the Adapter sends the one Finish it owns and rebinds in the same
    // ordered batch, then keeps retrying inside a bounded window while the runtime still refuses.
    private void RebindAfterSave(CheckpointFinishRequest? finish, string reason)
    {
        if (this.worldContext.Current is null || !this.sessionState.TaskExtensionAccepted)
            return;
        List<AdapterMessage> batch = new();
        if (finish is not null)
        {
            batch.Add(new AdapterMessage
            {
                MessageId = ProtocolMapper.NewMessageId("checkpoint_finish"),
                CheckpointFinish = ProtocolMapper.BuildCheckpointFinish(finish),
            });
        }
        this.saveRecoveryDeadlineTicks = Environment.TickCount64 + SaveRecoveryWindowMs;
        this.saveRecoveryNextAttemptTicks = Environment.TickCount64 + SaveRecoveryRetryIntervalMs;
        this.sessionState.PrepareWorldBinding();
        Interlocked.Exchange(ref this.worldBindingSent, 0);
        if (this.TryTakeWorldBinding(out string requestId, out AdapterMessage binding, out _))
        {
            batch.Add(binding);
            this.SendFireAndForget(this.SendBatchAsync(batch, this.cancellation?.Token ?? CancellationToken.None), $"checkpoint finish and rebind after {reason}");
            return;
        }
        if (batch.Count > 0)
            this.SendFireAndForget(this.SendBatchAsync(batch, this.cancellation?.Token ?? CancellationToken.None), $"checkpoint finish after {reason}");
    }

    private void SendCheckpointFinish(CheckpointFinishRequest finish, string reason)
    {
        AdapterMessage message = new()
        {
            MessageId = ProtocolMapper.NewMessageId("checkpoint_finish"),
            CheckpointFinish = ProtocolMapper.BuildCheckpointFinish(finish),
        };
        this.SendFireAndForget(this.SendAsync(message, this.cancellation?.Token ?? CancellationToken.None), $"checkpoint finish after {reason}");
    }

    // Retries are driven from the game's own update tick so nothing here needs a timer thread.
    private void UpdateSaveRecovery()
    {
        if (this.saveRecoveryDeadlineTicks == 0)
            return;
        long now = Environment.TickCount64;
        if (now > this.saveRecoveryDeadlineTicks)
        {
            this.saveRecoveryDeadlineTicks = 0;
            this.monitor.Log("GameAgent task rebind after a save gave up: the runtime still refuses this world.", LogLevel.Warn);
            return;
        }
        if (now < this.saveRecoveryNextAttemptTicks ||
            this.worldContext.Current is null ||
            !this.sessionState.TaskExtensionAccepted ||
            this.sessionState.CanUseTasks)
        {
            return;
        }
        this.saveRecoveryNextAttemptTicks = now + SaveRecoveryRetryIntervalMs;
        this.sessionState.PrepareWorldBinding();
        Interlocked.Exchange(ref this.worldBindingSent, 0);
        if (this.TryTakeWorldBinding(out string requestId, out AdapterMessage binding, out _))
            this.SendFireAndForget(this.SendAsync(binding, this.cancellation?.Token ?? CancellationToken.None), $"checkpoint rebind retry {requestId}");
    }

    private void HandleCheckpointPrepared(string correlationId, CheckpointPrepared? message)
    {
        if (message is null)
            return;
        CheckpointPreparedReply reply;
        try
        {
            reply = ProtocolMapper.ReadCheckpointPrepared(message);
        }
        catch (Exception ex)
        {
            this.monitor.Log($"GameAgent CheckpointPrepared rejected: {ex.Message}", LogLevel.Warn);
            return;
        }
        if (!this.checkpointReplies.TryResolve(correlationId, reply.SaveRequestId, reply.Scope, reply.Reference, reply.ErrorCode))
        {
            this.monitor.Log(
                $"Ignoring CheckpointPrepared correlation_id={correlationId} save_request_id={reply.SaveRequestId}: it does not match a pending save.",
                LogLevel.Debug
            );
        }
    }

    Task<CheckpointPreparedReply> ICheckpointTransport.PrepareAsync(CheckpointPrepareRequest request, CancellationToken token)
    {
        ArgumentNullException.ThrowIfNull(request);
        string messageId = ProtocolMapper.NewMessageId("checkpoint_prepare");
        return this.SendCheckpointPrepareAsync(messageId, request, this.cancellation?.Token ?? CancellationToken.None);
    }

    private async Task<CheckpointPreparedReply> SendCheckpointPrepareAsync(string messageId, CheckpointPrepareRequest request, CancellationToken cancellationToken)
    {
        Task<CheckpointPreparedReply> completion = this.checkpointReplies.Register(messageId, request.SaveRequestId, request.Scope);
        try
        {
            await this.SendAsync(
                new AdapterMessage
                {
                    MessageId = messageId,
                    CheckpointPrepare = ProtocolMapper.BuildCheckpointPrepare(request),
                },
                cancellationToken
            );
        }
        catch (Exception ex)
        {
            this.checkpointReplies.TryFail(messageId, "prepare_send_failed");
            this.monitor.Log($"GameAgent CheckpointPrepare send failed: {ex.Message}", LogLevel.Warn);
        }
        return await completion;
    }

    private async Task HandleTaskControlAsync(TaskControlRequest? request, CancellationToken cancellationToken)
    {
        if (request is null)
            return;

        TaskCompletionSource<TaskControlDecision> decided = new(TaskCreationOptions.RunContinuationsAsynchronously);
        this.dispatcher.Enqueue(() =>
        {
            if (cancellationToken.IsCancellationRequested)
                return;
            TaskControlDecision decision = new("unconfirmed", "scope_mismatch");
            RuntimeWorldSnapshot? snapshot = this.worldContext.Current;
            if (this.sessionState.TaskExtensionAccepted && snapshot is not null && ScopeMatches(request.Scope, snapshot))
            {
                decision = this.taskInteractionLifecycle.ReleaseForTaskControl(request.TaskId, request.OperationId, request.Reason);
                if (decision.Status == "released" || decision.Code is "control_lost" or "release_failed")
                {
                    OperationKey[] matching = this.activeTaskActions.Keys
                        .Where(operation => string.Equals(operation.TaskId, request.TaskId, StringComparison.Ordinal) &&
                            string.Equals(operation.OperationId, request.OperationId, StringComparison.Ordinal))
                        .ToArray();
                    foreach (OperationKey operation in matching)
                    {
                        if (this.activeTaskActions.Remove(operation, out ActiveTaskOperation? active))
                        {
                            foreach (ActionRequest action in active.Requests)
                                this.actionCancellationRegistry.Clear(action.ActionId);
                        }
                    }
                    this.meetingWaitMonitor.Cancel(request.TaskId, request.OperationId);
                    this.CleanupReleasedTaskInteraction(decision.EventId);
                }
            }
            decided.TrySetResult(decision);
        });
        using CancellationTokenRegistration registration = cancellationToken.Register(() => decided.TrySetCanceled(cancellationToken));
        TaskControlDecision resolved = await decided.Task;

        TaskControlResult result = new()
        {
            Scope = request.Scope,
            TaskId = request.TaskId,
            OperationId = request.OperationId,
            RequestId = request.RequestId,
            Status = resolved.Status,
        };
        if (!string.IsNullOrWhiteSpace(resolved.Code))
            result.Error = new Error { Code = resolved.Code, Message = "task control release could not be confirmed" };
        await this.SendAsync(
            new AdapterMessage
            {
                MessageId = ProtocolMapper.NewMessageId("task_control_result"),
                TaskControlResult = result,
            },
            cancellationToken
        );
    }

    private Task SendWorldClockAsync(RuntimeWorldSnapshot snapshot, CancellationToken cancellationToken)
    {
        return this.SendAsync(
            new AdapterMessage
            {
                MessageId = ProtocolMapper.NewMessageId("world_clock"),
                WorldClock = ProtocolMapper.BuildWorldClockUpdate(snapshot),
            },
            cancellationToken
        );
    }

    private static bool ScopeMatches(TaskScope? scope, RuntimeWorldSnapshot snapshot)
    {
        return scope is not null &&
            string.Equals(scope.GameId, snapshot.GameId, StringComparison.Ordinal) &&
            string.Equals(scope.WorldId, snapshot.WorldId, StringComparison.Ordinal) &&
            string.Equals(scope.WorldRunId, snapshot.WorldRunId, StringComparison.Ordinal) &&
            scope.ExecutionGeneration == snapshot.ExecutionGeneration;
    }

    private void HandleCancelAction(CancelActionRequest request)
    {
        this.actionCancellationRegistry.MarkCancelled(request.ActionId);
        this.monitor.Log($"GameAgent CancelAction recorded: {request.ActionId} reason={request.Reason}", LogLevel.Debug);
    }

    private void HandleObserveOnMainThread(string correlationId, ObserveRequest request)
    {
        try
        {
            if (!RuntimeWorldScope.Matches(request.WorldId, this.currentWorldId))
            {
                string message = RuntimeWorldScope.MismatchMessage(request.WorldId, this.currentWorldId);
                this.monitor.Log($"GameAgent ObserveRequest rejected: {message}", LogLevel.Warn);
                this.SendFireAndForget(
                    this.SendAsync(ProtocolMapper.BuildErrorMessage(correlationId, "world_mismatch", message), this.cancellation?.Token ?? CancellationToken.None),
                    "Observe world mismatch"
                );
                return;
            }

            NPC npc = this.RequireNpc(request.EntityId);
            StardewObservation stardewObservation = this.observationBuilder.Build(npc, Game1.player, "runtime_observe", this.currentWorldId);
            Observation observation = ProtocolMapper.BuildObservation(request.EntityId, stardewObservation, this.currentWorldId);
            foreach (TaskEvidence fact in this.deferredTaskEvidence.Take(request.EntityId))
                observation.TaskEvidence.Add(fact);

            this.SendFireAndForget(
                this.SendAsync(
                    new AdapterMessage
                    {
                        MessageId = ProtocolMapper.NewMessageId("observation_msg"),
                        CorrelationId = correlationId,
                        Observation = observation,
                    },
                    this.cancellation?.Token ?? CancellationToken.None
                ),
                "Observation"
            );

            this.monitor.Log($"GameAgent Observation sent for {request.EntityId}.", LogLevel.Debug);
        }
        catch (Exception ex)
        {
            this.monitor.Log($"GameAgent ObserveRequest failed: {ex.Message}", LogLevel.Error);
            this.SendFireAndForget(
                this.SendAsync(ProtocolMapper.BuildErrorMessage(correlationId, "observe_failed", ex), this.cancellation?.Token ?? CancellationToken.None),
                "Observe error"
            );
        }
    }

    private void HandleActionOnMainThread(ActionRequest request)
    {
        ActionResult result;
        this.presentDialogueCapability.CloseWaitingForNpc(request.EntityId);

        if (CapabilityCatalog.RequiresTaskReady(request.Capability) && !this.sessionState.TaskExtensionAccepted)
        {
            result = ProtocolMapper.BuildRejectedActionResult(request, "task_extension_not_negotiated", "durable task extension was not negotiated");
        }
        else if (CapabilityCatalog.RequiresTaskReady(request.Capability) && !this.IsTaskReady)
        {
            result = ProtocolMapper.BuildRejectedActionResult(request, "task_not_ready", "durable task world is not ready");
        }
        else if (request.TaskSource is not null && !this.IsTaskReady)
        {
            result = ProtocolMapper.BuildRejectedActionResult(request, "task_not_ready", "durable task world is not ready");
            this.monitor.Log($"GameAgent Task ActionRequest rejected before WorldBindingReady: {request.ActionId}", LogLevel.Warn);
        }
        else if (!RuntimeWorldScope.Matches(request.WorldId, this.currentWorldId))
        {
            string message = RuntimeWorldScope.MismatchMessage(request.WorldId, this.currentWorldId);
            result = ProtocolMapper.BuildRejectedActionResult(request, "world_mismatch", message);
            this.monitor.Log($"GameAgent ActionRequest rejected: {message}", LogLevel.Warn);
        }
        else if (this.actionCancellationRegistry.TryConsumeCancelled(request.ActionId))
        {
            result = ProtocolMapper.BuildCancelledActionResult(request, "action cancelled before execution");
            this.monitor.Log($"GameAgent ActionRequest skipped because it was cancelled: {request.ActionId}", LogLevel.Debug);
        }
        else
        {
            try
            {
                if (request.Capability == "present_dialogue")
                {
                    this.HandlePresentDialogueAction(request);
                    return;
                }

                if (request.Capability == "move_to")
                {
                    this.HandleMoveToAction(request);
                    return;
                }

                if (request.Capability == "resolve_meeting")
                {
                    this.HandleResolveMeetingAction(request);
                    return;
                }

                if (request.Capability == "move_to_landmark")
                {
                    this.HandleMoveToLandmarkAction(request);
                    return;
                }

                if (request.Capability == "wait_for_player")
                {
                    this.HandleWaitForPlayerAction(request);
                    return;
                }

                if (request.Capability == "approach_player")
                {
                    this.HandleApproachPlayerAction(request);
                    return;
                }

                result = request.Capability switch
                {
                    "emote" => this.HandleEmoteAction(request),
                    "face_player" => this.HandleFacePlayerAction(request),
                    _ => throw new InvalidOperationException($"unsupported capability: {request.Capability}"),
                };
            }
            catch (ArgumentException ex)
            {
                this.monitor.Log($"GameAgent ActionRequest rejected: {ex.Message}", LogLevel.Warn);
                result = ProtocolMapper.BuildRejectedActionResult(request, "invalid_action_arguments", ex.Message);
            }
            catch (Exception ex)
            {
                this.monitor.Log($"GameAgent ActionRequest failed: {ex.Message}", LogLevel.Error);
                result = ProtocolMapper.BuildFailedActionResult(request, "action_failed", ex);
            }
        }

        this.SendActionResult(result, request.Capability);
    }

    private void HandleEventAck(EventAck? ack)
    {
        if (ack is null)
            return;

        bool taskArrival = this.taskInteractionLifecycle.Find(ack.EventId) is not null;
        InteractionContextSnapshot? ackContext = string.IsNullOrWhiteSpace(ack.EventId)
            ? null
            : this.interactionContextStore.Find(ack.EventId);
        string taskInteractionEventId = taskArrival
            ? ack.EventId
            : ackContext is null
                ? string.Empty
                : this.taskInteractionConversations.FindTaskEvent(ackContext.ConversationId) ?? string.Empty;
        if (ack.Status is EventAckStatus.Accepted or EventAckStatus.Duplicate && ackContext is not null &&
            ProtocolMapper.TryParseNpcEntityId(ackContext.NpcEntityId, out string npcName))
            this.worldContext.RememberNpc(ackContext.WorldId, npcName);
        switch (ack.Status)
        {
            case EventAckStatus.Accepted:
                if (taskArrival)
                    this.taskInteractionLifecycle.Accept(ack.EventId);
                this.conversationStore.CommitPending(ack.EventId);
                this.interactionContextStore.Commit(ack.EventId);
                break;
            case EventAckStatus.Duplicate:
                if (taskArrival)
                    this.taskInteractionLifecycle.Accept(ack.EventId);
                this.conversationStore.CommitPending(ack.EventId);
                this.interactionContextStore.Commit(ack.EventId);
                break;
            case EventAckStatus.Rejected:
            case EventAckStatus.Unspecified:
                InteractionContextSnapshot? pending = this.interactionContextStore.DiscardPending(ack.EventId);
                this.conversationStore.DiscardPending(ack.EventId);
                if (taskArrival)
                {
                    this.taskInteractionLifecycle.Reject(ack.EventId, "event_rejected");
                    this.RemoveTaskInteractionConversation(ack.EventId);
                }
                else if (!string.IsNullOrWhiteSpace(taskInteractionEventId))
                {
                    this.CompleteTaskInteraction(taskInteractionEventId, "event_rejected");
                }
                this.CloseWaitingForNpcOnMainThread(pending?.NpcEntityId);
                this.CloseInteractionConversation(pending);
                break;
        }

        this.monitor.Log(
            $"GameAgent EventAck received: event_id={ack.EventId} status={ack.Status} code={ack.Error?.Code ?? string.Empty} message={ack.Error?.Message ?? string.Empty}",
            ack.Status == EventAckStatus.Rejected ? LogLevel.Warn : LogLevel.Debug
        );
    }

    private void HandleTurnCompletion(TurnCompletion? completion)
    {
        if (completion is null)
            return;

        if (!string.IsNullOrWhiteSpace(completion.EventId))
        {
            this.ordinaryInteractions.TurnEnded(completion.EventId);
            this.conversationStore.ReleaseInteractionPin(completion.EventId);
            InteractionContextSnapshot? current = this.interactionContextStore.TryGet(completion.EventId);
            string? conversationId = current?.ConversationId ??
                this.taskInteractionConversations.FindPresentationConversation(completion.EventId);
            string? taskInteractionEventId = conversationId is null
                ? null
                : this.taskInteractionConversations.FindTaskEvent(conversationId);
            if (taskInteractionEventId is not null &&
                this.taskInteractionConversations.ShouldReleaseAtTurnCompletion(completion.EventId, conversationId!))
                this.CompleteTaskInteraction(taskInteractionEventId, "turn_completed_without_ui");
            InteractionContextSnapshot? released = this.interactionContextStore.Release(completion.EventId);
            this.LogReleasedInteractionContext(released);
            this.CloseWaitingForNpcOnMainThread(released?.NpcEntityId);
        }

        this.monitor.Log(
            $"GameAgent TurnCompletion received: event_id={completion.EventId} turn_id={completion.TurnId} status={completion.Status} code={completion.Error?.Code ?? string.Empty} message={completion.Error?.Message ?? string.Empty}",
            LogLevel.Debug
        );
    }

    private void ClearRuntimeStreamStateOnMainThread()
    {
        this.moveToCapability.CancelAll("runtime disconnected before movement completed");
        this.ResetTaskRuntimeState("runtime_disconnected");
        this.presentDialogueCapability.CloseAll();
        this.conversationStore.Clear();
        this.interactionContextStore.Clear();
    }

    private NPC RequireNpc(string entityId)
    {
        if (!Context.IsWorldReady)
            throw new InvalidOperationException("world is not ready");

        if (!ProtocolMapper.TryParseNpcEntityId(entityId, out string npcName))
            throw new InvalidOperationException($"invalid npc entity_id: {entityId}");

        NPC? npc = Game1.getCharacterFromName(npcName, mustBeVillager: true);
        if (npc is null)
            throw new InvalidOperationException($"npc not found: {npcName}");

        return npc;
    }

    public void BeginWorldContext(CheckpointMarkerRead checkpointMarker)
    {
        string worldId = this.ResolveCurrentWorldId();
        if (!RuntimeWorldScope.IsAvailable(worldId))
        {
            this.ClearWorldContext();
            return;
        }

        this.ResetTaskRuntimeState("world_replaced");
        this.ClearConversations();
        this.currentWorldId = worldId;
        this.taskWorldBoundThisRun = false;
        this.saveRecoveryDeadlineTicks = 0;
        this.worldContext.BeginWorld(worldId, this.ReadCurrentWorldTick(), checkpointMarker);
        this.RestartConnection();
        this.monitor.Log($"GameAgent world context started: world_id={worldId} run_id={this.worldContext.Current?.WorldRunId}", LogLevel.Debug);
    }

    public void RefreshWorldClock()
    {
        if (this.worldContext.Current is null)
            return;

        this.worldContext.AdvanceClock(this.ReadCurrentWorldTick());
        if (this.IsTaskReady && this.worldContext.Current is RuntimeWorldSnapshot snapshot)
        {
            IReadOnlyList<AdapterMessage> messages = TaskOutboundBatch.ClockThenEvidence(
                new AdapterMessage
                {
                    MessageId = ProtocolMapper.NewMessageId("world_clock"),
                    WorldClock = ProtocolMapper.BuildWorldClockUpdate(snapshot),
                },
                this.CollectWaitEvidence(snapshot));
            this.SendFireAndForget(
                this.SendBatchAsync(messages, this.cancellation?.Token ?? CancellationToken.None),
                "TaskEvidence/WorldClockUpdate"
            );
        }
    }

    public void ClearWorldContext()
    {
        this.StopConnection();
        this.ResetTaskRuntimeState("world_cleared");
        this.moveToCapability.CancelAll("world context cleared before movement completed");
        this.presentDialogueCapability.CloseAll();
        this.currentWorldId = string.Empty;
        this.taskWorldBoundThisRun = false;
        this.saveRecoveryDeadlineTicks = 0;
        this.checkpointReplies.FailAll("world_cleared");
        this.checkpointBridge.OnDisconnected("world_cleared");
        this.worldContext.Clear();
        this.sessionState.PauseTasks();
        Interlocked.Exchange(ref this.worldBindingSent, 0);
        Interlocked.Exchange(ref this.worldBindingClockSequence, 0);
        this.worldBindingExchange.Reset();
        this.conversationStore.Clear();
        this.interactionContextStore.Clear();
    }

    public void ClearConversations()
    {
        this.moveToCapability.CancelAll("conversation context cleared before movement completed");
        this.presentDialogueCapability.CloseAll();
        this.conversationStore.Clear();
        this.interactionContextStore.Clear();
    }

    public void UpdateTaskActions()
    {
        this.UpdateSaveRecovery();
        foreach (EndedInteraction ended in this.ordinaryInteractions.Expire(npcId =>
        {
            NPC npc = this.RequireNpc(npcId);
            return !Game1.eventUp && ReferenceEquals(npc.currentLocation, Game1.player.currentLocation) &&
                InteractionPolicy.IsWithinMaxInteractionDistance(npc.TilePoint.X, npc.TilePoint.Y,
                    Game1.player.TilePoint.X, Game1.player.TilePoint.Y);
        }))
        {
            this.monitor.Log(
                $"GameAgent ordinary interaction ended: npc={ended.NpcEntityId} conversation={ended.Conversation} reason={ended.Reason}.",
                LogLevel.Debug);
            this.conversationStore.CloseIfConversation(this.currentWorldId, ended.NpcEntityId, ProtocolMapper.PlayerEntityId, ended.Conversation);
            this.presentDialogueCapability.CloseForNpc(ended.NpcEntityId);
        }
        RuntimeWorldSnapshot? world = this.worldContext.Current;
        if (world is null)
            return;
        if (!this.IsTaskReady)
            return;

        this.taskExecutionDriver.ExpireArrivalHandoffs();
        foreach (TaskInteractionHandoff expired in this.taskInteractionLifecycle.ExpirePendingHandoffs())
        {
            this.conversationStore.DiscardPending(expired.EventId);
            this.interactionContextStore.DiscardPending(expired.EventId);
        }
        foreach (OperationKey operation in this.activeTaskActions.Keys.ToArray())
        {
            if (!this.activeTaskActions.TryGetValue(operation, out ActiveTaskOperation? active))
                continue;

            try
            {
                TaskExecutionOutcome outcome = active.Requests.Any(request => this.actionCancellationRegistry.TryConsumeCancelled(request.ActionId))
                    ? this.taskExecutionDriver.Cancel(operation, "action cancelled while travelling")
                    : this.taskExecutionDriver.Poll(operation, world);
                if (!outcome.Result.IsTerminal)
                    continue;

                this.activeTaskActions.Remove(operation);
                foreach (ActionRequest request in active.Requests)
                {
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(
                        ProtocolMapper.BuildTaskDriverActionResult(request, active.Source, outcome.Result, world),
                        request.Capability);
                }
            }
            catch (Exception ex)
            {
                this.activeTaskActions.Remove(operation);
                foreach (ActionRequest request in active.Requests)
                    this.actionCancellationRegistry.Clear(request.ActionId);
                this.monitor.Log($"GameAgent task action polling failed: {ex.Message}", LogLevel.Error);
                foreach (ActionRequest request in active.Requests)
                    this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "task_action_failed", ex), request.Capability);
            }
        }

        foreach (AdapterMessage message in this.CollectWaitEvidence(world))
            this.SendFireAndForget(this.SendAsync(message, this.cancellation?.Token ?? CancellationToken.None), "TaskEvidence");
    }

    private string ResolveCurrentWorldId()
    {
        if (!Context.IsWorldReady)
            return string.Empty;

        if (!string.IsNullOrWhiteSpace(Constants.SaveFolderName))
            return Constants.SaveFolderName;

        return string.Empty;
    }

    private long ReadCurrentWorldTick()
    {
        return GameClock.ToTick(Game1.year, GameClock.SeasonIndex(Game1.currentSeason), Game1.dayOfMonth, Game1.timeOfDay);
    }

    private async Task SendAsync(AdapterMessage message, CancellationToken cancellationToken)
    {
        var target = this.stream ?? throw new InvalidOperationException("runtime stream is not connected");

        await this.sendMu.WaitAsync(cancellationToken);
        try
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (!ReferenceEquals(target, this.stream))
                throw new OperationCanceledException("runtime stream was replaced");
            this.TraceSend(message);
            await target.RequestStream.WriteAsync(message);
        }
        finally
        {
            this.sendMu.Release();
        }
    }

    private async Task SendBatchAsync(IReadOnlyList<AdapterMessage> messages, CancellationToken cancellationToken)
    {
        var target = this.stream ?? throw new InvalidOperationException("runtime stream is not connected");

        await this.sendMu.WaitAsync(cancellationToken);
        try
        {
            foreach (AdapterMessage message in messages)
            {
                cancellationToken.ThrowIfCancellationRequested();
                if (!ReferenceEquals(target, this.stream))
                    throw new OperationCanceledException("runtime stream was replaced");
                this.TraceSend(message);
                await target.RequestStream.WriteAsync(message);
            }
        }
        finally
        {
            this.sendMu.Release();
        }
    }

    private void TraceSend(AdapterMessage message)
    {
        if (!this.config.EnableProtocolTrace)
            return;

        string detail = message.PayloadCase switch
        {
            AdapterMessage.PayloadOneofCase.Hello =>
                $"AdapterHello message_id={message.MessageId} adapter_id={message.Hello?.AdapterId} game_id={message.Hello?.GameId} session_id={message.Hello?.SessionId}",
            AdapterMessage.PayloadOneofCase.Capabilities =>
                $"CapabilityList message_id={message.MessageId} correlation_id={message.CorrelationId} capabilities=[{string.Join(",", message.Capabilities.Capabilities.Select(capability => capability.Name))}]",
            AdapterMessage.PayloadOneofCase.Event =>
                $"GameEvent message_id={message.MessageId} event_id={message.Event?.EventId} event_type={message.Event?.EventType} world_id={message.Event?.WorldId} target_entity_id={message.Event?.TargetEntityId} entities=[{FormatEntities(message.Event)}]",
            AdapterMessage.PayloadOneofCase.Observation =>
                $"Observation message_id={message.MessageId} correlation_id={message.CorrelationId} entity_id={message.Observation?.EntityId} world_id={message.Observation?.WorldId}",
            AdapterMessage.PayloadOneofCase.ActionResult =>
                $"ActionResult message_id={message.MessageId} action_id={message.ActionResult?.ActionId} status={message.ActionResult?.Status}",
            AdapterMessage.PayloadOneofCase.ActionStatus =>
                $"ActionStatusUpdate message_id={message.MessageId} action_id={message.ActionStatus?.ActionId} status={message.ActionStatus?.Status}",
            AdapterMessage.PayloadOneofCase.Error =>
                $"Error message_id={message.MessageId} correlation_id={message.CorrelationId} code={message.Error?.Code} message={message.Error?.Message}",
            _ =>
                $"{message.PayloadCase} message_id={message.MessageId} correlation_id={message.CorrelationId}",
        };

        this.monitor.Log($"[GameAgent][send] {detail}", LogLevel.Info);
    }

    private void TraceRecv(RuntimeMessage message)
    {
        if (!this.config.EnableProtocolTrace)
            return;

        string detail = message.PayloadCase switch
        {
            RuntimeMessage.PayloadOneofCase.EnvironmentReady =>
                $"EnvironmentReady message_id={message.MessageId} session_id={message.EnvironmentReady?.SessionId}",
            RuntimeMessage.PayloadOneofCase.CapabilityRequest =>
                $"CapabilityRequest message_id={message.MessageId}",
            RuntimeMessage.PayloadOneofCase.EventAck =>
                $"EventAck message_id={message.MessageId} correlation_id={message.CorrelationId} event_id={message.EventAck?.EventId} status={message.EventAck?.Status}",
            RuntimeMessage.PayloadOneofCase.Observe =>
                $"ObserveRequest message_id={message.MessageId} entity_id={message.Observe?.EntityId} world_id={message.Observe?.WorldId}",
            RuntimeMessage.PayloadOneofCase.Action =>
                $"ActionRequest message_id={message.MessageId} action_id={message.Action?.ActionId} entity_id={message.Action?.EntityId} world_id={message.Action?.WorldId} capability={message.Action?.Capability} source_event_id={message.Action?.SourceEventId} source_turn_id={message.Action?.SourceTurnId} {FormatActionArguments(message.Action)}",
            RuntimeMessage.PayloadOneofCase.CancelAction =>
                $"CancelActionRequest message_id={message.MessageId} action_id={message.CancelAction?.ActionId} reason={message.CancelAction?.Reason}",
            RuntimeMessage.PayloadOneofCase.Error =>
                $"Error message_id={message.MessageId} correlation_id={message.CorrelationId} code={message.Error?.Code} message={message.Error?.Message}",
            _ =>
                $"{message.PayloadCase} message_id={message.MessageId} correlation_id={message.CorrelationId}",
        };

        this.monitor.Log($"[GameAgent][recv] {detail}", LogLevel.Info);
    }

    private ActionResult HandleEmoteAction(ActionRequest request)
    {
        NPC npc = this.RequireNpc(request.EntityId);
        string emote = ProtocolMapper.RequireEmoteArgument(request);

        string appliedEmote = this.emoteCapability.Emote(npc, emote);
        return ProtocolMapper.BuildSucceededActionResult(request, "emote", appliedEmote);
    }

    private void HandleResolveMeetingAction(ActionRequest request)
    {
        try
        {
            if (request.TaskSource is not null)
            {
                this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "invalid_task_source", "resolve_meeting requires a current player interaction"), request.Capability);
                return;
            }
            if (!this.TryGuardInteractionContext(request, requireProximity: true, out NPC? npc, out _, out ActionResult? rejected))
            {
                this.SendActionResult(rejected ?? throw new InvalidOperationException("interaction guard rejected without ActionResult"), request.Capability);
                return;
            }

            RuntimeWorldSnapshot world = this.worldContext.Current ?? throw new InvalidOperationException("world context is unavailable");
            NPC guardedNpc = npc ?? throw new InvalidOperationException("interaction guard passed without NPC");
            MeetingRequest input = ProtocolMapper.RequireResolveMeetingArgument(request);
            MeetingResolution resolution = this.resolveMeetingCapability.Resolve(
                world,
                request.EntityId,
                ProtocolMapper.PlayerEntityId,
                guardedNpc.currentLocation?.Name ?? string.Empty,
                input,
                ReadFestivalKnowledge(input.TargetDate)
            );
            this.SendActionResult(ProtocolMapper.BuildMeetingResolutionResult(request, world, resolution, ProtocolMapper.PlayerEntityId), request.Capability);
        }
        catch (ArgumentException ex)
        {
            this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "invalid_action_arguments", ex.Message), request.Capability);
        }
        catch (Exception ex)
        {
            this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "resolve_meeting_failed", ex), request.Capability);
        }
    }

    private static FestivalKnowledge ReadFestivalKnowledge(MeetingDate date)
    {
        try
        {
            Dictionary<string, string> festivalDates = Game1.content.Load<Dictionary<string, string>>("Data\\Festivals\\FestivalDates");
            return festivalDates.ContainsKey(date.Season + date.DayOfMonth.ToString(CultureInfo.InvariantCulture))
                ? FestivalKnowledge.Festival
                : FestivalKnowledge.NotFestival;
        }
        catch
        {
            return FestivalKnowledge.Unknown;
        }
    }

    private void HandleMoveToLandmarkAction(ActionRequest request)
    {
        try
        {
            RuntimeWorldSnapshot world = this.worldContext.Current ?? throw new InvalidOperationException("world context is unavailable");
            TaskOperationSource source = ProtocolMapper.RequireTaskOperationSource(request, world);
            string landmarkId = ProtocolMapper.RequireMoveToLandmarkArgument(request);
            TaskExecutionOutcome outcome = this.taskExecutionDriver.BeginTravel(source, world, landmarkId, this.landmarkCatalogStore.Current);
            if (outcome.Result.IsTerminal)
            {
                this.SendActionResult(ProtocolMapper.BuildTaskDriverActionResult(request, source, outcome.Result, world), request.Capability);
                return;
            }

            if (!this.activeTaskActions.TryGetValue(source.Operation, out ActiveTaskOperation? active))
            {
                active = new ActiveTaskOperation(source, new List<ActionRequest>());
                this.activeTaskActions.Add(source.Operation, active);
            }
            if (!active.Requests.Any(existing => string.Equals(existing.ActionId, request.ActionId, StringComparison.Ordinal)))
                active.Requests.Add(request);

            Struct metadata = ProtocolMapper.BuildTaskDriverStatusMetadata(outcome.Result);
            this.SendActionStatusUpdate(request, ActionStatus.Accepted, metadata);
            this.SendActionStatusUpdate(request, ActionStatus.Running, metadata);
        }
        catch (ArgumentException ex)
        {
            this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "invalid_action_arguments", ex.Message), request.Capability);
        }
        catch (Exception ex)
        {
            this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "task_action_failed", ex), request.Capability);
        }
    }

    private void HandleWaitForPlayerAction(ActionRequest request)
    {
        try
        {
            RuntimeWorldSnapshot world = this.worldContext.Current ?? throw new InvalidOperationException("world context is unavailable");
            TaskOperationSource source = ProtocolMapper.RequireTaskOperationSource(request, world);
            ProtocolMapper.RequireWaitForPlayerArgument(request);
            TaskExecutionOutcome outcome = this.taskExecutionDriver.BeginWait(source, world, this.landmarkCatalogStore.Current);
            if (string.Equals(outcome.Result.Status, "succeeded", StringComparison.Ordinal) && !outcome.Replayed)
            {
                Landmark landmark = this.landmarkCatalogStore.Current.Find(source.Contract.LandmarkId)
                    ?? throw new InvalidOperationException("task landmark disappeared after wait admission");
                if (!this.meetingWaitMonitor.Register(source, landmark.Position, world.NowTick))
                    throw new InvalidOperationException("wait monitor rejected an admitted operation");
            }
            this.SendActionResult(
                ProtocolMapper.BuildWaitRegisteredActionResult(request, source, outcome.Result, world),
                request.Capability);
        }
        catch (ArgumentException ex)
        {
            this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "invalid_action_arguments", ex.Message), request.Capability);
        }
        catch (Exception ex)
        {
            this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "wait_registration_failed", ex), request.Capability);
        }
    }

    private List<AdapterMessage> CollectWaitEvidence(RuntimeWorldSnapshot world)
    {
        List<AdapterMessage> messages = new();
        WorldPosition? player = Context.IsWorldReady && Game1.player?.currentLocation is not null
            ? new WorldPosition(Game1.player.currentLocation.NameOrUniqueName, Game1.player.TilePoint.X, Game1.player.TilePoint.Y)
            : null;
        foreach (OperationKey operation in this.meetingWaitMonitor.Operations.ToArray())
        {
            WorldPosition npcPosition;
            bool owned;
            try
            {
                npcPosition = this.taskExecutionDriver.ReadPosition(operation.NpcEntityId);
                owned = this.taskExecutionDriver.OwnsWait(operation);
            }
            catch
            {
                npcPosition = new WorldPosition("unavailable", 0, 0);
                owned = false;
            }

            WaitEvidence? evidence = this.meetingWaitMonitor.Observe(operation, world, new NpcWaitSample(npcPosition, owned), player);
            if (evidence is null)
                continue;

            // A save moves the world to a new generation while the operation keeps its own binding.
            // Such a fact may only be admitted through an observation reply, so it waits until the
            // runtime asks for this entity instead of being pushed as an event.
            if (evidence.Source.Operation.ExecutionGeneration != world.ExecutionGeneration)
            {
                if (evidence.Source.Operation.ExecutionGeneration > world.ExecutionGeneration)
                {
                    this.monitor.Log($"GameAgent wait evidence from a future generation ignored: {evidence.Code}.", LogLevel.Warn);
                    continue;
                }
                this.deferredTaskEvidence.Add(operation.NpcEntityId, ProtocolMapper.BuildWaitEvidenceFact(evidence, world));
                this.taskExecutionDriver.FinishWait(operation, evidence.Code);
                this.monitor.Log($"GameAgent wait evidence {evidence.Code} held for the next observation of {operation.NpcEntityId}.", LogLevel.Debug);
                continue;
            }

            GameEvent gameEvent = ProtocolMapper.BuildWaitEvidenceEvent(
                evidence,
                world,
                unchecked((ulong)Interlocked.Increment(ref this.eventSequence)));
            if (gameEvent.InteractionSource is not null)
            {
                if (!this.TryPrepareTaskArrival(gameEvent, evidence, out string reason))
                {
                    this.taskExecutionDriver.FinishWait(operation, "interaction_handoff_failed");
                    gameEvent.InteractionSource = null;
                    this.monitor.Log($"GameAgent task arrival interaction suppressed: {reason}.", LogLevel.Warn);
                }
            }
            else
            {
                this.taskExecutionDriver.FinishWait(operation, evidence.Code);
            }
            messages.Add(new AdapterMessage
            {
                MessageId = gameEvent.EventId,
                Event = gameEvent,
            });
        }
        return messages;
    }

    private bool TryPrepareTaskArrival(GameEvent gameEvent, WaitEvidence evidence, out string reason)
    {
        if (!this.taskInteractionLifecycle.TryBegin(gameEvent.EventId, evidence.Source, out reason))
            return false;
        try
        {
            NPC npc = this.RequireNpc(evidence.Source.Operation.NpcEntityId);
            string conversationId = this.conversationStore.PrepareInteraction(
                gameEvent.WorldId,
                evidence.Source.Operation.NpcEntityId,
                ProtocolMapper.PlayerEntityId,
                gameEvent.EventId);
            this.taskInteractionConversations.Register(conversationId, gameEvent.EventId);
            InteractionContextSnapshot snapshot = this.BuildInteractionContextSnapshot(
                gameEvent.EventId,
                gameEvent.WorldId,
                npc,
                Game1.player,
                conversationId);
            if (!this.interactionContextStore.TryReserve(snapshot, out reason))
            {
                this.conversationStore.DiscardPending(gameEvent.EventId);
                this.taskInteractionConversations.RemoveConversation(conversationId);
                this.taskInteractionLifecycle.Reject(gameEvent.EventId, reason);
                return false;
            }
            return true;
        }
        catch
        {
            this.conversationStore.DiscardPending(gameEvent.EventId);
            this.interactionContextStore.DiscardPending(gameEvent.EventId);
            this.RemoveTaskInteractionConversation(gameEvent.EventId);
            this.taskInteractionLifecycle.Reject(gameEvent.EventId, "interaction_prepare_failed");
            throw;
        }
    }

    private void HandlePresentDialogueAction(ActionRequest request)
    {
        try
        {
            if (!this.TryGuardInteractionContext(request, requireProximity: true, out NPC? npc, out InteractionContextSnapshot? interaction, out ActionResult? rejected))
            {
                this.SendActionResult(rejected ?? throw new InvalidOperationException("interaction guard rejected without ActionResult"), request.Capability);
                return;
            }

            NPC guardedNpc = npc ?? throw new InvalidOperationException("interaction guard passed without NPC");
            PresentDialogueInput input = ProtocolMapper.RequirePresentDialogueArgument(request);
            string taskInteractionEventId = interaction is null
                ? string.Empty
                : this.taskInteractionConversations.FindTaskEvent(interaction.ConversationId) ?? string.Empty;
            bool finalDialogue = input.ReplyOptions.Count == 0 && !input.AllowFreeText;
            if (!string.IsNullOrWhiteSpace(taskInteractionEventId))
                this.taskInteractionConversations.MarkPresentation(request.SourceEventId, interaction!.ConversationId);
            this.presentDialogueCapability.Present(
                guardedNpc,
                Game1.player,
                this.currentWorldId,
                input,
                isCancelled: () => this.actionCancellationRegistry.TryConsumeCancelled(request.ActionId),
                onCancelled: () =>
                {
                    this.ordinaryInteractions.End(interaction!.ConversationId);
                    this.CompleteTaskInteraction(taskInteractionEventId, "dialogue_cancelled");
                    this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, "action cancelled before dialogue display"), request.Capability);
                },
                onDisplayed: conversationId =>
                {
                    this.ordinaryInteractions.Presented(request.SourceEventId);
                    this.SendActionResult(ProtocolMapper.BuildPresentDialogueSucceededActionResult(request, conversationId, input), request.Capability);
                },
                onFailed: ex =>
                {
                    this.ordinaryInteractions.End(interaction!.ConversationId);
                    this.CompleteTaskInteraction(taskInteractionEventId, "dialogue_failed");
                    this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "action_failed", ex), request.Capability);
                },
                onSubmitted: submission => this.SendPlayerDialogueSubmission(guardedNpc, Game1.player, submission),
                onAbandoned: () =>
                {
                    this.ordinaryInteractions.End(interaction!.ConversationId);
                    this.ReleaseInteractionContext(request.SourceEventId);
                },
                onFinished: () =>
                {
                    if (finalDialogue)
                    {
                        this.ordinaryInteractions.End(interaction!.ConversationId);
                        this.CompleteTaskInteraction(taskInteractionEventId, "dialogue_finished");
                    }
                }
            );
        }
        catch (ArgumentException ex)
        {
            this.monitor.Log($"GameAgent present_dialogue rejected: {ex.Message}", LogLevel.Warn);
            this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "invalid_action_arguments", ex.Message), request.Capability);
        }
        catch (Exception ex)
        {
            this.monitor.Log($"GameAgent present_dialogue failed: {ex.Message}", LogLevel.Error);
            this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "action_failed", ex), request.Capability);
        }
    }

    private void HandleApproachPlayerAction(ActionRequest request)
    {
        LeaseToken? lease = null;
        OperationKey? operation = null;
        bool borrowedTaskInteraction = false;
        void ReleaseControl(string reason)
        {
            if (borrowedTaskInteraction && operation is not null)
                this.taskInteractionLifecycle.FinishApproach(operation, returnToInteraction: true);
            else if (lease is not null)
                this.npcControlLease.Release(lease, reason);
        }
        try
        {
            if (request.TaskSource is not null)
            {
                this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(
                    request,
                    "interaction_context_missing",
                    "background task actions cannot approach the player without an interaction source"), request.Capability);
                return;
            }
            if (!this.TryGuardInteractionContext(request, requireProximity: false, out NPC? npc, out InteractionContextSnapshot? interaction, out ActionResult? rejected))
            {
                this.SendActionResult(rejected ?? throw new InvalidOperationException("interaction guard rejected without ActionResult"), request.Capability);
                return;
            }
            ProtocolMapper.RequireApproachPlayerArgument(request);
            if (this.actionCancellationRegistry.TryConsumeCancelled(request.ActionId))
            {
                this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, "action cancelled before execution"), request.Capability);
                return;
            }

            RuntimeWorldSnapshot world = this.worldContext.Current ?? throw new InvalidOperationException("world context is unavailable");
            TaskInteractionHandoff? taskArrival = this.taskInteractionLifecycle.FindCommitted(request.SourceEventId);
            if (taskArrival is null && interaction is not null &&
                this.taskInteractionConversations.FindTaskEvent(interaction.ConversationId) is string taskInteractionEventId)
            {
                taskArrival = this.taskInteractionLifecycle.FindCommitted(taskInteractionEventId);
            }
            if (taskArrival is not null)
            {
                operation = taskArrival.Source.Operation with { OperationId = "approach:" + request.ActionId };
                if (!this.taskInteractionLifecycle.TryBeginApproach(taskArrival.EventId, operation))
                {
                    this.CompleteTaskInteraction(taskArrival.EventId, "control_lost");
                    this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "control_lost", "task interaction control is unavailable"), request.Capability);
                    return;
                }
                borrowedTaskInteraction = true;
            }
            else
            {
                if (!this.ordinaryInteractions.SuspendForMove(request.SourceEventId, request.ActionId))
                {
                    this.CloseInteractionConversation(interaction);
                    this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "control_lost", "interaction control is unavailable"), request.Capability);
                    return;
                }
                operation = new OperationKey(
                    world.WorldId,
                    world.WorldRunId,
                    world.ExecutionGeneration,
                    request.EntityId,
                    "interaction:" + request.SourceEventId,
                    request.ActionId);
                LeaseAttempt attempt = this.npcControlLease.Acquire(operation, "approach");
                if (!attempt.Acquired)
                {
                    this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, attempt.Code, "NPC is controlled by another operation"), request.Capability);
                    return;
                }
                lease = attempt.Token;
            }
            NPC guardedNpc = npc ?? throw new InvalidOperationException("interaction guard passed without NPC");
            ApproachPlayerStart start = this.approachPlayerCapability.Start(
                request.ActionId,
                guardedNpc,
                Game1.player,
                isCancelled: () => this.actionCancellationRegistry.IsCancelled(request.ActionId),
                onSucceeded: result =>
                {
                    ReleaseControl("arrived");
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildApproachPlayerSucceededActionResult(request, result), request.Capability);
                },
                onCancelled: reason =>
                {
                    ReleaseControl("cancelled");
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, reason), request.Capability);
                },
                onFailed: (code, ex) =>
                {
                    ReleaseControl(code);
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, code, ex), request.Capability);
                });

            Struct metadata = ProtocolMapper.BuildApproachPlayerStatusMetadata(start);
            this.SendActionStatusUpdate(request, ActionStatus.Accepted, metadata);
            this.SendActionStatusUpdate(request, ActionStatus.Running, metadata);
            if (start.AlreadyAtTarget)
            {
                ReleaseControl("already_at_target");
                this.actionCancellationRegistry.Clear(request.ActionId);
                ApproachPlayerResult result = ApproachPlayerCapability.CompleteAlreadyAtTarget(start, guardedNpc, Game1.player);
                this.SendActionResult(ProtocolMapper.BuildApproachPlayerSucceededActionResult(request, result), request.Capability);
            }
        }
        catch (ApproachPlayerException ex)
        {
            ReleaseControl(ex.Code);
            this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, ex.Code, ex.Message), request.Capability);
        }
        catch (ArgumentException ex)
        {
            ReleaseControl("invalid_action_arguments");
            this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "invalid_action_arguments", ex.Message), request.Capability);
        }
        catch (OperationCanceledException ex)
        {
            ReleaseControl("cancelled");
            this.actionCancellationRegistry.Clear(request.ActionId);
            this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, ex.Message), request.Capability);
        }
        catch (Exception ex)
        {
            ReleaseControl("approach_failed");
            this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "approach_failed", ex), request.Capability);
        }
    }

    private void HandleMoveToAction(ActionRequest request)
    {
        LeaseToken? lease = null;
        try
        {
            if (!this.TryGuardInteractionContext(request, requireProximity: true, out NPC? npc, out InteractionContextSnapshot? interaction, out ActionResult? rejected))
            {
                this.SendActionResult(rejected ?? throw new InvalidOperationException("interaction guard rejected without ActionResult"), request.Capability);
                return;
            }

            if (this.actionCancellationRegistry.TryConsumeCancelled(request.ActionId))
            {
                this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, "action cancelled before execution"), request.Capability);
                return;
            }

            NPC guardedNpc = npc ?? throw new InvalidOperationException("interaction guard passed without NPC");
            RuntimeWorldSnapshot world = this.worldContext.Current ?? throw new InvalidOperationException("world context is unavailable");
            OperationKey operation = new(
                world.WorldId,
                world.WorldRunId,
                world.ExecutionGeneration,
                request.EntityId,
                "interaction:" + request.SourceEventId,
                request.ActionId);
            if (!this.ordinaryInteractions.SuspendForMove(request.SourceEventId, request.ActionId))
            {
                this.CloseInteractionConversation(interaction);
                this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "control_lost", "interaction control is unavailable"), request.Capability);
                return;
            }
            LeaseAttempt leaseAttempt = this.npcControlLease.Acquire(operation, "travelling");
            if (!leaseAttempt.Acquired)
            {
                this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, leaseAttempt.Code, "NPC is controlled by another operation"), request.Capability);
                return;
            }
            lease = leaseAttempt.Token;
            MoveToInput input = ProtocolMapper.RequireMoveToArgument(request);
            MoveToStart start = this.moveToCapability.Start(
                request.ActionId,
                guardedNpc,
                input,
                isCancelled: () => this.actionCancellationRegistry.IsCancelled(request.ActionId),
                onSucceeded: progress =>
                {
                    this.npcControlLease.Release(lease!, "arrived");
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildMoveToSucceededActionResult(request, progress), request.Capability);
                },
                onCancelled: reason =>
                {
                    this.npcControlLease.Release(lease!, "cancelled");
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, reason), request.Capability);
                },
                onFailed: (code, ex) =>
                {
                    this.npcControlLease.Release(lease!, code);
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, code, ex), request.Capability);
                }
            );

            this.SendActionStatusUpdate(request, ActionStatus.Accepted, ProtocolMapper.BuildMoveToStatusMetadata(start.Progress));
            this.SendActionStatusUpdate(request, ActionStatus.Running, ProtocolMapper.BuildMoveToStatusMetadata(start.Progress));
            if (start.AlreadyAtTarget)
            {
                this.npcControlLease.Release(lease!, "already_at_target");
                this.actionCancellationRegistry.Clear(request.ActionId);
                this.SendActionResult(ProtocolMapper.BuildMoveToSucceededActionResult(request, start.Progress), request.Capability);
            }
        }
        catch (ArgumentException ex)
        {
            if (lease is not null)
                this.npcControlLease.Release(lease, "invalid_move_target");
            this.monitor.Log($"GameAgent move_to rejected: {ex.Message}", LogLevel.Warn);
            this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "invalid_move_target", ex.Message), request.Capability);
        }
        catch (OperationCanceledException ex)
        {
            if (lease is not null)
                this.npcControlLease.Release(lease, "cancelled");
            this.actionCancellationRegistry.Clear(request.ActionId);
            this.monitor.Log($"GameAgent move_to cancelled: {ex.Message}", LogLevel.Debug);
            this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, ex.Message), request.Capability);
        }
        catch (Exception ex)
        {
            if (lease is not null)
                this.npcControlLease.Release(lease, "move_failed");
            this.monitor.Log($"GameAgent move_to failed: {ex.Message}", LogLevel.Error);
            this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "move_failed", ex), request.Capability);
        }
    }

    private ActionResult HandleFacePlayerAction(ActionRequest request)
    {
        NPC npc = this.RequireNpc(request.EntityId);
        try
        {
            string direction = this.facePlayerCapability.FacePlayer(npc, Game1.player);
            return ProtocolMapper.BuildFacePlayerSucceededActionResult(request, direction);
        }
        catch (ArgumentException ex) when (ex.Message.Contains("same location", StringComparison.OrdinalIgnoreCase))
        {
            return ProtocolMapper.BuildRejectedActionResult(request, "different_location", ex.Message);
        }
    }

    private InteractionContextSnapshot BuildInteractionContextSnapshot(string eventId, string worldId, NPC npc, Farmer player, string conversationId)
    {
        return new InteractionContextSnapshot(
            EventId: eventId,
            WorldId: worldId,
            NpcEntityId: ProtocolMapper.ToNpcEntityId(npc),
            PlayerEntityId: ProtocolMapper.PlayerEntityId,
            ConversationId: conversationId,
            NpcLocation: npc.currentLocation?.Name ?? "unknown",
            NpcTileX: npc.TilePoint.X,
            NpcTileY: npc.TilePoint.Y,
            PlayerLocation: player.currentLocation?.Name ?? "unknown",
            PlayerTileX: player.TilePoint.X,
            PlayerTileY: player.TilePoint.Y,
            MaxInteractionDistance: InteractionPolicy.MaxInteractionDistance
        );
    }

    private bool TryGuardInteractionContext(ActionRequest request, bool requireProximity, out NPC? npc, out InteractionContextSnapshot? snapshot, out ActionResult? rejected)
    {
        npc = null;
        rejected = null;

        if (!this.interactionContextStore.TryResolve(request, out snapshot, out string errorCode, out string message))
        {
            this.CloseInteractionConversation(snapshot);
            rejected = ProtocolMapper.BuildRejectedActionResult(request, errorCode, message);
            return false;
        }

        InteractionContextSnapshot resolvedSnapshot = snapshot ?? throw new InvalidOperationException("interaction context resolved without snapshot");
        npc = this.RequireNpc(request.EntityId);
        InteractionContextCurrentState current = this.BuildInteractionContextCurrentState(npc, Game1.player);
        if (!this.interactionContextStore.TryValidateCurrentState(resolvedSnapshot, current, requireProximity, out errorCode, out message))
        {
            this.CloseInteractionConversation(resolvedSnapshot);
            rejected = ProtocolMapper.BuildRejectedActionResult(request, errorCode, message);
            return false;
        }

        return true;
    }

    private InteractionContextCurrentState BuildInteractionContextCurrentState(NPC npc, Farmer player)
    {
        string npcEntityId = ProtocolMapper.ToNpcEntityId(npc);
        ConversationSnapshot? conversation = this.conversationStore.GetCurrentConversation(this.currentWorldId, npcEntityId, ProtocolMapper.PlayerEntityId);
        return new InteractionContextCurrentState(
            WorldId: this.currentWorldId,
            NpcEntityId: npcEntityId,
            PlayerEntityId: ProtocolMapper.PlayerEntityId,
            ConversationId: conversation?.ConversationId ?? string.Empty,
            NpcLocation: npc.currentLocation?.Name ?? "unknown",
            NpcTileX: npc.TilePoint.X,
            NpcTileY: npc.TilePoint.Y,
            PlayerLocation: player.currentLocation?.Name ?? "unknown",
            PlayerTileX: player.TilePoint.X,
            PlayerTileY: player.TilePoint.Y
        );
    }

    private void CloseInteractionConversation(InteractionContextSnapshot? snapshot)
    {
        if (snapshot is null)
            return;

        this.conversationStore.CloseIfConversation(snapshot.WorldId, snapshot.NpcEntityId, snapshot.PlayerEntityId, snapshot.ConversationId);
        this.ordinaryInteractions.End(snapshot.ConversationId);
    }

    private void ReleaseInteractionContext(string eventId)
    {
        if (string.IsNullOrWhiteSpace(eventId))
            return;

        InteractionContextSnapshot? current = this.interactionContextStore.TryGet(eventId);
        string? conversationId = current?.ConversationId ??
            this.taskInteractionConversations.FindPresentationConversation(eventId);
        string taskInteractionEventId = conversationId is null
            ? eventId
            : this.taskInteractionConversations.FindTaskEvent(conversationId) ?? eventId;
        this.CompleteTaskInteraction(taskInteractionEventId, "interaction_abandoned");
        this.conversationStore.ReleaseInteractionPin(eventId);
        InteractionContextSnapshot? released = this.interactionContextStore.Release(eventId);
        this.LogReleasedInteractionContext(released);
        this.CloseWaitingForNpcOnMainThread(released?.NpcEntityId);
    }

    private void CompleteTaskInteraction(string eventId, string reason)
    {
        if (string.IsNullOrWhiteSpace(eventId))
            return;
        this.taskInteractionLifecycle.Complete(eventId, reason);
        this.RemoveTaskInteractionConversation(eventId);
    }

    private void CleanupReleasedTaskInteraction(string eventId)
    {
        if (string.IsNullOrWhiteSpace(eventId))
            return;
        this.conversationStore.DiscardPending(eventId);
        this.interactionContextStore.DiscardPending(eventId);
        this.RemoveTaskInteractionConversation(eventId);
    }

    private void RemoveTaskInteractionConversation(string eventId)
    {
        this.taskInteractionConversations.RemoveTaskEvent(eventId);
    }

    private void ResetTaskRuntimeState(string reason)
    {
        this.ordinaryInteractions.Clear();
        this.taskInteractionLifecycle.Clear(reason);
        this.taskExecutionDriver.Clear();
        this.activeTaskActions.Clear();
        this.meetingWaitMonitor.Clear();
        this.taskInteractionConversations.Clear();
        this.deferredTaskEvidence.Clear();
    }

    private void AbortTaskConversation(
        string worldId,
        string npcEntityId,
        string conversationId,
        string taskInteractionEventId,
        string reason)
    {
        this.ordinaryInteractions.End(conversationId);
        this.conversationStore.ReleaseInteractionPin(taskInteractionEventId);
        if (string.IsNullOrWhiteSpace(taskInteractionEventId))
            return;
        this.CompleteTaskInteraction(taskInteractionEventId, reason);
        this.interactionContextStore.Release(taskInteractionEventId);
        this.conversationStore.CloseIfConversation(
            worldId,
            npcEntityId,
            ProtocolMapper.PlayerEntityId,
            conversationId);
    }

    private void CloseWaitingForNpcOnMainThread(string? npcEntityId)
    {
        if (string.IsNullOrWhiteSpace(npcEntityId))
            return;

        this.dispatcher.Enqueue(() => this.presentDialogueCapability.CloseWaitingForNpc(npcEntityId));
    }

    private void LogReleasedInteractionContext(InteractionContextSnapshot? snapshot)
    {
        if (snapshot is null)
            return;

        this.monitor.Log($"GameAgent interaction context released: event_id={snapshot.EventId} entity_id={snapshot.NpcEntityId}", LogLevel.Debug);
    }

    private void SendActionResult(ActionResult result, string capability)
    {
        this.ordinaryInteractions.MoveFinished(result.ActionId);
        this.SendFireAndForget(
            this.SendAsync(
                new AdapterMessage
                {
                    MessageId = ProtocolMapper.NewMessageId("action_result_msg"),
                    ActionResult = result,
                },
                this.cancellation?.Token ?? CancellationToken.None
            ),
            "ActionResult"
        );

        this.monitor.Log(
            $"GameAgent ActionResult sent: action_id={result.ActionId} capability={capability} status={result.Status} code={result.Error?.Code ?? string.Empty} message={result.Error?.Message ?? string.Empty}",
            ActionResultLogLevel(result.Status)
        );
    }

    private static LogLevel ActionResultLogLevel(ActionStatus status)
    {
        return status is ActionStatus.Rejected or ActionStatus.Failed or ActionStatus.Cancelled
            ? LogLevel.Warn
            : LogLevel.Debug;
    }

    private void SendActionStatusUpdate(ActionRequest request, ActionStatus status, Struct metadata)
    {
        this.SendFireAndForget(
            this.SendAsync(
                new AdapterMessage
                {
                    MessageId = ProtocolMapper.NewMessageId("action_status_msg"),
                    ActionStatus = new ActionStatusUpdate
                    {
                        ActionId = request.ActionId,
                        Status = status,
                        Metadata = metadata,
                    },
                },
                this.cancellation?.Token ?? CancellationToken.None
            ),
            "ActionStatusUpdate"
        );

        this.monitor.Log($"GameAgent ActionStatusUpdate sent: {request.ActionId} {status}.", LogLevel.Debug);
    }

    private static string FormatEntities(GameEvent? gameEvent)
    {
        if (gameEvent is null)
            return string.Empty;

        return string.Join(",", gameEvent.Entities.Select(entity => $"{entity.EntityType}:{entity.EntityId}"));
    }

    private static string FormatActionArguments(ActionRequest? request)
    {
        return request?.Capability switch
        {
            "emote" => $"emote=\"{FormatStringArgument(request, "emote")}\"",
            "present_dialogue" => $"text=\"{FormatStringArgument(request, "text")}\"",
            "move_to" => FormatMoveToArguments(request),
            _ => string.Empty,
        };
    }

    private static string FormatMoveToArguments(ActionRequest? request)
    {
        string location = FormatStringArgument(request, "location");
        string tileX = FormatIntegerArgument(request, "tile", "x");
        string tileY = FormatIntegerArgument(request, "tile", "y");
        return $"location=\"{location}\" tile=({tileX},{tileY})";
    }

    private static string FormatStringArgument(ActionRequest? request, string name)
    {
        if (request?.Arguments is null || !request.Arguments.Fields.TryGetValue(name, out var value))
            return string.Empty;

        string text = value.StringValue ?? string.Empty;
        return text.Length <= 80 ? text : $"{text[..80]}...";
    }

    private static string FormatIntegerArgument(ActionRequest? request, string structName, string name)
    {
        if (request?.Arguments is null || !request.Arguments.Fields.TryGetValue(structName, out var value))
            return "?";

        var fields = value.StructValue?.Fields;
        if (fields is null || !fields.TryGetValue(name, out var number))
            return "?";

        if (number.KindCase != Value.KindOneofCase.NumberValue)
            return "?";

        double coordinate = number.NumberValue;
        if (double.IsNaN(coordinate) || double.IsInfinity(coordinate) || Math.Truncate(coordinate) != coordinate)
            return "?";

        return coordinate.ToString("0", CultureInfo.InvariantCulture);
    }

    private void SendFireAndForget(Task task, string operation)
    {
        _ = task.ContinueWith(
            failed => this.monitor.Log($"GameAgent {operation} send failed: {failed.Exception}", LogLevel.Error),
            TaskContinuationOptions.OnlyOnFaulted
        );
    }

    private sealed record ActiveTaskOperation(TaskOperationSource Source, List<ActionRequest> Requests);
}
