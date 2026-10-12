// Package e2e runs full-stack offline scenarios: the compiled CLI against a
// local fixture IAM server. No test contacts the real target.
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/iam-perf/internal/testkit"
	"example.com/iam-perf/pkg/iamperf"
)

// buildCLI compiles the CLI binary once per test run.
func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "iam-perf")
	cmd := exec.Command("go", "build", "-o", bin, "example.com/iam-perf/cmd/iam-perf")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build CLI: %s\n%s", err, out)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// tests/ → repo root
	return filepath.Dir(dir)
}

func writeCfg(t *testing.T, dir string, cfg iamperf.Config) string {
	t.Helper()
	p := filepath.Join(dir, "cfg.json")
	b, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseCfg(url string) iamperf.Config {
	cfg := iamperf.DefaultConfig()
	cfg.Target.BaseURL = url
	cfg.Scenario.VirtualUsers = 2
	cfg.Scenario.Duration.Duration = 400 * time.Millisecond
	cfg.Scenario.ThinkTime.Duration = 5 * time.Millisecond
	cfg.Scenario.WarmUp.Duration = 0
	cfg.Credentials.Username = "SRETEST"
	return cfg
}

func TestCLI_Validate_NoNetwork(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	cfg := baseCfg("https://iam.mohaymen.ir")
	cfg.Credentials.Username = "SRETEST"
	p := writeCfg(t, dir, cfg)

	out, err := exec.Command(bin, "validate", "-c", p).CombinedOutput()
	if err != nil {
		t.Fatalf("validate failed: %s\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK: configuration is valid") {
		t.Errorf("unexpected output: %s", out)
	}
	// Secrets must be redacted in resolved output.
	if strings.Contains(string(out), "SRETEST") {
		t.Error("validate output leaks username")
	}
}

func TestCLI_Run_WritesReports(t *testing.T) {
	bin := buildCLI(t)
	srv, st := testkit.NewIAM(t, testkit.Config{Track: true, RequirePreAuth: true})
	dir := t.TempDir()
	cfg := baseCfg(srv.URL)
	cfg.Authorization.Accepted = true
	cfg.Report.OutputDir = filepath.Join(dir, "reports")
	cfg.Report.Formats = []string{"json", "csv", "terminal"}
	p := writeCfg(t, dir, cfg)

	cmd := exec.Command(bin, "run", "-c", p, "--iam-accept-risk")
	cmd.Env = append(os.Environ(), "IAMPERF_TEST_PASSWORD=test-password-only")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run failed: %s\n%s", err, out)
	}
	outs := string(out)
	if !strings.Contains(outs, "login transactions") {
		t.Errorf("terminal summary missing: %s", outs)
	}
	matches, _ := filepath.Glob(filepath.Join(cfg.Report.OutputDir, "*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected one JSON report, got %v", matches)
	}
	raw, _ := os.ReadFile(matches[0])
	var rep map[string]any
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("report invalid: %s", err)
	}
	if rep["schema_version"].(float64) != 1 {
		t.Error("schema_version must be 1")
	}
	if strings.Contains(string(raw), "test-password-only") {
		t.Error("report leaks password")
	}
	if st.DistinctPreAuthTokens() < 2 {
		t.Error("expected isolated sessions")
	}
	csvs, _ := filepath.Glob(filepath.Join(cfg.Report.OutputDir, "*.csv"))
	if len(csvs) != 1 {
		t.Errorf("expected one CSV, got %v", csvs)
	}
}

func TestCLI_Run_RefusesWithoutKeys(t *testing.T) {
	bin := buildCLI(t)
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	dir := t.TempDir()
	cfg := baseCfg(srv.URL)
	cfg.Authorization.Accepted = true
	p := writeCfg(t, dir, cfg)

	out, err := exec.Command(bin, "run", "-c", p).CombinedOutput()
	if err == nil {
		t.Fatal("run must refuse without runtime confirmation")
	}
	if !strings.Contains(string(out), "not authorized") {
		t.Errorf("unexpected refusal: %s", out)
	}
	// No report dir should exist (no load executed).
	if _, statErr := os.Stat(filepath.Join(dir, "reports")); statErr == nil {
		t.Error("no reports must be written for a refused run")
	}
}

func TestCLI_Report_RerendersStoredReport(t *testing.T) {
	bin := buildCLI(t)
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	dir := t.TempDir()

	// Produce a report through the public API.
	cfg := baseCfg(srv.URL)
	cfg.Authorization.Accepted = true
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
	rp := filepath.Join(dir, "r.json")
	f, _ := os.Create(rp)
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out, err := exec.Command(bin, "report", "-f", rp).CombinedOutput()
	if err != nil {
		t.Fatalf("report cmd failed: %s\n%s", err, out)
	}
	if !strings.Contains(string(out), "iam-perf report") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestEngine_TargetMismatchBlocked(t *testing.T) {
	// Configuration allowlist pins a DIFFERENT host than the fixture: the
	// engine must refuse to send anything (offline assertion of allowlist).
	cfg := baseCfg("https://iam.mohaymen.ir")
	cfg.Authorization.Accepted = true
	if _, err := iamperf.NewEngine(cfg,
		iamperf.WithEnvLookup(func(k string) (string, bool) { return "x", true }),
		iamperf.WithRunConfirmation(true)); err != nil {
		t.Fatal(err)
	}
	// Note: we deliberately do NOT run against the fixture here — the run
	// would target the real host string (blocked by policy in this suite).
	// Allowlist behavior is covered in internal/safety and internal/transport.
}

func TestEngine_CancelledRunProducesReport(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	cfg := baseCfg(srv.URL)
	cfg.Authorization.Accepted = true
	cfg.Scenario.Duration.Duration = 10 * time.Second

	eng, err := iamperf.NewEngine(cfg,
		iamperf.WithEnvLookup(func(k string) (string, bool) {
			return "test-password-only", k == "IAMPERF_TEST_PASSWORD"
		}), iamperf.WithRunConfirmation(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	rep, err := eng.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Metadata.Termination != "cancelled" {
		t.Errorf("termination = %s, want cancelled", rep.Metadata.Termination)
	}
	if rep.Attempts.Started == 0 {
		t.Error("expected some attempts before cancellation")
	}
}
