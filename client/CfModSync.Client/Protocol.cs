using System.Text.Json;

namespace CfModSync.Client;

public static class Protocol
{
    public static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web)
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
        WriteIndented = true
    };
}

public sealed record JobRequest(
    string[] ProjectUrls, string MinecraftVersion, string ReleaseChannel,
    bool IncludeDependencies, bool Refresh);

public sealed record JobSnapshot
{
    public string Id { get; init; } = "";
    public string State { get; init; } = "";
    public string MinecraftVersion { get; init; } = "";
    public string ReleaseChannel { get; init; } = "";
    public int Total { get; init; }
    public int Completed { get; init; }
    public int CacheHits { get; init; }
    public bool SelectionCached { get; init; }
    public JarFile[] Files { get; init; } = [];
    public JobIssue[] Errors { get; init; } = [];
    public bool IsTerminal => State is "completed" or "failed" or "cancelled";
}

public sealed record JobIssue(string Project, string Error);
public sealed record ApiHash(string Value, int Algo);
public sealed record VerifiedFile(long SizeBytes, string Sha256, string Integrity);

public sealed record JarFile
{
    public string Name { get; init; } = "";
    public string Slug { get; init; } = "";
    public string ProjectUrl { get; init; } = "";
    public bool Requested { get; init; }
    public long ModId { get; init; }
    public long FileId { get; init; }
    public string Filename { get; init; } = "";
    public string FileDate { get; init; } = "";
    public string ReleaseType { get; init; } = "";
    public string[] GameVersions { get; init; } = [];
    public ApiHash[] ApiHashes { get; init; } = [];
    public long ExpectedLength { get; init; }
    public long[] RequiredDependencyIds { get; init; } = [];
    public string FilePageUrl { get; init; } = "";
    public string Status { get; init; } = "";
    public VerifiedFile? Verified { get; init; }
    public string Error { get; init; } = "";
    public string DownloadPath { get; init; } = "";
}

public sealed record ClientManifest(
    int SchemaVersion, string GeneratedAt, string MinecraftVersion, string ModLoader,
    string ReleaseChannel, JarFile[] Entries, JobIssue[] Errors);

public sealed record TransferProgress(string Filename, long Received, long Total);
