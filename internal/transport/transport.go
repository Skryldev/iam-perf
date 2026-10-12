// Package transport builds per-virtual-user HTTP clients with explicit,
// internally-consistent connection-pool, timeout, redirect, and TLS settings,
// plus httptrace hooks feeding the metrics collector.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"os"
	"time"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/internal/safety"
)

// ErrTooManyRedirects is returned when a redirect chain exceeds the limit.
var ErrTooManyRedirects = errors.New("transport: too many redirects")

// TraceInfo carries observable per-request transport timings.
type TraceInfo struct {
	DNS    time.Duration // 0 when connection was reused
	TCP    time.Duration
	TLS    time.Duration
	TTFB   time.Duration
	Reused bool
}

// ClientFactory creates an isolated client per virtual user.
type ClientFactory struct {
	cfg       config.Config
	allow     *safety.Allowlist
	collector *metrics.Collector
	rootCAs   *x509.CertPool
	proxy     func(*http.Request) (*url.URL, error)
	baseURL   *url.URL
}

// NewFactory validates transport settings and prepares shared resources
// (CA pool, proxy). It performs no network I/O.
func NewFactory(cfg config.Config, allow *safety.Allowlist, c *metrics.Collector) (*ClientFactory, error) {
	base, err := url.Parse(cfg.Target.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("transport: bad base url: %w", err)
	}
	f := &ClientFactory{cfg: cfg, allow: allow, collector: c, baseURL: base}
	if cfg.Transport.TrustedCAsFile != "" {
		pem, err := os.ReadFile(cfg.Transport.TrustedCAsFile) // #nosec G304 -- operator-provided path
		if err != nil {
			return nil, fmt.Errorf("transport: read trusted_cas_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("transport: trusted_cas_file %q contains no valid PEM certificates", cfg.Transport.TrustedCAsFile)
		}
		f.rootCAs = pool
	}
	if cfg.Transport.ProxyURL != "" {
		pu, err := url.Parse(cfg.Transport.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("transport: bad proxy_url: %w", err)
		}
		f.proxy = http.ProxyURL(pu)
	}
	return f, nil
}

// NewClient returns a fully isolated http.Client for one virtual user:
// own Transport, own CookieJar, own redirect/trace wiring.
func (f *ClientFactory) NewClient(vuID int) (*http.Client, error) {
	t := f.newTransport(vuID)
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("transport: cookie jar: %w", err)
	}
	client := &http.Client{
		Transport: t,
		Jar:       jar,
		Timeout:   f.cfg.Transport.RequestTimeout.Duration,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// via holds already-followed hops; following exactly MaxRedirects
			// hops means erroring when len(via) exceeds the limit.
			if len(via) > f.cfg.Transport.MaxRedirects {
				return fmt.Errorf("%w: limit %d", ErrTooManyRedirects, f.cfg.Transport.MaxRedirects)
			}
			// Every redirect hop must stay inside the approved scope.
			if err := f.allow.Check(req.URL.String()); err != nil {
				return err
			}
			if f.collector != nil {
				f.collector.MarkRedirect()
			}
			return nil
		},
	}
	return client, nil
}

func (f *ClientFactory) newTransport(vuID int) *http.Transport {
	tc := f.cfg.Transport
	dialer := &net.Dialer{
		Timeout:   tc.DialTimeout.Duration,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy:                 f.proxy,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          max(1, tc.MaxIdleConns),
		MaxIdleConnsPerHost:   max(1, tc.MaxIdleConnsPerHost),
		MaxConnsPerHost:       tc.MaxConnsPerHost,
		IdleConnTimeout:       tc.IdleConnTimeout.Duration,
		TLSHandshakeTimeout:   tc.TLSHandshakeTimeout.Duration,
		ResponseHeaderTimeout: tc.ResponseHeaderTimeout.Duration,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    f.rootCAs,
			// NOTE: InsecureSkipVerify intentionally ABSENT. TLS verification
			// cannot be disabled through configuration (ADR in docs/ARCHITECTURE.md).
		},
	}
}

