package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func harnessesPresent(t *testing.T, present ...string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := doctorHarnessPresent
	t.Cleanup(func() { doctorHarnessPresent = old })
	doctorHarnessPresent = func(h, _ string) bool {
		for _, p := range present {
			if p == h {
				return true
			}
		}
		return false
	}
}

func harnessLines(out, harness string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, harness+" ") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestDoctorWithoutArgumentSkipsHarnessesThatAreNotInstalled(t *testing.T) {
	harnessesPresent(t)
	var out bytes.Buffer
	cmdDoctor(nil, &out)
	text := out.String()
	for _, h := range []string{"claude", "codex", "opencode"} {
		if !strings.Contains(text, h+": not installed (skipped)") {
			t.Errorf("no skip line for %s:\n%s", h, text)
		}
		if l := harnessLines(text, h); len(l) != 0 {
			t.Errorf("%s was checked although it is not installed: %v", h, l)
		}
	}
}

func TestDoctorWithoutArgumentStillChecksWhatIsInstalled(t *testing.T) {
	harnessesPresent(t, "claude")
	var out bytes.Buffer
	cmdDoctor(nil, &out)
	text := out.String()
	if len(harnessLines(text, "claude")) == 0 || strings.Contains(text, "claude: not installed") {
		t.Fatalf("installed claude not checked:\n%s", text)
	}
	if !strings.Contains(text, "codex: not installed (skipped)") || len(harnessLines(text, "codex")) != 0 {
		t.Fatalf("codex not skipped:\n%s", text)
	}
}

func TestDoctorWithExplicitHarnessChecksItEvenWhenNotInstalled(t *testing.T) {
	harnessesPresent(t)
	var out bytes.Buffer
	code := cmdDoctor([]string{"codex"}, &out)
	text := out.String()
	if code != 1 || strings.Contains(text, "skipped") || len(harnessLines(text, "codex")) == 0 {
		t.Fatalf("exit %d, explicit harness must be checked as before:\n%s", code, text)
	}
}

func TestHarnessPresentSeesTheConfigDirectoryOrTheCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	if harnessPresent("codex", home) || harnessPresent("opencode", home) {
		t.Fatal("nothing installed, but reported present")
	}
	if err := os.MkdirAll(home+"/.codex", 0o755); err != nil || os.MkdirAll(home+"/.config/opencode", 0o755) != nil {
		t.Fatal(err)
	}
	if !harnessPresent("codex", home) || !harnessPresent("opencode", home) {
		t.Fatal("config directories not recognised")
	}
}
