using System;
using System.Threading;
using System.Threading.Tasks;
using GameAgent.Protocol.V1Alpha2;
using Grpc.Core;
using Wia.RimWorld.Dialogue;
using Wia.RimWorld.Threading;

namespace Wia.RimWorld.Runtime
{
    /// <summary>
    /// Owns the gRPC stream to the Runtime for the lifetime of the process, and reconnects while the
    /// Runtime is unavailable.
    ///
    /// The receive loop runs on a thread-pool thread and only touches protocol objects. Work that
    /// would read or change game state is marshalled onto the main-thread pump instead - the
    /// observation read and the dialogue execution both go through it - so there is exactly one
    /// place in the adapter where the boundary between the two worlds is crossed.
    /// </summary>
    internal sealed class RuntimeConnection : IDisposable
    {
        private readonly MainThreadPump pump;
        private readonly RuntimeSessionState session = new RuntimeSessionState();
        private readonly SemaphoreSlim sendGate = new SemaphoreSlim(1, 1);
        private readonly object lifecycleGate = new object();

        private CancellationTokenSource cancellation;
        private Task worker;
        private bool disposed;

        /// <summary>
        /// The stream currently open, or null between sessions. Held so that work originating on the
        /// main thread - a gizmo click, a dialogue reply - can send without owning the receive loop.
        /// </summary>
        private AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> activeCall;

        public RuntimeConnection(MainThreadPump pump)
        {
            this.pump = pump ?? throw new ArgumentNullException(nameof(pump));
            this.Observations = new ColonistObservationService(pump);
            this.Dialogue = new DialogueService(this.TrySendEvent);
        }

        public RuntimeSessionState Session
        {
            get { return this.session; }
        }

        public ColonistObservationService Observations { get; private set; }

        public DialogueService Dialogue { get; private set; }

        /// <summary>
        /// True when the handshake has completed and events can be sent. Read from the main thread to
        /// decide whether the dialogue gizmo is usable; the answer can go stale in the same frame,
        /// which is why the send path also reports failure rather than assuming success.
        /// </summary>
        public bool IsSessionActive
        {
            get
            {
                lock (this.lifecycleGate)
                {
                    return !this.disposed && this.activeCall != null && this.session.CanUseRuntime;
                }
            }
        }

        public void Start()
        {
            lock (this.lifecycleGate)
            {
                if (this.disposed)
                {
                    throw new ObjectDisposedException(nameof(RuntimeConnection));
                }

                if (this.worker != null && !this.worker.IsCompleted)
                {
                    return;
                }

                this.cancellation?.Dispose();
                this.cancellation = new CancellationTokenSource();
                CancellationToken token = this.cancellation.Token;
                this.worker = Task.Run(() => this.RunAsync(token));
            }
        }

        public void Dispose()
        {
            CancellationTokenSource source;
            lock (this.lifecycleGate)
            {
                if (this.disposed)
                {
                    return;
                }

                this.disposed = true;
                this.activeCall = null;
                source = this.cancellation;
                this.cancellation = null;
            }

            try
            {
                source?.Cancel();
            }
            catch (ObjectDisposedException)
            {
                // Already cancelled.
            }

            this.sendGate.Dispose();
        }

        /// <summary>
        /// Sends a message from the main thread. Returns false when there is no live session, which
        /// the caller reports to the player rather than swallowing: a click that produces nothing is
        /// the failure mode this project keeps having to remove.
        ///
        /// The write itself is detached. The main thread must never wait on the network, and the
        /// event's delivery is not what the caller is deciding - whether it was handed to a live
        /// session is.
        /// </summary>
        private bool TrySendEvent(GameEvent gameEvent)
        {
            AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> call;
            CancellationToken token;

            lock (this.lifecycleGate)
            {
                call = this.activeCall;
                if (this.disposed || call == null || !this.session.CanUseRuntime)
                {
                    return false;
                }

                token = this.cancellation.Token;
            }

            AdapterMessage message = ProtocolMapper.BuildEventMessage(gameEvent);
            _ = this.SendDetachedAsync(call, message, token);
            return true;
        }

        private async Task SendDetachedAsync(
            AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> call,
            AdapterMessage message,
            CancellationToken token)
        {
            try
            {
                await this.SendAsync(call, message, token).ConfigureAwait(false);
            }
            catch (Exception ex)
            {
                AdapterLog.Warn(
                    "could not send " + message.MessageId + ": " + ex.GetType().Name + ": " + ex.Message);
            }
        }

