using System;
using System.Collections.Generic;
using System.Globalization;

namespace GameAgent.Stardew.Tasks;

public enum CheckpointMarkerState
{
    Absent,
    Confirmed,
    Unconfirmed,
    Invalid,
}

/// <summary>
/// Save-data marker that names the task snapshot a game save refers to. The marker is a flat
/// dictionary of explicit field names so the stored shape never depends on property casing rules,
/// and every field is validated on read: an unreadable marker stays a diagnosis, never silently
/// becomes a usable reference.
/// </summary>
public sealed record CheckpointMarker(
    int SchemaVersion,
    string GameId,
    string WorldId,
    string Status,
    string? CheckpointId,
    string? Checksum,
    string? Reason
)
{
    public const int CurrentSchemaVersion = 1;
    public const string SaveDataKey = "runtime-task-checkpoint";
    public const string StatusAbsent = "absent";
    public const string StatusConfirmed = "confirmed";
    public const string StatusUnconfirmed = "unconfirmed";

    public static CheckpointMarker Confirmed(string gameId, string worldId, string checkpointId, string checksum)
    {
        RequireIdentity(gameId, nameof(gameId));
        RequireIdentity(worldId, nameof(worldId));
        RequireIdentity(checkpointId, nameof(checkpointId));
        RequireIdentity(checksum, nameof(checksum));
        return new CheckpointMarker(CurrentSchemaVersion, gameId, worldId, StatusConfirmed, checkpointId, checksum, null);
    }

    public static CheckpointMarker Unconfirmed(string gameId, string worldId, string reason)
    {
        RequireIdentity(gameId, nameof(gameId));
        RequireIdentity(worldId, nameof(worldId));
        RequireIdentity(reason, nameof(reason));
        return new CheckpointMarker(CurrentSchemaVersion, gameId, worldId, StatusUnconfirmed, null, null, reason);
    }

    public bool IsConfirmed => this.Status == StatusConfirmed;

    public Dictionary<string, string> ToSaveData()
    {
        Dictionary<string, string> data = new(StringComparer.Ordinal)
        {
            ["schema_version"] = this.SchemaVersion.ToString(CultureInfo.InvariantCulture),
            ["game_id"] = this.GameId,
            ["world_id"] = this.WorldId,
            ["status"] = this.Status,
        };
        if (this.CheckpointId is not null)
            data["checkpoint_id"] = this.CheckpointId;
        if (this.Checksum is not null)
            data["checksum"] = this.Checksum;
        if (this.Reason is not null)
            data["reason"] = this.Reason;
        return data;
    }

    public static CheckpointMarkerRead Read(IDictionary<string, string>? data)
    {
        if (data is null || !data.ContainsKey("schema_version"))
            return CheckpointMarkerRead.Absent;
        if (!data.TryGetValue("schema_version", out string? schemaVersion) ||
            !int.TryParse(schemaVersion, NumberStyles.None, CultureInfo.InvariantCulture, out int version) ||
            version != CurrentSchemaVersion)
        {
            return CheckpointMarkerRead.Invalid("schema_version");
        }
        if (!TryReadIdentity(data, "game_id", out string gameId))
            return CheckpointMarkerRead.Invalid("game_id");
        if (!TryReadIdentity(data, "world_id", out string worldId))
            return CheckpointMarkerRead.Invalid("world_id");
        if (!data.TryGetValue("status", out string? status))
            return CheckpointMarkerRead.Invalid("status");
        switch (status)
        {
            case StatusConfirmed:
                if (!TryReadIdentity(data, "checkpoint_id", out string checkpointId) ||
                    !TryReadIdentity(data, "checksum", out string checksum))
                {
                    return CheckpointMarkerRead.Invalid("confirmed_reference");
                }
                return CheckpointMarkerRead.Of(CheckpointMarker.Confirmed(gameId, worldId, checkpointId, checksum));
            case StatusUnconfirmed:
                if (!TryReadIdentity(data, "reason", out string reason))
                    return CheckpointMarkerRead.Invalid("unconfirmed_reason");
                return CheckpointMarkerRead.Of(CheckpointMarker.Unconfirmed(gameId, worldId, reason));
            default:
                return CheckpointMarkerRead.Invalid("status");
        }
    }

    private static bool TryReadIdentity(IDictionary<string, string> data, string key, out string value)
    {
        value = string.Empty;
        if (!data.TryGetValue(key, out string? raw) || string.IsNullOrWhiteSpace(raw) || raw != raw.Trim())
            return false;
        value = raw;
        return true;
    }

    private static void RequireIdentity(string value, string name)
    {
        if (string.IsNullOrWhiteSpace(value) || value != value.Trim())
            throw new ArgumentException($"{name} must be a canonical non-empty identity", name);
    }
}

public sealed record CheckpointMarkerRead(CheckpointMarkerState State, CheckpointMarker? Marker, string Reason)
{
    public static CheckpointMarkerRead Absent { get; } = new(CheckpointMarkerState.Absent, null, CheckpointMarker.StatusAbsent);

    public static CheckpointMarkerRead Of(CheckpointMarker marker) => new(
        marker.IsConfirmed ? CheckpointMarkerState.Confirmed : CheckpointMarkerState.Unconfirmed,
        marker,
        marker.Reason ?? string.Empty
    );

    public static CheckpointMarkerRead Invalid(string field) => new(CheckpointMarkerState.Invalid, null, $"invalid_{field}");
}
