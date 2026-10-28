using System.Text.RegularExpressions;

namespace CfModSync.Client;

public static partial class ForgeVersions
{
    [GeneratedRegex(@"^(?<minecraft>\d+\.\d+(?:\.\d+)?)-forge-(?<forge>\d+(?:\.\d+){1,3})$")]
    private static partial Regex StandardFolder();
    [GeneratedRegex(@"^forge-(?<minecraft>\d+\.\d+(?:\.\d+)?)-(?<forge>\d+(?:\.\d+){1,3})$")]
    private static partial Regex AlternateFolder();

    public static string[] InstalledFor(string minecraftVersion)
    {
        var root = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData), ".minecraft", "versions");
        if (!Directory.Exists(root)) return [];
        try
        {
            return Directory.GetDirectories(root).Select(Path.GetFileName).Select(name =>
            {
                var match = StandardFolder().Match(name ?? "");
                if (!match.Success) match = AlternateFolder().Match(name ?? "");
                return match.Success && match.Groups["minecraft"].Value == minecraftVersion ? match.Groups["forge"].Value : "";
            }).Where(version => version != "").Distinct().OrderByDescending(version => Version.TryParse(version, out var value) ? value : new Version(0, 0)).ToArray();
        }
        catch (IOException) { return []; }
        catch (UnauthorizedAccessException) { return []; }
    }
}
