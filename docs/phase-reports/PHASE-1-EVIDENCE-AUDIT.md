# Phase 1 — Repository & Evidence Audit (Engineering Report)

- Date: 2026-10-11
- Status: COMPLETE
- Next phase readiness: READY (Phase 2 — Architecture & API design)

## 1. Repository Inventory

The workspace at `/home/z/my-project/` is **greenfield**. Findings:

| Item | State |
|---|---|
| Existing source code | None — no repository, no build tooling, no prior conventions to preserve |
| Supplied evidence | `upload/HTTP_toolkit.txt` (1468 lines, ~46 KB, 15 HTTP request/response exchanges) |
| Toolchain | Go 1.27.2 (linux/amd64) installed during this phase |
| Container tooling | Docker **not available** in this build environment (image build must be verified on an operator machine; Dockerfile/compose are still produced and statically reviewed) |
| Target reachability | Not probed and will not be probed. No packets were sent to `iam.mohaymen.ir` during this phase and none will be until the authorization gate in `internal/safety` is satisfied. |

Nothing was overwritten; there was no pre-existing work.

## 2. Evidence-to-Implementation Mapping

The capture is a single authenticated session (HTTP Toolkit, 2026-10-11 05:15:49–05:16:14 GMT) against `https://iam.mohaymen.ir`. The SPA front end and JSON API sit behind an `envoy` proxy; all API responses use the envelope `{"data": <T>, "message": <string|null>, "timestamp": <int>}`. The capture uses a dedicated test account (`SRETEST`).

### 2.1 Captured exchange sequence

| # | Method | Path | Critical request facts | Status | Critical response facts |
|---|---|---|---|---|---|
| 1 | GET | `/login` | Browser UA; `Cookie: cookiesession1=…` already present (from an earlier, uncaptured visit) | 200 `text/html` (gzip) | SPA shell, CSP, `server: envoy` |
| 2 | GET | `/api/v1/branding` | `Accept-Language: fa-IR`, `clientTimestamp: 1791695749`, `Cross-Domain: true`, `Referer: /login` | 200 JSON | branding data |
| 3 | GET | `/api/v1/settings/dashboardtheme` | same pattern | 200 JSON | theme data |
| 4 | GET | `/api/v1/settings/logineditor` | same pattern | 200 JSON | login page config |
| 5 | GET | `/api/v1/settings/two-login-pages-config` | same pattern | 200 JSON | config |
| 6 | GET | `/api/v1/auth/check-first-factor-password` | same pattern | 200 JSON (62 B) | first-factor availability flag |
| 7 | GET | `/api/v1/activated-idp-list` | same pattern | 200 **`text/html`** (1413 B — same length as `/login`; server returns the SPA shell for this path) | no IdP list observed |
| 8 | GET | `/service-worker.js` | `If-None-Match` | 304 | not part of the login transaction |
| 9 | POST | `/api/v1/auth/initiate-login` | `Content-Type: application/json`, body `{"userName":"SRETEST"}`, `Origin`, `Referer: /login`, `Cross-Domain: true`, `clientTimestamp` | 200 | **response header `pre-auth-token: 80ff55a0-…`**; body `{"data":null,…}` |
| 10 | POST | `/api/v1/auth/verifyPassword` | header `Pre-Auth-Token: 80ff55a0-…`; body `{"password":"…"}` | 200 | `{"data":null,…}` |
| 11 | GET | `/api/v1/auth/authentication-status` | header `Pre-Auth-Token: …`; `Referer: /auth-status` | 200 | `{"data":null,…}` (single sample) |
| 12 | POST | `/api/v1/auth/login` | header `Pre-Auth-Token: …`; body `{}` | 200 | **response header `x-auth-token: 9fbda2b5-…`**; body `{"data":{"userId":"2a5c0e49-…","forced":false}}` |
| 13 | GET | `/api/v1/apps/users/dashboardPermissions` | headers `Pre-Auth-Token: …` **and** `X-Auth-Token: …` | 200 | `{"data":["postAuth:authenticated"]}` |
| 14 | GET | `/api/v1/users/profile` | header `X-Auth-Token: …` | 200 | profile of the test account |
| 15 | GET | `/api/v1/user-category` | header `X-Auth-Token: …`; `Referer: /dashboard` | 200 | post-login app catalog (appears twice) |

