namespace GameAgent.Stardew.Tasks;

public readonly record struct Tile(int X, int Y);

public sealed record AdjacentCandidate(
    Tile Tile,
    bool Passable,
    bool OccupiedByOther,
    IReadOnlyList<Tile>? Path);

public sealed record AdjacentSelection(Tile Target, IReadOnlyList<Tile> Path);

public static class AdjacentTileSelector
{
    public static AdjacentSelection? Select(
        Tile npc,
        Tile player,
        IReadOnlyList<AdjacentCandidate> candidates)
    {
        ArgumentNullException.ThrowIfNull(candidates);
        List<(AdjacentCandidate Candidate, int Length, int Rank)> viable = new();
        foreach (AdjacentCandidate candidate in candidates)
        {
            int rank = DirectionRank(candidate.Tile, player);
            if (!candidate.Passable || candidate.OccupiedByOther || candidate.Path is null || candidate.Path.Count == 0)
                continue;
            if (candidate.Path[0] != npc || candidate.Path[^1] != candidate.Tile)
                throw new ArgumentException("adjacent candidate path must run from NPC to candidate tile", nameof(candidates));
            viable.Add((candidate, candidate.Path.Count - 1, rank));
        }

        if (viable.Count == 0)
            return null;
        AdjacentCandidate selected = viable
            .OrderBy(item => item.Length)
            .ThenBy(item => item.Rank)
            .First().Candidate;
        return new AdjacentSelection(selected.Tile, Array.AsReadOnly(selected.Path!.ToArray()));
    }

    private static int DirectionRank(Tile candidate, Tile player)
    {
        return (candidate.X - player.X, candidate.Y - player.Y) switch
        {
            (0, -1) => 0,
            (1, 0) => 1,
            (0, 1) => 2,
            (-1, 0) => 3,
            _ => throw new ArgumentException("candidate tile must be orthogonally adjacent to the player"),
        };
    }
}
