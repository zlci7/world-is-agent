using Verse;

namespace Wia.RimWorld
{
    /// <summary>
    /// Adapter logging. Every line goes to the RimWorld log with a fixed prefix so the adapter's
    /// output can be separated from other mods without a side-channel log file.
    /// </summary>
    internal static class AdapterLog
    {
        public static void Info(string message)
        {
            Log.Message(Runtime.AdapterIdentity.LogPrefix + message);
        }

        public static void Warn(string message)
        {
            Log.Warning(Runtime.AdapterIdentity.LogPrefix + message);
        }

        public static void Error(string message)
        {
            Log.Error(Runtime.AdapterIdentity.LogPrefix + message);
        }
    }
}
