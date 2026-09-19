using System;
using System.Threading;
using UnityEngine;
using Verse;
using Wia.RimWorld.Runtime;
using Wia.RimWorld.Threading;

namespace Wia.RimWorld
{
    /// <summary>
    /// Mod entry point.
    ///
    /// RimWorld runs static constructors carrying this attribute on the main thread once mod
    /// assemblies are loaded. The pump and the transport are created here and belong to the process,
    /// not to a save: they survive returning to the main menu and reloading a game, which is why
    /// neither is a GameComponent.
    /// </summary>
    [StaticConstructorOnStartup]
    public static class AdapterStartup
    {
        private static RuntimeConnection connection;

        /// <summary>Managed id of the thread this mod was constructed on.</summary>
        public static int StartupThreadId { get; private set; }

        static AdapterStartup()
        {
            StartupThreadId = Thread.CurrentThread.ManagedThreadId;

            try
            {
                // Must run before the first gRPC call: Grpc.Core resolves its native library by
                // bare name, and this is what puts that module in the process.
                if (!NativeLibraryLoader.TryPreload(out string nativeReport))
                {
                    // Fail closed. No gRPC call can succeed without this library, so starting the
                    // transport anyway would only produce an endless connect-fail-retry loop that
                    // hides the real cause. Reconnecting cannot fix a missing file, and this static
                    // constructor runs once per process: staging the file afterwards does not recover
                    // the running game, RimWorld has to be restarted.
                    AdapterLog.Error(
                        "adapter disabled, no connection will be attempted: " + nativeReport +
                        " (restart RimWorld after installing it)");
                    return;
                }

                AdapterLog.Info(nativeReport);

                // Main thread, once per process. The connection worker reads this snapshot instead
                // of touching the game's API itself.
                GameVersionSnapshot.Capture();

                GameObject host = new GameObject("WiaRimWorldPump");
                host.hideFlags = HideFlags.HideAndDontSave;
                UnityEngine.Object.DontDestroyOnLoad(host);
                MainThreadPump pump = host.AddComponent<MainThreadPump>();

                connection = new RuntimeConnection(pump);
                connection.Start();

                AdapterLog.Info(
                    $"adapter {AdapterIdentity.AdapterVersion} started on thread {StartupThreadId}; " +
                    $"game {AdapterIdentity.GameId}/{GameVersionSnapshot.Diagnostic}; " +
                    $"runtime {AdapterIdentity.RuntimeHost}:{AdapterIdentity.RuntimePort}");
            }
            catch (Exception ex)
            {
                AdapterLog.Error("adapter startup failed: " + ex);
            }
        }
    }
}
