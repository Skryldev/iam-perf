# Safety Controls & Operational Runbook

## 1. Control inventory

| # | Control | Where | Behavior |
|---|---|---|---|
| 1 | Target allowlist | `internal/safety` | Host:port + path-prefix allowlist built from `target.*`. Enforced on the initial request URL **and every redirect hop**. Violation aborts the attempt/run with `ErrTargetNotAllowed`. |
| 2 | Two-key approval | `internal/safety.RequireApproval` | `run` proceeds only when config `authorization.accepted=true` **AND** `--iam-accept-risk` flag (or `IAMPERF_ACCEPT_RISK=true`). `validate`/`report` need no keys because they perform no network I/O. |
| 3 | Hard budgets | config `safety.*` | Max virtual users (100 default), max request rate, max duration, max total requests. Validation rejects configs exceeding budgets **before** any load. |
| 4 | Stop-condition controller | `internal/safety.Controller` | Live checks every 250 ms: `429` count (default trip ≥ 10), `5xx` count (≥ 100), attempt error-rate % (≥ 50 % after ≥ 20 samples), p99 latency ceiling, total-request budget. Sticky: once tripped, the run ends and the reason is written to the report. |
| 5 | Bounded resources | `internal/runner` | Fixed VU set (closed) or fixed `max_in_flight` queue (open); overflow arrivals are **dropped and counted** (`backpressure_drops`), never queued unboundedly. Hard total-request counter. Panic recovery per VU. |
| 6 | No auth-POST retries | `internal/auth` | `initiate-login`, `verifyPassword`, `login` are never retried on any error. A transport failure fails the attempt once. |
| 7 | Graceful termination | CLI + runner | SIGINT/SIGTERM from process start (incl. the 3 s abort window): stop issuing attempts, drain in-flight ≤ 10 s, write report `termination:"cancelled"`, exit 130. |
| 8 | Secret hygiene | everywhere | Passwords/tokens: env/file-injected, `0600` enforced for files, redacting slog handler, token-shape scrubbing on errors, username redacted in reports/config dumps, no secrets in flags/images/reports. |
| 9 | Honest identity | transport | `User-Agent: iam-perf/<ver> (+load-test; approved-scope-only)`. No browser fingerprint spoofing. TLS verification cannot be disabled. |

## 2. Pre-flight checklist (operator)

1. **Authorization:** written approval naming the target, scope (paths), duration, rate ceiling, and test accounts. Record it in `authorization.scope_note`.
2. **Environment:** confirm you are targeting the approved environment (staging/lab). The config's `target.base_url` must match the approval — the allowlist will pin it.
3. **Accounts:** use dedicated test accounts, never production user credentials. Confirm the account lockout threshold and keep `virtual_users × rate` far below it. Ideally one account per VU.
4. **Budgets:** set `safety.*` to the APPROVED traffic budget, not to the framework maximums.
5. **Smoke first:** run `validate`, then a short probe run (`configs/example-probe.json`, non-authenticating) to verify routing/DNS/TLS without authentication load.
6. **Abort path:** verify Ctrl-C/SIGTERM produces exit 130 and a `cancelled` report before the real test.
7. **Observer present:** watch the target's own dashboards; the controller reacts to HTTP signals only.

## 3. Runbook — executing an approved test

```bash
export IAMPERF_TEST_PASSWORD='<from your secret store>'
./bin/iam-perf validate -c my-scope.json          # gate 0: config sanity, no network
./bin/iam-perf run -c my-scope.json --iam-accept-risk   # gates 1–3 enforced here
```

During the run:
- Terminal shows live step of the abort window and, at the end, the summary.
- Optional Prometheus view: `metrics.prometheus_enabled` + `prometheus_addr`.

After the run:
- Reports: `reports/iamperf-report-<runid8>-<ts>.json` (+ `.csv` if configured).
- `termination: safety-stop` ⇒ read `safety.termination_reason`; treat as target distress; investigate before re-running.
- `termination: cancelled` ⇒ operator-initiated; partial metrics are still valid for the executed window.

## 4. Runbook — aborting

- Interactive: Ctrl-C (SIGINT).
- Orchestrated: `docker stop` (SIGTERM; compose grace 15 s > 10 s drain) or `kill -TERM <pid>`.
- Verify: exit code 130, report written with `termination:"cancelled"`, no further target traffic after drain.

## 5. Runbook — incidents

| Symptom | Immediate action |
|---|---|
| Target returns 429s | Controller stops the run automatically (do not raise `stop_429_count` for production). Respect the signal. |
| Error rate spikes | Controller trips at the configured %; check `errors.classes` + representative redacted errors. |
| Account locked | Stop; lockout means the budget was mis-set versus the account policy (open question #4 in AUTH_FLOW). |
| Generator saturated (CPU 100 %, observed_rps << configured) | Results are generator-bound: do not attribute limits to the target. Scale out or reduce rate. |

## 6. Prohibited uses (framework refuses by design; operator obligation regardless)

Credential guessing / password spraying / brute force · MFA bypass · rate-limit evasion · account enumeration · load against unapproved hosts or the real production endpoint without documented approval and traffic budget.
