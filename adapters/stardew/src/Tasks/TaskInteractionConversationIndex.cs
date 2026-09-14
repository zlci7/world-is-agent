namespace GameAgent.Stardew.Tasks;

public sealed class TaskInteractionConversationIndex
{
    private readonly Dictionary<string, string> taskEventByConversation = new(StringComparer.Ordinal);
    private readonly Dictionary<string, string> presentationConversationByTurn = new(StringComparer.Ordinal);
    private readonly Dictionary<string, string> deferredConversationByTurn = new(StringComparer.Ordinal);

    public void Register(string conversationId, string taskEventId)
    {
        this.taskEventByConversation[conversationId] = taskEventId;
    }

    public string? FindTaskEvent(string conversationId)
    {
        return this.taskEventByConversation.TryGetValue(conversationId, out string? eventId) ? eventId : null;
    }

    public string? FindPresentationConversation(string turnEventId) =>
        this.presentationConversationByTurn.TryGetValue(turnEventId, out string? conversationId) ||
        this.deferredConversationByTurn.TryGetValue(turnEventId, out conversationId)
            ? conversationId : null;

    public void MarkPresentation(string turnEventId, string conversationId)
    {
        this.presentationConversationByTurn[turnEventId] = conversationId;
    }

    public bool ShouldReleaseAtTurnCompletion(string turnEventId, string conversationId)
    {
        if (!this.taskEventByConversation.ContainsKey(conversationId))
            return false;
        if (this.deferredConversationByTurn.ContainsKey(turnEventId))
            return false;
        if (this.presentationConversationByTurn.Remove(turnEventId))
        {
            this.deferredConversationByTurn[turnEventId] = conversationId;
            return false;
        }
        return true;
    }

    public void RemoveTaskEvent(string taskEventId)
    {
        string[] conversations = this.taskEventByConversation
            .Where(pair => string.Equals(pair.Value, taskEventId, StringComparison.Ordinal))
            .Select(pair => pair.Key)
            .ToArray();
        foreach (string conversationId in conversations)
            this.RemoveConversation(conversationId);
    }

    public void RemoveConversation(string conversationId)
    {
        this.taskEventByConversation.Remove(conversationId);
        RemoveTurnsForConversation(this.presentationConversationByTurn, conversationId);
        RemoveTurnsForConversation(this.deferredConversationByTurn, conversationId);
    }

    public void Clear()
    {
        this.taskEventByConversation.Clear();
        this.presentationConversationByTurn.Clear();
        this.deferredConversationByTurn.Clear();
    }

    private static void RemoveTurnsForConversation(Dictionary<string, string> turns, string conversationId)
    {
        string[] matching = turns
            .Where(pair => string.Equals(pair.Value, conversationId, StringComparison.Ordinal))
            .Select(pair => pair.Key)
            .ToArray();
        foreach (string turnEventId in matching)
            turns.Remove(turnEventId);
    }
}
