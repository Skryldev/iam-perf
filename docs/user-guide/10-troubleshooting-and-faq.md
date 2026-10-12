# 10. Troubleshooting and FAQ

## 10.1 Triage by exit code

Start here. The exit code narrows the world before you read a single log line:

| Exit | Stage | Meaning | First actions |
|---|---|---|---|
| `0` | any | Success (`validate` passed / `run` completed its window / `report` rendered) | Read the report; check `metadata.termination` is what you intended. |
| `2` | config/engine | Configuration problem — before or during engine construction, zero traffic (or none beyond the abort window) | The message names the field. Fix the config; re-run `validate`. |
| `1` | gate/workload | Authorization refused the run, or a workload error after a report was produced | Read the message: authorization refusal prints the exact two keys it needs; otherwise open the report JSON and check `errors.classes`. |
| `130` | signals | You (or an orchestrator) cancelled the run | Expected behavior — a complete report was written with `termination: "cancelled"`. Not an error unless nobody cancelled. |

Remember: a **safety-stop** run is not any of the above failures — it
terminates the window early, reports `termination: "safety-stop"` plus
`safety.termination_reason`, and exits per the rules above.

## 10.2 Error message → cause → fix

The table below covers every message the tool is likely to hand you, with the
remediation that actually works. Messages are quoted as printed (the
variable parts are `<…>`).

### Configuration (exit 2)

| Message | Cause | Fix |
|---|---|---|
| `validate: -c <config.json> is required` | Flag omitted | Pass `-c path/to/config.json`. |
| `validate: cannot read config file "<path>": …` | Missing/unreadable file | Check path and permissions (remember: container mounts change paths). |
| `invalid configuration: parsing "<path>": json: unknown field "virtul_users"` | Typo — unknown fields are rejected, never ignored | Correct the field name against [04-configuration.md](04-configuration.md). |
| `invalid configuration: schema_version 2 not supported (want 1)` | Config written for a different schema | Set `"schema_version": 1`. |
| `invalid configuration: target.base_url … is not a valid URL` / `missing a host` | Malformed base URL | Use `scheme://host[:port]`, e.g. `https://iam.example.ir`. |
| `invalid configuration: target.allowed_paths must not be empty` / `entry "x" must start with '/'` | Scope not usable | Provide at least one absolute path prefix, e.g. `["/login", "/api/v1/"]`. |
| `invalid configuration: scenario.flow must be "mohaymen-login" or "transport-probe"` | Unknown flow | Pick one of the two; note `transport-probe` needs no credentials. |
| `invalid configuration: scenario.model must be "closed" or "open"` | Unknown model | `closed` or `open`. |
| `invalid configuration: scenario.virtual_users must be >= 1 for closed model` | VU count too low | At least 1. |
| `invalid configuration: scenario.request_rate must be > 0 for open model` / `scenario.max_in_flight must be >= 1` | Open-model preconditions | Set both; `max_in_flight` also bounds memory. |
| `invalid configuration: credentials.username is required for flow "mohaymen-login"` | Login flow without a user | Set `credentials.username` (or `IAMPERF_CREDENTIALS_USERNAME`). |
| `invalid configuration: credentials.password_env or credentials.password_file must be set` / `set either … not both` | Credential source rules | Exactly one source; default env name is `IAMPERF_TEST_PASSWORD`. |
| `invalid configuration: scenario.virtual_users (N) exceeds safety.max_virtual_users (M)` (and the `request_rate`/`duration` variants) | Workload exceeds the approved budget | Lower the workload or consciously raise the budget in the config — budgets are part of your authorization record. |
| `invalid configuration: transport.trusted_cas_file: …` | CA bundle missing or contains no valid PEM | Fix the path; export a PEM bundle (certificates, not keys). |
| `invalid configuration: metrics.prometheus_addr "x" must be host:port` | Bad bind address | e.g. `127.0.0.1:9537`. |
| `invalid configuration: log.level …` / `log.format …` / `report.formats entry "x" …` | Enum fields | Allowed: `debug|info|warn|error`; `text|json`; `json|csv|terminal`. |

### Authorization and credentials (exit 1)

| Message | Cause | Fix |
|---|---|---|
| `run not authorized: set authorization.accepted=true in config AND pass --iam-accept-risk (or env IAMPERF_ACCEPT_RISK=true)` (optionally suffixed `(config authorization.accepted=false)`) | One or both authorization keys missing | Key 1 in the config; key 2 at runtime. Both, every time. |
| `iamperf: credentials not available (set credentials.password_env target or credentials.password_file with mode 0600)` + `environment variable X is empty or unset` | Password source empty | `export IAMPERF_TEST_PASSWORD='…'` (or the var you named). Note this check fires *after* the gate — a missing password never causes traffic. |
| `…: "<path>" must have mode 0600 (found 644)` | Password file too open | `chmod 600 <path>` — the tool refuses to read secrets from world-readable files. |

### Allowlist and transport (surfaced as failures in the run / report)

| Message (in errors/classes) | Cause | Fix |
|---|---|---|
| `host not in approved allowlist: <scheme>://<host> is not the approved target` | Request aimed at another host (often a redirect, or base_url mismatch) | Correct `target.base_url`; if a redirect escapes your prefixes, stop — that host is not approved. |
| `path not in approved scope: "<path>" is outside approved prefixes […]` | Request path outside `allowed_paths` | Add the prefix only if it is genuinely in your approved scope. |
| `transport: too many redirects: limit N` | Redirect loop or chain past `max_redirects` | Usually a target behavior change; investigate before raising the limit. |
| `transport: response body exceeds max_response_body_bytes` | Response larger than the bounded read cap | Raise the cap consciously; a login API should not return megabytes. |
| `timeout` class (e.g. `login: timeout`) | Request exceeded `request_timeout` (or stage timeouts) | Distinguish slow-target from dead-target via `traces.*` and the target's own monitoring. |
| `transport` class with dial errors | Network/DNS/TLS path problems | Check egress, DNS, proxy settings, and that TLS verification can succeed (add the lab CA via `trusted_cas_file` — verification itself is never disabled). |

