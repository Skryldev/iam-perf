# 4. Configuration reference (schema v1)

Configuration is a single JSON document. Loading is layered and deterministic:

```
built-in defaults  <  config file  <  IAMPERF_* environment  <  CLI flags
```

Later layers win where they define values. Two load-time behaviors protect you
from silent mistakes: **unknown fields are rejected** (a typo like
`virtul_users` fails the load with the offending name in the message, instead
of being silently ignored), and `schema_version` must equal `1`.

Durations accept Go duration syntax as a JSON string (`"300ms"`, `"2m"`,
`"1h30m"`) or a bare integer meaning seconds (`30` ≡ `"30s"`). Floats,
negative values, and unitless strings that are not integers are rejected.

Everything in this chapter mirrors `internal/config`. The built-in defaults are
also the values `validate` prints when you give it a minimal file — when in
doubt, validate and read the resolved output.

## 4.1 Document skeleton

```json
{
  "schema_version": 1,
  "target":         { },
  "authorization":  { },
  "scenario":       { },
  "transport":      { },
  "credentials":    { },
  "safety":         { },
  "metrics":        { },
  "report":         { },
  "log":            { },
  "flow":           { }
}
```

Omitted sections fall back to defaults, so a probe config can be a handful of
lines (see §4.11).

## 4.2 `target` — where load may go

| Field | Type | Default | Constraints |
|---|---|---|---|
| `base_url` | string | `https://iam.mohaymen.ir` | Must parse as `http`/`https` URL with a host. This exact scheme+`host:port` becomes the **only** host the generator may contact. |
| `allowed_paths` | []string | `["/login", "/api/v1/"]` | Non-empty; each entry must start with `/`. Treated as path prefixes: a URL is allowed if its path equals a prefix or lies under `prefix + "/"`. Enforced on the initial request **and on every redirect hop**, plus again at request time in the transport layer. |

The allowlist is built from these two fields and is immutable for the life of
the run. Requests outside it fail with `host not in approved allowlist` or
`path not in approved scope` and are counted as request failures — they are
never silently followed.

## 4.3 `authorization` — key 1 of the two-key gate

| Field | Type | Default | Constraints |
|---|---|---|---|
| `accepted` | bool | `false` | Must be `true` to allow `run`. `validate` does not require it. |
| `scope_note` | string | `""` | Free text; document **who** approved, **what scope**, **when**. No secrets. Embedded in reports via the redacted config summary. |

This field alone is *not* sufficient to run: the runtime confirmation
(`--iam-accept-risk` or `IAMPERF_ACCEPT_RISK=true`) is the second, independent
key. The split is deliberate — a config file checked into git can carry
`accepted: true` as a team decision, while the person at the keyboard must
still consciously accept the run each time (or explicitly opt in via the
environment for automation).

## 4.4 `scenario` — the workload

| Field | Type | Default | Constraints |
|---|---|---|---|
| `name` | string | `login-baseline` | Label carried into the report. |
| `flow` | string | `mohaymen-login` | `mohaymen-login` (evidence-based login chain; requires credentials) or `transport-probe` (non-authenticating `GET /login`; credentials forbidden-by-omission). |
| `model` | string | `closed` | `closed` (fixed VU count, see below) or `open` (constant arrival rate). Details and trade-offs in [05-load-models.md](05-load-models.md). |
| `virtual_users` | int | `1` | `closed` model only; must be ≥ 1 and ≤ `safety.max_virtual_users`. |
| `request_rate` | float | `0` | `open` model only; arrivals per second; must be > 0 and ≤ `safety.max_requests_per_second`. |
| `max_in_flight` | int | `64` | `open` model only; bounded in-flight queue size; must be ≥ 1. Arrivals beyond capacity are **dropped and counted** (`workload.backpressure_drops`), never queued without bound. |
| `duration` | duration | `1m` | Total workload window; must be > 0 and ≤ `safety.max_duration`. |
| `ramp_up` | duration | `0s` | Stagger window over which VUs start (closed) or arrival rate grows linearly (open). |
| `ramp_down` | duration | `0s` | Stagger window over which VUs stop (closed) or arrivals cease (open). |
| `warm_up` | duration | `0s` | Attempts starting inside this window are counted (`attempts.warmup_excluded`) but excluded from latency and from rate denominators. |
| `think_time` | duration | `1s` | Closed model: pause between a VU's attempts (bounded by the VU's remaining window). Open model: not used (arrivals define pacing). |

