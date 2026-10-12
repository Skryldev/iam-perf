# iam-perf — IAM Login Performance Testing Framework

A production-grade, safety-gated HTTP performance testing framework in Go for the login flow of IAM applications. It was built **evidence-first**: the authentication client implements exactly the protocol captured in the supplied HTTP Toolkit artifacts (see [`docs/AUTH_FLOW.md`](docs/AUTH_FLOW.md)) and nothing more.

> ⚠️ **This tool generates load.** It refuses to run until you pass two independent authorization keys, every request URL is checked against a host/path allowlist, and live stop conditions abort the run the moment the target signals distress (`429`, `5xx`, error-rate, p99 ceiling). No load has been generated against any real IAM system during development — all tests run against local fixtures.

## Project purpose and scope

- **In scope:** performance testing of the evidence-derived login transaction (initiate → verify password → session establishment → session proof), driven by isolated virtual users, with accurate per-step and end-to-end metrics, JSON/CSV/terminal reports, Docker packaging, and a reusable Go API.
- **Out of scope:** credential guessing, password spraying, brute force, MFA bypass, rate-limit evasion, account enumeration, or any unauthorized access. These are impossible by construction: the flow performs one login per attempt, never retries authentication POSTs, and stops on `429`.

## Quick start

```bash
# 1. Build (Go 1.27+)
make build && ./bin/iam-perf version

# 2. Validate a configuration — NO network activity
./bin/iam-perf validate -c configs/example-probe.json

# 3. Dry-run shape (offline): probe flow against a localhost stub is the
#    recommended first live test; see docs/SAFETY.md for the full runbook.

# 4. A real (authorized) run — two keys required:
export IAMPERF_TEST_PASSWORD='...'     # secret via environment, never flags/files in repo
./bin/iam-perf run -c configs/example-closed.json --iam-accept-risk
```

Exit codes: `0` success · `1` runtime error · `2` invalid configuration · `130` cancelled.

## Architecture

```
cmd/iam-perf/       CLI: validate | run | report | version
pkg/iamperf/        Public Go API (Engine, Config, Report, sentinel errors)
internal/config/    Schema v1, defaults < file < env layering, validation, redaction
internal/safety/    Allowlist, budgets, two-key approval gate, stop-condition controller
internal/transport/ Per-VU client factory: pooling, timeouts, redirects, httptrace
internal/auth/      Evidence-based login flow + non-authenticating probe flow
internal/runner/    Closed/open workload models, ramp/warm-up, bounded queues
internal/metrics/   Log-linear histograms (global percentiles), counters, Prometheus
internal/report/    Schema-versioned JSON, CSV, terminal rendering
internal/telemetry/ Structured logging with mandatory redaction
tests/              Offline e2e suites and benchmarks (httptest fixtures only)
```

Design decisions and their trade-offs are recorded in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). Package highlights:

- **Success ≠ HTTP 200.** A login attempt counts as successful only when the full chain completes with a session token and (optionally) a session-proof `200`. Request-level activity is reported separately.
- **Isolated virtual users.** Each VU owns its `http.Client`, cookie jar, and token state. Token reuse across VUs is covered by tests.
- **Global percentiles.** Latency percentiles come from merged global histogram buckets — never averaged across workers.
- **TLS cannot be weakened.** There is no skip-verify option. Lab PKIs use `transport.trusted_cas_file`.

## Evidence-derived authentication flow (summary)

The implemented chain mirrors the capture step-for-step:

| Step | Request | Dynamic value handling |
|---|---|---|
| 1 | `POST /api/v1/auth/initiate-login` `{"userName":"…"}` | response header `pre-auth-token` → stored per attempt |
| 2 | `POST /api/v1/auth/verifyPassword` `{"password":"…"}` | header `Pre-Auth-Token` |
| 3 | `GET /api/v1/auth/authentication-status` | header `Pre-Auth-Token` (observational) |
| 4 | `POST /api/v1/auth/login` `{}` | header `Pre-Auth-Token`; response header `x-auth-token` + `data.userId` define success |
| 5 | `GET /api/v1/apps/users/dashboardPermissions` | header `X-Auth-Token` (session proof) |

