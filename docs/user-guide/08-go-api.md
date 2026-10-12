# 8. Embedding the Go API (`pkg/iamperf`)

The CLI is a thin shell over a reusable library. `pkg/iamperf` is the stable
public API: everything else (`internal/*`) is unimportable by design, so the
types listed here are the entire contract, guarded by semantic versioning
(MAJOR = API or report-schema break, MINOR = additive, PATCH = fixes). This
chapter shows how to run workloads from your own Go programs and how to test
them deterministically.

## 8.1 Module coordinates

The module path is currently the placeholder `example.com/iam-perf`. To import
it from another module today, use a `replace` directive:

```
// go.mod of your program
require example.com/iam-perf v0.0.0

replace example.com/iam-perf => ../iam-perf   // path to your checkout
```

Before publishing to a real repository, change the module path in `go.mod`
(and in the Makefile/Dockerfile `-ldflags -X` lines), then tag
`vMAJOR.MINOR.PATCH`. The `VERSION` file is the single source of truth for the
release version; `iamperf.Version()` returns what was linked in.

## 8.2 The 30-second tour

```go
package main

import (
    "context"
    "fmt"
    "os"
    "time"

    "example.com/iam-perf/pkg/iamperf"
)

func main() {
    cfg := iamperf.DefaultConfig()          // safe, conservative defaults
    cfg.Target.BaseURL = "https://iam-lab.example.ir"
    cfg.Target.AllowedPaths = []string{"/login", "/api/v1/"}
    cfg.Scenario.VirtualUsers = 5
    cfg.Scenario.Duration = iamperf.Duration{2 * time.Minute}
    cfg.Authorization.Accepted = true       // authorization key 1 (config)

    eng, err := iamperf.NewEngine(cfg,
        iamperf.WithRunConfirmation(true),  // authorization key 2 (runtime)
        iamperf.WithEnvLookup(os.LookupEnv),
    )
    if err != nil {
        panic(err)                          // validation error — actionable text
    }

    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
    defer cancel()

    rep, err := eng.Run(ctx)                // context-aware, cancellable
    if err != nil {
        panic(err)
    }
    fmt.Printf("success %.1f%%  p99 %.0fms  termination %s\n",
        rep.Attempts.SuccessRatePct, rep.Latency.Attempt.P99,
        rep.Metadata.Termination)
}
```

`NewEngine` validates the configuration and builds nothing else — **no network
I/O happens there**. All network activity is inside `Run`, after the
authorization gate. `Engine` is safe to reuse for sequential runs; each `Run`
is fully independent (fresh collector, fresh run id, fresh allowlist).

## 8.3 Core types

| Type | Role |
|---|---|
| `Config` | The engine configuration — a type alias to the internal config struct, so its JSON tags, defaults (`iamperf.DefaultConfig()`), and `Validate()` behavior are exactly what [04-configuration.md](04-configuration.md) documents. `Duration` wraps `time.Duration` with the JSON string/integer parsing described there. |
| `Report` | The run result — alias to the internal report struct; the schema-v1 fields of [07-metrics-and-reports.md](07-metrics-and-reports.md), including `WriteJSON(io.Writer)`, `WriteCSV(io.Writer)`, and `PrintTerminal(io.Writer)` helpers. |
| `Engine` | Executes configured workloads. Construct with `NewEngine(cfg, opts...)`. |

## 8.4 Options

| Option | Purpose |
|---|---|
| `WithLogger(*slog.Logger)` | Injects a structured logger (default: a redacting handler to stderr at the configured level). Supply your own to route logs into your stack — the redaction guarantees hold only for the provided default; if you swap loggers, your handler owns redaction. |
| `WithClock(func() time.Time)` | Overrides time for deterministic tests (deadlines, warm-up windows, durations). |
| `WithRunConfirmation(bool)` | Authorization **key 2** at the API level — the equivalent of the CLI's `--iam-accept-risk`. |
| `WithEnvLookup(func(string) (string, bool))` | Overrides environment access — used both for `IAMPERF_ACCEPT_RISK` inside the gate and for credential resolution; inject a map-backed function in tests. |
| `WithCollectorHook(func(*metrics.Collector))` | Receives the live metrics collector when a run starts. Intended for **in-module** integrations — the CLI uses it to serve Prometheus. Note the parameter type lives in an internal package, so external modules cannot name it; external consumers should use the CLI's Prometheus endpoint instead, or propose an API extension. |

