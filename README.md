# cfmodsync — Forge-aware Mod Cache Server and Windows Client

[Korean README](README.kr.md)

A Go server keeps the CurseForge API key and verified JAR cache. The English Windows client uses WinForms and WebView2 to import mod lists, collect CurseForge links, select an installed Forge version, and review compatibility before downloading.

## Repository layout

```text
server/                         Go server, CLI, compatibility engine, tests, go.mod
  .env.example                  Server configuration template
client/
  .mods.example                 Example project list
  CfModSync.Client/             Windows client
  CfModSync.Client.Checks/      Client/protocol checks
scripts/                        Build and launch scripts
artifacts/                      Generated binaries and Windows ZIP
```

Local `server/.env`, `client/.mods`, server caches and downloads are excluded from Git. The build packages a fresh staging directory, so an existing deployment's keys, private lists, and cache are not copied into the ZIP.

## Quick start

Build requirements: the Go version in `server/go.mod` and the .NET 10 SDK on Windows. Client runtime requirements: [.NET 10 Desktop Runtime](https://dotnet.microsoft.com/en-us/download/dotnet/10.0) and [WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/).

From the repository root:

```powershell
if (!(Test-Path server/.env)) { Copy-Item server/.env.example server/.env }
notepad server/.env
# Set CURSEFORGE_API_KEY to your approved key.
go -C server run . serve
```

In another terminal:

```powershell
.\scripts\build.ps1
.\artifacts\client\CfModSync.Client.exe
```

The ZIP is `artifacts/cfmodsync-windows.zip`. For a deployment, configure `server/.env` beside the server binary and run `Start-Server.ps1`. Distribute the entire `client/` directory to Windows users.

## Using the client

1. Enter **Server** and, if configured, **Access token**. Click **Connect**.
2. Choose **Minecraft** and **Forge version**. The editable Forge list detects installations in the standard Minecraft versions directory; you can also type a version. Enter the exact build, for example `47.4.28`. The client requires a Forge version before downloading; API/CLI calls without one report unchecked loader requirements.
3. Choose **Release policy**. The default is `release`. Import `.mods` with **Import list**, paste URLs, or select links in the browser. **Add mod links when clicked** collects project/file links; turn it off to browse project pages.
4. Optionally select **Installed mods**: an actual folder containing old JARs. The client sends their metadata and SHA-256 values, not the JARs or filesystem paths. A PowerShell directory listing cannot provide internal dependency information.
5. Choose a clean **Save to** directory and click **Download selected mods**. The server resolves and validates the whole combination before releasing download paths.
6. Inspect **Validation report** for missing projects, version conflicts, CF dependency provenance, prereleases, removals, downgrades and filename-family replacements. **Allow reviewed replacements** acknowledges baseline changes for one request; it never bypasses hard dependency or Forge errors.

The selected list is sorted by project name. Progress, logs, and validation details have separate areas. The UI supports per-monitor DPI scaling and an adjustable browser/list split.

The client reuses identical local files after SHA-256 validation. It refuses an output folder containing unselected JARs, which would leave obsolete or duplicate mods. Failed runs write `failed-job-<id>.json` and preserve the previous successful `manifest.json`. Settings are saved in `%LOCALAPPDATA%\cfmodsync\settings.json`; access tokens are not persisted.

## Version selection and validation

**Exclude prereleases** is enabled by default in the Windows client. It forces release-only selection and also rejects recognized prerelease markers in filenames and declared JAR versions (alpha, beta, snapshot, RC, preview, dev, nightly, and similar markers). It applies to automatic dependencies, pinned files, and cached selections. Pins cannot override this option. If no stable compatible combination exists, the job fails with a report; it never falls back to a prerelease. Uncheck the option to select beta/all policies or use pinned prereleases. The setting is saved locally.

For the standalone CLI, pass `-exclude-prereleases` or set `EXCLUDE_PRERELEASES=true`. Requests to the server API use `"exclude_prereleases": true`; jobs and manifests record the value. `-dry-run` checks API labels and filenames only, since JAR versions require downloading.

- The API identifies exact CurseForge projects and filters Minecraft version and Forge loader. Every requested project is accounted for, including failed projects.
- Candidate JARs are cached and checked for HTTPS delivery, safe filenames, expected size, API SHA-1/MD5, and ZIP/CRC integrity.
- The server reads `META-INF/mods.toml`, the main manifest section, and declared JarJar artifacts. It checks Minecraft, the selected Forge build, required mods, installed optional dependency ranges, and shared library ranges. It handles manifest version substitution, wildcards and Maven soft recommendations. Built-in FML loader requirements use the Forge major version; custom language loaders produce an explicit unchecked warning.
- Within a common mod identity, candidates are ranked by their declared mod version using Maven-style comparison. Publication date and file ID break ties. Backtracking searches for a compatible combination, giving requested projects priority in list order.
- A project with changing/unidentified mod identities cannot be reliably ordered by mod version. It gets an explicit warning and publication-date fallback; use file pins for content families.
- `release` excludes API beta/alpha files and recognized declared prereleases. `beta` permits API release/beta files but rejects declared alpha builds. `all` also includes API alpha files. With **Exclude prereleases** disabled, a specific file pin overrides release policy while still enforcing availability, integrity and runtime requirements.
- Baselines detect missing mod IDs, downgrades, renamed filename families, changed bytes for an unchanged version, and unidentified missing content packs. Review is required before releasing such a batch.
- The default limit is 200 eligible candidates per project; exceeding it fails explicitly. Raise `-candidate-limit` up to 1000 or pin a file. The compatibility search is bounded to 500 projects and 10000 search steps.

The first exact-version scan can download multiple candidate versions. Subsequent requests reuse the persistent cache. Cache eviction is manual. These checks validate declared metadata; Java/runtime features, mixins, content removed within a pack, and actual world/game loading still require a Minecraft test.

Forge references: [mod metadata and dependencies](https://docs.minecraftforge.net/en/1.20.x/gettingstarted/modfiles/), [versioning](https://docs.minecraftforge.net/en/1.20.1/gettingstarted/versioning/), [Jar-in-Jar negotiation](https://docs.minecraftforge.net/en/fg-6.x/dependencies/jarinjar/).

## Project lists and file pins

```text
# Latest compatible version:
https://www.curseforge.com/minecraft/mc-mods/jei

# Exact file pin, for example the previously used Kaminari file:
https://www.curseforge.com/minecraft/mc-mods/kaminari-motor-works/files/6549423
```

Blank lines and `#` comments are ignored. Conflicting pins for one project are rejected. Required CurseForge dependencies are included by default; `required_by` records why a project was added. If those relations omit a mandatory JAR dependency, the server tries an exact project slug derived from its mod ID and verifies the downloaded provider's actual mod ID. Unresolved dependencies still fail validation; add their project URLs explicitly. Other relationship types are not added. If dependency auto-addition is disabled, provide the complete list yourself; JAR dependency checks still apply.

## Standalone CLI and offline audit

Run from the repository root. Relative runtime paths are resolved from `server/` because of `go -C server`.

```powershell
if (!(Test-Path client/.mods)) { Copy-Item client/.mods.example client/.mods }
go -C server run . -forge-version 47.4.28 -channel release
go -C server run . -forge-version 47.4.28 -baseline "C:\mods-before" -output downloads/new-profile
go -C server run . -dry-run
go -C server run . -verify-only
go -C server run . audit -mods "C:\mods-after" -baseline "C:\mods-before" -forge-version 47.4.28 -report reports/audit.json
```

The offline audit reads existing JARs without API access and records local SHA-256 and ZIP/CRC results. Without an API checksum it does not authenticate their source. Directory listings alone support filename/size comparisons, not internal dependency checks.

| CLI option | Purpose |
| --- | --- |
| `-version` | Minecraft version, default `1.20.1` |
| `-forge-version` | Exact installed Forge version; defaults to `FORGE_VERSION` |
| `-channel` | `release`, `beta`, or `all`; environment `RELEASE_CHANNEL` can override the default |
| `-input` | Project list, default `../client/.mods` |
| `-output` | Selected JAR output, default `downloads/<MC>-forge` |
| `-baseline` | Previous JAR directory for review |
| `-allow-reviewed-changes` | Explicitly acknowledge baseline changes |
| `-candidate-limit` | Per-project candidate safety limit |
| `-deps=false` | Disable CF dependency auto-addition |
| `-workers` | Concurrent candidate downloads, 1–16 |
| `-max-file-mb` | Maximum JAR size in MiB, default 1024 |
| `-force` | Safely redownload and replace same-named selected files |
| `-prune` | After success, remove verified obsolete files managed by the previous manifest |
| `-dry-run` | API-only plan; actual JAR/Forge compatibility is `not_checked` |
| `-verify-only` | Offline integrity and declared dependency recheck |

CLI candidates live in `<output>/.candidate-cache/`; only selected files are installed at the output root. A failed CLI run writes `failed-run-*.json` instead of replacing the successful manifest. Pruning never deletes unmanaged or changed files.

## Server options and API

| Server option / environment | Default |
| --- | --- |
| `-listen` / `SERVER_LISTEN` | `127.0.0.1:8080` |
| `-cache` / `SERVER_CACHE_DIR` | `cache` |
| `-token` / `SERVER_TOKEN` | Empty for loopback |
| `-workers` | `4` |
| `-candidate-limit` | `200` |
| `-metadata-ttl` | `15m` |
| `-job-ttl` | `1h` |
| `-max-file-mb` | `1024` |
| `-tls-cert` / `-tls-key` | Optional certificate/key PEM files |

Remote listening requires a bearer token of at least 32 characters. Remote clients require HTTPS, either directly or through a reverse proxy. HTTP is accepted by the desktop client only on loopback. The CurseForge key stays on the server, and upstream URLs are not exposed. Use one server process per cache directory.

The cache uses `cache/files/<mod-id>/<file-id>/` and `cache/selections/`. File fills are shared across simultaneous requests; canceling one waiter does not cancel a shared fill. Completed jobs are kept in memory for `-job-ttl`, with eight active jobs and up to 128 retained records. Files/selections survive restarts; job IDs do not.

| Method | Route |
| --- | --- |
| `GET` | `/healthz` — public health |
| `GET` | `/api/v1/status` — authenticated connection check |
| `POST` | `/api/v1/jobs` — create an asynchronous job |
| `GET` | `/api/v1/jobs/{id}` — progress and validation report |
| `DELETE` | `/api/v1/jobs/{id}` — cancel |
| `GET` | `/api/v1/files/{modId}/{fileId}` — verified JAR, HTTP Range supported |

```json
{
  "project_urls": ["https://www.curseforge.com/minecraft/mc-mods/jei"],
  "minecraft_version": "1.20.1",
  "forge_version": "47.4.28",
  "release_channel": "release",
  "include_dependencies": true,
  "refresh": false,
  "allow_reviewed_changes": false
}
```

Optional `installed_files` contains `filename`, `sha256`, `mods_toml` and main-section `manifest` strings. Requests are limited to 8 MiB and 500 projects/baseline files. States are `queued`, `resolving`, `validating`, `completed`, `failed` and `cancelled`. Progress details describe candidate inspection. Failed or unreviewed batches have no download paths.

## API access and development

Obtain your own [approved CurseForge API key](https://support.curseforge.com/support/solutions/articles/9000208346-about-the-curseforge-api-and-how-to-apply-for-a-key). The tool uses the [official API](https://docs.curseforge.com/rest-api/) and respects project availability and third-party distribution settings. Missing/withheld files are reported; pins do not bypass distribution restrictions.

```powershell
go -C server test ./...
go -C server vet ./...
.\scripts\build.ps1
```

`scripts/build.ps1` also runs a real C# client against a Go server with a local mock CurseForge service, then packages both components and dependency licenses. `-Race` requires CGO and a supported C compiler. Regression tests cover Forge-specific version selection, dependency backtracking, missing requests, baseline review, cache persistence/repair, concurrent fills, credential redaction and client hash validation.
