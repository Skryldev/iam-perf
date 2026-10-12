package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrValidation is returned wrapped for every invalid configuration.
var ErrValidation = errors.New("invalid configuration")

func verr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

// Load reads and layers configuration: defaults < file < environment.
// Flags are applied by the CLI on top of the returned value.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path) // #nosec G304 -- path is operator-provided CLI input
		if err != nil {
			return cfg, verr("cannot read config file %q: %s", path, err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, verr("parsing %q: %s", path, err)
		}
	}
	applyEnv(&cfg, os.Environ())
	if cfg.SchemaVersion != SchemaVersion {
		return cfg, verr("schema_version %d not supported (want %d)", cfg.SchemaVersion, SchemaVersion)
	}
	return cfg, nil
}

// applyEnv overlays IAMPERF_* variables (file < env layering).
func applyEnv(cfg *Config, environ []string) {
	setStr := func(dst *string, key string) {
		if v, ok := lookup(environ, EnvPrefix+key); ok && v != "" {
			*dst = v
		}
	}
	setInt := func(dst *int, key string) {
		if v, ok := lookup(environ, EnvPrefix+key); ok && v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	setBool := func(dst *bool, key string) {
		if v, ok := lookup(environ, EnvPrefix+key); ok && v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}
	setDur := func(dst *Duration, key string) {
		if v, ok := lookup(environ, EnvPrefix+key); ok && v != "" {
			if d, err := parseFlexibleDuration(v); err == nil {
				dst.Duration = d
			}
		}
	}
	setStr(&cfg.Target.BaseURL, "TARGET_BASEURL")
	setBool(&cfg.Authorization.Accepted, "AUTHORIZATION_ACCEPTED")
	setStr(&cfg.Scenario.Name, "SCENARIO_NAME")
	setStr(&cfg.Scenario.Flow, "SCENARIO_FLOW")
	setStr(&cfg.Scenario.Model, "SCENARIO_MODEL")
	setInt(&cfg.Scenario.VirtualUsers, "SCENARIO_VIRTUAL_USERS")
	setDur(&cfg.Scenario.Duration, "SCENARIO_DURATION")
	setDur(&cfg.Scenario.ThinkTime, "SCENARIO_THINK_TIME")
	setDur(&cfg.Scenario.RampUp, "SCENARIO_RAMP_UP")
	setDur(&cfg.Scenario.RampDown, "SCENARIO_RAMP_DOWN")
	setDur(&cfg.Scenario.WarmUp, "SCENARIO_WARMUP")
	setFloatEnviron(environ, EnvPrefix+"SCENARIO_REQUEST_RATE", &cfg.Scenario.RequestRate)
	setInt(&cfg.Scenario.MaxInFlight, "SCENARIO_MAX_IN_FLIGHT")
	setInt(&cfg.Transport.MaxRedirects, "TRANSPORT_MAX_REDIRECTS")
	setStr(&cfg.Credentials.Username, "CREDENTIALS_USERNAME")
	// NOTE: passwords are read only at flow-build time via Credentials.PasswordEnv;
	// they are never loaded into Config.
}

func lookup(environ []string, key string) (string, bool) {
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func setFloatEnviron(environ []string, key string, dst *float64) {
	if v, ok := lookup(environ, key); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			*dst = f
		}
	}
}

func parseFlexibleDuration(s string) (d time.Duration, err error) {
	var dd Duration
	e := dd.UnmarshalJSON([]byte(`"` + s + `"`))
	return dd.Duration, e
}

// Default returns the safe, conservative default configuration.
func Default() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Target: Target{
			BaseURL:      "https://iam.mohaymen.ir",
			AllowedPaths: []string{"/login", "/api/v1/"},
		},
		Authorization: Authorization{},
		Scenario: Scenario{
			Name:         "login-baseline",
			Flow:         "mohaymen-login",
			Model:        "closed",
			VirtualUsers: 1,
			Duration:     Duration{1 * time.Minute},
			ThinkTime:    Duration{1 * time.Second},
			MaxInFlight:  64,
		},
		Transport: Transport{
			RequestTimeout:        Duration{15 * time.Second},
			DialTimeout:           Duration{5 * time.Second},
			TLSHandshakeTimeout:   Duration{5 * time.Second},
			ResponseHeaderTimeout: Duration{10 * time.Second},
			IdleConnTimeout:       Duration{60 * time.Second},
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   64,
			MaxConnsPerHost:       0, // unlimited; bounded by VU count / MaxInFlight
			MaxRedirects:          5,
			MaxResponseBodyBytes:  1 << 20,
			UserAgent:             "iam-perf/0.1 (+load-test; approved-scope-only)",
			AcceptLanguage:        "fa-IR",
			WarmLoginPage:         false,
		},
		Credentials: Credentials{
			Username:    "",
			PasswordEnv: "IAMPERF_TEST_PASSWORD",
		},
		Safety: Safety{
			MaxVirtualUsers:    100,
			MaxRequestsPerRate: 200,
			MaxDuration:        Duration{30 * time.Minute},
			MaxTotalRequests:   200_000,
			StopErrorRatePct:   50,
			Stop5xxCount:       100,
			Stop429Count:       20,
			StopP99Millis:      10_000,
			MinSamplesForStop:  20,
		},
		Metrics: Metrics{},
		Report: Report{
			OutputDir: "reports",
			Formats:   []string{"json", "terminal"},
		},
		Log: Log{Level: "info", Format: "text"},
		Flow: Flow{
			InitiateLoginPath:        "/api/v1/auth/initiate-login",
			VerifyPasswordPath:       "/api/v1/auth/verifyPassword",
			AuthenticationStatusPath: "/api/v1/auth/authentication-status",
			LoginPath:                "/api/v1/auth/login",
			SessionProofPath:         "/api/v1/apps/users/dashboardPermissions",
			SessionProof:             true,
			AuthenticationStatus:     true,
		},
	}
}

