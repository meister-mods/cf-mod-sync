using System.IO.Compression;
using System.Security.Cryptography;
using System.Text;

namespace CfModSync.Client;

public sealed record InstalledFile(string Filename, string Sha256, string ModsToml, string Manifest);

public static class BaselineReader
{
    public static Task<InstalledFile[]> ReadAsync(string folder, IProgress<string>? progress, CancellationToken cancellation) =>
        Task.Run(async () =>
        {
            if (string.IsNullOrWhiteSpace(folder)) return [];
            if (File.Exists(folder)) throw new IOException("Choose a folder containing actual JAR files, not a directory listing.");
            var paths = Directory.GetFiles(Path.GetFullPath(folder), "*.jar").Order(StringComparer.OrdinalIgnoreCase).ToArray();
            if (paths.Length is 0 or > 500) throw new IOException("The installed mods folder must contain between 1 and 500 JARs.");
            var result = new List<InstalledFile>();
            foreach (var path in paths)
            {
                cancellation.ThrowIfCancellationRequested();
                var info = new FileInfo(path);
                if ((info.Attributes & FileAttributes.ReparsePoint) != 0 || info.Length is <= 0 or > 4294967296L)
                    throw new IOException($"Unsupported baseline file: {info.Name}");
                progress?.Report($"Reading installed mods: {result.Count + 1}/{paths.Length} · {info.Name}");
                string toml, manifest;
                using (var archive = ZipFile.OpenRead(path))
                {
                    toml = ReadText(archive, "META-INF/mods.toml");
                    manifest = ReadText(archive, "META-INF/MANIFEST.MF");
                }
                await using var stream = File.OpenRead(path);
                var hash = Convert.ToHexString(await SHA256.HashDataAsync(stream, cancellation)).ToLowerInvariant();
                result.Add(new InstalledFile(info.Name, hash, toml, manifest));
            }
            return result.ToArray();
        }, cancellation);

    private static string ReadText(ZipArchive archive, string name)
    {
        var entry = archive.GetEntry(name);
        if (entry == null) return "";
        if (name == "META-INF/MANIFEST.MF")
        {
            using var manifestStream = entry.Open();
            using var reader = new StreamReader(manifestStream, Encoding.UTF8, true, 4096);
            var main = new StringBuilder();
            var previousNewline = false;
            int value;
            while ((value = reader.Read()) >= 0)
            {
                if (value == '\r') continue;
                if (main.Length >= 65536) throw new IOException("The JAR manifest main section exceeds 64 KiB.");
                main.Append((char)value);
                if (value == '\n' && previousNewline) break;
                previousNewline = value == '\n';
            }
            return main.ToString();
        }
        if (entry.Length > 65536) throw new IOException($"{name} exceeds the 64 KiB metadata limit.");
        using var source = entry.Open();
        using var memory = new MemoryStream();
        var buffer = new byte[4096];
        int count;
        while ((count = source.Read(buffer)) > 0)
        {
            if (memory.Length + count > 65536) throw new IOException($"{name} exceeds the metadata limit.");
            memory.Write(buffer, 0, count);
        }
        return Encoding.UTF8.GetString(memory.ToArray()).TrimStart('\uFEFF');
    }
}
