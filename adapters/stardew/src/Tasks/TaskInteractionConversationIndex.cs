namespace GameAgent.Stardew.Tasks;

public sealed class TaskInteractionConversationIndex
{
    private readonly Dictionary<string, string> taskEventByConversation = new(StringComparer.Ordinal);
    private readonly HashSet<string> turnsWithPresentation = new(StringComparer.Ordinal);

    public void Register(string conversationId, string taskEventId)
    {
        this.taskEventByConversation[conversationId] = taskEventId;
    }

    public string? FindTaskEvent(string conversationId)
    {
        return this.taskEventByConversation.TryGetValue(conversationId, out string? eventId) ? eventId : null;
    }

    public void MarkPresentation(string turnEventId)
    {
        this.turnsWithPresentation.Add(turnEventId);
    }

    public bool ShouldReleaseAtTurnCompletion(string turnEventId, string conversationId)
    {
        if (!this.taskEventByConversation.ContainsKey(conversationId))
            return false;
        return !this.turnsWithPresentation.Remove(turnEventId);
    }

    public void RemoveTaskEvent(string taskEventId)
    {
        string[] conversations = this.taskEventByConversation
            .Where(pair => string.Equals(pair.Value, taskEventId, StringComparison.Ordinal))
            .Select(pair => pair.Key)
            .ToArray();
        foreach (string conversationId in conversations)
            this.taskEventByConversation.Remove(conversationId);
    }

    public void RemoveConversation(string conversationId)
    {
        this.taskEventByConversation.Remove(conversationId);
    }

    public void Clear()
    {
        this.taskEventByConversation.Clear();
        this.turnsWithPresentation.Clear();
    }
}
