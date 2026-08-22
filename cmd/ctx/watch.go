package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

func pidFilePath() string {
	return filepath.Join(filepath.Dir(collector.DefaultStatePath()), "watch.pid")
}

func cmdWatch(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stdout)
	interval := fs.Duration("interval", 30*time.Second, "full sweep interval")
	once := fs.Bool("once", false, "sync once and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "load config: %v (run 'ctx setup' first)\n", err)
		return 1
	}
	st, err := collector.LoadState(collector.DefaultStatePath())
	if err != nil {
		fmt.Fprintf(stdout, "load state: %v\n", err)
		return 1
	}
	home, _ := os.UserHomeDir()
	roots := collector.DefaultRoots(home)
	c := client.New(cfg)
	if *once {
		if err := collector.Sweep(roots, c, st, cfg.Machine); err != nil {
			fmt.Fprintf(stdout, "sweep: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "swept %d transcripts\n", len(st.Files))
		return 0
	}
	pid := pidFilePath()
	if err := os.MkdirAll(filepath.Dir(pid), 0o755); err == nil {
		os.WriteFile(pid, []byte(strconv.Itoa(os.Getpid())), 0o644)
		defer os.Remove(pid)
	}
	fmt.Fprintf(stdout, "watching transcripts for %s every %s\n", cfg.Machine, *interval)
	if err := collector.Watch(roots, c, st, cfg.Machine, *interval); err != nil {
		fmt.Fprintf(stdout, "watch: %v\n", err)
		return 1
	}
	return 0
}
