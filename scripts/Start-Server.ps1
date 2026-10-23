param([Parameter(ValueFromRemainingArguments = $true)][string[]]$ServerArgs)

$ErrorActionPreference = "Stop"
$serverRoot = $PSScriptRoot
$serverExe = Join-Path $serverRoot "cfmodsync.exe"
if (-not (Test-Path -LiteralPath $serverExe)) {
    $serverRoot = Split-Path -Parent $PSScriptRoot
}
Push-Location $serverRoot
try {
    if (Test-Path -LiteralPath $serverExe) {
        & $serverExe serve @ServerArgs
    }
    else {
        & go run . serve @ServerArgs
    }
    exit $LASTEXITCODE
}
finally { Pop-Location }
