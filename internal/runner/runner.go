// Package runner executes the workload: virtual-user lifecycle, closed and
// open execution models, ramp-up/down, warm-up, bounded queues, stop-condition
// watching, and graceful termination.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"example.com/iam-perf/internal/auth"
	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/internal/safety"
	"example.com/iam-perf/internal/transport"
)

// Termination reasons reported by Run.
const (
	TerminationCompleted = "completed"
	TerminationSafety    = "safety-stop"
	TerminationCancelled = "cancelled"
)

// Options wires the runner to its dependencies.
type Options struct {
	Cfg       config.Config
	Flow      auth.Flow
	Factory   *transport.ClientFactory
	Collector *metrics.Collector
	Allow     *safety.Allowlist
	Creds     auth.Credentials
	Logger    *slog.Logger
	Clock     func() time.Time
	// WatchInterval is the stop-condition poll period (default 250ms).
	WatchInterval time.Duration
	// DrainGrace bounds the wait for in-flight attempts after cancellation.
	DrainGrace time.Duration
}

// Result reports what happened during a run.
type Result struct {
	Snapshot     metrics.Snapshot
	StartedAt    time.Time
	EndedAt      time.Time
	Termination  string
	SafetyReason string
}

// Runner executes one configured workload.
type Runner struct {
	opts   Options
	ctrl   *safety.Controller
	logger *slog.Logger
	clock  func() time.Time
}

// New validates options and returns a Runner.
func New(o Options) (*Runner, error) {
	if o.Flow == nil {
		return nil, fmt.Errorf("runner: flow is required")
	}
	if o.Factory == nil || o.Collector == nil || o.Allow == nil {
		return nil, fmt.Errorf("runner: factory, collector and allowlist are required")
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.WatchInterval <= 0 {
		o.WatchInterval = 250 * time.Millisecond
	}
	if o.DrainGrace <= 0 {
		o.DrainGrace = 10 * time.Second
	}
	return &Runner{opts: o, logger: o.Logger, clock: o.Clock}, nil
}

// Run executes the workload until the scenario deadline, a safety stop, or
// context cancellation. In-flight attempts are drained with a bounded grace
// period. The returned Result always carries a usable snapshot.
func (r *Runner) Run(ctx context.Context) (Result, error) {
	cfg := r.opts.Cfg
	started := r.clock()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Stop-condition watcher.
	ctrl := safety.NewController(cfg.Safety, r.opts.Collector)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		t := time.NewTicker(r.opts.WatchInterval)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				if reason, tripped := ctrl.Check(); tripped {
					r.logger.Warn("safety controller tripped; stopping run", "reason", reason)
					cancel()
					return
				}
			}
		}
	}()

	deadline := started.Add(cfg.Scenario.Duration.Duration)
	warmupEnd := started.Add(cfg.Scenario.WarmUp.Duration)
	var res Result
	var err error
	if cfg.Scenario.Model == "closed" {
		res, err = r.runClosed(runCtx, started, deadline, warmupEnd)
	} else {
		res, err = r.runOpen(runCtx, started, deadline, warmupEnd)
	}
	// The workload window is over: cancel the run context so the stop-condition
	// watcher exits. (Without this, a healthy run would block on watchDone.)
	cancel()
	<-watchDone

	terminated := TerminationCompleted
	safetyReason := ""
	if _, trippedReason, _ := ctrl.Tripped(); trippedReason != "" {
		terminated = TerminationSafety
		safetyReason = trippedReason
	} else if ctx.Err() != nil {
		terminated = TerminationCancelled
	}
	res.StartedAt = started
	res.EndedAt = r.clock()
	res.Termination = terminated
	res.SafetyReason = safetyReason
	return res, err
}

// runAttempt executes one login attempt and records its outcome.
func (r *Runner) runAttempt(ctx context.Context, sess *auth.Session, warmup bool) {
	out := r.opts.Flow.Run(ctx, sess)
	r.opts.Collector.RecordAttempt(metrics.AttemptResult{
		Success:  out.Success,
		Aborted:  out.Aborted,
		Class:    out.Class,
		ErrMsg:   out.ErrMsg,
		Duration: out.Duration,
		Warmup:   warmup,
	})
	if !out.Success && !out.Aborted {
		r.logger.Debug("attempt failed",
			"class", out.Class, "msg", out.ErrMsg)
	}
}

// newSession builds one isolated session (client + jar + state) for a VU.
func (r *Runner) newSession(vuID int) (*auth.Session, error) {
	client, err := r.opts.Factory.NewClient(vuID)
	if err != nil {
		return nil, err
	}
	return &auth.Session{
		VUID:    vuID,
		Client:  client,
		Factory: r.opts.Factory,
		Base:    r.opts.Factory.BaseURL(),
		Creds:   r.opts.Creds,
		Clock:   r.opts.Clock,
		Recorder: func(sr metrics.StepResult) {
			r.opts.Collector.RecordStep(sr)
		},
	}, nil
}

