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

    [Theory]
    [InlineData(1, 0, 1, 600, "06:00")]
    [InlineData(1, 0, 2, 840, "08:40")]
    [InlineData(1, 0, 2, 2030, "20:30")]
    [InlineData(1, 0, 2, 2350, "23:50")]
    public void FormatsAbsoluteTicksAsClockText(int year, int season, int day, int hhmm, string expected)
    {
        Assert.Equal(expected, GameClock.ToClockText(GameClock.ToTick(year, season, day, hhmm)));
    }

    [Fact]
    public void RejectsNegativeTicksWhenFormatting()
    {
        Assert.Throws<ArgumentOutOfRangeException>(() => GameClock.ToClockText(-1));
    }

    [Theory]
    [InlineData(1, 0, 1, 600)]
    [InlineData(1, 0, 1, 2350)]
    [InlineData(1, 0, 2, 600)]
    [InlineData(1, 2, 28, 2400)]
    [InlineData(1, 3, 28, 2600)]
    [InlineData(3, 1, 14, 1200)]
    public void AbsoluteDayInvertsTheTickEncoding(int year, int season, int day, int hhmm)
    {
        long tick = GameClock.ToTick(year, season, day, hhmm);

        Assert.Equal(((long)year - 1) * 112 + season * 28L + day - 1, GameClock.ToAbsoluteDay(tick));
    }

    [Fact]
    public void AbsoluteDayDoesNotRollOverAfterMidnight()
    {
        // The trap: a date runs 06:00 to 26:00, so 01:30 already carries a minute value above
        // MinutesPerDay. Dividing the tick by the day length would report the following date.
        long lateNight = GameClock.ToTick(1, 0, 2, 2530);
        long nextMorning = GameClock.ToTick(1, 0, 3, 600);

        Assert.Equal(1, GameClock.ToAbsoluteDay(lateNight));
        Assert.NotEqual(lateNight / GameClock.MinutesPerDay, GameClock.ToAbsoluteDay(lateNight));
        Assert.Equal(2, GameClock.ToAbsoluteDay(nextMorning));
        Assert.Equal(GameClock.ToAbsoluteDay(lateNight) + 1, GameClock.ToAbsoluteDay(nextMorning));
    }
}