// BaseURL returns the parsed target base URL.
func (f *ClientFactory) BaseURL() *url.URL { return f.baseURL }

// TracedRequest executes req with bounded body reads, allowlist enforcement,
// httptrace instrumentation, and guaranteed body closure. It returns the
// response with the body ALREADY read (bounded) and closed, plus trace info.
// This avoids response-body leaks under load and gives callers deterministic
// semantics.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte // bounded by MaxResponseBodyBytes
	Trailer    http.Header
	Trace      TraceInfo
	Duration   time.Duration
}

// ErrTargetNotAllowed wraps safety allowlist violations at request time.
var ErrTargetNotAllowed = safety.ErrHostNotAllowed

var errBodyTooLarge = errors.New("transport: response body exceeds max_response_body_bytes")

// Do sends req through the client with full instrumentation.
func (f *ClientFactory) Do(ctx context.Context, client *http.Client, req *http.Request) (*Response, error) {
	if err := f.allow.Check(req.URL.String()); err != nil {
		return nil, err
	}
	var ti TraceInfo
	var ttfbStart, ttfbEnd time.Time

	start := time.Now()
	rec := &traceRecorder{}
	req = req.WithContext(httptrace.WithClientTrace(ctx, rec.trace(&ti, &ttfbStart, &ttfbEnd)))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, f.cfg.Transport.MaxResponseBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("transport: reading body: %w", err)
	}
	if int64(len(body)) > f.cfg.Transport.MaxResponseBodyBytes {
		return nil, errBodyTooLarge
	}

	if f.collector != nil {
		if ti.Reused {
			f.collector.MarkConnReuse(true)
		} else {
			f.collector.MarkConnReuse(false)
		}
		if ti.DNS > 0 {
			f.collector.RecordDNS(ti.DNS)
		}
		if ti.TCP > 0 {
			f.collector.RecordTCP(ti.TCP)
		}
		if ti.TLS > 0 {
			f.collector.RecordTLS(ti.TLS)
		}
		if !ttfbStart.IsZero() && !ttfbEnd.IsZero() {
			ti.TTFB = ttfbEnd.Sub(ttfbStart)
			f.collector.RecordTTFB(ti.TTFB)
		}
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       body,
		Trailer:    resp.Trailer,
		Trace:      ti,
		Duration:   time.Since(start),
	}, nil
}

// traceRecorder records precise httptrace stage timings.
type traceRecorder struct {
	dnsStart, dnsDone   time.Time
	connStart, connDone time.Time
	tlsStart, tlsDone   time.Time
	reqWriteDone        time.Time
	gotFirstByte        time.Time
}

func (r *traceRecorder) trace(ti *TraceInfo, ttfbStart, ttfbEnd *time.Time) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn:  func(c httptrace.GotConnInfo) { ti.Reused = c.Reused },
		DNSStart: func(httptrace.DNSStartInfo) { r.dnsStart = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			r.dnsDone = time.Now()
			ti.DNS = r.dnsDone.Sub(r.dnsStart)
		},
		ConnectStart: func(string, string) { r.connStart = time.Now() },
		ConnectDone: func(network, addr string, err error) {
			r.connDone = time.Now()
			ti.TCP = r.connDone.Sub(r.connStart)
			if err != nil {
				ti.TCP = 0
			}
		},
		TLSHandshakeStart: func() { r.tlsStart = time.Now() },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			r.tlsDone = time.Now()
			ti.TLS = r.tlsDone.Sub(r.tlsStart)
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			r.reqWriteDone = time.Now()
			*ttfbStart = r.reqWriteDone
		},
		GotFirstResponseByte: func() {
			r.gotFirstByte = time.Now()
			*ttfbEnd = r.gotFirstByte
		},
	}
}
