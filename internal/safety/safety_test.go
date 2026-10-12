package safety

import (
	"errors"
	"testing"
	"time"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
)

func allow() *Allowlist {
	a, err := NewAllowlist(config.Target{
		BaseURL:      "https://iam.mohaymen.ir",
		AllowedPaths: []string{"/login", "/api/v1/"},
	})
	if err != nil {
		panic(err)
	}
	return a
}

func TestAllowlist(t *testing.T) {
	a := allow()
	ok := []string{
		"https://iam.mohaymen.ir/login",
		"https://iam.mohaymen.ir/api/v1/auth/login",
		"https://iam.mohaymen.ir/api/v1/branding",
	}
	for _, u := range ok {
		if err := a.Check(u); err != nil {
			t.Errorf("Check(%q) = %v, want nil", u, err)
		}
	}
	bad := []struct{ url, errWant string }{
		{"https://evil.example.ir/login", "host not in approved allowlist"},
		{"http://iam.mohaymen.ir/login", "host not in approved allowlist"},
		{"https://iam.mohaymen.ir.evil.com/login", "host not in approved allowlist"},
		{"https://iam.mohaymen.ir/admin/secret", "path not in approved scope"},
		{"https://iam.mohaymen.ir:8443/login", "host not in approved allowlist"},
		{"https://iam.mohaymen.ir/", "path not in approved scope"},
	}
	for _, c := range bad {
		err := a.Check(c.url)
		if err == nil {
			t.Errorf("Check(%q) = nil, want error", c.url)
			continue
		}
		if !(errors.Is(err, ErrHostNotAllowed) || errors.Is(err, ErrPathNotAllowed)) {
			t.Errorf("Check(%q) error = %v, want sentinel", c.url, err)
		}
	}
}

func TestRequireApproval_TwoKeys(t *testing.T) {
	env := func(k string) (string, bool) { return "", false }
	noop := config.Authorization{}

	if err := RequireApproval(noop, false, env); !errors.Is(err, ErrNotAuthorized) {
		t.Error("config=false flag=false must be rejected")
	}
	if err := RequireApproval(noop, true, env); !errors.Is(err, ErrNotAuthorized) {
		t.Error("config=false must be rejected even with flag")
	}
	if err := RequireApproval(config.Authorization{Accepted: true}, false, env); !errors.Is(err, ErrNotAuthorized) {
		t.Error("config=true flag=false (no env) must be rejected")
	}
	if err := RequireApproval(config.Authorization{Accepted: true}, true, env); err != nil {
		t.Errorf("both keys must pass: %v", err)
	}
	envOK := func(k string) (string, bool) { return "true", k == "IAMPERF_ACCEPT_RISK" }
	if err := RequireApproval(config.Authorization{Accepted: true}, false, envOK); err != nil {
		t.Errorf("env confirmation must pass: %v", err)
	}
	envNo := func(k string) (string, bool) { return "false", true }
	if err := RequireApproval(config.Authorization{Accepted: true}, false, envNo); !errors.Is(err, ErrNotAuthorized) {
		t.Error("env=false must be rejected")
	}
}

type stubObserver struct {
	snap metrics.Snapshot
}

func (s *stubObserver) Snapshot() metrics.Snapshot { return s.snap }

func controller(t *testing.T, snap metrics.Snapshot) *Controller {
	t.Helper()
	cfg := config.Default().Safety
	cfg.MinSamplesForStop = 5
	c := NewController(cfg, &stubObserver{snap})
	c.SetClock(func() time.Time { return time.Unix(0, 0) })
	return c
}

func TestController_429StopsImmediately(t *testing.T) {
	snap := metrics.Snapshot{Status429: 25} // default limit is 20
	snap.AttemptLatency.Count = 100
	c := controller(t, snap)
	reason, tripped := c.Check()
	if !tripped || reason == "" {
		t.Fatalf("want trip, got %q", reason)
	}
	if !contains(reason, "429") {
		t.Errorf("reason should mention 429: %q", reason)
	}
	// Sticky: stays tripped even after metrics improve.
	if _, tripped := c.Check(); !tripped {
		t.Error("controller must be sticky")
	}
}

func TestController_5xxLimit(t *testing.T) {
	snap := metrics.Snapshot{Status5xx: 100}
	c := controller(t, snap)
	if reason, tripped := c.Check(); !tripped || !contains(reason, "5xx") {
		t.Errorf("want 5xx trip, got %q", reason)
	}
}

func TestController_ErrorRate(t *testing.T) {
	snap := metrics.Snapshot{AttemptsFailed: 50, AttemptsSucceeded: 50}
	c := controller(t, snap)
	if reason, tripped := c.Check(); !tripped || !contains(reason, "error rate") {
		t.Errorf("want error-rate trip, got %q", reason)
	}
}

func TestController_P99(t *testing.T) {
	snap := metrics.Snapshot{AttemptsSucceeded: 100, AttemptsFailed: 0}
	snap.AttemptLatency.P99 = 20_000 // ms
	snap.AttemptLatency.Count = 100
	c := controller(t, snap)
	if reason, tripped := c.Check(); !tripped || !contains(reason, "p99") {
		t.Errorf("want p99 trip, got %q", reason)
	}
}

func TestController_RequestBudget(t *testing.T) {
	snap := metrics.Snapshot{RequestsStarted: 500_000}
	c := controller(t, snap)
	if reason, tripped := c.Check(); !tripped || !contains(reason, "budget") {
		t.Errorf("want budget trip, got %q", reason)
	}
}

func TestController_HealthyRunContinues(t *testing.T) {
	snap := metrics.Snapshot{AttemptsSucceeded: 1000}
	snap.AttemptLatency.P99 = 200
	snap.AttemptLatency.Count = 1000
	c := controller(t, snap)
	if _, tripped := c.Check(); tripped {
		t.Error("healthy metrics must not trip the controller")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
