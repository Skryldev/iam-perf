package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/internal/safety"
	"example.com/iam-perf/internal/testkit"
	"example.com/iam-perf/internal/transport"
)

// newSession builds a Session wired to the fixture server.
func newSession(t *testing.T, srvURL string, creds Credentials) *Session {
	t.Helper()
	cfg := config.Default()
	cfg.Target.BaseURL = srvURL
	cfg.Credentials.Username = creds.Username
	allow, err := safety.NewAllowlist(cfg.Target)
	if err != nil {
		t.Fatal(err)
	}
	col := metrics.NewCollector()
	factory, err := transport.NewFactory(cfg, allow, col)
	if err != nil {
		t.Fatal(err)
	}
	client, err := factory.NewClient(1)
	if err != nil {
		t.Fatal(err)
	}
	return &Session{
		VUID:    1,
		Client:  client,
		Factory: factory,
		Base:    factory.BaseURL(),
		Creds:   creds,
		Clock:   time.Now,
		Recorder: func(sr metrics.StepResult) {
			col.RecordStep(sr)
		},
	}
}

func flowFor() *MohaymenFlow {
	return NewMohaymenFlow(false, "fa-IR", config.Default().Flow)
}

func TestMohaymenFlow_Success(t *testing.T) {
	srv, st := testkit.NewIAM(t, testkit.Config{Track: true, RequirePreAuth: true})
	s := newSession(t, srv.URL, Credentials{Username: "SRETEST", Password: "test-password-only"})

	out := flowFor().Run(context.Background(), s)
	if !out.Success {
		t.Fatalf("expected success, got class=%q msg=%q", out.Class, out.ErrMsg)
	}
	if out.Aborted {
		t.Fatal("unexpected abort")
	}
	// Evidence contract: exact request sequence.
	if got := st.RequestCount("POST", "/api/v1/auth/initiate-login"); got != 1 {
		t.Errorf("initiate-login called %d times, want 1", got)
	}
	if got := st.RequestCount("POST", "/api/v1/auth/verifyPassword"); got != 1 {
		t.Errorf("verifyPassword called %d times, want 1 (no retry allowed)", got)
	}
	if got := st.RequestCount("GET", "/api/v1/auth/authentication-status"); got != 1 {
		t.Errorf("authentication-status called %d times, want 1", got)
	}
	if got := st.RequestCount("POST", "/api/v1/auth/login"); got != 1 {
		t.Errorf("login called %d times, want 1", got)
	}
	if got := st.RequestCount("GET", "/api/v1/apps/users/dashboardPermissions"); got != 1 {
		t.Errorf("session proof called %d times, want 1", got)
	}
	// Token correlation: verify used the pre-auth token issued by initiate.
	if n := len(st.InitiateUsers()); n != 1 || st.InitiateUsers()[0] != "SRETEST" {
		t.Errorf("initiate body users = %v", st.InitiateUsers())
	}
	if markers := st.VerifyBodyMarkers(); len(markers) != 1 || markers[0] != "len=18" {
		t.Errorf("verify body markers = %v (want len=18)", markers)
	}
}

func TestMohaymenFlow_WrongPassword(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{RequirePreAuth: true})
	s := newSession(t, srv.URL, Credentials{Username: "SRETEST", Password: "definitely-wrong"})

	out := flowFor().Run(context.Background(), s)
	if out.Success {
		t.Fatal("expected failure for wrong password")
	}
	if out.Class != ClassAuth {
		t.Fatalf("class = %q, want auth", out.Class)
	}
	// Redaction: no password material in the error.
	if strings.Contains(out.ErrMsg, "definitely-wrong") {
		t.Fatalf("error message leaks password: %q", out.ErrMsg)
	}
}

func TestMohaymenFlow_MissingPreAuthToken(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{OmitPreAuthToken: true})
	s := newSession(t, srv.URL, Credentials{Username: "SRETEST", Password: "test-password-only"})

	out := flowFor().Run(context.Background(), s)
	if out.Success || out.Class != ClassProtocol {
		t.Fatalf("class=%q msg=%q, want protocol failure", out.Class, out.ErrMsg)
	}
	if !strings.Contains(out.ErrMsg, "pre-auth-token") {
		t.Fatalf("error should mention missing token, got %q", out.ErrMsg)
	}
}

func TestMohaymenFlow_MissingAuthToken(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{OmitAuthToken: true})
	s := newSession(t, srv.URL, Credentials{Username: "SRETEST", Password: "test-password-only"})

	out := flowFor().Run(context.Background(), s)
	if out.Success || out.Class != ClassProtocol {
		t.Fatalf("class=%q msg=%q, want protocol failure", out.Class, out.ErrMsg)
	}
}

func TestMohaymenFlow_LoginNonOK(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{LoginStatus: http.StatusUnauthorized})
	s := newSession(t, srv.URL, Credentials{Username: "SRETEST", Password: "test-password-only"})

	out := flowFor().Run(context.Background(), s)
	if out.Success || out.Class != ClassAuth {
		t.Fatalf("class=%q msg=%q, want auth failure", out.Class, out.ErrMsg)
	}
}

func TestMohaymenFlow_ServerError(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{InitiateStatus: http.StatusInternalServerError})
	s := newSession(t, srv.URL, Credentials{Username: "SRETEST", Password: "test-password-only"})

	out := flowFor().Run(context.Background(), s)
	if out.Success || out.Class != ClassAuth {
		t.Fatalf("class=%q, want auth failure", out.Class)
	}
	if !strings.Contains(out.ErrMsg, "500") {
		t.Fatalf("error should mention status 500, got %q", out.ErrMsg)
	}
}

func TestMohaymenFlow_Cancellation(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	s := newSession(t, srv.URL, Credentials{Username: "SRETEST", Password: "test-password-only"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	out := flowFor().Run(ctx, s)
	if !out.Aborted || out.Class != ClassCancelled {
		t.Fatalf("class=%q aborted=%v, want cancelled abort", out.Class, out.Aborted)
	}
}

func TestMohaymenFlow_AllowlistBlocksRedirectEscape(t *testing.T) {
	// The evidence shows no redirects; verify the guard anyway: any redirect
	// to a non-approved host must abort the chain.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/initiate-login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/steal", http.StatusFound)
	})
	inner := httptest.NewServer(mux)
	defer inner.Close()

	s := newSession(t, inner.URL, Credentials{Username: "SRETEST", Password: "x"})
	out := flowFor().Run(context.Background(), s)
	if out.Success {
		t.Fatal("redirect escape must not succeed")
	}
}

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"token 80ff55a0-afb0-416c-a735-ab027592738d leaked", "token [REDACTED] leaked"},
		{"jwt eyJhbGciOi.abc.def here", "jwt [REDACTED] here"},
		{"hex aabbccddeeff00112233445566778899 end", "hex [REDACTED] end"},
		{"no secrets here", "no secrets here"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProbeFlow(t *testing.T) {
	srv, _ := testkit.NewIAM(t, testkit.Config{})
	s := newSession(t, srv.URL, Credentials{})
	out := NewProbeFlow("/login").Run(context.Background(), s)
	if !out.Success {
		t.Fatalf("probe should succeed: %q", out.ErrMsg)
	}
	if NewProbeFlow("/login").Name() != "transport-probe" {
		t.Fatal("probe flow name")
	}
}
