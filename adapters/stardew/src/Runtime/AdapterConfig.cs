using System.Collections.Generic;

namespace GameAgent.Stardew.Runtime;

public sealed class AdapterConfig
{
    public string RuntimeAddress { get; set; } = "http://127.0.0.1:50051";

    public string AdapterId { get; set; } = "stardew-smapi";

    public string AdapterVersion { get; set; } = "0.1.0";

    public string GameId { get; set; } = "stardew-valley";

    public List<string> AgentTargets { get; set; } = new();

    public bool EnableProtocolTrace { get; set; } = true;

    public bool EnablePhase9RouteProbe { get; set; } = false;

    public string Phase9TestSaveSlot { get; set; } = "";
}
