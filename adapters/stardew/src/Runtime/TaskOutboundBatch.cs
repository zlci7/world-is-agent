using GameAgent.Protocol.V1Alpha2;

namespace GameAgent.Stardew.Runtime;

public static class TaskOutboundBatch
{
    // The Runtime rejects evidence whose OccurredAt is later than the clock it has
    // already observed, so a same-moment fact only lands if the clock update is
    // processed first.
    public static IReadOnlyList<AdapterMessage> ClockThenEvidence(
        AdapterMessage clock,
        IEnumerable<AdapterMessage> evidence)
    {
        if (clock.PayloadCase != AdapterMessage.PayloadOneofCase.WorldClock)
            throw new ArgumentException("first task batch message must be a world clock update", nameof(clock));

        List<AdapterMessage> result = new() { clock };
        foreach (AdapterMessage message in evidence)
        {
            if (message.PayloadCase != AdapterMessage.PayloadOneofCase.Event || message.Event.TaskEvidence.Count == 0)
                throw new ArgumentException("task batch evidence must be carried by a GameEvent", nameof(evidence));
            result.Add(message);
        }
        return result;
    }
}
