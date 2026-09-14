using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class MeetingContractTests
{
    private static readonly RuntimeWorldSnapshot World = new(
        "stardew-valley", "Farm_1", "run-a", 1, GameClock.ClockId,
        GameClock.ToTick(1, 0, 1, 600), 1);

    [Fact]
    public void CreatesAStableCrossDayContractForAVerifiedRoute()
    {
        LandmarkCatalog catalog = LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson);
        MeetingRequest request = new("beach_meeting_spot", new MeetingDate(1, "spring", 2), 1000, 1100);

        MeetingResolution first = MeetingContract.Resolve(World, "npc:Linus", "player:local", "Mountain", request, FestivalKnowledge.NotFestival, catalog);
        MeetingResolution second = MeetingContract.Resolve(World, "npc:Linus", "player:local", "Mountain", request, FestivalKnowledge.NotFestival, catalog);

        Assert.True(first.Accepted);
        Assert.Equal(string.Empty, first.Code);
        MeetingAgreement agreement = Assert.IsType<MeetingAgreement>(first.Agreement);
        Assert.Equal(GameClock.ToTick(1, 0, 2, 600), agreement.DepartureAt);
        Assert.Equal(GameClock.ToTick(1, 0, 2, 1000), agreement.StartAt);
        Assert.Equal(GameClock.ToTick(1, 0, 2, 1100), agreement.EndAt);
        Assert.Equal(first.EquivalenceKey, second.EquivalenceKey);
    }

    [Theory]
    [InlineData(FestivalKnowledge.Festival, "festival_day")]
    [InlineData(FestivalKnowledge.Unknown, "festival_unknown")]
    public void RejectsFestivalAndUnknownFestivalDates(FestivalKnowledge knowledge, string expectedCode)
    {
        MeetingResolution result = Resolve(knowledge: knowledge);
        Assert.False(result.Accepted);
        Assert.Equal(expectedCode, result.Code);
    }

    [Fact]
    public void RejectsRoutesWithoutMatchingNpcAndOriginEvidence()
    {
        MeetingResolution result = Resolve(npc: "npc:Abigail");
        Assert.False(result.Accepted);
        Assert.Equal("route_not_supported", result.Code);
    }

    [Theory]
    [InlineData(955, 1100, "invalid_meeting_time")]
    [InlineData(1000, 1000, "invalid_meeting_window")]
    public void RejectsInvalidWindows(int start, int end, string expectedCode)
    {
        MeetingResolution result = Resolve(start: start, end: end);
        Assert.False(result.Accepted);
        Assert.Equal(expectedCode, result.Code);
    }

    [Fact]
    public void RejectsWindowsOutsideLandmarkOpeningHours()
    {
        string json = LandmarkCatalogTests.ValidCatalogJson.Replace("\"open_start\":600", "\"open_start\":900");
        MeetingResolution result = MeetingContract.Resolve(
            World,
            "npc:Linus",
            "player:local",
            "Mountain",
            new MeetingRequest("beach_meeting_spot", new MeetingDate(1, "spring", 2), 800, 900),
            FestivalKnowledge.NotFestival,
            LandmarkCatalog.Parse(json));

        Assert.False(result.Accepted);
        Assert.Equal("landmark_closed", result.Code);
    }

    [Fact]
    public void RejectsWhenDepartureIsNoLongerInTheFuture()
    {
        RuntimeWorldSnapshot late = World with { NowTick = GameClock.ToTick(1, 0, 2, 600) };
        MeetingResolution result = MeetingContract.Resolve(
            late,
            "npc:Linus",
            "player:local",
            "Mountain",
            new MeetingRequest("beach_meeting_spot", new MeetingDate(1, "spring", 2), 1000, 1100),
            FestivalKnowledge.NotFestival,
            LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));

        Assert.False(result.Accepted);
        Assert.Equal("departure_too_late", result.Code);
    }

    [Theory]
    [InlineData(1, "spring", 2, 240, 800, false)]
    [InlineData(1, "spring", 2, 240, 1000, true)]
    [InlineData(1, "spring", 2, 60, 700, true)]
    [InlineData(1, "summer", 1, 240, 800, false)]
    [InlineData(2, "spring", 1, 480, 600, false)]
    public void DepartureMustFitTheTargetDaysExecutionWindow(int year, string season, int day, int lead, int start, bool accepted)
    {
        string json = LandmarkCatalogTests.ValidCatalogJson.Replace("\"departure_lead_minutes\":240", $"\"departure_lead_minutes\":{lead}");
        MeetingResolution result = MeetingContract.Resolve(World, "npc:Linus", "player:local", "Mountain",
            new MeetingRequest("beach_meeting_spot", new MeetingDate(year, season, day), start, start + 100),
            FestivalKnowledge.NotFestival, LandmarkCatalog.Parse(json));

        Assert.Equal(accepted, result.Accepted);
        if (accepted)
            Assert.Equal(GameClock.ToTick(year, GameClock.SeasonIndex(season), day, 600), result.Agreement!.DepartureAt);
        else
        {
            Assert.Equal("departure_outside_execution_window", result.Code);
            Assert.Null(result.Agreement);
        }
    }

    private static MeetingResolution Resolve(
        FestivalKnowledge knowledge = FestivalKnowledge.NotFestival,
        string npc = "npc:Linus",
        int start = 1000,
        int end = 1100)
    {
        return MeetingContract.Resolve(
            World,
            npc,
            "player:local",
            "Mountain",
            new MeetingRequest("beach_meeting_spot", new MeetingDate(1, "spring", 2), start, end),
            knowledge,
            LandmarkCatalog.Parse(LandmarkCatalogTests.ValidCatalogJson));
    }
}
