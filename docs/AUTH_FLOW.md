# Authentication Flow Specification (evidence-derived)

Source: `upload/HTTP_toolkit.txt` (15 exchanges, 2026-10-11 05:15:49–05:16:14 GMT, single session, test account `SRETEST`). Implementation: `internal/auth/mohaymen.go` via `internal/auth/auth.go`. **Every implemented behavior cites a capture step; anything not captured is treated as an error, never invented.**

## 1. Implemented chain (default configuration)

```
[GET /login]                         ← optional (transport.warm_login_page), capture step 1
POST /api/v1/auth/initiate-login     ← step 9   body {"userName": "<u>"}
POST /api/v1/auth/verifyPassword     ← step 10  body {"password": "<p>"}
GET  /api/v1/auth/authentication-status ← step 11 (flow.authentication_status, default on)
POST /api/v1/auth/login              ← step 12  body {}
GET  /api/v1/apps/users/dashboardPermissions ← step 13 (flow.session_proof, default on)
```

## 2. Request headers replicated from the capture

| Header | Value | Steps |
|---|---|---|
| `Accept` | `*/*` | all API calls |
| `Content-Type` | `application/json` | POSTs |
| `Accept-Language` | `fa-IR` (configurable) | all |
| `clientTimestamp` | Unix seconds, generated per request via the injectable clock | all |
| `Cross-Domain` | `true` | all |
| `Origin` | `<scheme>://<host>` | POSTs |
| `Referer` | `…/login` for initiate/verify; `…/auth-status` for status/login/proof | per capture |
| `Pre-Auth-Token` | value from initiate-login response header | steps 10–13 |
| `X-Auth-Token` | value from login response header | step 13+ |

Deliberately omitted: `sec-ch-ua*` and `Sec-Fetch-*` (browser-generated fingerprint headers; the client identifies honestly via `User-Agent: iam-perf/…`). Operators may not add spoofed browser headers via configuration — by design, to keep the generator non-evasive.

## 3. Dynamic values and correlation

| Value | Issued by | Consumed as | Lifetime |
|---|---|---|---|
| `pre-auth-token` | response **header** of initiate-login (step 9) | request header `Pre-Auth-Token` | one attempt; reset at chain start; never replayed from the capture |
| `x-auth-token` | response **header** of login (step 12) | request header `X-Auth-Token` | one attempt; treated as a secret |
| `cookiesession1` | NOT observed being set (pre-existed the capture) | cookie via per-VU jar | unknown role; no captured response sets it |
| `userId` | `data.userId` of login (step 12) | success criterion (must be non-null) | not logged, not reported |

## 4. Success criteria (as implemented)

An attempt is **successful** iff:

1. initiate-login → 2xx **and** `pre-auth-token` response header present (else class `protocol`);
2. verifyPassword → 2xx;
3. (if enabled) authentication-status → 2xx;
4. login → 2xx **and** `x-auth-token` header present **and** `data.userId` non-null (else `protocol` / `auth`);
5. (if enabled) session-proof → 2xx.

## 5. Failure classification (conservative — capture has no failures)

| Class | Trigger |
|---|---|
| `transport` | dial/write/read errors, connection resets |
| `timeout` | request timeout (client timeout / deadline) |
| `auth` | non-2xx from any auth step; null `userId` with 2xx |
| `protocol` | missing correlation headers, unparseable envelope, unbuildable request |
| `cancelled` | context cancelled mid-chain (attempt counted `aborted`, excluded from latency) |
| `aborted` | internal panic (recovered; run continues) |

Error messages contain step name + status/class only — never bodies, tokens, or credentials.

## 6. Non-goals / deliberately NOT implemented

- **CSRF handling** — no CSRF token appears anywhere in the capture; `pre-auth-token` is a correlation token. If the real service requires one on failure paths, it is uncaptured behavior.
- **MFA / second factor / IdP redirects** — not captured; `check-first-factor-password` and `forced:false` hint they exist. An unexpected response shape fails the attempt (class `auth`/`protocol`).
- **Redirect chains** — the capture contains zero redirects. Redirect following exists (config-limited) with allowlist enforcement on every hop, but the flow itself never relies on it.
- **Replay of captured session values** — forbidden by policy and by code (fresh values obtained per attempt).

## 7. Open questions (blocking precision, not correctness)

1. Failure status codes / envelope for wrong password, unknown user, locked account, throttling?
2. Is `cookiesession1` (or any edge cookie) required to authenticate?
3. `authentication-status`: poll-until-ready with which terminal states?
4. Per-account concurrency/lockout policy for multi-VU safe operation?
5. How many test accounts are authorized (ideal: one per VU)?
6. Envoy/WAF limits in front of the app that would confound measurements?

Answers refine `internal/auth` classification and `configs/` presets only — no structural change.
