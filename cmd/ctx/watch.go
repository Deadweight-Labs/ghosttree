package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

func pidFilePath() string {
	return filepath.Join(filepath.Dir(collector.DefaultStatePath()), "watch.pid")
}

func watchLockPath() string {
	return filepath.Join(filepath.Dir(collector.DefaultStatePath()), "watch.lock")
}

var errWatchRunning = errors.New("another ctx watch is already running")

// acquireWatchLock takes an exclusive flock on the lock file. The lock belongs
// to the open file, so a crashed process never leaves a stale one behind.
func acquireWatchLock(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errWatchRunning
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
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
	release, err := acquireWatchLock(watchLockPath())
	if errors.Is(err, errWatchRunning) {
		// Exit 0: the collector the user wants is running, and a service
		// manager must not restart this one in a loop.
		fmt.Fprintln(stdout, "ctx watch is already running on this machine; this second instance exits and leaves the first one alone.")
		return 0
	}
	if err != nil {
		fmt.Fprintf(stdout, "watch lock: %v\n", err)
		return 1
	}
	defer release()
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