## 4.5 `transport` — HTTP client engineering

Per-VU clients are fully isolated: each has its own `http.Transport`, cookie
jar, timeout, and redirect policy. `ForceAttemptHTTP2` is on; TLS is always
verified (TLS ≥ 1.2) — **there is no skip-verify option anywhere in the tool**
(ADR in `docs/ARCHITECTURE.md`); lab PKIs use `trusted_cas_file`.

| Field | Type | Default | Notes |
|---|---|---|---|
| `request_timeout` | duration | `15s` | Whole-request client timeout per HTTP request. |
| `dial_timeout` | duration | `5s` | TCP connect timeout. |
| `tls_handshake_timeout` | duration | `5s` | TLS handshake timeout. |
| `response_header_timeout` | duration | `10s` | Wait for response headers after the request is written. |
| `idle_conn_timeout` | duration | `60s` | Idle keep-alive connection lifetime. |
| `max_idle_conns` | int | `256` | Global idle-pool cap. |
| `max_idle_conns_per_host` | int | `64` | Per-host idle cap — raise this (with `max_conns_per_host`) when raising VU count, or you will churn connections. |
| `max_conns_per_host` | int | `0` (unlimited) | `0` lets concurrency be bounded by VU count / `max_in_flight` instead. |
| `max_redirects` | int | `5` | Following hop limit + 1 errors (`transport: too many redirects`). Every hop is allowlist-checked. |
| `max_response_body_bytes` | int64 | `1048576` (1 MiB) | Bodies are read bounded; exceeding the cap fails the request (`response body exceeds max_response_body_bytes`). Must be > 0. |
| `user_agent` | string | `iam-perf/0.1 (+load-test; approved-scope-only)` | Identifies the generator honestly to the target and its owners. |
| `accept_language` | string | `fa-IR` | Sent on flow requests, mirroring the captured evidence. |
| `trusted_cas_file` | string | `""` | Optional PEM CA bundle appended for lab/private PKIs. File must exist and contain ≥ 1 valid certificate (validated at config load). |
| `proxy_url` | string | `""` | Explicit egress proxy (e.g. controlled lab egress). |
| `warm_login_page` | bool | `false` | Prepend `GET /login` (the captured SPA shell step) to each login attempt. Its failure is recorded but does not fail the chain (unless cancelled). |

## 4.6 `credentials` — references, never secrets

| Field | Type | Default | Constraints |
|---|---|---|---|
| `username` | string | `""` | Required for `mohaymen-login` (overridable via `IAMPERF_CREDENTIALS_USERNAME`). |
| `password_env` | string | `IAMPERF_TEST_PASSWORD` | Name of the env var holding the password. |
| `password_file` | string | `""` | Path to a file holding the password. **Mode must be exactly `0600`** — enforced by `stat` at run start; anything else refuses the run. Trailing newline is trimmed. |

Exactly one of `password_env` / `password_file` must be set for the login flow
(both or neither is a validation error). The password value is read once, when
the run starts *after* the authorization gate passes, held in memory only, and
never logged, marshaled, or included in reports. Config files therefore never
contain secrets — which is what makes it safe to commit scenario files.

## 4.7 `safety` — budgets and live stop conditions

These fields do double duty: they are **pre-flight constraints** (validation
refuses workloads exceeding them) and **live stop conditions** (the controller
aborts a running workload that crosses them). All thresholds are sticky: once
tripped, the run ends and the reason is recorded in the report. Set a
threshold to `0` to disable that check (except `max_total_requests`, which
must be > 0).

