using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using GameAgent.Protocol.V1Alpha2;
using Google.Protobuf.WellKnownTypes;
using RimWorld;
using Verse;
using Wia.RimWorld.Identity;
using Wia.RimWorld.Projection;
using Wia.RimWorld.Threading;

namespace Wia.RimWorld.Runtime
{
    /// <summary>
    /// Answers ObserveRequest.
    ///
    /// The request arrives on the stream thread, but everything it needs - the pawn, its map, the
    /// clock - is live game state that may only be touched on the main thread, so the whole read is
    /// marshalled there. A failure is thrown rather than returned as a half-filled observation: the
    /// caller turns it into a correlated protocol error, which is a truthful answer, whereas an
    /// observation with a defaulted state would be a fabrication the model cannot detect.
    /// </summary>
    internal sealed class ColonistObservationService
    {
        private readonly MainThreadPump pump;

        /// <summary>
        /// Per-entity observation counter. Main-thread only, like every other read of game state.
        /// Revisions are not compared anywhere in the Runtime, but a revision that never moves is
        /// indistinguishable from a stale read when someone is looking at a trace.
        /// </summary>
        private readonly Dictionary<string, ulong> revisions = new Dictionary<string, ulong>(StringComparer.Ordinal);

        public ColonistObservationService(MainThreadPump pump)
        {
            this.pump = pump ?? throw new ArgumentNullException(nameof(pump));
        }

        public async Task<Observation> ObserveAsync(string entityId, string worldId, CancellationToken token)
        {
            return await this.pump
                .InvokeAsync(() => this.Read(entityId, worldId), token)
                .ConfigureAwait(false);
        }

        private Observation Read(string entityId, string worldId)
        {
            long tick;
            if (!GameClock.TryReadTick(out tick))
            {
                throw new InvalidOperationException("no game is loaded, so there is nothing to observe");
            }

            string currentWorldId = WorldIdentityComponent.CurrentWorldId();
            if (string.IsNullOrEmpty(currentWorldId))
            {
                throw new InvalidOperationException("the loaded world has no WIA identity");
            }

            if (!string.Equals(currentWorldId, worldId, StringComparison.Ordinal))
            {
                // The Runtime is asking about a world this process is not running. Answering anyway
                // would hand it facts about a different colony under the id it asked for.
                throw new InvalidOperationException(
                    "requested world " + worldId + " is not the loaded world " + currentWorldId);
            }

            Pawn pawn = Find(entityId);
            if (pawn == null)
            {
                throw new InvalidOperationException("no pawn matches entity " + entityId);
            }

            string reason;
            if (!EligibleColonist.Is(pawn, out reason))
            {
                throw new InvalidOperationException("pawn " + entityId + " is not an eligible colonist: " + reason);
            }

            RimWorldSnapshot snapshot = ColonistSnapshotReader.Read(pawn);
            Struct state = ColonistProjection.Build(snapshot);

            ulong revision;
            this.revisions.TryGetValue(entityId, out revision);
            revision++;
            this.revisions[entityId] = revision;

            return ProtocolMapper.BuildObservation(entityId, currentWorldId, tick, revision, state);
        }

        /// <summary>
        /// Resolves an entity id back to the pawn that produced it.
        ///
        /// The id is derived from the game's own stable id rather than stored in a table, so the
        /// only way back is to ask the pawns. The list covers every place a colonist can be - a map,
        /// a caravan, a travelling transporter - which matters because a colonist in a caravan has
        /// no map at all.
        /// </summary>
        private static Pawn Find(string entityId)
        {
            foreach (Pawn pawn in PawnsFinder.AllMapsCaravansAndTravellingTransporters_Alive)
            {
                if (pawn != null && string.Equals(EntityId.For(pawn), entityId, StringComparison.Ordinal))
                {
                    return pawn;
                }
            }

            return null;
        }
    }
}
