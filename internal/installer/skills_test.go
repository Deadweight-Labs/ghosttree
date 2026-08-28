package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func harnessFor(t *testing.T, name string) Harness {
	t.Helper()
	for _, h := range Harnesses() {
		if h.Name == name {
			return h
		}
	}
	t.Fatalf("harness %q missing", name)
	return Harness{}
}

// isolate points the manifest at a throwaway config directory. Without it the
// test would read and write the real ~/.config/ghosttree/skills.json.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func TestInstallSkillsWritesAndIsIdempotent(t *testing.T) {
	home := isolate(t)
	h := harnessFor(t, "claude")

	changes, err := installSkills(h, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 {
		t.Fatal("first install wrote nothing")
	}
	for _, rel := range []string{
		filepath.Join("ghosttree-setup", "SKILL.md"),
		filepath.Join("ghosttree-onboard-repo", "SKILL.md"),
		filepath.Join("ghosttree-onboard-repo", "references", "inventory-run.md"),
	} {
		if _, err := os.Stat(filepath.Join(home, ".claude", "skills", rel)); err != nil {
			t.Errorf("%s not written: %v", rel, err)
		}
	}

	// A second run changes nothing. Installing is repeatable; that is the
	// promise of the whole package.
	changes, err = installSkills(h, home)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Action != "unchanged" {
			t.Errorf("second install touched %s (%s)", c.Path, c.Action)
		}
	}
}

