using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class RuntimeSessionStateTests
{
    [Fact]
    public void HappyPathSeparatesRuntimeAndTaskReadiness()
    {
        RuntimeSessionState session = new();

        session.BeginConnection();
        Assert.Equal(RuntimeSessionPhase.AwaitingEnvironmentReady, session.Phase);
        Assert.False(session.CanUseRuntime);
        Assert.False(session.CanUseTasks);

        Assert.True(session.AcceptEnvironmentReady(new[] { RuntimeSessionState.TaskExtension }, out _));
        Assert.True(session.AcceptCapabilityRequest(out _));
        session.MarkCapabilitiesSent();

        Assert.Equal(RuntimeSessionPhase.AwaitingWorldBindingReady, session.Phase);
        Assert.True(session.CanUseRuntime);
        Assert.False(session.CanUseTasks);

        Assert.True(session.AcceptWorldBindingReady("ready", out _));
        Assert.Equal(RuntimeSessionPhase.TaskReady, session.Phase);
        Assert.True(session.CanUseRuntime);
        Assert.True(session.CanUseTasks);
    }

    [Fact]
    public void RejectsHandshakeMessagesOutOfOrder()
    {
        RuntimeSessionState session = new();
        session.BeginConnection();

        Assert.False(session.AcceptCapabilityRequest(out string error));
        Assert.Equal("handshake_out_of_order", error);
        Assert.Equal(RuntimeSessionPhase.AwaitingEnvironmentReady, session.Phase);
    }

    [Fact]
    public void MissingTaskExtensionPausesOnlyTaskAdmission()
    {
        RuntimeSessionState session = new();
        session.BeginConnection();

        Assert.True(session.AcceptEnvironmentReady(Array.Empty<string>(), out _));
        Assert.True(session.AcceptCapabilityRequest(out _));
        session.MarkCapabilitiesSent();

        Assert.Equal(RuntimeSessionPhase.TaskPaused, session.Phase);
        Assert.True(session.CanUseRuntime);
        Assert.False(session.CanUseTasks);
    }

    [Fact]
    public void DisconnectAllowsACompleteManualReconnect()
    {
        RuntimeSessionState session = ReadySession();
        session.Disconnect();

        Assert.Equal(RuntimeSessionPhase.Disconnected, session.Phase);
        Assert.False(session.CanUseRuntime);
        Assert.False(session.CanUseTasks);

        session.BeginConnection();
        Assert.True(session.AcceptEnvironmentReady(new[] { RuntimeSessionState.TaskExtension }, out _));
        Assert.True(session.AcceptCapabilityRequest(out _));
        session.MarkCapabilitiesSent();
        Assert.True(session.AcceptWorldBindingReady("paused", out _));

        Assert.Equal(RuntimeSessionPhase.TaskPaused, session.Phase);
        Assert.True(session.CanUseRuntime);
        Assert.False(session.CanUseTasks);
    }

    [Fact]
    public void BindingValidationFailurePausesOnlyTasks()
    {
        RuntimeSessionState session = ReadySession();

        session.PauseTasks();

        Assert.Equal(RuntimeSessionPhase.TaskPaused, session.Phase);
        Assert.True(session.CanUseRuntime);
        Assert.False(session.CanUseTasks);
    }

    [Fact]
    public void NewWorldRequiresBindingAgainWithoutDroppingRuntimeAdmission()
    {
        RuntimeSessionState session = ReadySession();

        session.PrepareWorldBinding();

        Assert.Equal(RuntimeSessionPhase.AwaitingWorldBindingReady, session.Phase);
        Assert.True(session.CanUseRuntime);
        Assert.False(session.CanUseTasks);
    }

    private static RuntimeSessionState ReadySession()
    {
        RuntimeSessionState session = new();
        session.BeginConnection();
        session.AcceptEnvironmentReady(new[] { RuntimeSessionState.TaskExtension }, out _);
        session.AcceptCapabilityRequest(out _);
        session.MarkCapabilitiesSent();
        session.AcceptWorldBindingReady("ready", out _);
        return session;
    }
}
