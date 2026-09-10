using GameAgent.Stardew.Diagnostics;
using GameAgent.Stardew.Runtime;
using Xunit;

namespace Phase9Feasibility.Tests;

public sealed class SaveProbeTests
{
    [Theory]
    [InlineData("success", "prepared", null)]
    [InlineData("disconnect", "unconfirmed", "disconnected")]
    [InlineData("prepare_failure", "unconfirmed", "prepare_failed")]
    public void RealTransportCompletesProductionOrchestrationOnWorker(string mode, string status, string? reason)
    {
        using var transport = new SaveProbeTransport();
        var clock = System.Diagnostics.Stopwatch.StartNew();
        var probe = new SaveProbe(config, host, transport.Start, (task, timeout) => task.Wait(Math.Min(timeout, 1000)),
            () => clock.ElapsedMilliseconds, _ => { });
        probe.Arm(new[] { mode, "0" }); probe.Saving(); probe.Saved();
        var marker = Assert.Single(host.Writes);
        Assert.Equal(status, marker["status"]);
        Assert.Equal(reason, marker.GetValueOrDefault("reason"));
        Assert.NotEqual("0", marker["response_thread_id"]);
        Assert.NotEqual(marker["saving_thread_id"], marker["response_thread_id"]);
        Assert.True(SaveProbe.ValidateMarker(marker, out _));
    }
    private readonly FakeHost host = new();
    private readonly AdapterConfig config = new() { EnablePhase9SaveProbe = true };
    private readonly List<object> records = new();
    private long now;
    private Action<SaveProbeResponse>? callback;
    private SaveProbeRequest? request;
    private Func<Task<SaveProbeResponse>, int, bool>? onWait;
    private Action? onStart;
    private SaveProbe Create() => new(config, host, (input, complete) =>
    { request = input; callback = complete; onStart?.Invoke(); },
        (task, timeout) => { Assert.InRange(timeout, 1, 30000); return onWait?.Invoke(task, timeout) ?? false; },
        () => now, records.Add);
    private void Respond(string? error = null, string? id = null)
    {
        var thread = new Thread(() => callback!(new(id ?? request!.RequestId, error)));
        thread.Start(); Assert.True(thread.Join(1000));
    }

    [Fact]
    public void DirectTcsCompletionDoesNotRunContinuationOnResponseCallbackStack()
    {
        var probe = Create();
        using var inCallback = new ThreadLocal<bool>(() => false);
        onWait = (task, _) =>
        {
            var continuation = task.ContinueWith(_ => inCallback.Value, CancellationToken.None,
                TaskContinuationOptions.ExecuteSynchronously, TaskScheduler.Default);
            var thread = new Thread(() => { inCallback.Value = true; callback!(new(request!.RequestId)); inCallback.Value = false; });
            thread.Start(); Assert.True(thread.Join(1000));
            Assert.True(continuation.Wait(1000)); Assert.False(continuation.Result);
            return true;
        };
        probe.Arm(new[] { "success" }); probe.Saving();
        Assert.Equal("prepared", Assert.Single(host.Writes)["status"]);
    }

    [Fact]
    public void ResponseAtDeadlineReleasesOnlyTimeoutAndRecordsItsActualThread()
    {
        var probe = Create();
        onWait = (task, timeout) => { now = timeout; Respond(); return task.IsCompleted; };
        probe.Arm(new[] { "delayed", "5000" }); probe.Saving();
        var marker = Assert.Single(host.Writes);
        Assert.Equal("timeout", marker["reason"]);
        Assert.Equal("true", marker["arrived_late"]);
        Assert.NotEqual("0", marker["response_thread_id"]);
        Assert.True(SaveProbe.ValidateMarker(marker, out _));
    }

    [Fact]
    public void AcceptedResponseSurvivesDuplicateAtDeadlineAndCancel()
    {
        var probe = Create();
        onWait = (_, timeout) => { Respond(); now = timeout; Respond(); probe.Cancel(); return true; };
        probe.Arm(new[] { "success" }); probe.Saving();
        var marker = Assert.Single(host.Writes);
        Assert.Equal("prepared", marker["status"]); Assert.Equal("false", marker["arrived_late"]);
        Assert.True(SaveProbe.ValidateMarker(marker, out _));
    }

