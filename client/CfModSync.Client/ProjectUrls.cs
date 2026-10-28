using System.Text.RegularExpressions;

namespace CfModSync.Client;

public static partial class ProjectUrls
{
    [GeneratedRegex(@"^/minecraft/mc-mods/([a-z0-9][a-z0-9-]*)(?:/.*)?$", RegexOptions.CultureInvariant)]
    private static partial Regex ProjectPath();

    public static bool IsCurseForgePage(string raw) =>
        Uri.TryCreate(raw, UriKind.Absolute, out var uri) &&
        uri.Scheme == Uri.UriSchemeHttps && uri.IsDefaultPort && uri.UserInfo == "" &&
        (uri.Host.Equals("curseforge.com", StringComparison.OrdinalIgnoreCase) ||
         uri.Host.EndsWith(".curseforge.com", StringComparison.OrdinalIgnoreCase));

    public static string Normalize(string raw)
    {
        if (!Uri.TryCreate(raw.Trim().TrimStart('\uFEFF'), UriKind.Absolute, out var uri) ||
            uri.Scheme != Uri.UriSchemeHttps || !uri.IsDefaultPort || uri.UserInfo != "" ||
            (uri.Host != "www.curseforge.com" && uri.Host != "curseforge.com"))
            throw new FormatException("Enter a CurseForge Minecraft mod project URL.");
        var match = ProjectPath().Match(uri.AbsolutePath);
        if (!match.Success)
            throw new FormatException("Only mod project links can be added.");
        var project = "https://www.curseforge.com/minecraft/mc-mods/" + match.Groups[1].Value;
        var parts = uri.AbsolutePath.TrimEnd('/').Split('/');
        if (parts.Length == 6 && parts[4] == "files" && parts[5].All(char.IsAsciiDigit))
        {
            if (!long.TryParse(parts[5], out var fileId) || fileId <= 0)
                throw new FormatException("The pinned CurseForge file ID is invalid.");
            return project + "/files/" + fileId;
        }
        return project;
    }

    public static string ProjectKey(string normalized) => new Uri(normalized).AbsolutePath.Split('/')[3];

    public static string[] ParseList(IEnumerable<string> lines)
    {
        var urls = new Dictionary<string, string>(StringComparer.Ordinal);
        var lineNumber = 0;
        foreach (var raw in lines)
        {
            lineNumber++;
            var line = raw.Trim().TrimStart('\uFEFF');
            if (line == "" || line.StartsWith('#')) continue;
            try
            {
                var url = Normalize(line);
                var key = ProjectKey(url);
                if (urls.TryGetValue(key, out var previous) && previous != url &&
                    previous.Contains("/files/") && url.Contains("/files/"))
                    throw new FormatException($"Conflicting pinned files for {key}.");
                if (!urls.TryGetValue(key, out previous) || url.Contains("/files/")) urls[key] = url;
            }
            catch (FormatException error) { throw new FormatException($"Line {lineNumber}: {error.Message}"); }
        }
        if (urls.Count is 0 or > 500)
            throw new FormatException("A mod list must contain between 1 and 500 projects.");
        return urls.Values.ToArray();
    }
}
