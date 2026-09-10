using GameAgent.Stardew.Diagnostics;
using GameAgent.Stardew.Runtime;
using Xunit;

namespace Phase9Feasibility.Tests;

public sealed class ProbeTestSaveLoaderTests
{
    [Fact]
    public void SaveLoaderCommandIsRegisteredOnlyWithOptIn()
    {
        var commands = new List<string>(); var config = new AdapterConfig();
        ProbeTestSaveLoader.RegisterCommand(config, (name, _, _) => commands.Add(name), _ => { });
        Assert.Empty(commands);
        config.EnablePhase9RouteProbe = true;
        ProbeTestSaveLoader.RegisterCommand(config, (name, _, _) => commands.Add(name), _ => { });
        Assert.Equal(new[] { "gameagent_phase9_load_test_save" }, commands);
    }

    [Theory]
    [InlineData("")]
    [InlineData(" ")]
    [InlineData(".")]
    [InlineData("..")]
    [InlineData("../Farmer_123")]
    [InlineData("folder/Farmer_123")]
    [InlineData("folder\\Farmer_123")]
    [InlineData("C:\\Saves\\Farmer_123")]
    [InlineData("C:Farmer_123")]
    [InlineData("/Farmer_123")]
    public void InvalidSlotNeverLoads(string slot)
    {
        var loaded = new List<string>();
        var config = new AdapterConfig { EnablePhase9RouteProbe = true, Phase9TestSaveSlot = slot };
        Assert.Equal("invalid_save_slot", ProbeTestSaveLoader.Load(config, Array.Empty<string>(), false, false, loaded.Add));
        Assert.Empty(loaded);
    }

    [Fact]
    public void OnlyConfiguredBasenameLoadsAndArgumentsCannotOverrideIt()
    {
        var loaded = new List<string>();
        var config = new AdapterConfig { EnablePhase9RouteProbe = true, Phase9TestSaveSlot = "Farmer_123" };
        Assert.Equal("arguments_rejected", ProbeTestSaveLoader.Load(config, new[] { "Other_456" }, false, false, loaded.Add));
        Assert.Empty(loaded);
        Assert.Equal("load_requested", ProbeTestSaveLoader.Load(config, Array.Empty<string>(), false, false, loaded.Add));
        Assert.Equal(new[] { "Farmer_123" }, loaded);
    }

    [Fact]
    public void DisabledWorldReadyAndAlreadyLoadingReject()
    {
        var loaded = new List<string>(); var config = new AdapterConfig();
        Assert.Equal("disabled", ProbeTestSaveLoader.Load(config, Array.Empty<string>(), false, false, loaded.Add));
        config.EnablePhase9RouteProbe = true; config.Phase9TestSaveSlot = "Farmer_123";
        Assert.Equal("world_already_loaded", ProbeTestSaveLoader.Load(config, Array.Empty<string>(), true, false, loaded.Add));
        Assert.Equal("load_in_progress", ProbeTestSaveLoader.Load(config, Array.Empty<string>(), false, true, loaded.Add));
        Assert.Empty(loaded);
    }
}
