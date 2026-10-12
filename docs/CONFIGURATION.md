# Configuration Reference (schema v1)

Layering: **defaults < file < environment (`IAMPERF_*`) < CLI flags**. Validation errors are actionable and prefixed `invalid configuration:`. `iam-perf validate -c file` renders the fully-resolved, redacted configuration.

## target

| Field | Type | Default | Notes |
|---|---|---|---|
| `base_url` | string | `https://iam.mohaymen.ir` | `http(s)://host[:port]`; anchors the allowlist. |
| `allowed_paths` | []string | `["/login","/api/v1/"]` | Prefix list enforced on every request and redirect hop. Entries must start with `/`. |

## authorization

| Field | Type | Default | Notes |
|---|---|---|---|
| `accepted` | bool | `false` | Key 1 of 2. `run` refuses without it. |
| `scope_note` | string | `""` | Who approved what/when. Free text; no secrets. |

## scenario

| Field | Type | Default | Notes |
|---|---|---|---|
| `name` | string | `login-baseline` | Label only. |
| `flow` | string | `mohaymen-login` | `mohaymen-login` \| `transport-probe`. |
| `model` | string | `closed` | `closed` (N VUs loop) \| `open` (arrival rate). |
| `virtual_users` | int | 1 | closed model; must be ≤ `safety.max_virtual_users`. |
| `duration` | duration | `1m` | Must be ≤ `safety.max_duration`. |
| `ramp_up` | duration | `0s` | Closed: staggered VU start. Open: arrival-rate ramp. |
| `ramp_down` | duration | `0s` | Closed: staggered VU stop. Open: stop submitting early. |
| `warm_up` | duration | `0s` | Attempts tagged `warmup_excluded`; excluded from latency + rates. |
| `think_time` | duration | `1s` | Closed-model pause between attempts. |
| `request_rate` | float | 0 | open model; must be ≤ `safety.max_requests_per_second`. |
| `max_in_flight` | int | 64 | open model; bounded queue size = worker count. |

## transport

| Field | Type | Default | Notes |
|---|---|---|---|
| `request_timeout` | duration | `15s` | Whole-request (client.Timeout). |
| `dial_timeout` | duration | `5s` | TCP connect. |
| `tls_handshake_timeout` | duration | `5s` | — |
| `response_header_timeout` | duration | `10s` | — |
| `idle_conn_timeout` | duration | `60s` | Pool keep-alive hygiene. |
| `max_idle_conns` | int | 256 | — |
| `max_idle_conns_per_host` | int | 64 | Keep ≥ VU count to avoid pool-induced queuing. |
| `max_conns_per_host` | int | 0 (unlimited) | Bounded naturally by VUs / max_in_flight. |
| `max_redirects` | int | 5 | Followed hops; every hop allowlist-checked. |
| `max_response_body_bytes` | int | 1 MiB | Oversized bodies fail the step (class `transport`). |
| `user_agent` | string | honest iam-perf UA | Change only with documented approval; spoofing browsers is discouraged. |
| `accept_language` | string | `fa-IR` | Mirrors capture. |
| `trusted_cas_file` | string | `""` | PEM CA bundle for lab PKIs. **No skip-verify option exists.** |
| `proxy_url` | string | `""` | Explicit approved proxy only. |
| `warm_login_page` | bool | `false` | Adds capture step 1 (GET /login) before the auth chain. |

## credentials

| Field | Type | Default | Notes |
|---|---|---|---|
| `username` | string | `""` | Required for `mohaymen-login`. Redacted from all outputs. |
| `password_env` | string | `IAMPERF_TEST_PASSWORD` | Env var name holding the password. |
| `password_file` | string | `""` | Alternative; mode must be exactly `0600`. Mutually exclusive with `password_env`. |

## safety (budgets + stop conditions)

| Field | Type | Default | Notes |
|---|---|---|---|
| `max_virtual_users` | int | 100 | Hard cap. |
| `max_requests_per_second` | float | 200 | Hard cap (open model). |
| `max_duration` | duration | `30m` | Hard cap. |
| `max_total_requests` | int | 200000 | Controller stops the run at the budget. |
| `stop_error_rate_pct` | float | 50 | Trip threshold after `min_samples_for_stop`. |
| `stop_5xx_count` | int | 100 | — |
| `stop_429_count` | int | 10 | 429 = target says stop; keep low. |
| `stop_p99_millis` | float | 10000 | Attempt p99 ceiling. |
| `min_samples_for_stop` | int | 20 | Avoids premature trips. |

## metrics / report / log

| Field | Type | Default | Notes |
|---|---|---|---|
| `metrics.prometheus_enabled` | bool | false | Serves `/metrics` (text format) during the run. |
| `metrics.prometheus_addr` | string | `127.0.0.1:9537` | host:port, validated. |
| `report.output_dir` | string | `reports` | Created on demand. |
| `report.formats` | []string | `["json","terminal"]` | Subset of `json`,`csv`,`terminal`. |
| `log.level` | string | `info` | `debug`,`info`,`warn`,`error`. |
| `log.format` | string | `text` | `text`,`json`. |

## flow (evidence chain tuning)

| Field | Type | Default | Notes |
|---|---|---|---|
| `initiate_login_path` | string | `/api/v1/auth/initiate-login` | Capture step 9. |
| `verify_password_path` | string | `/api/v1/auth/verifyPassword` | Step 10. |
| `authentication_status_path` | string | `/api/v1/auth/authentication-status` | Step 11. |
| `login_path` | string | `/api/v1/auth/login` | Step 12. |
| `session_proof_path` | string | `/api/v1/apps/users/dashboardPermissions` | Step 13. |
| `session_proof` | bool | `true` | Perform step 13. |
| `authentication_status` | bool | `true` | Perform step 11. |

## Environment overrides

`IAMPERF_TARGET_BASEURL`, `IAMPERF_AUTHORIZATION_ACCEPTED`, `IAMPERF_SCENARIO_{NAME,FLOW,MODEL,VIRTUAL_USERS,DURATION,THINK_TIME,RAMP_UP,RAMP_DOWN,WARMUP,REQUEST_RATE,MAX_IN_FLIGHT}`, `IAMPERF_TRANSPORT_MAX_REDIRECTS`, `IAMPERF_CREDENTIALS_USERNAME`, plus `IAMPERF_ACCEPT_RISK` (runtime confirmation) and the password variable itself. Duration values accept `"45s"` / `"90"` (seconds).
