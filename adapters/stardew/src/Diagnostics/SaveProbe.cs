using System.Globalization;
using System.Text.RegularExpressions;
using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Diagnostics;

internal record SaveProbeIdentity(string World, string Date, int GameTime);
internal record SaveProbeRequest(string RequestId, string Mode, int ResponseDelayMs);
internal record SaveProbeResponse(string RequestId, string? Error = null);
internal record SaveProbeStatus(string Phase = "idle", Dictionary<string, string>? Marker = null,
    bool MarkerWritten = false, string Code = "idle");
internal record SaveProbeEvidence(string Event, string? RequestId = null, bool Late = false,
    int ResponseThreadId = 0, string? Code = null, SaveProbeStatus? State = null,
    Dictionary<string, string>? Marker = null);

internal interface ISaveProbeHost
{
    bool WorldReady { get; }
    bool HasAuthority { get; }
    SaveProbeIdentity Capture();
    void Write(Dictionary<string, string> marker);
    Dictionary<string, string>? Read();
}

/// <summary>Diagnostic Saving handoff only. A prepared marker proves no Runtime durability.</summary>
internal sealed class SaveProbe : IDisposable
{
    public const string DataKey = "gameagent-phase9-feasibility-save";
    private readonly object gate = new();
    private readonly AdapterConfig config;
    private readonly ISaveProbeHost host;
    private readonly Action<SaveProbeRequest, Action<SaveProbeResponse>> start;
    private readonly Func<Task<SaveProbeResponse>, int, bool> wait;
    private readonly Func<long> now;
    private readonly Action<object> emit;
    private SaveProbeStatus state = new();
    private SaveProbeIdentity? armedIdentity;
    private string mode = "success";
    private int delayMs, timeoutMs;
    private Pending? pending;
    private bool awaitingSaved, disposed;

    private sealed class Pending
    {
        public readonly TaskCompletionSource<SaveProbeResponse> Completion = new(TaskCreationOptions.RunContinuationsAsynchronously);
        public readonly SaveProbeRequest Request;
        public readonly long StartedAt;
        public readonly int SavingThread = Environment.CurrentManagedThreadId;
        public readonly string StartedUtc = DateTimeOffset.UtcNow.ToString("O", CultureInfo.InvariantCulture);
        public readonly int TimeoutMs;
        public bool Closed, Direct, Late;
        public int ResponseThread;
        public Pending(SaveProbeRequest request, long startedAt, int timeout)
        { Request = request; StartedAt = startedAt; TimeoutMs = timeout; }
    }

    public SaveProbe(AdapterConfig config, ISaveProbeHost host,
        Action<SaveProbeRequest, Action<SaveProbeResponse>> start,
        Func<Task<SaveProbeResponse>, int, bool> wait, Func<long> now, Action<object> emit)
    { this.config = config; this.host = host; this.start = start; this.wait = wait; this.now = now; this.emit = emit; }

    public SaveProbeStatus Status
    {
        get { lock (gate) return state with { Marker = state.Marker == null ? null : new(state.Marker) }; }
    }

    public string Arm(string[] args)
    {
        lock (gate)
        {
            if (!config.EnablePhase9SaveProbe || disposed) return "disabled";
            if (state.Phase is "armed" or "preparing" || awaitingSaved) return "probe_active";
            if (config.Phase9SaveProbeTimeoutSeconds is < 1 or > 30) return "invalid_timeout";
            int timeout = config.Phase9SaveProbeTimeoutSeconds * 1000;
            if (args.Length is < 1 or > 2 || !ValidMode(args[0])) return "invalid_arguments";
            int delay = args[0] == "delayed" ? timeout + 1000 : 0;
            if (args.Length == 2 && !int.TryParse(args[1], NumberStyles.None, CultureInfo.InvariantCulture, out delay)) return "invalid_arguments";
            if (delay < 0 || (args[0] == "success" && delay >= timeout) || (args[0] == "delayed" && delay < timeout)) return "invalid_arguments";
            try
            {
                if (!host.WorldReady) return "world_not_ready";
                if (!host.HasAuthority) return "authority_required";
                var identity = host.Capture();
                if (!ValidIdentity(identity)) return "identity_unavailable";
                armedIdentity = identity;
            }
            catch { return "prepare_failed"; }
            mode = args[0]; delayMs = delay; timeoutMs = timeout; pending = null;
            state = new("armed", Code: "armed");
            return "armed";
        }
    }

