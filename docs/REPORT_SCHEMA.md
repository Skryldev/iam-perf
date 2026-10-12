# Report Schema (v1)

JSON report written by `run` (and re-renderable via `iam-perf report -f`). `schema_version` is the first field; field order is stable. Comparison across runs: use `run_id`, `metadata.started_at`, and the fixed layout. Real examples produced against local fixtures: [`docs/examples/`](examples/).

## Top level

| Field | Type | Meaning |
|---|---|---|
| `schema_version` | int | `1`. Bumped only on breaking changes. |
| `run_id` | string | UUIDv4, unique per run. |
| `generated_at` | time | Report build time (UTC). |
| `module_version` | string | Build-injected version. |

## metadata

| Field | Meaning |
|---|---|
| `started_at` / `finished_at` | Run window. |
| `duration_seconds` | `finished_at − started_at` (includes drain/warm-up). |
| `warmup_seconds` | Configured warm-up window. |
| `termination` | `completed` \| `safety-stop` \| `cancelled`. |
| `config_summary` | Redacted resolved configuration (username/password/env refs → `[REDACTED]`). |

## target / scenario

`base_url`, `allowed_paths`, `flow` — the approved target identity (no secrets). Scenario: `name`, `model`, `virtual_users`, `duration_seconds`, `think_time_seconds` as executed.

## workload — configured vs observed

| Field | Meaning |
|---|---|
| `configured_rps` | Open-model configured rate (0 for closed). |
| `observed_rps` | Completed HTTP requests ÷ active seconds (active = window − warm-up). |
| `attempt_rps` | Completed login transactions ÷ active seconds. |
| `backpressure_drops` | Open-model arrivals dropped on a full queue (>0 means the generator could not keep up at the configured rate). |

## attempts — THE primary success measure

`started`, `succeeded`, `failed`, `aborted`, `warmup_excluded`, `success_rate_pct` (= succeeded ÷ (succeeded+failed)), `failure_rate_pct`.

Definitions: a *successful* attempt completed the full evidence chain incl. session token + proof (see docs/AUTH_FLOW.md §4). *Aborted* = cancelled/panicked mid-chain (excluded from rates' denominator and from latency). Warm-up attempts are counted but excluded from rates and latency.

## requests — secondary HTTP-activity view

`started`, `completed`, `failed`, `status_distribution` (map "200"→n), `redirects`, `connection_reuse`, `new_connections`, `timeouts`, `connection_errors`. **Never** equate request success with login success.

## latency

`attempt` — end-to-end login duration stats: `count`, `mean_ms`, `p50_ms`, `p90_ms`, `p95_ms`, `p99_ms`, `min_ms`, `max_ms`. Percentiles are computed from GLOBAL merged log-linear histogram buckets (bucket resolution ≈ 10 % relative; never averaged across workers). Aborted and warm-up attempts are excluded.

## steps[]

Per flow step: `step` (bounded name: `login-page`, `initiate-login`, `verify-password`, `authentication-status`, `login`, `session-proof`, `probe`), `started`, `failed`, `status_distribution`, `latency` (same stats shape).

## errors

`classes` — bounded failure classes (`transport`, `timeout`, `auth`, `protocol`, `cancelled`, `aborted`) with counts; `representative` — one redacted sample message per class. No usernames, tokens, bodies, or high-cardinality values.

## safety

`termination_reason` — controller text when `termination=safety-stop`; `tripped` — bool.

## traces — transport observables ("when observable")

`ttfb`, `dns`, `tcp`, `tls` — latency stats. DNS/TCP/TLS are observed only on NEW connections (0-count on fully-reused pools; that is expected, not an error).

## environment

`goos`, `goarch`, `go_version`, `num_cpu` — generator identity; use it to detect generator-bound runs.

## limitations[]

Machine-readable disclosure list, always present (failure-contract status, session model, single-factor scope, generator-bound caveat; plus a probe-specific note for `transport-probe` runs).
