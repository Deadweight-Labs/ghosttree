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
	linkAuthor(t, st, "person:1", "robin-gh", 5001)
	ev := prEvent(30, "opened", "open", "feat/title")
	ev.Author = "robin-gh"
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

// linkAuthor verbindet ein Konto mit einem bewiesenen GitHub-Login (github_id > 0).
func linkAuthor(t *testing.T, st *Store, account, login string, githubID int64) {
	t.Helper()
	if err := st.SetAccountGitHub(account, login, githubID); err != nil {
		t.Fatal(err)
	}
}

// Ein REQ-n im Titel zieht nur dann einen Request ins Paket, wenn der Autor des PRs
// ein bewiesenes GitHub-Konto einer Person mit Schreibrecht am Projekt ist. Den
// Titel schreibt jeder, der das Repo auf GitHub sieht.
func TestTheTitleCountsOnlyForAnAuthorWhoMayWriteInTheProject(t *testing.T) {
	st := prFixture(t)
	target := pkgRequestIn(t, st, prProject, "Nur für Schreibende")
	linkAuthor(t, st, "person:1", "robin-gh", 5001)   // Org-Owner: Schreibrecht
	linkAuthor(t, st, "person:2", "anna-gh", 5002)    // Org-Mitglied ohne Rolle im Projekt
	linkAuthor(t, st, "person:4", "mallory-gh", 5004) // nicht in der Org
	if err := st.SetAccountGitHub("person:3", "ben-gh", 0); err != nil {
		t.Fatal(err) // Login eingetragen, nicht bewiesen
	}
	cases := []struct {
		number  int64
		author  string
		members int
	}{
		{31, "robin-gh", 1},
		{32, "stranger", 0},   // kein Konto in ghosttree
		{33, "anna-gh", 0},    // Mitglied der Org, aber ohne Rolle im Projekt
		{34, "mallory-gh", 0}, // Konto ohne Zugang zum Projekt
		{35, "ben-gh", 0},     // Login nur eingetragen
		{36, "", 0},           // Autor unbekannt
	}
	for _, c := range cases {
		ev := prEvent(c.number, "opened", "open", "feat/author-"+itoa(c.number))
		ev.Author = c.author
		ev.Title = "feat: etwas (REQ-" + itoa(target) + ")"
		res, err := st.RecordPullRequest(ev)
		if err != nil || !res.PackageCreated || res.PackageMembers != c.members {
			t.Errorf("author %q: %+v %v, want %d members", c.author, res, err, c.members)
		}
	}
	// Mit Schreibrecht im Projekt (member) zählt der Titel; ein Gast zählt nicht.
	if err := st.SetProjectRole("person:1", prProject, "person:2", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	ev := prEvent(37, "opened", "open", "feat/author-37")
	ev.Author, ev.Title = "anna-gh", "feat: etwas (REQ-"+itoa(target)+")"
	if res, err := st.RecordPullRequest(ev); err != nil || res.PackageMembers != 1 {
		t.Errorf("member: %+v %v", res, err)
	}
	if err := st.SetProjectRole("person:1", prProject, "person:2", RoleGuest, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	ev = prEvent(38, "opened", "open", "feat/author-38")
	ev.Author, ev.Title = "anna-gh", "feat: etwas (REQ-"+itoa(target)+")"
	if res, err := st.RecordPullRequest(ev); err != nil || res.PackageMembers != 0 {
		t.Errorf("guest: %+v %v", res, err)
	}
}

// Auch ohne Schreibrecht des Autors gilt der Weg über start_work: die Session hat
// den Request gewählt, nicht der Titel.
func TestAForeignTitleStillLeavesTheStartWorkRoute(t *testing.T) {
	st := prFixture(t)
	mine := pkgRequestIn(t, st, prProject, "Die Arbeit")
	other := pkgRequestIn(t, st, prProject, "Fremder Verweis")
	sess := autoSession(t, st, "sess-a", prProject, "feat/start")
	if _, _, err := st.StartRequestWork(mine, sess, "primary", "robin"); err != nil {
		t.Fatal(err)
	}
	ev := prEvent(60, "opened", "open", "feat/start")
	ev.Author, ev.Title = "stranger", "feat: x (REQ-"+itoa(other)+")"
	res, err := st.RecordPullRequest(ev)
	if err != nil || res.PackageMembers != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	pkg, _ := st.PackageForPR(prProject, 60)
	if got := memberIDs(t, st, pkg.ID); len(got) != 1 || got[mine] == "" {
		t.Fatalf("members: %v, want only the request of start_work", got)
	}
}

// Merge oder Schließen schließt das Paket des PRs; der nächste PR auf demselben
// Branch bekommt ein eigenes, und package create verweist nicht auf das alte.
func TestAMergedPullRequestClosesItsPackageAndTheNextOneGetsItsOwn(t *testing.T) {
	st := prFixture(t)
	req := pkgRequestIn(t, st, prProject, "Erste Runde")
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/again", Members: []requestdomain.PackageMember{{RequestID: req}}}); err != nil {
		t.Fatal(err)
	}
	if res, err := st.RecordPullRequest(prEvent(70, "opened", "open", "feat/again")); err != nil || !res.PackageBound {
		t.Fatalf("bound: %+v %v", res, err)
	}
	first, _ := st.PackageForPR(prProject, 70)
	if _, err := st.RecordPullRequest(prEvent(70, "closed", "merged", "feat/again")); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.PackageByID(first.ID); d.State != requestdomain.PackageClosed {
		t.Fatalf("after the merge the package is %q, want closed", d.State)
	}
	// Das Lastenheft eines geschlossenen Pakets gibt es nicht mehr.
	if _, ok, _ := st.PullLastenheft(prProject, 70); ok {
		t.Error("a merged pull request keeps no open requirements list")
	}
	res, err := st.RecordPullRequest(prEvent(71, "opened", "open", "feat/again"))
	if err != nil || !res.PackageCreated || res.PackageBound {
		t.Fatalf("the next PR on the same branch: %+v %v", res, err)
	}
	second, err := st.PackageForPR(prProject, 71)
	if err != nil || second.ID == first.ID || second.State != requestdomain.PackageOpen {
		t.Fatalf("second package: %+v %v", second, err)
	}

	// Geschlossen ohne Merge: ebenso; wieder geöffnet, öffnet sich das Paket mit.
	if _, err := st.RecordPullRequest(prEvent(71, "closed", "closed", "feat/again")); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.PackageByID(second.ID); d.State != requestdomain.PackageClosed {
		t.Fatalf("after closing: %q", d.State)
	}
	if _, err := st.RecordPullRequest(prEvent(71, "reopened", "open", "feat/again")); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.PackageByID(second.ID); d.State != requestdomain.PackageOpen {
		t.Fatalf("after reopening: %q", d.State)
	}
}

// Ein offenes Paket, dessen PR schon gemergt ist (das Ereignis kam vor diesem
// Stand an), blockiert den Branchnamen nicht: der neue PR und package create
// schließen es und legen ein neues an.
func TestAnOpenPackageOfAFinishedPullRequestDoesNotBlockTheBranch(t *testing.T) {
	st := prFixture(t)
	old, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/stale", PR: 80})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO git_pull_requests(project,number,title,state,head_ref,base_ref,default_branch,merge_sha,opened_at,updated_at,merged_at,author)
		VALUES(?,80,'t','merged','feat/stale','main','main','','x','x','x','')`, prProject); err != nil {
		t.Fatal(err)
	}
	res, err := st.RecordPullRequest(prEvent(81, "opened", "open", "feat/stale"))
	if err != nil || !res.PackageCreated {
		t.Fatalf("new PR: %+v %v", res, err)
	}
	if d, _ := st.PackageByID(old.ID); d.State != requestdomain.PackageClosed {
		t.Fatalf("the old package is %q, want closed", d.State)
	}

	// package create, ohne PR, auf einem Branch, dessen offenes Paket zu einem
	// gemergten PR gehört.
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/stale2", PR: 90}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO git_pull_requests(project,number,title,state,head_ref,base_ref,default_branch,merge_sha,opened_at,updated_at,merged_at,author)
		VALUES(?,90,'t','merged','feat/stale2','main','main','','x','x','x','')`, prProject); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/stale2"}); err != nil {
		t.Fatalf("package create must not point to the finished package: %v", err)
	}
}

// Ein create auf dem Branch eines offenen PRs sagt, dass das Paket von selbst entstand
// und dass man mit add ergänzt; bei einem Paket ohne PR bleibt der allgemeine Hinweis.
func TestPackageExistsHintNamesTheAutomaticPackageOnlyForAnOpenPullRequest(t *testing.T) {
	st := prFixture(t)
	if _, err := st.RecordPullRequest(prEvent(100, "opened", "open", "feat/hint")); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/hint"})
	rule, ok := err.(*requestdomain.RuleError)
	if !ok || rule.ErrorCode != "package_exists" || !strings.Contains(rule.Resolution, "automatically") || !strings.Contains(rule.Resolution, "package action=add") {
		t.Fatalf("own open PR: %#v", err)
	}
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/manual"}); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/manual"})
	rule, ok = err.(*requestdomain.RuleError)
	if !ok || rule.ErrorCode != "package_exists" || strings.Contains(rule.Resolution, "automatically") {
		t.Fatalf("a package without a pull request: %#v", err)
	}
}
