using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class AdjacentTileSelectorTests
{
    [Fact]
    public void ChoosesShortestPathAndUsesUpRightDownLeftTieBreak()
    {
        Tile npc = new(5, 5);
        Tile player = new(10, 10);
        AdjacentCandidate[] candidates =
        {
            Candidate(npc, new Tile(10, 11), 3),
            Candidate(npc, new Tile(9, 10), 3),
            Candidate(npc, new Tile(11, 10), 2),
            Candidate(npc, new Tile(10, 9), 2),
        };

        AdjacentSelection? selected = AdjacentTileSelector.Select(npc, player, candidates);

        Assert.Equal(new Tile(10, 9), selected!.Target);
        Assert.Equal(2, selected.Path.Count - 1);
    }

    [Fact]
    public void AlreadyAtLegalAdjacentTileWinsWithZeroLengthPath()
    {
        Tile npc = new(10, 9);
        Tile player = new(10, 10);

        AdjacentSelection? selected = AdjacentTileSelector.Select(npc, player, new[]
        {
            new AdjacentCandidate(npc, true, false, new[] { npc }),
            Candidate(npc, new Tile(11, 10), 1),
        });

        Assert.Equal(npc, selected!.Target);
        Assert.Single(selected.Path);
    }

    [Fact]
    public void ExcludesBlockedOccupiedAndUnreachableCandidates()
    {
        Tile npc = new(5, 5);
        Tile player = new(10, 10);

        AdjacentSelection? selected = AdjacentTileSelector.Select(npc, player, new[]
        {
            new AdjacentCandidate(new Tile(10, 9), false, false, new[] { npc, new Tile(10, 9) }),
            new AdjacentCandidate(new Tile(11, 10), true, true, new[] { npc, new Tile(11, 10) }),
            new AdjacentCandidate(new Tile(10, 11), true, false, null),
            new AdjacentCandidate(new Tile(9, 10), true, false, Array.Empty<Tile>()),
        });

        Assert.Null(selected);
    }

    [Fact]
    public void RejectsCandidateThatIsNotAdjacentToPlayer()
    {
        Assert.Throws<ArgumentException>(() => AdjacentTileSelector.Select(
            new Tile(1, 1),
            new Tile(10, 10),
            new[] { Candidate(new Tile(1, 1), new Tile(12, 10), 2) }));
    }

    [Fact]
    public void ReturnsAPathCopyThatCallerCannotMutate()
    {
        Tile npc = new(1, 1);
        Tile player = new(2, 2);
        List<Tile> path = new() { npc, new Tile(2, 1) };

        AdjacentSelection selected = AdjacentTileSelector.Select(
            npc,
            player,
            new[] { new AdjacentCandidate(new Tile(2, 1), true, false, path) })!;
        path[1] = new Tile(99, 99);

        Assert.Equal(new Tile(2, 1), selected.Path[1]);
    }

    private static AdjacentCandidate Candidate(Tile npc, Tile target, int length)
    {
        Tile[] path = Enumerable.Repeat(npc, length + 1).ToArray();
        path[^1] = target;
        return new AdjacentCandidate(target, true, false, path);
    }
}
