using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Capabilities;
using GameAgent.Stardew.Integrations.MailFramework;
using GameAgent.Stardew.Runtime;
using GameAgent.Stardew.Tasks;
using Google.Protobuf.WellKnownTypes;
using Xunit;

namespace GameAgent.Stardew.Tests;

/// <summary>
/// Durable deferred delivery: schedule_mail plans a letter for the next in-game morning. The
/// Adapter owns the window and the gate; the model supplies only what it means to say.
/// </summary>
public sealed class ScheduleMailTests
{
    private const string ClockId = "stardew.game_time.v1";

    [Fact]
    public void NextMorningIsTheFollowingDayAtSix()
    {
        long now = GameClock.ToTick(1, 0, 2, 900);

        Assert.True(ScheduleMailCapability.TryNextMorning(now, out ScheduleMailWindow window));

        Assert.Equal(GameClock.ToTick(1, 0, 3, 600), window.WakeAt);
        Assert.Equal(window.WakeAt + (long)ScheduleMailCapability.WindowSlackDays * GameClock.MinutesPerDay, window.DeadlineAt);
    }

    [Fact]
    public void NextMorningIsAlwaysStrictlyInTheFuture()
    {
        // A Stardew day runs 06:00 to 26:00, so the window must stay ahead of the clock at every
        // minute a turn can happen, on any day, including one that rolls the season or year over.
        foreach (long day in new long[] { 0, 1, 27, 28, 111, 112, 5000 })
        {
            foreach (int minute in new[] { 360, 361, 720, 1200, 1530, 1559, 1560 })
            {
                long now = day * GameClock.MinutesPerDay + minute;

                Assert.True(ScheduleMailCapability.TryNextMorning(now, out ScheduleMailWindow window));
                Assert.True(window.WakeAt > now, $"wake {window.WakeAt} must be after {now}");
                Assert.True(window.DeadlineAt >= window.WakeAt);
            }
        }
    }

    [Fact]
    public void LateNightStillTargetsTheNextMorning()
    {
        // 01:30 in-game belongs to the same date, so the next morning is only hours away and is
        // still tomorrow rather than "later today".
        long now = GameClock.ToTick(1, 0, 2, 2530);

        Assert.True(ScheduleMailCapability.TryNextMorning(now, out ScheduleMailWindow window));

        Assert.Equal(GameClock.ToTick(1, 0, 3, 600), window.WakeAt);
        Assert.True(window.WakeAt - now < GameClock.MinutesPerDay);
    }

    [Fact]
    public void AnUnusableClockIsRejected()
    {
        Assert.False(ScheduleMailCapability.TryNextMorning(-1, out _));

        ScheduleMailOutcome outcome = ScheduleMailCapability.Decide(mailAvailable: true, "tell them later", -1, null);

        Assert.Equal(ScheduleMailDisposition.Rejected, outcome.Disposition);
        Assert.Equal(ScheduleMailCapability.WindowUnavailableCode, outcome.Code);
    }