### 2.2 Protocol model (evidence-derived)

1. **Bootstrap (optional for load):** `GET /login` plus SPA configuration GETs (steps 2–8). These carry no auth semantics; they exist to render the page.
2. **Initiate:** `POST /api/v1/auth/initiate-login` with JSON body `{"userName": "<username>"}`. The response **header `pre-auth-token`** carries a server-generated correlation UUID. This token is required by every subsequent pre-auth request as the `Pre-Auth-Token` request header.
3. **Verify first factor:** `POST /api/v1/auth/verifyPassword` with body `{"password": "<password>"}` and the `Pre-Auth-Token` header.
4. **Authentication status (observational):** `GET /api/v1/auth/authentication-status` with the `Pre-Auth-Token` header. The single captured sample returned `data: null`; the SPA treats this as a pollable endpoint. It did **not** gate the next step in the capture.
5. **Establish session:** `POST /api/v1/auth/login` with body `{}` and the `Pre-Auth-Token` header. Success is signaled by the **response header `x-auth-token`** (session token) and a non-null `data.userId` in the body.
6. **Session proof:** `GET /api/v1/apps/users/dashboardPermissions` with `X-Auth-Token` (and, in the capture, still `Pre-Auth-Token`) returns `["postAuth:authenticated"]`.

### 2.3 Dynamic values and correlation

| Value | Origin | Propagation | Implementation duty |
|---|---|---|---|
| `pre-auth-token` | Response **header** of step 9 | Request **header** `Pre-Auth-Token` on steps 10–13 | Extract per attempt; never reuse across attempts |
| `x-auth-token` | Response **header** of step 12 | Request **header** `X-Auth-Token` on steps 13+ | Extract per attempt; treat as secret |
| `clientTimestamp` | Client-generated Unix seconds | Request header on all API calls | Generate per request via injectable clock |
| `cookiesession1` | Not observed being set (pre-existed capture) | Cookie, host-scoped | Each VU gets an isolated cookie jar; the value must not be replayed from the capture |

### 2.4 Explicitly NOT observed (no fabrication allowed)

- **No CSRF token** appears in any request/response in the capture. The `pre-auth-token` is a correlation token, not a CSRF token. Implementation carries a documented assumption: bearer-of-pre-auth-token is accepted by the server.
- **No HTTP redirects** anywhere in the chain.
- **No MFA / second factor** for this account: `check-first-factor-password` suggests other first factors exist, and `authentication-status`/`forced:false` hint at possible challenge flows, but none was captured. The implemented client models only the captured single-factor path and treats any unexpected `data` shape as a protocol error rather than guessing.
- **No failure responses captured.** The failure envelope, error status codes, and lockout/`429` semantics of the real service are unknown. The client therefore classifies conservatively: non-2xx, missing `pre-auth-token`, missing `x-auth-token`, or null `data.userId` ⇒ attempt failure with a redacted reason. The real failure contract is an open question.
- **No `Set-Cookie` in any captured response.** Session state is header-token based in the capture; the only cookie (`cookiesession1`) predates it and is presumably edge/WAF-related. Unknown whether the server requires it.
- **`/api/v1/activated-idp-list` returned HTML**, not JSON — evidence of an SPA fallback route; treated as non-essential and excluded from the default flow.

### 2.5 Sensitive-material handling

The capture contains a real password value for the test account and live token/userId values. Actions taken:

- The password is **never** embedded in source, configs, tests, docs, or images. The framework reads credentials from `IAMPERF_PASSWORD`-style environment injection or a file with enforced `0600` permissions.
- Captured token values are recorded here **only** as opaque evidence markers and are excluded from all code.
- Every log line, error, report field, and trace attribute passes through a redaction middleware (`internal/telemetry` + `internal/auth` scrubbing of `password`, `x-auth-token`, `pre-auth-token`, `authorization`, `cookie`).

## 3. Missing-Information List (open questions)

