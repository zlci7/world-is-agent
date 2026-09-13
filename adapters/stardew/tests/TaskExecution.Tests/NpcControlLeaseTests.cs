using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class NpcControlLeaseTests
{
    [Fact]
    public void EnforcesOneOwnerAndIgnoresStaleRelease()
    {
        NpcControlLease leases = new();
        OperationKey first = TaskSourceContextStoreTests.Source("operation-a").Operation;
        OperationKey second = TaskSourceContextStoreTests.Source("operation-b").Operation;

        LeaseAttempt acquired = leases.Acquire(first, "travelling");
        LeaseAttempt conflict = leases.Acquire(second, "waiting");

        Assert.True(acquired.Acquired);
        Assert.False(conflict.Acquired);
        Assert.Equal("npc_control_busy", conflict.Code);
        Assert.False(leases.Release(acquired.Token! with { Value = "stale" }, "late"));
        Assert.True(leases.IsOwned(acquired.Token!));
        Assert.True(leases.Release(acquired.Token!, "done"));
    }

    [Fact]
    public void TransfersOnlyWithinTheSameWorldRunAndNpc()
    {
        NpcControlLease leases = new();
        OperationKey travelling = TaskSourceContextStoreTests.Source("travel").Operation;
        LeaseToken token = leases.Acquire(travelling, "travelling").Token!;
        OperationKey waiting = travelling with { OperationId = "wait" };

        LeaseAttempt transfer = leases.Transfer(token, waiting, "waiting");
        LeaseAttempt invalid = leases.Transfer(transfer.Token!, waiting with { WorldRunId = "other" }, "interaction");

        Assert.True(transfer.Acquired);
        Assert.Equal("waiting", transfer.Token!.Mode);
        Assert.False(invalid.Acquired);
        Assert.Equal("lease_transfer_mismatch", invalid.Code);
        Assert.True(leases.IsOwned(transfer.Token));
    }
}
