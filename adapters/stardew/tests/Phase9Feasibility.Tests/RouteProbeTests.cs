using GameAgent.Stardew.Diagnostics;
using GameAgent.Stardew.Runtime;
using Xunit;

namespace Phase9Feasibility.Tests;

public sealed class RouteProbeTests
{
    private readonly FakeDriver driver = new();
    private readonly List<ProbeStatus> records = new();
    private long now;
    private RouteProbe Create() => new(new AdapterConfig { EnablePhase9RouteProbe = true, AgentTargets = new() { "Linus" } }, driver, () => now, records.Add);
    private static string[] Args => new[] { "Linus", "Town", "10", "11", "2", "3" };

    [Fact]
    public void CrossMapProgressRequiresExactObservedDestination()
    {
        var probe = Create();
        Assert.Equal("started", probe.Start(Args));
        driver.Current = driver.Current with { Position = new("Mountain", 10, 11) };
        probe.Update();
        Assert.Equal(ProbePhase.Routing, probe.Status.Phase);
        driver.Current = driver.Current with { Position = new("Town", 10, 12) };
        probe.Update();
        Assert.Equal(ProbePhase.Routing, probe.Status.Phase);
        driver.Arrive(); probe.Update();
        Assert.Equal(ProbePhase.Dwelling, probe.Status.Phase);
        Assert.Equal(1, probe.Status.MapTransitions);
    }

    [Fact]
    public void ArrivalDwellReleaseAndActualNativeMovementSucceedsOnce()
    {
        var probe = Create(); probe.Start(Args);
        now = 1000; driver.Arrive(); probe.Update();
        now = 2999; probe.Update();
        Assert.Equal(ProbePhase.Dwelling, probe.Status.Phase);
        Assert.Equal(0, driver.Releases);
        now = 3000; probe.Update();
        Assert.Equal(ProbePhase.Restoring, probe.Status.Phase);
        Assert.Equal(1, driver.Releases);
        driver.Current = driver.Current with { NativeMovement = true };
        probe.Update();
        Assert.Equal(ProbePhase.Restoring, probe.Status.Phase);
        driver.Current = driver.Current with { Position = new("Town", 10, 12), NativeMovement = true };
        now = 3100; probe.Update(); probe.Update(); probe.Cancel();
        Assert.Equal("native_movement_observed", probe.Status.Code);
        Assert.Equal(ProbePhase.Succeeded, probe.Status.Phase);
        Assert.Equal(1000, probe.Status.TravelMs);
        Assert.Equal(2000, probe.Status.DwellMs);
        Assert.Equal(100, probe.Status.RestoreMs);
        Assert.Single(records, x => x.Phase == ProbePhase.Succeeded);
        Assert.Equal(1, driver.Releases);
    }

    [Fact]
    public void StationaryRequiresNativeEvidenceAndFullObservationWindow()
    {
        var probe = Create(); probe.Start(Args); driver.Arrive(); probe.Update();
        now = 2000; probe.Update();
        driver.Current = driver.Current with { NativeStationary = true };
        now = 4999; probe.Update();
        Assert.Equal(ProbePhase.Restoring, probe.Status.Phase);
        now = 5016; probe.Update();
        Assert.Equal("native_stationary", probe.Status.Code);
        Assert.Equal(ProbePhase.Succeeded, probe.Status.Phase);
    }

    [Fact]
    public void RepeatedCancelReleasesOwnedControllerOnce()
    {
        var probe = Create(); probe.Start(Args); probe.Cancel(); probe.Cancel(); probe.Update();
        Assert.Equal(ProbePhase.Cancelled, probe.Status.Phase);
        Assert.Equal(1, driver.Releases);
        Assert.True(driver.LastRejoin);
        Assert.Single(records, x => x.Phase == ProbePhase.Cancelled);
        Assert.Equal(ProbeControl.None, probe.Status.Final!.Control);
    }

    [Fact]
    public void ForeignControllerIsNeverReleasedOrHaltedEvenAtTarget()
    {
        var probe = Create(); probe.Start(Args); driver.Arrive();
        driver.Current = driver.Current with { Control = ProbeControl.Foreign };
        probe.Update(); probe.Cancel();
        Assert.Equal("control_lost", probe.Status.Code);
        Assert.Equal(0, driver.Releases);
        Assert.Equal(0, driver.Holds);
    }

