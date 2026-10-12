# 7. Metrics and reports

Every run produces one schema-versioned JSON report, optionally a CSV, and a
terminal summary — all derived from the same metric snapshot. This chapter
explains what the tool measures, how the numbers are computed (which matters
when you quote them), the full report structure, and how to interpret results
honestly.

## 7.1 The two levels of measurement

The tool deliberately keeps two measurement levels separate, and so should you:

- **Login transactions ("attempts")** — the primary measure. One attempt is a
  full login flow; it *succeeds* only when the whole chain completes: the
  `login` step returns the `x-auth-token` response header **and** a non-null
  `data.userId`, and (when enabled) the `dashboardPermissions` session-proof
  call returns a success status. An HTTP `200` on some middle step is not
  success; a completed chain that ends without a session token is a failure.
- **HTTP requests** — the secondary measure. Every request of every flow,
  including the optional page-warm and status-poll steps, with status-code
  distribution, redirect, timeout, and connection counters. Never equate
  request counts or request success rates with login success: a target can
  return `200` to every request and still fail every login (e.g. `userId`
  null).

In the report: `attempts.*` vs `requests.*`; in the terminal summary: the
`-- login transactions --` block vs the `-- requests --` block.

## 7.2 How percentiles are computed (and why it matters)

Latency percentiles come from a **log-linear histogram** spanning 10 µs to
65 s (bucket edges generated with exact integer arithmetic). All workers
record into shared global buckets, and quantiles are interpolated from the
merged distribution. The design has two consequences worth knowing:

1. **No percentile averaging, ever.** Averaging per-worker p99s is a common
   load-tool error; this tool cannot do it by construction because workers
   share one histogram and percentiles are computed once, globally, at
   snapshot time.
2. **Bounded resolution, stated honestly.** Bucket widths are ~10 % relative,
   so a reported p99 carries up to ≈ 10 % bucketing error. That is the price
   of constant memory and lock-sharded speed (≈ 153 ns per record, ≈ 64 ns
   per global quantile on the reference host). For pass/fail threshold work
   (e.g. `stop_p99_millis`) the resolution is more than adequate; for
   sub-10 % precision comparisons, look at `mean_ms`, `min_ms`, and `max_ms`
   alongside.

Reported statistics per histogram: `count`, `mean_ms`, `p50_ms`, `p90_ms`,
`p95_ms`, `p99_ms`, `min_ms`, `max_ms`. Aborted attempts and warm-up attempts
never enter the attempt-latency histogram.

## 7.3 What is collected

**Counters** (from `metrics.Counters`, surfaced across the report):

| Counter | Report location | Meaning |
|---|---|---|
| Attempts started / succeeded / failed / aborted | `attempts.*` | Transaction outcomes. `aborted` = cancelled or panicked mid-flight, counted separately from failed. |
| Warm-up attempts | `attempts.warmup_excluded` | Executed inside the warm-up window; excluded from latency and rates. |
| Requests started / completed / failed | `requests.*` | HTTP-level activity. |
| Status distribution | `requests.status_distribution` | Exact code → count map (e.g. `"200": 4802, "429": 3`). |
| Redirects | `requests.redirects` | Followed redirect hops (allowlist-checked). |
| Connection reuse / new connections | `requests.connection_reuse` / `requests.new_connections` | Keep-alive health; high churn = pool mis-sizing (§5.5). |
| Timeouts / connection errors | `requests.timeouts` / `requests.connection_errors` | Distinct failure modes with distinct classes. |
| Backpressure drops | `workload.backpressure_drops` | Open-model arrivals dropped on a full queue. |

**Histograms:** end-to-end attempt latency (`latency.attempt`), plus
transport traces `traces.ttfb`, `traces.dns`, `traces.tcp`, `traces.tls`
— recorded only when that stage was observable (reused connections have no
DNS/TCP/TLS stage, so those histograms simply see fewer samples; `0` values
are never fabricated).

**Per-step data** (`steps[]`): for each named flow step — `login-page`,
`initiate-login`, `verify-password`, `authentication-status`, `login`,
`session-proof` — the counts started/failed, its status distribution, and its
own latency histogram. This is where "which endpoint is slow" questions get
answered; the CSV export is exactly this table.

**Errors** (`errors.*`): a bounded set of failure *classes* — `transport`,
`timeout`, `auth` (server rejected the attempt: non-2xx, missing token, null
userId), `protocol` (response shape violated the evidence contract),
`cancelled`, `aborted` — with counts and one representative **redacted**
sample message per class. Classes are a fixed low-cardinality set; usernames,
tokens, and response bodies never appear.

## 7.4 Report structure (schema v1)

