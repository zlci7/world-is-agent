namespace GameAgent.Stardew.Tasks;

public static class NpcNativeSchedulePolicy
{
    public static int? SelectCurrentTime(IEnumerable<int> scheduleTimes, int now)
    {
        int? selected = null;
        foreach (int candidate in scheduleTimes)
        {
            if (candidate <= now && (!selected.HasValue || candidate > selected.Value))
                selected = candidate;
        }
        return selected;
    }
}