    [Fact]
    public void PlanningWithoutMailFrameworkIsRejected()
    {
        ScheduleMailOutcome outcome = ScheduleMailCapability.Decide(mailAvailable: false, "tell them later", 5000, null);

        Assert.Equal(ScheduleMailDisposition.Rejected, outcome.Disposition);
        Assert.Equal(MailDeliveryCodes.Unavailable, outcome.Code);
    }

    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData("   ")]
    public void AnEmptyIntentIsRejected(string? intent)
    {
        ScheduleMailOutcome outcome = ScheduleMailCapability.Decide(mailAvailable: true, intent, 5000, null);

        Assert.Equal(ScheduleMailDisposition.Rejected, outcome.Disposition);
        Assert.Equal(ScheduleMailCapability.IntentInvalidCode, outcome.Code);
    }

    [Fact]
    public void AnOversizedIntentIsRejectedAndTheLimitIsTheBoundary()
    {
        string tooLong = new('x', ScheduleMailCapability.MaxIntentLength + 1);
        ScheduleMailOutcome rejected = ScheduleMailCapability.Decide(mailAvailable: true, tooLong, 5000, null);
        Assert.Equal(ScheduleMailDisposition.Rejected, rejected.Disposition);
        Assert.Equal(ScheduleMailCapability.IntentInvalidCode, rejected.Code);

        string atLimit = new('x', ScheduleMailCapability.MaxIntentLength);
        Assert.Equal(ScheduleMailDisposition.Succeeded, ScheduleMailCapability.Decide(mailAvailable: true, atLimit, 5000, null).Disposition);
    }

    [Fact]
    public void TheIntentIsTrimmed()
    {
        ScheduleMailOutcome outcome = ScheduleMailCapability.Decide(mailAvailable: true, "  tell them later  ", 5000, null);

        Assert.Equal(ScheduleMailDisposition.Succeeded, outcome.Disposition);
        Assert.Equal("tell them later", outcome.Intent);
    }

    [Fact]
    public void OnePlannedLetterPerNpcPerInGameDay()
    {
        long now = GameClock.ToTick(1, 0, 2, 900);
        long today = GameClock.ToAbsoluteDay(now);

        ScheduleMailOutcome repeated = ScheduleMailCapability.Decide(mailAvailable: true, "again", now, today);

        Assert.Equal(ScheduleMailDisposition.Rejected, repeated.Disposition);
        Assert.Equal(ScheduleMailCapability.AlreadyScheduledCode, repeated.Code);
    }

    [Fact]
    public void YesterdayPlanDoesNotBlockToday()
    {
        long now = GameClock.ToTick(1, 0, 2, 900);
        long yesterday = GameClock.ToAbsoluteDay(now) - 1;

        ScheduleMailOutcome outcome = ScheduleMailCapability.Decide(mailAvailable: true, "the follow-up", now, yesterday);

        Assert.Equal(ScheduleMailDisposition.Succeeded, outcome.Disposition);
    }

    [Fact]
    public void ScheduleMailIsAbsentFromTheCatalogWithoutMailFramework()
    {
        CapabilityList capabilities = CapabilityCatalog.BuildEnvironmentCapabilities();

        Assert.DoesNotContain(capabilities.Capabilities, capability => capability.Name == "schedule_mail");
    }

    [Fact]
    public void ScheduleMailIsPublishedExactlyWhenSendMailIs()
    {
        // Planning a letter this environment cannot deliver would create a durable task whose
        // only consumer capability is missing.
        CapabilityList capabilities = CapabilityCatalog.BuildEnvironmentCapabilities(includeMailCapability: true);

        Capability schedule = Assert.Single(capabilities.Capabilities, capability => capability.Name == "schedule_mail");
        Assert.Single(capabilities.Capabilities, capability => capability.Name == "send_mail");
        Assert.Equal(ExecutionMode.Sync, schedule.ExecutionMode);
        Assert.Equal(CapabilityConcurrencyMode.Sequential, schedule.ConcurrencyMode);
        Assert.Contains($"\"maxLength\":{ScheduleMailCapability.MaxIntentLength}", schedule.InputSchemaJson);
        Assert.Contains("\"required\":[\"intent\"]", schedule.InputSchemaJson);
        // The model has to know that the plan is only real once create_task succeeds.
        Assert.Contains("create_task", schedule.Description);
    }

    [Fact]
    public void ParsesTheScheduleMailIntent()
    {
        string intent = ProtocolMapper.RequireScheduleMailIntent(CreateScheduleMailRequest("I owe them an answer"));

        Assert.Equal("I owe them an answer", intent);
    }

    [Fact]
    public void MissingIntentIsRejectedAsAMalformedCall()
    {
        ActionRequest request = CreateScheduleMailRequest("anything");
        request.Arguments.Fields.Remove("intent");

        TestSupport.ExpectArgumentException(() => ProtocolMapper.RequireScheduleMailIntent(request), "intent");
    }

    [Fact]
    public void NonStringIntentIsRejectedAsAMalformedCall()
    {
        ActionRequest request = CreateScheduleMailRequest("anything");
        request.Arguments.Fields["intent"] = Value.ForNumber(3);

        TestSupport.ExpectArgumentException(() => ProtocolMapper.RequireScheduleMailIntent(request), "string");
    }

    [Fact]
    public void TheResultCarriesTheWindowTheIntentAndBothParticipants()
    {
        long now = GameClock.ToTick(1, 0, 2, 900);
        RuntimeWorldSnapshot world = new("stardew-valley", "Farm_1", "run-a", 1, ClockId, now, 4);
        long wakeAt = GameClock.ToTick(1, 0, 3, 600);
        ActionRequest request = CreateScheduleMailRequest("tell them later");

        ActionResult result = ProtocolMapper.BuildScheduleMailResult(
            request,
            world,
            "tell them later",
            new ScheduleMailWindow(wakeAt, wakeAt + 1440),
            "npc:Abigail",
            ProtocolMapper.PlayerEntityId);

        Assert.Equal(ActionStatus.Succeeded, result.Status);
        Assert.Equal(request.ActionId, result.ActionId);

        TaskProposal proposal = Assert.IsType<TaskProposal>(result.TaskProposal);
        Assert.Equal(wakeAt, proposal.WakeAt);
        Assert.Equal(wakeAt + 1440, proposal.DeadlineAt);
        Assert.Equal(ClockId, proposal.Clock.ClockId);
        Assert.Equal(now, proposal.Clock.NowTick);
        Assert.Equal(4UL, proposal.Clock.Sequence);
        Assert.Equal("tell them later", proposal.Payload.Fields[ScheduleMailContract.IntentField].StringValue);
        Assert.Equal(ScheduleMailContract.SchemaVersion, (int)proposal.Payload.Fields["schema_version"].NumberValue);
        Assert.Equal(new[] { "npc:Abigail", ProtocolMapper.PlayerEntityId }, proposal.ParticipantEntityIds);
    }

    private static ActionRequest CreateScheduleMailRequest(string intent) => new()
    {
        ActionId = "act_schedule",
        EntityId = "npc:Abigail",
        WorldId = "Farm_1",
        Capability = "schedule_mail",
        SourceEventId = "event_schedule_1",
        SourceTurnId = "turn_schedule",
        Arguments = new Struct
        {
            Fields =
            {
                ["intent"] = Value.ForString(intent),
            },
        },
    };
}
