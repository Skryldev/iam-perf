// Package config defines the configuration schema, defaults, loading layering
// (defaults < file < environment < flags), validation, and redacted rendering.
//
// Configuration schema version 1.
package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SchemaVersion is the configuration schema version produced/consumed by this package.
const SchemaVersion = 1

// EnvPrefix is the prefix for environment-variable overrides.
const EnvPrefix = "IAMPERF_"

// Config is the root configuration object. Field order is API-stable.
type Config struct {
	SchemaVersion int           `json:"schema_version"`
	Target        Target        `json:"target"`
	Authorization Authorization `json:"authorization"`
	Scenario      Scenario      `json:"scenario"`
	Transport     Transport     `json:"transport"`
	Credentials   Credentials   `json:"credentials"`
	Safety        Safety        `json:"safety"`
	Metrics       Metrics       `json:"metrics"`
	Report        Report        `json:"report"`
	Log           Log           `json:"log"`
	Flow          Flow          `json:"flow"`
}

// Target identifies the system under test and the approved request scope.
type Target struct {
	// BaseURL is the scheme://host[:port] root of the system under test.
	BaseURL string `json:"base_url"`
	// AllowedPaths are path prefixes the generator may touch. They are
	// enforced on the initial request URL AND on every redirect hop.
	AllowedPaths []string `json:"allowed_paths"`
}

// Authorization records the operator's explicit decision to generate load.
type Authorization struct {
	// Accepted must be true to enable `run`; `validate` never requires it.
	Accepted bool `json:"accepted"`
	// ScopeNote documents who approved what, when. Free text, no secrets.
	ScopeNote string `json:"scope_note"`
}

// Scenario describes the workload to execute.
type Scenario struct {
	Name string `json:"name"`
	// Flow selects the protocol implementation: "mohaymen-login" (evidence-based)
	// or "transport-probe" (non-authenticating GET /login health probe).
	Flow         string   `json:"flow"`
	Model        string   `json:"model"` // "closed" | "open"
	VirtualUsers int      `json:"virtual_users"`
	Duration     Duration `json:"duration"`
	RampUp       Duration `json:"ramp_up"`
	RampDown     Duration `json:"ramp_down"`
	WarmUp       Duration `json:"warm_up"`
	ThinkTime    Duration `json:"think_time"`
	RequestRate  float64  `json:"request_rate"`  // arrivals/sec, open model only
	MaxInFlight  int      `json:"max_in_flight"` // bounded queue, open model
}

// Transport controls the per-VU HTTP clients.
type Transport struct {
	RequestTimeout        Duration `json:"request_timeout"`
	DialTimeout           Duration `json:"dial_timeout"`
	TLSHandshakeTimeout   Duration `json:"tls_handshake_timeout"`
	ResponseHeaderTimeout Duration `json:"response_header_timeout"`
	IdleConnTimeout       Duration `json:"idle_conn_timeout"`
	MaxIdleConns          int      `json:"max_idle_conns"`
	MaxIdleConnsPerHost   int      `json:"max_idle_conns_per_host"`
	MaxConnsPerHost       int      `json:"max_conns_per_host"`
	MaxRedirects          int      `json:"max_redirects"`
	MaxResponseBodyBytes  int64    `json:"max_response_body_bytes"`
	UserAgent             string   `json:"user_agent"`
	AcceptLanguage        string   `json:"accept_language"`
	// TrustedCAsFile optionally adds a PEM CA bundle for lab/private PKIs.
	// TLS verification is ALWAYS on; there is no skip-verify option by design.
	TrustedCAsFile string `json:"trusted_cas_file"`
	// ProxyURL enables an explicit, approved proxy (e.g. for controlled egress).
	ProxyURL string `json:"proxy_url"`
	// WarmLoginPage: GET /login before the auth chain (mirrors capture step 1).
	WarmLoginPage bool `json:"warm_login_page"`
}

// Credentials references secrets WITHOUT containing them.
type Credentials struct {
	Username string `json:"username"`
	// PasswordEnv names an environment variable holding the password.
	PasswordEnv string `json:"password_env"`
	// PasswordFile points at a file (mode 0600 enforced) holding the password.
	PasswordFile string `json:"password_file"`
}

// Safety holds hard budgets and live stop conditions.
type Safety struct {
	MaxVirtualUsers    int      `json:"max_virtual_users"`
	MaxRequestsPerRate float64  `json:"max_requests_per_second"`
	MaxDuration        Duration `json:"max_duration"`
	MaxTotalRequests   int64    `json:"max_total_requests"`
	StopErrorRatePct   float64  `json:"stop_error_rate_pct"`
	Stop5xxCount       int64    `json:"stop_5xx_count"`
	Stop429Count       int64    `json:"stop_429_count"`
	StopP99Millis      float64  `json:"stop_p99_millis"`
	// MinSamplesForStop avoids premature stops on tiny samples.
	MinSamplesForStop int64 `json:"min_samples_for_stop"`
}

// Metrics configures optional live observability.
type Metrics struct {
	PrometheusEnabled bool   `json:"prometheus_enabled"`
	PrometheusAddr    string `json:"prometheus_addr"`
}

// Report controls output locations and formats.
type Report struct {
	OutputDir string   `json:"output_dir"`
	Formats   []string `json:"formats"` // subset of: json, csv, terminal
}

// Log controls structured logging.
type Log struct {
	Level  string `json:"level"`  // debug|info|warn|error
	Format string `json:"format"` // text|json
}

// Flow tunes the evidence-based login chain.
type Flow struct {
	// Endpoint overrides; defaults mirror the captured evidence.
	InitiateLoginPath        string `json:"initiate_login_path"`
	VerifyPasswordPath       string `json:"verify_password_path"`
	AuthenticationStatusPath string `json:"authentication_status_path"`
	LoginPath                string `json:"login_path"`
	SessionProofPath         string `json:"session_proof_path"`
	// SessionProof enables the post-login dashboardPermissions check.
	SessionProof bool `json:"session_proof"`
	// AuthenticationStatus includes the observed status poll step.
	AuthenticationStatus bool `json:"authentication_status"`
}

// Duration wraps time.Duration for JSON with string support ("30s").
type Duration struct{ time.Duration }

// UnmarshalJSON accepts "30s", "500ms", or an integer number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if n, err := fmt.Sscanf(s, "%d", new(int64)); err == nil && n == 1 && !strings.ContainsAny(s, "smh. ") {
		d.Duration = time.Duration(int64From(s)) * time.Second
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// MarshalJSON renders the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.Duration.String()) }

func int64From(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