1. **Failure contract:** What status codes / envelope shapes does the service return for wrong password, unknown user, locked account, and rate limiting? (Blocks precise failure classification; current behavior is conservative.)
2. **Session model:** Is `x-auth-token` also acceptable as a cookie (`Set-Cookie`)? Does the platform require `cookiesession1` to be pre-seeded by hitting `/login` first?
3. **`authentication-status` semantics:** Poll-until-ready (with which terminal states), or fire-once observation? The capture shows one call with `data: null` followed by successful login.
4. **Server-side concurrency policy:** Per-account concurrent-session limits, lockout thresholds, and identity-provider throttling — needed to set safe per-account rates and VU counts.
5. **Test-account capacity:** How many authorized test accounts exist for multi-VU isolation? All VUs on one account may distort results and risk lockout.
6. **Edge behavior:** Envoy/WAF rate limits in front of the app (timeouts, connection caps) that would confound measurements.

## 4. Proposed Architecture (carried into Phase 2)

Module `example.com/iam-perf` (placeholder import path, rename documented for publishing):

```
cmd/iam-perf/        CLI: validate | run | report | version
internal/config/     schema, defaults, file+flag+env layering, validation, redaction
internal/safety/     host/path allowlist, budgets, approval gate, stop-condition controller
internal/transport/  client factory: pool tuning, redirects, timeouts, jars, httptrace hooks
internal/auth/       LoginFlow interface + evidence-based Mohaymen flow + redaction
internal/runner/     VU lifecycle, closed/open models, ramp/warmup, bounded queues
internal/metrics/    lock-sharded collectors, log-linear histograms, per-step + per-attempt
internal/report/     JSON (schema-versioned), terminal summary, CSV
internal/telemetry/  slog setup, redaction handler
pkg/iamperf/         stable public API: Engine, Config, Report, errors
tests/               offline integration/e2e suites (httptest only)
configs/             safe example configs (no secrets)
docs/                architecture, auth-flow spec, safety, runbook, phase reports
```

Key decisions and trade-offs are recorded in `docs/ARCHITECTURE.md` (Phase 2 deliverable).

## 5. Initial Threat Model & Safety Requirements

Threats introduced by a load generator and the controls that become mandatory:

| Threat | Control |
|---|---|
| Load fired at unapproved hosts (misconfiguration, SSRF-style drift) | Hard allowlist of host:port + path prefixes; every request URL checked before dial; violation aborts run |
| Unintentional production impact | Dry-run default; run requires config `authorization.accepted=true` **and** `--iam-accept-risk` (or env `IAMPERF_ACCEPT_RISK=true`); hard caps: max VUs, max rate, max duration, max total requests |
| Runaway generator (bug storm) | Bounded in-flight queue, semaphore-bounded concurrency, hard request counter, context cancellation, panic isolation per VU |
| Target distress during test | Stop-condition controller: error-rate %, 5xx count, 429 count, p99 latency ceiling — checked continuously; stop is immediate and graceful |
| Credential leakage | No secrets in flags/files/source/images; env/file injection only; redaction middleware on all sinks; secrets never appear in reports |
| Account lockout / auth abuse | No retries of auth POSTs (transport errors on auth steps are failures, not retries); single flow per attempt; per-account pacing via think time; documented lockout warning |
| Evidence contamination | Captured tokens/cookies never replayed; fresh values obtained via the protocol each attempt |
| Logs as an attack surface | No usernames/tokens in metric labels (bounded label set); representative errors are classified + redacted |

## 6. Phase 1 Verification Checklist

- [x] Evidence read in full (1468/1468 lines).
- [x] Flow documented with per-exchange evidence references; nothing invented.
- [x] Uncertainties and open questions enumerated (Section 3).
- [x] Sensitive values identified and excluded from all artifacts.
- [x] No network traffic generated toward the target.
- [x] Toolchain ready (Go 1.27.2); Docker availability gap recorded honestly.

## 7. Decision Request Before Protocol Implementation

Per the workflow gates, protocol-specific implementation (Phase 3 auth client) proceeds on the **captured single-factor happy path** with the conservative failure interpretation above, because the open questions do not block a correct implementation of the observed behavior — they only refine failure classification, which is config-extensible later. Load generation against the real service remains locked behind the authorization gate regardless.
