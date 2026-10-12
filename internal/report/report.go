// Package report builds the schema-versioned test report (JSON/CSV/terminal).
// Field order is API-stable; schema_version changes only on breaking changes.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"time"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
)

// SchemaVersion is the report schema version.
const SchemaVersion = 1

// Report is the complete, redacted result of one run.
type Report struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	GeneratedAt   time.Time `json:"generated_at"`
	ModuleVersion string    `json:"module_version"`

	Metadata    Metadata               `json:"metadata"`
	Target      Target                 `json:"target"`
	Scenario    Scenario               `json:"scenario"`
	Workload    Workload               `json:"workload"`
	Attempts    Attempts               `json:"attempts"`
	Requests    Requests               `json:"requests"`
	Latency     Latency                `json:"latency"`
	Steps       []metrics.StepSnapshot `json:"steps"`
	Errors      Errors                 `json:"errors"`
	Safety      Safety                 `json:"safety"`
	Traces      Traces                 `json:"traces"`
	Environment Environment            `json:"environment"`
	Limitations []string               `json:"limitations"`
}

// Metadata describes the run window and termination.
type Metadata struct {
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	DurationSec float64   `json:"duration_seconds"`
	WarmupSec   float64   `json:"warmup_seconds"`
	Termination string    `json:"termination"`
	// ConfigSummary is the redacted resolved configuration.
	ConfigSummary json.RawMessage `json:"config_summary"`
}

// Target records the approved target identity (no secrets).
type Target struct {
	BaseURL      string   `json:"base_url"`
	AllowedPaths []string `json:"allowed_paths"`
	Flow         string   `json:"flow"`
}

// Scenario mirrors the executed scenario.
type Scenario struct {
	Name         string  `json:"name"`
	Model        string  `json:"model"`
	VirtualUsers int     `json:"virtual_users"`
	DurationSec  float64 `json:"duration_seconds"`
	ThinkTimeSec float64 `json:"think_time_seconds"`
}

// Workload contrasts configured intent with observed load.
type Workload struct {
	ConfiguredRPS     float64 `json:"configured_rps"`
	ObservedRPS       float64 `json:"observed_rps"` // completed requests / active duration
	AttemptRPS        float64 `json:"attempt_rps"`  // completed login transactions / active duration
	BackpressureDrops int64   `json:"backpressure_drops"`
}

// Attempts is the login-transaction view (the primary success measure).
type Attempts struct {
	Started        int64   `json:"started"`
	Succeeded      int64   `json:"succeeded"`
	Failed         int64   `json:"failed"`
	Aborted        int64   `json:"aborted"`
	WarmupExcluded int64   `json:"warmup_excluded"`
	SuccessRatePct float64 `json:"success_rate_pct"`
	FailureRatePct float64 `json:"failure_rate_pct"`
}

// Requests is the HTTP-activity view (secondary; never equated with logins).
type Requests struct {
	Started    int64            `json:"started"`
	Completed  int64            `json:"completed"`
	Failed     int64            `json:"failed"`
	StatusDist map[string]int64 `json:"status_distribution"`
	Redirects  int64            `json:"redirects"`
	ConnReuse  int64            `json:"connection_reuse"`
	NewConns   int64            `json:"new_connections"`
	Timeouts   int64            `json:"timeouts"`
	ConnErrors int64            `json:"connection_errors"`
}

// Latency holds end-to-end login latency percentiles.
type Latency struct {
	Attempt metrics.LatencyStats `json:"attempt"`
}

// Traces holds transport-level observables ("when observable").
type Traces struct {
	TTFB metrics.LatencyStats `json:"ttfb"`
	DNS  metrics.LatencyStats `json:"dns"`
	TCP  metrics.LatencyStats `json:"tcp"`
	TLS  metrics.LatencyStats `json:"tls"`
}

// Errors lists classes with representative redacted samples.
type Errors struct {
	Classes        map[string]int64  `json:"classes"`
	Representative map[string]string `json:"representative"`
}

// Safety records controller interventions.
type Safety struct {
	TerminationReason string `json:"termination_reason"`
	Tripped           bool   `json:"tripped"`
}

