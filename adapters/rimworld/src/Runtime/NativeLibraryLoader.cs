using System;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;

namespace Wia.RimWorld.Runtime
{
    /// <summary>
    /// Loads Grpc.Core's native library before the first gRPC call.
    ///
    /// Grpc.Core resolves that library through <c>DllImport("grpc_csharp_ext")</c>, which asks the OS
    /// for a module by that exact base name. The file is not on any search path the game process
    /// uses, and it cannot sit next to the managed assemblies either: RimWorld hands every *.dll in
    /// Assemblies/ to Assembly.LoadFrom, where a native image throws BadImageFormatException and can
    /// stop later managed assemblies from loading at all.
    ///
    /// So the library lives in a sibling Native/ folder and is loaded here by absolute path. Once a
    /// module with the expected base name is in the process, the later DllImport resolves to it.
    /// </summary>
    internal static class NativeLibraryLoader
    {
        private const string NativeDirectoryName = "Native";

        /// <summary>
        /// Name the install script gives the copy in Native/. It must match the DllImport in
        /// Grpc.Core, and it is also the name LoadLibrary is asked for when verifying the preload.
        /// </summary>
        private const string NativeFileName = "grpc_csharp_ext.dll";

        /// <summary>
        /// Loads the native library and reports what happened. Returns false when the library is
        /// missing, in which case the first gRPC call will fail with DllNotFoundException; the
        /// report says so explicitly instead of leaving that failure unexplained.
        /// </summary>
        public static bool TryPreload(out string report)
        {
            string assemblyDirectory;
            try
            {
                assemblyDirectory = Path.GetDirectoryName(Assembly.GetExecutingAssembly().Location);
            }
            catch (Exception ex)
            {
                report = "assembly location unavailable: " + ex.GetType().Name;
                return false;
            }

            string modRoot = Path.GetDirectoryName(assemblyDirectory);
            string nativePath = Path.Combine(modRoot ?? string.Empty, NativeDirectoryName, NativeFileName);

            if (!File.Exists(nativePath))
            {
                report = "native library not found at " + nativePath +
                         "; run scripts/install-rimworld-adapter.ps1 to stage it";
                return false;
            }

            IntPtr handle = LoadLibraryW(nativePath);
            int loadError = Marshal.GetLastWin32Error();
            if (handle == IntPtr.Zero)
            {
                report = $"LoadLibrary failed for {nativePath} (win32 error {loadError})";
                return false;
            }

            // Asking for the bare name proves the next DllImport will find the module we just
            // loaded, rather than searching the disk again and failing.
            IntPtr byName = LoadLibraryW("grpc_csharp_ext");
            int byNameError = Marshal.GetLastWin32Error();

            report = $"native library loaded from {nativePath} (handle 0x{handle.ToInt64():X}); " +
                     $"bare-name resolution returned 0x{byName.ToInt64():X} (win32 error {byNameError})";

            return byName != IntPtr.Zero;
        }

        [DllImport("kernel32", SetLastError = true, CharSet = CharSet.Unicode, EntryPoint = "LoadLibraryW")]
        private static extern IntPtr LoadLibraryW(string lpFileName);
    }
}