// runClosed implements the fixed-VU model with ramp stagger.
func (r *Runner) runClosed(ctx context.Context, started, deadline, warmupEnd time.Time) (Result, error) {
	sc := r.opts.Cfg.Scenario
	n := sc.VirtualUsers
	stagger := time.Duration(0)
	if sc.RampUp.Duration > 0 && n > 1 {
		stagger = sc.RampUp.Duration / time.Duration(n)
	}
	staggerDown := time.Duration(0)
	if sc.RampDown.Duration > 0 && n > 1 {
		staggerDown = sc.RampDown.Duration / time.Duration(n)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		startAt := started.Add(time.Duration(i) * stagger)
		vuDeadline := deadline.Add(-time.Duration(n-1-i) * staggerDown)
		go func(vu int, startAt, vuDeadline time.Time) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					r.logger.Error("virtual user panicked", "vu", vu, "panic", fmt.Sprint(rec))
				}
			}()
			sleepCtx(ctx, startAt.Sub(r.clock()))
			sess, err := r.newSession(vu)
			if err != nil {
				r.logger.Error("cannot create session", "vu", vu, "err", err)
				return
			}
			for {
				if ctx.Err() != nil {
					return // cancelled: stop issuing attempts immediately
				}
				now := r.clock()
				if !now.Before(vuDeadline) {
					return
				}
				r.runAttempt(ctx, sess, now.Before(warmupEnd))
				// Think time: bounded by remaining per-VU window. A cancelled
				// context MUST end the loop — otherwise aborted attempts
				// busy-spin until the deadline (found by SIGTERM smoke test).
				remain := vuDeadline.Sub(r.clock())
				if remain <= 0 {
					return
				}
				if sleepCtx(ctx, minDur(sc.ThinkTime.Duration, remain)) {
					return
				}
			}
		}(i, startAt, vuDeadline)
	}
	r.waitDrained(&wg)
	return Result{Snapshot: r.opts.Collector.Snapshot()}, nil
}

// runOpen implements the constant-arrival model with a bounded in-flight queue.
func (r *Runner) runOpen(ctx context.Context, started, deadline, warmupEnd time.Time) (Result, error) {
	sc := r.opts.Cfg.Scenario
	maxInFlight := sc.MaxInFlight
	if maxInFlight < 1 {
		maxInFlight = 1
	}
	queue := make(chan struct{}, maxInFlight)
	arrivalsDone := make(chan struct{})
	go func() {
		defer close(arrivalsDone)
		baseInterval := time.Duration(float64(time.Second) / sc.RequestRate)
		lastSubmit := deadline.Add(-sc.RampDown.Duration)
		t := started
		for {
			now := r.clock()
			if !now.Before(deadline) {
				return
			}
			// Ramp-up: arrival interval shrinks linearly to the base interval.
			interval := baseInterval
			if sc.RampUp.Duration > 0 {
				elapsed := now.Sub(started)
				if elapsed < sc.RampUp.Duration {
					progress := math01(float64(elapsed) / float64(sc.RampUp.Duration))
					if progress < 0.05 {
						progress = 0.05
					}
					interval = time.Duration(float64(baseInterval) / progress)
				}
			}
			t = t.Add(interval)
			if t.Before(now) {
				t = now.Add(interval)
			}
			if sleepCtx(ctx, t.Sub(r.clock())) {
				return
			}
			if !r.clock().Before(lastSubmit) {
				return
			}
			select {
			case queue <- struct{}{}:
			case <-ctx.Done():
				return
			default:
				// Bounded queue is full: apply backpressure by dropping the
				// arrival. Counted explicitly; never queued unboundedly.
				r.opts.Collector.BackpressureDrops.Add(1)
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < maxInFlight; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					r.logger.Error("worker panicked", "worker", w, "panic", fmt.Sprint(rec))
				}
			}()
			sess, err := r.newSession(w)
			if err != nil {
				r.logger.Error("cannot create session", "worker", w, "err", err)
				return
			}
			for range queue {
				r.runAttempt(ctx, sess, r.clock().Before(warmupEnd))
			}
		}(w)
	}
	<-arrivalsDone
	close(queue)
	r.waitDrained(&wg)
	return Result{Snapshot: r.opts.Collector.Snapshot()}, nil
}

// waitDrained waits for workers, bounded by the drain grace period.
func (r *Runner) waitDrained(wg *sync.WaitGroup) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(r.opts.DrainGrace):
		r.logger.Warn("drain grace elapsed; proceeding with report", "grace", r.opts.DrainGrace)
	}
}

// sleepCtx sleeps d or returns early (true) when ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() != nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func math01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