// Environment records build/runtime identity.
type Environment struct {
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	GoVersion string `json:"go_version"`
	NumCPU    int    `json:"num_cpu"`
}

// BuildOptions carries everything needed to render a Report.
type BuildOptions struct {
	RunID         string
	ModuleVersion string
	Cfg           config.Config
	AllowHost     string
	Snapshot      metrics.Snapshot
	StartedAt     time.Time
	EndedAt       time.Time
	Termination   string
	SafetyReason  string
	Limitations   []string
}

// Build assembles the Report. Active duration excludes the warm-up window.
func Build(o BuildOptions) *Report {
	s := o.Snapshot
	active := o.EndedAt.Sub(o.StartedAt).Seconds()
	if s.WarmupAttempts > 0 {
		// warm-up share of the window is excluded from rate denominators
		active -= o.Cfg.Scenario.WarmUp.Duration.Seconds()
		if active <= 0 {
			active = o.EndedAt.Sub(o.StartedAt).Seconds()
		}
	}
	attemptTotal := s.AttemptsSucceeded + s.AttemptsFailed
	successPct, failurePct := 0.0, 0.0
	if attemptTotal > 0 {
		successPct = round2(100 * float64(s.AttemptsSucceeded) / float64(attemptTotal))
		failurePct = round2(100 * float64(s.AttemptsFailed) / float64(attemptTotal))
	}
	cfgSummary, _ := o.Cfg.RedactedJSON()

	r := &Report{
		SchemaVersion: SchemaVersion,
		RunID:         o.RunID,
		GeneratedAt:   o.EndedAt,
		ModuleVersion: o.ModuleVersion,
		Metadata: Metadata{
			StartedAt:     o.StartedAt,
			FinishedAt:    o.EndedAt,
			DurationSec:   round2(o.EndedAt.Sub(o.StartedAt).Seconds()),
			WarmupSec:     o.Cfg.Scenario.WarmUp.Duration.Seconds(),
			Termination:   o.Termination,
			ConfigSummary: json.RawMessage(cfgSummary),
		},
		Target: Target{
			BaseURL:      o.Cfg.Target.BaseURL,
			AllowedPaths: o.Cfg.Target.AllowedPaths,
			Flow:         o.Cfg.Scenario.Flow,
		},
		Scenario: Scenario{
			Name:         o.Cfg.Scenario.Name,
			Model:        o.Cfg.Scenario.Model,
			VirtualUsers: o.Cfg.Scenario.VirtualUsers,
			DurationSec:  o.Cfg.Scenario.Duration.Duration.Seconds(),
			ThinkTimeSec: o.Cfg.Scenario.ThinkTime.Duration.Seconds(),
		},
		Workload: Workload{
			ConfiguredRPS:     o.Cfg.Scenario.RequestRate,
			ObservedRPS:       round2(float64(s.RequestsCompleted) / active),
			AttemptRPS:        round2(float64(attemptTotal) / active),
			BackpressureDrops: s.BackpressureDrops,
		},
		Attempts: Attempts{
			Started:        s.AttemptsStarted,
			Succeeded:      s.AttemptsSucceeded,
			Failed:         s.AttemptsFailed,
			Aborted:        s.AttemptsAborted,
			WarmupExcluded: s.WarmupAttempts,
			SuccessRatePct: successPct,
			FailureRatePct: failurePct,
		},
		Requests: Requests{
			Started:    s.RequestsStarted,
			Completed:  s.RequestsCompleted,
			Failed:     s.RequestsFailed,
			StatusDist: s.StatusDist,
			Redirects:  s.Redirects,
			ConnReuse:  s.ConnReuse,
			NewConns:   s.NewConns,
			Timeouts:   s.Timeouts,
			ConnErrors: s.ConnErrors,
		},
		Latency: Latency{Attempt: s.AttemptLatency},
		Steps:   s.Steps,
		Errors: Errors{
			Classes:        s.ErrorClasses,
			Representative: s.ErrorSamples,
		},
		Safety: Safety{
			TerminationReason: o.SafetyReason,
			Tripped:           o.Termination == "safety-stop",
		},
		Traces: Traces{TTFB: s.TTFB, DNS: s.DNS, TCP: s.TCP, TLS: s.TLS},
		Environment: Environment{
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
			GoVersion: runtime.Version(), NumCPU: runtime.NumCPU(),
		},
		Limitations: o.Limitations,
	}
	return r
}

