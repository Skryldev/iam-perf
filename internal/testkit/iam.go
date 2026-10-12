// Package testkit provides deterministic offline HTTP fixtures that simulate
// the IAM login protocol captured in the evidence. NO test contacts the real
// service; everything runs against local httptest servers.
package testkit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Config tunes fixture behavior per scenario.
type Config struct {
	// ValidUser / ValidPassword are the only accepted credentials.
	ValidUser     string
	ValidPassword string
	// FailVerifyWith makes verifyPassword return this status (e.g. 401).
	FailVerifyWith int
	// InitiateStatus / LoginStatus override those endpoints' status codes.
	InitiateStatus int
	LoginStatus    int
	// OmitPreAuthToken / OmitAuthToken break the protocol contract on purpose.
	OmitPreAuthToken bool
	OmitAuthToken    bool
	// RequirePreAuth enforces the Pre-Auth-Token header on verify/login.
	RequirePreAuth bool
	// Track records per-attempt protocol facts for assertions.
	Track bool
	// SlowMs delays every handler response (deterministic saturation tests).
	SlowMs int
}

// State collects observed fixture facts (guarded for concurrency).
type State struct {
	mu            sync.Mutex
	preAuthTokens map[string]int
	authTokens    map[string]int
	verifyBodies  []string
	initiateUsers []string
	requests      []string
}

// DistinctPreAuthTokens returns the number of unique pre-auth tokens seen.
func (s *State) DistinctPreAuthTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.preAuthTokens)
}

// MaxPreAuthUses returns the highest single-token usage count.
func (s *State) MaxPreAuthUses() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	max := 0
	for _, n := range s.preAuthTokens {
		if n > max {
			max = n
		}
	}
	return max
}

// PreAuthUses reports how many times a pre-auth token was seen on requests.
func (s *State) PreAuthUses(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preAuthTokens[token]
}

// AuthUses reports how many times an auth token was seen on requests.
func (s *State) AuthUses(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authTokens[token]
}

// VerifyBodyMarkers returns captured verifyPassword body length markers
// (never the secret itself).
func (s *State) VerifyBodyMarkers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.verifyBodies...)
}

// InitiateUsers returns captured initiate-login usernames.
func (s *State) InitiateUsers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.initiateUsers...)
}

// RequestCount counts requests to "METHOD path".
func (s *State) RequestCount(method, path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := method + " " + path
	n := 0
	for _, r := range s.requests {
		if r == want {
			n++
		}
	}
	return n
}

func newState() *State {
	return &State{
		preAuthTokens: map[string]int{},
		authTokens:    map[string]int{},
	}
}

// NewIAM starts a fixture IAM server replicating the evidence chain.
func NewIAM(t *testing.T, cfg Config) (*httptest.Server, *State) {
	t.Helper()
	if cfg.ValidUser == "" {
		cfg.ValidUser = "SRETEST"
	}
	if cfg.ValidPassword == "" {
		cfg.ValidPassword = "test-password-only"
	}
	st := newState()

	record := func(r *http.Request) {
		if !cfg.Track {
			return
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		st.requests = append(st.requests, r.Method+" "+r.URL.Path)
		if tok := r.Header.Get("Pre-Auth-Token"); tok != "" {
			st.preAuthTokens[tok]++
		}
		if tok := r.Header.Get("X-Auth-Token"); tok != "" {
			st.authTokens[tok]++
		}
	}

	slow := func() {
		if cfg.SlowMs > 0 {
			time.Sleep(time.Duration(cfg.SlowMs) * time.Millisecond)
		}
	}

	writeEnv := func(w http.ResponseWriter, status int, data string) {
		slow()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		body := fmt.Sprintf(`{"data":%s,"message":null,"timestamp":0}`, data)
		if data == "" {
			body = `{"data":null,"message":null,"timestamp":0}`
		}
		_, _ = w.Write([]byte(body))
	}

	newToken := func() string {
		var b [16]byte
		_, _ = rand.Read(b[:])
		b[6] = (b[6] & 0x0f) | 0x40
		return fmt.Sprintf("%s-%s-%s-%s-%s",
			hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
			hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]),
			hex.EncodeToString(b[10:16]))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>login page</body></html>"))
	})

	mux.HandleFunc("/api/v1/auth/initiate-login", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var body struct {
			UserName string `json:"userName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeEnv(w, http.StatusBadRequest, "")
			return
		}
		if cfg.Track {
			st.mu.Lock()
			st.initiateUsers = append(st.initiateUsers, body.UserName)
			st.mu.Unlock()
		}
		status := cfg.InitiateStatus
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			writeEnv(w, status, "")
			return
		}
		tok := newToken()
		if cfg.Track {
			st.mu.Lock()
			st.preAuthTokens[tok] = 0
			st.mu.Unlock()
		}
		if !cfg.OmitPreAuthToken {
			w.Header().Set("Pre-Auth-Token", tok)
		}
		writeEnv(w, http.StatusOK, "")
	})

	mux.HandleFunc("/api/v1/auth/verifyPassword", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if cfg.RequirePreAuth && r.Header.Get("Pre-Auth-Token") == "" {
			writeEnv(w, http.StatusForbidden, "")
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeEnv(w, http.StatusBadRequest, "")
			return
		}
		if cfg.Track {
			st.mu.Lock()
			st.verifyBodies = append(st.verifyBodies, fmt.Sprintf("len=%d", len(body.Password)))
			st.mu.Unlock()
		}
		if body.Password != cfg.ValidPassword {
			writeEnv(w, http.StatusUnauthorized, "")
			return
		}
		if cfg.FailVerifyWith != 0 {
			writeEnv(w, cfg.FailVerifyWith, "")
			return
		}
		writeEnv(w, http.StatusOK, "")
	})

	mux.HandleFunc("/api/v1/auth/authentication-status", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeEnv(w, http.StatusOK, "")
	})

	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if cfg.RequirePreAuth && r.Header.Get("Pre-Auth-Token") == "" {
			writeEnv(w, http.StatusForbidden, "")
			return
		}
		status := cfg.LoginStatus
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			writeEnv(w, status, "")
			return
		}
		tok := newToken()
		if cfg.Track {
			st.mu.Lock()
			st.authTokens[tok] = 0
			st.mu.Unlock()
		}
		if !cfg.OmitAuthToken {
			w.Header().Set("X-Auth-Token", tok)
		}
		writeEnv(w, http.StatusOK, `{"userId":"2a5c0e49-1acf-474c-a6e9-0c7210a29bbe","forced":false}`)
	})

	mux.HandleFunc("/api/v1/apps/users/dashboardPermissions", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if cfg.RequirePreAuth && r.Header.Get("X-Auth-Token") == "" {
			writeEnv(w, http.StatusForbidden, "")
			return
		}
		writeEnv(w, http.StatusOK, `["postAuth:authenticated"]`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}