    [Theory]
    [InlineData("other-save", "spring-1")]
    [InlineData("save", "spring-2")]
    public void WorldOrDayChangeTerminatesOnceWithoutRejoining(string world, string date)
    {
        var probe = Create(); probe.Start(Args);
        driver.Current = driver.Current with { World = world, Date = date };
        probe.Update(); probe.Update();
        Assert.Equal("world_changed", probe.Status.Code);
        Assert.Equal(1, driver.Releases);
        Assert.False(driver.LastRejoin);
        Assert.Single(records, x => x.Phase == ProbePhase.Failed);
    }

    [Fact]
    public void MissingControllerBeforeArrivalFails()
    {
        var probe = Create(); probe.Start(Args);
        driver.Current = driver.Current with { Control = ProbeControl.None };
        probe.Update();
        Assert.Equal("controller_missing", probe.Status.Code);
    }

    [Fact]
    public void InvalidatedTargetFailsAndReleasesControl()
    {
        var probe = Create(); probe.Start(Args);
        driver.Current = driver.Current with { TargetValid = false };
        probe.Update();
        Assert.Equal("target_invalid", probe.Status.Code);
        Assert.Equal(1, driver.Releases);
    }

    [Fact]
    public void PreparationAndInstallHaveFiniteDeadlines()
    {
        var probe = Create(); driver.OnPrepare = () => now = 5001;
        Assert.Equal("start_timeout", probe.Start(Args));
        Assert.Equal(0, driver.Installs);
        driver.OnPrepare = null; driver.OnInstall = () => now += 5001;
        Assert.Equal("start_timeout", probe.Start(Args));
        Assert.Equal(1, driver.Releases);
    }

    [Fact]
    public void RoutingHasFiniteDeadline()
    {
        var probe = Create(); probe.Start(Args); now = 120001; probe.Update();
        Assert.Equal("route_timeout", probe.Status.Code);
        Assert.Equal(1, driver.Releases);
    }

    [Fact]
    public void MissedDwellDeadlineFailsInsteadOfReportingUnboundedDwell()
    {
        var probe = Create(); probe.Start(Args); driver.Arrive(); probe.Update();
        now = 7001; probe.Update();
        Assert.Equal("dwell_timeout", probe.Status.Code);
    }

    [Fact]
    public void RestorationWithoutNativeEvidenceTimesOut()
    {
        var probe = Create(); probe.Start(Args); driver.Arrive(); probe.Update();
        now = 2000; probe.Update(); now = 5000; probe.Update();
        Assert.Equal("restore_timeout", probe.Status.Code);
        Assert.Equal(ProbePhase.Failed, probe.Status.Phase);
        Assert.Equal(1, driver.Releases);
    }

    [Fact]
    public void LateNativeMovementCannotSatisfyExpiredObservation()
    {
        var probe = Create(); probe.Start(Args); driver.Arrive(); probe.Update();
        now = 2000; probe.Update();
        now = 5001;
        driver.Current = driver.Current with { NativeMovement = true, Position = new("Town", 11, 11) };
        probe.Update();
        Assert.Equal("restore_timeout", probe.Status.Code);
    }

    [Fact]
    public void RestoreExceptionIsTerminalWithExplicitRestorationFailure()
    {
        var probe = Create(); probe.Start(Args); driver.Arrive(); probe.Update();
        driver.ReleaseError = true; now = 2000; probe.Update(); probe.Update();
        Assert.Equal("restore_failed", probe.Status.Code);
        Assert.Equal("restore_failed", probe.Status.Restoration);
        Assert.Equal(ProbePhase.Failed, probe.Status.Phase);
        Assert.Equal(1, driver.Releases);
    }

