# Multi-stage Dockerfile for ForgeLab Go Backend Control Plane

# Build stage
FROM golang:alpine AS builder

WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binaries
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/forgelab-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/forgelab-migrate ./cmd/migrate

# Final runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates curl docker-cli

WORKDIR /app

COPY --from=builder /app/forgelab-server .
COPY --from=builder /app/forgelab-migrate .
COPY migrations ./migrations

EXPOSE 8080

CMD ["./forgelab-server"]
