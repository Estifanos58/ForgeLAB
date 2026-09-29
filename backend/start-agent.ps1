# ForgeLAB Local Agent Startup Script for Windows
# Launches the ForgeLAB Local Agent on http://127.0.0.1:4142

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$agentExe = Join-Path $scriptDir "forgelab-agent.exe"

# If running directly from root directory
if (-not (Test-Path $agentExe)) {
    $agentExe = Join-Path $scriptDir "backend\forgelab-agent.exe"
}

# If executable doesn't exist, build it from source
if (-not (Test-Path $agentExe)) {
    Write-Host "[ForgeLAB] Building forgelab-agent.exe from source..." -ForegroundColor Cyan
    Push-Location $scriptDir
    try {
        go build -o forgelab-agent.exe ./cmd/agent
        $agentExe = Join-Path $scriptDir "forgelab-agent.exe"
    } finally {
        Pop-Location
    }
}

if (-not (Test-Path $agentExe)) {
    Write-Error "[ForgeLAB] Could not find or build forgelab-agent.exe. Please ensure Go is installed."
    exit 1
}

Write-Host "====================================================" -ForegroundColor Green
Write-Host "   ForgeLAB Local Agent for Windows" -ForegroundColor Green
Write-Host "   Listening on: http://127.0.0.1:4142" -ForegroundColor Green
Write-Host "   Backend URL:  http://localhost:8080" -ForegroundColor Green
Write-Host "   Ready for ForgeLAB browser import requests." -ForegroundColor Cyan
Write-Host "   Press Ctrl+C to stop the agent." -ForegroundColor Yellow
Write-Host "====================================================" -ForegroundColor Green

& $agentExe -port 4142 -backend http://localhost:8080
