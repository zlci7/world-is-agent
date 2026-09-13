using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class NpcNativeSchedulePolicyTests
{
    [Theory]
    [InlineData(599, null)]
    [InlineData(600, 600)]
    [InlineData(1250, 1200)]
    [InlineData(2400, 2400)]
    [InlineData(2600, 2600)]
    public void SelectsLatestCurrentIntentWithoutReplayingFutureOrOldController(int now, int? expected)
    {
        int? selected = NpcNativeSchedulePolicy.SelectCurrentTime(new[] { 600, 1200, 2400, 2600 }, now);

        Assert.Equal(expected, selected);
    }

    [Fact]
    public void DuplicateOrUnorderedTimesStillProduceOneCurrentIntent()
    {
        int? selected = NpcNativeSchedulePolicy.SelectCurrentTime(new[] { 1800, 600, 1800, 1200 }, 1900);

        Assert.Equal(1800, selected);
    }
}