| Field | Default | Pre-flight | Live stop (polled every 250 ms) |
|---|---|---|---|
| `max_virtual_users` | `100` | `virtual_users` ≤ this | — |
| `max_requests_per_second` | `200` | `request_rate` ≤ this (open) | — |
| `max_duration` | `30m` | `duration` ≤ this | — |
| `max_total_requests` | `200000` | must be > 0 | `requests_started` ≥ budget |
| `stop_error_rate_pct` | `50` | — | attempt error rate ≥ value (guarded by `min_samples_for_stop`) |
| `stop_5xx_count` | `100` | — | cumulative `5xx` responses ≥ count |
| `stop_429_count` | `20` | — | cumulative `429` responses ≥ count — **the server told us to stop** |
| `stop_p99_millis` | `10000` | — | attempt p99 latency ≥ value ms (same sample guard) |
| `min_samples_for_stop` | `20` | — | minimum attempts before rate/p99 conditions are evaluated |

The shipped examples tighten these considerably (`stop_429_count: 10`,
`stop_error_rate_pct: 30`, …) — that is the recommended posture: start tight,
relax deliberately. The first condition to trip wins; its exact reason string
lands in `safety.termination_reason` and the terminal report prints
`SAFETY STOP: …`.

## 4.8 `metrics` — live observability

| Field | Type | Default | Notes |
|---|---|---|---|
| `prometheus_enabled` | bool | `false` | Enables the `/metrics` endpoint during `run`. |
| `prometheus_addr` | string | `""` | `host:port` to bind (validated when enabled). Prefer `127.0.0.1:<port>` and scrape locally, or publish the port deliberately (compose file comments show the pattern). |

## 4.9 `report` — outputs

| Field | Type | Default | Notes |
|---|---|---|---|
| `output_dir` | string | `reports` | Created if missing. Overridable per run with `run --out DIR`. |
| `formats` | []string | `["json", "terminal"]` | Any subset of `json`, `csv`, `terminal`. Unknown entries are validation errors. `terminal` prints to stdout (nothing is written for it). |

## 4.10 `log` and `flow`

| Section | Field | Default | Notes |
|---|---|---|---|
| `log.level` | string | `info` | `debug` \| `info` \| `warn` \| `error`. Debug logs every failed attempt's redacted class+message — useful, chatty. |
| `log.format` | string | `text` | `text` \| `json`. JSON is what you want when shipping logs somewhere. The handler redacts token-shaped strings on every attribute, at every level. |
| `flow.*` | see below | captured evidence | Path overrides for the five evidence endpoints; `session_proof` (default `true`) enables the `dashboardPermissions` check; `authentication_status` (default `true`) enables the observed status poll. |

`flow` defaults reproduce the captured protocol exactly. Change these only to
follow a target whose paths genuinely differ, and record why in your scenario
notes — the whole point of the flow section is to stay evidence-faithful.

## 4.11 Minimal configs

Probe (no credentials, no auth chain — good first target):

```json
{
  "schema_version": 1,
  "target": { "base_url": "http://127.0.0.1:18099", "allowed_paths": ["/login"] },
  "authorization": { "accepted": true, "scope_note": "local stub, 2026-10-11, self-approved lab exercise" },
  "scenario": { "name": "probe", "flow": "transport-probe", "model": "closed",
                "virtual_users": 2, "duration": "30s", "think_time": "200ms" },
  "report": { "output_dir": "reports", "formats": ["json", "terminal"] }
}
```

Everything not listed comes from defaults (1-minute closed runs, conservative
safety budgets, reports in `./reports`). The repository's `configs/` directory
contains three annotated examples — `example-probe.json`,
`example-closed.json`, `example-open.json` — each with
`authorization.accepted: false` so they can never run unmodified.

## 4.12 Secrets checklist

1. Passwords live in the environment (`password_env`) or a `0600` file
   (`password_file`) — never in config JSON, never in flags (flags leak via
   shell history and `ps`), never baked into images.
2. `scope_note` describes approval; it must not contain secrets.
3. The redacted resolved config (printed by `validate`/`run`, embedded in
   reports as `metadata.config_summary`) is safe to share by construction —
   verify with your own eyes the first time you use a new config.
4. Report JSON never contains the password or tokens: error samples are
   pre-redacted, response bodies are never logged, and usernames are excluded
   from metric labels by design (labels are bounded, low-cardinality sets).
