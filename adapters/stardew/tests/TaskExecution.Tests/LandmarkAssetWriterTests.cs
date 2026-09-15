using GameAgent.Stardew.Authoring;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class LandmarkAssetWriterTests
{
    [Fact]
    public void ReplacesAnExistingCatalogWithoutLeavingATemporaryFile()
    {
        WithTemporaryDirectory(directory =>
        {
            string path = Path.Combine(directory, "landmarks.json");
            File.WriteAllText(path, "{\"landmarks\":[]}");

            LandmarkAssetWriter.Write(path, "{\"landmarks\":[{\"landmark_id\":\"tavern_door\"}]}");

            Assert.Equal("{\"landmarks\":[{\"landmark_id\":\"tavern_door\"}]}", File.ReadAllText(path));
            Assert.False(File.Exists(path + ".tmp"));
        });
    }

    [Fact]
    public void CreatesTheCatalogWhenTheFileIsAbsent()
    {
        WithTemporaryDirectory(directory =>
        {
            string path = Path.Combine(directory, "landmarks.json");

            LandmarkAssetWriter.Write(path, "{\"landmarks\":[{\"landmark_id\":\"tavern_door\"}]}");

            Assert.Equal("{\"landmarks\":[{\"landmark_id\":\"tavern_door\"}]}", File.ReadAllText(path));
            Assert.False(File.Exists(path + ".tmp"));
        });
    }

    private static void WithTemporaryDirectory(Action<string> run)
    {
        string directory = Path.Combine(Path.GetTempPath(), "gameagent-landmarks-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        try
        {
            run(directory);
        }
        finally
        {
            Directory.Delete(directory, recursive: true);
        }
    }
}
