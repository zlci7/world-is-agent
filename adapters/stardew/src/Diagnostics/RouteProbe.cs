using System.Globalization;
using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Diagnostics;

internal enum ProbePhase { Idle, Routing, Dwelling, Restoring, Succeeded, Failed, Cancelled }
internal enum ProbeControl { None, Owned, Foreign }
internal record ProbePosition(string Location, int X, int Y, float PixelX = 0, float PixelY = 0);
internal record ProbeRequest(string Npc, ProbePosition Target, int DwellSeconds, int ObservationSeconds);
internal record ProbeSample(string World, string Date, int GameTime, ProbePosition Position,
    ProbeControl Control, bool TargetValid = true, bool NativeMovement = false, bool NativeStationary = false);
internal record ProbePrepared(ProbeSample Sample, int RouteLength, string NativeState);
internal record ProbeStatus(string? RunId = null, string? Npc = null, ProbePhase Phase = ProbePhase.Idle,
    string Code = "idle", ProbePosition? Start = null, ProbePosition? Target = null,
    ProbePosition? Arrival = null, ProbePosition? Release = null, ProbeSample? Final = null,
    int RouteLength = 0, int MapTransitions = 0, long TravelMs = 0, long DwellMs = 0, long RestoreMs = 0,
    string? NativeState = null, string? Restoration = null);

internal interface IRouteProbeDriver
{
    bool WorldReady { get; }
    bool HasAuthority { get; }
    ProbePrepared Prepare(ProbeRequest request);
    void Install();
    ProbeSample Sample();
    void Hold();
    void RestoreFlags();
    string Release(bool rejoinSchedule);
}

internal sealed class RouteProbe
{
    private readonly AdapterConfig config;
    private readonly IRouteProbeDriver driver;
    private readonly Func<long> now;
    private readonly Action<ProbeStatus> emit;
    private ProbeRequest? request;
    private ProbeSample? initial;
    private long startedAt, arrivedAt, releasedAt;
    private bool installed, released;

    public RouteProbe(AdapterConfig config, IRouteProbeDriver driver, Func<long> now, Action<ProbeStatus> emit)
    {
        this.config = config;
        this.driver = driver;
        this.now = now;
        this.emit = emit;
    }
    public ProbeStatus Status { get; private set; } = new();
    public bool Active => Status.Phase is ProbePhase.Routing or ProbePhase.Dwelling or ProbePhase.Restoring;
    public string Start(string[] args)
    {
        if (!config.EnablePhase9RouteProbe) return "disabled";
        if (Active) return "probe_active";
        if (!driver.WorldReady) return "world_not_ready";
        if (!driver.HasAuthority) return "authority_required";
        if (!TryParse(args, out var parsed)) return "invalid_arguments";
        if (config.AgentTargets?.Any(n => string.Equals(n?.Trim(), parsed!.Npc, StringComparison.OrdinalIgnoreCase)) != true)
            return "npc_not_controlled";

        request = parsed;
        startedAt = now();
        installed = released = false;
        initial = null;
        Status = new(RunId: Guid.NewGuid().ToString("N"), Npc: request!.Npc,
            Phase: ProbePhase.Routing, Code: "preparing", Target: request.Target);
        try
        {
            var prepared = driver.Prepare(request);
            initial = prepared.Sample;
            Status = Status with { Start = initial.Position, Final = initial, RouteLength = prepared.RouteLength, NativeState = prepared.NativeState };
            if (now() - startedAt > 5000) return FailStart("start_timeout");
            if (prepared.RouteLength <= 0) return FailStart("target_unreachable");
            installed = true;
            driver.Install();
            Status = Status with { Final = driver.Sample() };
            if (now() - startedAt > 5000) return FailStart("start_timeout");
            Status = Status with { Code = "routing" };
            emit(Status);
            return "started";
        }
        catch (Exception ex)
        {
            return FailStart(ex is InvalidOperationException ? ex.Message : "start_failed");
        }
    }

