// Package safety enforces the authorization gate: target allowlisting,
// workload budgets, operator approval, and live stop conditions.
package safety

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
)

// Allowlist errors.
var (
	ErrHostNotAllowed = errors.New("host not in approved allowlist")
	ErrPathNotAllowed = errors.New("path not in approved scope")
)

// Allowlist is the hard boundary of where the generator may send traffic.
type Allowlist struct {
	scheme   string
	hostport string
	prefixes []string
}

// NewAllowlist builds the allowlist from the configured target.
func NewAllowlist(cfg config.Target) (*Allowlist, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("allowlist: bad base url: %w", err)
	}
	prefixes := make([]string, 0, len(cfg.AllowedPaths))
	for _, p := range cfg.AllowedPaths {
		prefixes = append(prefixes, strings.TrimSuffix(p, "/"))
	}
	return &Allowlist{
		scheme:   u.Scheme,
		hostport: u.Host,
		prefixes: prefixes,
	}, nil
}

// Check verifies that rawURL is inside the approved scheme/host and path scope.
// It is enforced on the initial request URL AND on every redirect hop.
func (a *Allowlist) Check(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("allowlist: %w: %q unparseable", ErrPathNotAllowed, rawURL)
	}
	if u.Scheme != a.scheme || u.Host != a.hostport {
		return fmt.Errorf("%w: %s://%s is not the approved target", ErrHostNotAllowed, u.Scheme, u.Host)
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	for _, p := range a.prefixes {
		if p == "/" || path == p || strings.HasPrefix(path, p+"/") {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is outside approved prefixes %v", ErrPathNotAllowed, path, a.prefixes)
}

// Host returns the approved host:port (no secrets; safe for reports).
func (a *Allowlist) Host() string { return a.hostport }

// ErrNotAuthorized is returned when the approval gate rejects a run.
var ErrNotAuthorized = errors.New("run not authorized: set authorization.accepted=true in config AND pass --iam-accept-risk (or env IAMPERF_ACCEPT_RISK=true)")

// RequireApproval enforces the two-key approval: config assertion AND a
// runtime operator confirmation (CLI flag or environment variable).
func RequireApproval(cfg config.Authorization, runtimeConfirm bool, env func(string) (string, bool)) error {
	if !cfg.Accepted {
		return fmt.Errorf("%w (config authorization.accepted=false)", ErrNotAuthorized)
	}
	envConfirm := false
	if v, ok := env("IAMPERF_ACCEPT_RISK"); ok {
		envConfirm = strings.EqualFold(v, "true") || v == "1"
	}
	if !runtimeConfirm && !envConfirm {
		return ErrNotAuthorized
	}
	return nil
}

// Observer supplies metric snapshots to the controller.
type Observer interface{ Snapshot() metrics.Snapshot }

// Controller evaluates live stop conditions against metric snapshots.
// Once tripped it stays tripped (sticky) and reports the reason.
type Controller struct {
	mu        sync.Mutex
	tripped   bool
	reason    string
	at        time.Time
	threshold config.Safety
	src       Observer
	clock     func() time.Time
}

// NewController creates a controller wired to a snapshot provider.
func NewController(threshold config.Safety, src Observer) *Controller {
	return &Controller{threshold: threshold, src: src, clock: time.Now}
}

// SetClock overrides the clock (tests).
func (c *Controller) SetClock(f func() time.Time) { c.clock = f }

// Check evaluates stop thresholds once. It returns the termination reason
// and true when a threshold is crossed.
func (c *Controller) Check() (string, bool) {
	c.mu.Lock()
	if c.tripped {
		r := c.reason
		c.mu.Unlock()
		return r, true
	}
	c.mu.Unlock()

	s := c.src.Snapshot()
	// 429: the server is telling us to stop — always respect it.
	if c.threshold.Stop429Count > 0 && s.Status429 >= c.threshold.Stop429Count {
		return c.trip(fmt.Sprintf("safety: HTTP 429 count %d reached limit %d", s.Status429, c.threshold.Stop429Count)), true
	}
	if c.threshold.Stop5xxCount > 0 && s.Status5xx >= c.threshold.Stop5xxCount {
		return c.trip(fmt.Sprintf("safety: HTTP 5xx count %d reached limit %d", s.Status5xx, c.threshold.Stop5xxCount)), true
	}
	attempts := s.AttemptsSucceeded + s.AttemptsFailed
	if attempts >= c.threshold.MinSamplesForStop {
		errRate := 100 * float64(s.AttemptsFailed) / float64(attempts)
		if c.threshold.StopErrorRatePct > 0 && errRate >= c.threshold.StopErrorRatePct {
			return c.trip(fmt.Sprintf("safety: attempt error rate %.1f%% reached limit %.1f%%", errRate, c.threshold.StopErrorRatePct)), true
		}
		if c.threshold.StopP99Millis > 0 && s.AttemptLatency.Count > 0 && s.AttemptLatency.P99 >= c.threshold.StopP99Millis {
			return c.trip(fmt.Sprintf("safety: p99 latency %.0fms reached limit %.0fms", s.AttemptLatency.P99, c.threshold.StopP99Millis)), true
		}
	}
	if c.threshold.MaxTotalRequests > 0 && s.RequestsStarted >= c.threshold.MaxTotalRequests {
		return c.trip(fmt.Sprintf("safety: total requests %d reached budget %d", s.RequestsStarted, c.threshold.MaxTotalRequests)), true
	}
	return "", false
}

func (c *Controller) trip(reason string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tripped {
		return c.reason
	}
	c.tripped = true
	c.reason = reason
	c.at = c.clock()
	return reason
}

// Tripped reports whether and why the controller stopped the run.
func (c *Controller) Tripped() (bool, string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tripped, c.reason, c.at
}