    public void Saving()
    {
        Pending run;
        SaveProbeIdentity identity;
        lock (gate)
        {
            if (!config.EnablePhase9SaveProbe || disposed || state.Phase != "armed") return;
            run = new(new(Guid.NewGuid().ToString("N"), mode, delayMs), now(), timeoutMs);
            pending = run; identity = armedIdentity!;
            state = new("preparing", Code: "preparing");
        }
        string? reason;
        string identitySource = "armed";
        try
        {
            if (!host.WorldReady || !host.HasAuthority) throw new InvalidOperationException("world_unavailable");
            var current = host.Capture();
            if (!ValidIdentity(current)) throw new InvalidOperationException("identity_unavailable");
            bool sameWorld = current.World == identity.World;
            identity = current;
            identitySource = "saving";
            if (!sameWorld) throw new InvalidOperationException("world_changed");
            start(run.Request, response => Receive(run, response));
            // Capture and transport startup consume the same finite preparation budget.
            int remaining = (int)Math.Max(0, run.TimeoutMs - (now() - run.StartedAt));
            bool completed = run.Completion.Task.IsCompletedSuccessfully || (remaining > 0 && wait(run.Completion.Task, remaining));
            reason = completed ? run.Completion.Task.GetAwaiter().GetResult().Error : "timeout";
        }
        catch { reason = "prepare_failed"; }
        lock (gate)
        {
            run.Closed = true;
            if (reason == "timeout" && run.Completion.Task.IsCompletedSuccessfully)
                reason = run.Completion.Task.GetAwaiter().GetResult().Error;
            // Freeze the selected decision before crossing the save-data boundary.
            var marker = BuildMarker(run, identity, identitySource, reason, Math.Max(0, now() - run.StartedAt));
            state = new(reason == null ? "prepared" : "unconfirmed", marker, Code: reason ?? "prepared");
            awaitingSaved = true;
        }
        try
        {
            host.Write(Status.Marker!);
            lock (gate) state = state with { MarkerWritten = true };
        }
        catch { lock (gate) state = state with { Code = "marker_write_failed" }; }
        Emit(new SaveProbeEvidence("saving_decided", run.Request.RequestId, State: Status));
    }

    private void Receive(Pending run, SaveProbeResponse response)
    {
        int thread = Environment.CurrentManagedThreadId;
        bool late = false;
        string code;
        lock (gate)
        {
            if (response.RequestId != run.Request.RequestId) code = "mismatched_request";
            else if (run.Closed || !ReferenceEquals(pending, run))
            {
                late = true; code = "late_response";
            }
            else if (run.Completion.Task.IsCompleted) code = "duplicate_response";
            else if (now() - run.StartedAt >= run.TimeoutMs)
            {
                late = run.Late = true; run.ResponseThread = thread; run.Direct = true; code = "late_response";
                run.Completion.TrySetResult(new(response.RequestId, "timeout"));
            }
            else
            {
                run.ResponseThread = thread; run.Direct = true; code = "response_completed";
                string? error = response.Error is null or "disconnected" or "prepare_failed" ? response.Error : "prepare_failed";
                run.Completion.TrySetResult(response with { Error = error });
            }
        }
        Emit(new SaveProbeEvidence("response", response.RequestId, late, thread, code));
    }

    public void Cancel()
    {
        lock (gate)
        {
            if (state.Phase == "armed") state = new("cancelled", Code: "cancelled");
            else if (state.Phase == "preparing" && pending != null && !pending.Closed)
                pending.Completion.TrySetResult(new(pending.Request.RequestId, "cancelled"));
        }
    }

    public void Saved()
    {
        lock (gate)
        {
            if (!awaitingSaved || state.Marker == null) return;
            awaitingSaved = false; state = state with { Phase = "saved" };
        }
        Emit(new SaveProbeEvidence("saved", State: Status));
    }

    public void WorldChanged() { Cancel(); lock (gate) awaitingSaved = false; }
    public void SaveLoaded()
    {
        if (!config.EnablePhase9SaveProbe || disposed) return;
        WorldChanged();
        try
        {
            var marker = host.Read();
            if (marker == null) Emit(new SaveProbeEvidence("persisted_marker_absent"));
            else if (ValidateMarker(marker, out var error)) Emit(new SaveProbeEvidence("persisted_marker_observed", marker["request_id"], Marker: new(marker)));
            else Emit(new SaveProbeEvidence("persisted_marker_invalid", Code: error));
        }
        catch { Emit(new SaveProbeEvidence("persisted_marker_invalid", Code: "marker_read_failed")); }
    }
    public void Dispose() { Cancel(); lock (gate) disposed = true; }
    private void Emit(SaveProbeEvidence evidence) { try { emit(evidence); } catch { /* Diagnostics cannot abort a save. */ } }

    public static SaveProbe? Install(AdapterConfig config, Func<SaveProbe> create,
        Action<string, string, Action<string[]>> add, Action<SaveProbe> subscribe)
    {
        if (!config.EnablePhase9SaveProbe) return null;
        var probe = create();
        add("gameagent_phase9_save_arm", "Diagnostic handoff only, not Runtime durability. Usage: gameagent_phase9_save_arm <success|delayed|disconnect|prepare_failure> [response-delay-ms]. Requires a loaded single-player host save. Default delays: 0 ms; delayed: timeout + 1000 ms.",
            args => probe.Emit(new SaveProbeEvidence("arm", Code: probe.Arm(args), State: probe.Status)));
        add("gameagent_phase9_save_status", "Print the latest diagnostic save probe state.",
            args => probe.Emit(new SaveProbeEvidence("status", Code: args.Length == 0 ? "status" : "invalid_arguments", State: probe.Status)));
        add("gameagent_phase9_save_cancel", "Disarm or cancel pending diagnostic preparation.", args =>
        {
            if (args.Length == 0) probe.Cancel();
            probe.Emit(new SaveProbeEvidence("cancel", Code: args.Length == 0 ? "cancel" : "invalid_arguments", State: probe.Status));
        });
        subscribe(probe);
        return probe;
    }

