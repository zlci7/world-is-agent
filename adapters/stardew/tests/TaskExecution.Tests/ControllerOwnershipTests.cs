using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class ControllerOwnershipTests
{
    [Fact]
    public void ExclusiveOwnershipRequiresExpectedMainControllerWithoutTemporaryController()
    {
        object owned = new();

        Assert.True(ControllerOwnership.IsExclusive(owned, owned, null));
        Assert.False(ControllerOwnership.IsExclusive(new object(), owned, null));
        Assert.False(ControllerOwnership.IsExclusive(owned, owned, new object()));
        Assert.False(ControllerOwnership.IsExclusive(null, owned, null));
    }

    [Fact]
    public void OwnedControllerIsDetachedAndHaltedOnce()
    {
        object owned = new();
        int detached = 0;
        int halted = 0;

        bool foreign = ControllerOwnership.Release(owned, owned, null, () => detached++, () => halted++);

        Assert.False(foreign);
        Assert.Equal(1, detached);
        Assert.Equal(1, halted);
    }

    [Fact]
    public void ForeignControllerIsNeverDetachedOrHalted()
    {
        int detached = 0;
        int halted = 0;

        bool foreign = ControllerOwnership.Release(new object(), new object(), null, () => detached++, () => halted++);

        Assert.True(foreign);
        Assert.Equal(0, detached);
        Assert.Equal(0, halted);
    }

    [Fact]
    public void TemporaryControllerPreventsHaltEvenWhenMainControllerIsOwned()
    {
        object owned = new();
        int detached = 0;
        int halted = 0;

        bool foreign = ControllerOwnership.Release(owned, owned, new object(), () => detached++, () => halted++);

        Assert.True(foreign);
        Assert.Equal(1, detached);
        Assert.Equal(0, halted);
    }
}