    [Fact]
    public void WorldResetDisarmsAndDisposalTerminatesInflightPreparation()
    {
        var probe = Create(); probe.Arm(new[] { "success" }); probe.WorldChanged(); probe.Saving();
        Assert.Empty(host.Writes);
        probe.Arm(new[] { "success" });
        onWait = (task, _) => { probe.Dispose(); Assert.True(task.IsCompleted); return true; };
        probe.Saving(); Assert.Equal("cancelled", Assert.Single(host.Writes)["reason"]);
        Assert.Equal("disabled", probe.Arm(new[] { "success" }));
    }

    [Fact]
    public void DiagnosticEmitterFailureCannotAbortSavingOrSaved()
    {
        var probe = new SaveProbe(config, host, (_, _) => throw new InvalidOperationException(),
            (_, _) => false, () => 0, _ => throw new InvalidOperationException());
        probe.Arm(new[] { "success" }); probe.Saving(); probe.Saved();
        Assert.Equal("prepare_failed", Assert.Single(host.Writes)["reason"]);
        Assert.Equal("saved", probe.Status.Phase);
    }

    [Theory]
    [InlineData("delayed")]
    [InlineData("cancelled")]
    public void UnconfirmedTimeoutAndCancellationMarkersRoundTrip(string scenario)
    {
        var probe = Create();
        onWait = (task, timeout) =>
        { if (scenario == "cancelled") probe.Cancel(); else now += timeout; return task.IsCompleted; };
        probe.Arm(new[] { scenario == "delayed" ? "delayed" : "success" }); probe.Saving();
        var marker = Assert.Single(host.Writes);
        Assert.True(SaveProbe.ValidateMarker(marker, out _));
        Create().SaveLoaded();
        Assert.Contains(records.OfType<SaveProbeEvidence>(), x => x.Event == "persisted_marker_observed" && x.Marker!["status"] == "unconfirmed");
    }

    [Fact]
    public void SuccessWritesDiagnosticMarkerInsideSavingAfterDirectResponse()
    {
        var host = new FakeHost();
        var probe = new SaveProbe(new AdapterConfig { EnablePhase9SaveProbe = true }, host,
            (request, complete) =>
            {
                var thread = new Thread(() => complete(new(request.RequestId)));
                thread.Start(); Assert.True(thread.Join(1000));
            },
            (task, timeout) => task.IsCompleted, () => 0, _ => { });
        Assert.Equal("armed", probe.Arm(new[] { "success", "0" }));
        Assert.Empty(host.Writes);
        probe.Saving();
        var marker = Assert.Single(host.Writes);
        Assert.Equal("prepared", marker["status"]);
        Assert.Equal("save-123", marker["world"]);
    }

    [Fact]
    public void SuccessfulRunIsCorrelatedFiniteAndIdempotentThroughSaved()
    {
        var probe = Create();
        onWait = (task, timeout) =>
        {
            Assert.Equal(5000, timeout);
            now = 12; Respond();
            Assert.True(task.IsCompletedSuccessfully);
            return true;
        };
        probe.Arm(new[] { "success", "12" });
        probe.Saving(); probe.Saving();
        var marker = Assert.Single(host.Writes);
        Assert.Equal("1", marker["schema_version"]);
        Assert.Equal(request!.RequestId, marker["request_id"]);
        Assert.Equal("prepared", marker["status"]);
        Assert.Equal("true", marker["completion_direct"]);
        Assert.NotEqual(marker["saving_thread_id"], marker["response_thread_id"]);
        Assert.Equal("12", marker["wait_ms"]);
        Assert.True(SaveProbe.ValidateMarker(marker, out _));
        probe.Saved(); probe.Saved(); probe.Cancel();
        Assert.Equal("saved", probe.Status.Phase);
        Assert.Single(records.OfType<SaveProbeEvidence>(), x => x.Event == "saved");
        Assert.Single(host.Writes);
    }

