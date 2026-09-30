package main

import (
	"database/sql/driver"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"modernc.org/sqlite"
)

func TestServeStartupSignalChild(t *testing.T) {
	if os.Getenv("GHOSTTREE_TEST_STARTUP_CHILD") != "1" {
		return
	}
	marker := os.Getenv("GHOSTTREE_TEST_STARTUP_MARKER")
	release := os.Getenv("GHOSTTREE_TEST_STARTUP_RELEASE")
	sqlite.MustRegisterScalarFunction("test_startup_gate", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		if err := os.WriteFile(marker, []byte("ApplyStaleness is executing"), 0600); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				return int64(0), nil
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("gate timed out")
			}
			time.Sleep(time.Millisecond)
		}
	})
	os.Exit(cmdServe([]string{"--db", os.Getenv("GHOSTTREE_TEST_STARTUP_DB"), "--listen", "127.0.0.1:0"}, os.Stdout))
}

func TestServeSIGTERMDuringAcceptedStartupWrite(t *testing.T) {
	dir := t.TempDir()
	path, marker, release := filepath.Join(dir, "state.db"), filepath.Join(dir, "entered"), filepath.Join(dir, "release")
	seed, err := store.OpenWithOptions(path, store.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.InsertKnowledge(store.Knowledge{Type: "plan", Title: "old plan", Body: "body", ObservedAt: "2000-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.DB().Exec(`CREATE TRIGGER test_startup BEFORE UPDATE OF status ON knowledge WHEN new.status='stale' BEGIN SELECT test_startup_gate(); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(exe, "-test.run=^TestServeStartupSignalChild$")
	child.Env = append(os.Environ(), "GHOSTTREE_TEST_STARTUP_CHILD=1", "GHOSTTREE_TEST_STARTUP_DB="+path, "GHOSTTREE_TEST_STARTUP_MARKER="+marker, "GHOSTTREE_TEST_STARTUP_RELEASE="+release)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("child failed before startup mutation: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("startup mutation did not enter gate")
		}
		time.Sleep(time.Millisecond)
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("SIGTERM killed process during accepted ApplyStaleness instead of draining: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unclean startup shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server started listening after canceled startup instead of exiting")
	}
	reopened, err := store.OpenWithOptions(path, store.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var status string
	if err := reopened.DB().QueryRow("SELECT status FROM knowledge WHERE title='old plan'").Scan(&status); err != nil || status != "stale" {
		t.Fatalf("startup write was not drained: %q %v", status, err)
	}
}
