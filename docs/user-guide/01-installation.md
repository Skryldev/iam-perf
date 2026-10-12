# 1. Installation

`iam-perf` is a single static Go binary with **zero third-party dependencies**
(the module has an empty `go.sum` by design). That makes installation unusually
simple: you need a Go toolchain to build it, and nothing else. This chapter
covers the prerequisites, the build, the verification steps you should run
after building, and how to obtain the binary from a container image instead.

## 1.1 Prerequisites

| Requirement | Minimum | Notes |
|---|---|---|
| Go toolchain | **1.27+** | Only needed at build time. Check with `go version`. |
| Operating system | Linux / macOS / Windows | Any platform Go cross-compiles to. Development and testing were done on Linux/amd64. |
| Disk | ~20 MB | Source tree + binary. |
| Network at run time | only the approved target | The binary contacts exactly one host: the `target.base_url` you configure — and only after both authorization keys are supplied. |

There are no runtime library dependencies because the tool is built entirely on
the Go standard library, including its HTTP stack, JSON handling, structured
logging (`log/slog`), and metrics. This was a deliberate supply-chain decision
(ADR-1 in `docs/ARCHITECTURE.md`): with no third-party modules, there is no
dependency to audit, pin, or patch.

If you plan to run the test-suite gauntlet yourself (`make verify`), no extra
packages are required either — all tests use in-process `httptest` fixtures
that simulate the IAM protocol locally.

## 1.2 Build from source

```bash
git clone https://github.com/Skryldev/iam-perf.git   # or unpack the release zip
cd iam-perf
make build
```

`make build` performs a static, reproducible-friendly build:

```bash
mkdir -p bin
CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X example.com/iam-perf/pkg/iamperf.version=$(cat VERSION)" \
    -o bin/iam-perf ./cmd/iam-perf
./bin/iam-perf version
```

The `-ldflags -X` line injects the version from the `VERSION` file (currently
`0.1.0`) into the binary, so `iam-perf version` reports the true build version
rather than the development fallback. `-trimpath` removes local filesystem
paths from the binary, and `CGO_ENABLED=0` produces a fully static executable
with no libc dependency — the same mode used inside the container image.

Cross-compiling is standard Go; for example, to build from an Apple Silicon
macOS host for a Linux load-generator box:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w -X example.com/iam-perf/pkg/iamperf.version=$(cat VERSION)" \
  -o bin/iam-perf-linux-amd64 ./cmd/iam-perf
```

## 1.3 Install the binary

The build drops the binary at `bin/iam-perf`. To make it available system-wide,
copy it somewhere on your `PATH`:

```bash
sudo cp bin/iam-perf /usr/local/bin/     # Linux/macOS
iam-perf version
```

Expected output:

```
iam-perf 0.1.0
go      go1.27.2
```

The second line records the Go toolchain version that compiled the binary,
which is useful when comparing reports produced by different generator hosts.

## 1.4 Verify the build

Before pointing the tool at anything, run the verification gauntlet. It is
fast, fully offline, and proves the binary you built behaves as documented:

```bash
make verify
```

This target chains four gates: `gofmt` cleanliness, `go vet`, the unit and
integration test suite, and the same suite under the race detector. All tests
run against local `httptest` fixtures — **no test ever contacts a real IAM
service**, so this is safe to run anywhere, including air-gapped CI (after the
one-time toolchain download).

Other useful targets:

| Target | What it does |
|---|---|
| `make test` | Unit + integration tests (300 s timeout). |
| `make race` | The same suite with `-race`. |
| `make cover` | Test coverage summary (core packages land in the 79–95 % range). |
| `make bench` | Generator benchmarks against local fixtures (see §7.7). |
| `make e2e-local` | Black-box end-to-end suites that invoke the compiled CLI as a subprocess. |
| `make tidy` | Confirms the module still has **zero** external dependencies (fails if `go.sum` becomes non-empty). |
| `make clean` | Removes `bin/`, `reports/`, and coverage artifacts. |

## 1.5 Alternative: container image

If you prefer not to install Go, use the container image (see
[09-docker.md](09-docker.md) for the full treatment):

```bash
make docker-build docker-smoke     # requires a Docker daemon on this machine
```

The image is multi-stage: the build stage compiles the same static binary, and
the runtime stage is `distroless/static-debian12:nonroot` — no shell, no
package manager, runs as uid `65532`. The image is built by the Dockerfile in
the repository root; the `docker-smoke` target then proves the image starts as
a non-root user and prints its version.

> **Honest verification note.** The Dockerfile and compose file were authored
> and statically reviewed during development, but the build environment had no
> Docker daemon, so `make docker-build` / `docker-smoke` have **not** been
> executed by the developer. Run them on an operator machine before first use,
> and pin the base images by digest as described in the Dockerfile header and
> in [09-docker.md](09-docker.md).

## 1.6 First sanity check (zero network)

Whatever installation path you chose, confirm the tool works and — more
importantly — confirm what it does **not** do by default:

```bash
iam-perf validate -c configs/example-probe.json
```

`validate` parses the configuration, applies environment overrides, validates
every field and safety constraint, and prints the fully resolved configuration
in redacted form. It performs **zero network activity** by design. If this
command prints `OK: configuration is valid. No network activity was performed.`
and exits `0`, your installation is healthy and you are ready for the
[quickstart](02-quickstart.md).

A final supply-chain habit worth adopting: record the binary's checksum when
you build it for a test campaign, so every report produced during the campaign
can be tied to a specific binary:

```bash
sha256sum bin/iam-perf | tee bin/iam-perf.sha256
```
