package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/storebench"
)

func TestRunCommandWritesVerifiedPostgreSQLReport(t *testing.T) {
	adminURL := os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL")
	if adminURL == "" {
		t.Skip("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL is not set")
	}
	output := filepath.Join(t.TempDir(), "postgres.json")
	var stdout bytes.Buffer
	if err := runCommand([]string{"--admin-url=" + adminURL, "--preset=small", "--seed=254",
		"--concurrency=4", "--max-connections=4", "--output=" + output}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var report storebench.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Backend != "postgresql" || report.Errors != 0 || report.VerificationError != "" ||
		report.Config.Preset != "small" || report.Environment.GitCommit == "" || report.BackendStats.EngineVersion == "" {
		t.Fatalf("report = %+v", report)
	}
}
