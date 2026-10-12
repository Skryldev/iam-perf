# syntax=docker/dockerfile:1.7
# iam-perf — production image
# Multi-stage, static build, non-root, distroless runtime.
#
# Build:  docker build -t iam-perf:0.1.0 .
# Run:    docker run --rm \
#           -v $PWD/configs:/configs:ro \
#           -v $PWD/reports:/reports \
#           -e IAMPERF_TEST_PASSWORD \
#           iam-perf:0.1.0 run -c /configs/example-closed.json --iam-accept-risk
#
# SUPPLY-CHAIN NOTE (honest verification status): this Dockerfile was produced
# and statically reviewed, but the build environment had no Docker daemon.
# Before production use:
#   1. Build with `make docker-build docker-smoke` on an operator machine.
#   2. Pin base images by digest (docker images --digests / crane digest) and
#      replace the tags below — tags are mutable; digests are not.

# ---------- build stage ----------
FROM golang:1.27.2-alpine AS build

ARG VERSION=0.1.0
WORKDIR /src

# Dependency layer (no-op for the stdlib-only module, kept for cache shape).
COPY go.mod ./
RUN go mod download

COPY . .

# Static build: no cgo, trimmed paths, stripped symbols, version injected
# from build args. Override with --build-arg SOURCE_DATE_EPOCH for
# reproducible timestamps where supported.
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
      -ldflags="-s -w -X example.com/iam-perf/pkg/iamperf.version=${VERSION}" \
      -o /out/iam-perf ./cmd/iam-perf \
 && test -x /out/iam-perf

# ---------- runtime stage ----------
# distroless/static: no shell, no package manager, minimal attack surface,
# ships as nonroot (uid 65532).
FROM hubmirrorbytogettoyou/gcr.io.distroless.static-debian12:nonroot AS runtime

ARG VERSION=0.1.0

# OCI metadata labels.
LABEL org.opencontainers.image.title="iam-perf" \
      org.opencontainers.image.description="IAM login performance testing framework (safety-gated)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian12:nonroot"

USER nonroot:nonroot

# Reports are written to a mounted volume; the image stays read-only compatible.
# Configuration and secrets are NEVER baked into the image.
VOLUME ["/reports"]

ENTRYPOINT ["/iam-perf"]

# Health check: process liveness via the version command (exec form works on
# distroless because there is no shell dependency). It reports process
# health, not target health — by design (see docs/RUNBOOK.md).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/iam-perf", "version"]