    public void Update()
    {
        if (!Active) return;
        try
        {
            var sample = driver.Sample();
            var previous = Status.Final;
            Status = Status with { Final = sample,
                MapTransitions = Status.MapTransitions + (previous != null && previous.Position.Location != sample.Position.Location ? 1 : 0) };
            UpdateElapsed();
            if (!driver.WorldReady || sample.World != initial!.World || sample.Date != initial.Date)
            { Finish(ProbePhase.Failed, "world_changed", false); return; }
            if (!driver.HasAuthority) { Finish(ProbePhase.Failed, "authority_lost", false); return; }
            if (!sample.TargetValid) { Finish(ProbePhase.Failed, "target_invalid"); return; }

            if (Status.Phase != ProbePhase.Restoring)
            {
                if (sample.Control != ProbeControl.Owned)
                { Finish(ProbePhase.Failed, sample.Control == ProbeControl.Foreign ? "control_lost" : "controller_missing"); return; }
                if (Status.Phase == ProbePhase.Routing)
                {
                    if (now() - startedAt > 120000) { Finish(ProbePhase.Failed, "route_timeout"); return; }
                    if (AtTarget(sample.Position, request!.Target))
                    {
                        arrivedAt = now();
                        driver.Hold();
                        Status = Status with { Phase = ProbePhase.Dwelling, Code = "arrived", Arrival = sample.Position };
                        emit(Status);
                    }
                }
                else
                {
                    if (!AtTarget(sample.Position, request!.Target)) { Finish(ProbePhase.Failed, "dwell_position_changed"); return; }
                    if (now() - arrivedAt > request.DwellSeconds * 1000L + 5000)
                    { Finish(ProbePhase.Failed, "dwell_timeout"); return; }
                    if (now() - arrivedAt >= request.DwellSeconds * 1000L)
                    {
                        releasedAt = now();
                        Status = Status with { Release = sample.Position, Phase = ProbePhase.Restoring, Code = "restoring" };
                        Release(true);
                        Status = Status with { Final = driver.Sample() };
                        emit(Status);
                    }
                }
            }
            else
            {
                if (sample.Control == ProbeControl.Foreign && !sample.NativeMovement)
                { Finish(ProbePhase.Failed, "native_control_lost", false); return; }
                long observationMs = now() - releasedAt;
                if (observationMs <= request!.ObservationSeconds * 1000L && sample.NativeMovement && sample.Position != Status.Release)
                { Finish(ProbePhase.Succeeded, "native_movement_observed"); return; }
                if (observationMs >= request.ObservationSeconds * 1000L)
                {
                    Finish(sample.NativeStationary && sample.Position == Status.Release ? ProbePhase.Succeeded : ProbePhase.Failed,
                        sample.NativeStationary && sample.Position == Status.Release ? "native_stationary" : "restore_timeout");
                    return;
                }
            }
            if (previous?.GameTime != sample.GameTime || previous?.Position.Location != sample.Position.Location || previous?.Control != sample.Control)
                emit(Status);
        }
        catch
        {
            Finish(ProbePhase.Failed, Status.Phase == ProbePhase.Restoring ? "restore_failed" : "probe_failed");
        }
    }

    public void Cancel(string code = "cancelled", bool rejoinSchedule = true)
    {
        if (!Active) return;
        try { Status = Status with { Final = driver.Sample() }; }
        catch { rejoinSchedule = false; }
        if (!driver.WorldReady || Status.Final?.World != initial?.World || Status.Final?.Date != initial?.Date)
            rejoinSchedule = false;
        Finish(ProbePhase.Cancelled, code, rejoinSchedule);
    }

    private string FailStart(string code) { Finish(ProbePhase.Failed, code); return code; }

    private void Finish(ProbePhase phase, string code, bool rejoinSchedule = true)
    {
        if (!Active) return;
        UpdateElapsed();
        try { Release(rejoinSchedule); }
        catch { Status = Status with { Restoration = "restore_failed" }; }
        if (installed)
        {
            try { Status = Status with { Final = driver.Sample() }; }
            catch { /* Preserve the last real sample when a save has already been detached. */ }
        }
        Status = Status with { Phase = phase, Code = code };
        emit(Status);
    }

    private void Release(bool rejoinSchedule)
    {
        if (!installed || released) return;
        released = true;
        if (Status.Final?.Control == ProbeControl.Foreign)
        {
            driver.RestoreFlags();
            Status = Status with { Restoration = "foreign_control_preserved" };
            return;
        }
        Status = Status with { Release = Status.Final?.Position };
        try { Status = Status with { Restoration = driver.Release(rejoinSchedule) }; }
        catch
        {
            Status = Status with { Restoration = "restore_failed" };
            throw;
        }
    }

    private void UpdateElapsed()
    {
        Status = Status.Phase switch
        {
            ProbePhase.Routing => Status with { TravelMs = now() - startedAt },
            ProbePhase.Dwelling => Status with { DwellMs = now() - arrivedAt },
            ProbePhase.Restoring => Status with { RestoreMs = now() - releasedAt },
            _ => Status
        };
    }

    private static bool AtTarget(ProbePosition position, ProbePosition target) =>
        position.Location == target.Location && position.X == target.X && position.Y == target.Y;

    private static bool TryParse(string[] args, out ProbeRequest? request)
    {
        request = null;
        int dwell = 2, observation = 30;
        if (args.Length < 4 || args.Length > 6 || string.IsNullOrWhiteSpace(args[0]) || string.IsNullOrWhiteSpace(args[1]) ||
            !int.TryParse(args[2], NumberStyles.Integer, CultureInfo.InvariantCulture, out int x) ||
            !int.TryParse(args[3], NumberStyles.Integer, CultureInfo.InvariantCulture, out int y) ||
            (args.Length >= 5 && !int.TryParse(args[4], NumberStyles.Integer, CultureInfo.InvariantCulture, out dwell)) ||
            (args.Length >= 6 && !int.TryParse(args[5], NumberStyles.Integer, CultureInfo.InvariantCulture, out observation)) ||
            dwell < 1 || dwell > 60 || observation < 1 || observation > 120) return false;
        request = new(args[0].Trim(), new(args[1].Trim(), x, y), dwell, observation);
        return true;
    }
    public static void RegisterCommands(AdapterConfig config, Action<string, string, Action<string[]>> add,
        Action<string[]> start, Action<string[]> status, Action<string[]> cancel)
    {
        if (!config.EnablePhase9RouteProbe) return;
        add("gameagent_phase9_route_probe", "Console-only feasibility probe. Usage: gameagent_phase9_route_probe <configured-npc> <location> <tile-x> <tile-y> [dwell-seconds:1..60,default=2] [restore-observation-seconds:1..120,default=30]. Travel deadline: 120 seconds. Requires single player and a loaded save.", start);
        add("gameagent_phase9_route_status", "Print the last sampled phase9 route probe JSON status without changing state.", status);
        add("gameagent_phase9_route_cancel", "Cancel the active phase9 route probe, release its control, and rejoin the current native schedule.", cancel);
    }
}
