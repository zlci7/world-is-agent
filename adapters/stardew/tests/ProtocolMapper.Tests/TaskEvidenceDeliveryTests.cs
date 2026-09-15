using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class TaskEvidenceDeliveryTests
{
    private static readonly RuntimeWorldSnapshot World = new("stardew-valley", "Farm_1", "run-a", 3, "clock", 900, 5);
    private static readonly MeetingAgreement Agreement = new(
        1, "community_center", "npc:Linus", new MeetingDate(1, "spring", 5), "clock", 700, 800, 900);

    private static WaitEvidence Evidence(ulong generation) => new(
        new TaskOperationSource(
            new OperationKey("Farm_1", "run-a", generation, "npc:Linus", "task_1", "operation_1"),
            4,
            "wake_1",
            Agreement
        ),
        900,
        "unsatisfied",
        "expired",
        new WorldPosition("Town", 55, 22)
    );

    [Fact]
    public void FactKeepsTheOperationBindingAfterASave()
    {
        TaskEvidence fact = ProtocolMapper.BuildWaitEvidenceFact(Evidence(1), World);

        Assert.Equal(1UL, fact.Scope.ExecutionGeneration);
        Assert.Equal("run-a", fact.Scope.WorldRunId);
        Assert.Equal("task_1:operation_1:expired", fact.FactId);
        Assert.Equal("unsatisfied", fact.Outcome);
        Assert.Equal(900, fact.OccurredAt);
        Assert.Equal(4UL, fact.StartRevision);
    }

    [Fact]
    public void FactFromANewerGenerationOrAnotherRunIsRejected()
    {
        Assert.Throws<System.ArgumentException>(() => ProtocolMapper.BuildWaitEvidenceFact(Evidence(4), World));
        Assert.Throws<System.ArgumentException>(() => ProtocolMapper.BuildWaitEvidenceFact(Evidence(1), World with { WorldRunId = "run-b" }));
    }

    [Fact]
    public void InboxKeepsFactsPerEntityUntilAnObservationTakesThem()
    {
        TaskEvidenceInbox inbox = new();
        inbox.Add("npc:Linus", ProtocolMapper.BuildWaitEvidenceFact(Evidence(1), World));
        inbox.Add("npc:Linus", ProtocolMapper.BuildWaitEvidenceFact(Evidence(1), World));
        inbox.Add("npc:Abigail", ProtocolMapper.BuildWaitEvidenceFact(Evidence(1), World));

        IReadOnlyList<TaskEvidence> linus = inbox.Take("npc:Linus");

        Assert.Equal(2, linus.Count);
        Assert.Empty(inbox.Take("npc:Linus"));
        Assert.Single(inbox.Take("npc:Abigail"));
        inbox.Add("npc:Linus", ProtocolMapper.BuildWaitEvidenceFact(Evidence(1), World));
        inbox.Clear();
        Assert.Empty(inbox.Take("npc:Linus"));
        Assert.Equal(0, inbox.Count);
    }
}
