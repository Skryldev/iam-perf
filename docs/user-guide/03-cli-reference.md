# 3. CLI reference

`iam-perf` has four commands: `validate`, `run`, `report`, and `version`.
Running the binary with no arguments, with `help`, `-h`, or `--help` prints
the usage text; an unknown command prints an error and the usage text, then
exits `2`.

```
Usage:
  iam-perf validate -c <config.json>
  iam-perf run -c <config.json> [--iam-accept-risk] [--out <dir>]
  iam-perf report -f <report.json>
  iam-perf version
```

Flag parsing is standard Go `flag` package semantics: flags may appear in any
order after the command name, `-flag=value` and `-flag value` both work, and
`-h`/`--help` on any subcommand prints that subcommand's flag help.

## 3.1 `validate` — parse and validate configuration

```bash
iam-perf validate -c <config.json>
```

| Flag | Required | Meaning |
|---|---|---|
| `-c` | yes | Path to the configuration JSON file. |

What it does, in order: loads built-in defaults, overlays your JSON file,
overlays `IAMPERF_*` environment variables, enforces `schema_version`, runs
the full validation pass (URL shape, flow/model constraints, workload-vs-budget
consistency, credential source declarations, format checks), and prints the
fully **redacted resolved configuration** as JSON.

What it never does: open a network socket. `validate` performs zero network
activity by design — it is the tool's dry-run and your first safety habit.

Exit codes: `0` configuration valid · `2` file missing/unreadable, JSON
syntax error, unknown field, schema-version mismatch, or any validation
failure (the error message names the offending field and the fix).

## 3.2 `run` — safety-gated workload execution

```bash
iam-perf run -c <config.json> [--iam-accept-risk] [--out <dir>]
```

| Flag | Required | Meaning |
|---|---|---|
| `-c` | yes | Path to the configuration JSON file. |
| `--iam-accept-risk` | no (but see below) | Runtime authorization key 2. Equivalent to environment `IAMPERF_ACCEPT_RISK=true`. |
| `--out` | no | Overrides `report.output_dir` for this run. |

### Execution sequence

1. **Config load** — same layering as `validate` (defaults < file <
   `IAMPERF_*` env < flags). Failures exit `2` before anything else happens.
2. **Signal handlers installed first.** SIGINT/SIGTERM are graceful from the
   very first millisecond, including inside the abort window below.
3. **Redacted resolved configuration printed** to stdout — the run's durable
   record of what was about to happen.
4. **3-second abort window** — the tool prints
   `safety: resolved config printed above; load starts in 3 seconds (Ctrl-C aborts).`
   A signal during the window prints `run: cancelled before load started`,
   exits `130`, and sends nothing.
5. **Engine construction** — configuration re-validated; a Prometheus
   endpoint is prepared if `metrics.prometheus_enabled` is set.
6. **`Engine.Run` begins** with the authorization gate: it requires
   `authorization.accepted = true` **and** runtime confirmation. Refusal
   prints the exact remediation and exits `1` with zero traffic sent.
7. **Allowlist built; credentials resolved** (only for the login flow, only
   after the gate passes); per-VU clients constructed; workload starts.
8. **Stop-condition watcher polls every 250 ms** while the workload runs;
   tripping a condition cancels the workload (see
   [06-safety-and-operations.md](06-safety-and-operations.md)).
9. **Report written** to `report.output_dir` in every configured format; the
   terminal summary prints; exit code reflects the outcome.

### Prometheus endpoint

With `"metrics": {"prometheus_enabled": true, "prometheus_addr": "127.0.0.1:9537"}`
the CLI serves `GET /metrics` (Prometheus text format 0.0.4) at that address
for the duration of the run, starting only after the authorization gate has
passed. The endpoint dies with the process; there is nothing to shut down
manually. If the address cannot be bound, the run proceeds and a warning is
printed — the endpoint is observability, never a load-bearing component.

## 3.3 `report` — re-render a stored report

```bash
iam-perf report -f <report.json>
```

| Flag | Required | Meaning |
|---|---|---|
| `-f` | yes | Path to a JSON report produced by a previous run. |

Reads the report file, parses it into the schema-v1 `Report` structure, and
prints the same terminal summary that the run itself produced. Useful for
triage on a machine that never generated load, and for re-checking a stored
report months later. Exit codes: `0` rendered · `1` file unreadable or not a
parseable report.

## 3.4 `version` — build metadata

```bash
iam-perf version
```