## 8.5 Sentinel errors

All errors from `NewEngine` / `Run` are comparable with `errors.Is`:

```go
var (
    ErrValidation        // config invalid (message names the field)
    ErrNotAuthorized     // two-key gate refused: config key, runtime key, or both missing
    ErrTargetNotAllowed  // host not in approved allowlist
    ErrPathNotAllowed    // path outside approved prefixes
    ErrCredentialsNeeded // no usable password: env unset, or file missing / not 0600
)
```

Typical handling:

```go
rep, err := eng.Run(ctx)
switch {
case err == nil:
    // use rep
case errors.Is(err, iamperf.ErrNotAuthorized):
    log.Fatal("authorization refused: set authorization.accepted AND provide the runtime key")
case errors.Is(err, iamperf.ErrCredentialsNeeded):
    log.Fatal("credentials: export IAMPERF_TEST_PASSWORD or fix password_file mode to 0600")
default:
    log.Fatalf("run failed: %v", err)
}
```

A returned `*Report` with a non-nil error is still meaningful (the CLI relies
on this): termination/cancellation produce a report *and* an error, and the
report carries the full snapshot — check `rep.Metadata.Termination`.

## 8.6 Credentials at the API level

Resolution is identical to the CLI and happens **inside `Run`, after the
authorization gate**: `credentials.password_file` must exist with mode exactly
`0600` (trailing newline trimmed), otherwise the variable named by
`credentials.password_env` (default `IAMPERF_TEST_PASSWORD`) is read through
the injected env lookup. Failures return `ErrCredentialsNeeded` wrapped with
the exact remediation. The password value is held in memory only.

## 8.7 Deterministic testing pattern

Because clock and environment are injectable, integration tests over the real
engine are fast and flake-free. Sketch (see `pkg/iamperf/api_test.go` for the
complete working versions):

```go
func TestEngine_ProbeFlow(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(200) // probe flow: any 2xx is a completed probe
    }))
    defer srv.Close()

    cfg := iamperf.DefaultConfig()
    cfg.Target.BaseURL = srv.URL
    cfg.Target.AllowedPaths = []string{"/login"}
    cfg.Scenario.Flow = "transport-probe"
    cfg.Scenario.Model = "closed"
    cfg.Scenario.VirtualUsers = 2
    cfg.Scenario.Duration = iamperf.Duration{200 * time.Millisecond}
    cfg.Authorization.Accepted = true

    eng, err := iamperf.NewEngine(cfg, iamperf.WithRunConfirmation(true))
    if err != nil {
        t.Fatal(err)
    }
    rep, err := eng.Run(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if rep.Attempts.Succeeded == 0 {
        t.Fatalf("expected completed probes, got %+v", rep.Attempts)
    }
}
```

The same pattern with a fake `WithClock` lets you assert warm-up exclusion and
deadline behavior without sleeping. For a faithful protocol fixture (token
correlation, failure injection, per-request tracking), reuse the in-repo
`internal/testkit` package from a test inside this module.

## 8.8 Guarantees you can rely on

When you embed the engine, these properties of the CLI hold unchanged, because
they live below the API surface: the two-key authorization gate; the immutable
allowlist checked on initial URLs and every redirect hop; pre-flight budget
validation; sticky stop conditions polled every 250 ms; context cancellation
honored immediately (new attempts stop, in-flight drain ≤ 10 s); no retry of
authentication POSTs; TLS verification that cannot be disabled; bounded
response-body reads; and redaction of token-shaped strings in default-log
attributes and error samples. If your wrapper needs to *weaken* any of these,
the correct answer is that it cannot — that is the library's contract, and
the reason it is safe to let other teams import it.
