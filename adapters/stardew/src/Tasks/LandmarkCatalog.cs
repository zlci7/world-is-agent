using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace GameAgent.Stardew.Tasks;

public sealed record WorldPosition(string Location, int X, int Y);

public sealed record SupportedRoute(string NpcId, string OriginLocation);

public sealed record Landmark(
    string LandmarkId,
    string DisplayName,
    WorldPosition Position,
    int OpenStart,
    int OpenEnd,
    int DepartureLeadMinutes,
    IReadOnlyList<SupportedRoute> SupportedRoutes
);

public sealed class LandmarkCatalogException : Exception
{
    public LandmarkCatalogException(string code, string message) : base(message)
    {
        this.Code = code;
    }

    public string Code { get; }
}

public sealed class LandmarkCatalog
{
    // A route field set to this value matches every NPC or every origin location.
    public const string AnyRoute = "*";

    private static readonly JsonSerializerOptions WriteOptions = new() { WriteIndented = true };

    private readonly IReadOnlyDictionary<string, Landmark> landmarks;

    private LandmarkCatalog(IReadOnlyDictionary<string, Landmark> landmarks)
    {
        this.landmarks = landmarks;
    }

    public IEnumerable<Landmark> Landmarks => this.landmarks.Values;

    // Returns a new catalog with the entry added or replaced, revalidated through
    // the same path used for the on-disk file so a written catalog always parses.
    public LandmarkCatalog WithLandmark(Landmark landmark)
    {
        ArgumentNullException.ThrowIfNull(landmark);
        List<Landmark> updated = this.landmarks.Values
            .Where(existing => !string.Equals(existing.LandmarkId, landmark.LandmarkId, StringComparison.Ordinal))
            .Append(landmark)
            .ToList();
        return Parse(Serialize(updated));
    }

    public string ToJson() => Serialize(this.landmarks.Values);

    public static string Serialize(IEnumerable<Landmark> landmarks)
    {
        ArgumentNullException.ThrowIfNull(landmarks);
        CatalogDocument document = new()
        {
            Landmarks = landmarks
                .OrderBy(landmark => landmark.LandmarkId, StringComparer.Ordinal)
                .Select(ToDocument)
                .ToList(),
        };
        return JsonSerializer.Serialize(document, WriteOptions);
    }

    private static LandmarkDocument ToDocument(Landmark landmark) => new()
    {
        LandmarkId = landmark.LandmarkId,
        DisplayName = landmark.DisplayName,
        Location = landmark.Position.Location,
        Tile = new TileDocument { X = landmark.Position.X, Y = landmark.Position.Y },
        OpenStart = landmark.OpenStart,
        OpenEnd = landmark.OpenEnd,
        DepartureLeadMinutes = landmark.DepartureLeadMinutes,
        SupportedRoutes = landmark.SupportedRoutes
            .OrderBy(route => route.NpcId, StringComparer.Ordinal)
            .ThenBy(route => route.OriginLocation, StringComparer.Ordinal)
            .Select(route => new RouteDocument { NpcId = route.NpcId, OriginLocation = route.OriginLocation })
            .ToList(),
    };

    public static LandmarkCatalog Parse(string json)
    {
        CatalogDocument? document;
        try
        {
            document = JsonSerializer.Deserialize<CatalogDocument>(json);
        }
        catch (JsonException ex)
        {
            throw new LandmarkCatalogException("invalid_landmark_catalog", ex.Message);
        }

        if (document?.Landmarks is null || document.Landmarks.Count == 0)
            throw new LandmarkCatalogException("landmark_catalog_empty", "landmark catalog must contain at least one landmark");

        Dictionary<string, Landmark> result = new(StringComparer.Ordinal);
        foreach (LandmarkDocument source in document.Landmarks)
        {
            ValidateIdentity(source.LandmarkId, "invalid_landmark_id");
            ValidateIdentity(source.DisplayName, "invalid_landmark_display_name");
            ValidateIdentity(source.Location, "invalid_landmark_location");
            if (source.Tile is null || source.Tile.X < 0 || source.Tile.Y < 0)
                throw new LandmarkCatalogException("invalid_landmark_tile", $"landmark {source.LandmarkId} has an invalid tile");
            if (!ValidAppointmentTime(source.OpenStart) || !ValidAppointmentTime(source.OpenEnd) || source.OpenEnd <= source.OpenStart)
                throw new LandmarkCatalogException("invalid_landmark_window", $"landmark {source.LandmarkId} has an invalid open window");
            if (source.DepartureLeadMinutes <= 0 || source.DepartureLeadMinutes % 10 != 0)
                throw new LandmarkCatalogException("invalid_departure_lead", $"landmark {source.LandmarkId} has an invalid departure lead");
            if (!result.TryAdd(source.LandmarkId, BuildLandmark(source)))
                throw new LandmarkCatalogException("duplicate_landmark_id", $"duplicate landmark_id {source.LandmarkId}");
        }

        return new LandmarkCatalog(result);
    }

