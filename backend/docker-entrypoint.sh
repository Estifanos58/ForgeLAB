#!/bin/sh
set -e

# If running as root (UID 0), dynamically manage Docker socket access
# and drop privileges to the unprivileged 'forgelab' user.
if [ "$(id -u)" = "0" ]; then
    # Dynamically detect Docker socket GID if mounted
    if [ -S /var/run/docker.sock ]; then
        # Ensure docker socket has read/write permissions for local access
        chmod 666 /var/run/docker.sock 2>/dev/null || chmod g+rw /var/run/docker.sock 2>/dev/null || true

        SOCK_GID=$(stat -c '%g' /var/run/docker.sock 2>/dev/null || stat -f '%g' /var/run/docker.sock 2>/dev/null || echo "")
        if [ -n "$SOCK_GID" ]; then
            if [ "$SOCK_GID" = "0" ]; then
                addgroup forgelab root 2>/dev/null || true
            else
                GRP_NAME=$(getent group "$SOCK_GID" | cut -d: -f1)
                if [ -z "$GRP_NAME" ]; then
                    GRP_NAME="dockersock"
                    addgroup -g "$SOCK_GID" -S "$GRP_NAME" 2>/dev/null || true
                fi
                addgroup forgelab "$GRP_NAME" 2>/dev/null || true
            fi
        fi
    fi

    # Ensure application data directories exist and are owned by forgelab
    mkdir -p /app/data/builds /app/data/sources
    chown -R forgelab:forgelab /app/data

    # Step down to non-root 'forgelab' user while preserving supplementary groups
    exec su-exec forgelab "$@"
fi

# Already running as unprivileged user
exec "$@"
