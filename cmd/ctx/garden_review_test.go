package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Point 3: a checkout of another repository is refused, not compared.
func TestGardenRefusesACheckoutOfAnotherProject(t *testing.T) {
	repo := gardenRepo(t)
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", repo.Root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := verifyCheckout(repo.Root, gardenProject); err == nil {
		t.Fatal("a checkout without origin was accepted")
	}
	run("remote", "add", "origin", "https://github.com/other/thing.git")
	if err := verifyCheckout(repo.Root, gardenProject); err == nil || !strings.Contains(err.Error(), "other/thing") {
		t.Fatalf("wrong origin accepted or unexplained: %v", err)
	}
	run("remote", "set-url", "origin", "git@github.com:O/R.git")
	if err := verifyCheckout(repo.Root, gardenProject); err != nil {
		t.Fatalf("matching origin (other spelling) refused: %v", err)
	}
}

// Point 4: NaN and Inf must not turn the budget off.
func TestGardenRejectsNonFiniteBudget(t *testing.T) {
	for _, v := range []string{"NaN", "Inf", "+Inf", "-1", "0"} {
		var out bytes.Buffer
		if code := cmdGarden([]string{"check-knowledge", "--dry-run", "--max-usd", v}, &out); code != 2 {
			t.Errorf("--max-usd %s: exit %d, want 2 (%s)", v, code, out.String())
		}
	}
}

type failingSink struct{ gardenFakeStore }

func (f *failingSink) Remember(store.Knowledge, scope.Axes) (store.Knowledge, error) {
	return store.Knowledge{}, errors.New("server down")
}

// Point 6 and the file mode: the report is on disk even if proposing fails,
// and only its owner can read it.
func TestGardenWritesTheReportBeforeProposingAndPrivately(t *testing.T) {
	st := &failingSink{gardenFakeStore{entries: gardenEntries()}}
	out := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil { // an existing, looser file
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 1, Propose: true}
	code := runGardenCheck(st, gardenRepo(t), gardenProject, opts, &gardenFakeModel{}, gardenPrice, out, &stdout)
	if code != 1 {
		t.Fatalf("exit %d, want 1 for the failed proposal\n%s", code, stdout.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(raw), "veraltet") {
		t.Fatalf("report lost when proposing failed: %v %q", err, raw)
	}
	info, _ := os.Stat(out)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("report mode %v, want 0600", info.Mode().Perm())
	}
}
