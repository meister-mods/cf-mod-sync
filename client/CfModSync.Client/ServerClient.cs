using System.Net;
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace CfModSync.Client;

public sealed partial class ServerClient : IDisposable
{
    private readonly HttpClient http;

    public ServerClient(string address, string token)
    {
        if (!Uri.TryCreate(address.Trim(), UriKind.Absolute, out var uri) ||
            (uri.Scheme != "http" && uri.Scheme != "https") || uri.UserInfo != "" ||
            uri.Query != "" || uri.Fragment != "" || uri.AbsolutePath != "/")
            throw new ArgumentException("서버 주소는 http(s)://호스트:포트 형식이어야 합니다.");
        if (uri.Scheme == "http" && !uri.IsLoopback)
            throw new ArgumentException("원격 서버에는 HTTPS를 사용해 주세요. 서버 TLS 또는 HTTPS 역방향 프록시를 설정할 수 있습니다.");
        // An upstream redirect must never carry the server bearer token.
        http = new HttpClient(new HttpClientHandler { AllowAutoRedirect = false })
        {
            BaseAddress = uri,
            Timeout = Timeout.InfiniteTimeSpan
        };
        if (!string.IsNullOrWhiteSpace(token))
            http.DefaultRequestHeaders.Authorization = new AuthenticationHeaderValue("Bearer", token.Trim());
    }

    public async Task CheckAsync(CancellationToken cancellation)
    {
        using var response = await SendAsync(HttpMethod.Get, "api/v1/status", null, cancellation);
        var status = await response.Content.ReadFromJsonAsync<JsonElement>(Protocol.Json, cancellation);
        if (status.GetProperty("protocol_version").GetInt32() != 1)
            throw new IOException("지원되지 않는 서버 프로토콜입니다.");
    }

    public async Task<JobSnapshot> CreateJobAsync(JobRequest request, CancellationToken cancellation)
    {
        using var response = await SendAsync(HttpMethod.Post, "api/v1/jobs", request, cancellation);
        return await ReadJob(response, cancellation);
    }

    public async Task<JobSnapshot> GetJobAsync(string id, CancellationToken cancellation)
    {
        ValidateJobId(id);
        using var response = await SendAsync(HttpMethod.Get, $"api/v1/jobs/{id}", null, cancellation);
        return await ReadJob(response, cancellation);
    }

    public async Task CancelJobAsync(string id, CancellationToken cancellation)
    {
        ValidateJobId(id);
        using var response = await SendAsync(HttpMethod.Delete, $"api/v1/jobs/{id}", null, cancellation);
    }

    [GeneratedRegex("^[a-f0-9]{32}$")]
    private static partial Regex JobId();
    [GeneratedRegex(@"^/api/v1/files/[1-9][0-9]*/[1-9][0-9]*$")]
    private static partial Regex DownloadRoute();
    [GeneratedRegex("^[a-fA-F0-9]{64}$")]
    private static partial Regex Sha256();

    private static void ValidateJobId(string id)
    {
        if (!JobId().IsMatch(id)) throw new IOException("서버가 유효하지 않은 작업 ID를 반환했습니다.");
    }

    private static async Task<JobSnapshot> ReadJob(HttpResponseMessage response, CancellationToken cancellation) =>
        await response.Content.ReadFromJsonAsync<JobSnapshot>(Protocol.Json, cancellation)
        ?? throw new IOException("서버 작업 응답이 비어 있습니다.");

