# ForgeLAB Local Agent Startup Script for Windows (backend convenience wrapper)
# Delegates to the repository root start-agent.ps1 when present so interactive session
# validation, auto-rebuild, Application Control fallback, and listener readiness match.

param(
    [string]$HostAddr = "127.0.0.1",
    [int]$Port = 4142,
    [string]$Backend = "http://localhost:8080",
    [string]$AllowedRoots = "",
    [switch]$ForceRebuild,
    [switch]$NoRebuild,
    [switch]$AllowNonInteractive
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$rootScript = Join-Path (Split-Path -Parent $scriptDir) "start-agent.ps1"

if (Test-Path $rootScript) {
    $forwardParams = @{
        HostAddr = $HostAddr
        Port     = $Port
        Backend  = $Backend
    }
    if ($AllowedRoots) { $forwardParams.AllowedRoots = $AllowedRoots }
    if ($ForceRebuild) { $forwardParams.ForceRebuild = $true }
    if ($NoRebuild) { $forwardParams.NoRebuild = $true }
    if ($AllowNonInteractive) { $forwardParams.AllowNonInteractive = $true }

    & $rootScript @forwardParams
    exit $LASTEXITCODE
}

# Fallback standalone execution when invoked without the repository root script
$currentSessionId = (Get-Process -Id $PID).SessionId
$isUserInteractive = [Environment]::UserInteractive
if (-not $AllowNonInteractive -and ($currentSessionId -eq 0 -or -not $isUserInteractive)) {
    Write-Error "[ForgeLAB] Non-interactive session detected (SessionId=$currentSessionId, UserInteractive=$isUserInteractive). Launch from the interactive logged-in user session or pass -AllowNonInteractive."
    exit 1
}

$agentExe = Join-Path $scriptDir "forgelab-agent.exe"
if ($ForceRebuild -or (-not $NoRebuild -and -not (Test-Path $agentExe))) {
    Write-Host "[ForgeLAB] Building forgelab-agent.exe from source..." -ForegroundColor Cyan
    Push-Location $scriptDir
    try {
        go build -o forgelab-agent.exe ./cmd/agent
        if ($LASTEXITCODE -ne 0) {
            exit 1
        }
    } finally {
        Pop-Location
    }
}

$agentArgs = @("-host", $HostAddr, "-port", $Port, "-backend", $Backend)
if ($AllowedRoots) {
    $agentArgs += @("-allowed-roots", $AllowedRoots)
}

Write-Host "[ForgeLAB] Starting ForgeLAB Local Agent (target http://$HostAddr`:$Port, session $currentSessionId)..." -ForegroundColor Cyan
Write-Host "[ForgeLAB] Readiness will be confirmed once the HTTP listener binds." -ForegroundColor Gray

$canDirectRun = $false
if (Test-Path $agentExe) {
    try {
        $testProc = Start-Process -FilePath $agentExe -ArgumentList "-version" -NoNewWindow -PassThru -Wait -ErrorAction Stop
        if ($testProc.ExitCode -eq 0) {
            $canDirectRun = $true
        }
    } catch {}
}

if ($canDirectRun) {
    & $agentExe @agentArgs
    exit $LASTEXITCODE
}

Push-Location $scriptDir
try {
    go run ./cmd/agent @agentArgs
    exit $LASTEXITCODE
} finally {
    Pop-Location
}