    private static Dictionary<string, string> BuildMarker(Pending run, SaveProbeIdentity identity, string identitySource, string? reason, long waited)
    {
        var marker = new Dictionary<string, string>
        {
            ["schema_version"] = "1", ["request_id"] = run.Request.RequestId,
            ["world"] = identity.World, ["date"] = identity.Date, ["game_time"] = Number(identity.GameTime), ["identity_source"] = identitySource,
            ["started_utc"] = run.StartedUtc, ["mode"] = run.Request.Mode, ["status"] = reason == null ? "prepared" : "unconfirmed",
            ["timeout_ms"] = Number(run.TimeoutMs), ["response_delay_ms"] = Number(run.Request.ResponseDelayMs), ["wait_ms"] = Number(waited),
            ["saving_thread_id"] = Number(run.SavingThread), ["response_thread_id"] = Number(run.ResponseThread),
            ["completion_direct"] = run.Direct ? "true" : "false", ["arrived_late"] = run.Late ? "true" : "false"
        };
        if (reason != null) marker["reason"] = reason;
        return marker;
    }
    private static string Number(long value) => value.ToString(CultureInfo.InvariantCulture);
    private static bool ValidMode(string value) => value is "success" or "delayed" or "disconnect" or "prepare_failure";
    private static bool ValidIdentity(SaveProbeIdentity value) => !string.IsNullOrWhiteSpace(value.World) &&
        Regex.IsMatch(value.Date, @"^[1-9][0-9]*-(spring|summer|fall|winter)-([1-9]|1[0-9]|2[0-8])$") &&
        value.GameTime is >= 0 and <= 2800 && value.GameTime % 100 < 60;

    public static bool ValidateMarker(Dictionary<string, string> marker, out string error)
    {
        error = "invalid_marker";
        string[] required = { "schema_version", "request_id", "world", "date", "game_time", "identity_source", "started_utc", "mode", "status",
            "timeout_ms", "response_delay_ms", "wait_ms", "saving_thread_id", "response_thread_id", "completion_direct", "arrived_late" };
        if (required.Any(key => !marker.ContainsKey(key) || marker[key] == null) || marker.Keys.Any(key => key != "reason" && !required.Contains(key)) ||
            marker["schema_version"] != "1" || !Guid.TryParseExact(marker["request_id"], "N", out _) || !ValidMode(marker["mode"]) ||
            !DateTimeOffset.TryParseExact(marker["started_utc"], "O", CultureInfo.InvariantCulture, DateTimeStyles.None, out _)) return false;
        long Read(string key) => long.TryParse(marker[key], NumberStyles.None, CultureInfo.InvariantCulture, out var value) ? value : -1;
        long timeout = Read("timeout_ms"), delay = Read("response_delay_ms"), waited = Read("wait_ms");
        long saving = Read("saving_thread_id"), response = Read("response_thread_id"), gameTime = Read("game_time");
        if (timeout is < 1000 or > 30000 || timeout % 1000 != 0 || delay is < 0 or > int.MaxValue || waited < 0 ||
            saving is < 1 or > int.MaxValue || response is < 0 or > int.MaxValue || gameTime is < 0 or > 2800 ||
            !ValidIdentity(new(marker["world"], marker["date"], (int)gameTime)) ||
            marker["completion_direct"] is not ("true" or "false") || marker["arrived_late"] is not ("true" or "false") ||
            (marker["mode"] == "success" && delay >= timeout) || (marker["mode"] == "delayed" && delay < timeout)) return false;
        bool direct = marker["completion_direct"] == "true", late = marker["arrived_late"] == "true";
        if (direct != (response > 0)) return false;
        marker.TryGetValue("reason", out string? reason);
        if (marker["identity_source"] is not ("armed" or "saving") || (marker["identity_source"] == "armed" && reason != "prepare_failed")) return false;
        if (marker["status"] == "prepared")
        {
            if (reason != null || !direct || response == saving || late || marker["mode"] != "success") return false;
        }
        else if (marker["status"] == "unconfirmed")
        {
            if (reason is not ("timeout" or "disconnected" or "prepare_failed" or "cancelled")) return false;
            if (reason == "timeout" && (waited < timeout || direct != late)) return false;
            if (reason == "cancelled" && direct) return false;
            if (reason == "disconnected" && (!direct || marker["mode"] != "disconnect")) return false;
            if (late && reason != "timeout") return false;
        }
        else return false;
        error = ""; return true;
    }
}
