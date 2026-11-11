package store

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Das Paket entsteht von selbst (REQ-601 AC-12).

func autoSession(t *testing.T, st *Store, external, project, branch string) int64 {
	t.Helper()
	id, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: external, AccountID: 1, Scope: scope.Axes{Project: project, Branch: branch}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func memberIDs(t *testing.T, st *Store, pkgID int64) map[int64]string {
	t.Helper()
	d, err := st.PackageByID(pkgID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, m := range d.Members {
		out[m.RequestID] = m.Role
	}
	return out
}

func TestAnOpenedPullRequestGetsItsPackageAndTakesTheRequestsOfStartWork(t *testing.T) {
	st := prFixture(t)
	mine := pkgRequestIn(t, st, prProject, "Die Arbeit")
	second := pkgRequestIn(t, st, prProject, "Danach")
	related := pkgRequestIn(t, st, prProject, "Nur gelesen")
	abandoned := pkgRequestIn(t, st, prProject, "Aufgegeben")
	elsewhere := pkgRequestIn(t, st, prProject, "Andere Session")
	foreign := pkgRequestIn(t, st, "github.com/deadweight-labs/other", "Anderes Projekt")

	onBranch := autoSession(t, st, "on-branch", prProject, "feat/auto")
	otherBranch := autoSession(t, st, "other-branch", prProject, "feat/else")
	if _, _, err := st.StartRequestWork(related, onBranch, "related", "robin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.StartRequestWork(elsewhere, otherBranch, "primary", "robin"); err != nil {
		t.Fatal(err)
	}
	// Ein Hauptauftrag nach dem anderen, wie bei einer Session über ein Backlog:
	// aufgegeben zählt nicht, abgeschlossen und laufend schon.
	finish := func(req int64, state string) {
		t.Helper()
		work, _, err := st.StartRequestWork(req, onBranch, "primary", "robin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.FinishRequestWork(work.ID, state, "fertig", "robin"); err != nil {
			t.Fatalf("finish: %v", err)
		}
	}
	finish(abandoned, "abandoned")
	finish(mine, "completed")
	if _, _, err := st.StartRequestWork(second, onBranch, "primary", "robin"); err != nil {
		t.Fatal(err)
	}
	// Ein Request des fremden Projekts kann nicht aus einer Session dieses Projekts
	// stammen; auch nicht, wenn jemand die Zeile sich anlegt.
	foreignSession := autoSession(t, st, "foreign", "github.com/deadweight-labs/other", "feat/auto")
	if _, _, err := st.StartRequestWork(foreign, foreignSession, "primary", "robin"); err != nil {
		t.Fatal(err)
	}

	res, err := st.RecordPullRequest(prEvent(21, "opened", "open", "feat/auto"))
	if err != nil || !res.Applied || !res.PackageCreated || res.PackageBound || res.PackageMembers != 2 {
		t.Fatalf("opened: %+v %v", res, err)
	}
	pkg, err := st.PackageForPR(prProject, 21)
	if err != nil {
		t.Fatalf("the package of the PR: %v", err)
	}
	if pkg.Branch != "feat/auto" || pkg.Name != "Titel" || pkg.CreatedBy != "github" || pkg.State != requestdomain.PackageOpen {
		t.Fatalf("package: %+v", pkg)
	}
	got := memberIDs(t, st, pkg.ID)
	if len(got) != 2 || got[mine] != "pflicht" || got[second] != "pflicht" {
		t.Fatalf("members: %v, want the two main requests of the session on the branch", got)
	}
	// Der Verlauf des Requests sagt, woher die Mitgliedschaft kommt.
	d, err := st.RequestByID(mine)
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, a := range d.Activity {
		if a.Kind == "package.added" && a.Person == "github" && strings.Contains(a.Data, "start_work") {
			said = true
		}
	}
	if !said {
		t.Fatalf("activity names no reason: %+v", d.Activity)
	}

	// Dasselbe Ereignis noch einmal (Wiederholung des Webhooks) legt nichts an.
	res, err = st.RecordPullRequest(prEvent(21, "synchronize", "open", "feat/auto"))
	if err != nil || res.PackageCreated || res.PackageBound {
		t.Fatalf("repeat: %+v %v", res, err)
	}
	if list, _ := st.packagesOfProject(prProject, ""); len(list) != 1 {
		t.Fatalf("one package, got %d", len(list))
	}
}

func TestTheTitleNamesTheRequestsOfThePackage(t *testing.T) {
	st := prFixture(t)
	named := pkgRequestIn(t, st, prProject, "Im Titel")
	dropped := pkgRequestIn(t, st, prProject, "Fallengelassen")
	if err := st.DropRequest(dropped, "nicht mehr nötig", "robin"); err != nil {
		t.Fatal(err)
	}
	foreign := pkgRequestIn(t, st, "github.com/deadweight-labs/other", "Im anderen Projekt")
	ev := prEvent(30, "opened", "open", "feat/title")
	ev.Title = "feat: etwas (REQ-" + itoa(named) + ", REQ-" + itoa(dropped) + ", REQ-" + itoa(foreign) + ", REQ-99999)"
	res, err := st.RecordPullRequest(ev)
	if err != nil || !res.PackageCreated || res.PackageMembers != 1 {
		t.Fatalf("opened: %+v %v", res, err)
	}
	pkg, _ := st.PackageForPR(prProject, 30)
	if got := memberIDs(t, st, pkg.ID); len(got) != 1 || got[named] != "pflicht" {
		t.Fatalf("members: %v, want only the open request of this project", got)
	}
}

// Wo nichts eindeutig ist, bleibt das Paket leer: ein Branchname, den schon ein
// anderer PR trug, gilt für die Sessions nicht.
func TestAReusedBranchNameLeavesThePackageEmpty(t *testing.T) {
	st := prFixture(t)
	req := pkgRequestIn(t, st, prProject, "Alte Arbeit")
	sess := autoSession(t, st, "old", prProject, "fix/typo")
	if _, _, err := st.StartRequestWork(req, sess, "primary", "robin"); err != nil {
		t.Fatal(err)
	}
	// Ein früherer PR mit demselben Branchnamen, längst geschlossen.
	if _, err := st.RecordPullRequest(prEvent(40, "closed", "closed", "fix/typo")); err != nil {
		t.Fatal(err)
	}
	res, err := st.RecordPullRequest(prEvent(41, "opened", "open", "fix/typo"))
	if err != nil || !res.PackageCreated || res.PackageMembers != 0 {
		t.Fatalf("opened: %+v %v", res, err)
	}
	pkg, err := st.PackageForPR(prProject, 41)
	if err != nil || len(pkg.Members) != 0 {
		t.Fatalf("the package must be empty: %+v %v", pkg, err)
	}
}

// Ein bestehendes Paket wird gebunden und nicht verdoppelt; ein PR, der nicht offen
// ankommt (zuerst als gemergt oder geschlossen gesehen), bekommt keines.
func TestNoSecondPackageAndNoneForPullRequestsThatAreNotOpen(t *testing.T) {
	st := prFixture(t)
	req := pkgRequestIn(t, st, prProject, "Vorhanden")
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/had", Members: []requestdomain.PackageMember{{RequestID: req}}}); err != nil {
		t.Fatal(err)
	}
	res, err := st.RecordPullRequest(prEvent(50, "opened", "open", "feat/had"))
	if err != nil || !res.PackageBound || res.PackageCreated {
		t.Fatalf("bound, not created: %+v %v", res, err)
	}
	for _, c := range []PullRequestChange{prEvent(51, "closed", "merged", "feat/old"), prEvent(52, "closed", "closed", "feat/gone")} {
		res, err := st.RecordPullRequest(c)
		if err != nil || res.PackageCreated {
			t.Fatalf("PR %d: %+v %v", c.Number, res, err)
		}
		if _, err := st.PackageForPR(prProject, c.Number); err == nil {
			t.Errorf("PR %d got a package although it was not open", c.Number)
		}
	}
	// Ein offener Branch mit einem Paket für einen anderen PR bekommt kein zweites.
	res, err = st.RecordPullRequest(prEvent(53, "opened", "open", "feat/had"))
	if err != nil || res.PackageCreated {
		t.Fatalf("second PR on a branch that has a package: %+v %v", res, err)
	}
	if list, _ := st.packagesOfProject(prProject, ""); len(list) != 1 {
		t.Fatalf("packages: %d, want 1", len(list))
	}
}
