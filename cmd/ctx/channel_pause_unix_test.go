//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/agentpause"
)

func TestChannelPauseSyncClearsAnObjectAtTheFlagPathWithoutAControl(t *testing.T) {
	pauseEnv(t)
	src := &fakePauseSource{}
	p := &pauseSyncer{agent: pauseTestAgent}
	p.src = src
	dir, _ := agentpause.Dir(pauseTestAgent)
	flag := filepath.Join(dir, "flag.json")
	for name, mk := range map[string]func(){
		"directory with content": func() { _ = os.MkdirAll(flag, 0o700); _ = os.WriteFile(filepath.Join(flag, "x"), []byte("x"), 0o600) },
		"symlink":                func() { _ = os.Symlink("/etc/passwd", flag) },
		"fifo":                   func() { _ = syscall.Mkfifo(flag, 0o600) },
	} {
		_ = os.RemoveAll(flag)
		_ = os.MkdirAll(dir, 0o700)
		mk()
		if _, set := agentpause.ReadFlag(pauseTestAgent); !set {
			t.Fatalf("%s: setup must look like a pause", name)
		}
		if err := p.sync(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Lstat(flag); err == nil {
			t.Fatalf("%s: still there after sync without a control", name)
		}
	}
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Fatal("the symlink target must be untouched")
	}
}
