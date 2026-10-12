// Command iam-perf is the CLI entry point for the IAM login performance
// testing framework.
//
// Commands:
//
//	validate  parse + validate configuration (NO network activity)
//	run       safety-gated workload execution (requires two-key approval)
//	report    render a stored JSON report
//	version   print build metadata
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"example.com/iam-perf/pkg/iamperf"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "validate":
		code = cmdValidate(os.Args[2:])
	case "run":
		code = cmdRun(os.Args[2:])
	case "report":
		code = cmdReport(os.Args[2:])
	case "version":
		code = cmdVersion(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprint(os.Stderr, `iam-perf — IAM login performance testing framework

Usage:
  iam-perf validate -c <config.json>
  iam-perf run -c <config.json> [--iam-accept-risk] [--out <dir>]
  iam-perf report -f <report.json>
  iam-perf version

Safety model:
  - validate and report NEVER generate traffic.
  - run requires authorization.accepted=true in config AND the runtime
    confirmation flag --iam-accept-risk (or env IAMPERF_ACCEPT_RISK=true).
  - Every request URL is checked against the approved allowlist; stop
    conditions abort the run automatically.

Exit codes: 0 success · 1 runtime error · 2 invalid configuration · 130 cancelled
`)
}

// runContext returns a context cancelled on SIGINT/SIGTERM (graceful).
func runContext() (context.Context, func()) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	return ctx, stop
}

func cmdVersion(_ []string) int {
	v := iamperf.Version()
	fmt.Printf("iam-perf %s\ngo      %s\n", v, goVersion())
	return 0
}

func goVersion() string { return runtimeVersion() }
