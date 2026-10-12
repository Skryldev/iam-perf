package metrics

import (
	"fmt"
	"io"
	"strings"
)

// WritePrometheus renders a snapshot in the Prometheus text exposition
// format (v0.0.4). Bounded labels only: step, class.
func WritePrometheus(w io.Writer, s Snapshot) error {
	wr := func(line string) {
		fmt.Fprintln(w, line)
	}
	wr("# HELP iamperf_attempts_started Total login attempts started.")
	wr("# TYPE iamperf_attempts_started counter")
	wr(fmt.Sprintf("iamperf_attempts_started %d", s.AttemptsStarted))
	wr("# HELP iamperf_attempts_succeeded Successful login transactions.")
	wr("# TYPE iamperf_attempts_succeeded counter")
	wr(fmt.Sprintf("iamperf_attempts_succeeded %d", s.AttemptsSucceeded))
	wr("# HELP iamperf_attempts_failed Failed login transactions.")
	wr("# TYPE iamperf_attempts_failed counter")
	wr(fmt.Sprintf("iamperf_attempts_failed %d", s.AttemptsFailed))
	wr("# HELP iamperf_attempts_aborted Aborted/cancelled login transactions.")
	wr("# TYPE iamperf_attempts_aborted counter")
	wr(fmt.Sprintf("iamperf_attempts_aborted %d", s.AttemptsAborted))
	wr("# HELP iamperf_requests_started HTTP requests started.")
	wr("# TYPE iamperf_requests_started counter")
	wr(fmt.Sprintf("iamperf_requests_started %d", s.RequestsStarted))
	wr("# HELP iamperf_requests_completed HTTP requests completed.")
	wr("# TYPE iamperf_requests_completed counter")
	wr(fmt.Sprintf("iamperf_requests_completed %d", s.RequestsCompleted))
	wr("# HELP iamperf_requests_failed HTTP requests failed.")
	wr("# TYPE iamperf_requests_failed counter")
	wr(fmt.Sprintf("iamperf_requests_failed %d", s.RequestsFailed))
	wr("# HELP iamperf_status_total HTTP responses by status code.")
	wr("# TYPE iamperf_status_total counter")
	for code, n := range s.StatusDist {
		wr(fmt.Sprintf("iamperf_status_total{code=%q} %d", sanitizeLabel(code), n))
	}
	wr("# HELP iamperf_429_total HTTP 429 responses observed.")
	wr("# TYPE iamperf_429_total counter")
	wr(fmt.Sprintf("iamperf_429_total %d", s.Status429))
	wr("# HELP iamperf_5xx_total HTTP 5xx responses observed.")
	wr("# TYPE iamperf_5xx_total counter")
	wr(fmt.Sprintf("iamperf_5xx_total %d", s.Status5xx))
	wr("# HELP iamperf_backpressure_dropped Arrivals dropped on a full queue.")
	wr("# TYPE iamperf_backpressure_dropped counter")
	wr(fmt.Sprintf("iamperf_backpressure_dropped %d", s.BackpressureDrops))

	writeHist := func(name, help string, h LatencyStats) {
		wr("# HELP " + name + " " + help)
		wr("# TYPE " + name + " summary")
		for _, q := range []struct {
			p string
			v float64
		}{{"0.5", h.P50}, {"0.9", h.P90}, {"0.95", h.P95}, {"0.99", h.P99}} {
			wr(fmt.Sprintf("%s{quantile=%q} %g", name, q.p, q.v/1000))
		}
		wr(fmt.Sprintf("%s_sum %g", name, h.Mean*float64(h.Count)/1000))
		wr(fmt.Sprintf("%s_count %d", name, h.Count))
	}
	writeHist("iamperf_login_duration_seconds", "End-to-end login transaction duration.", s.AttemptLatency)
	for _, st := range s.Steps {
		writeHist("iamperf_step_duration_seconds", "Per-step HTTP duration.", st.Latency)
	}
	wr("# HELP iamperf_errors_total Failures by bounded class.")
	wr("# TYPE iamperf_errors_total counter")
	for cls, n := range s.ErrorClasses {
		wr(fmt.Sprintf("iamperf_errors_total{class=%q} %d", sanitizeLabel(cls), n))
	}
	return nil
}

func sanitizeLabel(v string) string {
	return strings.NewReplacer("\"", "", "\\", "", "\n", "").Replace(v)
}
