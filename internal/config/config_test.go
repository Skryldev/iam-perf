package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefault_TemplateIsValidWithCredentials(t *testing.T) {
	cfg := Default()
	// The default is a template: with credentials supplied it must validate.
	cfg.Credentials.Username = "SRETEST"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("template config must validate with credentials: %s", err)
	}
	// Safety posture of defaults: authorization NOT accepted.
	if cfg.Authorization.Accepted {
		t.Fatal("defaults must never auto-authorize load generation")
	}
}

func TestLoad_FileLayering(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.json")
	raw := `{
                "schema_version": 1,
                "target": {"base_url": "https://lab.example.ir", "allowed_paths": ["/login"]},
                "scenario": {"virtual_users": 3, "duration": "30s", "think_time": "500ms"},
                "safety": {"max_virtual_users": 10},
                "unknown_field": true
        }`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	// Unknown fields must be rejected (typo protection).
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}

	rawOK := `{
                "schema_version": 1,
                "target": {"base_url": "https://lab.example.ir", "allowed_paths": ["/login"]},
                "scenario": {"virtual_users": 3, "duration": "30s", "think_time": "500ms"},
                "safety": {"max_virtual_users": 10}
        }`
	if err := os.WriteFile(p, []byte(rawOK), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Target.BaseURL != "https://lab.example.ir" {
		t.Errorf("base_url = %q", cfg.Target.BaseURL)
	}
	if cfg.Scenario.VirtualUsers != 3 {
		t.Errorf("virtual_users = %d", cfg.Scenario.VirtualUsers)
	}
	if cfg.Scenario.Duration.Duration != 30*time.Second {
		t.Errorf("duration = %s", cfg.Scenario.Duration.Duration)
	}
	if cfg.Scenario.ThinkTime.Duration != 500*time.Millisecond {
		t.Errorf("think_time = %s", cfg.Scenario.ThinkTime.Duration)
	}
	// Defaults preserved for unspecified fields:
	if cfg.Transport.MaxRedirects != 5 {
		t.Errorf("max_redirects default lost: %d", cfg.Transport.MaxRedirects)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.json")
	if err := os.WriteFile(p, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IAMPERF_TARGET_BASEURL", "https://env.example.ir")
	t.Setenv("IAMPERF_SCENARIO_VIRTUAL_USERS", "7")
	t.Setenv("IAMPERF_SCENARIO_DURATION", "45s")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Target.BaseURL != "https://env.example.ir" {
		t.Errorf("env base_url = %q", cfg.Target.BaseURL)
	}
	if cfg.Scenario.VirtualUsers != 7 {
		t.Errorf("env virtual_users = %d", cfg.Scenario.VirtualUsers)
	}
	if cfg.Scenario.Duration.Duration != 45*time.Second {
		t.Errorf("env duration = %s", cfg.Scenario.Duration.Duration)
	}
}

func TestValidate_Errors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"bad scheme", func(c *Config) { c.Target.BaseURL = "ftp://x" }, "http or https"},
		{"no host", func(c *Config) { c.Target.BaseURL = "https://" }, "missing a host"},
		{"empty paths", func(c *Config) { c.Target.AllowedPaths = nil }, "allowed_paths"},
		{"path no slash", func(c *Config) { c.Target.AllowedPaths = []string{"login"} }, "must start with"},
		{"bad flow", func(c *Config) { c.Scenario.Flow = "magic" }, "flow"},
		{"zero VUs closed", func(c *Config) { c.Scenario.VirtualUsers = 0 }, "virtual_users"},
		{"open no rate", func(c *Config) { c.Scenario.Model = "open" }, "request_rate"},
		{"VUs over budget", func(c *Config) { c.Scenario.VirtualUsers = 999 }, "max_virtual_users"},
		{"rate over budget", func(c *Config) { c.Scenario.Model = "open"; c.Scenario.RequestRate = 999 }, "max_requests_per_second"},
		{"duration over budget", func(c *Config) { c.Scenario.Duration = Duration{time.Hour} }, "max_duration"},
		{"no username", func(c *Config) { c.Credentials.Username = "" }, "username"},
		{"no secret source", func(c *Config) { c.Credentials.PasswordEnv = ""; c.Credentials.PasswordFile = "" }, "password_env"},
		{"both secret sources", func(c *Config) { c.Credentials.PasswordFile = "/tmp/x" }, "not both"},
		{"bad log level", func(c *Config) { c.Log.Level = "loud" }, "log.level"},
		{"bad format", func(c *Config) { c.Report.Formats = []string{"pdf"} }, "report.formats"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Credentials.Username = "u"
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestRedactedJSON(t *testing.T) {
	cfg := Default()
	cfg.Credentials.Username = "SRETEST"
	b, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "SRETEST") {
		t.Fatal("username must be redacted from resolved-config output")
	}
	for _, secret := range []string{"password", "IAMPERF_TEST_PASSWORD"} {
		if strings.Contains(s, secret) {
			// The key NAME appears as "password_env":"[REDACTED]" — that is fine;
			// VALUES must not appear.
			if strings.Contains(s, `"IAMPERF_TEST_PASSWORD":`) {
				t.Fatalf("secret reference value leaked: %s", secret)
			}
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("redacted output must be valid JSON: %s", err)
	}
}

func TestDurationUnmarshal(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{`"30s"`, 30 * time.Second},
		{`"500ms"`, 500 * time.Millisecond},
		{`"2m"`, 2 * time.Minute},
		{`90`, 90 * time.Second},
		{`"1h30m"`, 90 * time.Minute},
	}
	for _, c := range cases {
		var d Duration
		if err := json.Unmarshal([]byte(c.in), &d); err != nil {
			t.Fatalf("%s: %s", c.in, err)
		}
		if d.Duration != c.want {
			t.Errorf("%s = %s, want %s", c.in, d.Duration, c.want)
		}
	}
	if err := json.Unmarshal([]byte(`"banana"`), new(Duration)); err == nil {
		t.Error("invalid duration must error")
	}
}
