using System;
using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Tasks;

public enum FestivalKnowledge
{
    Unknown,
    NotFestival,
    Festival,
}

public sealed record MeetingDate(int Year, string Season, int DayOfMonth);

public sealed record MeetingRequest(string LandmarkId, MeetingDate TargetDate, int StartTime, int EndTime);

public sealed record MeetingAgreement(
    int SchemaVersion,
    string LandmarkId,
    string ParticipantEntityId,
    MeetingDate TargetDate,
    string ClockId,
    long DepartureAt,
    long StartAt,
    long EndAt
);

public sealed record MeetingResolution(
    bool Accepted,
    string Code,
    string Message,
    MeetingAgreement? Agreement,
    string EquivalenceKey
);

public static class MeetingContract
{
    public const int SchemaVersion = 1;

    public static MeetingResolution Resolve(
        RuntimeWorldSnapshot world,
        string npcEntityId,
        string playerEntityId,
        string originLocation,
        MeetingRequest request,
        FestivalKnowledge festival,
        LandmarkCatalog catalog)
    {
        ArgumentNullException.ThrowIfNull(world);
        ArgumentNullException.ThrowIfNull(request);
        ArgumentNullException.ThrowIfNull(catalog);

        Landmark? landmark = catalog.Find(request.LandmarkId);
        if (landmark is null)
            return Reject("landmark_not_found", "meeting landmark is not configured");
        if (!catalog.SupportsRoute(npcEntityId, originLocation, request.LandmarkId))
            return Reject("route_not_supported", "NPC route has not been verified for this origin and landmark");
        if (festival == FestivalKnowledge.Festival)
            return Reject("festival_day", "meeting date is a festival day");
        if (festival == FestivalKnowledge.Unknown)
            return Reject("festival_unknown", "festival calendar is unavailable for the meeting date");
        if (!ValidAppointmentTime(request.StartTime) || !ValidAppointmentTime(request.EndTime))
            return Reject("invalid_meeting_time", "meeting times must use 10-minute HHMM values between 0600 and 2200");
        if (request.EndTime <= request.StartTime)
            return Reject("invalid_meeting_window", "meeting end must be after start on the target date");
        if (request.StartTime < landmark.OpenStart || request.EndTime > landmark.OpenEnd)
            return Reject("landmark_closed", "meeting window is outside landmark opening hours");

        int seasonIndex;
        long startAt;
        long endAt;
        try
        {
            seasonIndex = GameClock.SeasonIndex(request.TargetDate.Season);
            startAt = GameClock.ToTick(request.TargetDate.Year, seasonIndex, request.TargetDate.DayOfMonth, request.StartTime);
            endAt = GameClock.ToTick(request.TargetDate.Year, seasonIndex, request.TargetDate.DayOfMonth, request.EndTime);
        }
        catch (ArgumentException)
        {
            return Reject("invalid_meeting_date", "meeting target date is invalid");
        }

        long departureAt = checked(startAt - landmark.DepartureLeadMinutes);
        if (departureAt <= world.NowTick)
            return Reject("departure_too_late", "meeting departure time must remain in the future");

        MeetingAgreement agreement = new(
            SchemaVersion,
            request.LandmarkId,
            npcEntityId,
            request.TargetDate,
            world.ClockId,
            departureAt,
            startAt,
            endAt
        );
        string equivalenceKey = string.Join(
            ":",
            "meeting-v1",
            Escape(world.GameId),
            Escape(world.WorldId),
            Escape(npcEntityId),
            Escape(playerEntityId),
            Escape(request.LandmarkId),
            startAt.ToString(System.Globalization.CultureInfo.InvariantCulture),
            endAt.ToString(System.Globalization.CultureInfo.InvariantCulture)
        );
        return new MeetingResolution(true, string.Empty, string.Empty, agreement, equivalenceKey);
    }

    private static bool ValidAppointmentTime(int hhmm)
    {
        if (hhmm is < 600 or > 2200 || hhmm % 10 != 0)
            return false;
        try
        {
            GameClock.ToMinute(hhmm);
            return true;
        }
        catch (ArgumentOutOfRangeException)
        {
            return false;
        }
    }

    private static string Escape(string value)
    {
        return Uri.EscapeDataString(value);
    }

    private static MeetingResolution Reject(string code, string message)
    {
        return new MeetingResolution(false, code, message, null, string.Empty);
    }
}
