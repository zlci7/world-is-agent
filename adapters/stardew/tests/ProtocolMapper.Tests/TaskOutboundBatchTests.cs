using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskOutboundBatchTests
{
    [Fact]
    public void PlacesTheClockUpdateBeforeEveryEvidenceEvent()
    {
        AdapterMessage first = Evidence("first");
        AdapterMessage second = Evidence("second");
        AdapterMessage clock = new() { MessageId = "clock", WorldClock = new WorldClockUpdate() };

        IReadOnlyList<AdapterMessage> batch = TaskOutboundBatch.ClockThenEvidence(clock, new[] { first, second });

        Assert.Equal(new[] { "clock", "first", "second" }, batch.Select(message => message.MessageId));
        Assert.Equal(AdapterMessage.PayloadOneofCase.WorldClock, batch[0].PayloadCase);
    }

    private static AdapterMessage Evidence(string id) => new()
    {
        MessageId = id,
        Event = new GameEvent { TaskEvidence = { new TaskEvidence { FactId = id } } },
    };
}
