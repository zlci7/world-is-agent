namespace GameAgent.Stardew.Runtime;

public enum WorldBindingReplyStatus
{
    Current,
    Duplicate,
    Stale,
}

public sealed class WorldBindingExchange
{
    private readonly object gate = new();
    private string activeRequestId = string.Empty;
    private string completedRequestId = string.Empty;

    public void Begin(string requestId)
    {
        if (string.IsNullOrWhiteSpace(requestId))
            throw new ArgumentException("world binding request_id must not be empty", nameof(requestId));
        lock (this.gate)
            this.activeRequestId = requestId;
    }

    public WorldBindingReplyStatus Classify(string correlationId)
    {
        if (string.IsNullOrWhiteSpace(correlationId))
            return WorldBindingReplyStatus.Stale;
        lock (this.gate)
        {
            if (string.Equals(correlationId, this.activeRequestId, StringComparison.Ordinal))
                return WorldBindingReplyStatus.Current;
            if (string.Equals(correlationId, this.completedRequestId, StringComparison.Ordinal))
                return WorldBindingReplyStatus.Duplicate;
            return WorldBindingReplyStatus.Stale;
        }
    }

    public void Complete(string requestId)
    {
        lock (this.gate)
        {
            if (!string.Equals(requestId, this.activeRequestId, StringComparison.Ordinal))
                return;
            this.completedRequestId = requestId;
            this.activeRequestId = string.Empty;
        }
    }

    public void Cancel(string requestId)
    {
        lock (this.gate)
        {
            if (string.Equals(requestId, this.activeRequestId, StringComparison.Ordinal))
                this.activeRequestId = string.Empty;
        }
    }

    public void Reset()
    {
        lock (this.gate)
        {
            this.activeRequestId = string.Empty;
            this.completedRequestId = string.Empty;
        }
    }
}
