using System;

namespace GameAgent.Stardew.Tasks;

public static class GameClock
{
    public const string ClockId = "stardew.game_time.v1";

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