    [Theory]
    [InlineData("Linus Town 10 nope")]
    [InlineData("Robin Town 10 11")]
    [InlineData("Linus Town 10 11 0")]
    [InlineData("Linus Town 10 11 61")]
    [InlineData("Linus Town 10 11 2 121")]
    [InlineData("Linus Town 10 11 2 NaN")]
    [InlineData("Linus Town 10 11 2 3 extra")]
    public void InvalidArgumentsAndUncontrolledNpcRejectBeforeMutation(string args)
    {
        var probe = Create(); Assert.NotEqual("started", probe.Start(args.Split(' ')));
        Assert.Equal(0, driver.Prepares); Assert.Equal(0, driver.Installs);
    }

    [Fact]
    public void EmptyAllowlistAndMissingWorldOrAuthorityRejectBeforePreparation()
    {
        var probe = new RouteProbe(new AdapterConfig { EnablePhase9RouteProbe = true }, driver, () => now, records.Add);
        Assert.Equal("npc_not_controlled", probe.Start(Args));
        probe = Create(); driver.WorldReady = false;
        Assert.Equal("world_not_ready", probe.Start(Args));
        driver.WorldReady = true; driver.HasAuthority = false;
        Assert.Equal("authority_required", probe.Start(Args));
        Assert.Equal(0, driver.Prepares);
    }

    [Fact]
    public void UnreachableTargetAndDuplicateStartDoNotInstall()
    {
        var probe = Create(); driver.PrepareError = "target_unreachable";
        Assert.Equal("target_unreachable", probe.Start(Args));
        Assert.Equal(0, driver.Installs);
        driver.PrepareError = null; probe.Start(Args);
        Assert.Equal("probe_active", probe.Start(Args));
        Assert.Equal(1, driver.Installs);
    }

    [Fact]
    public void StatusDoesNotSampleOrMutateDriver()
    {
        var probe = Create(); probe.Start(Args); int reads = driver.Reads; int count = records.Count;
        var first = probe.Status; now = 999999; var second = probe.Status;
        Assert.Same(first, second); Assert.Equal(reads, driver.Reads); Assert.Equal(count, records.Count);
        Assert.Equal(0, driver.Releases);
    }

    [Fact]
    public void DefaultsDisableStartAndCommandRegistration()
    {
        var config = new AdapterConfig(); var commands = new List<string>();
        RouteProbe.RegisterCommands(config, (name, _, _) => commands.Add(name), _ => { }, _ => { }, _ => { });
        Assert.Empty(commands);
        var probe = new RouteProbe(config, driver, () => now, records.Add);
        Assert.Equal("disabled", probe.Start(Args)); Assert.Equal(0, driver.Prepares);
        config.EnablePhase9RouteProbe = true;
        RouteProbe.RegisterCommands(config, (name, _, _) => commands.Add(name), _ => { }, _ => { }, _ => { });
        Assert.Equal(new[] { "gameagent_phase9_route_probe", "gameagent_phase9_route_status", "gameagent_phase9_route_cancel" }, commands);
    }

    private sealed class FakeDriver : IRouteProbeDriver
    {
        public bool WorldReady { get; set; } = true;
        public bool HasAuthority { get; set; } = true;
        public ProbeSample Current = new("save", "spring-1", 900, new("Mountain", 2, 3), ProbeControl.None);
        public int Prepares, Installs, Reads, Releases, Holds;
        public bool LastRejoin;
        public string? PrepareError;
        public bool ReleaseError;
        public Action? OnPrepare, OnInstall;
        public ProbePrepared Prepare(ProbeRequest request)
        {
            Prepares++; OnPrepare?.Invoke();
            if (PrepareError != null) throw new InvalidOperationException(PrepareError);
            return new(Current, 100, "native_schedule");
        }
        public void Install() { Installs++; Current = Current with { Control = ProbeControl.Owned }; OnInstall?.Invoke(); }
        public ProbeSample Sample() { Reads++; return Current; }
        public void Hold() { Holds++; }
        public void RestoreFlags() { }
        public string Release(bool rejoinSchedule)
        {
            if (Current.Control == ProbeControl.Foreign) throw new Exception("foreign controller released");
            Releases++; LastRejoin = rejoinSchedule; Current = Current with { Control = ProbeControl.None };
            if (ReleaseError) throw new InvalidOperationException("native_route_unreachable");
            return rejoinSchedule ? "native_rejoined" : "released";
        }
        public void Arrive() => Current = Current with { Position = new("Town", 10, 11) };
    }
}