// WriteJSON emits the canonical machine-readable report.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteCSV emits one row per flow step with latency summary columns.
func (r *Report) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"step", "started", "failed", "p50_ms", "p90_ms", "p95_ms", "p99_ms", "max_ms"}); err != nil {
		return err
	}
	for _, s := range r.Steps {
		row := []string{
			s.Step, strconv.FormatInt(s.Started, 10), strconv.FormatInt(s.Failed, 10),
			f2(s.Latency.P50), f2(s.Latency.P90), f2(s.Latency.P95), f2(s.Latency.P99), f2(s.Latency.Max),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// PrintTerminal renders the human-readable summary.
func (r *Report) PrintTerminal(w io.Writer) {
	fmt.Fprintf(w, "\n===== iam-perf report %s =====\n", r.RunID)
	fmt.Fprintf(w, "target: %s   flow: %s\n", r.Target.BaseURL, r.Target.Flow)
	fmt.Fprintf(w, "window: %.1fs (warmup %.1fs)   termination: %s\n",
		r.Metadata.DurationSec, r.Metadata.WarmupSec, r.Metadata.Termination)
	fmt.Fprintf(w, "\n-- login transactions --\n")
	fmt.Fprintf(w, "succeeded: %d   failed: %d   aborted: %d   success rate: %.1f%%\n",
		r.Attempts.Succeeded, r.Attempts.Failed, r.Attempts.Aborted, r.Attempts.SuccessRatePct)
	fmt.Fprintf(w, "transactions/sec: %.2f   (http req/sec: %.2f)\n",
		r.Workload.AttemptRPS, r.Workload.ObservedRPS)
	a := r.Latency.Attempt
	fmt.Fprintf(w, "\n-- login latency (ms) --\n")
	fmt.Fprintf(w, "p50 %.0f   p90 %.0f   p95 %.0f   p99 %.0f   max %.0f   (n=%d)\n",
		a.P50, a.P90, a.P95, a.P99, a.Max, a.Count)
	fmt.Fprintf(w, "\n-- requests --\n")
	fmt.Fprintf(w, "started: %d   completed: %d   failed: %d   429: %d   5xx: %d   timeouts: %d   conn-errors: %d\n",
		r.Requests.Started, r.Requests.Completed, r.Requests.Failed, countStatus(r.Requests.StatusDist, "429"),
		sum5xx(r.Requests.StatusDist), r.Requests.Timeouts, r.Requests.ConnErrors)
	fmt.Fprintf(w, "conn reuse: %d   new conns: %d   redirects: %d\n",
		r.Requests.ConnReuse, r.Requests.NewConns, r.Requests.Redirects)
	if len(r.Steps) > 0 {
		fmt.Fprintf(w, "\n-- steps (p50/p99 ms) --\n")
		for _, s := range r.Steps {
			fmt.Fprintf(w, "  %-24s n=%-7d %.0f / %.0f\n", s.Step, s.Started, s.Latency.P50, s.Latency.P99)
		}
	}
	if len(r.Errors.Classes) > 0 {
		fmt.Fprintf(w, "\n-- error classes --\n")
		for cls, n := range r.Errors.Classes {
			fmt.Fprintf(w, "  %-12s %d\n", cls, n)
		}
	}
	if r.Safety.Tripped {
		fmt.Fprintf(w, "\nSAFETY STOP: %s\n", r.Safety.TerminationReason)
	}
	fmt.Fprintf(w, "\nlimitations: %d recorded (see JSON report)\n", len(r.Limitations))
	fmt.Fprintf(w, "======================================\n")
}

func countStatus(m map[string]int64, code string) int64 { return m[code] }

func sum5xx(m map[string]int64) int64 {
	var n int64
	for k, v := range m {
		if len(k) == 3 && k[0] == '5' {
			n += v
		}
	}
	return n
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
func f2(v float64) string      { return strconv.FormatFloat(v, 'f', 2, 64) }
