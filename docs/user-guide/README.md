# iam-perf User Guide

This is the complete **"How to use"** manual for `iam-perf`, the safety-gated IAM
login performance testing framework. It is written for the person who will
actually operate the tool: an SRE, performance engineer, or QA engineer who has
been authorized to load-test an IAM login endpoint and needs to do so safely,
correctly, and with trustworthy results.

Every command, flag, configuration key, exit code, and report field documented
here reflects the implemented behavior of the tool (version 0.1.0, report and
configuration schema v1). Nothing in this guide describes aspirational or
unimplemented features.

## What this tool is (and is not)

`iam-perf` drives a controlled volume of **login transactions** against an IAM
service and measures how it behaves. One login transaction is the full
evidence-derived protocol chain — `initiate-login` → `verifyPassword` →
(`authentication-status`) → `login` → (`dashboardPermissions` session proof) —
executed by isolated virtual users, each with its own HTTP client, cookie jar,
and token state.

It is **not** a credential-testing tool. It performs one login per attempt with
the single credential you supply, never retries authentication POSTs, and
treats any `429` from the server as an instruction to stop. Brute force,
password spraying, MFA bypass, rate-limit evasion, and account enumeration are
impossible by construction. Load generation only starts after **two
independent authorization keys** are provided, and every request URL is checked
against a host/path allowlist — including every redirect hop.

## Reading map

Read the files in order for a complete education, or jump straight to what you
need:

| # | Document | What it covers | Read when |
|---|----------|----------------|-----------|
| 1 | [01-installation.md](01-installation.md) | Prerequisites, building from source, Makefile targets, verifying the build | First contact |
| 2 | [02-quickstart.md](02-quickstart.md) | A complete first session in ~10 minutes: validate, probe a localhost stub, run a real (authorized) login test, read the report | First contact |
| 3 | [03-cli-reference.md](03-cli-reference.md) | Every command, flag, environment variable, exit code, and signal behavior | Daily reference |
| 4 | [04-configuration.md](04-configuration.md) | Full configuration schema v1: every field, default, constraint, and override rule | Setting up a scenario |
| 5 | [05-load-models.md](05-load-models.md) | Closed vs open models, ramp-up/down, warm-up, think time, backpressure, sizing guidance | Designing a workload |
| 6 | [06-safety-and-operations.md](06-safety-and-operations.md) | The two-key approval gate, allowlist, budgets, stop conditions, cancellation, and the operator runbook (before / during / after a run) | Before every real run |
| 7 | [07-metrics-and-reports.md](07-metrics-and-reports.md) | Attempt vs request metrics, percentile methodology, per-step data, report schema tour, CSV, terminal output, Prometheus, interpretation guidance | After every run |
| 8 | [08-go-api.md](08-go-api.md) | Embedding `pkg/iamperf` in your own Go programs: Engine, Options, sentinel errors, deterministic testing | Integrating |
| 9 | [09-docker.md](09-docker.md) | Building and running the container image, hardened compose setup, signals in containers, resource limits | Containerized execution |
| 10 | [10-troubleshooting-and-faq.md](10-troubleshooting-and-faq.md) | Exit-code triage, every common error message with its fix, frequently asked questions | When something breaks |

## The 60-second version

```bash
# Build (Go 1.27+)
make build && ./bin/iam-perf version

# Validate a configuration — performs ZERO network activity
./bin/iam-perf validate -c configs/example-probe.json

# A real, authorized run — requires BOTH authorization keys:
#   key 1: "authorization": { "accepted": true } in the config file
#   key 2: --iam-accept-risk flag (or IAMPERF_ACCEPT_RISK=true in the environment)
export IAMPERF_TEST_PASSWORD='the-test-account-password'
./bin/iam-perf run -c configs/example-closed.json --iam-accept-risk

# Re-render a stored report later
./bin/iam-perf report -f reports/iamperf-report-1a2b3c4d-20260101T120000Z.json
```

Exit codes: `0` success · `1` runtime error (including refused authorization) ·
`2` invalid configuration · `130` cancelled by signal (a report is still
written).

## Conventions used in this guide

- Shell examples assume the repository root as the working directory and the
  built binary at `./bin/iam-perf`. Substitute `iam-perf` if the binary is on
  your `PATH`.
- Configuration values are shown in JSON, the only accepted config format
  (unknown fields are rejected at load time, which catches typos early).
- Durations accept Go syntax (`"300ms"`, `"2m"`, `"1h30m"`) or a bare integer
  meaning seconds (`30` ≡ `"30s"`).
- Placeholders are written in `CAPS` (e.g. `IAMPERF_TEST_PASSWORD`). Real
  secrets must never be committed to config files, scripts, or this
  repository — the tool reads the password from the environment or a
  `0600`-mode file at run time.

## Where the deeper material lives

This guide covers operation. Complementary documents in the repository go
deeper on specific subjects:

- `docs/ARCHITECTURE.md` — package boundaries and the ten architecture
  decision records (ADR-1 … ADR-10) explaining *why* the tool is built the way
  it is.
- `docs/AUTH_FLOW.md` — the evidence-derived protocol specification with full
  traceability to the captured HTTP Toolkit exchanges, plus the list of
  behaviors deliberately **not** implemented.
- `docs/SAFETY.md` — the safety control catalogue and the pre-existing
  operational runbook.
- `docs/CONFIGURATION.md` and `docs/REPORT_SCHEMA.md` — condensed field
  references for configuration and reports.
- `docs/phase-reports/` — the per-phase engineering reports produced during
  the build, including what was verified and what was not.
- `docs/examples/` — real JSON reports generated from local fixture runs.

If this guide and any other document ever disagree, the implemented behavior
(`cmd/iam-perf`, `pkg/iamperf`, `internal/*`) is the source of truth — and an
issue should be filed against the docs.
