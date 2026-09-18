using System;

namespace GameAgent.Stardew.Tasks;

public static class GameClock
{
    public const string ClockId = "stardew.game_time.v1";

    public const int MinutesPerDay = 1440;

    /// <summary>
    /// Minute of day a Stardew day starts on. A day runs 06:00 to 26:00, so one date reports minute
    /// values from 360 up to 1560, and the last two hours are already past <see cref="MinutesPerDay"/>.
    /// </summary>
    public const int DayStartMinute = 360;

    public static long ToTick(int year, int seasonIndex, int dayOfMonth, int hhmm)
    {
        if (year < 1)
            throw new ArgumentOutOfRangeException(nameof(year));
        if (seasonIndex is < 0 or > 3)
            throw new ArgumentOutOfRangeException(nameof(seasonIndex));
        if (dayOfMonth is < 1 or > 28)
            throw new ArgumentOutOfRangeException(nameof(dayOfMonth));

        long absoluteDay = checked(((long)year - 1) * 112 + seasonIndex * 28L + dayOfMonth - 1);
        return checked(absoluteDay * 1440 + ToMinute(hhmm));
    }

    /// <summary>
    /// The absolute day index a tick belongs to, inverting <see cref="ToTick"/>.
    /// <para>
    /// Not <c>tick / MinutesPerDay</c>. Because a date spans 06:00 to 26:00, the final two hours of
    /// a day carry minute values above 1440: tick 2970 is 01:30 on absolute day 1, not 02:03 on day
    /// 2. Shifting by the day start recovers the date the tick was built from exactly, for every
    /// time the clock can report. A tick below the day start is not a time this clock produces.
    /// </para>
    /// </summary>
    public static long ToAbsoluteDay(long tick) => (tick - DayStartMinute) / MinutesPerDay;

    public static int ToMinute(int hhmm)
    {
        if (hhmm is < 0 or > 2600)
            throw new ArgumentOutOfRangeException(nameof(hhmm));

        int hour = hhmm / 100;
        int minute = hhmm % 100;
        if (minute is < 0 or > 59 || (hour == 26 && minute != 0))
            throw new ArgumentOutOfRangeException(nameof(hhmm));

        return checked(hour * 60 + minute);
    }

    public static int SeasonIndex(string season)
    {
        return season switch
        {
            "spring" => 0,
            "summer" => 1,
            "fall" => 2,
            "winter" => 3,
            _ => throw new ArgumentException("season must be spring, summer, fall, or winter", nameof(season)),
        };
    }

    public static string ToClockText(long tick)
    {
        if (tick < 0)
            throw new ArgumentOutOfRangeException(nameof(tick));

        int minuteOfDay = (int)(tick % 1440);
        return $"{minuteOfDay / 60:D2}:{minuteOfDay % 60:D2}";
    }
}