Prints the module version (injected at link time from the `VERSION` file) and
the compiling Go toolchain version. Include this output when filing issues or
comparing reports from different generator hosts.

## 3.5 Environment variables

Two kinds of environment variables exist: **configuration overrides** (part of
the layering: defaults < file < env < flags) and **security-sensitive
variables** (never part of configuration, never rendered in the redacted
config print).

### Configuration overrides (`IAMPERF_*`)

Applied only when set and non-empty; invalid values are silently ignored
(the file value or default remains in force — run `validate` to see what
actually took effect).

| Variable | Overrides |
|---|---|
| `IAMPERF_TARGET_BASEURL` | `target.base_url` |
| `IAMPERF_AUTHORIZATION_ACCEPTED` | `authorization.accepted` (`true`/`1`) |
| `IAMPERF_SCENARIO_NAME` | `scenario.name` |
| `IAMPERF_SCENARIO_FLOW` | `scenario.flow` |
| `IAMPERF_SCENARIO_MODEL` | `scenario.model` |
| `IAMPERF_SCENARIO_VIRTUAL_USERS` | `scenario.virtual_users` |
| `IAMPERF_SCENARIO_DURATION` | `scenario.duration` |
| `IAMPERF_SCENARIO_THINK_TIME` | `scenario.think_time` |
| `IAMPERF_SCENARIO_RAMP_UP` | `scenario.ramp_up` |
| `IAMPERF_SCENARIO_RAMP_DOWN` | `scenario.ramp_down` |
| `IAMPERF_SCENARIO_WARMUP` | `scenario.warm_up` |
| `IAMPERF_SCENARIO_REQUEST_RATE` | `scenario.request_rate` (float) |
| `IAMPERF_SCENARIO_MAX_IN_FLIGHT` | `scenario.max_in_flight` |
| `IAMPERF_TRANSPORT_MAX_REDIRECTS` | `transport.max_redirects` |
| `IAMPERF_CREDENTIALS_USERNAME` | `credentials.username` |

### Security-sensitive variables

| Variable | Role |
|---|---|
| `IAMPERF_TEST_PASSWORD` | Default password source: the variable named by `credentials.password_env` (this name is the default). Read at run start, held in memory only, never logged or marshaled. |
| `IAMPERF_ACCEPT_RISK` | Runtime authorization key 2 when you cannot pass `--iam-accept-risk` (e.g. container entrypoints). Accepted values: `true` or `1`. |

## 3.6 Exit codes

| Code | Meaning | Typical causes |
|---|---|---|
| `0` | Success | `validate` passed; `run` completed its window; `report` rendered. |
| `1` | Runtime error | Authorization gate refused the run; workload error after a report was produced; `report` input unreadable/unparseable. |
| `2` | Invalid configuration | Missing `-c`/`-f`; unreadable config; JSON syntax error; **unknown field**; schema-version mismatch; any validation failure; engine construction failure. |
| `130` | Cancelled by signal | SIGINT/SIGTERM during the abort window, or mid-run graceful cancellation (a complete report is still written with `termination: "cancelled"`). |

Note that a run which is **stopped by a safety threshold** does *not* exit
`130` — it terminates normally, reports `termination: "safety-stop"` plus the
controller's reason in `safety.termination_reason`, and exits per the rules
above (the workload itself did not error; check the report).

## 3.7 Signals and shutdown semantics

| Signal | Effect |
|---|---|
| SIGINT / SIGTERM | Graceful at any moment. New attempts stop immediately; in-flight attempts are given a bounded drain period (default 10 s); a full report is written; the terminal summary prints; exit `130`. |
| Second signal | No special handling is registered — the graceful path is the only path, and the drain grace bounds how long shutdown can take. |

Shutdown latency is therefore bounded: worst case ≈ drain grace (10 s) plus
report writing. There is no force-kill escape hatch because none is needed —
if you need the load gone *right now*, it is already stopping.

## 3.8 Report file naming

Reports are written to `<output_dir>/iamperf-report-<runid[:8]>-<UTC-timestamp>.json`
(and `.csv` when configured), for example:

```
reports/iamperf-report-9f2c1a8b-20260101T120000Z.json
reports/iamperf-report-9f2c1a8b-20260101T120000Z.csv
```

The run id prefix ties the filename to the `run_id` inside the report, so
artifacts can be matched to reports unambiguously even after many runs land in
the same directory. The directory is created (`0755`) if it does not exist.
