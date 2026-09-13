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

public sealed class RuntimeClient : IDisposable
{
    private const string ProtocolVersion = "v1alpha2";

    private readonly AdapterConfig config;
    private readonly MainThreadDispatcher dispatcher;
    private readonly ObservationBuilder observationBuilder;
    private readonly ConversationStateStore conversationStore;
    private readonly InteractionContextStore interactionContextStore = new();
    private readonly EmoteCapability emoteCapability;
    private readonly PresentDialogueCapability presentDialogueCapability;
    private readonly FacePlayerCapability facePlayerCapability;
    private readonly MoveToCapability moveToCapability;
    private readonly ActionCancellationRegistry actionCancellationRegistry = new();
    private readonly IMonitor monitor;
    private readonly SemaphoreSlim sendMu = new(1, 1);
    private readonly object connectionGate = new();
    private readonly RuntimeSessionState sessionState = new();
    private readonly RuntimeWorldContext worldContext;

    private GrpcChannel? channel;
    private AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage>? stream;
    private CancellationTokenSource? cancellation;
    private Task? receiveTask;
    private readonly string sessionId = Guid.NewGuid().ToString("N");
    // currentWorldId is maintained from SMAPI main-thread lifecycle events.
    // Background gRPC threads must not resolve Stardew world state directly.
    private volatile string currentWorldId = string.Empty;
    private long eventSequence;
    private int worldBindingSent;
    private long worldBindingClockSequence;
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
        this.monitor = monitor;
        this.worldContext = new RuntimeWorldContext(this.config.GameId, GameClock.ClockId);
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
            this.receiveTask = Task.Run(() => this.RunAsync(this.cancellation.Token));
        }
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
            InteractionContextSnapshot snapshot = this.BuildInteractionContextSnapshot(eventId, worldId, npc, player, conversationId);
            if (!this.interactionContextStore.TryReserve(snapshot, out reason))
            {
                this.conversationStore.DiscardPending(eventId);
                return false;
            }

            GameEvent gameEvent = ProtocolMapper.BuildPlayerInteractedWithNpcEvent(npc, player, conversationId, trigger, sequence, worldId, eventId);
            this.presentDialogueCapability.QueueWaitingForNpc(npcEntityId);
            this.SendFireAndForget(this.SendPreparedGameEventAsync(gameEvent, eventId), "GameEvent");
            reason = string.Empty;
            return true;
        }
        catch (Exception ex)
        {
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
        if (!this.IsReady || !RuntimeWorldScope.IsAvailable(worldId))
            return;

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
                this.monitor.Log($"GameAgent player dialogue event suppressed: {reason}.", LogLevel.Debug);
                return;
            }

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
            this.CloseWaitingForNpcOnMainThread(npcEntityId);
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
            await this.SendHelloAsync(cancellationToken);

            while (this.stream is not null && await this.stream.ResponseStream.MoveNext(cancellationToken))
            {
                RuntimeMessage message = this.stream.ResponseStream.Current;
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
                await this.HandleWorldBindingReadyAsync(message.WorldBindingReady, cancellationToken);
                break;

            case RuntimeMessage.PayloadOneofCase.TaskControl:
                await this.HandleTaskControlAsync(message.TaskControl, cancellationToken);
                break;

            case RuntimeMessage.PayloadOneofCase.EventAck:
                this.HandleEventAck(message.EventAck);
                break;

            case RuntimeMessage.PayloadOneofCase.TurnCompletion:
                this.HandleTurnCompletion(message.TurnCompletion);
                break;

            case RuntimeMessage.PayloadOneofCase.Observe:
                if (message.Observe is not null)
                    this.dispatcher.Enqueue(() => this.HandleObserveOnMainThread(message.MessageId, message.Observe));
                break;

            case RuntimeMessage.PayloadOneofCase.Action:
                if (message.Action is not null)
                    this.dispatcher.Enqueue(() => this.HandleActionOnMainThread(message.Action));
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
        CapabilityList capabilities = CapabilityCatalog.BuildEnvironmentCapabilities();

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
        RuntimeWorldSnapshot? snapshot = this.worldContext.Current;
        if (snapshot is null ||
            this.sessionState.Phase != RuntimeSessionPhase.AwaitingWorldBindingReady ||
            Interlocked.CompareExchange(ref this.worldBindingSent, 1, 0) != 0)
        {
            return;
        }

        try
        {
            Interlocked.Exchange(ref this.worldBindingClockSequence, checked((long)snapshot.ClockSequence));
            await this.SendAsync(
                new AdapterMessage
                {
                    MessageId = ProtocolMapper.NewMessageId("world_binding"),
                    WorldBinding = ProtocolMapper.BuildWorldBinding(snapshot, this.config.AgentTargets),
                },
                cancellationToken
            );
            this.monitor.Log($"GameAgent WorldBinding sent: world_id={snapshot.WorldId} run_id={snapshot.WorldRunId} sequence={snapshot.ClockSequence}.", LogLevel.Info);
        }
        catch
        {
            Interlocked.Exchange(ref this.worldBindingSent, 0);
            Interlocked.Exchange(ref this.worldBindingClockSequence, 0);
            throw;
        }
    }

    private async Task HandleWorldBindingReadyAsync(WorldBindingReady? ready, CancellationToken cancellationToken)
    {
        if (ready is null)
            throw new InvalidOperationException("world_binding_ready_missing");

        if (string.Equals(ready.Status, "ready", StringComparison.Ordinal))
        {
            TaskScope scope = ready.Scope ?? throw new InvalidOperationException("world_binding_scope_missing");
            if (!this.worldContext.TryApplyBinding(scope.GameId, scope.WorldId, scope.WorldRunId, scope.ExecutionGeneration, out string bindingError))
            {
                this.sessionState.PauseTasks();
                throw new InvalidOperationException(bindingError);
            }
        }

        if (!this.sessionState.AcceptWorldBindingReady(ready.Status, out string stateError))
            throw new InvalidOperationException(stateError);

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

    private async Task HandleTaskControlAsync(TaskControlRequest? request, CancellationToken cancellationToken)
    {
        if (request is null)
            return;

        string status = "mismatch";
        RuntimeWorldSnapshot? snapshot = this.worldContext.Current;
        if (this.sessionState.CanUseTasks && snapshot is not null && ScopeMatches(request.Scope, snapshot))
            status = "unconfirmed";

        TaskControlResult result = new()
        {
            Scope = request.Scope,
            TaskId = request.TaskId,
            OperationId = request.OperationId,
            RequestId = request.RequestId,
            Status = status,
        };
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

        if (request.TaskSource is not null && !this.IsTaskReady)
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

        switch (ack.Status)
        {
            case EventAckStatus.Accepted:
                this.conversationStore.CommitPending(ack.EventId);
                this.interactionContextStore.Commit(ack.EventId);
                break;
            case EventAckStatus.Duplicate:
                this.conversationStore.DiscardPending(ack.EventId);
                this.CloseWaitingForNpcOnMainThread(this.interactionContextStore.DiscardPending(ack.EventId)?.NpcEntityId);
                break;
            case EventAckStatus.Rejected:
            case EventAckStatus.Unspecified:
                InteractionContextSnapshot? pending = this.interactionContextStore.DiscardPending(ack.EventId);
                this.conversationStore.DiscardPending(ack.EventId);
                this.CloseWaitingForNpcOnMainThread(pending?.NpcEntityId);
                if (!IsTransientEventReject(ack))
                    this.CloseInteractionConversation(pending);
                break;
        }

        this.monitor.Log(
            $"GameAgent EventAck received: event_id={ack.EventId} status={ack.Status} code={ack.Error?.Code ?? string.Empty} message={ack.Error?.Message ?? string.Empty}",
            ack.Status == EventAckStatus.Rejected ? LogLevel.Warn : LogLevel.Debug
        );
    }

    private static bool IsTransientEventReject(EventAck ack)
    {
        return string.Equals(ack.Error?.Code, "session_queue_full", StringComparison.Ordinal);
    }

    private void HandleTurnCompletion(TurnCompletion? completion)
    {
        if (completion is null)
            return;

        if (!string.IsNullOrWhiteSpace(completion.EventId))
        {
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
        this.moveToCapability.Clear();
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

    public void BeginWorldContext()
    {
        string worldId = this.ResolveCurrentWorldId();
        if (!RuntimeWorldScope.IsAvailable(worldId))
        {
            this.ClearWorldContext();
            return;
        }

        this.ClearConversations();
        this.currentWorldId = worldId;
        this.worldContext.BeginWorld(worldId, this.ReadCurrentWorldTick());
        this.sessionState.PrepareWorldBinding();
        Interlocked.Exchange(ref this.worldBindingSent, 0);
        Interlocked.Exchange(ref this.worldBindingClockSequence, 0);
        this.SendFireAndForget(
            this.TrySendWorldBindingAsync(this.cancellation?.Token ?? CancellationToken.None),
            "WorldBinding"
        );
        this.monitor.Log($"GameAgent world context started: world_id={worldId} run_id={this.worldContext.Current?.WorldRunId}", LogLevel.Debug);
    }

    public void RefreshWorldClock()
    {
        if (this.worldContext.Current is null)
            return;

        this.worldContext.AdvanceClock(this.ReadCurrentWorldTick());
        if (this.IsTaskReady && this.worldContext.Current is RuntimeWorldSnapshot snapshot)
        {
            this.SendFireAndForget(
                this.SendWorldClockAsync(snapshot, this.cancellation?.Token ?? CancellationToken.None),
                "WorldClockUpdate"
            );
        }
    }

    public void ClearWorldContext()
    {
        this.moveToCapability.CancelAll("world context cleared before movement completed");
        this.presentDialogueCapability.CloseAll();
        this.currentWorldId = string.Empty;
        this.worldContext.Clear();
        this.sessionState.PauseTasks();
        Interlocked.Exchange(ref this.worldBindingSent, 0);
        Interlocked.Exchange(ref this.worldBindingClockSequence, 0);
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
        if (this.stream is null)
            throw new InvalidOperationException("runtime stream is not connected");

        await this.sendMu.WaitAsync(cancellationToken);
        try
        {
            this.TraceSend(message);
            await this.stream.RequestStream.WriteAsync(message);
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

    private void HandlePresentDialogueAction(ActionRequest request)
    {
        try
        {
            if (!this.TryGuardInteractionContext(request, requireProximity: false, out NPC? npc, out _, out ActionResult? rejected))
            {
                this.SendActionResult(rejected ?? throw new InvalidOperationException("interaction guard rejected without ActionResult"), request.Capability);
                return;
            }

            NPC guardedNpc = npc ?? throw new InvalidOperationException("interaction guard passed without NPC");
            PresentDialogueInput input = ProtocolMapper.RequirePresentDialogueArgument(request);
            this.presentDialogueCapability.Present(
                guardedNpc,
                Game1.player,
                this.currentWorldId,
                input,
                isCancelled: () => this.actionCancellationRegistry.TryConsumeCancelled(request.ActionId),
                onCancelled: () => this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, "action cancelled before dialogue display"), request.Capability),
                onDisplayed: conversationId => this.SendActionResult(ProtocolMapper.BuildPresentDialogueSucceededActionResult(request, conversationId, input), request.Capability),
                onFailed: ex => this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, "action_failed", ex), request.Capability),
                onSubmitted: submission => this.SendPlayerDialogueSubmission(guardedNpc, Game1.player, submission),
                onAbandoned: () => this.ReleaseInteractionContext(request.SourceEventId)
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

    private void HandleMoveToAction(ActionRequest request)
    {
        try
        {
            if (!this.TryGuardInteractionContext(request, requireProximity: true, out NPC? npc, out _, out ActionResult? rejected))
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
            MoveToInput input = ProtocolMapper.RequireMoveToArgument(request);
            MoveToStart start = this.moveToCapability.Start(
                request.ActionId,
                guardedNpc,
                input,
                isCancelled: () => this.actionCancellationRegistry.IsCancelled(request.ActionId),
                onSucceeded: progress =>
                {
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildMoveToSucceededActionResult(request, progress), request.Capability);
                },
                onCancelled: reason =>
                {
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, reason), request.Capability);
                },
                onFailed: (code, ex) =>
                {
                    this.actionCancellationRegistry.Clear(request.ActionId);
                    this.SendActionResult(ProtocolMapper.BuildFailedActionResult(request, code, ex), request.Capability);
                }
            );

            this.SendActionStatusUpdate(request, ActionStatus.Accepted, ProtocolMapper.BuildMoveToStatusMetadata(start.Progress));
            this.SendActionStatusUpdate(request, ActionStatus.Running, ProtocolMapper.BuildMoveToStatusMetadata(start.Progress));
            if (start.AlreadyAtTarget)
            {
                this.actionCancellationRegistry.Clear(request.ActionId);
                this.SendActionResult(ProtocolMapper.BuildMoveToSucceededActionResult(request, start.Progress), request.Capability);
            }
        }
        catch (ArgumentException ex)
        {
            this.monitor.Log($"GameAgent move_to rejected: {ex.Message}", LogLevel.Warn);
            this.SendActionResult(ProtocolMapper.BuildRejectedActionResult(request, "invalid_move_target", ex.Message), request.Capability);
        }
        catch (OperationCanceledException ex)
        {
            this.actionCancellationRegistry.Clear(request.ActionId);
            this.monitor.Log($"GameAgent move_to cancelled: {ex.Message}", LogLevel.Debug);
            this.SendActionResult(ProtocolMapper.BuildCancelledActionResult(request, ex.Message), request.Capability);
        }
        catch (Exception ex)
        {
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
        ConversationSnapshot? conversation = this.conversationStore.GetActiveConversation(this.currentWorldId, npcEntityId, ProtocolMapper.PlayerEntityId);
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
    }

    private void ReleaseInteractionContext(string eventId)
    {
        if (string.IsNullOrWhiteSpace(eventId))
            return;

        InteractionContextSnapshot? released = this.interactionContextStore.Release(eventId);
        this.LogReleasedInteractionContext(released);
        this.CloseWaitingForNpcOnMainThread(released?.NpcEntityId);
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
}