// Validate checks internal consistency and returns actionable errors.
func (c *Config) Validate() error {
	u, err := url.Parse(c.Target.BaseURL)
	if err != nil {
		return verr("target.base_url %q is not a valid URL: %s", c.Target.BaseURL, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return verr("target.base_url must use http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return verr("target.base_url is missing a host")
	}
	if len(c.Target.AllowedPaths) == 0 {
		return verr("target.allowed_paths must not be empty")
	}
	for _, p := range c.Target.AllowedPaths {
		if !strings.HasPrefix(p, "/") {
			return verr("target.allowed_paths entry %q must start with '/'", p)
		}
	}

	switch c.Scenario.Flow {
	case "mohaymen-login", "transport-probe":
	default:
		return verr("scenario.flow must be \"mohaymen-login\" or \"transport-probe\", got %q", c.Scenario.Flow)
	}
	switch c.Scenario.Model {
	case "closed":
		if c.Scenario.VirtualUsers < 1 {
			return verr("scenario.virtual_users must be >= 1 for closed model")
		}
	case "open":
		if c.Scenario.RequestRate <= 0 {
			return verr("scenario.request_rate must be > 0 for open model")
		}
		if c.Scenario.MaxInFlight < 1 {
			return verr("scenario.max_in_flight must be >= 1 for open model")
		}
	default:
		return verr("scenario.model must be \"closed\" or \"open\", got %q", c.Scenario.Model)
	}
	if c.Scenario.Duration.Duration <= 0 {
		return verr("scenario.duration must be > 0")
	}

	if c.Scenario.Flow == "mohaymen-login" {
		if c.Credentials.Username == "" {
			return verr("credentials.username is required for flow %q", c.Scenario.Flow)
		}
		if c.Credentials.PasswordEnv == "" && c.Credentials.PasswordFile == "" {
			return verr("credentials.password_env or credentials.password_file must be set")
		}
		if c.Credentials.PasswordEnv != "" && c.Credentials.PasswordFile != "" {
			return verr("credentials: set either password_env or password_file, not both")
		}
	}

	// Safety consistency: requested workload must fit inside the approved budget.
	if c.Scenario.VirtualUsers > c.Safety.MaxVirtualUsers {
		return verr("scenario.virtual_users (%d) exceeds safety.max_virtual_users (%d)",
			c.Scenario.VirtualUsers, c.Safety.MaxVirtualUsers)
	}
	if c.Scenario.Model == "open" && c.Scenario.RequestRate > c.Safety.MaxRequestsPerRate {
		return verr("scenario.request_rate (%.2f) exceeds safety.max_requests_per_second (%.2f)",
			c.Scenario.RequestRate, c.Safety.MaxRequestsPerRate)
	}
	if c.Scenario.Duration.Duration > c.Safety.MaxDuration.Duration {
		return verr("scenario.duration (%s) exceeds safety.max_duration (%s)",
			c.Scenario.Duration.Duration, c.Safety.MaxDuration.Duration)
	}
	if c.Safety.MaxTotalRequests <= 0 {
		return verr("safety.max_total_requests must be > 0")
	}
	if c.Transport.MaxRedirects < 0 {
		return verr("transport.max_redirects must be >= 0")
	}
	if c.Transport.MaxResponseBodyBytes <= 0 {
		return verr("transport.max_response_body_bytes must be > 0")
	}
	if c.Transport.TrustedCAsFile != "" {
		if _, err := os.ReadFile(c.Transport.TrustedCAsFile); err != nil {
			return verr("transport.trusted_cas_file: %s", err)
		}
	}
	if c.Metrics.PrometheusEnabled {
		if _, _, err := splitHostPort(c.Metrics.PrometheusAddr); err != nil {
			return verr("metrics.prometheus_addr %q must be host:port", c.Metrics.PrometheusAddr)
		}
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return verr("log.level must be debug|info|warn|error, got %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return verr("log.format must be text|json, got %q", c.Log.Format)
	}
	if c.Report.OutputDir == "" {
		return verr("report.output_dir must not be empty")
	}
	for _, f := range c.Report.Formats {
		switch f {
		case "json", "csv", "terminal":
		default:
			return verr("report.formats entry %q must be json|csv|terminal", f)
		}
	}
	return nil
}

func splitHostPort(addr string) (string, int, error) {
	idx := strings.LastIndex(addr, ":")
	if idx <= 0 || idx == len(addr)-1 {
		return "", 0, fmt.Errorf("bad address")
	}
	port, err := strconv.Atoi(addr[idx+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("bad port")
	}
	return addr[:idx], port, nil
}
