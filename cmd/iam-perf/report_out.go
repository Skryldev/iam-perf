package main

import (
	"encoding/json"
	"fmt"
	"os"

	"example.com/iam-perf/internal/config"
	"example.com/iam-perf/internal/report"
	"example.com/iam-perf/pkg/iamperf"
)

// configLoad loads + validates configuration via the internal loader.
func configLoad(path string) (iamperf.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func redactedJSON(cfg iamperf.Config) ([]byte, error) { return cfg.RedactedJSON() }

func writeJSON(path string, rep *iamperf.Report) error {
	f, err := os.Create(path) // #nosec G304 -- operator-configured output dir
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	defer f.Close()
	if err := rep.WriteJSON(f); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return f.Sync()
}

func writeCSV(path string, rep *iamperf.Report) error {
	f, err := os.Create(path) // #nosec G304 -- operator-configured output dir
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	defer f.Close()
	if err := rep.WriteCSV(f); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return f.Sync()
}

// ensureReportJSON round-trips a report for validation (used by tests/CI).
func ensureReportJSON(rep *iamperf.Report) error {
	_, err := json.Marshal(rep)
	return err
}

var _ = report.SchemaVersion
