package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/storebench"
)

func main() {
	if err := runCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("storebench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	backendName := flags.String("backend", "current", "current or queued")
	dbPath := flags.String("db", "", "new SQLite database path")
	presetName := flags.String("preset", "small", "small, medium, or monorepo")
	seed := flags.Uint64("seed", 1, "deterministic workload seed")
	concurrency := flags.Int("concurrency", 4, "maximum concurrent operations")
	arrivalMultiplier := flags.Float64("arrival-multiplier", 1, "arrival rate multiplier")
	queueOperations := flags.Int("queue-operations", 1024, "maximum accepted queued operations")
	queueBytes := flags.Int64("queue-bytes", 512<<20, "maximum accepted queued payload bytes")
	batch := flags.Int("batch", 64, "maximum chunk operations per transaction")
	gatherWindow := flags.Duration("gather-window", 2*time.Millisecond, "chunk batch gathering window")
	readConnections := flags.Int("read-connections", 3, "read-only SQLite connections")
	repetition := flags.Int("repetition", 1, "one-based repetition number")
	output := flags.String("output", "", "new JSON report path")
	keepDB := flags.Bool("keep-db", false, "retain the generated database")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *backendName != "current" && *backendName != "queued" {
		return fmt.Errorf("unknown backend %q", *backendName)
	}
	if *repetition <= 0 {
		return fmt.Errorf("repetition must be positive")
	}
	scale, err := storebench.Preset(*presetName)
	if err != nil {
		return err
	}
	if *output != "" {
		if _, err := os.Lstat(*output); err == nil {
			return fmt.Errorf("report path %q already exists", *output)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	path, cleanup, err := prepareDatabasePath(*dbPath, *keepDB)
	if err != nil {
		return err
	}
	if *output == "" {
		absolute, err := filepath.Abs(fmt.Sprintf("storebench-%s-%d.json", *backendName, time.Now().UnixNano()))
		if err != nil {
			_ = cleanup()
			return err
		}
		*output = absolute
	}

	workload, expected, err := storebench.GenerateChecked(*seed, scale)
	if err != nil {
		_ = cleanup()
		return err
	}
	var backend storebench.Backend
	switch *backendName {
	case "current":
		backend, err = storebench.OpenCurrentSQLite(path)
	case "queued":
		backend, err = storebench.OpenQueuedSQLite(path, storebench.QueueConfig{
			MaxOperations: *queueOperations, MaxBytes: *queueBytes, MaxBatch: *batch,
			GatherWindow: *gatherWindow, ReadConnections: *readConnections,
		})
	}
	if err != nil {
		_ = cleanup()
		return err
	}
	runID := fmt.Sprintf("%s-%s-%d-%d", *backendName, *presetName, *seed, time.Now().UnixNano())
	report, runErr := storebench.Run(context.Background(), backend, workload, expected, storebench.RunConfig{
		Concurrency: *concurrency, ArrivalMultiplier: *arrivalMultiplier, RunID: runID,
		Preset: *presetName, Repetition: *repetition,
	})
	report.Environment = runEnvironment()
	writeErr := writeReport(*output, report)
	closeErr := backend.Close()
	cleanupErr := cleanup()
	if writeErr == nil {
		fmt.Fprintf(stdout, "%s\n%s: %d operations, %d errors, %.2f operations/s\n",
			*output, report.Backend, report.Operations, report.Errors, report.Throughput)
	}
	return errors.Join(runErr, writeErr, closeErr, cleanupErr)
}

func runEnvironment() storebench.RunEnvironment {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	commit, modified := "unknown", false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					commit = setting.Value
				}
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}
	return storebench.RunEnvironment{Host: host, GoVersion: runtime.Version(), GitCommit: commit, GitModified: modified}
}

func prepareDatabasePath(requested string, keep bool) (string, func() error, error) {
	if requested == "" {
		dir, err := os.MkdirTemp("", "ghosttree-storebench-")
		if err != nil {
			return "", nil, err
		}
		path := filepath.Join(dir, "ghosttree.db")
		return path, func() error {
			if keep {
				return nil
			}
			return os.RemoveAll(dir)
		}, nil
	}
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", nil, err
	}
	if info, err := os.Lstat(absolute); err == nil {
		return "", nil, fmt.Errorf("database path %q already exists as %s", absolute, info.Mode())
	} else if !os.IsNotExist(err) {
		return "", nil, err
	}
	parent := filepath.Dir(absolute)
	info, err := os.Stat(parent)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("database parent %q is not a directory", parent)
	}
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return "", nil, fmt.Errorf("database path %q already exists", absolute)
		}
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(absolute)
		return "", nil, err
	}
	owned, err := os.Lstat(absolute)
	if err != nil {
		return "", nil, err
	}
	return absolute, func() error {
		if keep {
			return nil
		}
		current, err := os.Lstat(absolute)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !os.SameFile(owned, current) {
			return nil
		}
		var cleanupErr error
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(absolute + suffix); err != nil && !os.IsNotExist(err) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
		return cleanupErr
	}, nil
}

func writeReport(path string, report storebench.Report) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
