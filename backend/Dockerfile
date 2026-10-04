# Multi-stage Dockerfile for ForgeLab Go Backend Control Plane

# Build stage
FROM golang:alpine AS builder

WORKDIR /app

# Configure GOPROXY with fallback and retry for reliable module downloads
ENV GOPROXY=https://proxy.golang.org,https://goproxy.io,direct

# Copy dependency files
COPY go.mod go.sum ./
RUN for i in 1 2 3; do go mod download && break || (echo "Retrying go mod download in 3s..." && sleep 3); done

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
FROM alpine:3.19

RUN apk add --no-cache ca-certificates curl docker-cli

WORKDIR /app

COPY --from=builder /app/forgelab-server .
COPY --from=builder /app/forgelab-migrate .
COPY migrations ./migrations

EXPOSE 8080

CMD ["./forgelab-server"]
