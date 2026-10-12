package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
)

func sampleSnapshot() metrics.Snapshot {
	s := metrics.Snapshot{
		AttemptsStarted:   10,
		AttemptsSucceeded: 8,
		AttemptsFailed:    2,
		AttemptsAborted:   1,
		WarmupAttempts:    2,
		RequestsStarted:   50,
		RequestsCompleted: 48,
		RequestsFailed:    2,
		Status429:         1,
		Redirects:         3,
		ConnReuse:         30,
		NewConns:          20,
		StatusDist:        map[string]int64{"200": 46, "401": 2, "429": 1, "500": 1},
		ErrorClasses:      map[string]int64{"auth": 2},
		ErrorSamples:      map[string]string{"auth": "verify-password: http 401"},
	}
	s.AttemptLatency = metrics.LatencyStats{Count: 8, Mean: 100, P50: 90, P90: 150, P95: 180, P99: 200, Min: 50, Max: 220}
	s.Steps = []metrics.StepSnapshot{
		{Step: "login", Started: 10, Failed: 0, Statuses: map[string]int64{"200": 10},
			Latency: metrics.LatencyStats{Count: 10, P50: 20, P99: 40, Max: 45}},
	}
	return s
}

func TestBuild_SchemaAndValues(t *testing.T) {
	cfg := config.Default()
	cfg.Credentials.Username = "SRETEST"
	start := time.Now().Add(-time.Minute)
	end := time.Now()
	rep := Build(BuildOptions{
		RunID:         "test-run-id",
		ModuleVersion: "0.1.0",
		Cfg:           cfg,
		Snapshot:      sampleSnapshot(),
		StartedAt:     start,
		EndedAt:       end,
		Termination:   "completed",
		Limitations:   []string{"limitation-1"},
	})
	if rep.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d", rep.SchemaVersion)
	}
	if rep.Attempts.SuccessRatePct != 80 {
		t.Errorf("success rate = %.1f, want 80", rep.Attempts.SuccessRatePct)
	}
	if rep.Attempts.FailureRatePct != 20 {
		t.Errorf("failure rate = %.1f, want 20", rep.Attempts.FailureRatePct)
	}
	// Redaction: report must not contain the username.
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "SRETEST") {
		t.Fatal("report leaks username")
	}
}

func TestWriteJSON_FieldOrderAndSchemaVersionFirst(t *testing.T) {
	cfg := config.Default()
	rep := Build(BuildOptions{
		RunID: "rid", ModuleVersion: "0.1.0", Cfg: cfg,
		Snapshot: sampleSnapshot(), StartedAt: time.Now(), EndedAt: time.Now(),
		Termination: "completed",
	})
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	idx := strings.Index(out, `"schema_version"`)
	if idx == -1 || idx > 10 {
		t.Errorf("schema_version must be the first field, output starts: %.60s", out)
	}
	var back Report
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("round-trip failed: %s", err)
	}
	if back.RunID != "rid" || back.Metadata.Termination != "completed" {
		t.Errorf("round-trip mismatch: %+v", back.Metadata)
	}
}

func TestWriteCSV(t *testing.T) {
	cfg := config.Default()
	rep := Build(BuildOptions{RunID: "rid", Cfg: cfg, Snapshot: sampleSnapshot(),
		StartedAt: time.Now(), EndedAt: time.Now(), Termination: "completed"})
	var buf bytes.Buffer
	if err := rep.WriteCSV(&buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "step,") {
		t.Fatalf("csv = %q", lines)
	}
	if !strings.HasPrefix(lines[1], "login,10,0,") {
		t.Fatalf("csv row = %q", lines[1])
	}
}

func TestPrintTerminal(t *testing.T) {
	cfg := config.Default()
	rep := Build(BuildOptions{RunID: "rid", Cfg: cfg, Snapshot: sampleSnapshot(),
		StartedAt: time.Now(), EndedAt: time.Now(), Termination: "safety-stop",
		SafetyReason: "safety: HTTP 429 count 25 reached limit 20"})
	var buf bytes.Buffer
	rep.PrintTerminal(&buf)
	out := buf.String()
	for _, want := range []string{"login transactions", "success rate: 80.0%", "SAFETY STOP", "p50 90"} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output missing %q", want)
		}
	}
}

func TestBuild_WarmupExcludedFromRates(t *testing.T) {
	cfg := config.Default()
	cfg.Scenario.WarmUp = config.Duration{Duration: 10 * time.Second}
	rep := Build(BuildOptions{RunID: "rid", Cfg: cfg, Snapshot: sampleSnapshot(),
		StartedAt: time.Now().Add(-30 * time.Second), EndedAt: time.Now(), Termination: "completed"})
	// Active window = 30 - 10 = 20s; requests completed 48 → 2.4 rps
	if rep.Workload.ObservedRPS != 2.4 {
		t.Errorf("observed rps = %.2f, want 2.4", rep.Workload.ObservedRPS)
	}
}