```
schema_version      1
run_id              UUID (v4-shaped) identifying the run
generated_at        end-of-run timestamp
module_version      building binary's version
metadata            started_at, finished_at, duration_seconds, warmup_seconds,
                    termination (completed|safety-stop|cancelled),
                    config_summary (redacted resolved config)
target              base_url, allowed_paths, flow
scenario            name, model, virtual_users, duration_seconds, think_time_seconds
workload            configured_rps, observed_rps, attempt_rps, backpressure_drops
attempts            started, succeeded, failed, aborted, warmup_excluded,
                    success_rate_pct, failure_rate_pct
requests            started, completed, failed, status_distribution, redirects,
                    connection_reuse, new_connections, timeouts, connection_errors
latency.attempt     the global attempt-latency histogram stats
steps[]             per-step: step, started, failed, status_distribution, latency
errors              classes {class: count}, representative {class: message}
safety              tripped, termination_reason
traces              ttfb, dns, tcp, tls   (histogram stats each)
environment         goos, goarch, go_version, num_cpu
limitations[]       the tool's own disclosure block for this run
```

Field order is API-stable; `schema_version` changes only on breaking changes.
Rates use the **active duration** — wall-clock time minus the warm-up window —
so `observed_rps` and `attempt_rps` are steady-state rates, not averages that
include ramp. Full worked examples (generated from real local fixture runs:
1788 logins, 100 % success, loopback) live in `docs/examples/`.

**Files.** Written to `report.output_dir`:

```
iamperf-report-<runid[:8]>-<YYYYMMDDTHHMMSSZ>.json   # full report
iamperf-report-<runid[:8]>-<YYYYMMDDTHHMMSSZ>.csv    # per-step latency table
```

CSV columns: `step,started,failed,p50_ms,p90_ms,p95_ms,p99_ms,max_ms` — one
row per step, ready for spreadsheets.

**Terminal rendering** is the same block printed at run end; re-render any
stored report with `iam-perf report -f <file>`.

## 7.5 Prometheus (live view)

With `metrics.prometheus_enabled: true`, the CLI serves
`GET http://<prometheus_addr>/metrics` in Prometheus text format (0.0.4) for
the duration of the run. The endpoint starts only after the authorization
gate passes and dies with the process. Scrape it to graph the run live —
counters and histograms are the same underlying collector the final report
uses, so a live scrape and the report always agree. Keep the address on
`127.0.0.1` unless you deliberately want the port published; the compose file
shows the commented-out `ports:` line pattern for the deliberate case.

## 7.6 The `limitations` block — read before quoting numbers

Every report embeds a `limitations[]` array written by the tool itself. For
login-flow runs it currently records four facts: (1) the target's
failure-response contract was not in the supplied evidence, so failures are
classified conservatively (non-2xx / missing tokens / null userId); (2) the
session model is the captured header-token model — server cookie behavior was
never observed and is not modeled; (3) the flow is single-factor only — no
MFA/challenge path was captured, so none is implemented; (4) results are
bounded by the generator host — check `environment` and the §5.5 signals
before attributing limits to the target. Probe-flow runs add: "attempt
metrics describe HTTP probes, NOT successful logins."

These are not disclaimers for their own sake; they are the boundaries within
which this report's numbers mean what they say. Quote them alongside your
results.

## 7.7 Interpretation cookbook

- **"Is p99 good?"** Compare against the single-VU baseline (§5.4 step 1) at
  similar think-time, not against intuition. Degradation relative to baseline
  under concurrency is the signal; absolute milliseconds vary by network.
- **"Why is success rate < 100 %?"** `errors.classes` first: `auth` means the
  server rejected attempts (wrong password, lockout, conservative
  classification of an unexpected response — see limitations); `timeout` vs
  `transport` distinguishes slow from broken; `protocol` means the target's
  responses no longer match the evidence contract and the tool refused to
  guess.
- **"Which step is slow?"** `steps[]` / the CSV: a step with p99 ≫ the
  attempt p50 is your bottleneck; `traces.ttfb` vs `traces.tls` splits
  server-time from handshake-time for new connections.
- **"Did the generator keep up?"** Open model: `backpressure_drops` and
  `observed_rps` vs `configured_rps` (§5.5). Closed model: attempt rate per
  VU ≈ 1/(attempt+think) — a falling attempt rate with rising latency is the
  target slowing, not the generator.
- **"Comparing two runs?"** Same binary version (`module_version`), same
  environment class (`environment.*`), same config (`metadata.config_summary`),
  same warm-up — otherwise you are comparing anecdotes.

## 7.8 A note on what the tool does *not* measure

The report contains no per-request log, no request/response bodies, no
timestamps per attempt, and no high-cardinality labels — by design (privacy
and cardinality discipline; ADRs in `docs/ARCHITECTURE.md`). If you need
packet-level truth, run a separate capture on the generator host during the
window and correlate by time; the report's `started_at`/`finished_at` give
you the exact window.
