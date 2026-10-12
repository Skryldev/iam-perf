# 5. Load models: virtual users, arrivals, ramps, and warm-up

`iam-perf` implements two execution models — **closed** and **open** — because
they answer different questions, and confusing them is the classic load-testing
mistake. This chapter explains exactly how each model behaves in this tool,
what ramp-up/down, warm-up, and think time do in each, how backpressure works,
and how to size a workload responsibly.

## 5.1 Closed model — "how long does a login take under N concurrent users?"

```json
"scenario": {
  "model": "closed",
  "virtual_users": 25,
  "duration": "3m",
  "ramp_up": "30s",
  "ramp_down": "10s",
  "warm_up": "15s",
  "think_time": "2s"
}
```

Each virtual user is an independent, fully isolated loop: create session →
(one login attempt → think time) repeated until the VU's deadline → session
discarded. "Isolated" is literal here — each VU owns its own `http.Client`,
connection pool slice, cookie jar, and token state. No state crosses VU
boundaries, which is what makes per-VU measurements meaningful and prevents
one VU's protocol state from contaminating another's.

Timing behavior, precisely:

- **Start stagger (`ramp_up`).** VU *i* starts at `T + i × (ramp_up / N)`.
  With 25 VUs and `ramp_up: 30s`, a new user starts every 1.2 s. Concurrency
  therefore grows linearly instead of snapping to 25 at once, which is both
  gentler on the target and reveals latency degradation as load builds.
- **Stop stagger (`ramp_down`).** VU *i*'s deadline is pulled earlier by
  `(N-1-i) × (ramp_down / N)`, so users switch off progressively instead of
  all at the same instant.
- **Think time** is the pause between attempts and is bounded by the VU's
  remaining window: a VU never sleeps past its own deadline, and a cancelled
  context ends the sleep (and the VU) immediately.
- **Per-VU attempt rate** is emergent: roughly `1000ms / (attempt_duration +
  think_time)` attempts per second per VU. You control concurrency; request
  rate follows. If the target slows down, the attempt rate *drops* — that is
  the closed model's nature and its value: it measures user-perceived latency
  under fixed concurrency, not a fixed demand.

Use the closed model for capacity questions of the form "how does p99 behave
at 25 concurrent users?" and for soak tests. It is also the gentler model to
start with on a new target.

## 5.2 Open model — "what happens when logins arrive at R per second?"

```json
"scenario": {
  "model": "open",
  "request_rate": 5.0,
  "max_in_flight": 32,
  "duration": "2m",
  "ramp_up": "10s",
  "ramp_down": "10s"
}
```

An arrival generator schedules one login *start* every `1/request_rate`
seconds, independent of how long attempts take. A pool of `max_in_flight`
workers pulls arrivals from a bounded queue and executes them.

- **Ramp-up** linearly scales the arrival interval from 20× the base interval
  down to the base interval over the `ramp_up` window (progress is floored at
  5 % so the very start is not infinitely slow).
- **Backpressure is explicit.** If all `max_in_flight` workers are busy when
  an arrival fires, the arrival is **dropped and counted**
  (`workload.backpressure_drops` in the report). The tool never queues
  arrivals without bound — an unbounded queue would silently manufacture the
  very latency you are trying to measure.
- **Arrivals stop** at `deadline − ramp_down`, and the run ends when in-flight
  attempts finish.
- **Think time is unused** in the open model; arrival pacing replaces it.

Use the open model for "what does the login service do at 20 logins/second?"
— the question that matches how production traffic actually arrives. Compare
`workload.configured_rps` against `workload.observed_rps`: a large gap with
non-zero `backpressure_drops` means the *generator or target* could not keep
up with demand (see §5.5).

## 5.3 Warm-up — measure the steady state, keep the evidence

```json
"warm_up": "15s"
```

Attempts that *start* inside the warm-up window are fully executed and counted
in `attempts.warmup_excluded`, but they are excluded from the attempt-latency
histogram and from rate denominators (the active duration shrinks by the
warm-up length). The point is honest numbers: the first seconds of a run are
dominated by JIT-like effects — TCP/TLS connection setup, keep-alive pools
filling, server-side caches cold. Warm-up removes that distortion without
hiding the traffic (the requests still happen and still count against
budgets), and the report still tells you how many attempts were excluded.

