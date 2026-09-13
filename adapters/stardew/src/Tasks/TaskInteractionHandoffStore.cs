namespace GameAgent.Stardew.Tasks;

public enum TaskInteractionHandoffState
{
    Pending,
    Committed,
}

public enum HandoffAckResult
{
    Missing,
    Accepted,
    Duplicate,
}

public sealed record TaskInteractionHandoff(
    string EventId,
    TaskOperationSource Source,
    TaskInteractionHandoffState State,
    long CreatedAtMilliseconds);

public sealed class TaskInteractionHandoffStore
{
    public const long AckTimeoutMilliseconds = 30_000;

    private readonly Func<long> elapsedMilliseconds;
    private readonly Dictionary<string, TaskInteractionHandoff> byEvent = new(StringComparer.Ordinal);
    private readonly Dictionary<OperationKey, string> byOperation = new();

    public TaskInteractionHandoffStore(Func<long>? elapsedMilliseconds = null)
    {
        this.elapsedMilliseconds = elapsedMilliseconds ?? (() => Environment.TickCount64);
    }

    public bool TryReserve(string eventId, TaskOperationSource source, out string reason)
    {
        if (this.byEvent.TryGetValue(eventId, out TaskInteractionHandoff? existing))
        {
            reason = existing.Source == source ? string.Empty : "handoff_event_conflict";
            return existing.Source == source;
        }
        if (this.byOperation.ContainsKey(source.Operation))
        {
            reason = "handoff_operation_conflict";
            return false;
        }

        TaskInteractionHandoff handoff = new(eventId, source, TaskInteractionHandoffState.Pending, this.elapsedMilliseconds());
        this.byEvent.Add(eventId, handoff);
        this.byOperation.Add(source.Operation, eventId);
        reason = string.Empty;
        return true;
    }

    public HandoffAckResult Accept(string eventId)
    {
        if (!this.byEvent.TryGetValue(eventId, out TaskInteractionHandoff? handoff))
            return HandoffAckResult.Missing;
        if (handoff.State == TaskInteractionHandoffState.Committed)
            return HandoffAckResult.Duplicate;
        this.byEvent[eventId] = handoff with { State = TaskInteractionHandoffState.Committed };
        return HandoffAckResult.Accepted;
    }

    public TaskInteractionHandoff? FindCommitted(string eventId)
    {
        return this.byEvent.TryGetValue(eventId, out TaskInteractionHandoff? handoff) &&
            handoff.State == TaskInteractionHandoffState.Committed
            ? handoff
            : null;
    }

    public TaskInteractionHandoff? Find(string eventId) =>
        this.byEvent.TryGetValue(eventId, out TaskInteractionHandoff? handoff) ? handoff : null;

    public TaskInteractionHandoff? Find(string taskId, string operationId) =>
        this.byEvent.Values.SingleOrDefault(handoff =>
            string.Equals(handoff.Source.Operation.TaskId, taskId, StringComparison.Ordinal) &&
            string.Equals(handoff.Source.Operation.OperationId, operationId, StringComparison.Ordinal));

    public TaskInteractionHandoff? Reject(string eventId)
    {
        if (!this.byEvent.TryGetValue(eventId, out TaskInteractionHandoff? handoff) ||
            handoff.State != TaskInteractionHandoffState.Pending)
        {
            return null;
        }
        return this.Remove(eventId, committedOnly: false);
    }

    public TaskInteractionHandoff? Complete(string eventId) => this.Remove(eventId, committedOnly: true);

    public IReadOnlyList<TaskInteractionHandoff> ExpirePending()
    {
        string[] expired = this.byEvent.Values
            .Where(item => item.State == TaskInteractionHandoffState.Pending &&
                this.elapsedMilliseconds() - item.CreatedAtMilliseconds >= AckTimeoutMilliseconds)
            .Select(item => item.EventId)
            .ToArray();
        List<TaskInteractionHandoff> result = new(expired.Length);
        foreach (string eventId in expired)
        {
            TaskInteractionHandoff? removed = this.Remove(eventId, committedOnly: false);
            if (removed is not null)
                result.Add(removed);
        }
        return result;
    }

    public IReadOnlyList<TaskInteractionHandoff> Drain()
    {
        TaskInteractionHandoff[] result = this.byEvent.Values.ToArray();
        this.byEvent.Clear();
        this.byOperation.Clear();
        return result;
    }

    private TaskInteractionHandoff? Remove(string eventId, bool committedOnly)
    {
        if (!this.byEvent.TryGetValue(eventId, out TaskInteractionHandoff? handoff) ||
            (committedOnly && handoff.State != TaskInteractionHandoffState.Committed))
        {
            return null;
        }
        this.byEvent.Remove(eventId);
        this.byOperation.Remove(handoff.Source.Operation);
        return handoff;
    }
}
