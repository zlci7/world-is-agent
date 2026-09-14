namespace GameAgent.Stardew.Events;

public sealed record InteractCandidate(
    string Name,
    int Left,
    int Top,
    int Right,
    int Bottom,
    float TileX,
    float TileY
);

public static class PlayerInteractTargetSelector
{
    private const float AdjacentTileRadius = 1.25f;

    public static InteractCandidate? Select(
        IEnumerable<InteractCandidate> candidates,
        IEnumerable<string> allowedNames,
        float cursorPixelX,
        float cursorPixelY,
        float grabTileX,
        float grabTileY,
        bool allowAdjacentTile = true
    )
    {
        HashSet<string> allowed = new(allowedNames, StringComparer.Ordinal);
        bool hasAllowList = allowed.Count > 0;

        return candidates
            .Where(candidate => !hasAllowList || allowed.Contains(candidate.Name))
            .Select(candidate => Evaluate(candidate, cursorPixelX, cursorPixelY, grabTileX, grabTileY, allowAdjacentTile))
            .Where(hit => hit is not null)
            .Select(hit => hit!)
            .OrderBy(hit => hit.Rank)
            .ThenBy(hit => hit.DistanceSquared)
            .ThenBy(hit => hit.Candidate.Name, StringComparer.Ordinal)
            .Select(hit => hit.Candidate)
            .FirstOrDefault();
    }

    private static CandidateHit? Evaluate(
        InteractCandidate candidate,
        float cursorPixelX,
        float cursorPixelY,
        float grabTileX,
        float grabTileY,
        bool allowAdjacentTile
    )
    {
        if (Contains(candidate, cursorPixelX, cursorPixelY))
        {
            float centerX = (candidate.Left + candidate.Right) / 2f;
            float centerY = (candidate.Top + candidate.Bottom) / 2f;
            return new CandidateHit(candidate, Rank: 0, DistanceSquared(cursorPixelX, cursorPixelY, centerX, centerY));
        }

        if (allowAdjacentTile)
        {
            float tileDistanceSquared = DistanceSquared(grabTileX, grabTileY, candidate.TileX, candidate.TileY);
            if (tileDistanceSquared <= AdjacentTileRadius * AdjacentTileRadius)
                return new CandidateHit(candidate, Rank: 1, tileDistanceSquared);
        }

        return null;
    }

    private static bool Contains(InteractCandidate candidate, float x, float y)
    {
        return x >= candidate.Left && x < candidate.Right && y >= candidate.Top && y < candidate.Bottom;
    }

    private static float DistanceSquared(float ax, float ay, float bx, float by)
    {
        float dx = ax - bx;
        float dy = ay - by;
        return dx * dx + dy * dy;
    }

    private sealed record CandidateHit(InteractCandidate Candidate, int Rank, float DistanceSquared);
}
