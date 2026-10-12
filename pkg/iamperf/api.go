// Package iamperf is the stable public API of the IAM login performance
// testing framework. It lets another Go application execute a controlled,
// safety-gated login workload WITHOUT the CLI.
//
// Minimal usage:
//
//	cfg := iamperf.DefaultConfig()
//	cfg.Target.BaseURL = "https://iam-lab.example.ir"
//	cfg.Authorization.Accepted = true // plus runtime confirmation below
//	eng, err := iamperf.NewEngine(cfg, iamperf.WithRunConfirmation(true))
//	rep, err := eng.Run(context.Background())
//
// Stability: this package follows semantic versioning. Type aliases exposed
// here are the API surface; internal packages are unimportable by design.
package iamperf

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"example.com/iam-perf/internal/auth"
	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/internal/report"
	"example.com/iam-perf/internal/runner"
	"example.com/iam-perf/internal/safety"
	"example.com/iam-perf/internal/telemetry"
	"example.com/iam-perf/internal/transport"
)

// Config is the engine configuration (alias for stability).
type Config = config.Config

// Report is the run result (alias for stability).
type Report = report.Report

// Sentinel errors (errors.Is-compatible with internal sources).
var (
	ErrValidation        = config.ErrValidation
	ErrNotAuthorized     = safety.ErrNotAuthorized
	ErrTargetNotAllowed  = safety.ErrHostNotAllowed
	ErrPathNotAllowed    = safety.ErrPathNotAllowed
	ErrCredentialsNeeded = errors.New("iamperf: credentials not available (set credentials.password_env target or credentials.password_file with mode 0600)")
)

// version is overridden at build time via -ldflags -X.
var version = "0.1.0"

// Version returns the module version.
func Version() string { return version }

// DefaultConfig returns the safe default configuration.
func DefaultConfig() Config { return config.Default() }

// Engine executes configured workloads. It is safe to reuse for sequential
// runs; each Run is fully independent.
type Engine struct {
	cfg  Config
	opts options
}

type options struct {
	logger        *slog.Logger
	clock         func() time.Time
	runConfirm    bool
	envLookup     func(string) (string, bool)
	collectorHook func(*metrics.Collector) // live access for observability integrations
}

// Option configures the Engine.
type Option func(*options)

// WithLogger injects a structured logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithClock injects a clock for deterministic tests.
func WithClock(f func() time.Time) Option { return func(o *options) { o.clock = f } }

// WithRunConfirmation supplies the runtime two-key confirmation
// (equivalent of the CLI --iam-accept-risk flag).
func WithRunConfirmation(ok bool) Option { return func(o *options) { o.runConfirm = ok } }

// WithEnvLookup overrides environment access (tests).
func WithEnvLookup(f func(string) (string, bool)) Option {
	return func(o *options) { o.envLookup = f }
}

// WithCollectorHook receives the live collector when a run starts, enabling
// callers to expose Prometheus endpoints or other live observability.
func WithCollectorHook(f func(*metrics.Collector)) Option {
	return func(o *options) { o.collectorHook = f }
}

// NewEngine validates configuration (no network I/O) and returns an Engine.
func NewEngine(cfg Config, opts ...Option) (*Engine, error) {
	if cfg.SchemaVersion != config.SchemaVersion {
		return nil, fmt.Errorf("%w: schema_version %d not supported (want %d)",
			ErrValidation, cfg.SchemaVersion, config.SchemaVersion)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	o := options{envLookup: os.LookupEnv}
	for _, f := range opts {
		f(&o)
	}
	if o.logger == nil {
		o.logger = telemetry.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stderr)
	}
	if o.clock == nil {
		o.clock = time.Now
	}
	return &Engine{cfg: cfg, opts: o}, nil
}

// ValidateConfig checks configuration without building an engine.
func ValidateConfig(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := safety.NewAllowlist(cfg.Target); err != nil {
		return err
	}
	return nil
}

