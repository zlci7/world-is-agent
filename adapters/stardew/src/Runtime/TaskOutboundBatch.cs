using GameAgent.Protocol.V1Alpha2;

namespace GameAgent.Stardew.Runtime;

public static class TaskOutboundBatch
{
    public static IReadOnlyList<AdapterMessage> EvidenceThenClock(
        IEnumerable<AdapterMessage> evidence,
        AdapterMessage clock)
    {
        if (clock.PayloadCase != AdapterMessage.PayloadOneofCase.WorldClock)
            throw new ArgumentException("final task batch message must be a world clock update", nameof(clock));

        List<AdapterMessage> result = new();
        foreach (AdapterMessage message in evidence)
        {
            if (message.PayloadCase != AdapterMessage.PayloadOneofCase.Event || message.Event.TaskEvidence.Count == 0)
                throw new ArgumentException("task batch evidence must be carried by a GameEvent", nameof(evidence));
            result.Add(message);
        }
        result.Add(clock);
        return result;
    }
}
