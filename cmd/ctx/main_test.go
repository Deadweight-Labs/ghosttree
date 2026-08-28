package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/sessiondistill"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestReviewActivationSeparatesScopeAndGates(t *testing.T) {
	var out bytes.Buffer
	writePendingEntry(&out, client.PendingEntry{Knowledge: store.Knowledge{
		ID: 7, Type: "instruction", Title: "core rules", Confidence: "staged",
		Scope:      scope.Axes{Project: "github.com/x/y"},
		Activation: activation.Rule{Paths: []string{"core/**"}},
	}})
	got := out.String()
	for _, want := range []string{"scope: project=github.com/x/y", "activation: paths:core/**"} {
		if !strings.Contains(got, want) {
			t.Errorf("review output missing %q:\n%s", want, got)
		}
	}
}

func TestReviewPrintsMigrationEvidence(t *testing.T) {
	var out bytes.Buffer
	writePendingEntry(&out, client.PendingEntry{Knowledge: store.Knowledge{ID: 8, Type: "decision", Title: "migrated", Confidence: "quarantined"}, MigrationEvidence: &store.MigrationEvidence{RunID: 3, Source: "AGENTS.md", Digest: "abc", ItemKey: "item", Quote: "keep this"}})
	for _, want := range []string{"migration: AGENTS.md sha256:abc", "run 3", "source quote: keep this"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("review output missing %q:\n%s", want, out.String())
		}
	}
}

func TestMigrateRejectsBadArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cases := map[string][]string{"no repo": {"migrate", filepath.Join(home, "does-not-exist")}, "too many": {"migrate", ".", "."}}
	for name, args := range cases {
		var out bytes.Buffer
		if code := run(args, &out); code == 0 {
			t.Errorf("%s: exit code=0", name)
		}
		if strings.Contains(out.String(), "unknown command") {
			t.Errorf("%s: migrate not wired: %s", name, out.String())
		}
	}
}

func TestDistillSessionsRejectsUnsafeScheduleArguments(t *testing.T) {
	for _, args := range [][]string{{"distill-sessions", "--idle", "0s"}, {"distill-sessions", "--limit", "0"}} {
		var out bytes.Buffer
		if code := run(args, &out); code != 2 {
			t.Errorf("args=%v code=%d output=%s", args, code, out.String())
		}
	}
}

func TestCleanRefusesWithoutMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# rules"), 0o644)
	var out bytes.Buffer
	if code := run([]string{"migrate", "--clean", repo}, &out); code == 0 {
		t.Error("clean without migration succeeded")
	}
	if _, err := os.Stat(filepath.Join(repo, "CLAUDE.md")); err != nil {
		t.Error("source was removed")
	}
}

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "ctx ") {
		t.Errorf("output %q should contain version string", out.String())
	}
}

func TestExportRejectsBadArguments(t *testing.T) {
	cases := map[string][]string{
		"no session id":    {"export"},
		"not a number":     {"export", "abc"},
		"too many session": {"export", "1", "2"},
	}
	for name, args := range cases {
		var out bytes.Buffer
		code := run(args, &out)
		if code == 0 {
			t.Errorf("%s: exit code = 0, want non-zero", name)
		}
		// Without this the test would pass purely because "export" is an
		// unknown command, proving nothing about argument handling.
		if got := out.String(); strings.Contains(got, "unknown command") {
			t.Errorf("%s: export is not wired up: %s", name, got)
		} else if !strings.Contains(got, "usage: ctx export") {
			t.Errorf("%s: want export usage, got %q", name, got)
		}
	}
}

// The usage line promises "ctx export <session-id> [-o file]", so flags have
// to be accepted after the id even though flag.Parse stops at the first
// positional argument.
func TestExportAcceptsFlagAfterSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	run([]string{"export", "1137", "-o", filepath.Join(home, "x.jsonl")}, &out)
	if strings.Contains(out.String(), "usage: ctx export") {
		t.Errorf("flag after session id must parse, got:\n%s", out.String())
	}
}

func TestDoctorReportsUnwiredHarnesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	if code := run([]string{"doctor"}, &out); code == 0 {
		t.Errorf("nothing is wired up, doctor must exit non-zero:\n%s", out.String())
	}
	got := out.String()
	for _, want := range []string{"claude mcp registration", "codex mcp registration", "fix:"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output missing %q:\n%s", want, got)
		}
	}
}

func TestDoctorFixInstallsHarnesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var fixed bytes.Buffer
	run([]string{"doctor", "--fix"}, &fixed)

	var after bytes.Buffer
	run([]string{"doctor"}, &after)
	for _, name := range []string{"claude mcp registration", "codex rule section"} {
		if failed(after.String(), name) {
			t.Errorf("after --fix, %q should pass:\n%s", name, after.String())
		}
	}
}

func TestInstallAcceptsRepeatedOnlyAfterHarness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	if code := run([]string{"install", "codex", "--only", "mcp", "--only", "hooks"}, &out); code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("rules were installed outside selection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skills were installed outside selection: %v", err)
	}
}

func TestInstallRejectsUnsupportedComponent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	if code := run([]string{"install", "opencode", "--only", "hooks"}, &out); code != 2 {
		t.Fatalf("exit code = %d, want 2:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "mcp, rules") {
		t.Fatalf("supported components missing:\n%s", out.String())
	}
}