Rule of thumb: warm-up ≈ the time your target needs to reach steady state.
For a first probe, 5–15 s is plenty; for long soaks, a longer warm-up changes
little because it is a one-time exclusion.

## 5.4 Choosing numbers — a responsible sizing ladder

There is no substitute for starting small. The tool's own defaults and
examples embody the ladder; follow it:

1. **Step 0 — probe.** `transport-probe`, 1–2 VUs, 30 s. Confirms
   transport, DNS, TLS, allowlist, and reporting with zero authentication
   impact.
2. **Step 1 — single user.** `closed`, `virtual_users: 1`, `duration: 1m`.
   This is the *baseline* transaction: every later number is judged against
   it. Record p50/p99 and the per-step table.
3. **Step 2 — small concurrency.** `closed`, 5 VUs, 2 minutes, think time
   ≥ 1 s. This is exactly `configs/example-closed.json`. Watch the target's
   own monitoring while it runs.
4. **Step 3 — scale horizontally.** Raise VUs in steps (5 → 10 → 25 → …),
   with `ramp_up` long enough to watch each step settle (stagger = VUs
   ÷ ramp-up; keep the per-VU spacing ≥ 1 s while learning a target). Keep
   think time realistic — real users read pages; removing think time multiplies
   load in a way no production day looks like.
5. **Step 4 — open model**, only after closed behavior is understood. Start at
   a small fraction of expected production arrivals and step up.

Constraints that always apply, enforced by validation: VUs ≤
`safety.max_virtual_users`, `request_rate` ≤ `safety.max_requests_per_second`,
`duration` ≤ `safety.max_duration`, and the run can never exceed
`safety.max_total_requests` requests — the controller stops the run the moment
`requests_started` reaches the budget, even mid-window.

Two sanity checks before quoting any result: **generator health** (§5.5) and
**report limitations** (§7.6).

## 5.5 Is it the target or the generator?

A load generator is itself a program on a machine, and a saturated generator
produces garbage numbers that *look like* target problems. The tool therefore
records its own environment in every report (`environment.goos`, `goarch`,
`go_version`, `num_cpu`) and exposes the signals you need:

- `workload.backpressure_drops` > 0 (open model): demand exceeded
  `max_in_flight`. Either raise the cap (if the machine can take it) or
  accept that you measured a capped system.
- `observed_rps` far below `configured_rps` with 100 % success and low target
  latency: the generator host (CPU, ephemeral ports, bandwidth) is the
  bottleneck, not the target.
- Connection churn (`requests.new_connections` high, `connection_reuse` low):
  pool mis-sizing — raise `max_idle_conns_per_host` before concluding the
  target is slow to accept connections.

For reference ceilings on modest hardware, the repository's benchmarks
(2 vCPU, loopback fixture) put the *generator's* overhead at ≈ 0.17 ms of CPU
per full 5-request login attempt and ≈ 153 ns per metric record. Those numbers
bound the tool, not any target: a 2-vCPU container will never generate
thousands of RPS regardless of how fast the target is. Scale generators out,
not up: run one `iam-perf` process per host, each with its own report, and
aggregate offline — the tool deliberately does not coordinate across
generators (a single run ID and a single safety controller per process keep
the safety story simple and honest).

## 5.6 Model selection cheat sheet

| Question | Model | Key settings |
|---|---|---|
| Latency under fixed concurrency / soak | closed | `virtual_users`, `think_time` |
| Behavior at a fixed arrival rate | open | `request_rate`, `max_in_flight` |
| Gentle first contact with a target | probe flow, closed | 1–2 VUs, short duration |
| Finding the knee where p99 degrades | closed, stepped VUs | long `ramp_up`, generous `duration` |
| Verifying graceful degradation / overload alarms | open, stepped rates | tight `stop_429_count`, tight `stop_error_rate_pct` |

Whichever you choose, the safety layer behaves identically: the allowlist,
budgets, stop conditions, and cancellation semantics do not depend on the
model — only the arrival pattern does.
