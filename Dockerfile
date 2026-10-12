# syntax=docker/dockerfile:1.7
# iam-perf — production image
# Multi-stage, static build, non-root, minimal runtime with shell.
#
# Build:  docker build -t iam-perf:0.2.0 .
# Run:    docker run --rm \
#           -v "$PWD/configs:/configs:ro" \
#           -v "$PWD/reports:/reports" \
#           -e IAMPERF_TEST_PASSWORD \
#           iam-perf:0.2.0 run -c /configs/example-closed.json --iam-accept-risk
#
# SUPPLY-CHAIN NOTE:
#   Base images are pinned by tag for readability. For production, pin by digest:
#     docker buildx imagetools inspect golang:1.27-alpine
#     docker buildx imagetools inspect alpine:3.19
#   then replace `:tag` with `@sha256:...`.

# ---------- build stage ----------
FROM golang:1.27-alpine AS build

# If the pinned toolchain is ever older than what go.mod requires, let Go
# fetch the correct version from the module proxy. This also keeps the
# Dockerfile forward-compatible with future Go versions.
ENV GOTOOLCHAIN=auto

ARG VERSION=0.1.0
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

# Dependency layer: copy only the module files first so Docker can cache
# `go mod download` independently of source changes.
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy every source directory that the module needs.
# Keep this list explicit (instead of `COPY . .`) to:
#   - keep the build layer small and cache-friendly,
#   - avoid leaking local files into the image,
#   - make the module layout self-documenting.
COPY cmd/       ./cmd/
COPY internal/  ./internal/
COPY pkg/       ./pkg/

# Static, reproducible, minimal build.
#   CGO_ENABLED=0        → fully static binary (runs on any Linux base)
#   GOOS/GOARCH          → cross-build via buildx --platform
#   -trimpath            → no build-machine paths in the binary
#   -buildvcs=false      → deterministic output, no VCS stamping
#   -s -w                → strip symbol table and DWARF
#   SOURCE_DATE_EPOCH=0  → reproducible timestamps
#   -X ...version=...    → inject version from build arg
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    SOURCE_DATE_EPOCH=0 \
    go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -X example.com/iam-perf/pkg/iamperf.version=${VERSION}" \
      -o /out/iam-perf ./cmd/iam-perf \
 && test -x /out/iam-perf

# ---------- runtime stage ----------
# Alpine: ~5 MB, musl libc, BusyBox shell available.
# Chosen over distroless because the operator explicitly wants an
# inspectable, debuggable runtime with a shell.
FROM alpine:3.19 AS runtime

ARG VERSION=0.1.0

# OCI metadata labels.
LABEL org.opencontainers.image.title="iam-perf" \
      org.opencontainers.image.description="IAM login performance testing framework (safety-gated)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.base.name="alpine:3.19"

# Install CA certificates so the binary can perform outbound TLS.
# Drop this line if iam-perf makes no HTTPS calls.
RUN apk add --no-cache ca-certificates && update-ca-certificates

# Non-root user + writable reports directory, created in a single layer.
RUN addgroup -g 10001 -S appgroup \
 && adduser  -u 10001 -S appuser -G appgroup \
 && mkdir -p /reports \
 && chown -R appuser:appgroup /reports

# 👇 Critical fix: copy the binary from the build stage.
# Without this line the runtime image is empty and Docker fails with
# `exec: "/iam-perf": stat /iam-perf: no such file or directory`.
COPY --from=build --chown=appuser:appgroup /out/iam-perf /iam-perf

# Drop privileges.
USER appuser:appgroup

# Reports are written to a mounted volume; the image stays read-only compatible.
# Configuration and secrets are NEVER baked into the image.
VOLUME ["/reports"]

ENTRYPOINT ["/iam-perf"]

# Liveness probe via the version subcommand. Reports process health,
# not target health — by design (see docs/RUNBOOK.md).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/iam-perf", "version"]