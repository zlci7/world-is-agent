using System;
using System.Threading;
using System.Threading.Tasks;
using GameAgent.Protocol.V1Alpha2;
using Grpc.Core;
using Wia.RimWorld.Threading;

namespace Wia.RimWorld.Runtime
{
    /// <summary>
    /// Owns the gRPC stream to the Runtime for the lifetime of the process, and reconnects while the
    /// Runtime is unavailable.
    ///
    /// The receive loop runs on a thread-pool thread and only touches protocol objects. Work that
    /// would read or change game state is queued onto the main-thread pump instead. The handshake
    /// stage has no such work yet; the boundary is established here so later stages cannot bypass it.
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

        public RuntimeConnection(MainThreadPump pump)
        {
            this.pump = pump ?? throw new ArgumentNullException(nameof(pump));
        }

        public RuntimeSessionState Session => this.session;

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
                MessageId = NewMessageId("hello"),
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

            // Logs the list that arrived rather than restating what was expected. It is always empty
            // at this point because a non-empty list is rejected above, but the line keeps reporting
            // data instead of a claim if that ever stops being true.
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

            // No capabilities yet: present_dialogue arrives with the dialogue stage. Declaring the
            // handshake step now keeps the skeleton complete without claiming a capability the
            // adapter cannot execute.
            CapabilityList capabilities = new CapabilityList { Revision = 1 };

            await this.SendAsync(
                    call,
                    new AdapterMessage
                    {
                        MessageId = NewMessageId("capabilities_msg"),
                        CorrelationId = message.MessageId,
                        Capabilities = capabilities,
                    },
                    token)
                .ConfigureAwait(false);

            this.session.MarkCapabilitiesSent();
            AdapterLog.Info($"CapabilityList sent: (none) revision={capabilities.Revision}");

            this.ReportPumpOnMainThread();
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

        private static string NewMessageId(string prefix)
        {
            return prefix + "-" + Guid.NewGuid().ToString("N");
        }
    }
}
