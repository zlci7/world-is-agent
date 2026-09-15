using System.Collections.Generic;
using GameAgent.Stardew.Tasks;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class CheckpointMarkerTests
{
    private static Dictionary<string, string> ConfirmedData() => new()
    {
        ["schema_version"] = "1",
        ["game_id"] = "stardew-valley",
        ["world_id"] = "world-a",
        ["status"] = "confirmed",
        ["checkpoint_id"] = "checkpoint_1",
        ["checksum"] = "sha256-abc",
    };

    private static Dictionary<string, string> UnconfirmedData() => new()
    {
        ["schema_version"] = "1",
        ["game_id"] = "stardew-valley",
        ["world_id"] = "world-a",
        ["status"] = "unconfirmed",
        ["reason"] = "timeout",
    };

    [Fact]
    public void ConfirmedMarkerRoundTripsThroughSaveData()
    {
        CheckpointMarker marker = CheckpointMarker.Confirmed("stardew-valley", "world-a", "checkpoint_1", "sha256-abc");

        Dictionary<string, string> data = marker.ToSaveData();
        CheckpointMarkerRead read = CheckpointMarker.Read(data);

        Assert.Equal(CheckpointMarkerState.Confirmed, read.State);
        Assert.Equal(marker, read.Marker);
        Assert.Equal("1", data["schema_version"]);
        Assert.Equal("checkpoint_1", data["checkpoint_id"]);
        Assert.Equal("sha256-abc", data["checksum"]);
        Assert.False(data.ContainsKey("reason"));
    }

    [Fact]
    public void UnconfirmedMarkerRoundTripsWithoutAReference()
    {
        CheckpointMarker marker = CheckpointMarker.Unconfirmed("stardew-valley", "world-a", "timeout");

        Dictionary<string, string> data = marker.ToSaveData();
        CheckpointMarkerRead read = CheckpointMarker.Read(data);

        Assert.Equal(CheckpointMarkerState.Unconfirmed, read.State);
        Assert.Equal("timeout", read.Marker?.Reason);
        Assert.False(data.ContainsKey("checkpoint_id"));
        Assert.False(data.ContainsKey("checksum"));
    }

    [Fact]
    public void MissingSaveDataReadsAsAbsent()
    {
        Assert.Equal(CheckpointMarkerState.Absent, CheckpointMarker.Read(null).State);
        Assert.Equal(CheckpointMarkerState.Absent, CheckpointMarker.Read(new Dictionary<string, string>()).State);
    }

    [Theory]
    [InlineData("schema_version", "one")]
    [InlineData("game_id", "")]
    [InlineData("game_id", " stardew-valley")]
    [InlineData("world_id", "")]
    [InlineData("status", "pending")]
    [InlineData("checkpoint_id", "")]
    [InlineData("checksum", "")]
    public void ConfirmedMarkerFieldsAreValidated(string field, string value)
    {
        Dictionary<string, string> data = ConfirmedData();
        data[field] = value;

        CheckpointMarkerRead read = CheckpointMarker.Read(data);

        Assert.Equal(CheckpointMarkerState.Invalid, read.State);
        Assert.Null(read.Marker);
    }

    [Fact]
    public void ConfirmedMarkerWithoutAReferenceIsInvalid()
    {
        Dictionary<string, string> data = ConfirmedData();
        data.Remove("checksum");

        CheckpointMarkerRead read = CheckpointMarker.Read(data);

        Assert.Equal(CheckpointMarkerState.Invalid, read.State);
        Assert.Equal("invalid_confirmed_reference", read.Reason);
    }

    [Theory]
    [InlineData("reason", "")]
    [InlineData("status", "confirmed")]
    public void UnconfirmedMarkerFieldsAreValidated(string field, string value)
    {
        Dictionary<string, string> data = UnconfirmedData();
        data[field] = value;

        CheckpointMarkerRead read = CheckpointMarker.Read(data);

        Assert.Equal(CheckpointMarkerState.Invalid, read.State);
    }

    [Fact]
    public void UnconfirmedMarkerWithoutAReasonIsInvalid()
    {
        Dictionary<string, string> data = UnconfirmedData();
        data.Remove("reason");

        CheckpointMarkerRead read = CheckpointMarker.Read(data);

        Assert.Equal(CheckpointMarkerState.Invalid, read.State);
        Assert.Equal("invalid_unconfirmed_reason", read.Reason);
    }
}
