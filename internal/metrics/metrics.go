// Package metrics implements low-overhead, bounded-cardinality metric
// collection: atomic counters, lock-sharded status maps, and log-linear
// latency histograms whose percentiles are computed from GLOBAL merged
// buckets (never by averaging per-worker percentiles).
package metrics

import (
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Histogram is a fixed-boundary log-linear histogram safe for concurrent use.
type Histogram struct {
	mu     sync.Mutex
	bounds []float64 // milliseconds, ascending
	counts []uint64  // len(bounds)+1
	count  uint64
	sumMS  float64
	minMS  float64
	maxMS  float64
}

// NewHistogram returns a histogram with log-linear buckets covering
// 10µs..65s. Bucket edges are generated with exact integer arithmetic to
// avoid float-accumulation artifacts; max relative quantile error ≈ 10%.
func NewHistogram() *Histogram {
	var bounds []float64
	add := func(denom, from, to, step int) {
		for i := from; i <= to; i += step {
			bounds = append(bounds, round3(float64(i)/float64(denom)))
		}
	}
	add(100, 1, 10, 1)     // 0.01 .. 0.10 ms
	add(10, 2, 10, 1)      // 0.2 .. 1.0 ms
	add(1, 2, 10, 1)       // 2 .. 10 ms
	add(1, 20, 100, 10)    // 20 .. 100 ms
	add(1, 200, 1000, 100) // 200 .. 1000 ms
	add(1, 2000, 10000, 1000)
	add(1, 20000, 65000, 5000)
	return newHistogramWithBounds(bounds)
}

func newHistogramWithBounds(bounds []float64) *Histogram {
	return &Histogram{
		bounds: bounds,
		counts: make([]uint64, len(bounds)+1),
		minMS:  math.MaxFloat64,
	}
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// Record observes one latency value (any duration; zero values ignored).
func (h *Histogram) Record(d time.Duration) {
	ms := float64(d.Nanoseconds()) / 1e6
	if ms <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	idx := sortIndex(h.bounds, ms)
	h.counts[idx]++
	h.count++
	h.sumMS += ms
	if ms < h.minMS {
		h.minMS = ms
	}
	if ms > h.maxMS {
		h.maxMS = ms
	}
}

// sortIndex returns the bucket index for value ms (first bound > ms).
func sortIndex(bounds []float64, ms float64) int {
	lo, hi := 0, len(bounds)
	for lo < hi {
		mid := (lo + hi) / 2
		if bounds[mid] < ms {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// Quantile returns the p-quantile in milliseconds (interpolated, 0 if empty).
func (h *Histogram) Quantile(p float64) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	rank := p * float64(h.count)
	var cum uint64
	for i, c := range h.counts {
		cum += c
		if float64(cum) >= rank {
			upper := h.bucketUpper(i)
			lower := h.bucketLower(i, upper)
			// linear interpolation inside the bucket
			frac := (rank - float64(cum-c)) / math.Max(1, float64(c))
			return lower + (upper-lower)*math.Min(1, frac)
		}
	}
	return h.maxMS
}

func (h *Histogram) bucketUpper(i int) float64 {
	if i < len(h.bounds) {
		return h.bounds[i]
	}
	return math.Max(h.maxMS, h.bounds[len(h.bounds)-1]*1.5)
}

func (h *Histogram) bucketLower(i int, upper float64) float64 {
	if i == 0 {
		return math.Min(h.minMS, upper/2)
	}
	return h.bounds[i-1]
}

// Merge folds src into h (used to aggregate sharded/period histograms).
func (h *Histogram) Merge(src *Histogram) {
	src.mu.Lock()
	defer src.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, c := range src.counts {
		h.counts[i] += c
	}
	h.count += src.count
	h.sumMS += src.sumMS
	if src.minMS < h.minMS {
		h.minMS = src.minMS
	}
	if src.maxMS > h.maxMS {
		h.maxMS = src.maxMS
	}
}

// Count returns the number of observations.
func (h *Histogram) Count() uint64 { h.mu.Lock(); defer h.mu.Unlock(); return h.count }

// Mean returns the arithmetic mean in milliseconds.
func (h *Histogram) Mean() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	return h.sumMS / float64(h.count)
}

// Min returns the minimum observation in milliseconds.
func (h *Histogram) Min() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	return h.minMS
}

// Max returns the maximum observation in milliseconds.
func (h *Histogram) Max() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	return h.maxMS
}

// LatencyStats is a serializable percentile summary (milliseconds).
type LatencyStats struct {
	Count uint64  `json:"count"`
	Mean  float64 `json:"mean_ms"`
	P50   float64 `json:"p50_ms"`
	P90   float64 `json:"p90_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Min   float64 `json:"min_ms"`
	Max   float64 `json:"max_ms"`
}

// Stats renders the histogram summary.
func (h *Histogram) Stats() LatencyStats {
	return LatencyStats{
		Count: h.Count(),
		Mean:  round2(h.Mean()),
		P50:   round2(h.Quantile(0.50)),
		P90:   round2(h.Quantile(0.90)),
		P95:   round2(h.Quantile(0.95)),
		P99:   round2(h.Quantile(0.99)),
		Min:   round2(h.Min()),
		Max:   round2(h.Max()),
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Counters is the set of atomic event counters.
type Counters struct {
	AttemptsStarted   atomic.Int64
	AttemptsSucceeded atomic.Int64
	AttemptsFailed    atomic.Int64
	AttemptsAborted   atomic.Int64 // cancelled/panicked before completion
	RequestsStarted   atomic.Int64
	RequestsCompleted atomic.Int64
	RequestsFailed    atomic.Int64
	Timeouts          atomic.Int64
	ConnErrors        atomic.Int64
	Status429         atomic.Int64
	Status5xx         atomic.Int64
	Redirects         atomic.Int64
	ConnReuse         atomic.Int64 // responses served on a reused connection
	NewConns          atomic.Int64
	BackpressureDrops atomic.Int64 // open model: arrivals rejected on full queue
	WarmupAttempts    atomic.Int64 // attempts executed during warm-up window
}

// Collector aggregates all metrics for one run. Safe for concurrent use.
type Collector struct {
	Counters
	attemptLatency *Histogram
	ttfb           *Histogram
	dns            *Histogram
	tcp            *Histogram
	tls            *Histogram
	stepMu         sync.Mutex
	steps          map[string]*stepAgg
	statusMu       sync.Mutex
	statusDist     map[string]int64
	errMu          sync.Mutex
	errClasses     map[string]int64
	errSamples     map[string]string
	started        time.Time
	ended          atomic.Int64 // unix nanos, set by SnapshotEnd
}

type stepAgg struct {
	hist     *Histogram
	started  int64
	failed   int64
	byStatus map[string]int64
}

// NewCollector creates an empty collector.
func NewCollector() *Collector {
	return &Collector{
		attemptLatency: NewHistogram(),
		ttfb:           NewHistogram(),
		dns:            NewHistogram(),
		tcp:            NewHistogram(),
		tls:            NewHistogram(),
		steps:          map[string]*stepAgg{},
		statusDist:     map[string]int64{},
		errClasses:     map[string]int64{},
		errSamples:     map[string]string{},
	}
}

// StepResult carries one recorded HTTP step observation.
type StepResult struct {
	Step     string
	Status   int
	ErrClass string // "" on success
	ErrMsg   string // pre-redacted
	Duration time.Duration
}

// RecordStep records one HTTP request within a flow.
func (c *Collector) RecordStep(s StepResult) {
	c.stepMu.Lock()
	agg, ok := c.steps[s.Step]
	if !ok {
		agg = &stepAgg{hist: NewHistogram(), byStatus: map[string]int64{}}
		c.steps[s.Step] = agg
	}
	agg.started++
	if s.Status > 0 {
		agg.byStatus[intToString(s.Status)]++
	}
	if s.ErrClass != "" {
		agg.failed++
	}
	agg.hist.Record(s.Duration)
	c.stepMu.Unlock()

	c.RequestsStarted.Add(1)
	if s.Status > 0 {
		c.statusMu.Lock()
		c.statusDist[intToString(s.Status)]++
		c.statusMu.Unlock()
		switch {
		case s.Status == 429:
			c.Status429.Add(1)
		case s.Status >= 500:
			c.Status5xx.Add(1)
		}
	}
	switch s.ErrClass {
	case "":
		c.RequestsCompleted.Add(1)
	case "timeout":
		c.RequestsFailed.Add(1)
		c.Timeouts.Add(1)
		c.recordErr(s)
	case "transport":
		c.RequestsFailed.Add(1)
		c.ConnErrors.Add(1)
		c.recordErr(s)
	default:
		c.RequestsFailed.Add(1)
		c.recordErr(s)
	}
}

func (c *Collector) recordErr(s StepResult) {
	c.errMu.Lock()
	c.errClasses[s.ErrClass]++
	if _, ok := c.errSamples[s.ErrClass]; !ok && s.ErrMsg != "" {
		c.errSamples[s.ErrClass] = s.ErrMsg
	}
	c.errMu.Unlock()
}

// AttemptResult carries one recorded login-transaction observation.
type AttemptResult struct {
	Success  bool
	Aborted  bool
	Class    string // failure class, "" on success
	ErrMsg   string // pre-redacted
	Duration time.Duration
	Warmup   bool
}

// RecordAttempt records one end-to-end login transaction.
func (c *Collector) RecordAttempt(a AttemptResult) {
	c.AttemptsStarted.Add(1)
	if a.Warmup {
		c.WarmupAttempts.Add(1)
	}
	switch {
	case a.Aborted:
		c.AttemptsAborted.Add(1)
	case a.Success:
		c.AttemptsSucceeded.Add(1)
	default:
		c.AttemptsFailed.Add(1)
	}
	if !a.Aborted && !a.Warmup {
		c.attemptLatency.Record(a.Duration)
	}
	if a.Class != "" {
		c.errMu.Lock()
		c.errClasses[a.Class]++
		if _, ok := c.errSamples[a.Class]; !ok && a.ErrMsg != "" {
			c.errSamples[a.Class] = a.ErrMsg
		}
		c.errMu.Unlock()
	}
}

// RecordTTFB, RecordDNS, RecordTCP, RecordTLS observe httptrace timings
// (only called when the trace stage was observable).
func (c *Collector) RecordTTFB(d time.Duration) { c.ttfb.Record(d) }
func (c *Collector) RecordDNS(d time.Duration)  { c.dns.Record(d) }
func (c *Collector) RecordTCP(d time.Duration)  { c.tcp.Record(d) }
func (c *Collector) RecordTLS(d time.Duration)  { c.tls.Record(d) }

// MarkConnReuse records connection reuse information from httptrace.
func (c *Collector) MarkConnReuse(reused bool) {
	if reused {
		c.ConnReuse.Add(1)
	} else {
		c.NewConns.Add(1)
	}
}

// MarkRedirect records one followed redirect.
func (c *Collector) MarkRedirect() { c.Redirects.Add(1) }

// StatusDistribution returns a copy of the status-code histogram.
func (c *Collector) StatusDistribution() map[string]int64 {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	out := make(map[string]int64, len(c.statusDist))
	for k, v := range c.statusDist {
		out[k] = v
	}
	return out
}

// StepSnapshot is the serializable per-step aggregate.
type StepSnapshot struct {
	Step     string           `json:"step"`
	Started  int64            `json:"started"`
	Failed   int64            `json:"failed"`
	Statuses map[string]int64 `json:"status_distribution"`
	Latency  LatencyStats     `json:"latency"`
}

// Snapshot is a consistent point-in-time view of all metrics.
type Snapshot struct {
	AttemptsStarted   int64
	AttemptsSucceeded int64
	AttemptsFailed    int64
	AttemptsAborted   int64
	WarmupAttempts    int64
	RequestsStarted   int64
	RequestsCompleted int64
	RequestsFailed    int64
	Timeouts          int64
	ConnErrors        int64
	Status429         int64
	Status5xx         int64
	Redirects         int64
	ConnReuse         int64
	NewConns          int64
	BackpressureDrops int64
	StatusDist        map[string]int64
	AttemptLatency    LatencyStats
	TTFB              LatencyStats
	DNS               LatencyStats
	TCP               LatencyStats
	TLS               LatencyStats
	Steps             []StepSnapshot
	ErrorClasses      map[string]int64
	ErrorSamples      map[string]string
}

// Snapshot captures the current state.
func (c *Collector) Snapshot() Snapshot {
	c.stepMu.Lock()
	steps := make([]StepSnapshot, 0, len(c.steps))
	for name, agg := range c.steps {
		st := map[string]int64{}
		for k, v := range agg.byStatus {
			st[k] = v
		}
		steps = append(steps, StepSnapshot{
			Step: name, Started: agg.started, Failed: agg.failed,
			Statuses: st, Latency: agg.hist.Stats(),
		})
	}
	c.stepMu.Unlock()
	sortSteps(steps)

	c.errMu.Lock()
	errCls := map[string]int64{}
	for k, v := range c.errClasses {
		errCls[k] = v
	}
	errSmp := map[string]string{}
	for k, v := range c.errSamples {
		errSmp[k] = v
	}
	c.errMu.Unlock()

	return Snapshot{
		AttemptsStarted:   c.AttemptsStarted.Load(),
		AttemptsSucceeded: c.AttemptsSucceeded.Load(),
		AttemptsFailed:    c.AttemptsFailed.Load(),
		AttemptsAborted:   c.AttemptsAborted.Load(),
		WarmupAttempts:    c.WarmupAttempts.Load(),
		RequestsStarted:   c.RequestsStarted.Load(),
		RequestsCompleted: c.RequestsCompleted.Load(),
		RequestsFailed:    c.RequestsFailed.Load(),
		Timeouts:          c.Timeouts.Load(),
		ConnErrors:        c.ConnErrors.Load(),
		Status429:         c.Status429.Load(),
		Status5xx:         c.Status5xx.Load(),
		Redirects:         c.Redirects.Load(),
		ConnReuse:         c.ConnReuse.Load(),
		NewConns:          c.NewConns.Load(),
		BackpressureDrops: c.BackpressureDrops.Load(),
		StatusDist:        c.StatusDistribution(),
		AttemptLatency:    c.attemptLatency.Stats(),
		TTFB:              c.ttfb.Stats(),
		DNS:               c.dns.Stats(),
		TCP:               c.tcp.Stats(),
		TLS:               c.tls.Stats(),
		Steps:             steps,
		ErrorClasses:      errCls,
		ErrorSamples:      errSmp,
	}
}

func sortSteps(s []StepSnapshot) {
	// insertion sort keeps dependency surface zero; N is tiny (<10 steps)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Step < s[j-1].Step; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func intToString(n int) string { return strconv.Itoa(n) }