    [Fact]
    public void TimeoutAndLateResponseCannotRewriteTheChosenMarkerOrNextRun()
    {
        var probe = Create();
        onWait = (_, timeout) => { now += timeout; return false; };
        probe.Arm(new[] { "delayed", "6000" }); probe.Saving();
        var chosen = new Dictionary<string, string>(Assert.Single(host.Writes));
        Assert.Equal("unconfirmed", chosen["status"]); Assert.Equal("timeout", chosen["reason"]);
        probe.Saved(); var terminal = probe.Status;
        Respond();
        Assert.Equal(terminal.Phase, probe.Status.Phase);
        Assert.Equal(terminal.Marker, probe.Status.Marker);
        Assert.Equal(chosen, host.Writes[0]);
        Assert.Contains(records.OfType<SaveProbeEvidence>(), x => x.Event == "response" && x.Late);
        var oldCallback = callback; var oldRequest = request;
        probe.Arm(new[] { "success" });
        onWait = (task, _) =>
        {
            oldCallback!(new(oldRequest!.RequestId));
            Assert.False(task.IsCompleted); Respond(); return true;
        };
        probe.Saving();
        Assert.Equal("prepared", host.Writes[1]["status"]);
    }

    [Theory]
    [InlineData("disconnect", "disconnected")]
    [InlineData("prepare_failure", "prepare_failed")]
    public void BackgroundFailuresAllowSavingAndSavedWithDistinctReasons(string mode, string error)
    {
        var probe = Create(); onWait = (_, _) => { Respond(error); return true; };
        probe.Arm(new[] { mode }); probe.Saving(); probe.Saved();
        var marker = Assert.Single(host.Writes);
        Assert.Equal("unconfirmed", marker["status"]); Assert.Equal(error, marker["reason"]);
        Assert.Equal("saved", probe.Status.Phase);
        Assert.True(SaveProbe.ValidateMarker(marker, out _));
    }

    [Fact]
    public void CancelBeforeSavingDisarmsAndInflightCancelCompletesOnce()
    {
        var probe = Create(); probe.Arm(new[] { "success" }); probe.Cancel(); probe.Cancel(); probe.Saving();
        Assert.Empty(host.Writes); Assert.Equal("cancelled", probe.Status.Phase);
        probe.Arm(new[] { "success" });
        onWait = (task, _) => { probe.Cancel(); probe.Cancel(); Assert.True(task.IsCompleted); return true; };
        probe.Saving(); Respond(); probe.Saved(); probe.Cancel();
        Assert.Equal("cancelled", Assert.Single(host.Writes)["reason"]);
        Assert.Equal("saved", probe.Status.Phase);
    }

    [Fact]
    public void MismatchedResponseDoesNotReleasePendingWait()
    {
        var probe = Create(); probe.Arm(new[] { "success" });
        onWait = (task, _) => { Respond(id: "wrong-request"); Assert.False(task.IsCompleted); Respond(); return true; };
        probe.Saving(); Assert.Equal("prepared", Assert.Single(host.Writes)["status"]);
        Assert.Contains(records.OfType<SaveProbeEvidence>(), x => x.Code == "mismatched_request");
    }

    [Fact]
    public void StatusIsReadOnlyAndCallerCannotMutateSelectedMarker()
    {
        var probe = Create(); onWait = (_, _) => { Respond(); return true; };
        probe.Arm(new[] { "success" }); probe.Saving();
        int reads = host.Reads, captures = host.Captures, logs = records.Count;
        var first = probe.Status;
        first.Marker!["status"] = "changed";
        Assert.Equal("prepared", probe.Status.Marker!["status"]);
        Assert.Equal(reads, host.Reads); Assert.Equal(captures, host.Captures); Assert.Equal(logs, records.Count);
    }

    [Theory]
    [InlineData(0, "success")]
    [InlineData(-1, "success")]
    [InlineData(31, "success")]
    [InlineData(5, "")]
    [InlineData(5, "unknown")]
    [InlineData(5, "success -1")]
    [InlineData(5, "success 5000")]
    [InlineData(5, "delayed 4999")]
    [InlineData(5, "success 0 extra")]
    [InlineData(5, "success NaN")]
    [InlineData(5, "disconnect -1")]
    public void InvalidArgumentsOrTimeoutRejectBeforeMutation(int seconds, string arguments)
    {
        config.Phase9SaveProbeTimeoutSeconds = seconds;
        var probe = Create();
        Assert.NotEqual("armed", probe.Arm(arguments.Length == 0 ? Array.Empty<string>() : arguments.Split(' ')));
        probe.Saving(); Assert.Equal("idle", probe.Status.Phase); Assert.Empty(host.Writes); Assert.Equal(0, host.Captures);
    }

