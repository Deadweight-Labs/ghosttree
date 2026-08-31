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

	postgresbench "github.com/Deadweight-Labs/ghosttree/bench/postgres"
	"github.com/Deadweight-Labs/ghosttree/internal/storebench"
)

func main() {
	if err := runCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("storebenchpg", flag.ContinueOnError)
	flags.SetOutput(stderr)
	adminURL := flags.String("admin-url", os.Getenv("GHOSTTREE_BENCH_POSTGRES_ADMIN_URL"), "temporary PostgreSQL admin URL")
	presetName := flags.String("preset", "small", "small, medium, or monorepo")
	seed := flags.Uint64("seed", 1, "deterministic workload seed")
	concurrency := flags.Int("concurrency", 4, "maximum concurrent operations")
	arrivalMultiplier := flags.Float64("arrival-multiplier", 1, "arrival rate multiplier")
	maxConnections := flags.Int("max-connections", 4, "maximum PostgreSQL pool connections")
	repetition := flags.Int("repetition", 1, "one-based repetition number")
	output := flags.String("output", "", "new JSON report path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *adminURL == "" {
		return fmt.Errorf("admin URL is required")
	}
	if *repetition <= 0 {
		return fmt.Errorf("repetition must be positive")
	}
	scale, err := storebench.Preset(*presetName)
	if err != nil {
		return err
	}
	if *output == "" {
		absolute, err := filepath.Abs(fmt.Sprintf("storebench-postgresql-%d.json", time.Now().UnixNano()))
		if err != nil {
			return err
		}
		*output = absolute
	}
	if _, err := os.Lstat(*output); err == nil {
		return fmt.Errorf("report path %q already exists", *output)
	} else if !os.IsNotExist(err) {
		return err
	}
	workload, expected, err := storebench.GenerateChecked(*seed, scale)
	if err != nil {
		return err
	}
	runID := fmt.Sprintf("postgresql-%s-%d-%d", *presetName, *seed, time.Now().UnixNano())
	backend, err := postgresbench.OpenEphemeral(context.Background(), *adminURL, runID, *maxConnections)
	if err != nil {
		return err
	}
	report, runErr := storebench.Run(context.Background(), backend, workload, expected, storebench.RunConfig{
		Concurrency: *concurrency, ArrivalMultiplier: *arrivalMultiplier, RunID: runID,
		Preset: *presetName, Repetition: *repetition,
	})
	report.Environment = runEnvironment()
	writeErr := writeReport(*output, report)
	closeErr := backend.Close()
	if writeErr == nil {
		fmt.Fprintf(stdout, "%s\n%s: %d operations, %d errors, %.2f operations/s\n",
			*output, report.Backend, report.Operations, report.Errors, report.Throughput)
	}
	return errors.Join(runErr, writeErr, closeErr)
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
