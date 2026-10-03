package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeWatch richtet ein Scheinverzeichnis /proc mit einem Prozess ein, dessen
// exe auf target zeigt, und ein HOME mit passender pid-Datei (der Test-Prozess
// selbst als "Watch-Prozess", damit watchProcess ihn als laufend erkennt).
func fakeWatch(t *testing.T, exeTarget string) int {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	pid := os.Getpid()
	if err := os.MkdirAll(filepath.Dir(pidFilePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	proc := t.TempDir()
	old := procRoot
	procRoot = proc
	t.Cleanup(func() { procRoot = old })
	if err := os.MkdirAll(filepath.Join(proc, strconv.Itoa(pid)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exeTarget, filepath.Join(proc, strconv.Itoa(pid), "exe")); err != nil {
		t.Fatal(err)
	}
	return pid
}

func installedBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ctx")
	if err := os.WriteFile(bin, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return bin
}

func TestDoctorWarnsAboutADeletedWatchBinary(t *testing.T) {
	installedBinary(t)
	fakeWatch(t, "/home/x/.local/bin/ctx (deleted)")
	c, ok := watchBinaryCheck()
	if !ok || c.OK || !strings.Contains(c.Detail, "outdated") || c.Fix == "" {
		t.Fatalf("check = %+v ok=%v", c, ok)
	}
}

func TestDoctorWarnsAboutAWatchBinaryThatIsNotTheInstalledOne(t *testing.T) {
	installedBinary(t)
	other := filepath.Join(t.TempDir(), "old-ctx")
	if err := os.WriteFile(other, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeWatch(t, other)
	c, _ := watchBinaryCheck()
	if c.OK || !strings.Contains(c.Detail, "not the installed") {
		t.Fatalf("check = %+v", c)
	}
}

func TestDoctorAcceptsAWatchRunningTheInstalledBinary(t *testing.T) {
	bin := installedBinary(t)
	fakeWatch(t, bin)
	c, ok := watchBinaryCheck()
	if !ok || !c.OK {
		t.Fatalf("check = %+v ok=%v", c, ok)
	}
}

func TestInstallRestartsAStaleWatchUnitOnly(t *testing.T) {
	bin := installedBinary(t)
	var calls [][]string
	old := systemctlUser
	systemctlUser = func(args ...string) error { calls = append(calls, args); return nil }
	t.Cleanup(func() { systemctlUser = old })

	// Current binary: nothing happens.
	fakeWatch(t, bin)
	var out bytes.Buffer
	restartStaleWatch(&out)
	if len(calls) != 0 {
		t.Fatalf("restarted a current watch: %v", calls)
	}

	// Replaced binary: the user unit is restarted.
	fakeWatch(t, bin+" (deleted)")
	restartStaleWatch(&out)
	if len(calls) != 2 || calls[1][0] != "restart" || calls[1][1] != watchUnit {
		t.Fatalf("calls = %v", calls)
	}
	if !strings.Contains(out.String(), "restarted") {
		t.Errorf("output = %q", out.String())
	}
}

func TestInstallLeavesAHandStartedWatchAlone(t *testing.T) {
	bin := installedBinary(t)
	var calls [][]string
	old := systemctlUser
	systemctlUser = func(args ...string) error {
		calls = append(calls, args)
		if args[0] == "is-active" {
			return os.ErrNotExist // the unit is not running
		}
		return nil
	}
	t.Cleanup(func() { systemctlUser = old })
	fakeWatch(t, bin+" (deleted)")
	var out bytes.Buffer
	restartStaleWatch(&out)
	for _, c := range calls {
		if c[0] == "restart" {
			t.Fatalf("restarted without an active unit: %v", calls)
		}
	}
	if !strings.Contains(out.String(), "outdated") {
		t.Errorf("output = %q", out.String())
	}
}

// Kein Test dieses Pakets darf je das echte systemctl des Rechners rufen.
func init() {
	systemctlUser = func(args ...string) error { return os.ErrPermission }
}
