package metrics

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHistogram_Quantiles(t *testing.T) {
	h := NewHistogram()
	// Deterministic distribution: 100 values 1..100 ms.
	for i := 1; i <= 100; i++ {
		h.Record(time.Duration(i) * time.Millisecond)
	}
	if h.Count() != 100 {
		t.Fatalf("count = %d", h.Count())
	}
	// Log-linear buckets bound quantile error to one bucket width.
	check := func(name string, got, wantLow, wantHigh float64) {
		t.Helper()
		if got < wantLow || got > wantHigh {
			t.Errorf("%s = %.2f ms, want within [%.2f, %.2f]", name, got, wantLow, wantHigh)
		}
	}
	check("p50", h.Quantile(0.50), 46, 56)
	check("p90", h.Quantile(0.90), 86, 96)
	check("p99", h.Quantile(0.99), 96, 101)
	check("max", h.Max(), 99.5, 100.5)
	check("min", h.Min(), 0, 1.5)
	check("mean", h.Mean(), 49.5, 51.5)
}

func TestHistogram_Merge(t *testing.T) {
	a, b := NewHistogram(), NewHistogram()
	for i := 1; i <= 50; i++ {
		a.Record(time.Duration(i) * time.Millisecond)
		b.Record(time.Duration(100+i) * time.Millisecond)
	}
	m := NewHistogram()
	m.Merge(a)
	m.Merge(b)
	if m.Count() != 100 {
		t.Fatalf("merged count = %d", m.Count())
	}
	if m.Max() < 149 {
		t.Errorf("merged max = %.2f", m.Max())
	}
}

func TestHistogram_Concurrent(t *testing.T) {
	h := NewHistogram()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				h.Record(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	if h.Count() != 8000 {
		t.Fatalf("concurrent count = %d", h.Count())
	}
}

func TestCollector_AttemptAndStepAccounting(t *testing.T) {
	c := NewCollector()
	c.RecordStep(StepResult{Step: "initiate-login", Status: 200, Duration: time.Millisecond})
	// Error-class constants are auth-package values; use literals here to
	// keep metrics free of an auth dependency.
	c.RecordStep(StepResult{Step: "verify-password", Status: 401, ErrClass: "auth", ErrMsg: "http 401", Duration: time.Millisecond})
	c.RecordStep(StepResult{Step: "login", Status: 0, ErrClass: "timeout", ErrMsg: "timeout", Duration: 5 * time.Millisecond})

	c.RecordAttempt(AttemptResult{Success: true, Duration: 10 * time.Millisecond})
	c.RecordAttempt(AttemptResult{Class: "auth", ErrMsg: "http 401", Duration: 8 * time.Millisecond})
	c.RecordAttempt(AttemptResult{Aborted: true, Class: "cancelled", Duration: time.Millisecond})
	c.RecordAttempt(AttemptResult{Success: true, Duration: 10 * time.Millisecond, Warmup: true})

	s := c.Snapshot()
	if s.RequestsStarted != 3 || s.RequestsFailed != 2 || s.RequestsCompleted != 1 {
		t.Errorf("requests: started=%d completed=%d failed=%d", s.RequestsStarted, s.RequestsCompleted, s.RequestsFailed)
	}
	if s.Status429 != 0 || s.Status5xx != 0 {
		t.Errorf("429/5xx miscounted")
	}
	if s.AttemptsStarted != 4 || s.AttemptsSucceeded != 2 || s.AttemptsFailed != 1 || s.AttemptsAborted != 1 {
		t.Errorf("attempts: %+v", s)
	}
	if s.WarmupAttempts != 1 {
		t.Errorf("warmup = %d", s.WarmupAttempts)
	}
	// Warm-up AND aborted attempts must be excluded from latency stats
	// (aborted flows have no meaningful end-to-end duration).
	if s.AttemptLatency.Count != 2 {
		t.Errorf("attempt latency count = %d, want 2 (warmup+aborted excluded)", s.AttemptLatency.Count)
	}
	if s.ErrorClasses["timeout"] != 1 || s.ErrorClasses["auth"] != 2 {
		t.Errorf("error classes = %v", s.ErrorClasses)
	}
	if s.ErrorSamples["timeout"] != "timeout" {
		t.Errorf("representative sample = %q", s.ErrorSamples["timeout"])
	}
	// Steps sorted by name.
	if len(s.Steps) != 3 || s.Steps[0].Step != "initiate-login" {
		t.Errorf("steps = %+v", s.Steps)
	}
}

func TestCollector_StatusDistribution(t *testing.T) {
	c := NewCollector()
	for i := 0; i < 3; i++ {
		c.RecordStep(StepResult{Step: "s", Status: 200, Duration: time.Millisecond})
	}
	c.RecordStep(StepResult{Step: "s", Status: 429, Duration: time.Millisecond})
	c.RecordStep(StepResult{Step: "s", Status: 503, Duration: time.Millisecond})
	s := c.Snapshot()
	if s.StatusDist["200"] != 3 || s.StatusDist["429"] != 1 || s.StatusDist["503"] != 1 {
		t.Errorf("dist = %v", s.StatusDist)
	}
	if s.Status429 != 1 || s.Status5xx != 1 {
		t.Errorf("429=%d 5xx=%d", s.Status429, s.Status5xx)
	}
}

func TestWritePrometheus(t *testing.T) {
	c := NewCollector()
	c.RecordStep(StepResult{Step: "login", Status: 200, Duration: 2 * time.Millisecond})
	c.RecordAttempt(AttemptResult{Success: true, Duration: 5 * time.Millisecond})
	var sb strings.Builder
	if err := WritePrometheus(&sb, c.Snapshot()); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		"iamperf_attempts_succeeded 1",
		"iamperf_requests_started 1",
		`iamperf_status_total{code="200"} 1`,
		"iamperf_login_duration_seconds",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prometheus output missing %q", want)
		}
	}
}
