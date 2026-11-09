package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakeDue struct {
	jobs     []store.GardenJob
	started  []string
	finished map[int64]string
	costs    map[int64]float64
}

func (f *fakeDue) GardenDue() ([]store.GardenJob, error) { return f.jobs, nil }
func (f *fakeDue) StartGardenRun(org int64, project string) (int64, error) {
	if project == "github.com/acme/refused" {
		return 0, errors.New("refused")
	}
	f.started = append(f.started, project)
	return int64(len(f.started)), nil
}
func (f *fakeDue) FinishGardenRun(id int64, state string, cost float64, note string) error {
	f.finished[id], f.costs[id] = state, cost
	return nil
}

func TestRunDueRunsEachDueRepositoryWithTheServersBudget(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "acme", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeDue{finished: map[int64]string{}, costs: map[int64]float64{},
		jobs: []store.GardenJob{{OrgID: 1, Slug: "acme", Projects: []string{"github.com/acme/app", "github.com/acme/missing", "github.com/acme/refused"}, BudgetRun: 3, BudgetMon: 90}}}
	var calls [][]string
	spent := int64(0)
	run := func(args []string) int {
		calls = append(calls, args)
		spent += 1_250_000
		return 0
	}
	code := runGardenDue(f, dir, "", func() (int64, error) { return spent, nil }, run, io.Discard)
	if code != 1 { // a missing checkout and a refused run count as failures
		t.Fatalf("exit = %d", code)
	}
	if len(calls) != 1 || !slices.Contains(calls[0], "github.com/acme/app") || !slices.Contains(calls[0], "3.00") || slices.Contains(calls[0], "--monthly-usd") {
		t.Fatalf("calls = %v", calls)
	}
	if f.finished[1] != "done" || f.costs[1] != 1.25 || f.finished[2] != "failed" {
		t.Fatalf("finished = %v costs = %v", f.finished, f.costs)
	}
	// Nothing due: nothing runs.
	calls = nil
	if code := runGardenDue(&fakeDue{finished: map[int64]string{}, costs: map[int64]float64{}}, dir, "", func() (int64, error) { return 0, nil }, run, io.Discard); code != 0 || calls != nil {
		t.Fatalf("idle run: %d %v", code, calls)
	}
	if _, ok := gardenCheckout(dir, "github.com/../etc"); ok {
		t.Fatal("a path outside the folder was accepted")
	}
}

func TestRunDueRefusesFlagsTheServerSets(t *testing.T) {
	if code := cmdGardenRunDue([]string{"--repos-dir", "/x", "--state-file", "/x/s", "--", "--max-usd", "100"}, io.Discard); code != 2 {
		t.Fatalf("exit = %d", code)
	}
	// Without the state file the cost of a run cannot be measured.
	if code := cmdGardenRunDue([]string{"--repos-dir", "/x"}, io.Discard); code != 2 {
		t.Fatalf("without --state-file: exit = %d", code)
	}
}

// Security review 5: each namespace has its own month, checked before every
// repository; a run never gets more than the rest of it.
func TestRunDueKeepsTheMonthOfEachNamespace(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"acme/a", "acme/b", "acme/c", "lab/x"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(p)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeDue{finished: map[int64]string{}, costs: map[int64]float64{}, jobs: []store.GardenJob{
		{OrgID: 1, Slug: "acme", Projects: []string{"github.com/acme/a", "github.com/acme/b", "github.com/acme/c"}, BudgetRun: 3, BudgetMon: 10, SpentMon: 6},
		{OrgID: 2, Slug: "lab", Projects: []string{"github.com/lab/x"}, BudgetRun: 3, BudgetMon: 10, SpentMon: 0},
	}}
	var calls [][]string
	spent := int64(0)
	run := func(args []string) int {
		calls = append(calls, args)
		spent += 3_000_000 // every run spends 3 USD
		return 0
	}
	runGardenDue(f, dir, "", func() (int64, error) { return spent, nil }, run, io.Discard)
	// acme: 4 left -> a gets 3, b the last 1, c nothing; lab its own full 3.
	want := [][2]string{{"github.com/acme/a", "3.00"}, {"github.com/acme/b", "1.00"}, {"github.com/lab/x", "3.00"}}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for i, w := range want {
		if !slices.Contains(calls[i], w[0]) || !slices.Contains(calls[i], w[1]) {
			t.Errorf("call %d = %v, want %v", i, calls[i], w)
		}
	}
	if slices.Contains(f.started, "github.com/acme/c") {
		t.Error("a run started with the month spent")
	}
}

// An unreadable state file stops run-due before any run: a cost it cannot
// measure would count as nothing (review point 8).
func TestRunDueStopsOnAnUnreadableStateFile(t *testing.T) {
	f := &fakeDue{finished: map[int64]string{}, costs: map[int64]float64{},
		jobs: []store.GardenJob{{OrgID: 1, Slug: "acme", Projects: []string{"github.com/acme/app"}, BudgetRun: 3, BudgetMon: 90}}}
	ran := false
	code := runGardenDue(f, t.TempDir(), "", func() (int64, error) { return 0, errors.New("broken") }, func([]string) int { ran = true; return 0 }, io.Discard)
	if code != 1 || ran || len(f.started) != 0 {
		t.Fatalf("exit %d ran %v started %v", code, ran, f.started)
	}
}
