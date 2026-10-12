package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"example.com/iam-perf/internal/metrics"
	"example.com/iam-perf/pkg/iamperf"
)

func runtimeVersion() string { return runtime.Version() }

// cmdValidate parses + validates configuration. It performs zero network I/O.
func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	cfgPath := fs.String("c", "", "path to configuration JSON (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "validate: -c <config.json> is required")
		return 2
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate: %s\n", err)
		return 2
	}
	printResolved(cfg)
	fmt.Println("\nOK: configuration is valid. No network activity was performed.")
	return 0
}

// cmdRun executes the safety-gated workload.
func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("c", "", "path to configuration JSON (required)")
	acceptRisk := fs.Bool("iam-accept-risk", false, "runtime confirmation key for load generation")
	outDir := fs.String("out", "", "override report.output_dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "run: -c <config.json> is required")
		return 2
	}
	// Signal handling installs FIRST: SIGINT/SIGTERM must be graceful from
	// process start (including the pre-run abort window), never a hard kill.
	ctx, stop := runContext()
	defer stop()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %s\n", err)
		return 2
	}
	if *outDir != "" {
		cfg.Report.OutputDir = *outDir
	}

	printResolved(cfg)
	fmt.Println("\nsafety: resolved config printed above; load starts in 3 seconds (Ctrl-C aborts).")
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done(): // signal during the abort window
		fmt.Fprintln(os.Stderr, "run: cancelled before load started")
		return 130
	}

	opts := []iamperf.Option{iamperf.WithRunConfirmation(*acceptRisk)}
	if cfg.Metrics.PrometheusEnabled {
		opts = append(opts, iamperf.WithCollectorHook(servePrometheus(cfg.Metrics.PrometheusAddr)))
	}
	eng, err := iamperf.NewEngine(cfg, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %s\n", err)
		return 2
	}

	rep, runErr := eng.Run(ctx)
	if rep == nil {
		fmt.Fprintf(os.Stderr, "run: %s\n", runErr)
		if errors.Is(runErr, iamperf.ErrNotAuthorized) {
			return 1
		}
		return 2
	}
	paths := writeReport(cfg, rep)
	rep.PrintTerminal(os.Stdout)
	for _, p := range paths {
		fmt.Println("report written:", p)
	}
	switch {
	case rep.Metadata.Termination == "cancelled":
		return 130
	case runErr != nil:
		fmt.Fprintf(os.Stderr, "run: workload error: %s\n", runErr)
		return 1
	}
	return 0
}

// cmdReport renders a stored JSON report.
func cmdReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	path := fs.String("f", "", "path to report JSON (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "report: -f <report.json> is required")
		return 2
	}
	raw, err := os.ReadFile(*path) // #nosec G304 -- operator-provided path
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %s\n", err)
		return 1
	}
	var rep iamperf.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		fmt.Fprintf(os.Stderr, "report: unparseable report: %s\n", err)
		return 1
	}
	rep.PrintTerminal(os.Stdout)
	return 0
}

// loadConfig layers defaults < file < env and validates.
func loadConfig(path string) (iamperf.Config, error) {
	return configLoad(path)
}

func printResolved(cfg iamperf.Config) {
	b, err := redactedJSON(cfg)
	if err != nil {
		fmt.Println("resolved config: <render error:", err, ">")
		return
	}
	fmt.Println("resolved configuration (redacted):")
	fmt.Println(string(b))
}

// writeReport persists the report in all configured formats.
func writeReport(cfg iamperf.Config, rep *iamperf.Report) []string {
	var written []string
	dir := cfg.Report.OutputDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "report: cannot create output dir: %s\n", err)
		return nil
	}
	base := filepath.Join(dir, "iamperf-report-"+rep.RunID[:8]+"-"+time.Now().UTC().Format("20060102T150405Z"))
	for _, f := range cfg.Report.Formats {
		switch f {
		case "json":
			p := base + ".json"
			if err := writeJSON(p, rep); err != nil {
				fmt.Fprintf(os.Stderr, "report: %s\n", err)
				continue
			}
			written = append(written, p)
		case "csv":
			p := base + ".csv"
			if err := writeCSV(p, rep); err != nil {
				fmt.Fprintf(os.Stderr, "report: %s\n", err)
				continue
			}
			written = append(written, p)
		case "terminal":
			// rendered to stdout already
		}
	}
	return written
}

// servePrometheus starts a minimal metrics endpoint for the run duration.
func servePrometheus(addr string) func(*metrics.Collector) {
	return func(c *metrics.Collector) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "prometheus endpoint disabled: %s\n", err)
			return
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			_ = metrics.WritePrometheus(w, c.Snapshot())
		})
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		// The CLI process exits when the run completes; the server dies with it.
		go func() { _ = srv.Serve(ln) }()
		fmt.Printf("prometheus metrics: http://%s/metrics\n", ln.Addr())
	}
}
