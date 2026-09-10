using GameAgent.Stardew.Runtime;

namespace GameAgent.Stardew.Diagnostics;

internal static class ProbeTestSaveLoader
{
    public static void RegisterCommand(AdapterConfig config, Action<string, string, Action<string[]>> add, Action<string[]> load)
    {
        if (config.EnablePhase9RouteProbe)
            add("gameagent_phase9_load_test_save",
                "Load only Phase9TestSaveSlot from local config while no world is loaded. Accepts no arguments. Uses Stardew's global Saves directory. Before launching SMAPI, safely move the original Saves directory aside and place only the copied test slot in a temporary Saves directory at that location. After the test, close the game and restore the original Saves directory.", load);
    }

    public static string Load(AdapterConfig config, string[] args, bool worldReady, bool loading, Action<string> load)
    {
        if (!config.EnablePhase9RouteProbe) return "disabled";
        if (worldReady) return "world_already_loaded";
        if (loading) return "load_in_progress";
        if (args.Length != 0) return "arguments_rejected";
        string slot = config.Phase9TestSaveSlot;
        if (string.IsNullOrWhiteSpace(slot) || slot != slot.Trim() || slot is "." or ".." ||
            slot.EndsWith('.') || slot.Any(c => char.IsControl(c) || "/\\:<>\"|?*".Contains(c)) || Path.IsPathRooted(slot))
            return "invalid_save_slot";
        load(slot);
        return "load_requested";
    }
}
