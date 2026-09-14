using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
using Google.Protobuf;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class PlayerTaskSourceTests
{
    private static readonly RuntimeWorldSnapshot World = new("sim", "world", "run", 1, "game", 10, 1);

    private static GameEvent Build(string kind) => kind == "click"
        ? ProtocolMapper.BuildPlayerInteractedWithNpcEvent("actor", "Linus", "player", "Player", "conversation", "test", 1, "world", new GameTime(), "player-click")
        : ProtocolMapper.BuildPlayerSaidToNpcEvent("actor", "Linus", "player", "Player", "conversation", kind,
            "Meet at the beach at 14:00 until 15:00.", kind == "option" ? 0 : null, "test", 1, "world", new GameTime(), "player-" + kind);

    [Theory]
    [InlineData("click")]
    [InlineData("option")]
    [InlineData("free_text")]
    public void ProductionEventMatchesSharedRuntimeFixture(string kind)
    {
        GameEvent message = Build(kind);
        ProtocolMapper.AttachPlayerInteractionSource(message, World);
        GameEvent expected = JsonParser.Default.Parse<GameEvent>(File.ReadAllText(Path.Combine(AppContext.BaseDirectory, "fixtures", "player-" + kind + ".json")));
        Assert.Equal(expected, message);
        Assert.Equal(message, GameEvent.Parser.ParseFrom(message.ToByteArray()));
    }

    [Theory]
    [InlineData("click")]
    [InlineData("option")]
    [InlineData("free_text")]
    public void PausedOrDisconnectedKeepsOrdinaryEventWithoutTaskAuthority(string kind)
    {
        GameEvent message = Build(kind);
        GameEvent before = message.Clone();
        ProtocolMapper.AttachPlayerInteractionSource(message, null);
        Assert.Equal(before, message);
        Assert.Null(message.InteractionSource);
    }

    [Fact]
    public void NewBindingDoesNotRewriteOldEventAuthority()
    {
        GameEvent old = Build("click");
        ProtocolMapper.AttachPlayerInteractionSource(old, World);
        GameEvent next = Build("free_text");
        ProtocolMapper.AttachPlayerInteractionSource(next, World with { WorldRunId = "new-run", ExecutionGeneration = 3 });
        Assert.Equal("run", old.InteractionSource.Scope.WorldRunId);
        Assert.Equal((ulong)1, old.InteractionSource.Scope.ExecutionGeneration);
        Assert.Equal("new-run", next.InteractionSource.Scope.WorldRunId);
        Assert.Equal((ulong)3, next.InteractionSource.Scope.ExecutionGeneration);
    }

    [Fact]
    public void MismatchedWorldUnboundGenerationAndExistingArrivalSourceAreRejected()
    {
        Assert.Throws<ArgumentException>(() => ProtocolMapper.AttachPlayerInteractionSource(Build("click"), World with { WorldId = "other" }));
        Assert.Throws<ArgumentException>(() => ProtocolMapper.AttachPlayerInteractionSource(Build("click"), World with { ExecutionGeneration = 0 }));
        GameEvent arrival = Build("free_text");
        arrival.InteractionSource = new InteractionSource { Kind = "task_arrival", TaskId = "task", OperationId = "operation" };
        Assert.Throws<ArgumentException>(() => ProtocolMapper.AttachPlayerInteractionSource(arrival, World));
        Assert.Equal("task_arrival", arrival.InteractionSource.Kind);
    }
}
