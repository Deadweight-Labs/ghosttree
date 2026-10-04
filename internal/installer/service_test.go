package installer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ctlLog struct {
	calls []string
	fail  map[string]error
	state map[string]bool
}

func (l *ctlLog) run(name string, args ...string) error {
	call := name + " " + strings.Join(args, " ")
	l.calls = append(l.calls, call)
	for needle, err := range l.fail {
		if strings.Contains(call, needle) {
			return err
		}
	}
	return nil
}

func TestWatchServiceLinuxWritesUnitAndEnables(t *testing.T) {
	home := t.TempDir()
	ctx := fakeBinary(t, filepath.Join(home, ".local", "bin", "ctx"))
	useCtx(t, ctx)
	log := &ctlLog{}
	defer SetServiceBackend("linux", log.run, 1000)()

	changes, err := InstallWatchService(home)
	if err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "ghosttree-watch.service")
	raw, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ExecStart="+ctx+" watch\n") || !strings.Contains(string(raw), "WantedBy=default.target") {
		t.Fatalf("unit:\n%s", raw)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now ghosttree-watch.service"}
	if strings.Join(log.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", log.calls)
	}
	if changes[0].Action != "created" || changes[len(changes)-1].Action != "collector running" {
		t.Fatalf("changes = %+v", changes)
	}

	// Idempotent: same definition, no reload and no restart.
	log.calls = nil
	changes, err = InstallWatchService(home)
	if err != nil {
		t.Fatal(err)
	}
	if changes[0].Action != "unchanged" || strings.Join(log.calls, "|") != "systemctl --user enable --now ghosttree-watch.service" {
		t.Fatalf("second run: changes=%+v calls=%v", changes, log.calls)
	}

	// A new binary path updates the existing unit and restarts the service.
	log.calls = nil
	useCtx(t, fakeBinary(t, filepath.Join(home, "elsewhere", "ctx")))
	changes, err = InstallWatchService(home)
	if err != nil || changes[0].Action != "updated" || len(log.calls) != 3 {
		t.Fatalf("update: err=%v changes=%+v calls=%v", err, changes, log.calls)
	}
}

func TestWatchServiceQuotesSpacesAndPercent(t *testing.T) {
	if got := watchUnitText("/a b/c%d/ctx"); !strings.Contains(got, `ExecStart="/a b/c%%d/ctx" watch`) {
		t.Fatalf("unit:\n%s", got)
	}
}

func TestWatchServiceWithoutUserBusIsNotAnError(t *testing.T) {
	home := t.TempDir()
	useCtx(t, fakeBinary(t, filepath.Join(home, "ctx")))
	log := &ctlLog{fail: map[string]error{"daemon-reload": errors.New("Failed to connect to bus")}}
	defer SetServiceBackend("linux", log.run, 1000)()
	changes, err := InstallWatchService(home)
	if err != nil {
		t.Fatal(err)
	}
	last := changes[len(changes)-1].Action
	if !strings.Contains(last, "not started") || !strings.Contains(last, "systemctl --user enable --now ghosttree-watch.service") {
		t.Fatalf("last change = %q", last)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".config", "systemd", "user", "ghosttree-watch.service")); statErr != nil {
		t.Fatal("unit was not written")
	}
}

