# Multi-stage Dockerfile for joshbot Go implementation
#
# Build stage
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set Go build environment
ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/

# Build the binary
# CGO_ENABLED=0 for static binary (no libc dependency)
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X github.com/bigknoxy/joshbot/cmd/joshbot.Version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')" \
    -trimpath \
    -o joshbot ./cmd/joshbot

# Runtime stage - minimal image
FROM alpine:3.21 AS runtime

# Install CA certificates for HTTPS and runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user for security
RUN adduser -D -g '' joshbot

WORKDIR /home/joshbot

# Create config directory with proper permissions
RUN mkdir -p /home/joshbot/.joshbot && \
    chown -R joshbot:joshbot /home/joshbot

# Copy binary from builder
COPY --from=builder /build/joshbot /usr/local/bin/joshbot

# Switch to non-root user
USER joshbot

# Set environment variables
ENV HOME=/home/joshbot
ENV PATH=/usr/local/bin:$PATH

# Expose ports (if needed for future HTTP server)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD joshbot status > /dev/null 2>&1 || exit 1

# Default command
ENTRYPOINT ["joshbot"]
CMD ["gateway"]
