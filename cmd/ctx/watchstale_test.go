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

func init() {
	watchUnitState = func() unitState { return unitState{} }
}

func mockUnit(t *testing.T, u unitState) {
	t.Helper()
	old := watchUnitState
	watchUnitState = func() unitState { return u }
	t.Cleanup(func() { watchUnitState = old })
}

func TestParseUnitShow(t *testing.T) {
	u := parseUnitShow("MainPID=4242\nActiveState=active\nExecStart={ path=/home/x/.local/bin/ctx ; argv[]=/home/x/.local/bin/ctx watch ; ignore_errors=no }\n")
	if !u.Active || u.MainPID != 4242 || u.ExecPath != "/home/x/.local/bin/ctx" {
		t.Fatalf("u = %+v", u)
	}
}

// Das ctx auf dem PATH ist nicht maßgeblich, sondern das, was die Unit startet.
func TestDoctorComparesWithTheUnitsExecStartNotThePath(t *testing.T) {
	installedBinary(t) // a different ctx on PATH
	unitBin := filepath.Join(t.TempDir(), "unit-ctx")
	if err := os.WriteFile(unitBin, []byte("unit"), 0o755); err != nil {
		t.Fatal(err)
	}
	pid := fakeWatch(t, unitBin)
	mockUnit(t, unitState{Active: true, MainPID: pid, ExecPath: unitBin})
	c, ok := watchBinaryCheck()
	if !ok || !c.OK {
		t.Fatalf("false warning against the PATH ctx: %+v ok=%v", c, ok)
	}
}

// Die pid-Datei nennt einen Prozess, der nicht mehr der Watch ist; die Unit läuft
// mit einer anderen pid und dem richtigen Binary: keine Warnung.
func TestDoctorIgnoresAStalePidFileWhileTheUnitRuns(t *testing.T) {
	bin := installedBinary(t)
	fakeWatch(t, "/usr/bin/something (deleted)") // pid file points at an unrelated process
	proc := procRoot
	unitPID := os.Getpid() + 100000
	if err := os.MkdirAll(filepath.Join(proc, strconv.Itoa(unitPID)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, filepath.Join(proc, strconv.Itoa(unitPID), "exe")); err != nil {
		t.Fatal(err)
	}
	mockUnit(t, unitState{Active: true, MainPID: unitPID, ExecPath: bin})
	c, ok := watchBinaryCheck()
	if !ok || !c.OK || !strings.Contains(c.Detail, strconv.Itoa(unitPID)) {
		t.Fatalf("check = %+v ok=%v", c, ok)
	}
}

// Ohne Unit: ein Prozess aus der pid-Datei, der kein "watch" ist (wiederverwendete
// pid), gilt nicht als Collector.
func TestDoctorIgnoresAPidFileProcessThatIsNotAWatch(t *testing.T) {
	installedBinary(t)
	pid := fakeWatch(t, "/usr/bin/other (deleted)")
	if err := os.WriteFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"), []byte("/usr/bin/other\x00--x\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := watchBinaryCheck(); ok {
		t.Fatal("a reused pid was taken for the watch")
	}
}
