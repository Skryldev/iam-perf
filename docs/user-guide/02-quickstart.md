# 2. Quickstart: your first session in ten minutes

This chapter walks through one complete, safe session: build the tool, validate
a configuration, generate real load against a **localhost stub** (not any real
IAM system), inspect the report, and finally — only if you hold written
authorization — point the same machinery at a real target. Following the whole
chapter touches nothing but your own machine until the explicitly-marked final
step.

## 2.1 Build and confirm

```bash
cd iam-perf
make build
./bin/iam-perf version
```

You should see `iam-perf 0.1.0` and the Go toolchain version. If the version
line shows a stale value, rebuild — the version is injected at link time from
the `VERSION` file.

## 2.2 Understand the safety model before anything moves

Two design points shape everything you are about to do:

1. **`validate` and `report` never touch the network.** Only `run` generates
   traffic, and it refuses to start unless *two independent keys* are present:
   `authorization.accepted = true` in the config file (key 1) **and** the
   runtime confirmation `--iam-accept-risk` or `IAMPERF_ACCEPT_RISK=true`
   (key 2). Miss either one and the run is refused before any socket is
   opened.
2. **Every request is allowlisted.** The scheme and `host:port` must equal the
   configured `target.base_url`, and the URL path must fall under one of
   `target.allowed_paths`. The check runs on the initial request *and* on
   every redirect hop, so a redirect cannot pull the generator off-target.

## 2.3 Validate a configuration (offline)

```bash
./bin/iam-perf validate -c configs/example-probe.json
```

Output (abridged):

```
resolved configuration (redacted):
{ "schema_version": 1, "target": { "base_url": "https://iam.mohaymen.ir", ... } }

OK: configuration is valid. No network activity was performed.
```

Note what validation just caught for free: unknown fields (typos are rejected,
not ignored), out-of-range values, workload/safety contradictions such as more
virtual users than `safety.max_virtual_users`, and missing credentials for the
login flow. Get in the habit of running `validate` after every config edit —
it is the fastest way to catch mistakes before a run instead of during one.

## 2.4 Generate your first (harmless) load: localhost stub

The recommended first live exercise is the **probe flow** against a throwaway
HTTP stub on `127.0.0.1`. The probe flow (`"flow": "transport-probe"`) sends
`GET /login` requests only — no credentials, no authentication — and exists
precisely so you can exercise transport, models, metrics, reports, and
cancellation without an IAM server or any authorization risk.

Start a minimal stub (any static-file server works; this is the same pattern
used by `scripts/sigterm-smoke.sh`):

```bash
python3 - <<'PY' &
import http.server, socketserver
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"<html>ok</html>")
    def log_message(self, *a): pass
with socketserver.TCPServer(("127.0.0.1", 18099), H) as s:
    s.serve_forever()
PY
STUB_PID=$!
```

Create a stub config, `stub.json`:

```json
{
  "schema_version": 1,
  "target": { "base_url": "http://127.0.0.1:18099", "allowed_paths": ["/login"] },
  "authorization": { "accepted": true, "scope_note": "local stub exercise (who/when/approval)" },
  "scenario": {
    "name": "first-probe", "flow": "transport-probe", "model": "closed",
    "virtual_users": 2, "duration": "20s", "warm_up": "5s", "think_time": "200ms"
  },
  "report": { "output_dir": "reports", "formats": ["json", "csv", "terminal"] },
  "log": { "level": "info", "format": "text" }
}
```

Run it:

```bash
./bin/iam-perf run -c stub.json --iam-accept-risk
```

What you will see, in order:

1. The **redacted resolved configuration** is printed, so the record of what
   ran is on your terminal.
2. The message `safety: resolved config printed above; load starts in 3
   seconds (Ctrl-C aborts).` — a deliberate abort window. Press `Ctrl-C` now
   to prove it works: the run exits `130` with `run: cancelled before load
   started` and zero requests sent. Re-run the command to continue.
