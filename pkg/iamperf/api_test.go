package iamperf_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"example.com/iam-perf/internal/testkit"
	"example.com/iam-perf/pkg/iamperf"
)

func validCfg(srvURL string) iamperf.Config {
	cfg := iamperf.DefaultConfig()
	cfg.Target.BaseURL = srvURL
	cfg.Scenario.VirtualUsers = 2
	cfg.Scenario.Duration.Duration = 300 * time.Millisecond
	cfg.Scenario.ThinkTime.Duration = 10 * time.Millisecond
	cfg.Scenario.WarmUp.Duration = 0
	cfg.Credentials.Username = "SRETEST"
	return cfg
}

func envWith(pw string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		if k == "IAMPERF_TEST_PASSWORD" {
			return pw, true
		}
		return "", false
	}
}

func TestEngine_ApprovalGate(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	cfg := validCfg(srv.URL)

	// No confirmation at all → must refuse BEFORE any network activity.
	eng, err := iamperf.NewEngine(cfg, iamperf.WithEnvLookup(envWith("pw")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("want ErrNotAuthorized, got %v", err)
	}

	// Only the runtime key → still refused (config key missing).
	eng2, err := iamperf.NewEngine(cfg,
		iamperf.WithEnvLookup(envWith("pw")), iamperf.WithRunConfirmation(true))
	if err != nil {
		t.Fatal(err)
	}
	cfgBad := cfg
	cfgBad.Authorization.Accepted = false
	eng2Bad, _ := iamperf.NewEngine(cfgBad,
		iamperf.WithEnvLookup(envWith("pw")), iamperf.WithRunConfirmation(true))
	if _, err := eng2Bad.Run(context.Background()); err == nil {
		t.Fatal("run without config approval must fail")
	}
	_ = eng2
}

func TestEngine_RunEndToEnd(t *testing.T) {
	srv, st := testkit.NewIAM(t, testkit.Config{Track: true, RequirePreAuth: true})
	cfg := validCfg(srv.URL)
	cfg.Authorization.Accepted = true

	eng, err := iamperf.NewEngine(cfg,
		iamperf.WithEnvLookup(envWith("test-password-only")),
		iamperf.WithRunConfirmation(true))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Metadata.Termination != "completed" {
		t.Errorf("termination = %s", rep.Metadata.Termination)
	}
	if rep.Attempts.Succeeded < 2 {
		t.Errorf("succeeded = %d, want >= 2", rep.Attempts.Succeeded)
	}
	if rep.Attempts.SuccessRatePct != 100 {
		t.Errorf("success rate = %.1f", rep.Attempts.SuccessRatePct)
	}
	if len(rep.Limitations) == 0 {
		t.Error("limitations must be disclosed")
	}
	// Evidence trace: username reached the fixture; tokens never reused.
	if users := st.InitiateUsers(); len(users) < 2 {
		t.Errorf("initiate users = %v", users)
	}
	if st.DistinctPreAuthTokens() < 2 {
		t.Error("expected isolated per-VU tokens")
	}
}

func TestEngine_RunWithoutCredentials(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	cfg := validCfg(srv.URL)
	cfg.Authorization.Accepted = true

	eng, err := iamperf.NewEngine(cfg,
		iamperf.WithEnvLookup(func(string) (string, bool) { return "", false }),
		iamperf.WithRunConfirmation(true))
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "credentials not available") {
		t.Fatalf("want ErrCredentialsNeeded, got %v", err)
	}
}

func TestEngine_ProbeFlow(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	cfg := validCfg(srv.URL)
	cfg.Authorization.Accepted = true
	cfg.Scenario.Flow = "transport-probe"

	eng, err := iamperf.NewEngine(cfg,
		iamperf.WithEnvLookup(envWith("x")), iamperf.WithRunConfirmation(true))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Probe runs must be explicitly marked in limitations.
	found := false
	for _, l := range rep.Limitations {
		if strings.Contains(l, "transport-probe") {
			found = true
		}
	}
	if !found {
		t.Error("probe run must disclose non-authenticating semantics")
	}
	if rep.Target.Flow != "transport-probe" {
		t.Errorf("flow = %s", rep.Target.Flow)
	}
}

func TestEngine_InvalidConfigRejected(t *testing.T) {
	cfg := iamperf.DefaultConfig()
	cfg.Target.BaseURL = "https://x"
	cfg.Credentials.Username = "" // required by default flow
	if _, err := iamperf.NewEngine(cfg); err == nil {
		t.Fatal("engine must reject invalid config")
	}
}

func TestReportJSON_IsStableAndRedacted(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	cfg := validCfg(srv.URL)
	cfg.Authorization.Accepted = true

	eng, _ := iamperf.NewEngine(cfg,
		iamperf.WithEnvLookup(envWith("test-password-only")),
		iamperf.WithRunConfirmation(true))
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "test-password-only") {
		t.Fatal("password leaked into report")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatalf("report not valid JSON: %s", err)
	}
	if _, ok := probe["schema_version"]; !ok {
		t.Error("schema_version missing")
	}
}
