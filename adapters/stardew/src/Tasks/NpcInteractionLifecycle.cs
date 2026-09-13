namespace GameAgent.Stardew.Tasks;

public interface ITaskInteractionControl
{
    bool PromoteWaitToInteraction(OperationKey operation);
    bool CommitInteraction(OperationKey operation);
    bool ReleaseInteraction(OperationKey operation, string reason);
    bool BeginInteractionApproach(OperationKey interactionOperation, OperationKey approachOperation);
    bool FinishInteractionApproach(OperationKey approachOperation, bool returnToInteraction);
    bool IsHandedOff(string taskId, string operationId);
}

public sealed class NpcInteractionLifecycle
{
    private readonly TaskInteractionHandoffStore handoffs;
    private readonly ITaskInteractionControl control;

    public NpcInteractionLifecycle(TaskInteractionHandoffStore handoffs, ITaskInteractionControl control)
    {
        this.handoffs = handoffs;
        this.control = control;
    }

    public bool TryBegin(string eventId, TaskOperationSource source, out string reason)
    {
        if (!this.handoffs.TryReserve(eventId, source, out reason))
            return false;
        if (this.control.PromoteWaitToInteraction(source.Operation))
            return true;

        this.handoffs.Reject(eventId);
        reason = "interaction_handoff_failed";
        return false;
    }

    public HandoffAckResult Accept(string eventId)
    {
        HandoffAckResult result = this.handoffs.Accept(eventId);
        if (result == HandoffAckResult.Accepted)
        {
            TaskInteractionHandoff handoff = this.handoffs.FindCommitted(eventId)
                ?? throw new InvalidOperationException("accepted handoff is missing");
            if (!this.control.CommitInteraction(handoff.Source.Operation))
            {
                this.handoffs.Complete(eventId);
                this.control.ReleaseInteraction(handoff.Source.Operation, "handoff_commit_failed");
                throw new InvalidOperationException("accepted handoff control could not be committed");
            }
        }
        return result;
    }

    public TaskInteractionHandoff? FindCommitted(string eventId) => this.handoffs.FindCommitted(eventId);
    public TaskInteractionHandoff? Find(string eventId) => this.handoffs.Find(eventId);

    public bool Reject(string eventId, string reason)
    {
        TaskInteractionHandoff? handoff = this.handoffs.Reject(eventId);
        return handoff is not null && this.control.ReleaseInteraction(handoff.Source.Operation, reason);
    }

    public bool Complete(string eventId, string reason)
    {
        TaskInteractionHandoff? handoff = this.handoffs.Complete(eventId);
        return handoff is not null && this.control.ReleaseInteraction(handoff.Source.Operation, reason);
    }

    public int ExpirePending()
    {
        return this.ExpirePendingHandoffs().Count;
    }

    public IReadOnlyList<TaskInteractionHandoff> ExpirePendingHandoffs()
    {
        IReadOnlyList<TaskInteractionHandoff> expired = this.handoffs.ExpirePending();
        foreach (TaskInteractionHandoff handoff in expired)
            this.control.ReleaseInteraction(handoff.Source.Operation, "event_ack_timeout");
        return expired;
    }

    public bool TryBeginApproach(string eventId, OperationKey approachOperation)
    {
        TaskInteractionHandoff? handoff = this.handoffs.FindCommitted(eventId);
        return handoff is not null && this.control.BeginInteractionApproach(handoff.Source.Operation, approachOperation);
    }

    public bool FinishApproach(OperationKey approachOperation, bool returnToInteraction) =>
        this.control.FinishInteractionApproach(approachOperation, returnToInteraction);

    public bool IsHandedOff(string taskId, string operationId) => this.control.IsHandedOff(taskId, operationId);

    public void Clear(string reason)
    {
        foreach (TaskInteractionHandoff handoff in this.handoffs.Drain())
            this.control.ReleaseInteraction(handoff.Source.Operation, reason);
    }
}
