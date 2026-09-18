using System.Collections.Generic;
using GameAgent.Stardew.Tasks;

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

    public bool EnablePhase9SaveProbe { get; set; } = false;

    public bool EnableMailBridgeProbe { get; set; } = false;

    public int Phase9SaveProbeTimeoutSeconds { get; set; } = 5;

    public string Phase9TestSaveSlot { get; set; } = "";

    public int CheckpointPrepareTimeoutMilliseconds { get; set; } = CheckpointBridgeOptions.DefaultTimeoutMilliseconds;
}