// Run executes the workload and returns the report. Load is generated ONLY
// after the authorization gate passes; all other paths return errors without
// touching the network.
func (e *Engine) Run(ctx context.Context) (*Report, error) {
	started := e.opts.clock()

	// 1) Authorization gate (two keys).
	if err := safety.RequireApproval(e.cfg.Authorization, e.opts.runConfirm, e.opts.envLookup); err != nil {
		return nil, err
	}

	// 2) Allowlist.
	allow, err := safety.NewAllowlist(e.cfg.Target)
	if err != nil {
		return nil, err
	}

	// 3) Flow construction (no I/O yet).
	var flow auth.Flow
	var creds auth.Credentials
	switch e.cfg.Scenario.Flow {
	case "mohaymen-login":
		creds, err = e.resolveCredentials()
		if err != nil {
			return nil, err
		}
		flow = auth.NewMohaymenFlow(e.cfg.Transport.WarmLoginPage, e.cfg.Transport.AcceptLanguage, e.cfg.Flow)
	case "transport-probe":
		flow = auth.NewProbeFlow("/login")
	}

	// 4) Transport factory + collector + runner.
	collector := metrics.NewCollector()
	if e.opts.collectorHook != nil {
		e.opts.collectorHook(collector)
	}
	factory, err := transport.NewFactory(e.cfg, allow, collector)
	if err != nil {
		return nil, err
	}
	rn, err := runner.New(runner.Options{
		Cfg:       e.cfg,
		Flow:      flow,
		Factory:   factory,
		Collector: collector,
		Allow:     allow,
		Creds:     creds,
		Logger:    e.opts.logger,
		Clock:     e.opts.clock,
	})
	if err != nil {
		return nil, err
	}
	res, rerr := rn.Run(ctx)

	// 5) Report.
	ended := e.opts.clock()
	rep := report.Build(report.BuildOptions{
		RunID:         newRunID(),
		ModuleVersion: version,
		Cfg:           e.cfg,
		AllowHost:     allow.Host(),
		Snapshot:      res.Snapshot,
		StartedAt:     started,
		EndedAt:       ended,
		Termination:   res.Termination,
		SafetyReason:  res.SafetyReason,
		Limitations:   standardLimitations(e.cfg),
	})
	return rep, rerr
}

// resolveCredentials reads the password from env or file (0600 enforced).
// The value is held in memory only, never logged, never marshaled.
func (e *Engine) resolveCredentials() (auth.Credentials, error) {
	c := e.cfg.Credentials
	if c.PasswordFile != "" {
		st, err := os.Stat(c.PasswordFile)
		if err != nil {
			return auth.Credentials{}, fmt.Errorf("%w: stat %q: %s", ErrCredentialsNeeded, c.PasswordFile, err)
		}
		if st.Mode().Perm() != 0o600 {
			return auth.Credentials{}, fmt.Errorf("%w: %q must have mode 0600 (found %o)",
				ErrCredentialsNeeded, c.PasswordFile, st.Mode().Perm())
		}
		pw, err := os.ReadFile(c.PasswordFile) // #nosec G304 -- operator-provided path
		if err != nil {
			return auth.Credentials{}, fmt.Errorf("%w: read %q: %s", ErrCredentialsNeeded, c.PasswordFile, err)
		}
		return auth.Credentials{Username: c.Username, Password: trimNewline(string(pw))}, nil
	}
	envName := c.PasswordEnv
	if envName == "" {
		envName = "IAMPERF_TEST_PASSWORD"
	}
	v, ok := e.opts.envLookup(envName)
	if !ok || v == "" {
		return auth.Credentials{}, fmt.Errorf("%w: environment variable %s is empty or unset", ErrCredentialsNeeded, envName)
	}
	return auth.Credentials{Username: c.Username, Password: v}, nil
}

// standardLimitations returns the disclosure block embedded in every report.
func standardLimitations(cfg Config) []string {
	lim := []string{
		"Failure-response contract of the target was not present in the supplied evidence; " +
			"failures are classified conservatively (non-2xx, missing tokens, null userId).",
		"Session model is header-token based per the capture (x-auth-token); server-side " +
			"cookie behavior was not observed and is not modeled.",
		"Single-factor password flow only; MFA/challenge paths were not captured and are not implemented.",
		"Results are bounded by this generator host's CPU/memory/network; verify the generator " +
			"was not saturated (see environment fields) before attributing limits to the target.",
	}
	if cfg.Scenario.Flow == "transport-probe" {
		lim = append(lim,
			"This run used the non-authenticating transport-probe flow; attempt metrics describe "+
				"HTTP probes, NOT successful logins.")
	}
	return lim
}

// newRunID produces a random RFC-4122-shaped v4 UUID.
func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// non-cryptographic fallback keeps the run alive; uniqueness only
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
