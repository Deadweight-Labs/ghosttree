package garden

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// commitChange schreibt eine Zeile in internal/distill.go und legt einen Commit
// an; geliefert wird der volle Hash.
func commitChange(t *testing.T, repo *Repo, line, subject string) string {
	t.Helper()
	f := filepath.Join(repo.Root, "internal", "distill.go")
	old, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, append(old, []byte("// "+line+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", subject}} {
		cmd := exec.Command("git", append([]string{"-C", repo.Root, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := repo.git("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// Veralten wird vom Anker-Commit des Eintrags aus gemessen. Der Eintrag ist nach
// Datum uralt (alle Commits liegen danach), nach Anker aber erst vor einem
// Commit entstanden: nur der zählt.
func TestStalenessCountsFromTheAnchorCommitOfTheEntry(t *testing.T) {
	repo := newRepo(t)
	commitChange(t, repo, "zwei", "second change")
	anchor := commitChange(t, repo, "drei", "third change")
	commitChange(t, repo, "vier", "fourth change")

	e := entry(1, "`SessionDistillationExists` in internal/distill.go")
	e.AnchorCommit = anchor
	cc, err := repo.Gather(e, ContextChars)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Commits != 1 || !strings.Contains(cc.Text, "fourth change") {
		t.Fatalf("want exactly the commit after the anchor, got %d:\n%s", cc.Commits, cc.Text)
	}
	for _, old := range []string{"second change", "third change", "add distill"} {
		if strings.Contains(cc.Text, old) {
			t.Errorf("%q is not after the anchor and must not count:\n%s", old, cc.Text)
		}
	}
	if !strings.Contains(cc.Text, "Commits seit Anker "+anchor[:12]) || cc.Since != "Anker "+anchor[:12] {
		t.Errorf("the context must say what it counts from (%q):\n%s", cc.Since, cc.Text)
	}
	if len(cc.Hashes) != 1 {
		t.Errorf("a verdict may cite only the commits since the anchor: %v", cc.Hashes)
	}
}

// Steht der Anker auf HEAD, hat sich seither nichts geändert, auch wenn die Daten
// weit zurückliegen.
func TestAnAnchorAtHeadMeansNoCommitsSince(t *testing.T) {
	repo := newRepo(t)
	head := commitChange(t, repo, "zwei", "second change")
	e := entry(1, "`SessionDistillationExists` in internal/distill.go")
	e.AnchorCommit = head
	cc, err := repo.Gather(e, ContextChars)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Commits != 0 || !strings.Contains(cc.Text, "keine") {
		t.Fatalf("commits %d:\n%s", cc.Commits, cc.Text)
	}
}

// Ohne Anker, und mit einem Anker, den dieser Checkout nicht kennt, gilt wie
// bisher das Datum der Beobachtung.
func TestWithoutAUsableAnchorStalenessFallsBackToTheDate(t *testing.T) {
	repo := newRepo(t)
	commitChange(t, repo, "zwei", "second change")
	for name, anchor := range map[string]string{
		"no anchor":        "",
		"unknown commit":   strings.Repeat("a", 40),
		"not a hex string": "--all",
	} {
		e := entry(1, "`SessionDistillationExists` in internal/distill.go")
		e.AnchorCommit = anchor
		cc, err := repo.Gather(e, ContextChars)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cc.Commits != 2 || cc.Since != e.ObservedAt || !strings.Contains(cc.Text, "Commits seit "+e.ObservedAt) {
			t.Errorf("%s: commits %d since %q:\n%s", name, cc.Commits, cc.Since, cc.Text)
		}
	}
}
