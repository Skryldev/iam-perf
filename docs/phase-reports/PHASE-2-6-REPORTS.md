# Phase 2–6 Engineering Reports (consolidated)

Per-phase reports required by the workflow gates. Commands were executed in this environment and results are reported verbatim; anything that could not be executed here is labeled as such.

---

## Phase 2 — Architecture & API Design (COMPLETE)

**Files created:** `docs/ARCHITECTURE.md` (package boundaries + ADR-1…ADR-10), skeleton decision for module path `example.com/iam-perf` (placeholder, rename documented).

**Major decisions:** stdlib-only (zero third-party deps → no supply chain); custom log-linear histogram with exact integer-generated bucket edges; success = completed login transaction (distinct from HTTP request activity); no automatic retries of auth POSTs; per-VU isolation; safety as a hard gate (two-key + allowlist + budgets + live stop conditions); public API via documented type aliases; closed + open workload models; injectable clock; report schema v1.

**Readiness for Phase 3:** READY.

---

## Phase 3 — Core Implementation (COMPLETE)

**Files created:** `go.mod`, `VERSION`, `internal/config/{config,load,redact}.go`, `internal/metrics/{metrics,prometheus}.go`, `internal/safety/safety.go`, `internal/transport/transport.go`, `internal/auth/{auth,probe,redact}.go`, `internal/runner/runner.go`, `internal/report/report.go`, `internal/telemetry/telemetry.go`, `pkg/iamperf/api.go`, `cmd/iam-perf/{main,run,report_out}.go`, `configs/example-{closed,open,probe}.json`.

**Commands executed:** `go build ./...` → clean. `go vet ./...` → clean.

**Security/safety notes:** allowlist enforced in `transport.Do` and `CheckRedirect`; two-key approval in `pkg` + CLI; passwords only via env/0600-file; TLS verification has no disable path; bounded body reads; no secrets in errors by construction + scrubbing.

**Readiness for Phase 4:** READY.

---

## Phase 4 — Verification (COMPLETE)

**Files created:** `internal/testkit/iam.go` (offline evidence-protocol fixture), test suites for config, metrics, safety, transport, auth, runner, report, telemetry, pkg/iamperf, plus `tests/e2e_test.go` and `tests/bench_test.go`.

**Commands executed (actual results):**
- `gofmt -l .` → empty (after formatting pass)
- `go vet ./...` → clean
- `go test ./...` → **PASS** (11 packages)
- `go test -race ./...` → **PASS**
- `go test -cover ./...` → auth 82.4 %, config 80.1 %, metrics 92.4 %, report 94.8 %, runner 83.1 %, safety 87.1 %, telemetry 52.8 %, transport 79.3 %, pkg/iamperf 64.8 %
- `go test -bench=. -benchtime=200x -run=^$ ./tests/` → login flow ≈ 172 µs/op (5 HTTP requests vs loopback fixture), metrics record ≈ 153 ns/op, global quantile ≈ 64 ns/op

**Defects found by tests and fixed (all real bugs, none cosmetic):**
1. Typed-nil `*strings.Reader` passed as `io.Reader` crashed bodyless GETs (auth flow).
2. Runner never cancelled the stop-watcher context on healthy completion → run hung after the workload window.
3. Redirect limit off-by-one (Go `CheckRedirect` semantics).
4. TTFB recorded in the collector but never written into `TraceInfo`.
5. slog redaction handler mutated attribute copies — tokens could leak into logs; handler now rebuilds a scrubbed record.
6. Histogram bucket edges accumulated float error (e.g. p99 reporting 123 ms for a 100 ms max) — replaced with exact integer generation.
7. **SIGTERM smoke test found the closed-model VU loop busy-spinning after cancellation (1.7 M aborted attempts in 10 s)** — loop now exits on cancelled context; re-verified: clean drain, 10 attempts, 0 aborted, report written, exit 130.
8. CLI installed signal handling after the 3 s abort window — early SIGTERM hard-killed the process (exit 143, no report); handler now installs first.