All API calls carry `clientTimestamp` (Unix seconds), `Cross-Domain: true`, `Accept-Language: fa-IR`. Full traceability, the complete captured exchange table, and **what was deliberately NOT implemented** (no CSRF observed, no MFA captured, failure contract unknown) are documented in [`docs/AUTH_FLOW.md`](docs/AUTH_FLOW.md).

## Configuration

JSON configuration with layered overrides: defaults < file < `IAMPERF_*` environment < CLI flags. Secrets are **never** in config files, flags, images, or reports — use `credentials.password_env` (environment) or `credentials.password_file` (mode `0600` enforced). The complete field reference with defaults and constraints lives in [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md); safe examples in [`configs/`](configs/).

## CLI usage

```
iam-perf validate -c <config.json>          # parse + validate; zero network
iam-perf run -c <config.json> --iam-accept-risk [--out DIR]
iam-perf report -f <report.json>            # re-render a stored report
iam-perf version
```

`run` prints the redacted resolved configuration, waits 3 seconds (Ctrl-C abort window), then executes. SIGTERM/SIGINT at any point — including the abort window — results in graceful shutdown, a written report with `termination: "cancelled"`, and exit code `130` (verified by `scripts/sigterm-smoke.sh`).

## Go package usage

```go
import "example.com/iam-perf/pkg/iamperf"

cfg := iamperf.DefaultConfig()
cfg.Target.BaseURL = "https://iam-lab.example.ir"   // your approved lab target
cfg.Authorization.Accepted = true                   // key 1 (config)
eng, err := iamperf.NewEngine(cfg,
    iamperf.WithRunConfirmation(true),              // key 2 (runtime)
    iamperf.WithEnvLookup(os.LookupEnv),
)
if err != nil { /* validation error, actionable */ }
rep, err := eng.Run(ctx)                            // context-aware, cancellable
fmt.Println(rep.Attempts.SuccessRatePct, rep.Latency.Attempt.P99)
```

The engine is importable without the CLI, validates configuration before any network I/O, supports clock/logger/env injection for deterministic tests, and exposes typed sentinel errors (`ErrNotAuthorized`, `ErrValidation`, `ErrTargetNotAllowed`, …). Module path `example.com/iam-perf` is a placeholder — rename it in `go.mod` before publishing (see *Versioning*).

## Docker image

```bash
make docker-build docker-smoke
docker run --rm -v $PWD/configs:/configs:ro -v $PWD/reports:/reports \
  -e IAMPERF_TEST_PASSWORD iam-perf:0.1.0 run -c /configs/example-closed.json --iam-accept-risk
```

Multi-stage build: static binary (`CGO_ENABLED=0`), `distroless/static-debian12:nonroot` runtime, OCI labels, read-only-rootfs compatible, SIGTERM-aware, reports written to a mounted volume, secrets injected at runtime only. `docker-compose.yml` adds hardened defaults (`read_only`, `cap_drop: ALL`, `no-new-privileges`).

> **Verification status:** the Dockerfile and compose file were produced and statically reviewed, but the build environment had **no Docker daemon** — `make docker-build docker-smoke` must be executed on an operator machine before first use, and base images should be pinned by digest (instructions in the Dockerfile header).

Container CPU/memory limits cap achievable load: a 1-CPU container cannot sustain multi-thousand-RPS generation regardless of target capacity. Check the report's `environment` fields and compare `observed_rps` against your platform budget.

## Secret management

| Channel | Mechanism | Enforcement |
|---|---|---|
| Environment | `credentials.password_env` (default `IAMPERF_TEST_PASSWORD`) | read at run start, held in memory only |
| File | `credentials.password_file` | `0600` mode enforced at stat time |

Passwords/tokens never appear in: CLI flags, config files, logs (redacting `slog` handler), metric labels (bounded label set), reports (username redacted), or error messages (token-shaped scrubbing + no response bodies in errors).

## Testing

The suite is fully offline (local `httptest` fixtures simulating the evidence protocol):

