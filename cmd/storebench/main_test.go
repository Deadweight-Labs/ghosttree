package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/storebench"
)

func TestRunCommandRejectsUnknownBackend(t *testing.T) {
	err := runCommand([]string{"--backend=oracle"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCommandRefusesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runCommand([]string{"--backend=current", "--db=" + path}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("existing file changed: %q %v", raw, err)
	}
}

func TestRunCommandWritesVerifiedSmallReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	dbPath := filepath.Join(dir, "benchmark.db")
	var stdout bytes.Buffer
	err := runCommand([]string{
		"--backend=queued", "--preset=small", "--seed=23", "--concurrency=4",
		"--db=" + dbPath, "--output=" + reportPath,
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report storebench.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Backend != "sqlite_queued" || report.Operations == 0 || report.Errors != 0 || report.VerificationError != "" {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(stdout.String(), reportPath) {
		t.Fatalf("stdout = %q, want report path", stdout.String())
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("temporary database remains: %v", err)
	}
}

func TestRunCommandDoesNotOverwriteReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	if err := os.WriteFile(reportPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runCommand([]string{"--backend=current", "--preset=small", "--output=" + reportPath}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v", err)
	}
	raw, _ := os.ReadFile(reportPath)
	if string(raw) != "keep" {
		t.Fatalf("report was overwritten with %q", raw)
	}
}