func TestWatchServiceDarwinWritesLaunchAgent(t *testing.T) {
	home := t.TempDir()
	ctx := fakeBinary(t, filepath.Join(home, "Applications", "a&b", "ctx"))
	useCtx(t, ctx)
	log := &ctlLog{fail: map[string]error{"print": errors.New("not loaded")}}
	defer SetServiceBackend("darwin", log.run, 501)()
	if _, err := InstallWatchService(home); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.deadweightlabs.ghosttree-watch.plist")
	raw, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "a&amp;b/ctx</string>") || !strings.Contains(string(raw), "<string>watch</string>") || !strings.Contains(string(raw), "<key>RunAtLoad</key>") {
		t.Fatalf("plist:\n%s", raw)
	}
	if got := log.calls[len(log.calls)-1]; got != "launchctl bootstrap gui/501 "+plist {
		t.Fatalf("calls = %v", log.calls)
	}

	// Loaded and unchanged: nothing to do.
	log.fail = nil
	log.calls = nil
	if _, err := InstallWatchService(home); err != nil {
		t.Fatal(err)
	}
	if len(log.calls) != 1 || !strings.HasPrefix(log.calls[0], "launchctl print") {
		t.Fatalf("calls = %v", log.calls)
	}
	// Changed while loaded: bootout, then bootstrap.
	log.calls = nil
	useCtx(t, fakeBinary(t, filepath.Join(home, "other", "ctx")))
	if _, err := InstallWatchService(home); err != nil {
		t.Fatal(err)
	}
	if len(log.calls) != 3 || !strings.Contains(log.calls[1], "bootout") || !strings.Contains(log.calls[2], "bootstrap") {
		t.Fatalf("calls = %v", log.calls)
	}
}

func TestWatchServiceNeedsAbsoluteCtx(t *testing.T) {
	home := t.TempDir()
	log := &ctlLog{}
	defer SetServiceBackend("linux", log.run, 1000)()
	changes, err := InstallWatchService(home)
	if err != nil || len(log.calls) != 0 || !strings.Contains(changes[0].Action, "skipped") {
		t.Fatalf("err=%v calls=%v changes=%+v", err, log.calls, changes)
	}
}

func TestSystemdUnitEscapesDollarAndPercent(t *testing.T) {
	got := watchUnitText(`/home/a b/$HOME/50%/ctx`)
	want := `ExecStart="/home/a b/$$HOME/50%%/ctx" watch`
	if !strings.Contains(got, want) {
		t.Errorf("unit lacks %q:\n%s", want, got)
	}
}

func TestWatchServiceRefusesControlCharactersInThePath(t *testing.T) {
	restore := SetServiceBackend("linux", func(string, ...string) error { return nil }, 1000)
	defer restore()
	home := t.TempDir()
	useCtx(t, "/opt/ctx\nExecStartPre=/bin/evil")
	if _, err := InstallWatchService(home); err == nil {
		t.Fatal("a newline in the ctx path was written into the unit")
	}
	if _, err := os.Stat(WatchServicePath(home)); err == nil {
		t.Error("unit file written")
	}
}

func TestWatchServiceLeavesAForeignDefinitionAlone(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		home := t.TempDir()
		useCtx(t, fakeBinary(t, filepath.Join(home, ".local", "bin", "ctx")))
		log := &ctlLog{}
		restore := SetServiceBackend(goos, log.run, 1000)
		path := WatchServicePath(home)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		foreign := "[Service]\nExecStart=/usr/bin/my-own-watcher --fast\n"
		if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := InstallWatchService(home)
		restore()
		if err == nil || !strings.Contains(err.Error(), "not written by ghosttree") {
			t.Fatalf("%s: err = %v", goos, err)
		}
		if got, _ := os.ReadFile(path); string(got) != foreign {
			t.Errorf("%s: foreign definition overwritten:\n%s", goos, got)
		}
		if len(log.calls) != 0 {
			t.Errorf("%s: service manager called: %v", goos, log.calls)
		}
	}
}

func TestWatchServiceUpdatesItsOwnDefinitionForAnotherPathOnBothPlatforms(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		home := t.TempDir()
		log := &ctlLog{}
		restore := SetServiceBackend(goos, log.run, 1000)
		useCtx(t, fakeBinary(t, filepath.Join(home, "a b", "$x", "ctx")))
		if _, err := InstallWatchService(home); err != nil {
			t.Fatal(err)
		}
		useCtx(t, fakeBinary(t, filepath.Join(home, ".local", "bin", "ctx")))
		changes, err := InstallWatchService(home)
		restore()
		if err != nil || changes[0].Action != "updated" {
			t.Fatalf("%s: err=%v changes=%+v", goos, err, changes)
		}
	}
}
