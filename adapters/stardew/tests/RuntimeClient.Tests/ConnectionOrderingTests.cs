using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
using Grpc.Core;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ConnectionOrderingTests
{
    [Fact]
    public async Task EmptyTargetsSendTheCapturedWorldNpcDirectory()
    {
        var fixture = new ClientFixture();
        var transport = Attach(fixture);
        var state = fixture.Field<RuntimeSessionState>("sessionState");
        state.BeginConnection();
        state.AcceptEnvironmentReady(new[] { RuntimeSessionState.TaskExtension }, out _);
        state.AcceptCapabilityRequest(out _);
        state.MarkCapabilitiesSent();
        fixture.Field<RuntimeWorldContext>("worldContext").BeginWorld("world", 600);
        fixture.SetField("availableVillagerNames", new[] { "Linus", "Abigail" });

        await ((Task)fixture.Call("TrySendWorldBindingAsync", CancellationToken.None)!).WaitAsync(TimeSpan.FromSeconds(2));

        var message = Assert.Single(transport.Writer.Messages);
        Assert.Equal(new[] { "player:local", "npc:Abigail", "npc:Linus" }, message.WorldBinding.Entities.Select(entity => entity.EntityId));
        Assert.False(fixture.Client.IsTaskReady);
    }

    [Fact]
    public async Task BindingReadyUsesLatestClockOnlyWhenMainThreadProcessesIt()
    {
        var fixture = new ClientFixture();
        var transport = Attach(fixture);
        var state = fixture.Field<RuntimeSessionState>("sessionState");
        state.BeginConnection();
        state.AcceptEnvironmentReady(new[] { RuntimeSessionState.TaskExtension }, out _);
        state.AcceptCapabilityRequest(out _);
        state.MarkCapabilitiesSent();
        var world = fixture.Field<RuntimeWorldContext>("worldContext");
        world.BeginWorld("world", 600);
        var snapshot = world.Current!;
        fixture.SetField("worldBindingClockSequence", 1L);
        fixture.Field<WorldBindingExchange>("worldBindingExchange").Begin("binding");
        world.AdvanceClock(610);
        var message = new RuntimeMessage
        {
            CorrelationId = "binding",
            WorldBindingReady = new WorldBindingReady
            {
                Status = "ready",
                Scope = new TaskScope { GameId = snapshot.GameId, WorldId = snapshot.WorldId,
                    WorldRunId = snapshot.WorldRunId, ExecutionGeneration = 1 },
            },
        };

        Task received = (Task)fixture.Call("HandleRuntimeMessageAsync", message, CancellationToken.None)!;
        Assert.False(state.CanUseTasks);
        Assert.Empty(transport.Writer.Messages);
        world.AdvanceClock(620);
        fixture.Field<MainThreadDispatcher>("dispatcher").Drain();
        await received.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.True(state.CanUseTasks);
        Assert.Single(transport.Writer.Messages);
        Assert.Equal(3UL, transport.Writer.Messages[0].WorldClock.Clock.Sequence);
    }

    [Fact]
    public void ReconnectClosesAnExistingStreamBeforeStartingAnotherHandshake()
    {
        var fixture = new ClientFixture();
        var transport = Attach(fixture);
        using var cancellation = new CancellationTokenSource();
        var stopped = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.SetField("cancellation", cancellation);
        fixture.SetField("receiveTask", stopped.Task);

        try
        {
            fixture.Client.Reconnect();
            Assert.True(cancellation.IsCancellationRequested);
            Assert.True(transport.Disposed);
            Assert.Empty(transport.Writer.Messages);
            Assert.False(fixture.Client.IsReady);
        }
        finally { stopped.TrySetResult(); }
    }

    [Theory]
    [InlineData(false)]
    [InlineData(true)]
    public async Task QueuedSendCannotWriteIntoAReplacementStream(bool batch)
    {
        var fixture = new ClientFixture();
        var previous = Attach(fixture);
        var mutex = fixture.Field<SemaphoreSlim>("sendMu");
        await mutex.WaitAsync();
        var message = new AdapterMessage { MessageId = "old" };
        Task sending = batch
            ? (Task)fixture.Call("SendBatchAsync", new[] { message }, CancellationToken.None)!
            : (Task)fixture.Call("SendAsync", message, CancellationToken.None)!;
        var replacement = Attach(fixture);
        mutex.Release();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => sending);
        Assert.Empty(previous.Writer.Messages);
        Assert.Empty(replacement.Writer.Messages);
    }

    [Fact]
    public async Task CancelledBindingReplyDoesNotMutateTheNextWorld()
    {
        var fixture = new ClientFixture();
        var transport = Attach(fixture);
        using var cancellation = new CancellationTokenSource();
        Task received = (Task)fixture.Call("HandleRuntimeMessageAsync", new RuntimeMessage
        {
            CorrelationId = "old",
            WorldBindingReady = new WorldBindingReady { Status = "ready" },
        }, cancellation.Token)!;
        cancellation.Cancel();
        fixture.Field<MainThreadDispatcher>("dispatcher").Drain();
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => received);
        Assert.False(fixture.Client.IsTaskReady);
        Assert.Empty(transport.Writer.Messages);
    }

    [Fact]
    public void StopInvalidatesAPendingRestart()
    {
        var fixture = new ClientFixture();
        Attach(fixture);
        fixture.Client.Reconnect();
        fixture.Call("StopConnection");
        fixture.Field<MainThreadDispatcher>("dispatcher").Drain();
        Assert.False(fixture.Client.IsReady);
        Assert.Null(fixture.Field<object?>("stream"));
    }

    [Fact]
    public async Task SlowReceiverShutdownKeepsTheRestartPending()
    {
        var fixture = new ClientFixture();
        var stopped = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        Task restart = (Task)fixture.Call("StartAfterDisconnectAsync", stopped.Task, 0L)!;
        await Task.Delay(TimeSpan.FromMilliseconds(5100));
        Assert.False(restart.IsCompleted);
        stopped.SetResult();
        await restart.WaitAsync(TimeSpan.FromSeconds(2));
        fixture.Call("StopConnection");
        fixture.Field<MainThreadDispatcher>("dispatcher").Drain();
        Assert.Null(fixture.Field<object?>("stream"));
    }

    private static Transport Attach(ClientFixture fixture)
    {
        var transport = new Transport();
        fixture.SetField("stream", transport.Stream);
        return transport;
    }

    private sealed class Transport
    {
        public readonly RecordingWriter Writer = new();
        public readonly AsyncDuplexStreamingCall<AdapterMessage, RuntimeMessage> Stream;
        public bool Disposed;
        public Transport() => Stream = new(Writer, new EmptyReader(), Task.FromResult(new Metadata()),
            () => Status.DefaultSuccess, () => new Metadata(), () => Disposed = true);
    }

    private sealed class RecordingWriter : IClientStreamWriter<AdapterMessage>
    {
        public readonly List<AdapterMessage> Messages = new();
        public WriteOptions? WriteOptions { get; set; }
        public Task CompleteAsync() => Task.CompletedTask;
        public Task WriteAsync(AdapterMessage message) { Messages.Add(message); return Task.CompletedTask; }
    }

    private sealed class EmptyReader : IAsyncStreamReader<RuntimeMessage>
    {
        public RuntimeMessage Current => new();
        public Task<bool> MoveNext(CancellationToken token) => Task.FromResult(false);
        public void Dispose() { }
    }
}
