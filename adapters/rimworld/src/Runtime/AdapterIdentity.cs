using System;

namespace Wia.RimWorld.Runtime
{
    /// <summary>
    /// Values the adapter declares to the Runtime during the handshake.
    /// </summary>
    internal static class AdapterIdentity
    {
        /// <summary>Adapter identifier reported in AdapterHello.</summary>
        public const string AdapterId = "wia-rimworld";

        /// <summary>Protocol family this adapter is built against.</summary>
        public const string ProtocolVersion = "v1alpha2";

        /// <summary>Game identifier reported in AdapterHello and used in every session key.</summary>
        public const string GameId = "rimworld";

        /// <summary>
        /// RimWorld version this adapter is built and tested against. This is a build-time fact:
        /// what AdapterHello reports is the version the process is actually running, which the
        /// game may update without the adapter being rebuilt. See <see cref="GameVersionSnapshot"/>.
        /// </summary>
        public const string BuildTargetGameVersion = "1.6.4871";

        /// <summary>Version of the adapter assembly itself.</summary>
        public const string AdapterVersion = "0.1.0";

        /// <summary>
        /// The Runtime's adapter endpoint: loopback only, on the Runtime's default gRPC port.
        ///
        /// This is fixed, not negotiated. The adapter has no settings entry yet, so it does not
        /// follow the Runtime's <c>--grpc-addr</c> flag: pointing the Runtime at another address
        /// requires rebuilding the adapter with that address.
        /// </summary>
        public const string RuntimeHost = "127.0.0.1";

        /// <summary>Default Runtime gRPC port.</summary>
        public const int RuntimePort = 50051;

        /// <summary>Delay between connection attempts while the Runtime is unavailable.</summary>
        public static readonly TimeSpan RetryDelay = TimeSpan.FromSeconds(5);

        /// <summary>
        /// Prefix for every line this adapter writes to the RimWorld log, so its output can be
        /// separated from other mods without a side-channel log file.
        /// </summary>
        public const string LogPrefix = "[WIA] ";
    }
}
