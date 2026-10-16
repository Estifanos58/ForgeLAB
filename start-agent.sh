#!/usr/bin/env bash
# ForgeLAB Local Agent Startup Script for Linux / macOS
# Launches the ForgeLAB Local Agent on http://0.0.0.0:4142 (accessible by host browser & Docker containers)

set -euo pipefail

HOST_ADDR="${HOST:-0.0.0.0}"
PORT="${PORT:-4142}"
BACKEND_URL="${BACKEND:-http://localhost:8080}"
ALLOWED_ROOTS="${ALLOWED_ROOTS:-}"
FORCE_REBUILD=0

# Parse CLI flags
while [[ $# -gt 0 ]]; do
    case "$1" in
        --help)
            echo "ForgeLAB Local Agent Startup Script"
            echo "Usage: ./start-agent.sh [--port 4142] [--host 0.0.0.0] [--backend http://localhost:8080] [--allowed-roots /path] [--rebuild]"
            exit 0
            ;;
        --host)
            HOST_ADDR="$2"
            shift 2
            ;;
        --port|-p)
            PORT="$2"
            shift 2
            ;;
        --backend|-b)
            BACKEND_URL="$2"
            shift 2
            ;;
        --allowed-roots|-r)
            ALLOWED_ROOTS="$2"
            shift 2
            ;;
        --rebuild|-f)
            FORCE_REBUILD=1
            shift
            ;;
        *)
            echo "Unknown argument: $1" >&2
            echo "Usage: ./start-agent.sh [--port 4142] [--host 127.0.0.1] [--backend http://localhost:8080] [--allowed-roots /path] [--rebuild]" >&2
            exit 1
            ;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="${SCRIPT_DIR}/backend"
if [[ ! -d "$BACKEND_DIR" ]]; then
    BACKEND_DIR="$SCRIPT_DIR"
fi

AGENT_BIN="${BACKEND_DIR}/forgelab-agent"

# Check if port is already in use
if command -v ss >/dev/null 2>&1; then
    if ss -tulpn 2>/dev/null | grep -q ":${PORT} "; then
        echo -e "\033[31m[ForgeLAB] Startup Error: Port ${PORT} is already in use!\033[0m" >&2
        exit 1
    fi
elif command -v lsof >/dev/null 2>&1; then
    if lsof -i ":${PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
        echo -e "\033[31m[ForgeLAB] Startup Error: Port ${PORT} is already in use!\033[0m" >&2
        exit 1
    fi
fi

# Build if binary doesn't exist, forced, or sources are newer
NEEDS_REBUILD=0
if [[ "$FORCE_REBUILD" -eq 1 ]]; then
    NEEDS_REBUILD=1
elif [[ ! -x "$AGENT_BIN" ]]; then
    NEEDS_REBUILD=1
else
    # Check if any Go files or mod files are newer than the binary
    NEWEST_SRC=$(find "$BACKEND_DIR" -type f \( -name "*.go" -o -name "go.mod" -o -name "go.sum" \) ! -path "*/.git/*" ! -path "*/vendor/*" -newer "$AGENT_BIN" -print -quit 2>/dev/null || true)
    if [[ -n "$NEWEST_SRC" ]]; then
        NEEDS_REBUILD=1
    fi
fi

if [[ "$NEEDS_REBUILD" -eq 1 ]]; then
    if ! command -v go >/dev/null 2>&1; then
        echo -e "\033[31m[ForgeLAB] Error: 'go' is not installed or not in PATH. Please install Go to build the agent.\033[0m" >&2
        exit 1
    fi

    echo -e "\033[36m[ForgeLAB] Building forgelab-agent from source...\033[0m"
    COMMIT_SHA="dev"
    if command -v git >/dev/null 2>&1; then
        COMMIT_SHA=$(git -C "$BACKEND_DIR" rev-parse --short HEAD 2>/dev/null || echo "dev")
    fi
    BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    LDFLAGS="-X main.CommitSHA=${COMMIT_SHA} -X main.BuildTime=${BUILD_TIME}"

    (
        cd "$BACKEND_DIR"
        go build -ldflags "${LDFLAGS}" -o forgelab-agent ./cmd/agent
    )
    chmod +x "$AGENT_BIN"
    echo -e "\033[32m[ForgeLAB] Successfully built forgelab-agent (commit: ${COMMIT_SHA})\033[0m"
fi

ARGS=("-host" "$HOST_ADDR" "-port" "$PORT" "-backend" "$BACKEND_URL")
if [[ -n "$ALLOWED_ROOTS" ]]; then
    ARGS+=("-allowed-roots" "$ALLOWED_ROOTS")
fi

echo -e "\033[32m====================================================\033[0m"
echo -e "\033[32m   ForgeLAB Local Agent for Linux/macOS\033[0m"
echo -e "\033[36m   Listening on: http://${HOST_ADDR}:${PORT} (browser access: http://127.0.0.1:${PORT})\033[0m"
echo -e "\033[32m   Backend URL:  ${BACKEND_URL}\033[0m"
echo -e "\033[90m   Binary Path:  ${AGENT_BIN}\033[0m"
echo -e "\033[36m   Ready for ForgeLAB browser import requests.\033[0m"
echo -e "\033[33m   Press Ctrl+C to stop the agent.\033[0m"
echo -e "\033[32m====================================================\033[0m"

exec "$AGENT_BIN" "${ARGS[@]}"