    public Landmark? Find(string landmarkId)
    {
        return this.landmarks.TryGetValue(landmarkId, out Landmark? landmark) ? landmark : null;
    }

    public bool SupportsRoute(string npcId, string originLocation, string landmarkId)
    {
        Landmark? landmark = this.Find(landmarkId);
        return landmark is not null && landmark.SupportedRoutes.Any(route =>
            RouteMatches(route.NpcId, npcId) && RouteMatches(route.OriginLocation, originLocation));
    }

    private static bool RouteMatches(string allowed, string actual) =>
        string.Equals(allowed, AnyRoute, StringComparison.Ordinal) ||
        string.Equals(allowed, actual, StringComparison.Ordinal);

    private static Landmark BuildLandmark(LandmarkDocument source)
    {
        HashSet<string> routes = new(StringComparer.Ordinal);
        List<SupportedRoute> supported = new();
        foreach (RouteDocument route in source.SupportedRoutes ?? new List<RouteDocument>())
        {
            ValidateIdentity(route.NpcId, "invalid_route_npc");
            if (!string.Equals(route.NpcId, AnyRoute, StringComparison.Ordinal) &&
                !route.NpcId.StartsWith("npc:", StringComparison.Ordinal))
                throw new LandmarkCatalogException("invalid_route_npc", $"route NPC {route.NpcId} is not canonical");
            ValidateIdentity(route.OriginLocation, "invalid_route_origin");
            string key = route.NpcId + "\n" + route.OriginLocation;
            if (!routes.Add(key))
                throw new LandmarkCatalogException("duplicate_supported_route", $"duplicate route {route.NpcId} from {route.OriginLocation}");
            supported.Add(new SupportedRoute(route.NpcId, route.OriginLocation));
        }

        return new Landmark(
            source.LandmarkId,
            source.DisplayName,
            new WorldPosition(source.Location, source.Tile!.X, source.Tile.Y),
            source.OpenStart,
            source.OpenEnd,
            source.DepartureLeadMinutes,
            supported
        );
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

    private static void ValidateIdentity(string? value, string code)
    {
        if (string.IsNullOrWhiteSpace(value) || !string.Equals(value, value.Trim(), StringComparison.Ordinal))
            throw new LandmarkCatalogException(code, "catalog identity must be canonical and non-empty");
    }

    private sealed class CatalogDocument
    {
        [JsonPropertyName("landmarks")]
        public List<LandmarkDocument>? Landmarks { get; set; }
    }

    private sealed class LandmarkDocument
    {
        [JsonPropertyName("landmark_id")]
        public string LandmarkId { get; set; } = string.Empty;

        [JsonPropertyName("display_name")]
        public string DisplayName { get; set; } = string.Empty;

        [JsonPropertyName("location")]
        public string Location { get; set; } = string.Empty;

        [JsonPropertyName("tile")]
        public TileDocument? Tile { get; set; }

        [JsonPropertyName("open_start")]
        public int OpenStart { get; set; }

        [JsonPropertyName("open_end")]
        public int OpenEnd { get; set; }

        [JsonPropertyName("departure_lead_minutes")]
        public int DepartureLeadMinutes { get; set; }

        [JsonPropertyName("supported_routes")]
        public List<RouteDocument>? SupportedRoutes { get; set; }
    }

    private sealed class TileDocument
    {
        [JsonPropertyName("x")]
        public int X { get; set; }

        [JsonPropertyName("y")]
        public int Y { get; set; }
    }

    private sealed class RouteDocument
    {
        [JsonPropertyName("npc_id")]
        public string NpcId { get; set; } = string.Empty;

        [JsonPropertyName("origin_location")]
        public string OriginLocation { get; set; } = string.Empty;
    }
}
