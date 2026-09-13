namespace GameAgent.Stardew.Tasks;

public sealed record OperationKey(
    string WorldId,
    string WorldRunId,
    ulong ExecutionGeneration,
    string NpcEntityId,
    string TaskId,
    string OperationId
);

public sealed record TaskOperationSource(
    OperationKey Operation,
    ulong StartRevision,
    string WakeId,
    MeetingAgreement Contract
);

public sealed record DriverResult(
    string Status,
    string Code,
    WorldPosition Position,
    string Message = ""
)
{
    public bool IsTerminal => this.Status != "running";
}

public sealed record TaskExecutionOutcome(DriverResult Result, LeaseToken? Lease, bool Replayed);
