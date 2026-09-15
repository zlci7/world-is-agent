using System;
using System.Diagnostics;
using System.Threading;
using System.Threading.Tasks;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskCheckpointBridgeTests
{
    private static readonly CheckpointScope Scope = new("stardew-valley", "Farm_1", "run-a", 2);

    private static CheckpointPrepareRequest Request(string saveRequestId = "save_1") =>
        new(Scope, 1800, 4, saveRequestId);

    private static CheckpointMarker Reference() => CheckpointMarker.Confirmed("stardew-valley", "Farm_1", "checkpoint_1", "sha256-abc");

    private sealed class FakeTransport : ICheckpointTransport
    {
        public Func<CheckpointPrepareRequest, Task<CheckpointPreparedReply>> Handler { get; set; } =
            request => throw new InvalidOperationException("handler not configured");

        public int Calls { get; private set; }

        public Task<CheckpointPreparedReply> PrepareAsync(CheckpointPrepareRequest request, CancellationToken token)
        {
            this.Calls++;
            return this.Handler(request);
        }
    }

    [Fact]
    public void AcceptedPrepareProducesAConfirmedMarkerAndOneFinish()
    {
        CheckpointScope preparedScope = Scope with { ExecutionGeneration = 3 };
        FakeTransport transport = new()
        {
            Handler = request => Task.Run(() => new CheckpointPreparedReply(preparedScope, request.SaveRequestId, Reference(), string.Empty)),
        };
        TaskCheckpointBridge bridge = new(transport);

        CheckpointMarker marker = bridge.PrepareForSaving(Request(), TimeSpan.FromSeconds(1));
        CheckpointFinishRequest? saved = bridge.OnSaved();
        CheckpointFinishRequest? again = bridge.OnSaved();

        Assert.Equal(CheckpointMarkerState.Confirmed, CheckpointMarker.Read(marker.ToSaveData()).State);
        Assert.Equal("checkpoint_1", marker.CheckpointId);
        Assert.Equal(1, transport.Calls);
        Assert.NotNull(saved);
        Assert.Equal(preparedScope, saved!.Scope);
        Assert.Equal("save_1", saved.SaveRequestId);
        Assert.True(saved.Saved);
        Assert.Null(again);
    }

    [Fact]
    public void RefusedPrepareWritesAnUnconfirmedMarkerWithTheRuntimeCode()
    {
        FakeTransport transport = new()
        {
            Handler = request => Task.Run(() => new CheckpointPreparedReply(request.Scope, request.SaveRequestId, null, "save_in_progress")),
        };
        TaskCheckpointBridge bridge = new(transport);

        CheckpointMarker marker = bridge.PrepareForSaving(Request(), TimeSpan.FromSeconds(1));

        CheckpointMarkerRead read = CheckpointMarker.Read(marker.ToSaveData());
        Assert.Equal(CheckpointMarkerState.Unconfirmed, read.State);
        Assert.Equal("save_in_progress", read.Marker?.Reason);
        Assert.Null(bridge.OnSaved());
        Assert.Null(bridge.OnSaveAborted("test"));
    }

    [Fact]
    public void SilentRuntimeTimesOutWithoutBlockingTheSaveThread()
    {
        TaskCompletionSource<CheckpointPreparedReply> late = new(TaskCreationOptions.RunContinuationsAsynchronously);
        FakeTransport transport = new() { Handler = _ => late.Task };
        TaskCheckpointBridge bridge = new(transport);
        Stopwatch watch = Stopwatch.StartNew();

        CheckpointMarker marker = bridge.PrepareForSaving(Request(), TimeSpan.FromMilliseconds(150));
        watch.Stop();

        Assert.Equal("timeout", CheckpointMarker.Read(marker.ToSaveData()).Marker?.Reason);
        Assert.True(watch.Elapsed < TimeSpan.FromSeconds(5), $"bounded wait took {watch.Elapsed}");
        Assert.Null(bridge.OnSaved());

        late.SetResult(new CheckpointPreparedReply(Scope with { ExecutionGeneration = 3 }, "save_1", Reference(), string.Empty));
        Assert.Equal("timeout", CheckpointMarker.Read(marker.ToSaveData()).Marker?.Reason);
    }

    [Fact]
    public async Task ReplyFromTheNetworkThreadReleasesTheWaitingSave()
    {
        TaskCompletionSource<CheckpointPreparedReply> answer = new(TaskCreationOptions.RunContinuationsAsynchronously);
        FakeTransport transport = new() { Handler = _ => answer.Task };
        TaskCheckpointBridge bridge = new(transport);

        Task<CheckpointMarker> saving = Task.Run(() => bridge.PrepareForSaving(Request(), TimeSpan.FromSeconds(5)));
        Thread.Sleep(50);
        answer.SetResult(new CheckpointPreparedReply(Scope with { ExecutionGeneration = 3 }, "save_1", Reference(), string.Empty));

        CheckpointMarker marker = await saving;

        Assert.Equal(CheckpointMarkerState.Confirmed, CheckpointMarker.Read(marker.ToSaveData()).State);
    }

    [Fact]
    public void TransportFailureAndMissingReferenceStayUnconfirmed()
    {
        TaskCheckpointBridge throwing = new(new FakeTransport
        {
            Handler = _ => Task.FromException<CheckpointPreparedReply>(new InvalidOperationException("stream down")),
        });
        TaskCheckpointBridge empty = new(new FakeTransport
        {
            Handler = request => Task.FromResult(new CheckpointPreparedReply(request.Scope, request.SaveRequestId, null, string.Empty)),
        });

        Assert.Equal("prepare_failed", CheckpointMarker.Read(throwing.PrepareForSaving(Request(), TimeSpan.FromSeconds(1)).ToSaveData()).Marker?.Reason);
        Assert.Equal("checkpoint_missing", CheckpointMarker.Read(empty.PrepareForSaving(Request(), TimeSpan.FromSeconds(1)).ToSaveData()).Marker?.Reason);
    }

    [Fact]
    public void AbortedSaveSendsAnUnconfirmedFinishAndDisconnectSendsNone()
    {
        FakeTransport transport = new()
        {
            Handler = request => Task.FromResult(new CheckpointPreparedReply(Scope with { ExecutionGeneration = 3 }, request.SaveRequestId, Reference(), string.Empty)),
        };
        TaskCheckpointBridge bridge = new(transport);

        bridge.PrepareForSaving(Request("save_aborted"), TimeSpan.FromSeconds(1));
        CheckpointFinishRequest? aborted = bridge.OnSaveAborted("saving_failed");
        Assert.NotNull(aborted);
        Assert.False(aborted!.Saved);
        Assert.Equal("save_aborted", aborted.SaveRequestId);

        bridge.PrepareForSaving(Request("save_disconnected"), TimeSpan.FromSeconds(1));
        bridge.OnDisconnected("stream_lost");
        Assert.Null(bridge.OnSaved());
    }

    [Fact]
    public void UnfinishedHandoffDoesNotLeakIntoTheNextSave()
    {
        FakeTransport transport = new()
        {
            Handler = request => Task.FromResult(new CheckpointPreparedReply(Scope with { ExecutionGeneration = 3 }, request.SaveRequestId, Reference(), string.Empty)),
        };
        TaskCheckpointBridge bridge = new(transport);

        bridge.PrepareForSaving(Request("save_first"), TimeSpan.FromSeconds(1));
        bridge.PrepareForSaving(Request("save_second"), TimeSpan.FromSeconds(1));
        CheckpointFinishRequest? finish = bridge.OnSaved();

        Assert.NotNull(finish);
        Assert.Equal("save_second", finish!.SaveRequestId);
    }
}

