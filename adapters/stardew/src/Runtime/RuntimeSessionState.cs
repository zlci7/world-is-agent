using System;
using System.Collections.Generic;
using System.Linq;

namespace GameAgent.Stardew.Runtime;

public enum RuntimeSessionPhase
{
    Disconnected,
    AwaitingEnvironmentReady,
    AwaitingCapabilityRequest,
    AwaitingWorldBindingReady,
    TaskReady,
    TaskPaused,
}

public sealed class RuntimeSessionState
{
    public const string TaskExtension = "gameagent.tasks.v1";

    private readonly object gate = new();
    private bool taskExtensionAccepted;
    private RuntimeSessionPhase phase = RuntimeSessionPhase.Disconnected;

    public RuntimeSessionPhase Phase
    {
        get { lock (this.gate) return this.phase; }
    }

    public bool CanUseRuntime
    {
        get
        {
            lock (this.gate)
                return this.phase is RuntimeSessionPhase.AwaitingWorldBindingReady or RuntimeSessionPhase.TaskReady or RuntimeSessionPhase.TaskPaused;
        }
    }

    public bool CanUseTasks
    {
        get { lock (this.gate) return this.phase == RuntimeSessionPhase.TaskReady; }
    }

    public bool TaskExtensionAccepted
    {
        get { lock (this.gate) return this.taskExtensionAccepted; }
    }

    public void BeginConnection()
    {
        lock (this.gate)
        {
            this.taskExtensionAccepted = false;
            this.phase = RuntimeSessionPhase.AwaitingEnvironmentReady;
        }
    }

    public bool AcceptEnvironmentReady(IEnumerable<string> acceptedExtensions, out string errorCode)
    {
        ArgumentNullException.ThrowIfNull(acceptedExtensions);
        lock (this.gate)
        {
            if (this.phase != RuntimeSessionPhase.AwaitingEnvironmentReady)
                return Reject(out errorCode);

            this.taskExtensionAccepted = acceptedExtensions.Any(extension =>
                string.Equals(extension, TaskExtension, StringComparison.Ordinal));
            this.phase = RuntimeSessionPhase.AwaitingCapabilityRequest;
            errorCode = string.Empty;
            return true;
        }
    }

    public bool AcceptCapabilityRequest(out string errorCode)
    {
        lock (this.gate)
        {
            if (this.phase != RuntimeSessionPhase.AwaitingCapabilityRequest)
                return Reject(out errorCode);

            errorCode = string.Empty;
            return true;
        }
    }

    public void MarkCapabilitiesSent()
    {
        lock (this.gate)
        {
            if (this.phase != RuntimeSessionPhase.AwaitingCapabilityRequest)
                throw new InvalidOperationException("capabilities sent outside capability discovery");

            this.phase = this.taskExtensionAccepted
                ? RuntimeSessionPhase.AwaitingWorldBindingReady
                : RuntimeSessionPhase.TaskPaused;
        }
    }

    public bool AcceptWorldBindingReady(string status, out string errorCode)
    {
        lock (this.gate)
        {
            if (this.phase != RuntimeSessionPhase.AwaitingWorldBindingReady)
                return Reject(out errorCode);

            if (string.Equals(status, "ready", StringComparison.Ordinal))
                this.phase = RuntimeSessionPhase.TaskReady;
            else if (string.Equals(status, "paused", StringComparison.Ordinal))
                this.phase = RuntimeSessionPhase.TaskPaused;
            else
            {
                errorCode = "invalid_world_binding_status";
                return false;
            }

            errorCode = string.Empty;
            return true;
        }
    }

    public void PauseTasks()
    {
        lock (this.gate)
        {
            if (this.phase is RuntimeSessionPhase.AwaitingWorldBindingReady or RuntimeSessionPhase.TaskReady)
                this.phase = RuntimeSessionPhase.TaskPaused;
        }
    }

    public void PrepareWorldBinding()
    {
        lock (this.gate)
        {
            if (this.taskExtensionAccepted && this.phase is
                RuntimeSessionPhase.AwaitingWorldBindingReady or
                RuntimeSessionPhase.TaskReady or
                RuntimeSessionPhase.TaskPaused)
            {
                this.phase = RuntimeSessionPhase.AwaitingWorldBindingReady;
            }
        }
    }

    public void Disconnect()
    {
        lock (this.gate)
        {
            this.taskExtensionAccepted = false;
            this.phase = RuntimeSessionPhase.Disconnected;
        }
    }

    private static bool Reject(out string errorCode)
    {
        errorCode = "handshake_out_of_order";
        return false;
    }
}