func TestDoctorAcceptsHarnessAndOnlyAfterIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	run([]string{"doctor", "codex", "--only", "hooks"}, &out)
	got := out.String()
	if strings.Contains(got, "claude ") || strings.Contains(got, "opencode ") || strings.Contains(got, "codex mcp") {
		t.Fatalf("doctor ignored selected scope:\n%s", got)
	}
	if !strings.Contains(got, "codex session-start hook") {
		t.Fatalf("selected hooks missing:\n%s", got)
	}
}

func TestDoctorPrintsUnverifiedWithoutFailingForIt(t *testing.T) {
	checks := []installer.Check{{Name: "observed activity", Unverified: true, Detail: "no real event yet"}}
	var out bytes.Buffer
	if ok := printDoctorChecks(&out, checks); !ok {
		t.Fatal("UNVERIFIED must not make Doctor fail")
	}
	if !strings.Contains(out.String(), "UNVERIFIED") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDoctorReportsCheckedInSpecsAndPlans(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"docs/superpowers/specs/design.md",
		"docs/team/plans/release.md",
		"docs/architecture.md",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# document\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := checkedInDocumentCheck(root)
	if check.OK || !strings.Contains(check.Detail, "2") || !strings.Contains(check.Fix, "ctx doc import") {
		t.Fatalf("check = %+v", check)
	}
}

func TestDoctorOnlyHooksWithoutHarnessSkipsUnsupportedHarnesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var out bytes.Buffer
	run([]string{"doctor", "--only", "hooks"}, &out)
	got := out.String()
	if strings.Contains(got, "does not support") || strings.Contains(got, "opencode ") {
		t.Fatalf("all-harness hook scope included unsupported opencode:\n%s", got)
	}
	for _, want := range []string{"claude session-start hook", "codex session-start hook"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestDoctorOnlyCollectorRunsNoHarnessChecks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var out bytes.Buffer
	run([]string{"doctor", "--only", "collector"}, &out)
	got := out.String()
	if !strings.Contains(got, "collector") {
		t.Fatalf("collector checks missing:\n%s", got)
	}
	for _, unwanted := range []string{"claude mcp", "claude session", "codex mcp", "codex session", "opencode mcp", "client config"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("collector-only output contains %q:\n%s", unwanted, got)
		}
	}
}

func TestDoctorScopedFixLeavesUnselectedComponentUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	selected := installer.ComponentSet{installer.ComponentMCP: true, installer.ComponentRules: true}
	if _, err := installer.InstallSelected("codex", home, selected); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.WriteFile(mcpPath, []byte("[mcp_servers.ghosttree]\ncommand = \"wrong\"\nargs = [\"mcp\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rulePath := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.WriteFile(rulePath, []byte("outdated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	run([]string{"doctor", "codex", "--only", "rules", "--fix"}, &out)
	if !installer.HasMarker(rulePath) {
		t.Fatalf("selected rule was not repaired:\n%s", out.String())
	}
	raw, _ := os.ReadFile(mcpPath)
	if !strings.Contains(string(raw), `command = "wrong"`) {
		t.Fatalf("unselected MCP config was changed:\n%s", raw)
	}
}

func TestCodexInstallNamesHooksAndScopedDoctorNextSteps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var out bytes.Buffer
	if code := run([]string{"install", "codex", "--only", "hooks"}, &out); code != 0 {
		t.Fatalf("exit = %d: %s", code, out.String())
	}
	for _, want := range []string{"/hooks", "fresh", "ctx doctor codex --only hooks"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("install output missing %q:\n%s", want, out.String())
		}
	}
}

// failed reports whether the doctor line for a check is marked FAIL.
func failed(output, checkName string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, checkName) {
			return strings.Contains(line, "FAIL")
		}
	}
	return true // check never appeared at all
}

func TestReviewRejectsBadArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cases := map[string][]string{
		"unknown subcommand": {"review", "bogus"},
		"approve without id": {"review", "approve"},
		"non-numeric id":     {"review", "approve", "abc"},
	}
	for name, args := range cases {
		var out bytes.Buffer
		if code := run(args, &out); code == 0 {
			t.Errorf("%s: exit code = 0, want non-zero", name)
		}
		if got := out.String(); strings.Contains(got, "unknown command") {
			t.Errorf("%s: review is not wired up: %s", name, got)
		} else if !strings.Contains(got, "usage: ctx review") {
			t.Errorf("%s: want review usage, got %q", name, got)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"nope"}, &out); code == 0 {
		t.Error("unknown command should return non-zero")
	}
}

// Releasing the current version would put every processed session back in the
// queue at once, which is the blind reprocessing the version exists to prevent.
func TestDistillSessionsRefusesToReleaseTheCurrentPromptVersion(t *testing.T) {
	dir := t.TempDir()
	// Isolate the configuration: without this the lookup falls back to the
	// operator's own home and a failure prints their live API key.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty"))
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(dir, "absent.json"))
	var out bytes.Buffer
	code := cmdDistillSessions([]string{
		"--db", filepath.Join(dir, "t.db"),
		"--reprocess-version", sessiondistill.PromptVersion, "--dry-run",
	}, &out)
	if code == 0 {
		t.Fatalf("releasing the current version was accepted: %s", out.String())
	}
	if !strings.Contains(out.String(), "current prompt version") {
		t.Fatalf("output does not explain the refusal: %s", out.String())
	}
}