3. The workload executes for 20 seconds (probe attempts with 200 ms think
   time), then a **terminal report** is printed.

Sample terminal report (numbers will differ on your machine):

```
===== iam-perf report 3f2a…  =====
target: http://127.0.0.1:18099   flow: transport-probe
window: 20.0s (warmup 5.0s)   termination: completed

-- login transactions --
succeeded: 58   failed: 0   aborted: 0   success rate: 100.0%
transactions/sec: 3.87   (http req/sec: 3.87)

-- login latency (ms) --
p50 0   p90 0   p95 0   p99 1   max 2   (n=58)

-- requests --
started: 58   completed: 58   failed: 0   429: 0   5xx: 0   timeouts: 0   conn-errors: 0
conn reuse: 55   new conns: 3   redirects: 0
```

Kill the stub: `kill $STUB_PID`.

> With the probe flow, "login transactions" are really HTTP probes — the
> report says so explicitly in its embedded `limitations` array, and the
> `metrics`/`steps` sections make the distinction visible. Never quote probe
> numbers as login capacity.

## 2.5 Inspect the artifacts

```bash
ls reports/
# iamperf-report-<runid8>-<UTCtimestamp>.json
# iamperf-report-<runid8>-<UTCtimestamp>.csv
```

The JSON file is the complete machine-readable report (schema v1 — full tour
in [07-metrics-and-reports.md](07-metrics-and-reports.md)). The CSV has one
row per protocol step with latency columns. Re-render the stored JSON any
time without regenerating load:

```bash
./bin/iam-perf report -f reports/iamperf-report-*.json
```

Two fields worth finding immediately: `metadata.termination` (should be
`"completed"`) and `limitations` (the tool's own honesty block — read it
before quoting any number from the report).

## 2.6 Cancel a running load (graceful shutdown)

Start the stub run again, wait ~4 seconds (past the abort window), then send
`SIGINT` or `SIGTERM`:

```bash
./bin/iam-perf run -c stub.json --iam-accept-risk &
sleep 4; kill -TERM %1
```

The tool stops issuing new attempts immediately, lets in-flight attempts
finish (bounded by a 10-second drain grace), writes a complete report with
`"termination": "cancelled"`, prints the terminal summary, and exits `130`.
This is verified behavior — `scripts/sigterm-smoke.sh` automates exactly this
sequence, including the trickier case of a signal arriving *inside* the 3-second
abort window (zero load, exit `130`).

## 2.7 First real login run (authorization required)

Everything so far exercised the machinery. A real login test against a real
IAM service is an **authorized operation** — proceed only with documented
approval from the system's owner, and follow the pre-flight checklist in
[06-safety-and-operations.md](06-safety-and-operations.md). The mechanics:

```bash
# Secret via environment — never a flag, never a file in the repo
export IAMPERF_TEST_PASSWORD='the-test-account-password'

# Key 1 lives in the config: "authorization": { "accepted": true, "scope_note": "..." }
./bin/iam-perf run -c configs/example-closed.json --iam-accept-risk
```

The example config starts conservatively (5 virtual users, 2 minutes,
`stop_429_count: 10`), which is exactly where a first real run should start.
Watch the first minute; if the target stays healthy, scale the workload per
the sizing guidance in [05-load-models.md](05-load-models.md) — gradually, and
with the stop conditions still armed.

## 2.8 Where to go next

- **Command-by-command detail** → [03-cli-reference.md](03-cli-reference.md)
- **Every configuration field explained** → [04-configuration.md](04-configuration.md)
- **Choosing VU counts, rates, and durations** → [05-load-models.md](05-load-models.md)
- **What the report numbers mean** → [07-metrics-and-reports.md](07-metrics-and-reports.md)
- **Running from containers** → [09-docker.md](09-docker.md)
- **Errors and fixes** → [10-troubleshooting-and-faq.md](10-troubleshooting-and-faq.md)
