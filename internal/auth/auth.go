// Package auth implements the evidence-derived IAM login flow and the
// per-session state model. Every behavior here traces to the HTTP Toolkit
// capture documented in docs/phase-reports/PHASE-1-EVIDENCE-AUDIT.md;
// behaviors NOT present in the evidence are treated as errors, never guessed.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/internal/transport"
)

// Failure classes (bounded set used as metric labels — never high-cardinality).
const (
	ClassTransport = "transport"
	ClassTimeout   = "timeout"
	ClassAuth      = "auth"      // server rejected the credentials/attempt (non-2xx)
	ClassProtocol  = "protocol"  // response shape violated the evidence contract
	ClassCancelled = "cancelled" // caller cancelled mid-flight
	ClassAborted   = "aborted"   // panic or internal error; attempt did not complete
)

// Credentials are resolved at run start and injected per session.
type Credentials struct {
	Username string
	Password string
}

// Session is one virtual user's isolated protocol state: dedicated client,
// cookie jar, and token fields. Sessions are never shared between VUs.
type Session struct {
	VUID    int
	Client  *http.Client
	Factory *transport.ClientFactory
	Base    *url.URL
	Creds   Credentials
	Clock   func() time.Time

	// Recorder receives every observed HTTP step (pre-redacted).
	Recorder func(metrics.StepResult)

	// Protocol state — per-session, refreshed every attempt.
	preAuthToken string
	authToken    string
	userID       string
}

// Outcome is the result of one end-to-end login attempt.
type Outcome struct {
	Success  bool
	Aborted  bool
	Class    string
	ErrMsg   string
	Duration time.Duration
}

// Flow executes one full login attempt against a session.
type Flow interface {
	Name() string
	Run(ctx context.Context, s *Session) Outcome
}

// envelope is the universal API response wrapper observed in the evidence.
type envelope struct {
	Data      json.RawMessage `json:"data"`
	Message   *string         `json:"message"`
	Timestamp int64           `json:"timestamp"`
}

// stepError is an internal carrier for classified step failures.
type stepError struct {
	Class string
	Msg   string
}

func (e *stepError) Error() string { return e.Msg }

// Redact scrubs anything token-shaped from an error string before it can
// reach logs, metrics, or reports.
func Redact(s string) string {
	return scrub(s)
}

// tokenish matches UUID-shaped values and long hex/base64ish runs.
// Implementation lives in redact.go.

// endpoints carries the configured path set for the evidence-based flow.
type endpoints struct {
	warmLoginPage    bool
	initiateLogin    string
	verifyPassword   string
	authStatus       string
	login            string
	sessionProof     string
	withSessionProof bool
	withAuthStatus   bool
	acceptLanguage   string
}

// MohaymenFlow is the flow derived from the captured evidence.
type MohaymenFlow struct {
	ep endpoints
}

// NewMohaymenFlow builds the flow from configuration values.
func NewMohaymenFlow(warmPage bool, acceptLanguage string, f config.Flow) *MohaymenFlow {
	return &MohaymenFlow{ep: endpoints{
		warmLoginPage:    warmPage,
		initiateLogin:    f.InitiateLoginPath,
		verifyPassword:   f.VerifyPasswordPath,
		authStatus:       f.AuthenticationStatusPath,
		login:            f.LoginPath,
		sessionProof:     f.SessionProofPath,
		withSessionProof: f.SessionProof,
		withAuthStatus:   f.AuthenticationStatus,
		acceptLanguage:   acceptLanguage,
	}}
}

// Name implements Flow.
func (m *MohaymenFlow) Name() string { return "mohaymen-login" }

