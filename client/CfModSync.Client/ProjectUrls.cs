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
        return "https://www.curseforge.com/minecraft/mc-mods/" + match.Groups[1].Value;
    }

    public static string[] ParseList(IEnumerable<string> lines)
    {
        var urls = new HashSet<string>(StringComparer.Ordinal);
        var lineNumber = 0;
        foreach (var raw in lines)
        {
            lineNumber++;
            var line = raw.Trim().TrimStart('\uFEFF');
            if (line == "" || line.StartsWith('#')) continue;
            try { urls.Add(Normalize(line)); }
            catch (FormatException error) { throw new FormatException($"Line {lineNumber}: {error.Message}"); }
        }
        if (urls.Count is 0 or > 500)
            throw new FormatException("A mod list must contain between 1 and 500 projects.");
        return urls.ToArray();
    }
}
