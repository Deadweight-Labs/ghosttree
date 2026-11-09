package store

import (
	"errors"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"testing"
	"time"
)

func gardenFixture(t *testing.T) (*Store, Org) {
	t.Helper()
	st := orgStore(t, "robin", "anna", "gardener")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"github.com/alpha/one", "github.com/alpha/two"} {
		if _, err := st.ClaimProject("person:1", p, "alpha"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetGardenerAccount(3); err != nil {
		t.Fatal(err)
	}
	return st, o
}

func TestGardenSettingsAreTheOwnersAndValidated(t *testing.T) {
	st, o := gardenFixture(t)
	g := DefaultGardenSettings(o.ID)
	g.Enabled, g.Rules, g.Repos = true, []string{GardenRuleProof, GardenRuleProof}, []string{"github.com/alpha/one"}
	if err := st.SetGardenSettings("person:2", g); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("member sets the gardener: %v", err)
	}
	if err := st.SetGardenSettings("person:1", g); err != nil {
		t.Fatal(err)
	}
	got, ok := st.GardenSettingsFor(o.ID)
	if !ok || !got.Enabled || len(got.Rules) != 1 || !got.HasRule(GardenRuleProof) || got.HasRule(GardenRuleTest) || got.UpdatedBy != 1 {
		t.Fatalf("settings = %+v %v", got, ok)
	}
	for name, bad := range map[string]func(*GardenSettings){
		"rule":      func(g *GardenSettings) { g.Rules = []string{"merge"} },
		"repo":      func(g *GardenSettings) { g.Repos = []string{"github.com/other/x"} },
		"frequency": func(g *GardenSettings) { g.Frequency = "hourly" },
		"budget":    func(g *GardenSettings) { g.BudgetRun = -1 },
		"minute":    func(g *GardenSettings) { g.AtMinute = 1440 },
	} {
		b := got
		bad(&b)
		if err := st.SetGardenSettings("person:1", b); !errors.Is(err, ErrGardenSettings) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestGardenDueFollowsScheduleRunsAndBudget(t *testing.T) {
	st, o := gardenFixture(t)
	g := DefaultGardenSettings(o.ID)
	g.Enabled, g.AtMinute, g.BudgetMon = true, 3*60+5, 5
	// "All repositories" means those where the gardener works: none yet.
	if err := st.SetGardenSettings("person:1", g); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if jobs, _ := st.GardenDue(now); len(jobs) != 0 {
		t.Fatalf("due without any role of the gardener: %+v", jobs)
	}
	// Picking repositories gives the gardener the role member there.
	g.Repos = []string{"github.com/alpha/one", "github.com/alpha/two"}
	if err := st.SetGardenSettings("person:1", g); err != nil {
		t.Fatal(err)
	}
	if role := st.ProjectRole("github.com/alpha/one", "person:3"); role.Role != RoleMember {
		t.Fatalf("gardener role = %+v", role)
	}
	jobs, err := st.GardenDue(now)
	if err != nil || len(jobs) != 1 || len(jobs[0].Projects) != 2 || jobs[0].Slug != "alpha" {
		t.Fatalf("due = %+v %v", jobs, err)
	}
	// Only the gardener reports runs, and only for repositories of the owner.
	if _, err := st.StartGardenRun("person:1", o.ID, "github.com/alpha/one"); !errors.Is(err, ErrNotGardener) {
		t.Fatalf("person reported a run: %v", err)
	}
	if _, err := st.StartGardenRun("person:3", o.ID, "github.com/other/x"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("run in a foreign repository: %v", err)
	}
	id, err := st.StartGardenRun("person:3", o.ID, "github.com/alpha/one")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishGardenRun("person:3", id, "done", 2.5, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishGardenRun("person:3", id, "done", 1, ""); !errors.Is(err, ErrGardenAutoGone) {
		t.Fatalf("finished twice: %v", err)
	}
	// Fixed dates from here on, so the month never turns during the test.
	day := func(d, h int) time.Time { return time.Date(2026, 3, d, h, 0, 0, 0, time.UTC) }
	if _, err := st.db.Exec(`UPDATE garden_runs SET started_at=?`, day(10, 10).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// A run since the last slot: not due again until the next one.
	if jobs, _ := st.GardenDue(day(10, 12)); len(jobs) != 0 {
		t.Fatalf("due right after a run: %+v", jobs)
	}
	if jobs, _ := st.GardenDue(day(11, 12)); len(jobs) != 1 {
		t.Fatalf("not due the next day: %+v", jobs)
	}
	// Budget of the month spent: nothing is due.
	id, _ = st.StartGardenRun("person:3", o.ID, "github.com/alpha/two")
	_ = st.FinishGardenRun("person:3", id, "budget", 3, "")
	if _, err := st.db.Exec(`UPDATE garden_runs SET started_at=? WHERE id=?`, day(10, 11).Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := st.GardenDue(day(11, 12)); len(jobs) != 0 {
		t.Fatalf("due with the budget spent: %+v", jobs)
	}
	if spent := st.GardenSpent(o.ID, day(11, 12)); spent != 5.5 {
		t.Fatalf("spent = %v", spent)
	}
	if runs, _ := st.GardenRuns(o.ID, 10); len(runs) != 2 || runs[0].State != "budget" || runs[1].CostUSD != 2.5 {
		t.Fatalf("runs = %+v", runs)
	}
	// Dropping a repository from the list takes the role away there.
	g.Repos = []string{"github.com/alpha/one"}
	if err := st.SetGardenSettings("person:1", g); err != nil {
		t.Fatal(err)
	}
	if role := st.ProjectRole("github.com/alpha/two", "person:3"); role.Role == RoleMember {
		t.Fatalf("gardener kept the role in an unlisted repository: %+v", role)
	}
	if role := st.ProjectRole("github.com/alpha/one", "person:3"); role.Role != RoleMember {
		t.Fatalf("gardener lost the listed repository: %+v", role)
	}
	// Switched off: never due, and no role left; the guest seat goes too.
	g.Enabled = false
	_ = st.SetGardenSettings("person:1", g)
	if jobs, _ := st.GardenDue(day(20, 12)); len(jobs) != 0 {
		t.Fatalf("due while off: %+v", jobs)
	}
	if role := st.ProjectRole("github.com/alpha/one", "person:3"); role.Role == RoleMember {
		t.Fatalf("gardener kept a role while switched off: %+v", role)
	}
	if st.OrgRole(o.ID, "person:3") != "" {
		t.Fatalf("gardener kept its guest seat: %q", st.OrgRole(o.ID, "person:3"))
	}
}

func TestGardenSlotsByFrequency(t *testing.T) {
	sat := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) // Saturday
	if s := lastGardenSlot(GardenSettings{Frequency: GardenWeekdays, AtMinute: 185}, sat); s.Weekday() != time.Friday {
		t.Errorf("weekdays on a Saturday -> %v", s)
	}
	if s := lastGardenSlot(GardenSettings{Frequency: GardenWeekly, Weekday: 1, AtMinute: 185}, sat); s.Weekday() != time.Monday || s.Day() != 2 {
		t.Errorf("weekly Monday -> %v", s)
	}
	if s := lastGardenSlot(GardenSettings{Frequency: GardenDaily, AtMinute: 13 * 60}, sat); s.Day() != 6 {
		t.Errorf("daily 13:00 at noon -> %v", s)
	}
}

func TestGardenAutoLogIsAppendOnlyAndUndoneOnce(t *testing.T) {
	st, o := gardenFixture(t)
	id, err := st.RecordGardenAuto(GardenAuto{OrgID: o.ID, Rule: GardenRuleDescription, NoteID: 77, Project: "github.com/alpha/one", TargetKind: "ghost", TargetID: "cmd/x.go", After: "text", EnabledBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !st.GardenAutoNoted(77) {
		t.Fatal("approval not noted")
	}
	if _, err := st.RecordGardenAuto(GardenAuto{OrgID: o.ID, Rule: GardenRuleDescription, NoteID: 77, TargetKind: "ghost", TargetID: "x", EnabledBy: 1}); err == nil {
		t.Fatal("the same proposal was approved twice")
	}
	if err := st.MarkGardenAutoUndone(id, "person:1"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkGardenAutoUndone(id, "person:1"); !errors.Is(err, ErrGardenAutoGone) {
		t.Fatalf("undone twice: %v", err)
	}
	if _, err := st.db.Exec(`DELETE FROM garden_auto_log`); err == nil {
		t.Fatal("the log can be deleted")
	}
	log, _ := st.GardenAutoLog(o.ID, 10)
	if len(log) != 1 || log[0].UndoneBy != 1 {
		t.Fatalf("log = %+v", log)
	}
}

func TestNextGardenRun(t *testing.T) {
	g := GardenSettings{Enabled: true, Frequency: GardenDaily, AtMinute: 185}
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if n := NextGardenRun(g, at); n != time.Date(2026, 11, 8, 3, 5, 0, 0, time.UTC) {
		t.Errorf("daily next = %v", n)
	}
	g.Frequency = GardenWeekdays // Saturday noon -> Monday 03:05
	if n := NextGardenRun(g, at); n != time.Date(2026, 11, 9, 3, 5, 0, 0, time.UTC) {
		t.Errorf("weekdays next = %v", n)
	}
	early := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)
	if n := NextGardenRun(g, early); n != time.Date(2026, 11, 9, 3, 5, 0, 0, time.UTC) {
		t.Errorf("same day next = %v", n)
	}
	if !NextGardenRun(GardenSettings{}, at).IsZero() {
		t.Error("switched off has a next run")
	}
}

// Security review 3: the rule "missing proof, new proof" adds a synchronized
// commit to a met criterion whose commit proof vanished, and never changes a
// state; anything else is refused.
func TestVanishedProofGetsANewCommitAndNothingElse(t *testing.T) {
	st, _ := gardenFixture(t)
	const project = "github.com/alpha/one"
	d, err := st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "r", Scope: scope.Axes{Project: project}, Person: "robin"},
		Criteria: []string{"met with a vanished commit", "still open", "met with a standing commit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := d.Criteria
	for i, sha := range map[int]string{0: "deadbeef0001", 2: "cafe00000002"} {
		if err := st.SetCriterionState(c[i].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: sha + " (#1)", Person: "robin"}); err != nil {
			t.Fatal(err)
		}
	}
	// Synchronized commits of merged pull request 7: the new one and the
	// standing one; deadbeef is gone. Pull request 8 is still open, 9 was
	// squashed onto the base branch.
	pr := func(n int, state, mergeSHA string) {
		if _, err := st.db.Exec(`INSERT INTO git_pull_requests(project, number, state, head_ref, merge_sha, opened_at, updated_at) VALUES(?,?,?,?,?,?,?)`,
			project, n, state, "b", mergeSHA, now(), now()); err != nil {
			t.Fatal(err)
		}
	}
	pr(7, "merged", "")
	pr(8, "open", "")
	pr(9, "merged", "5a5a5a5a5a5a")
	for _, c := range []struct {
		pr  int
		sha string
	}{{7, "abc1234567890"}, {7, "cafe00000002"}, {8, "0be0be0be0be"}} {
		if _, err := st.db.Exec(`INSERT INTO git_pr_commits(project, pr, sha, recorded_at) VALUES(?,?,?,?)`, project, c.pr, c.sha, now()); err != nil {
			t.Fatal(err)
		}
	}
	// Review point 5: an open pull request's commit is no proof; a squash
	// merge on the base branch is.
	if commitKnownTx(st.db, project, "0be0be0") {
		t.Error("a commit of an open pull request counts")
	}
	if !commitKnownTx(st.db, project, "5a5a5a5") {
		t.Error("the squash commit of a merged pull request does not count")
	}
	id, err := st.ReplaceVanishedEvidence(c[0].ID, project, "abc1234 (#7)", "robin")
	if err != nil || id == 0 {
		t.Fatalf("vanished proof: %d %v", id, err)
	}
	for name, try := range map[string]func() error{
		"open criterion": func() error {
			_, err := st.ReplaceVanishedEvidence(c[1].ID, project, "abc1234 (#7)", "robin")
			return err
		},
		"standing proof": func() error {
			_, err := st.ReplaceVanishedEvidence(c[2].ID, project, "abc1234 (#7)", "robin")
			return err
		},
		"unknown commit": func() error {
			_, err := st.ReplaceVanishedEvidence(c[0].ID, project, "0000000000 (#9)", "robin")
			return err
		},
		"other project": func() error {
			_, err := st.ReplaceVanishedEvidence(c[0].ID, "github.com/alpha/two", "abc1234 (#7)", "robin")
			return err
		},
		"test as evidence": func() error {
			_, err := st.ReplaceVanishedEvidence(c[0].ID, project, "pkg/x_test.go TestX", "robin")
			return err
		},
	} {
		if err := try(); !errors.Is(err, ErrNotVanished) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var state string
	_ = st.db.QueryRow(`SELECT state FROM request_criteria WHERE id=?`, c[1].ID).Scan(&state)
	if state != "open" {
		t.Fatalf("the open criterion changed to %q", state)
	}
	if err := st.RemoveAutoEvidence(id, c[0].ID, "something else"); !errors.Is(err, ErrGardenAutoGone) {
		t.Fatalf("removed a changed proof: %v", err)
	}
	if err := st.RemoveAutoEvidence(id, c[0].ID, "abc1234 (#7)"); err != nil {
		t.Fatal(err)
	}
}
