using System;
using System.Collections.Generic;

namespace Wia.RimWorld.Runtime
{
    internal enum SessionPhase
    {
        Disconnected,
        AwaitingEnvironmentReady,
        AwaitingCapabilityRequest,
        Active,
    }

    /// <summary>
    /// Handshake state for one connection.
    ///
    /// This adapter never advertises the task extension, so the handshake ends at capability
    /// discovery. There is no WorldBinding, no WorldClock and no checkpoint step, and ordinary
    /// turns do not depend on them.
    /// </summary>
    internal sealed class RuntimeSessionState
    {
        private readonly object gate = new object();
        private SessionPhase phase = SessionPhase.Disconnected;

        public SessionPhase Phase
        {
            get
            {
                lock (this.gate)
                {
                    return this.phase;
                }
            }
        }

        /// <summary>True once the handshake has completed and the Runtime may drive this adapter.</summary>
        public bool CanUseRuntime
        {
            get
            {
                lock (this.gate)
                {
                    return this.phase == SessionPhase.Active;
                }
            }
        }

        public void BeginConnection()
        {
            lock (this.gate)
            {
                this.phase = SessionPhase.AwaitingEnvironmentReady;
            }
        }

        /// <summary>
        /// Accepts EnvironmentReady.
        ///
        /// This adapter advertises no supported extensions, so an accepted extension is an anomaly
        /// rather than something to remember: the Runtime and the adapter would disagree about what
        /// was negotiated. Treating it as a handshake failure keeps the "no task extension" boundary
        /// enforced by the code instead of by convention.
        /// </summary>
        public bool AcceptEnvironmentReady(IReadOnlyList<string> acceptedExtensions, out string error)
        {
            if (acceptedExtensions == null)
            {
                throw new ArgumentNullException(nameof(acceptedExtensions));
            }

            lock (this.gate)
            {
                if (this.phase != SessionPhase.AwaitingEnvironmentReady)
                {
                    return Reject(out error);
                }

                if (acceptedExtensions.Count != 0)
                {
                    error = "unexpected_accepted_extension:" + string.Join(",", acceptedExtensions);
                    return false;
                }

                this.phase = SessionPhase.AwaitingCapabilityRequest;
                error = string.Empty;
                return true;
            }
        }

        public bool AcceptCapabilityRequest(out string error)
        {
            lock (this.gate)
            {
                if (this.phase != SessionPhase.AwaitingCapabilityRequest)
                {
                    return Reject(out error);
                }

                error = string.Empty;
                return true;
            }
        }

        public void MarkCapabilitiesSent()
        {
            lock (this.gate)
            {
                if (this.phase != SessionPhase.AwaitingCapabilityRequest)
                {
                    throw new InvalidOperationException("capabilities sent outside capability discovery");
                }

                this.phase = SessionPhase.Active;
            }
        }

        public void Disconnect()
        {
            lock (this.gate)
            {
                this.phase = SessionPhase.Disconnected;
            }
        }

        private static bool Reject(out string error)
        {
            error = "handshake_out_of_order";
            return false;
        }
    }
}
