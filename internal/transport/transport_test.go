package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/internal/safety"
)

func factoryFor(t *testing.T, baseURL string, mutate func(*config.Config)) (*ClientFactory, *metrics.Collector) {
	t.Helper()
	cfg := config.Default()
	cfg.Target.BaseURL = baseURL
	cfg.Transport.RequestTimeout = config.Duration{Duration: 2 * time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	allow, err := safety.NewAllowlist(cfg.Target)
	if err != nil {
		t.Fatal(err)
	}
	col := metrics.NewCollector()
	f, err := NewFactory(cfg, allow, col)
	if err != nil {
		t.Fatal(err)
	}
	return f, col
}

func TestDo_BasicAndReuse(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	f, col := factoryFor(t, srv.URL, nil)
	client, err := f.NewClient(1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/login", nil)
	resp1, err := f.Do(ctx, client, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp1.StatusCode != 200 || string(resp1.Body) != "hello" {
		t.Fatalf("resp = %d %q", resp1.StatusCode, resp1.Body)
	}
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/login", nil)
	resp2, err := f.Do(ctx, client, req2)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d", hits)
	}
	snap := col.Snapshot()
	if snap.ConnReuse < 1 {
		t.Errorf("expected at least one reused connection, snap = %+v reuse=%d new=%d", snap, snap.ConnReuse, snap.NewConns)
	}
	if resp2.Trace.TTFB <= 0 {
		t.Errorf("TTFB not observed: %v", resp2.Trace.TTFB)
	}
}

func TestDo_BoundedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 1<<21)) // 2 MiB
	}))
	defer srv.Close()
	f, _ := factoryFor(t, srv.URL, func(c *config.Config) {
		c.Transport.MaxResponseBodyBytes = 1024
	})
	client, _ := f.NewClient(1)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/login", nil)
	_, err := f.Do(context.Background(), client, req)
	if err == nil || !strings.Contains(err.Error(), "max_response_body_bytes") {
		t.Fatalf("expected bounded-body error, got %v", err)
	}
}

func TestDo_RedirectPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loop" {
			http.Redirect(w, r, "/api/v1/loop", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	f, col := factoryFor(t, srv.URL, func(c *config.Config) { c.Transport.MaxRedirects = 3 })
	client, _ := f.NewClient(1)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/loop", nil)
	_, err := f.Do(context.Background(), client, req)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("want ErrTooManyRedirects, got %v", err)
	}
	if col.Snapshot().Redirects != 3 {
		t.Errorf("redirects recorded = %d, want 3", col.Snapshot().Redirects)
	}
}

func TestDo_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()
	f, _ := factoryFor(t, srv.URL, func(c *config.Config) {
		c.Transport.RequestTimeout = config.Duration{Duration: 50 * time.Millisecond}
	})
	client, _ := f.NewClient(1)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/login", nil)
	_, err := f.Do(context.Background(), client, req)
	if err == nil {
		t.Fatal("expected timeout")
	}
	var ne interface{ Timeout() bool }
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("error must be a timeout, got %v", err)
	}
}

func TestDo_AllowlistEnforcedAtRequestTime(t *testing.T) {
	// The factory's allowlist pins to the ORIGINAL configured target; a
	// client pointed at a different server must be rejected.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	f, _ := factoryFor(t, "http://127.0.0.1:1", nil) // unreachable config target
	client, _ := f.NewClient(1)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/login", nil)
	_, err := f.Do(context.Background(), client, req)
	if !errors.Is(err, safety.ErrHostNotAllowed) {
		t.Fatalf("want ErrHostNotAllowed, got %v", err)
	}
}

func TestDo_CookieIsolation(t *testing.T) {
	var setCookies int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sid"); err != nil || c == nil {
			setCookies++
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "v1"})
			_, _ = w.Write([]byte("set"))
			return
		}
		_, _ = w.Write([]byte("seen"))
	}))
	defer srv.Close()
	f, _ := factoryFor(t, srv.URL, nil)
	c1, _ := f.NewClient(1)
	c2, _ := f.NewClient(2)
	ctx := context.Background()
	for _, cl := range []*http.Client{c1, c2} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/login", nil)
		r1, err := f.Do(ctx, cl, req)
		if err != nil || string(r1.Body) != "set" {
			t.Fatalf("first visit: %v %q", err, r1.Body)
		}
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/login", nil)
		r2, err := f.Do(ctx, cl, req2)
		if err != nil || string(r2.Body) != "seen" {
			t.Fatalf("second visit: %v %q", err, r2.Body)
		}
	}
	// Each client saw its own jar: the server issued the cookie once per client.
	if setCookies != 2 {
		t.Errorf("Set-Cookie count = %d, want 2 (jars must be isolated)", setCookies)
	}
}

func TestFactory_TLSVerificationCannotBeDisabled(t *testing.T) {
	// Local HTTPS server with a self-signed cert NOT in the trust store.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	f, _ := factoryFor(t, srv.URL, nil)
	client, _ := f.NewClient(1)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/login", nil)
	if _, err := f.Do(context.Background(), client, req); err == nil {
		t.Fatal("untrusted TLS must fail: verification cannot be disabled")
	}
}

func TestFactory_TrustedCAs(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	// Trust the server's own cert via the CA file option (lab-PKI simulation).
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	// Write PEM via the exported helper path: use the cert DER -> PEM.
	pemBytes := certToPEM(srv.Certificate().Raw)
	p := t.TempDir() + "/ca.pem"
	if err := writeFile(p, pemBytes); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Target.BaseURL = srv.URL
	cfg.Transport.TrustedCAsFile = p
	allow, err := safety.NewAllowlist(cfg.Target)
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFactory(cfg, allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := f.NewClient(1)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/login", nil)
	if _, err := f.Do(context.Background(), client, req); err != nil {
		t.Fatalf("trusted CA must verify: %v", err)
	}
}

func TestTransportConfig_TLSFields(t *testing.T) {
	f, _ := factoryFor(t, "https://x.example.ir", nil)
	tr := f.newTransport(1)
	tlsCfg := tr.TLSClientConfig
	if tlsCfg == nil || tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Fatal("TLS 1.2 minimum must be enforced")
	}
	// InsecureSkipVerify must remain at its zero value (false).
	if tlsCfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must never be set")
	}
}
