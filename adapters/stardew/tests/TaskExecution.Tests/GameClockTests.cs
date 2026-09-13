using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class GameClockTests
{
    [Theory]
    [InlineData(600, 360)]
    [InlineData(2350, 1430)]
    [InlineData(2400, 1440)]
    [InlineData(2600, 1560)]
    public void ConvertsValidCurrentTimesToMinutes(int hhmm, int expected)
    {
        Assert.Equal(expected, GameClock.ToMinute(hhmm));
    }

    [Theory]
    [InlineData(-10)]
    [InlineData(1260)]
    [InlineData(2610)]
    public void RejectsInvalidCurrentTimes(int hhmm)
    {
        Assert.Throws<ArgumentOutOfRangeException>(() => GameClock.ToMinute(hhmm));
    }

    [Fact]
    public void ConvertsCalendarCoordinatesToAnAbsoluteTick()
    {
        Assert.Equal(360, GameClock.ToTick(1, 0, 1, 600));
        Assert.Equal(1800, GameClock.ToTick(1, 0, 2, 600));
        Assert.Equal(161640, GameClock.ToTick(2, 0, 1, 600));
    }

    [Theory]
    [InlineData(0, 0, 1)]
    [InlineData(1, -1, 1)]
    [InlineData(1, 4, 1)]
    [InlineData(1, 0, 0)]
    [InlineData(1, 0, 29)]
    public void RejectsInvalidCalendarCoordinates(int year, int season, int day)
    {
        Assert.Throws<ArgumentOutOfRangeException>(() => GameClock.ToTick(year, season, day, 600));
    }

    [Theory]
    [InlineData("spring", 0)]
    [InlineData("summer", 1)]
    [InlineData("fall", 2)]
    [InlineData("winter", 3)]
    public void MapsCanonicalSeasonNames(string season, int expected)
    {
        Assert.Equal(expected, GameClock.SeasonIndex(season));
    }

    [Fact]
    public void RejectsUnknownSeasonNames()
    {
        Assert.Throws<ArgumentException>(() => GameClock.SeasonIndex("autumn"));
    }
}
