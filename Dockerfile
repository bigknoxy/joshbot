# Multi-stage Dockerfile for joshbot Go implementation
#
# Build stage
# The base images are pinned by digest, so what the build runs against is a
# reviewed artefact rather than a moving tag. Bumping them is deliberate: a
# new digest is a new base image, and the packages inside it need the same
# review the image itself gets (see the apk --upgrade line in the runtime
# stage -- the image's packages are pinned no more tightly than the image).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS builder

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

# Build the binary
# CGO_ENABLED=0 for static binary (no libc dependency)
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X main.Version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')" \
    -trimpath \
    -o joshbot ./cmd/joshbot

# Runtime stage - minimal image
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS runtime

# Install CA certificates for HTTPS and runtime dependencies.
# --upgrade: the base image ships the packages it was built with, and `apk add`
# leaves an already-installed package alone, so CVE-2026-4738 (zlib < 1.4-r0)
# survived a base bump until it was named explicitly. libssl3 and libcrypto3
# are named for the same reason (#495): five OpenSSL CVEs (CVE-2026-95692 and
# friends) ship inside the base image and stay at the base's version however
# fresh the build is, until the package is named here.
RUN apk add --no-cache --upgrade ca-certificates tzdata zlib libssl3 libcrypto3

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

# Health check - uses version (doesn't require config) for resilience
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD joshbot version > /dev/null 2>&1 || exit 1

# Default command
ENTRYPOINT ["joshbot"]
CMD ["gateway"]
