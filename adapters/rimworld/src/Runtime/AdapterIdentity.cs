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

        /// <summary>RimWorld version this adapter is built and tested against.</summary>
        public const string GameVersion = "1.6.4871";

        /// <summary>Version of the adapter assembly itself.</summary>
        public const string AdapterVersion = "0.1.0";

        /// <summary>
        /// The Runtime's adapter endpoint. The Runtime listens on loopback only; the address is
        /// the default gRPC port and can be overridden by the Runtime's --grpc-addr flag.
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