    [Fact]
    public void WorldAuthorityAndActiveRunAreRequiredBeforeArming()
    {
        var probe = Create(); host.WorldReady = false;
        Assert.Equal("world_not_ready", probe.Arm(new[] { "success" }));
        host.WorldReady = true; host.HasAuthority = false;
        Assert.Equal("authority_required", probe.Arm(new[] { "success" }));
        Assert.Equal(0, host.Captures);
        host.HasAuthority = true; probe.Arm(new[] { "success" });
        Assert.Equal("probe_active", probe.Arm(new[] { "success" }));
        onWait = (_, _) => { Assert.Equal("probe_active", probe.Arm(new[] { "success" })); Respond(); return true; };
        probe.Saving(); Assert.Equal("probe_active", probe.Arm(new[] { "success" }));
    }

    [Fact]
    public void DefaultsRegisterNothingAndNeverReadOrWriteSaveData()
    {
        config.EnablePhase9SaveProbe = false;
        var probe = Create(); int creates = 0, subscriptions = 0;
        var commands = new Dictionary<string, Action<string[]>>();
        SaveProbe? installed = SaveProbe.Install(config, () => { creates++; return probe; },
            (name, _, command) => commands.Add(name, command), _ => subscriptions++);
        Assert.Null(installed); Assert.Equal(0, creates); Assert.Equal(0, subscriptions); Assert.Empty(commands);
        Assert.Equal("disabled", probe.Arm(new[] { "success" }));
        probe.Saving(); probe.Saved(); probe.SaveLoaded(); Assert.Empty(host.Writes); Assert.Equal(0, host.Reads);
        config.EnablePhase9SaveProbe = true;
        Assert.Same(probe, SaveProbe.Install(config, () => probe, (name, _, command) => commands.Add(name, command), _ => subscriptions++));
        Assert.Equal(1, subscriptions);
        Assert.Equal(new[] { "gameagent_phase9_save_arm", "gameagent_phase9_save_status", "gameagent_phase9_save_cancel" }, commands.Keys);
        commands["gameagent_phase9_save_arm"](new[] { "success" });
        commands["gameagent_phase9_save_cancel"](new[] { "extra" });
        Assert.Equal("armed", probe.Status.Phase);
        commands["gameagent_phase9_save_cancel"](Array.Empty<string>());
        Assert.Equal("cancelled", probe.Status.Phase);
    }

    [Theory]
    [InlineData("capture")]
    [InlineData("start")]
    [InlineData("wait")]
    public void PreparationBoundaryExceptionsBecomeUnconfirmedAndReturn(string boundary)
    {
        var probe = Create(); probe.Arm(new[] { "success" });
        if (boundary == "capture") host.ThrowCapture = true;
        if (boundary == "start") onStart = () => throw new InvalidOperationException();
        if (boundary == "wait") onWait = (_, _) => throw new InvalidOperationException();
        probe.Saving(); probe.Saved();
        Assert.Equal("prepare_failed", Assert.Single(host.Writes)["reason"]);
        Assert.Equal(boundary == "capture" ? "armed" : "saving", host.Writes[0].GetValueOrDefault("identity_source"));
        Assert.True(SaveProbe.ValidateMarker(host.Writes[0], out _));
        Assert.Equal("saved", probe.Status.Phase);
    }

    [Fact]
    public void SavingCapturesCurrentDateAndHonorsAlreadyCompletedPreparation()
    {
        var probe = Create(); probe.Arm(new[] { "success" });
        host.Current = new("save-123", "1-spring-3", 600);
        onStart = () => { Respond(); now = 5000; };
        probe.Saving();
        var marker = Assert.Single(host.Writes);
        Assert.Equal("1-spring-3", marker["date"]); Assert.Equal("600", marker["game_time"]);
        Assert.Equal("prepared", marker["status"]); Assert.Equal("saving", marker.GetValueOrDefault("identity_source"));
    }

    [Theory]
    [InlineData("request_id")]
    [InlineData("world")]
    [InlineData("date")]
    [InlineData("completion_direct")]
    public void MissingRequiredPersistedFieldsAreRejected(string field)
    {
        var probe = Create(); onWait = (_, _) => { Respond(); return true; };
        probe.Arm(new[] { "success" }); probe.Saving();
        var marker = Assert.Single(host.Writes); marker.Remove(field);
        Assert.False(SaveProbe.ValidateMarker(marker, out _));
    }

