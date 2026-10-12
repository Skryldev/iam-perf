# Architecture — iam-perf

Status: Phase 2 deliverable. Decisions below are binding for Phase 3.

## 1. Design Goals (ordered)

1. Safety of the target system and of credentials.
2. Measurement correctness (login transaction ≠ HTTP request; no percentile averaging).
3. Determinism and offline testability.
4. Minimal dependency surface (Go standard library only — no third-party modules).
5. Operational clarity (explicit errors, structured logs, versioned report schema).

## 2. Package Boundaries

```
cmd/iam-perf      → wires config + engine; owns signals, exit codes. No business logic.
pkg/iamperf       → ONLY public surface: Engine, Config alias, Report alias, Options, errors.
internal/config   → schema, defaults, layering (file < env < flags), validation, redacted rendering.
internal/safety   → allowlist, budgets, approval gate, live stop-condition controller.
internal/transport→ per-VU http.Client factory: pool, timeouts, redirect policy, jars, httptrace.
internal/auth     → Flow interface; evidence-derived Mohaymen flow; per-session state; redaction.
internal/runner   → VU lifecycle, closed/open models, ramp/warm-up, bounded queues, panic isolation.
internal/metrics  → sharded collectors, log-linear histograms, per-step/per-attempt/trace stats.
internal/report   → schema-versioned JSON, CSV, terminal rendering.
internal/telemetry→ slog logger factory + redaction handler.
```

Dependency direction: `cmd → pkg → internal/*`; `auth` and `runner` depend on interfaces they define for their needs (Clock, ClientFactory); nothing depends on `cmd` or `pkg`.

## 3. Architecture Decision Records (condensed)

**ADR-1: Standard library only.** Zero third-party dependencies. Histograms, JSON config, CSV, Prometheus text endpoint, CLI (stdlib `flag` subcommands) are all implementable with stdlib. Rationale: minimal supply chain, deterministic builds, no license surface. Trade-off: config format is JSON (YAML support deliberately omitted; documented).

**ADR-2: Custom log-linear histogram.** Latencies bucketed at `ceil(10^x)`-style log-linear boundaries (~90 buckets from 10 µs to 65 s). Quantiles interpolated from cumulative counts. Rationale: no HDR dependency; error bounded to one bucket (≤ ~2.3 % relative); memory bounded and cardinality-free. **Global percentiles are computed from merged global buckets — never by averaging per-worker percentiles.**

**ADR-3: Success = completed login transaction.** An attempt is "successful" only when the full evidence-derived chain completes with a session token AND a session-proof request returns 200. HTTP 200 on an individual request never implies login success. Requests are counted separately; both views are reported.

**ADR-4: No retries of non-idempotent steps.** `initiate-login`, `verifyPassword`, `login` are never retried automatically. Transport errors on these steps fail the attempt (class `transport`). Only the optional session-proof GET may be retried once (idempotent). Rationale: no accidental credential re-submission, no lockout amplification.

**ADR-5: Per-VU isolation.** Each virtual user owns: dedicated `http.Client` (own `http.Transport` sized consistently with global pool caps), own `cookiejar.New`, own session state (pre-auth token, auth token). No credential or token sharing. VU→client sizing: `MaxIdleConnsPerHost ≥ VUs` so pooling does not throttle closed-model runs.

**ADR-6: Safety is a gate, not a warning.** Three independent locks before any load: (a) allowlist check of every request URL (also on redirect hops); (b) budget validation (hard caps on VUs/rate/duration/requests); (c) approval gate (config `authorization.accepted` AND `--iam-accept-risk` or `IAMPERF_ACCEPT_RISK=true`). Plus a live stop-condition controller (error rate, 429 count, 5xx count, p99 ceiling). Dry-run and validate commands never touch the network.

**ADR-7: Public API via aliases.** `pkg/iamperf` re-exports `Config`/`Report` as type aliases of the internal types with documented stability guarantees (semver, schema_version). Rationale: avoids DTO duplication drift; internal packages are unimportable by external consumers, so the alias IS the API.

**ADR-8: Closed and open models.** Closed model: fixed VUs loop `attempt → think-time`. Open model: fixed arrival rate via ticker; arrivals enter a bounded queue (`max_in_flight`); overflow is counted as `backpressure_dropped` (never silently queued to infinity). Both context-cancel-aware.

**ADR-9: Clock injection.** All timing (`clientTimestamp` header, think time, deadlines) uses an injectable `Clock` for deterministic tests.

**ADR-10: Report schema version 1.** `schema_version` first field; stable field order; `run_id` (UUIDv4) enables run comparison. Reports embed limitations (e.g. unverified failure contract) automatically.

## 4. Error Model

- Sentinel errors (pkg): `ErrNotAuthorized`, `ErrSafetyStopped`, `ErrTargetNotAllowed`, `ErrValidation`, `ErrCancelled`.
- Wrapped with `%w` and actionable context; `errors.Is/As` supported.
- Attempt failure classes (bounded set, metric labels): `transport`, `auth`, `protocol`, `cancelled`, `aborted`, `backpressure`.
- Redaction: error strings pass through a scrubber (token-shaped values, known secret fields) before reaching logs/metrics/reports.

## 5. Configuration Schema (v1)

JSON file; layering: defaults < file < env (`IAMPERF_` prefix, e.g. `IAMPERF_TARGET_BASEURL`) < flags. Secrets only via env/file. See `docs/CONFIGURATION.md` for the full reference and `configs/` for safe examples.

## 6. CLI Contract

| Command | Network | Behavior |
|---|---|---|
| `validate -c f.json` | none | Parse + validate + print resolved redacted config; exit 0/2 |
| `run -c f.json --iam-accept-risk` | gated | Print resolved config + safety verdict, wait 3 s (abort window), execute, write report |
| `report -f report.json` | none | Render terminal summary from a stored JSON report |
| `version` | none | Print version/commit/go version |

Exit codes: 0 success · 1 runtime error · 2 invalid configuration · 130 cancelled. SIGTERM/SIGINT → graceful stop (drain in-flight ≤ 10 s), partial report written with `termination: cancelled`.

## 7. Metrics Model

Counters (attempts: started/succeeded/failed/aborted; requests: started/completed/failed, timeouts, conn errors, 429, 5xx, redirects, conn reuse, backpressure drops) + histograms (attempt end-to-end, per-step durations, TTFB, DNS/TCP/TLS when observable) + status-code distribution map. Bounded labels only: `step` name and `class` — never usernames/tokens/IDs.

## 8. Report Schema

`schema_version, run_id, started_at, finished_at, duration, warmup_duration, termination, target, scenario, workload, attempts, requests, latency, steps[], errors, safety, environment, limitations[]` — full field reference in `docs/REPORT_SCHEMA.md`.

## 9. Versioning

Module path `example.com/iam-perf` (placeholder — rename before publishing, documented in README). Semver: MAJOR = public API/report schema break, MINOR = additive, PATCH = fixes. `VERSION` file is the single source of truth, injected via `-ldflags -X`.
