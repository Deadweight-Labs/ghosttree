//go:build !windows

package agentpause

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func flagPath(t *testing.T) string {
	t.Helper()
	dir, _ := Dir(agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, flagName)
}

func TestUnusualObjectsAtTheFlagPathPauseWithoutBeingRead(t *testing.T) {
	cases := map[string]func(p string){
		"fifo":      func(p string) { _ = syscall.Mkfifo(p, 0o600) },
		"symlink":   func(p string) { _ = os.Symlink("/etc/passwd", p) },
		"directory": func(p string) { _ = os.Mkdir(p, 0o700) },
		"oversize":  func(p string) { _ = os.WriteFile(p, bytes.Repeat([]byte("x"), maxFlagSize+1), 0o600) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			stateEnv(t)
			mk(flagPath(t))
			done := make(chan bool, 1)
			go func() {
				var out bytes.Buffer
				done <- Gate(agent, strings.NewReader(`{"tool_use_id":"t"}`), &out)
			}()
			select {
			case paused := <-done:
				if !paused {
					t.Fatal("an object at the pause path must pause")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the gate blocked on the object at the flag path")
			}
		})
	}
}

func TestUnreadableStateDirectoryIsFailOpen(t *testing.T) {
	stateEnv(t)
	p := flagPath(t)
	if err := os.WriteFile(p, []byte(`{"control_id":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	var out bytes.Buffer
	if Gate(agent, strings.NewReader(`{}`), &out) || out.Len() != 0 {
		t.Fatal("an unreadable state directory is documented fail-open")
	}
}
