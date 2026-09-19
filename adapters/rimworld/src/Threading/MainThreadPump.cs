using System;
using System.Collections.Concurrent;
using System.Threading;
using UnityEngine;

namespace Wia.RimWorld.Threading
{
    /// <summary>
    /// Runs queued work on the Unity main thread.
    ///
    /// The transport is process-level and must not be a GameComponent: there is no Current.Game on
    /// the main menu, and Map is null while a caravan travels. This component therefore lives on a
    /// hidden, indestructible GameObject and drains its queue once per frame, independently of
    /// whether a game is loaded.
    /// </summary>
    public sealed class MainThreadPump : MonoBehaviour
    {
        private const int MaxWorkItemsPerFrame = 64;

        private readonly ConcurrentQueue<Action> pending = new ConcurrentQueue<Action>();

        /// <summary>
        /// Managed id of the thread this pump drains on. Recorded on the first Update, so it is
        /// available to any callback that runs inside Update.
        /// </summary>
        public int PumpThreadId { get; private set; }

        /// <summary>Queues work to run on the main thread. Safe to call from any thread.</summary>
        public void Enqueue(Action work)
        {
            if (work == null)
            {
                throw new ArgumentNullException(nameof(work));
            }

            this.pending.Enqueue(work);
        }

        private void Update()
        {
            if (this.PumpThreadId == 0)
            {
                this.PumpThreadId = Thread.CurrentThread.ManagedThreadId;
            }

            int drained = 0;
            while (drained < MaxWorkItemsPerFrame && this.pending.TryDequeue(out Action work))
            {
                drained++;
                try
                {
                    work();
                }
                catch (Exception ex)
                {
                    AdapterLog.Error("main-thread work failed: " + ex);
                }
            }
        }
    }
}
