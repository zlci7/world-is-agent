namespace GameAgent.Stardew.Runtime;

public static class AgentTargetPolicy
{
    public static string[] SelectNames(IEnumerable<string> availableNames, IEnumerable<string> configuredTargets)
    {
        ArgumentNullException.ThrowIfNull(availableNames);
        ArgumentNullException.ThrowIfNull(configuredTargets);
        HashSet<string> allowed = new(configuredTargets.Where(name => !string.IsNullOrWhiteSpace(name))
            .Select(name => name.Trim()), StringComparer.Ordinal);
        return availableNames.Where(name => !string.IsNullOrWhiteSpace(name))
            .Where(name => allowed.Count == 0 || allowed.Contains(name))
            .Distinct(StringComparer.Ordinal).OrderBy(name => name, StringComparer.Ordinal).ToArray();
    }
}
