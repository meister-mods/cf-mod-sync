# cfmodsync — CurseForge Forge Mod Downloader (Go)

[Korean README](README.kr.md)

Add one CurseForge Minecraft **mod project URL** per line to `mods.txt`. The tool searches each project and downloads the most recently listed JAR that matches the **Minecraft version and Forge loader**.

- Default target: **Minecraft 1.20.1 / Forge**. Change it with `MC_VERSION` in `.env` or the `-version` flag.
- Uses the official [CurseForge Core API](https://docs.curseforge.com/rest-api/). It does not scrape web pages, guess `/download/latest` redirects, or hardcode URLs or file IDs.
- Filters API results by `gameVersion` and `modLoaderType=1`, then checks the returned version and loader metadata. Files are ranked by **listing date (`FileDate`)**; this does not guarantee that a mod will run successfully.
- Recursively adds required dependencies (CurseForge `relationType=3`) by default. Other relationships are not added automatically.
- Validates downloads using HTTPS, safe filenames, file size, CurseForge-provided **SHA-1/MD5** checksums, and JAR/ZIP entry CRC and decompression checks. Records a local SHA-256 hash in the manifest.
- Rejects files without a provided checksum by default because their integrity **cannot be verified**. Passing the hash and ZIP checks is not a malware scan or proof that Minecraft will start.
- Revalidates and reuses an already downloaded identical file as `cached`. It does not overwrite a damaged existing file automatically; use `-force` to download it again.
- Writes `manifest.json`; supports `-dry-run` to inspect the plan, `-verify-only` to recheck files offline, and `-prune` to remove obsolete versions managed by a previous run.

## 1. API key required

**Since July 16, 2026, direct downloads from the CurseForge CDN require an API key.** The CurseForge REST API also requires `x-api-key` authentication for searches. Official policy and guidance:

- [CurseForge: CDN API key authentication](https://blog.curseforge.com/introducing-api-key-authentication-for-curseforge-file-downloads/)
- [How to apply for an API key](https://support.curseforge.com/support/solutions/articles/9000208346-about-the-curseforge-api-and-how-to-apply-for-a-key)
- [API documentation](https://docs.curseforge.com/rest-api/)

You must obtain your own approved API key. This project does not provide someone else's key or use an unofficial bypass API. If a project owner disables **third-party distribution**, the API may not provide the file; this tool does not bypass that setting.

## 2. Installation and usage (Windows PowerShell)

Run these commands from the project directory. You can run from source with Go 1.23 or later. A Windows `.exe` build is also provided separately.

```powershell
Copy-Item .env.example .env
notepad .env      # Enter CURSEFORGE_API_KEY=...
notepad mods.txt  # Edit the list of project URLs to download

go run . -dry-run          # Select compatible latest releases via the API and write plan.json
go run .                   # Download and validate JARs
go run . -verify-only      # Revalidate files in manifest.json offline
go run . -force            # Safely redownload existing identical files
```

To use the binary instead of the source, replace `go run .` with `./cfmodsync.exe`.

Example configuration (`.env`):

```dotenv
CURSEFORGE_API_KEY=YOUR_APPROVED_KEY
MC_VERSION=1.20.1
RELEASE_CHANNEL=all
MOD_LIST=mods.txt
AUTO_DEPS=true
```

If `OUTPUT_DIR` is omitted, downloads are **separated by Minecraft version**, for example in `downloads/1.20.1-forge/`. Changing to 1.21.1 also changes the default directory to `downloads/1.21.1-forge/`.

Example input (`mods.txt`):

```text
# One project URL per line. Do not use download/latest, /files/123, or other non-project URLs.
https://www.curseforge.com/minecraft/mc-mods/create
https://www.curseforge.com/minecraft/mc-mods/jei
```

This package's **`mods.txt` is prefilled with 93 project URLs** compiled earlier. Keep only the mods you want. `mods.example.txt` is a much shorter example.

## 3. Command-line options

| Option | Description |
| --- | --- |
| `-version 1.21.1` | Select a specific Minecraft version instead of the one in `.env` |
| `-input my-mods.txt` | Use a different project URL list |
| `-output ./my-mods` | Use a different JAR output directory |
| `-channel release` | Select stable releases only (`beta`: release and beta; `all`: includes alpha) |
| `-deps=false` | Disable automatic addition of required CurseForge dependencies |
| `-workers 4` | Number of concurrent downloads (1–16) |
| `-max-file-mb 1024` | Maximum download size per JAR, in MiB |
| `-dry-run` | Query the API and write `plan.json` without downloading |
| `-verify-only` | Revalidate files from `manifest.json` without internet access or an API key |
| `-force` | Download an existing same-named file again, validate it, then replace it |
| `-prune` | After a successful run, validate and delete only unneeded JARs managed by the previous manifest |

`-prune` runs only at the end of a fully successful download. Avoid running the tool directly in a `.minecraft/mods` directory. Download and validate files in a separate output directory first, then copy the files you need.

## 4. Selection and validation rules

1. Validate the host and path of each supplied URL and extract only an exact `slug`.
2. Confirm the exact project ID using CurseForge `GET /v1/mods/search?gameId=432&classId=6&slug=...`.
3. Paginate `GET /v1/mods/{modId}/files?gameVersion=<MC_VERSION>&modLoaderType=1` **through the last page**.
4. Check `isAvailable`, the Minecraft version, the `.jar` extension, and Forge compatibility; exclude server packs, source packs, and unpublished files. If a file has no loader tags at all, rely on the API's Forge-filtered result.
5. Among candidates in the allowed release channels, select the file with the latest `FileDate`. Break ties by File ID.
6. Use the **actual API download URL** from `GET /v1/mods/{modId}/files/{fileId}/download-url`, not `/download/latest` from a project page.
7. Download to a `.partial` temporary file. Move it to `.jar` only after size, SHA-1/MD5, and ZIP/CRC validation. Send CDN credentials in an `x-api-key` header and remove that header when redirected to another domain.
8. Record failed items in `manifest.json` under `errors` and continue. The process exits with code `1` if any item fails.

## 5. Limitations and cautions

- **Actual external downloads cannot be validated without an official API key.** The code was tested with local mock HTTP servers in unit and integration tests. The API responses and binary downloads for the 93 projects were not tested with an authenticated key.
- Tag compatibility depends on metadata supplied by API publishers. The tool does not automatically resolve minimum Forge versions in `mods.toml`, other mods' version ranges, or conflicts.
- **Vehicle content packs and bundle projects** can switch pack families between releases in the same CurseForge project. For example, Blue Seoul Vehicles may switch from the Garam to the Narae family. The newest project-wide `1.20.1/Forge` file is not guaranteed to succeed the pack you installed. Pin a specific file manually if needed.
- `RELEASE_CHANNEL=all` includes alpha and beta files. For regular play, consider restricting it to `beta` or `release`. Projects that publish only snapshot or alpha builds may then have no selected file.
- Without `-prune`, older JARs for the same mod may remain in the output directory after an update. Downloading directly into a Minecraft mods directory is discouraged.
- Forge and NeoForge are distinct. Changing `MC_VERSION` changes only the **Minecraft target version for Forge**; it does not switch to NeoForge.

## 6. Development and testing

The tool uses only the Go standard library (no external Go modules).

```bash
go test -race ./...
go vet ./...
go build -o cfmodsync .
```

Tests cover URL parsing, newest-file selection, Minecraft version and loader exclusion, release channels, SHA-1 integrity, ZIP CRC, mock-API integration downloads, required dependency addition, rerun caching and damaged-file recovery, and prevention of authentication-header leakage on cross-domain redirects.
