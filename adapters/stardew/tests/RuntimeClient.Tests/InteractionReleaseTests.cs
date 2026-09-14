using System.Reflection;
using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Dialogue;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using StardewModdingAPI;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class InteractionReleaseTests
{
    [Fact]
    public void AbandoningReplyAfterItsTurnEndedReleasesOriginalArrivalExactlyOnce()
    {
        var fixture = new ClientFixture();
        fixture.Index.MarkPresentation("reply", "conversation");
        fixture.Contexts.TryReserveHandoff(ClientFixture.Snapshot("reply"), out _);
        fixture.Contexts.Commit("reply");
        fixture.Call("HandleTurnCompletion", new TurnCompletion { EventId = "reply" });
        Assert.Null(fixture.Contexts.TryGet("reply"));

        fixture.Call("ReleaseInteractionContext", "reply");
        fixture.Call("ReleaseInteractionContext", "reply");

        Assert.Equal(1, fixture.Control.Releases);
        Assert.Null(fixture.Index.FindTaskEvent("conversation"));
    }

    [Fact]
    public void LateArrivalTurnCompletionPreservesReplyAndDisplayedInteraction()
    {
        var fixture = new ClientFixture();
        fixture.Contexts.TryReserveHandoff(ClientFixture.Snapshot("reply"), out _);
        fixture.Contexts.Commit("reply");

        fixture.Call("HandleTurnCompletion", new TurnCompletion { EventId = "arrival" });
        fixture.Call("HandleTurnCompletion", new TurnCompletion { EventId = "arrival" });

        Assert.Equal(0, fixture.Control.Releases);
        Assert.NotNull(fixture.Lifecycle.FindCommitted("arrival"));
        Assert.NotNull(fixture.Contexts.TryGet("reply"));
    }
}

internal sealed class ClientFixture
{
    public readonly CountingControl Control = new();
    public readonly NpcInteractionLifecycle Lifecycle;
    public readonly RuntimeClient Client;
    public InteractionContextStore Contexts => Field<InteractionContextStore>("interactionContextStore");
    public TaskInteractionConversationIndex Index => Field<TaskInteractionConversationIndex>("taskInteractionConversations");

    public ClientFixture()
    {
        Lifecycle = new(new TaskInteractionHandoffStore(), Control);
        var monitor = DispatchProxy.Create<IMonitor, SilentMonitor>();
        Client = new RuntimeClient(new AdapterConfig(), new MainThreadDispatcher(monitor), null!,
            new ConversationStateStore(new ConversationIdGenerator()), null!, null!, null!, null!, null!, null!, null!,
            new TaskExecutionDriver(new TaskSourceContextStore(), new TaskOperationReceipts(), new NpcControlLease()),
            new NpcControlLease(), new MeetingWaitMonitor(), Lifecycle, monitor);
        var operation = new OperationKey("world", "run", 1, "npc:Linus", "task", "wait");
        var agreement = new MeetingAgreement(1, "beach", "npc:Linus", new MeetingDate(1, "spring", 1), GameClock.ClockId, 660, 900, 960);
        Assert.True(Lifecycle.TryBegin("arrival", new TaskOperationSource(operation, 1, "wake", agreement), out _));
        Lifecycle.Accept("arrival");
        Index.Register("conversation", "arrival");
        Index.MarkPresentation("arrival", "conversation");
        Assert.True(Contexts.TryReserve(Snapshot("arrival"), out _));
        Contexts.Commit("arrival");
    }

    public static InteractionContextSnapshot Snapshot(string eventId) =>
        new(eventId, "world", "npc:Linus", "player:local", "conversation", "Beach", 28, 36, "Beach", 28, 37, 2);
    public T Field<T>(string name) => (T)typeof(RuntimeClient).GetField(name, BindingFlags.NonPublic | BindingFlags.Instance)!.GetValue(Client)!;
    public void SetField(string name, object value) =>
        typeof(RuntimeClient).GetField(name, BindingFlags.NonPublic | BindingFlags.Instance)!.SetValue(Client, value);
    public object? Call(string method, params object[] arguments) =>
        typeof(RuntimeClient).GetMethod(method, BindingFlags.NonPublic | BindingFlags.Instance)!.Invoke(Client, arguments);
}

public class SilentMonitor : DispatchProxy
{
    protected override object? Invoke(MethodInfo? targetMethod, object?[]? args) =>
        targetMethod?.ReturnType == typeof(bool) ? false : null;
}

internal sealed class CountingControl : ITaskInteractionControl
{
    public int Releases { get; private set; }
    public bool PromoteWaitToInteraction(OperationKey operation) => true;
    public bool CommitInteraction(OperationKey operation) => true;
    public bool ReleaseInteraction(OperationKey operation, string reason) { Releases++; return true; }
    public bool BeginInteractionApproach(OperationKey interactionOperation, OperationKey approachOperation) => false;
    public bool FinishInteractionApproach(OperationKey operation, bool returnToInteraction) => false;
    public bool IsHandedOff(string taskId, string operationId) => true;
    public TaskControlDecision ReleaseForTaskControl(string taskId, string operationId, string reason) => new("handed_off");
}