        private async Task RunAsync(CancellationToken token)
        {
            while (!token.IsCancellationRequested)
            {
                try
                {
                    await this.SessionAsync(token).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    break;
                }
                catch (Exception ex)
                {
                    AdapterLog.Warn("runtime session ended: " + ex.GetType().Name + ": " + ex.Message);
                }
                finally
                {
                    lock (this.lifecycleGate)
                    {
                        this.activeCall = null;
                    }

                    this.session.Disconnect();
                }

                if (token.IsCancellationRequested)
                {
                    break;
                }

                AdapterLog.Info($"reconnecting to the Runtime in {AdapterIdentity.RetryDelay.TotalSeconds:0}s");
                try
                {
                    await Task.Delay(AdapterIdentity.RetryDelay, token).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    break;
                }
            }
        }

        private async Task SessionAsync(CancellationToken token)
        {
            Channel channel = new Channel(
                AdapterIdentity.RuntimeHost,
                AdapterIdentity.RuntimePort,
                ChannelCredentials.Insecure);

            try
            {
                GameAgentGateway.GameAgentGatewayClient client =
                    new GameAgentGateway.GameAgentGatewayClient(channel);

                using (AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> call =
                    client.Connect(cancellationToken: token))
                {
                    string sessionId = Guid.NewGuid().ToString("N");
                    this.session.BeginConnection();

                    lock (this.lifecycleGate)
                    {
                        this.activeCall = call;
                    }

                    await this.SendAsync(call, BuildHello(sessionId), token).ConfigureAwait(false);
                    AdapterLog.Info(
                        $"AdapterHello sent to {AdapterIdentity.RuntimeHost}:{AdapterIdentity.RuntimePort} " +
                        $"session={sessionId} adapter={AdapterIdentity.AdapterId} " +
                        $"game={AdapterIdentity.GameId}/{GameVersionSnapshot.Current} extensions=(none)");

                    while (await call.ResponseStream.MoveNext(token).ConfigureAwait(false))
                    {
                        await this.HandleAsync(call, call.ResponseStream.Current, token).ConfigureAwait(false);
                    }

                    AdapterLog.Info("runtime stream closed by the Runtime");
                }
            }
            finally
            {
                try
                {
                    await channel.ShutdownAsync().ConfigureAwait(false);
                }
                catch (Exception)
                {
                    // Shutdown failures are not the measurement; the session already ended.
                }
            }
        }

        private static AdapterMessage BuildHello(string sessionId)
        {
            AdapterMessage hello = new AdapterMessage
            {
                MessageId = ProtocolMapper.NewMessageId("hello"),
                Hello = new AdapterHello
                {
                    AdapterId = AdapterIdentity.AdapterId,
                    AdapterVersion = AdapterIdentity.AdapterVersion,
                    ProtocolVersion = AdapterIdentity.ProtocolVersion,
                    GameId = AdapterIdentity.GameId,
                    GameVersion = GameVersionSnapshot.Current,
                    SessionId = sessionId,
                },
            };

            // Deliberately empty: this adapter does not negotiate gameagent.tasks.v1.
            return hello;
        }

        private async Task HandleAsync(
            AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> call,
            RuntimeMessage message,
            CancellationToken token)
        {
            switch (message.PayloadCase)
            {
                case RuntimeMessage.PayloadOneofCase.EnvironmentReady:
                    this.HandleEnvironmentReady(message.EnvironmentReady);
                    break;

                case RuntimeMessage.PayloadOneofCase.CapabilityRequest:
                    await this.HandleCapabilityRequestAsync(call, message, token).ConfigureAwait(false);
                    break;

                case RuntimeMessage.PayloadOneofCase.Observe:
                    await this.HandleObserveAsync(call, message, token).ConfigureAwait(false);
                    break;

                case RuntimeMessage.PayloadOneofCase.Action:
                    await this.HandleActionAsync(call, message, token).ConfigureAwait(false);
                    break;

                case RuntimeMessage.PayloadOneofCase.TurnCompletion:
                    AdapterLog.Info(
                        "turn completed turn=" + message.TurnCompletion.TurnId +
                        " status=" + message.TurnCompletion.Status);
                    break;

                case RuntimeMessage.PayloadOneofCase.CancelAction:
                    // present_dialogue answers as soon as the window is up, so by the time a cancel
                    // could arrive there is no action left to cancel. Nothing hangs: the action is
                    // already terminal on both sides.
                    AdapterLog.Info("cancel request ignored: no action is outstanding");
                    break;

                case RuntimeMessage.PayloadOneofCase.Error:
                    AdapterLog.Warn(
                        $"runtime error: {message.Error?.Code} {message.Error?.Message}");
                    break;

                default:
                    AdapterLog.Warn($"ignoring unsupported RuntimeMessage payload: {message.PayloadCase}");
                    break;
            }
        }

