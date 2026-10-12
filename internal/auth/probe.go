package auth

import (
	"context"
	"net/http"
	"strconv"

	"example.com/iam-perf/internal/metrics"
)

// ProbeFlow is a non-authenticating flow: one GET to the login page per
// attempt. It supports transport/protocol validation scenarios when valid
// credentials or an authorized authentication environment are unavailable
// (spec §6). It performs no authentication and must never be interpreted
// as a successful login.
type ProbeFlow struct {
	Path string
}

// NewProbeFlow builds the probe flow (default path /login).
func NewProbeFlow(path string) *ProbeFlow {
	if path == "" {
		path = "/login"
	}
	return &ProbeFlow{Path: path}
}

// Name implements Flow.
func (p *ProbeFlow) Name() string { return "transport-probe" }

// Run performs one GET and reports success purely at HTTP level.
func (p *ProbeFlow) Run(ctx context.Context, s *Session) (out Outcome) {
	start := s.Clock()
	defer func() {
		if r := recover(); r != nil {
			out = Outcome{Aborted: true, Class: ClassAborted,
				ErrMsg: Redact("internal panic"), Duration: s.Clock().Sub(start)}
		}
	}()

	u := *s.Base
	u.Path = p.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Outcome{Class: ClassProtocol, ErrMsg: "probe: bad request", Duration: s.Clock().Sub(start)}
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")
	req.Header.Set("Accept-Language", "fa-IR")
	req.Header.Set("clientTimestamp", strconv.FormatInt(s.Clock().Unix(), 10))

	resp, err := s.Factory.Do(ctx, s.Client, req)
	if resp != nil && s.Recorder != nil {
		sr := metrics.StepResult{Step: "probe", Status: resp.StatusCode, Duration: resp.Duration}
		if err != nil {
			sr.ErrClass, sr.ErrMsg = classifyTransportErr(ctx, err, "probe")
		}
		s.Recorder(sr)
	}
	if err != nil {
		class, msg := classifyTransportErr(ctx, err, "probe")
		if class == ClassCancelled {
			return Outcome{Aborted: true, Class: class, ErrMsg: msg, Duration: s.Clock().Sub(start)}
		}
		return Outcome{Class: class, ErrMsg: msg, Duration: s.Clock().Sub(start)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Outcome{Class: ClassAuth, ErrMsg: "probe: http " + strconv.Itoa(resp.StatusCode), Duration: s.Clock().Sub(start)}
	}
	return Outcome{Success: true, Duration: s.Clock().Sub(start)}
}