func TestInstallSkillsKeepsUserEditsAndReportsThem(t *testing.T) {
	home := isolate(t)
	h := harnessFor(t, "claude")
	if _, err := installSkills(h, home); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(home, ".claude", "skills", "ghosttree-setup", "SKILL.md")
	if err := os.WriteFile(target, []byte("my own version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := installSkills(h, home)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	// Somebody who adapted a skill to their way of working does not lose it to
	// an update — and gets told they are running their own version.
	if string(got) != "my own version\n" {
		t.Error("a user-edited skill must not be overwritten")
	}
	var reported bool
	for _, c := range changes {
		if c.Path == target && c.Action == "kept (edited locally)" {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the edit must be reported, got %+v", changes)
	}
	drift := SkillDrift(h, home)
	if len(drift) != 1 || drift[0] != target {
		t.Errorf("SkillDrift should name exactly the edited file, got %v", drift)
	}
}

// The case the manifest exists for: our own older version must be refreshed,
// while the user's version above must not. Both differ from what is shipped;
// only the manifest tells them apart.
func TestInstallSkillsUpdatesItsOwnOlderVersion(t *testing.T) {
	home := isolate(t)
	h := harnessFor(t, "claude")
	if _, err := installSkills(h, home); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".claude", "skills", "ghosttree-setup", "SKILL.md")
	shipped, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate an older shipped version: overwrite the file AND record its hash
	// as ours, which is exactly the state a previous ctx release would leave.
	older := []byte("an older shipped version\n")
	if err := os.WriteFile(target, older, 0o644); err != nil {
		t.Fatal(err)
	}
	m := readManifest()
	m[target] = sum(older)
	if err := writeManifest(m); err != nil {
		t.Fatal(err)
	}

	if _, err := installSkills(h, home); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(shipped) {
		t.Error("our own older version must be refreshed")
	}
	if drift := SkillDrift(h, home); len(drift) != 0 {
		t.Errorf("after a refresh nothing drifts, got %v", drift)
	}
}

func TestHarnessWithoutSkillsRootWritesNothing(t *testing.T) {
	home := isolate(t)
	changes, err := installSkills(Harness{Name: "nowhere"}, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("a harness without a skills root must be a no-op, got %+v", changes)
	}
	if drift := SkillDrift(Harness{Name: "nowhere"}, home); drift != nil {
		t.Errorf("and it cannot drift either, got %v", drift)
	}
}

// opencode reads no skills as far as anyone has checked. Filling a directory
// nobody reads looks like support from the outside.
func TestOpencodeGetsNoSkills(t *testing.T) {
	if root := harnessFor(t, "opencode").SkillsRoot; root != nil {
		t.Error("opencode must not declare a skills root until someone has measured one")
	}
}

func TestBothWiredHarnessesShipTheSameSource(t *testing.T) {
	home := isolate(t)
	for _, name := range []string{"claude", "codex"} {
		if _, err := installSkills(harnessFor(t, name), home); err != nil {
			t.Fatal(err)
		}
	}
	claude, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "ghosttree-setup", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	codex, err := os.ReadFile(filepath.Join(home, ".agents", "skills", "ghosttree-setup", "SKILL.md"))
	if err != nil {
		t.Fatalf("codex skill not written: %v", err)
	}
	// One canonical source, two locations. Harness-specific is where it goes
	// and how it is proven, never what it says.
	if string(claude) != string(codex) {
		t.Error("both harnesses must get byte-identical skills")
	}
}

// Eine eigene Fassung zu fahren ist erlaubt. Sie unbemerkt zu fahren ist der
// Fehler: der Nutzer glaubt dann, er bekomme Updates, und bekommt keine.
func TestVerifyReportsSkillDrift(t *testing.T) {
	home := isolate(t)
	h := harnessFor(t, "claude")
	if _, err := installSkills(h, home); err != nil {
		t.Fatal(err)
	}
	if c := skillCheck(h, "claude skills", home, "fix"); !c.OK {
		t.Fatalf("a fresh install must not drift: %+v", c)
	}

	target := filepath.Join(home, ".claude", "skills", "ghosttree-setup", "SKILL.md")
	if err := os.WriteFile(target, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := skillCheck(h, "claude skills", home, "fix")
	if c.OK {
		t.Fatal("doctor must not stay green on a locally edited skill")
	}
	if !strings.Contains(c.Detail, "ghosttree-setup") {
		t.Errorf("the finding must name the file, got %q", c.Detail)
	}
}

// Fuer eine Umgebung ohne Skill-Kanal darf die Pruefung nicht dauerhaft rot
// stehen — das erzieht dazu, rote Haken zu ueberlesen.
func TestSkillCheckIsGreenWhereThereIsNoChannel(t *testing.T) {
	home := isolate(t)
	c := skillCheck(harnessFor(t, "opencode"), "opencode skills", home, "fix")
	if !c.OK {
		t.Errorf("no skill channel is not a finding: %+v", c)
	}
}

// Ein Drift-Check allein ist auf einer Maschine gruen, auf der nie etwas
// installiert wurde — nichts weicht von nichts ab. Genau dieser gruene Haken
// ueber einem leeren Kanal liess Codex 482 Sitzungen ohne Kontext laufen.
func TestSkillCheckIsRedWhenNothingIsInstalled(t *testing.T) {
	home := isolate(t)
	c := skillCheck(harnessFor(t, "claude"), "claude skills", home, "fix")
	if c.OK {
		t.Fatalf("nothing installed must not read as fine: %+v", c)
	}
	if !strings.Contains(c.Detail, "not installed") {
		t.Errorf("the finding must say what is wrong, got %q", c.Detail)
	}
}

func TestSkillCheckRequiresManifestOwnership(t *testing.T) {
	home := isolate(t)
	h := harnessFor(t, "codex")
	if _, err := installSkills(h, home); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestPath()); err != nil {
		t.Fatal(err)
	}
	c := skillCheck(h, "codex skills", home, "fix")
	if c.OK || !strings.Contains(c.Detail, "manifest") {
		t.Fatalf("missing ownership manifest passed: %+v", c)
	}
}

func TestSkillCheckRejectsInvalidFrontmatter(t *testing.T) {
	home := isolate(t)
	h := harnessFor(t, "claude")
	if _, err := installSkills(h, home); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(h.SkillsRoot(home), "ghosttree-setup", "SKILL.md")
	if err := os.WriteFile(target, []byte("name: no-frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := skillCheck(h, "claude skills", home, "fix")
	if c.OK || !strings.Contains(c.Detail, "frontmatter") {
		t.Fatalf("invalid frontmatter finding = %+v", c)
	}
}
