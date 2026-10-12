package e2e

import (
	"context"
	"testing"
	"time"

	"example.com/iam-perf/internal/auth"
	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/internal/safety"
	"example.com/iam-perf/internal/testkit"
	"example.com/iam-perf/internal/transport"
)

// BenchmarkLoginFlow measures the generator's per-attempt overhead against a
// local fixture. It bounds the CPU cost of the framework itself; it says
// NOTHING about the real target's capacity (see README §Interpreting results).
func BenchmarkLoginFlow(b *testing.B) {
	srv, _ := testkit.NewIAM(&testing.T{}, testkit.Config{})
	cfg := config.Default()
	cfg.Target.BaseURL = srv.URL

	allow, err := safety.NewAllowlist(cfg.Target)
	if err != nil {
		b.Fatal(err)
	}
	col := metrics.NewCollector()
	factory, err := transport.NewFactory(cfg, allow, col)
	if err != nil {
		b.Fatal(err)
	}
	client, err := factory.NewClient(1)
	if err != nil {
		b.Fatal(err)
	}
	flow := auth.NewMohaymenFlow(false, "fa-IR", cfg.Flow)
	sess := &auth.Session{
		VUID: 1, Client: client, Factory: factory, Base: factory.BaseURL(),
		Creds:    auth.Credentials{Username: "SRETEST", Password: "test-password-only"},
		Clock:    time.Now,
		Recorder: func(sr metrics.StepResult) { col.RecordStep(sr) },
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := flow.Run(ctx, sess)
		if !out.Success {
			b.Fatalf("attempt failed: %s", out.ErrMsg)
		}
	}
}

// BenchmarkMetricsRecord measures collector overhead per step record.
func BenchmarkMetricsRecord(b *testing.B) {
	c := metrics.NewCollector()
	d := 2 * time.Millisecond
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.RecordStep(metrics.StepResult{Step: "verify-password", Status: 200, Duration: d})
	}
}

// BenchmarkHistogramQuantile measures percentile computation on a full histogram.
func BenchmarkHistogramQuantile(b *testing.B) {
	h := metrics.NewHistogram()
	for i := 0; i < 100000; i++ {
		h.Record(time.Duration(i%9000+100) * time.Millisecond)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Quantile(0.99)
	}
}
