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

func TestPrepareDatabasePathAtomicallyReservesRequestedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reserved.db")
	got, cleanup, err := prepareDatabasePath(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
	if _, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !os.IsExist(err) {
		t.Fatalf("competing create = %v, want existence error", err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDatabasePathDoesNotDeleteReplacementFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reserved.db")
	_, cleanup, err := prepareDatabasePath(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "replacement" {
		t.Fatalf("replacement changed: %q %v", raw, err)
	}
}

func TestPrepareDatabasePathRefusesExistingSQLiteSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reserved.db")
			sidecar := path + suffix
			if err := os.WriteFile(sidecar, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := prepareDatabasePath(path, false)
			if err == nil || !strings.Contains(err.Error(), sidecar) {
				t.Fatalf("error = %v, want sidecar path", err)
			}
			raw, readErr := os.ReadFile(sidecar)
			if readErr != nil || string(raw) != "keep" {
				t.Fatalf("sidecar changed: %q %v", raw, readErr)
			}
		})
	}
}

func TestPrepareDatabasePathNeverDeletesUntrackedSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reserved.db")
	_, cleanup, err := prepareDatabasePath(path, false)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := path + "-wal"
	if err := os.WriteFile(sidecar, []byte("untracked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sidecar)
	if err != nil || string(raw) != "untracked" {
		t.Fatalf("untracked sidecar changed: %q %v", raw, err)
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
	if report.Config.Preset != "small" || report.Config.Repetition != 1 || report.Environment.Host == "" ||
		report.Environment.GoVersion == "" || report.Environment.GitCommit == "" {
		t.Fatalf("reproducibility metadata = config %+v environment %+v", report.Config, report.Environment)
	}
	if report.BackendStats.Engine != "sqlite" || report.BackendStats.EngineVersion == "" || report.BackendStats.QueueConfig == nil {
		t.Fatalf("backend metadata = %+v", report.BackendStats)
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
