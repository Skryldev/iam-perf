# 6. Safety controls and the operator runbook

This chapter is the one to re-read before every real run. It documents every
control the tool enforces, then packages them into a practical before / during
/ after runbook. The controls are not documentation decoration — each one is
implemented in `internal/safety` and covered by tests, and each one exists
because load generation against a shared service is a privileged operation.

## 6.1 The authorization model

Load generation requires **two independent keys**. Neither is sufficient
alone; both are checked by the same gate (`safety.RequireApproval`) before a
single socket is opened:

| Key | Where | Enforced |
|---|---|---|
| **1. Config assertion** | `"authorization": { "accepted": true }` in the config JSON (overridable via `IAMPERF_AUTHORIZATION_ACCEPTED`) | At the start of `Engine.Run` |
| **2. Runtime confirmation** | `--iam-accept-risk` flag **or** environment `IAMPERF_ACCEPT_RISK=true`/`1` | Same gate, same instant |

When the gate refuses, the run exits `1` with an actionable message:

```
run not authorized: set authorization.accepted=true in config AND pass
--iam-accept-risk (or env IAMPERF_ACCEPT_RISK=true)
```

Note the split of responsibilities this creates. The config file — which may
be committed, reviewed, and versioned — carries the *standing decision* that
this scenario is approved. The runtime key forces a *human (or automation)
present at execution* to consciously accept each run. A leaked scenario file
cannot generate load by itself; an accidental `--iam-accept-risk` in a wrapper
script cannot either without the approved config.

## 6.2 The allowlist — where traffic may go

The allowlist is built once from `target.base_url` + `target.allowed_paths`
and is immutable for the run:

- **Scheme + host:port must match exactly.** `https://iam.example.ir` does not
  authorize `http://iam.example.ir`, `iam.example.ir:8443`, or any other host.
  Violations fail with `host not in approved allowlist`.
- **Paths must fall under an approved prefix.** With
  `allowed_paths: ["/login", "/api/v1/"]`, the path `/api/v1/auth/login` is
  inside; `/api/v2/...` and `/admin` are not. Violations fail with
  `path not in approved scope: "…" is outside approved prefixes […]`.
- **Every redirect hop is re-checked.** The redirect policy passes each
  hop's URL through the same allowlist before following, so a compromised or
  misbehaving target cannot redirect the generator into attacking someone
  else.
- **The check also runs at request time** in the transport layer, so there is
  no path to the network that skips it.

Failure responses are counted as request failures in the report — they are
visible, never silent.

## 6.3 Budgets — the maximum size of any run

Four hard ceilings bound every run, enforced twice: as pre-flight validation
(the run refuses to start) and as live enforcement where applicable:

| Budget | Pre-flight | Live |
|---|---|---|
| `safety.max_virtual_users` | VU count above budget → exit 2 | — |
| `safety.max_requests_per_second` | open-model rate above budget → exit 2 | — |
| `safety.max_duration` | duration above budget → exit 2 | the workload window itself |
| `safety.max_total_requests` (default 200 000) | must be > 0 | controller trips at `requests_started ≥ budget` |

The total-request budget deserves emphasis: even if every other number is
misconfigured, the run **cannot** exceed this many started requests. It is the
final backstop, and the reason a typo like `"duration": "3h"` in a 30-minute
budget simply refuses to run rather than silently hammering a service.

## 6.4 Live stop conditions

A watcher goroutine polls the metric snapshot every 250 ms (default
`WatchInterval`). Conditions are **sticky** — once tripped, the workload is
cancelled and stays cancelled:

| Condition (default) | Meaning |
|---|---|
| `429` count ≥ `stop_429_count` (default 20) | The server is telling the generator to back off. The tool always respects this; it never retries a `429` and never adapts to evade it. |
| `5xx` count ≥ `stop_5xx_count` (default 100) | The target is erroring under load — stop before you make it worse. |
| Attempt error rate ≥ `stop_error_rate_pct` (default 50 %) | Something is systematically failing; measured only after `min_samples_for_stop` (default 20) attempts so tiny samples can't trip it. |
| Attempt p99 ≥ `stop_p99_millis` (default 10 000 ms) | The target is effectively unusable for users; more load adds no information. |
| `requests_started` ≥ `max_total_requests` | Budget exhausted (§6.3). |

When a condition trips, the run terminates as `safety-stop`; the exact reason
string (e.g. `safety: HTTP 429 count 10 reached limit 10`) is stored in
`safety.termination_reason`, printed by the terminal report as
`SAFETY STOP: …`, and the in-flight attempts drain with the same bounded grace
as a manual cancellation. A safety-stopped run is a *successful measurement*
— you learned where the edge is — but check the target's health before
re-running.

