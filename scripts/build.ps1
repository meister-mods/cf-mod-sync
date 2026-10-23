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
        $testArgs = @("test", "./...")
        if ($Race) { $testArgs = @("test", "-race", "./...") }
        Invoke-Checked -Command "go" -Arguments $testArgs
        Invoke-Checked -Command "go" -Arguments @("vet", "./...")
    }
    $serverDir = Join-Path $workspace "artifacts/server"
    $clientDir = Join-Path $workspace "artifacts/client"
    New-Item -ItemType Directory -Force -Path $serverDir, $clientDir | Out-Null
    Invoke-Checked -Command "go" -Arguments @("build", "-trimpath", "-o", "$serverDir/cfmodsync.exe", ".")
    Invoke-Checked -Command "dotnet" -Arguments @("publish", $clientProject, "-c", "Release", "--no-restore", "-o", $clientDir)
    Copy-Item -LiteralPath ".env.example" -Destination "$serverDir/.env.example" -Force
    Copy-Item -LiteralPath "scripts/Start-Server.ps1" -Destination "$serverDir/Start-Server.ps1" -Force
    Copy-Item -LiteralPath ".mods.example" -Destination "$clientDir/.mods.example" -Force
    foreach ($name in @("README.md", "README.kr.md", "LICENSE")) {
        Copy-Item -LiteralPath $name -Destination (Join-Path $workspace "artifacts/$name") -Force
    }
    $archive = Join-Path $workspace "artifacts/cfmodsync-windows.zip"
    Compress-Archive -Path $serverDir, $clientDir, "artifacts/README.md", "artifacts/README.kr.md", "artifacts/LICENSE" -DestinationPath $archive -Force
    Write-Host "Built server, Windows client, and package: $archive"
}
finally { Pop-Location }