public sealed class CheckpointReplyInboxTests
{
    private static readonly CheckpointScope Scope = new("stardew-valley", "Farm_1", "run-a", 2);

    [Fact]
    public async Task MatchingReplyResolvesTheWaitingSave()
    {
        CheckpointReplyInbox inbox = new();
        Task<CheckpointPreparedReply> waiting = inbox.Register("msg_1", "save_1", Scope);

        bool resolved = inbox.TryResolve(
            "msg_1",
            "save_1",
            Scope with { ExecutionGeneration = 3 },
            CheckpointMarker.Confirmed("stardew-valley", "Farm_1", "checkpoint_1", "sha256-abc"),
            string.Empty
        );

        Assert.True(resolved);
        Assert.Equal(3UL, (await waiting).Scope.ExecutionGeneration);
        Assert.Equal(0, inbox.Count);
    }

    [Theory]
    [InlineData("msg_other", "save_1", 3UL)]
    [InlineData("msg_1", "save_other", 3UL)]
    [InlineData("msg_1", "save_1", 1UL)]
    public void ForeignReplyLeavesTheWaitingSaveUntouched(string correlationId, string saveRequestId, ulong generation)
    {
        CheckpointReplyInbox inbox = new();
        Task<CheckpointPreparedReply> waiting = inbox.Register("msg_1", "save_1", Scope);

        bool resolved = inbox.TryResolve(
            correlationId,
            saveRequestId,
            Scope with { ExecutionGeneration = generation },
            CheckpointMarker.Confirmed("stardew-valley", "Farm_1", "checkpoint_1", "sha256-abc"),
            string.Empty
        );

        Assert.False(resolved);
        Assert.False(waiting.IsCompleted);
        Assert.Equal(1, inbox.Count);
    }

    [Fact]
    public async Task ErrorReplyStillNeedsTheRightIdentity()
    {
        CheckpointReplyInbox inbox = new();
        Task<CheckpointPreparedReply> waiting = inbox.Register("msg_1", "save_1", Scope);

        Assert.True(inbox.TryResolve("msg_1", "save_1", Scope, null, "save_in_progress"));
        Assert.Equal("save_in_progress", (await waiting).ErrorCode);
    }

    [Fact]
    public async Task DroppedStreamResolvesEveryWaitingSave()
    {
        CheckpointReplyInbox inbox = new();
        Task<CheckpointPreparedReply> waiting = inbox.Register("msg_1", "save_1", Scope);

        inbox.FailAll("disconnected");

        Assert.Equal("disconnected", (await waiting).ErrorCode);
        Assert.Equal(0, inbox.Count);
    }
}