## 6.5 What the tool will never do

These are properties of the implementation, not policy statements:

- It performs **one login per attempt** with the one credential you supplied.
  It never varies credentials, never iterates over usernames or passwords, and
  has no facility to do so. (Brute force and spraying are structurally
  impossible, not merely discouraged.)
- It **never retries authentication POSTs**. A failed attempt ends; the VU's
  next attempt is a fresh, complete flow.
- It **never evades rate limits**: `429` is a stop condition, and there is no
  header/identity rotation anywhere in the code.
- It **never sends a request outside the allowlist**, including via redirects.
- It **never weakens TLS**: verification is always on; the only knob is
  *adding* trust (`trusted_cas_file`), not skipping it.
- It **never logs or reports secrets**: the password exists only in memory
  during the run; tokens are scrubbed from logs, metric samples, and errors by
  the redacting telemetry handler; response bodies are never logged.

## 6.6 Runbook — before the run

1. **Authorization.** You hold documented approval from the system owner
   covering: the target host, the login endpoint, the workload size, the time
   window, and the test account. Record it in `authorization.scope_note`
   (who / what / when — no secrets).
2. **Target correctness.** `validate -c` your config; read the redacted
   resolved output and confirm `base_url` letter-for-letter. A test domain
   typo'd into the production name is the classic accident; the exact-match
   allowlist is your second line of defense, and your eyes are the first.
3. **Test account.** Use a dedicated load-test account with the weakest
   privileges that still exercises login (the evidence flow ends with a
   `dashboardPermissions` check — the account must be able to log in, nothing
   more). Never a personal or administrative account.
4. **Stop conditions armed.** For a first run against a new target use the
   example-file posture (`stop_429_count: 10`, `stop_error_rate_pct: 30`,
   `stop_p99_millis: 8000`, `max_total_requests` well under what the target
   can absorb). Tighten, don't loosen, until you have a baseline.
5. **Communication.** Someone who owns the target knows the run is happening,
   the window, and how to reach you. On shared services this is not courtesy,
   it is part of authorization.
6. **Generator health.** Enough CPU/memory for the intended VU count (see
   §5.5); correct egress (proxy configured if needed); clock roughly sane
   (timestamps appear in reports).
7. **Dry-run the machinery.** If you have not exercised this exact config,
   `validate` it, then (for flow configs) run it once with
   `virtual_users: 1` and a 30-second window. Costs almost nothing, catches
   almost everything.

## 6.7 Runbook — during the run

The 3-second abort window is your last free cancellation: `Ctrl-C` there
sends nothing. Once load starts, your job is observation, not intervention:

- Watch the terminal output or the Prometheus endpoint
  (`metrics.prometheus_enabled: true`, then scrape `/metrics` on
  `metrics.prometheus_addr`) for `429`/`5xx` counters and attempt success
  rate.
- Watch the **target's own monitoring** simultaneously — the generator can
  only see its side of the wire. Cross-reference both when interpreting.
- If anything looks wrong, `SIGINT`/`SIGTERM` stops the run gracefully
  (≤ 10 s drain), writes the report, exits `130`. Do not `kill -9`: you lose
  the report for no benefit — the graceful path is fast.
- Do not re-run immediately after a safety stop. Read
  `safety.termination_reason` first; re-running into the same threshold is a
  signal you are measuring harm, not capacity.

## 6.8 Runbook — after the run

1. Confirm `metadata.termination` (`completed` | `safety-stop` | `cancelled`)
   and, if safety-stopped, the reason.
2. Read `limitations[]` in the report before quoting any number — the tool
   states its own evidence boundaries there (failure contract, session model,
   single-factor scope, generator-bound caveats).
3. Archive the report JSON/CSV together with the exact config (its redacted
   form is embedded in `metadata.config_summary`) and the binary version — a
   result without its config and version is not reproducible.
4. Report incidents (target errors, unexpected `429`s, alarms) to the target
   owner even when the run "went fine" — the data you no longer need may
   still matter to them.

## 6.9 Failure containment, in one paragraph

Assume every layer above fails to prevent a bad run — what actually happens?
The allowlist caps the blast radius to one host and its approved prefixes;
`max_total_requests` caps the count; `max_duration` caps the time; the
stop-condition watcher ends distress signals (`429`, `5xx`, error storms,
latency explosions) within one 250 ms poll plus drain; signals stop the run
within the drain grace; and every attempt, step, and drop is counted in a
report you can hand to the target owner afterwards. Layered like this, no
single misconfiguration produces an unbounded run — which is the standard a
tool that generates load on other people's systems should meet.