```bash
make verify        # fmt-check + vet + test + race
make cover         # coverage (core packages 79–95%)
make bench         # generator benchmarks
make e2e-local     # black-box CLI suites
```

Covered behaviors include: the exact evidence request contract, token correlation and per-VU isolation, all failure classes, allowlist violations (including redirect escapes), stop conditions, backpressure drops, warm-up exclusion, bounded response bodies, timeout vs transport-error distinction, redaction, and graceful cancellation. `scripts/sigterm-smoke.sh` performs a live signal-handling check against a localhost-only stub.

**Benchmarks** (2 vCPU, loopback fixture — bounds the *generator's* overhead, not any target's capacity): login flow ≈ 0.17 ms/attempt (5 HTTP requests), metric record ≈ 153 ns, global quantile ≈ 64 ns.

## Interpreting reports

- `attempts.*` — login transactions (the primary success measure). `requests.*` — HTTP activity (secondary; do not equate).
- `latency.attempt.p99` is computed from global histogram buckets (≤ ~10 % bucket resolution).
- Warm-up attempts are counted but excluded from latency and rate denominators.
- `termination` ∈ `completed | safety-stop | cancelled`, with the controller's reason recorded under `safety`.
- Every report embeds `limitations[]` — read them before drawing conclusions. Field-by-field reference: [`docs/REPORT_SCHEMA.md`](docs/REPORT_SCHEMA.md). Synthetic examples from local fixtures: [`docs/examples/`](docs/examples/).

## Safety controls and operational runbook

Two-key authorization, hard workload budgets, URL allowlist (initial + every redirect hop), live stop conditions, bounded queues, and panic isolation are described with operator procedures in [`docs/SAFETY.md`](docs/SAFETY.md). Summary of non-negotiables:

1. Dry-run by default; `validate`/`report` never touch the network.
2. `run` requires `authorization.accepted=true` **and** `--iam-accept-risk` (or `IAMPERF_ACCEPT_RISK=true`).
3. `429` responses increment a counter that trips the controller (default: 10).
4. Authentication POSTs are never retried automatically.

## Known limitations and unverified assumptions

- **Failure contract unverified** — the capture contains no failed logins; failure classification is conservative (non-2xx / missing tokens / null `userId`).
- **Header-token session model** — no `Set-Cookie` was captured; server cookie behavior is unknown and unmodeled.
- **Single-factor only** — no MFA/challenge path was captured, so none is implemented; unexpected `data` shapes fail the attempt rather than being guessed.
- **`authentication-status` semantics** — implemented as the single observed poll; poll-until-ready behavior is an open question.
- **Docker build unverified** — no Docker daemon in the development environment (documented above).
- Generator-host resources bound achievable load; the report records the environment so generator-bound runs are detectable.

## Versioning and release

- Module path: `example.com/iam-perf` (placeholder). To publish: set `go.mod` to your repository path, tag `vMAJOR.MINOR.PATCH`, and `go tag`/push.
- Semver: MAJOR = public API or report schema break; MINOR = additive; PATCH = fixes.
- `VERSION` is the single source of truth, injected via `-ldflags -X example.com/iam-perf/pkg/iamperf.version=…` (Makefile/Dockerfile do this automatically).
- Report `schema_version` and config `schema_version` are currently `1`; both are checked at load/build time.

## Documentation map

| Document | Content |
|---|---|
| [`docs/user-guide/`](docs/user-guide/README.md) | **Complete "How to use" manual** — installation, quickstart, CLI, configuration, load models, safety runbook, reports, Go API, Docker, troubleshooting |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Package boundaries, ADR-1…ADR-10 |
| [`docs/AUTH_FLOW.md`](docs/AUTH_FLOW.md) | Evidence traceability, protocol spec, open questions |
| [`docs/SAFETY.md`](docs/SAFETY.md) | Controls, thresholds, operator runbook |
| [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) | Full configuration reference |
| [`docs/REPORT_SCHEMA.md`](docs/REPORT_SCHEMA.md) | Report fields (schema v1) |
| [`docs/phase-reports/`](docs/phase-reports/) | Per-phase engineering reports |