    private async Task<HttpResponseMessage> SendAsync(HttpMethod method, string path, object? value, CancellationToken cancellation)
    {
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellation);
        timeout.CancelAfter(TimeSpan.FromSeconds(45));
        using var request = new HttpRequestMessage(method, path);
        if (value != null) request.Content = JsonContent.Create(value, options: Protocol.Json);
        var response = await http.SendAsync(request, HttpCompletionOption.ResponseContentRead, timeout.Token);
        try { await EnsureSuccess(response, timeout.Token); }
        catch { response.Dispose(); throw; }
        return response;
    }

    private static async Task EnsureSuccess(HttpResponseMessage response, CancellationToken cancellation)
    {
        if (response.IsSuccessStatusCode) return;
        string detail = response.StatusCode switch
        {
            HttpStatusCode.Unauthorized => "서버 접속 토큰을 확인해 주세요.",
            HttpStatusCode.TooManyRequests => "서버가 처리 중인 작업이 많습니다. 잠시 후 다시 시도해 주세요.",
            _ => response.ReasonPhrase ?? "서버 요청 실패"
        };
        if (response.Content.Headers.ContentLength is > 0 and < 65536)
        {
            try
            {
                var body = await response.Content.ReadFromJsonAsync<JsonElement>(Protocol.Json, cancellation);
                if (body.TryGetProperty("error", out var error)) detail = error.GetString() ?? detail;
            }
            catch (JsonException) { }
        }
        throw new IOException($"HTTP {(int)response.StatusCode}: {detail}");
    }

    public static bool SafeFilename(string filename)
    {
        if (string.IsNullOrEmpty(filename) || filename.Length > 240 ||
            filename.StartsWith('.') || !filename.EndsWith(".jar", StringComparison.OrdinalIgnoreCase) ||
            filename.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0 ||
            filename.Contains('/') || filename.Contains('\\') || filename.Contains(':') ||
            filename.EndsWith(' ') || filename.EndsWith('.')) return false;
        var stem = filename.Split('.')[0].ToUpperInvariant();
        return stem is not ("CON" or "PRN" or "AUX" or "NUL") &&
            !(stem.Length == 4 && (stem.StartsWith("COM") || stem.StartsWith("LPT")) && stem[3] is >= '1' and <= '9');
    }

    public async Task<string> DownloadAsync(JarFile file, string output, IProgress<TransferProgress>? progress, CancellationToken cancellation)
    {
        if (!SafeFilename(file.Filename) || !DownloadRoute().IsMatch(file.DownloadPath) ||
            file.Verified is not { SizeBytes: > 0 } verified || !Sha256().IsMatch(verified.Sha256))
            throw new IOException("서버가 유효하지 않은 파일 정보를 반환했습니다.");
        Directory.CreateDirectory(output);
        var target = Path.Combine(Path.GetFullPath(output), file.Filename);
        if (File.Exists(target) && new FileInfo(target).Length == verified.SizeBytes)
        {
            await using var existing = File.OpenRead(target);
            var hash = Convert.ToHexString(await SHA256.HashDataAsync(existing, cancellation));
            if (hash.Equals(verified.Sha256, StringComparison.OrdinalIgnoreCase))
            {
                progress?.Report(new(file.Filename, verified.SizeBytes, verified.SizeBytes));
                return "cached";
            }
        }
        var temporary = Path.Combine(output, $".cfmodsync-{Guid.NewGuid():N}.partial");
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellation);
        timeout.CancelAfter(TimeSpan.FromMinutes(30));
        try
        {
            using var response = await http.GetAsync(file.DownloadPath.TrimStart('/'), HttpCompletionOption.ResponseHeadersRead, timeout.Token);
            await EnsureSuccess(response, timeout.Token);
            if (response.Content.Headers.ContentLength is long length && length != verified.SizeBytes)
                throw new IOException("서버 파일 크기가 작업 정보와 다릅니다.");
            using var hasher = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
            await using (var destination = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None, 65536, true))
            await using (var source = await response.Content.ReadAsStreamAsync(timeout.Token))
            {
                var buffer = new byte[65536];
                long received = 0;
                int count;
                while ((count = await source.ReadAsync(buffer, timeout.Token)) != 0)
                {
                    received += count;
                    if (received > verified.SizeBytes) throw new IOException("파일이 예상 크기를 초과했습니다.");
                    await destination.WriteAsync(buffer.AsMemory(0, count), timeout.Token);
                    hasher.AppendData(buffer, 0, count);
                    progress?.Report(new(file.Filename, received, verified.SizeBytes));
                }
                if (received != verified.SizeBytes ||
                    !Convert.ToHexString(hasher.GetHashAndReset()).Equals(verified.Sha256, StringComparison.OrdinalIgnoreCase))
                    throw new IOException("다운로드한 파일의 SHA-256 또는 크기 검증에 실패했습니다.");
                await destination.FlushAsync(timeout.Token);
            }
            File.Move(temporary, target, true);
            return "downloaded";
        }
        finally
        {
            if (File.Exists(temporary)) File.Delete(temporary);
        }
    }

    public static async Task WriteManifestAsync(string output, JobSnapshot job, JarFile[] files, JobIssue[] errors, CancellationToken cancellation)
    {
        Directory.CreateDirectory(output);
        var path = Path.Combine(output, $".manifest-{Guid.NewGuid():N}.partial");
        try
        {
            var manifest = new ClientManifest(1, DateTimeOffset.UtcNow.ToString("O"), job.MinecraftVersion,
                "Forge", job.ReleaseChannel, files, errors);
            await File.WriteAllTextAsync(path, JsonSerializer.Serialize(manifest, Protocol.Json), cancellation);
            File.Move(path, Path.Combine(output, "manifest.json"), true);
        }
        finally { if (File.Exists(path)) File.Delete(path); }
    }

    public void Dispose() => http.Dispose();
}
