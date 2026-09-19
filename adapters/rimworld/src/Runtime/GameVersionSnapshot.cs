using System;

namespace Wia.RimWorld.Runtime
{
    /// <summary>
    /// The version of the game this process is actually running, read once from RimWorld's own
    /// version API and kept for the lifetime of the process.
    ///
    /// It is captured during startup rather than read where it is needed, because AdapterHello is
    /// built on the connection worker and game APIs belong to the main thread. Capturing once also
    /// freezes the value: a reconnect reports the same version as the first handshake, so the
    /// Runtime never sees the identity of one process change underneath it.
    ///
    /// This is deliberately not the version the adapter is *built* against. That is a build-time
    /// fact and lives in <see cref="AdapterIdentity.BuildTargetGameVersion"/>.
    /// </summary>
    internal static class GameVersionSnapshot
    {
        /// <summary>Reported when the version could not be read, matching other adapters' behaviour.</summary>
        public const string Unknown = "unknown";

        private static string current = Unknown;
        private static string diagnostic = Unknown;

        /// <summary>Version reported in AdapterHello.</summary>
        public static string Current
        {
            get { return current; }
        }

        /// <summary>
        /// Version for diagnostic logs, including the build revision. The revision is what makes the
        /// log distinguish a version read from the running game from one compiled into the adapter:
        /// the build target in <see cref="AdapterIdentity.BuildTargetGameVersion"/> carries no
        /// revision, so it cannot produce this string.
        /// </summary>
        public static string Diagnostic
        {
            get { return diagnostic; }
        }

        /// <summary>
        /// Reads the running game's version. Must be called on the main thread during startup.
        ///
        /// A failure here is not fatal by itself: the handshake is still valid with an unknown
        /// version, which is what this adapter reported before it could read one. It is logged so
        /// the placeholder is visible rather than silently passed off as a real version.
        /// </summary>
        public static void Capture()
        {
            try
            {
                // global:: is required: this assembly's root namespace is Wia.RimWorld, so a bare
                // RimWorld.VersionControl binds to the adapter's own namespace, not the game's.
                string version = global::RimWorld.VersionControl.CurrentVersionString;
                if (string.IsNullOrEmpty(version))
                {
                    AdapterLog.Warn("game version reported as empty; sending " + Unknown);
                    return;
                }

                current = version;

                string withRevision = global::RimWorld.VersionControl.CurrentVersionStringWithRev;
                diagnostic = string.IsNullOrEmpty(withRevision) ? version : withRevision;
            }
            catch (Exception ex)
            {
                AdapterLog.Warn(
                    "could not read the game version; sending " + Unknown + ": " +
                    ex.GetType().Name + ": " + ex.Message);
            }
        }
    }
}