// Run executes the evidence-derived chain:
//
//	[GET /login] → POST initiate-login → POST verifyPassword
//	→ [GET authentication-status] → POST login → [GET session proof]
//
// Success requires the x-auth-token response header AND a non-null
// data.userId on the login call (evidence step 12), plus an optional
// session-proof 200 (evidence step 13).
func (m *MohaymenFlow) Run(ctx context.Context, s *Session) (out Outcome) {
	start := s.Clock()
	defer func() {
		if r := recover(); r != nil {
			out = Outcome{Aborted: true, Class: ClassAborted,
				ErrMsg: Redact(fmt.Sprintf("internal panic: %v", r)), Duration: s.Clock().Sub(start)}
		}
	}()

	s.preAuthToken, s.authToken, s.userID = "", "", ""

	if m.ep.warmLoginPage {
		// Evidence step 1: GET /login (SPA shell). Failure here is not fatal
		// to the auth chain but is recorded; classify conservatively.
		if _, err := m.do(ctx, s, http.MethodGet, "/login", nil, "", "", "login-page"); err != nil {
			var se *stepError
			if errors.As(err, &se) && se.Class == ClassCancelled {
				return Outcome{Aborted: true, Class: ClassCancelled, ErrMsg: se.Msg, Duration: s.Clock().Sub(start)}
			}
		}
	}

	// Evidence step 9: initiate-login. Issues the pre-auth-token response header.
	resp, err := m.do(ctx, s, http.MethodPost, m.ep.initiateLogin,
		[]byte(`{"userName":`+quote(s.Creds.Username)+`}`), "", "", "initiate-login")
	if err != nil {
		return m.fail(ctx, err, start, s)
	}
	preAuth := resp.Header.Get("Pre-Auth-Token")
	if preAuth == "" {
		return m.fail(ctx, &stepError{Class: ClassProtocol,
			Msg: "initiate-login: missing pre-auth-token response header (evidence step 9)"}, start, s)
	}
	s.preAuthToken = preAuth

	// Evidence step 10: verifyPassword with Pre-Auth-Token header.
	if _, err := m.do(ctx, s, http.MethodPost, m.ep.verifyPassword,
		[]byte(`{"password":`+quote(s.Creds.Password)+`}`), preAuth, "", "verify-password"); err != nil {
		return m.fail(ctx, err, start, s)
	}

	// Evidence step 11: authentication-status (observational poll).
	if m.ep.withAuthStatus {
		if _, err := m.do(ctx, s, http.MethodGet, m.ep.authStatus, nil, preAuth, "", "authentication-status"); err != nil {
			return m.fail(ctx, err, start, s)
		}
	}

	// Evidence step 12: login establishes the session (x-auth-token header).
	resp, err = m.do(ctx, s, http.MethodPost, m.ep.login, []byte(`{}`), preAuth, "", "login")
	if err != nil {
		return m.fail(ctx, err, start, s)
	}
	authToken := resp.Header.Get("X-Auth-Token")
	if authToken == "" {
		return m.fail(ctx, &stepError{Class: ClassProtocol,
			Msg: "login: missing x-auth-token response header (evidence step 12)"}, start, s)
	}
	var env envelope
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return m.fail(ctx, &stepError{Class: ClassProtocol,
			Msg: fmt.Sprintf("login: unparseable envelope: %s", Redact(err.Error()))}, start, s)
	}
	var loginData struct {
		UserID string `json:"userId"`
		Forced bool   `json:"forced"`
	}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &loginData); err != nil {
			return m.fail(ctx, &stepError{Class: ClassProtocol,
				Msg: "login: unparseable data payload (evidence step 12)"}, start, s)
		}
	}
	if loginData.UserID == "" {
		return m.fail(ctx, &stepError{Class: ClassAuth,
			Msg: "login: success criteria not met (null userId)"}, start, s)
	}
	s.authToken = authToken
	s.userID = loginData.UserID

	// Evidence step 13: session proof.
	if m.ep.withSessionProof {
		if _, err := m.do(ctx, s, http.MethodGet, m.ep.sessionProof, nil, preAuth, authToken, "session-proof"); err != nil {
			return m.fail(ctx, err, start, s)
		}
	}

	return Outcome{Success: true, Duration: s.Clock().Sub(start)}
}

// fail converts a step failure into an Outcome, preserving cancellation.
func (m *MohaymenFlow) fail(ctx context.Context, err error, start time.Time, s *Session) Outcome {
	if ctx.Err() != nil {
		return Outcome{Aborted: true, Class: ClassCancelled,
			ErrMsg: "cancelled during login flow", Duration: s.Clock().Sub(start)}
	}
	var se *stepError
	if errors.As(err, &se) {
		return Outcome{Success: false, Class: se.Class, ErrMsg: se.Msg, Duration: s.Clock().Sub(start)}
	}
	return Outcome{Success: false, Class: ClassTransport,
		ErrMsg: Redact(err.Error()), Duration: s.Clock().Sub(start)}
}

// do performs one instrumented HTTP step with evidence-faithful headers.
func (m *MohaymenFlow) do(ctx context.Context, s *Session, method, path string, body []byte,
	preAuth, authToken, stepName string) (*transport.Response, error) {

	u := *s.Base
	u.Path = path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, &stepError{Class: ClassProtocol, Msg: fmt.Sprintf("%s: bad request: %s", stepName, Redact(err.Error()))}
	}
	// Headers observed on the captured API exchanges (steps 2–15).
	req.Header.Set("Accept", "*/*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept-Language", m.ep.acceptLanguage)
	req.Header.Set("clientTimestamp", strconv.FormatInt(s.Clock().Unix(), 10))
	req.Header.Set("Cross-Domain", "true")
	if preAuth != "" {
		req.Header.Set("Pre-Auth-Token", preAuth)
	}
	if authToken != "" {
		req.Header.Set("X-Auth-Token", authToken)
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", s.Base.Scheme+"://"+s.Base.Host)
	}
	switch stepName {
	case "initiate-login", "verify-password", "login-page":
		req.Header.Set("Referer", s.Base.String()+"/login")
	case "authentication-status", "login":
		req.Header.Set("Referer", s.Base.String()+"/auth-status")
	case "session-proof":
		req.Header.Set("Referer", s.Base.String()+"/auth-status")
	}

	resp, err := s.Factory.Do(ctx, s.Client, req)
	dur := 0 * time.Second
	status := 0
	class := ""
	msg := ""
	if resp != nil {
		dur = resp.Duration
		status = resp.StatusCode
	}
	if err != nil {
		class, msg = classifyTransportErr(ctx, err, stepName)
	} else if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Failure envelope of the real service is NOT captured in evidence
		// (Phase 1, open question #1): classify conservatively, never log body.
		class = ClassAuth
		msg = fmt.Sprintf("%s: http %d", stepName, resp.StatusCode)
	}
	if class != "" {
		if s.Recorder != nil {
			s.Recorder(metrics.StepResult{Step: stepName, Status: status, ErrClass: class, ErrMsg: Redact(msg), Duration: dur})
		}
		return resp, &stepError{Class: class, Msg: msg}
	}
	if s.Recorder != nil {
		s.Recorder(metrics.StepResult{Step: stepName, Status: status, Duration: dur})
	}
	return resp, nil
}

func classifyTransportErr(ctx context.Context, err error, step string) (class, msg string) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return ClassCancelled, step + ": cancelled"
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return ClassTimeout, fmt.Sprintf("%s: timeout", step)
	}
	return ClassTransport, fmt.Sprintf("%s: %s", step, Redact(err.Error()))
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
