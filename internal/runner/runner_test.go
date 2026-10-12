package runner

import (
	"context"
	"testing"
	"time"

	"example.com/iam-perf/internal/auth"
	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/internal/safety"
	"example.com/iam-perf/internal/testkit"
	"example.com/iam-perf/internal/transport"
)

func buildRunner(t *testing.T, cfg config.Config, srvURL string, creds auth.Credentials) (*Runner, *metrics.Collector) {
	t.Helper()
	allow, err := safety.NewAllowlist(cfg.Target)
	if err != nil {
		t.Fatal(err)
	}
	col := metrics.NewCollector()
	factory, err := transport.NewFactory(cfg, allow, col)
	if err != nil {
		t.Fatal(err)
	}
	flow := auth.NewMohaymenFlow(cfg.Transport.WarmLoginPage, cfg.Transport.AcceptLanguage, cfg.Flow)
	r, err := New(Options{
		Cfg:           cfg,
		Flow:          flow,
		Factory:       factory,
		Collector:     col,
		Allow:         allow,
		Creds:         creds,
		WatchInterval: 20 * time.Millisecond,
		DrainGrace:    2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, col
}

func baseScenario(vu int, dur time.Duration) func(*config.Config) {
	return func(c *config.Config) {
		c.Scenario.VirtualUsers = vu
		c.Scenario.Duration = config.Duration{Duration: dur}
		c.Scenario.ThinkTime = config.Duration{Duration: 10 * time.Millisecond}
		c.Scenario.WarmUp = config.Duration{Duration: 0}
	}
}

func TestRunner_ClosedModel_SuccessAndIsolation(t *testing.T) {
	srv, st := testkit.NewIAM(t, testkit.Config{Track: true, RequirePreAuth: true})
	cfg := config.Default()
	cfg.Target.BaseURL = srv.URL
	baseScenario(4, 300*time.Millisecond)(&cfg)

	r, col := buildRunner(t, cfg, srv.URL, auth.Credentials{Username: "SRETEST", Password: "test-password-only"})
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snap := res.Snapshot
	if snap.AttemptsSucceeded < 4 {
		t.Errorf("attempts succeeded = %d, want >= 4", snap.AttemptsSucceeded)
	}
	if snap.AttemptsFailed != 0 || snap.AttemptsAborted != 0 {
		t.Errorf("failed=%d aborted=%d, want 0/0", snap.AttemptsFailed, snap.AttemptsAborted)
	}
	// Isolation: every attempt used a FRESH pre-auth token (never reused).
	if n := st.DistinctPreAuthTokens(); n < int(snap.AttemptsSucceeded) {
		t.Errorf("distinct pre-auth tokens = %d, want >= succeeded=%d (state must not be shared)", n, snap.AttemptsSucceeded)
	}
	if uses := st.MaxPreAuthUses(); uses > 5 { // 1 token serves at most one VU's 4-step chain (incl. proof)
		t.Errorf("token used %d times (cross-session sharing suspected)", uses)
	}
	if col.Snapshot().BackpressureDrops != 0 {
		t.Error("closed model must not drop arrivals")
	}
}

func TestRunner_Cancellation(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	cfg := config.Default()
	cfg.Target.BaseURL = srv.URL
	baseScenario(2, 10*time.Second)(&cfg) // long run, cancelled early

	r, _ := buildRunner(t, cfg, srv.URL, auth.Credentials{Username: "SRETEST", Password: "test-password-only"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	res, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Termination != TerminationCancelled {
		t.Errorf("termination = %q, want cancelled", res.Termination)
	}
	if res.Snapshot.AttemptsSucceeded > 100 {
		t.Errorf("cancellation did not stop the workload: %d successes", res.Snapshot.AttemptsSucceeded)
	}
}

func TestRunner_SafetyStop(t *testing.T) {
	// Fixture always returns 401 on verify → error rate hits 100%.
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	cfg := config.Default()
	cfg.Target.BaseURL = srv.URL
	baseScenario(2, 10*time.Second)(&cfg)
	cfg.Safety.StopErrorRatePct = 50
	cfg.Safety.MinSamplesForStop = 5

	r, _ := buildRunner(t, cfg, srv.URL, auth.Credentials{Username: "SRETEST", Password: "wrong"})
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Termination != TerminationSafety {
		t.Fatalf("termination = %q, want safety-stop", res.Termination)
	}
	if res.SafetyReason == "" {
		t.Error("safety reason must be recorded")
	}
}

func TestRunner_OpenModel_Backpressure(t *testing.T) {
	// 60ms per auth step (~300ms per attempt) against 200 arrivals/s and a
	// 2-slot queue: the queue MUST saturate and drop deterministically.
	srv, _ := testkit.NewIAM(t, testkit.Config{SlowMs: 60})
	cfg := config.Default()
	cfg.Target.BaseURL = srv.URL
	cfg.Scenario.Model = "open"
	cfg.Scenario.RequestRate = 200
	cfg.Scenario.MaxInFlight = 2
	cfg.Scenario.Duration = config.Duration{Duration: 300 * time.Millisecond}
	cfg.Scenario.RampUp = config.Duration{Duration: 0}
	cfg.Scenario.ThinkTime = config.Duration{Duration: 0}
	cfg.Scenario.WarmUp = config.Duration{Duration: 0}
	cfg.Safety.MaxRequestsPerRate = 1000

	r, _ := buildRunner(t, cfg, srv.URL, auth.Credentials{Username: "SRETEST", Password: "test-password-only"})
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Snapshot.RequestsStarted == 0 {
		t.Error("open model generated no load")
	}
	if res.Snapshot.BackpressureDrops == 0 {
		t.Error("expected backpressure drops with tiny queue and high rate")
	}
}

func TestRunner_WarmupExcluded(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	cfg := config.Default()
	cfg.Target.BaseURL = srv.URL
	baseScenario(1, 400*time.Millisecond)(&cfg)
	cfg.Scenario.WarmUp = config.Duration{Duration: 200 * time.Millisecond}

	r, _ := buildRunner(t, cfg, srv.URL, auth.Credentials{Username: "SRETEST", Password: "test-password-only"})
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snap := res.Snapshot
	if snap.WarmupAttempts == 0 {
		t.Error("expected warm-up attempts to be tagged")
	}
	if snap.AttemptLatency.Count != uint64(snap.AttemptsStarted-snap.WarmupAttempts-snap.AttemptsAborted) {
		t.Errorf("latency count %d inconsistent with attempts %d (warmup %d, aborted %d)",
			snap.AttemptLatency.Count, snap.AttemptsStarted, snap.WarmupAttempts, snap.AttemptsAborted)
	}
}
