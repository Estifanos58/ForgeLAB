# ForgeLAB Local Agent Startup Script for Windows
# Launches the ForgeLAB Local Agent on http://127.0.0.1:4142

param(
    [int]$Port = 4142,
    [string]$Backend = "http://localhost:8080",
    [string]$AllowedRoots = "",
    [switch]$ForceRebuild,
    [switch]$NoRebuild
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$backendDir = Join-Path $scriptDir "backend"
if (-not (Test-Path $backendDir)) {
    $backendDir = $scriptDir
}

$agentExe = Join-Path $backendDir "forgelab-agent.exe"

# Determine if rebuild is necessary
$needsRebuild = $false
$rebuildReason = ""

if ($ForceRebuild) {
    $needsRebuild = $true
    $rebuildReason = "force rebuild requested"
} elseif ($NoRebuild) {
    $needsRebuild = $false
} elseif (-not (Test-Path $agentExe)) {
    $needsRebuild = $true
    $rebuildReason = "agent binary does not exist"
} else {
    # Check if any Go source file in backend is newer than the executable
    $exeTime = (Get-Item $agentExe).LastWriteTime
    $newestSource = Get-ChildItem -Path $backendDir -Filter "*.go" -Recurse -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -notmatch "[\\/](\.git|vendor)[\\/]" } |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1

    if ($newestSource -and ($newestSource.LastWriteTime -gt $exeTime)) {
        $needsRebuild = $true
        $rebuildReason = "source '$($newestSource.Name)' ($($newestSource.LastWriteTime)) is newer than binary ($exeTime)"
    }
}

if ($needsRebuild) {
    Write-Host "[ForgeLAB] Rebuilding forgelab-agent.exe from source ($rebuildReason)..." -ForegroundColor Cyan

    # Get git commit identifier
    $commitSha = "dev"
    try {
        $gitCommit = git -C $backendDir rev-parse --short HEAD 2>$null
        if ($LASTEXITCODE -eq 0 -and $gitCommit) {
            $commitSha = $gitCommit.Trim()
        }
    } catch {
        $commitSha = "dev"
    }

    $buildTime = (Get-Date -Format "yyyy-MM-ddTHH:mm:ssK")
    $ldflags = "-X main.CommitSHA=$commitSha -X main.BuildTime=$buildTime"

    Push-Location $backendDir
    try {
        go build -ldflags $ldflags -o forgelab-agent.exe ./cmd/agent
        if ($LASTEXITCODE -ne 0) {
            Write-Error "[ForgeLAB] Failed to build forgelab-agent.exe from source."
            exit 1
        }
        $agentExe = Join-Path $backendDir "forgelab-agent.exe"
        Write-Host "[ForgeLAB] Successfully built forgelab-agent.exe (commit: $commitSha, built: $buildTime)" -ForegroundColor Green
    } finally {
        Pop-Location
    }
}

if (-not (Test-Path $agentExe)) {
    Write-Error "[ForgeLAB] Could not find or build forgelab-agent.exe. Please ensure Go is installed."
    exit 1
}

# Determine binary info for startup announcement
$binaryInfo = ""
try {
    $verOutput = & $agentExe -version 2>$null
    if ($LASTEXITCODE -eq 0 -and $verOutput) {
        $binaryInfo = $verOutput.Trim()
    }
} catch {}

if (-not $binaryInfo) {
    $exeItem = Get-Item $agentExe
    $binaryInfo = "Binary built: $($exeItem.LastWriteTime)"
}

Write-Host "====================================================" -ForegroundColor Green
Write-Host "   ForgeLAB Local Agent for Windows" -ForegroundColor Green
Write-Host "   Build:        $binaryInfo" -ForegroundColor Cyan
Write-Host "   Listening on: http://127.0.0.1:$Port" -ForegroundColor Green
Write-Host "   Backend URL:  $Backend" -ForegroundColor Green
Write-Host "   Binary Path:  $agentExe" -ForegroundColor Gray
Write-Host "   Ready for ForgeLAB browser import requests." -ForegroundColor Cyan
Write-Host "   Press Ctrl+C to stop the agent." -ForegroundColor Yellow
Write-Host "====================================================" -ForegroundColor Green

$agentArgs = @("-port", $Port, "-backend", $Backend)
if ($AllowedRoots) {
    $agentArgs += @("-allowed-roots", $AllowedRoots)
}

$canDirectRun = $false
try {
    $testProc = Start-Process -FilePath $agentExe -ArgumentList "-version" -NoNewWindow -PassThru -Wait -ErrorAction Stop
    if ($testProc.ExitCode -eq 0) {
        $canDirectRun = $true
    }
} catch {}

if ($canDirectRun) {
    & $agentExe @agentArgs
} else {
    Write-Host "[ForgeLAB] Notice: Direct binary execution was restricted by Windows Application Control." -ForegroundColor Yellow
    Write-Host "[ForgeLAB] Running agent using 'go run ./cmd/agent' from current source..." -ForegroundColor Cyan
    Push-Location $backendDir
    try {
        go run ./cmd/agent @agentArgs
    } finally {
        Pop-Location
    }
}

