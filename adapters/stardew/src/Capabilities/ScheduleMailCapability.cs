using GameAgent.Stardew.Integrations.MailFramework;
using GameAgent.Stardew.Tasks;

namespace GameAgent.Stardew.Capabilities;

/// <summary>How schedule_mail ended, which decides the ActionResult status the caller sends.</summary>
internal enum ScheduleMailDisposition
{
    Succeeded,
    Rejected,
}

/// <summary>The game-clock window the planned letter is delivered in. The wake moment and the deadline are
/// both adapter-owned: the model never sees or supplies a tick.
/// </summary>
public readonly record struct ScheduleMailWindow(long WakeAt, long DeadlineAt);

/// <summary>
/// The Adapter-owned payload contract for a planned letter. The Runtime stores it opaquely and
/// publishes it back verbatim in the wake turn's Task Context, so it is versioned here rather than
/// implied by the field names.
/// </summary>
internal static class ScheduleMailContract
{
    public const int SchemaVersion = 1;
    public const string IntentField = "intent";
}

/// <summary>Outcome of one schedule_mail call.</summary>
internal readonly record struct ScheduleMailOutcome(
    ScheduleMailDisposition Disposition,
    string Code,
    string Message,
    string Intent,
    ScheduleMailWindow Window)
{
    public static ScheduleMailOutcome Succeeded(string intent, ScheduleMailWindow window) =>
        new(ScheduleMailDisposition.Succeeded, string.Empty, string.Empty, intent, window);

    public static ScheduleMailOutcome Rejected(string code, string message) =>
        new(ScheduleMailDisposition.Rejected, code, message, string.Empty, default);
}

/// <summary>
/// The schedule_mail decision. It never sends anything and never touches the mail repository: it
/// decides whether a deferred letter may be planned, and turns "tomorrow morning" into the game
/// tick window the Runtime schedules a task on.
/// <para>
/// Deliberately separate from send_mail. send_mail means "put this letter in the mailbox now";
/// this means "the NPC intends to write tomorrow". Merging them into one capability with an
/// optional send_at would make one tool carry both an environment action and a plan, and the
/// model would have to compute a tick to use it.
/// </para>
/// <para>
/// The gate is deterministic and reads game facts only. The Adapter cannot see Runtime tasks, so
/// "one planned letter per NPC per in-game day" is the rule it can actually enforce; the binding
/// one-active-task-per-owner limit is the Runtime's own admission check, not this gate's.
/// </para>
/// </summary>
internal static class ScheduleMailCapability
{
    public const int MaxIntentLength = 512;

    /// <summary>
    /// Slack after the wake moment, in in-game days. The wake action is only admitted while the
    /// clock is still inside the deadline, and sleeping advances the clock by a whole day at a
    /// time, so the window has to survive a day the player never actually played.
    /// </summary>
    public const int WindowSlackDays = 2;

    public const string IntentInvalidCode = "schedule_mail_intent_invalid";
    public const string AlreadyScheduledCode = "schedule_mail_already_scheduled";
    public const string WindowUnavailableCode = "schedule_mail_window_unavailable";

    private const long MaxAbsoluteDay = (long.MaxValue - 3L * GameClock.MinutesPerDay) / GameClock.MinutesPerDay;

    /// <summary>
    /// The start of the next in-game day, and a deadline far enough out to still be inside it after
    /// a skipped day. Always strictly after <paramref name="nowTick"/>: the latest a turn can happen
    /// on a date is 26:00, which is still before that date's following morning.
    /// </summary>
    public static bool TryNextMorning(long nowTick, out ScheduleMailWindow window)
    {
        window = default;

        // A current game time is never before 06:00, so a tick below the day start is not a time
        // this clock can report and its day index would be meaningless.
        if (nowTick < GameClock.DayStartMinute)
            return false;

        long day = GameClock.ToAbsoluteDay(nowTick);
        if (day > MaxAbsoluteDay)
            return false;

        long wakeAt = (day + 1) * GameClock.MinutesPerDay + GameClock.DayStartMinute;
        long deadlineAt = wakeAt + (long)WindowSlackDays * GameClock.MinutesPerDay;
        if (wakeAt <= nowTick || deadlineAt < wakeAt)
            return false;

        window = new ScheduleMailWindow(wakeAt, deadlineAt);
        return true;
    }

    /// <summary>
    /// Decide one schedule_mail call.
    /// </summary>
    /// <param name="mailAvailable">
    /// False when MFM was not resolved. The capability is not published in that case, so this is
    /// the defensive branch that keeps the two mail capabilities from drifting apart: a planned
    /// letter with no way to deliver it would fail at the deadline instead of here.
    /// </param>
    /// <param name="intent">
    /// What the NPC intends to tell the player. It is not letter text and never reaches the game:
    /// the wake turn reads it back and writes the letter then, which is the whole point of the
    /// exercise.
    /// </param>
    /// <param name="lastScheduledDay">
    /// The in-game day this NPC last planned a letter on, if any.
    /// </param>
    public static ScheduleMailOutcome Decide(bool mailAvailable, string? intent, long nowTick, long? lastScheduledDay)
    {
        if (!mailAvailable)
            return ScheduleMailOutcome.Rejected(MailDeliveryCodes.Unavailable, "Mail Framework Mod is not available");

        string normalized = intent?.Trim() ?? string.Empty;
        if (normalized.Length == 0 || normalized.Length > MaxIntentLength)
            return ScheduleMailOutcome.Rejected(
                IntentInvalidCode,
                $"intent must be 1 to {MaxIntentLength} characters");

        // The gate runs before the window so a repeated call is rejected for the reason that
        // actually applies, rather than looking like a clock problem.
        if (lastScheduledDay is long scheduledDay && scheduledDay == GameClock.ToAbsoluteDay(nowTick))
            return ScheduleMailOutcome.Rejected(
                AlreadyScheduledCode,
                "this NPC already planned a letter for tomorrow during this in-game day");

        if (!TryNextMorning(nowTick, out ScheduleMailWindow window))
            return ScheduleMailOutcome.Rejected(WindowUnavailableCode, "the game clock is not usable for scheduling");

        return ScheduleMailOutcome.Succeeded(normalized, window);
    }
}
