param(
    [string]$PackageSource = "",
    [switch]$SkipTests,
    [switch]$Race
)

$ErrorActionPreference = "Stop"
$workspace = Split-Path -Parent $PSScriptRoot

function Invoke-Checked {
    param([string]$Command, [string[]]$Arguments)
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Command failed with exit code $LASTEXITCODE" }
}

Push-Location $workspace
try {
    $env:DOTNET_CLI_HOME = Join-Path $workspace ".build/dotnet-home"
    $env:DOTNET_SKIP_FIRST_TIME_EXPERIENCE = "1"
    $env:DOTNET_CLI_TELEMETRY_OPTOUT = "1"
    $env:DOTNET_GENERATE_ASPNET_CERTIFICATE = "false"
    $env:MSBuildEnableWorkloadResolver = "false"
    $env:NUGET_PACKAGES = Join-Path $workspace ".build/packages"
    $env:GOMODCACHE = Join-Path $workspace ".build/go-mod-cache"
    $env:GOCACHE = Join-Path $workspace ".build/go-build-cache"
    $clientProject = "client/CfModSync.Client/CfModSync.Client.csproj"
    $checksProject = "client/CfModSync.Client.Checks/CfModSync.Client.Checks.csproj"
    foreach ($project in @($clientProject, $checksProject)) {
        $restoreArgs = @("restore", $project, "-p:NuGetAudit=false")
        if ($PackageSource) { $restoreArgs += @("--source", $PackageSource) }
        Invoke-Checked -Command "dotnet" -Arguments $restoreArgs
        Invoke-Checked -Command "dotnet" -Arguments @("build", $project, "-c", "Release", "--no-restore")
    }
    if (-not $SkipTests) {
        $env:CFMODSYNC_CLIENT_CHECKS = Join-Path $workspace "client/CfModSync.Client.Checks/bin/Release/net10.0/CfModSync.Client.Checks.dll"
        Invoke-Checked -Command "dotnet" -Arguments @($env:CFMODSYNC_CLIENT_CHECKS)
        $testArgs = @("-C", "server", "test", "./...")
        if ($Race) { $testArgs = @("-C", "server", "test", "-race", "./...") }
        Invoke-Checked -Command "go" -Arguments $testArgs
        Invoke-Checked -Command "go" -Arguments @("-C", "server", "vet", "./...")
    }
    $serverDir = Join-Path $workspace "artifacts/server"
    $clientDir = Join-Path $workspace "artifacts/client"
    New-Item -ItemType Directory -Force -Path $serverDir, $clientDir | Out-Null
    # Package only a fresh publish, never a user's .env, .mods, or cache.
    $staging = Join-Path $workspace (".build/package-" + [Guid]::NewGuid().ToString("N"))
    $stagedServer = Join-Path $staging "server"
    $stagedClient = Join-Path $staging "client"
    New-Item -ItemType Directory -Path $stagedServer, $stagedClient -Force | Out-Null
    Invoke-Checked -Command "go" -Arguments @("-C", "server", "build", "-mod=readonly", "-trimpath", "-o", "$stagedServer/cfmodsync.exe", ".")
    Invoke-Checked -Command "dotnet" -Arguments @("publish", $clientProject, "-c", "Release", "--no-restore", "-o", $stagedClient)
    Copy-Item -LiteralPath "server/.env.example" -Destination "$stagedServer/.env.example" -Force
    Copy-Item -LiteralPath "scripts/Start-Server.ps1" -Destination "$stagedServer/Start-Server.ps1" -Force
    Copy-Item -LiteralPath "client/.mods.example" -Destination "$stagedClient/.mods.example" -Force
    $modules = & go -C server list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all
    if ($LASTEXITCODE -ne 0) { throw "Could not list Go dependency licenses." }
    foreach ($module in $modules) {
        $parts = $module.Split('|')
        if ($parts.Length -ne 3 -or -not $parts[1] -or -not $parts[2]) { continue }
        $licenseDir = Join-Path $stagedServer ("licenses/" + ($parts[0] + "@" + $parts[1]).Replace('/', '_'))
        New-Item -ItemType Directory -Path $licenseDir -Force | Out-Null
        Get-ChildItem -LiteralPath $parts[2] -File | Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE)' } |
            Copy-Item -Destination $licenseDir -Force
    }
    $webViewLicenseDir = Join-Path $stagedClient "licenses/WebView2"
    New-Item -ItemType Directory -Path $webViewLicenseDir -Force | Out-Null
    foreach ($name in @("LICENSE.txt", "NOTICE.txt")) {
        Copy-Item -LiteralPath (Join-Path $env:NUGET_PACKAGES "microsoft.web.webview2/1.0.4191.47/$name") -Destination $webViewLicenseDir -Force
    }
    Copy-Item -Path "$stagedServer/*" -Destination $serverDir -Recurse -Force
    Copy-Item -Path "$stagedClient/*" -Destination $clientDir -Recurse -Force
    foreach ($name in @("README.md", "README.kr.md", "LICENSE")) {
        Copy-Item -LiteralPath $name -Destination (Join-Path $workspace "artifacts/$name") -Force
    }
    $archive = Join-Path $workspace "artifacts/cfmodsync-windows.zip"
    Compress-Archive -Path $stagedServer, $stagedClient, "artifacts/README.md", "artifacts/README.kr.md", "artifacts/LICENSE" -DestinationPath $archive -Force
    Write-Host "Built server, Windows client, and package: $archive"
}
finally { Pop-Location }