### Protocol (evidence-contract) failures

| Message | Cause | Fix |
|---|---|---|
| `initiate-login: missing pre-auth-token response header (evidence step 9)` | Target did not issue the token the captured protocol guarantees | Target changed, wrong endpoint, or a gateway stripped the header. Do not raise load until understood. |
| `login: missing x-auth-token response header (evidence step 12)` | Session was not established | Same triage; also verify the test account is valid. |
| `login: success criteria not met (null userId)` | Chain completed but no session identity — counted as `auth` failure | Usually wrong credentials or account lockout; verify with a single manual login. |
| `…: http 4xx/5xx` (class `auth`) | Server rejected the step | See 10.3 — the exact failure envelope was never captured, so classification is deliberately conservative. |

### Runtime and environment

| Message | Cause | Fix |
|---|---|---|
| `prometheus endpoint disabled: <err>` | Bind address unavailable | Free the port or change `metrics.prometheus_addr`; the run continues without it. |
| `drain grace elapsed; proceeding with report` (warn) | Cancellation while attempts were in flight past the 10 s grace | Report reflects the drained snapshot; acceptable under teardown, investigate if routine. |
| `report: cannot create output dir: …` | `output_dir` not creatable (permissions, read-only fs) | Fix permissions or mount; in containers ensure the volume is writable by uid 65532. |
| `report: unparseable report: …` (from `report -f`) | File is not a schema-v1 JSON report | Pass the `.json` produced by a run. |
| Exit `2` immediately on `run` with a config `validate` accepts | Engine construction failure after the abort window | Re-read the message; check disk space for reports and that no unknown env override mangled the config (run `validate` again to inspect the resolved output). |

## 10.3 Why failure responses are classified "conservatively"

The captured evidence contains no failed logins, so the tool never learned
what a rejection envelope looks like. Rather than guess, every non-2xx,
missing token, or null `userId` is classified as `auth` with a message that
names the step and the observable fact (`login: http 401`), and **response
bodies are never logged** (they might contain tokens or user data). Practical
consequence: when you see `auth` failures, confirm with one manual login
whether the account/password is the cause before assuming a target problem —
and treat any *new* response shape as a protocol change to investigate, not a
tool bug to work around.

## 10.4 FAQ

**Can I test `https://iam.mohaymen.ir` with this tool?**
Only with documented authorization from the system's owner. The tool cannot
distinguish "authorized SRE" from "attacker" — that boundary is yours to hold.
Technically, set `target.base_url`, provide both authorization keys, and use
the test account you were given. Operationally, follow §6.6 of the runbook
first.

**Why is `validate` offline and `run` gated twice?**
Because the most dangerous bugs are the ones that act before you notice.
Offline validation means you can iterate on configs anywhere with zero risk;
the two-key gate means no single accident — a committed config, a stray flag,
a stale script — can generate load by itself.

**Can I disable TLS certificate verification for my lab?**
No — there is no such option anywhere in the code, and that is a documented
design decision. Add your lab CA instead: `transport.trusted_cas_file` with a
PEM bundle. Verification stays on; trust is *added*, never skipped.

**Why did my run exit 130 "by itself"?**
An orchestrator, systemd, a CI timeout, or a colleague sent SIGTERM/SIGINT. A
complete report was written with `termination: "cancelled"` — find it in
`report.output_dir`. The exit code 130 is the conventional "terminated by
signal" value.

**Can I run multiple generators and merge reports?**
Run multiple processes (per host), yes — each is independent and produces its
own report. There is deliberately no built-in cross-generator coordination or
report merging; aggregate JSON/CSV offline with your own tooling. Keep each
process's `max_total_requests` so the *sum* stays within your approved budget.

**Where does the password end up?**
In memory during the run, nowhere else: not in flags (never a flag!), not in
config files, not in logs (redacting handler), not in metric labels (bounded
set), not in reports (error samples are pre-redacted). The redacted config
print shows the *name* of the env var, never the value.

**How do I know the generator wasn't the bottleneck?**
Three checks in order: `environment.num_cpu` matches what you provisioned;
`backpressure_drops` = 0 (open model); `observed_rps` ≈ `configured_rps`
(open) or per-VU attempt rate ≈ 1/(attempt+think) (closed). Benchmarks put
generator overhead at ≈ 0.17 ms CPU per full login attempt — modest, but a
1-vCPU box has a hard ceiling regardless of the target.

**Does the tool support HTTP/2?**
Yes — `ForceAttemptHTTP2` is enabled on all transports; over TLS the
negotiation is automatic, and the report's connection counters tell you how
connections behaved. There is no HTTP/3.

**Why JSON config only (no YAML/TOML)?**
One format, parsed by the standard library with unknown-field rejection, is
one less dependency and one less class of surprise. The schema is small
enough that JSON's verbosity costs nothing in practice.

**Something failed that isn't in this table.**
Reproduce with `log.level: debug` (redacted, per-attempt messages appear),
capture `iam-perf version`, the report JSON, and the exact config (its
redacted form is embedded in `metadata.config_summary`), and file an issue
with those three artifacts — that is the minimum set that makes a bug
reproducible for the maintainer.