        private void HandleEnvironmentReady(EnvironmentReady ready)
        {
            string[] accepted = new string[ready.AcceptedExtensions.Count];
            ready.AcceptedExtensions.CopyTo(accepted, 0);

            if (!this.session.AcceptEnvironmentReady(accepted, out string error))
            {
                throw new InvalidOperationException("EnvironmentReady rejected: " + error);
            }

            AdapterLog.Info("EnvironmentReady received; accepted extensions=" + FormatExtensions(accepted));
        }

        private static string FormatExtensions(string[] extensions)
        {
            return extensions.Length == 0 ? "(none)" : string.Join(",", extensions);
        }

        private async Task HandleCapabilityRequestAsync(
            AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> call,
            RuntimeMessage message,
            CancellationToken token)
        {
            if (!this.session.AcceptCapabilityRequest(out string error))
            {
                throw new InvalidOperationException("CapabilityRequest rejected: " + error);
            }

            CapabilityList capabilities = ProtocolMapper.BuildCapabilityList();

            await this.SendAsync(
                    call,
                    ProtocolMapper.BuildCapabilityListMessage(message.MessageId, capabilities),
                    token)
                .ConfigureAwait(false);

            this.session.MarkCapabilitiesSent();

            AdapterLog.Info(
                "CapabilityList sent: " +
                string.Join(",", System.Linq.Enumerable.Select(capabilities.Capabilities, item => item.Name)) +
                " revision=" + capabilities.Revision);

            this.ReportPumpOnMainThread();
        }

        private async Task HandleObserveAsync(
            AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> call,
            RuntimeMessage message,
            CancellationToken token)
        {
            ObserveRequest request = message.Observe;
            try
            {
                Observation observation = await this.Observations
                    .ObserveAsync(request.EntityId, request.WorldId, token)
                    .ConfigureAwait(false);

                await this.SendAsync(
                        call,
                        ProtocolMapper.BuildObservationMessage(message.MessageId, observation),
                        token)
                    .ConfigureAwait(false);

                AdapterLog.Info(
                    "Observation sent entity=" + observation.EntityId +
                    " revision=" + observation.Revision +
                    " tick=" + observation.GameTime.Tick +
                    " world=" + observation.WorldId);
            }
            catch (Exception ex) when (!(ex is OperationCanceledException))
            {
                // A correlated error is the truthful answer. Staying silent would leave the Runtime
                // waiting for an observation that is never coming.
                AdapterLog.Warn("observation failed for " + request.EntityId + ": " + ex.Message);
                await this.SendAsync(
                        call,
                        ProtocolMapper.BuildErrorMessage(message.MessageId, "observation_failed", ex.Message),
                        token)
                    .ConfigureAwait(false);
            }
        }

        private async Task HandleActionAsync(
            AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> call,
            RuntimeMessage message,
            CancellationToken token)
        {
            ActionRequest request = message.Action;
            ActionResult result;

            try
            {
                // Every game-touching step happens on the main thread; the capability dispatch and
                // all argument validation happen there too, so a rejection is produced by the same
                // thread that would have executed the action.
                result = await this.pump
                    .InvokeAsync(() => this.Dialogue.Execute(request), token)
                    .ConfigureAwait(false);
            }
            catch (Exception ex) when (!(ex is OperationCanceledException))
            {
                result = ProtocolMapper.BuildFailed(request, "action_failed", ex.Message);
            }

            await this.SendAsync(call, ProtocolMapper.BuildActionResultMessage(result), token).ConfigureAwait(false);

            AdapterLog.Info(
                "ActionResult sent action=" + result.ActionId +
                " capability=" + request.Capability +
                " status=" + result.Status);
        }

        private void ReportPumpOnMainThread()
        {
            this.pump.Enqueue(() => AdapterLog.Info(
                $"main-thread pump drained on thread {this.pump.PumpThreadId}, " +
                $"startup thread was {AdapterStartup.StartupThreadId}, " +
                $"same={this.pump.PumpThreadId == AdapterStartup.StartupThreadId}"));
        }

        private async Task SendAsync(
            AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> call,
            AdapterMessage message,
            CancellationToken token)
        {
            await this.sendGate.WaitAsync(token).ConfigureAwait(false);
            try
            {
                await call.RequestStream.WriteAsync(message).ConfigureAwait(false);
            }
            finally
            {
                this.sendGate.Release();
            }
        }
    }
}