    [Theory]
    [InlineData("timeout")]
    [InlineData("cancelled")]
    public void StrictReadRejectsDirectCompletionThatContradictsTerminalReason(string reason)
    {
        var probe = Create();
        onWait = (task, timeout) =>
        { if (reason == "cancelled") probe.Cancel(); else now = timeout; return task.IsCompleted; };
        probe.Arm(new[] { "success" }); probe.Saving();
        var marker = Assert.Single(host.Writes); Assert.True(SaveProbe.ValidateMarker(marker, out _));
        marker["completion_direct"] = "true";
        marker["response_thread_id"] = (int.Parse(marker["saving_thread_id"]) + 1).ToString();
        Assert.False(SaveProbe.ValidateMarker(marker, out _));
    }

    [Fact]
    public void ResponseAcceptedBeforeDeadlineWinsWaitBoundaryRace()
    {
        var probe = Create();
        onWait = (_, timeout) => { Respond(); now = timeout; return false; };
        probe.Arm(new[] { "success" }); probe.Saving();
        Assert.Equal("prepared", Assert.Single(host.Writes)["status"]);
    }

    [Fact]
    public void WriteFailureIsContainedAndCannotClaimMarkerWasPersisted()
    {
        var probe = Create(); probe.Arm(new[] { "success" });
        onWait = (_, _) => { Respond(); return true; }; host.ThrowWrite = true;
        probe.Saving(); probe.Saved();
        Assert.False(probe.Status.MarkerWritten); Assert.Equal("marker_write_failed", probe.Status.Code);
        Assert.Empty(host.Writes);
    }

    [Fact]
    public void LoadingObservesValidAbsentAndInvalidMarkersWithoutWriting()
    {
        var probe = Create(); probe.SaveLoaded();
        Assert.Contains(records.OfType<SaveProbeEvidence>(), x => x.Event == "persisted_marker_absent");
        onWait = (_, _) => { Respond(); return true; }; probe.Arm(new[] { "success" }); probe.Saving(); probe.Saved();
        var reloaded = Create(); reloaded.SaveLoaded();
        Assert.Contains(records.OfType<SaveProbeEvidence>(), x => x.Event == "persisted_marker_observed" && x.Marker!["status"] == "prepared");
        host.Writes[0]["schema_version"] = "2"; reloaded.SaveLoaded();
        Assert.Contains(records.OfType<SaveProbeEvidence>(), x => x.Event == "persisted_marker_invalid");
        Assert.Single(host.Writes);
        host.ThrowRead = true; reloaded.SaveLoaded();
        Assert.Contains(records.OfType<SaveProbeEvidence>(), x => x.Code == "marker_read_failed");
    }

    [Theory]
    [InlineData("schema_version", "2")]
    [InlineData("request_id", "")]
    [InlineData("world", "")]
    [InlineData("checkpoint_id", "fake")]
    [InlineData("checksum", "fake")]
    [InlineData("reason", "timeout")]
    [InlineData("status", "unconfirmed")]
    [InlineData("completion_direct", "false")]
    [InlineData("response_thread_id", "0")]
    [InlineData("arrived_late", "true")]
    [InlineData("timeout_ms", "0")]
    [InlineData("date", "invalid")]
    public void StrictReadRejectsInvalidAndUnknownFields(string field, string value)
    {
        var probe = Create(); onWait = (_, _) => { Respond(); return true; };
        probe.Arm(new[] { "success" }); probe.Saving();
        var marker = Assert.Single(host.Writes);
        Assert.True(SaveProbe.ValidateMarker(marker, out _));
        marker[field] = value;
        Assert.False(SaveProbe.ValidateMarker(marker, out _));
    }

    private sealed class FakeHost : ISaveProbeHost
    {
        public bool WorldReady { get; set; } = true;
        public bool HasAuthority { get; set; } = true;
        public List<Dictionary<string, string>> Writes { get; } = new();
        public int Reads, Captures;
        public bool ThrowCapture, ThrowWrite, ThrowRead;
        public SaveProbeIdentity Current = new("save-123", "1-spring-2", 2600);
        public SaveProbeIdentity Capture()
        { Captures++; if (ThrowCapture) throw new InvalidOperationException(); return Current; }
        public void Write(Dictionary<string, string> marker)
        { if (ThrowWrite) throw new InvalidOperationException(); Writes.Add(new(marker)); }
        public Dictionary<string, string>? Read()
        { Reads++; if (ThrowRead) throw new InvalidOperationException(); return Writes.LastOrDefault(); }
    }
}
