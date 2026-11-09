# Multi-stage Dockerfile for ForgeLab Go Backend Control Plane

# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Configure GOPROXY with fallback and retry for reliable module downloads
ENV GOPROXY=https://proxy.golang.org,https://goproxy.io,direct

# Copy dependency files
COPY go.mod go.sum ./
RUN success=0; for i in 1 2 3; do \
        if go mod download; then success=1; break; fi; \
        echo "Retrying go mod download in 3s..." && sleep 3; \
    done; \
    [ "$success" -eq 1 ] || exit 1

# Copy source code
COPY . .

ARG VERSION=1.0.0
ARG COMMIT_SHA=dev
ARG BUILD_TIME=unknown

# Build binaries
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s -X main.Version=${VERSION} -X main.CommitSHA=${COMMIT_SHA} -X main.BuildTime=${BUILD_TIME}" -o /app/forgelab-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s -X main.Version=${VERSION} -X main.CommitSHA=${COMMIT_SHA} -X main.BuildTime=${BUILD_TIME}" -o /app/forgelab-migrate ./cmd/migrate

# Final runtime stage
FROM alpine:3.21

RUN success=0; for i in 1 2 3 4 5; do \
        if apk add --no-cache ca-certificates curl docker-cli; then success=1; break; fi; \
        echo "Retrying apk add in 3s..." && sleep 3; \
    done; \
    [ "$success" -eq 1 ] || exit 1

# Install official cloudflared binary for domainless public sharing with pinned version and checksum verification
ARG CLOUDFLARED_VERSION=2026.10.7
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "x86_64" ]; then \
        CLOUDFLARED_ARCH="amd64"; \
        CLOUDFLARED_SHA256="1f99fb518b83dec2ba8d870a7cc991d82a77e6196295a3d2754ba17ab9f91872"; \
    elif [ "$ARCH" = "aarch64" ]; then \
        CLOUDFLARED_ARCH="arm64"; \
        CLOUDFLARED_SHA256="3df43aedc9ff291d1735b5cb576cb97e0b157488796db2e32b28aecabb445775"; \
    else \
        CLOUDFLARED_ARCH="amd64"; \
        CLOUDFLARED_SHA256="1f99fb518b83dec2ba8d870a7cc991d82a77e6196295a3d2754ba17ab9f91872"; \
    fi && \
    success=0; for i in 1 2 3 4 5; do \
        if curl -fsSL -o /usr/local/bin/cloudflared "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${CLOUDFLARED_ARCH}" && \
           echo "${CLOUDFLARED_SHA256}  /usr/local/bin/cloudflared" | sha256sum -c - && \
           chmod +x /usr/local/bin/cloudflared; then \
            success=1; break; \
        fi; \
        echo "Retrying cloudflared download/verification in 3s..." && rm -f /usr/local/bin/cloudflared && sleep 3; \
    done; \
    [ "$success" -eq 1 ] || exit 1

WORKDIR /app

COPY --from=builder /app/forgelab-server .
COPY --from=builder /app/forgelab-migrate .
COPY migrations ./migrations

EXPOSE 8080

CMD ["./forgelab-server"]
