namespace GameAgent.Stardew.Authoring;

// Writes the landmark asset through a sibling temporary file so an interrupted
// write cannot leave a partial catalog that fails to parse on the next load.
public static class LandmarkAssetWriter
{
    public static void Write(string path, string json)
    {
        if (string.IsNullOrWhiteSpace(path))
            throw new ArgumentException("path is required", nameof(path));
        ArgumentNullException.ThrowIfNull(json);

        string temporary = path + ".tmp";
        File.WriteAllText(temporary, json);
        try
        {
            if (File.Exists(path))
                File.Replace(temporary, path, destinationBackupFileName: null);
            else
                File.Move(temporary, path);
        }
        catch
        {
            TryDelete(temporary);
            throw;
        }
    }

    private static void TryDelete(string path)
    {
        try
        {
            if (File.Exists(path))
                File.Delete(path);
        }
        catch
        {
            // The temporary file is inert; the next successful write replaces it.
        }
    }
}
