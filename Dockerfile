# Multi-binary image (server + indexer, default CMD runs the server).
# Prefer Dockerfile.server / Dockerfile.indexer for per-binary images;
# this file stays for parity with the pre-#432 CI image.
#
# Planned PR#432: pinned base images + HEALTHCHECK (05-deploy.md 1.6).
# Base images are pinned to exact tags (no :latest); bump deliberately.

# Build stage - golang:1.23-alpine3.20 (go.mod requires go 1.23)
FROM golang:1.23-alpine3.20 AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binaries
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o hivemind-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o hivemind-indexer ./cmd/indexer

# Runtime stage - alpine 3.20 (pinned; no :latest)
FROM alpine:3.20

# Install ca-certificates for HTTPS and wget for health checks
RUN apk --no-cache add ca-certificates tzdata wget

# Create app user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

WORKDIR /app

# Copy binaries from builder
COPY --from=builder /build/hivemind-server .
COPY --from=builder /build/hivemind-indexer .

# Change ownership
RUN chown -R appuser:appuser /app

# Switch to non-root user
USER appuser

# Expose default port (JSON-RPC; the CMD default runs the server)
EXPOSE 8080

# Real health endpoint (PR#430): 200 once the database is reachable and the
# indexed head is fresh. See Dockerfile.server for the full rationale
# (including why the probe is GET-and-discard, not wget --spider).
HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
  CMD wget -q --tries=1 -O /dev/null "http://localhost:${HIVE_HTTP_SERVER_PORT:-8080}/health" || exit 1

# Default command (can be overridden)
CMD ["./hivemind-server"]