**Known defects:** none open.

**Unresolved questions:** see docs/AUTH_FLOW.md §7 (target-behavior questions; do not block correctness).

**Readiness for Phase 5:** READY.

---

## Phase 5 — Packaging & Containerization (COMPLETE, with an honestly-disclosed gap)

**Files created:** `Dockerfile` (multi-stage, CGO off, distroless `static-debian12:nonroot`, OCI labels, exec-form HEALTHCHECK), `.dockerignore`, `docker-compose.yml` (read_only, cap_drop ALL, no-new-privileges, tmpfs, secret via env, stop_grace_period), `Makefile` (`fmt-check vet test race cover bench build verify docker-build docker-smoke e2e-local clean tidy`), `.gitignore`, `scripts/sigterm-smoke.sh`.

**Verified in this environment:**
- `make build` → static binary, `./bin/iam-perf version` → `iam-perf 0.1.0 / go go1.27.2`.
- `scripts/sigterm-smoke.sh` (localhost-only stub): SIGTERM mid-run → graceful exit **130**, report written with `termination:"cancelled"`; SIGTERM inside the abort window → exit 130, no load generated.

**NOT verified in this environment (no Docker daemon — disclosed, not claimed):** `docker build`, non-root execution inside the image, HEALTHCHECK behavior. `make docker-build docker-smoke` plus digest pinning (instructions in the Dockerfile header) are operator pre-release steps.

**Readiness for Phase 6:** READY.

---

## Phase 6 — Documentation & Release Readiness (COMPLETE)

**Files created:** `README.md` (purpose, quick start, architecture, flow summary, config/CLI/pkg/docker usage, secrets, testing, benchmarks, report interpretation, safety, limitations, versioning), `docs/AUTH_FLOW.md`, `docs/SAFETY.md`, `docs/CONFIGURATION.md`, `docs/REPORT_SCHEMA.md`, `docs/examples/sample-report-closed.json` and `sample-report-open.json` (**real reports generated from local fixture runs**, not fabricated), `LICENSE` (MIT), phase reports (this file), `tests/sample_reports_test.go` (sample regeneration gate).

**Final verification (re-run at Phase 6 close):** `gofmt` clean · `go vet` clean · `go test ./...` PASS · `go test -race ./...` PASS · coverage as above · CLI `validate` smoke PASS (no network) · SIGTERM smoke PASS.

**Release metadata:** `VERSION` = 0.1.0; ldflags injection verified via `make build`; module path rename procedure documented in README.

## Definition of Done — checklist

1. ✅ Auth implementation traceable to evidence (docs/AUTH_FLOW.md, per-step comments).
2. ✅ Unverified behavior documented, not fabricated (AUTH_FLOW §5–7, report `limitations[]`).
3. ✅ Public package importable without CLI (pkg/iamperf + example in README + pkg tests).
4. ✅ CLI validate + controlled run (validate never touches network — asserted in e2e).
5. ✅ Isolated VU state (per-VU client/jar/tokens; test-asserted via fixture token accounting).
6. ✅ Workload limits + emergency cancellation implemented and tested (unit + race + live smoke).
7. ✅ Metrics distinguish transactions from requests (Attempts vs Requests; report + tests).
8. ✅ Tests offline (testkit only; no test references the real host as a target).
9. ✅ fmt/vet/test/race executed, results documented above.
10. ⚠️ Docker image builds and runs non-root — **Dockerfile authored & reviewed; execution NOT verifiable here (no daemon)**; operator step documented. No embedded secrets by construction (no ARG/COPY of secrets; .dockerignore excludes tests/docs/env files).
11. ✅ Documentation and examples complete.
12. ✅ Limitations and assumptions disclosed (README, AUTH_FLOW, REPORT_SCHEMA, report limitations[]).
13. ✅ No unauthorized load generated — the only network traffic produced in this project hit `127.0.0.1` stubs and `httptest` servers, plus toolchain downloads from `go.dev`.
