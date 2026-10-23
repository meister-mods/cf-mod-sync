using System.Security.Cryptography;
using System.Text.Json;
using CfModSync.Client;

static void Require(bool condition, string message)
{
    if (!condition) throw new Exception(message);
}

static async Task MustFail(Func<Task> operation, string message)
{
    try { await operation(); }
    catch (Exception error) when (error is IOException or ArgumentException or FormatException or OperationCanceledException) { return; }
    throw new Exception(message);
}

var parsed = ProjectUrls.ParseList([
    "\uFEFF# comment", "", "https://www.curseforge.com/minecraft/mc-mods/create",
    "https://curseforge.com/minecraft/mc-mods/create/files/all?gameVersion=1.20.1",
    "https://www.curseforge.com/minecraft/mc-mods/jei"]);
Require(parsed.Length == 2, "import must canonicalize and deduplicate URLs");
await MustFail(() => Task.Run(() => ProjectUrls.Normalize("https://evil.example/minecraft/mc-mods/create")), "accepted foreign URL");
await MustFail(() => Task.Run(() => ProjectUrls.Normalize("http://www.curseforge.com/minecraft/mc-mods/create")), "accepted HTTP project URL");
await MustFail(() => Task.Run(() => new ServerClient("http://192.0.2.1:8080", "token").Dispose()), "accepted insecure remote server");
foreach (var bad in new[] { "../escape.jar", "CON.jar", "lpt1.jar", ".hidden.jar", "dir\\escape.jar" })
    Require(!ServerClient.SafeFilename(bad), "unsafe filename: " + bad);

if (args.Length != 0)
{
    Require(args.Length == 3, "expected server URL, token, and output directory");
    var output = args[2];
    using var cancellation = new CancellationTokenSource(TimeSpan.FromMinutes(2));
    var ct = cancellation.Token;
    using var api = new ServerClient(args[0], args[1]);
    await api.CheckAsync(ct);
    var request = new JobRequest([parsed[0]], "1.20.1", "all", true, false);
    var job = await api.CreateJobAsync(request, ct);
    while (!job.IsTerminal)
    {
        await Task.Delay(25, ct);
        job = await api.GetJobAsync(job.Id, ct);
    }
    Require(job.State == "completed" && job.Files.Length == 2, "Go job protocol or dependency selection failed");
    var local = new List<JarFile>();
    foreach (var file in job.Files)
    {
        Require(file.Verified is { SizeBytes: > 0 } && file.ApiHashes.Length > 0, "file metadata was not deserialized");
        Require(await api.DownloadAsync(file, output, null, ct) == "downloaded", "first transfer was not downloaded");
        Require(await api.DownloadAsync(file, output, null, ct) == "cached", "local file was not revalidated and reused");
        local.Add(file with { Status = "downloaded" });
    }
    await ServerClient.WriteManifestAsync(output, job, local.ToArray(), [], ct);
    using (var manifest = JsonDocument.Parse(await File.ReadAllTextAsync(Path.Combine(output, "manifest.json"), ct)))
    {
        Require(manifest.RootElement.GetProperty("schema_version").GetInt32() == 1, "wrong manifest schema");
        Require(manifest.RootElement.GetProperty("entries").GetArrayLength() == 2, "manifest entries missing");
    }
    var original = job.Files[0];
    var path = Path.Combine(output, original.Filename);
    var before = SHA256.HashData(await File.ReadAllBytesAsync(path, ct));
    var wrongHash = original with { Verified = original.Verified! with { Sha256 = new string('0', 64) } };
    await MustFail(() => api.DownloadAsync(wrongHash, output, null, ct), "accepted wrong SHA-256");
    var after = SHA256.HashData(await File.ReadAllBytesAsync(path, ct));
    Require(before.SequenceEqual(after), "failed transfer overwrote existing bytes");
    Require(Directory.GetFiles(output, "*.partial").Length == 0, "temporary transfer files leaked");
    await MustFail(() => api.DownloadAsync(original with { Filename = "../escape.jar" }, output, null, ct), "accepted path traversal");
    await MustFail(() => api.DownloadAsync(original with { DownloadPath = "https://evil.example/file" }, output, null, ct), "accepted foreign download URL");
    await File.WriteAllTextAsync(path, "damaged local file", ct);
    Require(await api.DownloadAsync(original, output, null, ct) == "downloaded", "damaged local file was not repaired");
    var second = await api.CreateJobAsync(request, ct);
    while (!second.IsTerminal)
    {
        await Task.Delay(25, ct);
        second = await api.GetJobAsync(second.Id, ct);
    }
    Require(second.CacheHits == 2 && second.SelectionCached, "second job did not use the server cache");
}
Console.WriteLine("PASS: client URL import, protocol, transfer, integrity, cache, and manifest checks.");
