using GameAgent.Stardew.Integrations.MailFramework;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class MailDeliveryGuardTests
{
    [Fact]
    public void DeliversInTheSaveThatRegisteredTheLetter()
    {
        Assert.True(MailDeliveryGuard.ShouldDeliver(registeredSaveId: 41UL, currentSaveId: 41UL, alreadyRead: false));
    }

    [Fact]
    public void DoesNotDeliverAgainOnceThePlayerReadIt()
    {
        Assert.False(MailDeliveryGuard.ShouldDeliver(registeredSaveId: 41UL, currentSaveId: 41UL, alreadyRead: true));
    }

    [Fact]
    public void DoesNotDeliverIntoAnotherSave()
    {
        // MailFrameworkMod keeps API-registered letters in a static repository that survives
        // returning to the title screen, so the next loaded save would otherwise receive a letter
        // written for a different one: its mailReceived cannot contain an id it never saw.
        Assert.False(MailDeliveryGuard.ShouldDeliver(registeredSaveId: 41UL, currentSaveId: 42UL, alreadyRead: false));
    }

    [Fact]
    public void DoesNotDeliverIntoAnotherSaveEvenIfTheIdWasSomehowRead()
    {
        Assert.False(MailDeliveryGuard.ShouldDeliver(registeredSaveId: 41UL, currentSaveId: 42UL, alreadyRead: true));
    }
}
