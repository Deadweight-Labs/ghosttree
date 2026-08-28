package installer

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/hookstate"
)

func TestCodexEffectiveMCPConfigReportsVendorParserFailure(t *testing.T) {
	oldProbe := codexEffectiveProbe
	codexEffectiveProbe = func(string) (bool, error) { return true, errors.New("invalid TOML") }
	defer func() { codexEffectiveProbe = oldProbe }()
	c := codexEffectiveMCPCheck(t.TempDir(), true)
	if c.OK || c.Unverified || !strings.Contains(c.Detail, "invalid TOML") {
		t.Fatalf("check = %+v", c)
	}
}

func TestVerifySelectedChecksHookInstalledRunnableAndActiveSeparately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	selected := ComponentSet{ComponentHooks: true}
	if _, err := InstallSelected("codex", home, selected); err != nil {
		t.Fatal(err)
	}
	oldProbe := hookCommandProbe
	hookCommandProbe = func(command, event string) error { return nil }
	defer func() { hookCommandProbe = oldProbe }()

	checks := VerifySelected("codex", home, selected)
	for _, event := range []string{"session-start", "user-prompt-submit", "pre-tool-use"} {
		for _, kind := range []string{"hook", "runnable", "active"} {
			if findNamedCheck(checks, event, kind) == nil {
				t.Errorf("missing %s %s check: %+v", event, kind, checks)
			}
		}
		active := findNamedCheck(checks, event, "active")
		if active == nil || !active.Unverified || active.OK {
			t.Errorf("unseen activity = %+v, want UNVERIFIED", active)
		}
	}

	if err := hookstate.RecordAt("codex", "PreToolUse", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	checks = VerifySelected("codex", home, selected)
	active := findNamedCheck(checks, "pre-tool-use", "active")
	if active == nil || !active.OK || active.Unverified {
		t.Fatalf("observed activity = %+v, want OK", active)
	}
}

func TestVerifySelectedDoesNotTreatStaleHookActivityAsActive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	selected := ComponentSet{ComponentHooks: true}
	if _, err := InstallSelected("codex", home, selected); err != nil {
		t.Fatal(err)
	}
	oldProbe := hookCommandProbe
	hookCommandProbe = func(command, event string) error { return nil }
	defer func() { hookCommandProbe = oldProbe }()
	if err := hookstate.RecordAt("codex", "PreToolUse", time.Now().UTC().Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	active := findNamedCheck(VerifySelected("codex", home, selected), "pre-tool-use", "active")
	if active == nil || active.OK || !active.Unverified || !strings.Contains(active.Detail, "stale") {
		t.Fatalf("stale activity = %+v", active)
	}
}

func findNamedCheck(checks []Check, parts ...string) *Check {
	for i := range checks {
		match := true
		for _, part := range parts {
			if !strings.Contains(checks[i].Name, part) {
				match = false
			}
		}
		if match {
			return &checks[i]
		}
	}
	return nil
}
