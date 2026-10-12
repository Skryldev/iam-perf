package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"example.com/iam-perf/internal/testkit"
	"example.com/iam-perf/pkg/iamperf"
)

// TestGenerateSampleReports writes REAL reports produced against the local
// fixture (never the real service) to docs/examples/. Gated by
// IAMPERF_GENERATE_SAMPLES=1 so normal CI skips it.
func TestGenerateSampleReports(t *testing.T) {
	if os.Getenv("IAMPERF_GENERATE_SAMPLES") != "1" {
		t.Skip("set IAMPERF_GENERATE_SAMPLES=1 to regenerate docs/examples")
	}
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	dir := filepath.Join(repoRoot(t), "docs", "examples")

	// Scenario A: closed-model login flow.
	cfg := baseCfg(srv.URL)
	cfg.Authorization.Accepted = true
	cfg.Scenario.Duration.Duration = 5 * time.Second
	cfg.Scenario.WarmUp.Duration = time.Second
	cfg.Report.Formats = []string{"json"}
	eng, err := iamperf.NewEngine(cfg,
		iamperf.WithEnvLookup(func(k string) (string, bool) {
			return "test-password-only", k == "IAMPERF_TEST_PASSWORD"
		}), iamperf.WithRunConfirmation(true))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "sample-report-closed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Scenario B: open-model login flow with ramp.
	cfgB := baseCfg(srv.URL)
	cfgB.Authorization.Accepted = true
	cfgB.Scenario.Model = "open"
	cfgB.Scenario.RequestRate = 20
	cfgB.Scenario.MaxInFlight = 8
	cfgB.Scenario.Duration.Duration = 3 * time.Second
	cfgB.Scenario.RampUp.Duration = time.Second
	engB, err := iamperf.NewEngine(cfgB,
		iamperf.WithEnvLookup(func(k string) (string, bool) {
			return "test-password-only", k == "IAMPERF_TEST_PASSWORD"
		}), iamperf.WithRunConfirmation(true))
	if err != nil {
		t.Fatal(err)
	}
	repB, err := engB.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Create(filepath.Join(dir, "sample-report-open.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repB.WriteJSON(fb); err != nil {
		t.Fatal(err)
	}
	fb.Close()
}
