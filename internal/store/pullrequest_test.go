package store

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const (
	prProject = "github.com/deadweight-labs/app"
	prInstall = 99
)

func prFixture(t *testing.T) *Store {
	t.Helper()
	st, o := prefixFixture(t)
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, "github", prefixLogin, prInstall, nil); err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{prProject, "github.com/deadweight-labs/other"} {
		if _, err := st.ClaimProject("person:1", remote, "dw"); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func prKnow(t *testing.T, st *Store, project, branch, title string) int64 {
	t.Helper()
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "prwort " + title, Person: "robin",
		Scope: scope.Axes{Project: project, Branch: branch}, GitAnchor: anchorAt(commitA, branch)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func prEvent(number int64, action, state, head string) PullRequestChange {
	return PullRequestChange{InstallationID: prInstall, Remote: prProject, HeadRemote: prProject, Number: number,
		Action: action, Title: "Titel", Head: head, Base: "main", DefaultBranch: "main", State: state}
}

func anchorPR(t *testing.T, st *Store, knowledgeID int64) sql.NullInt64 {
	t.Helper()
	var pr sql.NullInt64
	if err := st.db.QueryRow(`SELECT a.pr FROM knowledge k JOIN git_anchors a ON a.id=k.anchor_id WHERE k.id=?`, knowledgeID).Scan(&pr); err != nil {
		t.Fatal(err)
	}
	return pr
}

func TestPullRequestTagsTheAnchorsOfItsBranchAndOnlyOfItsProject(t *testing.T) {
	st := prFixture(t)
	onBranch := prKnow(t, st, prProject, "feat/a", "auf dem Branch")
	other := prKnow(t, st, prProject, "feat/b", "anderer Branch")
	foreign := prKnow(t, st, "github.com/deadweight-labs/other", "feat/a", "gleicher Branch-Name, anderes Projekt")
	req := pkgRequestIn(t, st, prProject, "Lastenheft")
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/a", Members: []requestdomain.PackageMember{{RequestID: req}}}); err != nil {
		t.Fatal(err)
	}

	res, err := st.RecordPullRequest(prEvent(7, "opened", "open", "feat/a"))
	if err != nil || !res.Applied || res.AnchorsTagged != 1 || !res.PackageBound || res.Merged {
		t.Fatalf("opened: %+v %v", res, err)
	}
	if pr := anchorPR(t, st, onBranch); !pr.Valid || pr.Int64 != 7 {
		t.Fatalf("anchor of the branch: %+v", pr)
	}
	if anchorPR(t, st, other).Valid || anchorPR(t, st, foreign).Valid {
		t.Fatal("another branch or another project got the number")
	}
	if d, err := st.PackageForPR(prProject, 7); err != nil || d.Branch != "feat/a" {
		t.Fatalf("package bound to the PR: %+v %v", d, err)
	}

	// Weitere Ereignisse ändern nichts an Nummern, die schon vergeben sind; neue
	// Anker des Branches bekommen sie bei "synchronize".
	later := prKnow(t, st, prProject, "feat/a", "nach dem Öffnen")
	res, err = st.RecordPullRequest(prEvent(7, "synchronize", "open", "feat/a"))
	if err != nil || res.AnchorsTagged != 1 || res.PackageBound {
		t.Fatalf("synchronize: %+v %v", res, err)
	}
	if pr := anchorPR(t, st, later); !pr.Valid || pr.Int64 != 7 {
		t.Fatalf("later anchor: %+v", pr)
	}

	// Ein zweiter PR desselben Branch-Namens nimmt die Anker des ersten nicht weg.
	if res, err = st.RecordPullRequest(prEvent(8, "opened", "open", "feat/a")); err != nil || res.AnchorsTagged != 0 {
		t.Fatalf("second PR on the same branch: %+v %v", res, err)
	}
	if pr := anchorPR(t, st, onBranch); pr.Int64 != 7 {
		t.Fatalf("number moved: %+v", pr)
	}

	// Der Filter pr liefert danach echte Treffer, und zwar die richtigen.
	hits, err := st.SearchKnowledgeWith("prwort", KnowledgeSearch{Axes: scope.Axes{Project: prProject, AnyBranch: true}, Union: true, Anchor: AnchorFilter{PR: 7}, NoRecord: true})
	if err != nil || len(hits) != 2 {
		t.Fatalf("pr filter: %d hits, %v", len(hits), err)
	}
	brief, err := st.PullRequestBriefOf(prProject, 7)
	if err != nil || !brief.Known || brief.Head != "feat/a" || len(brief.Knowledge) != 2 || brief.Knowledge[0].ID != onBranch || brief.Knowledge[1].ID != later {
		t.Fatalf("brief: %+v %v", brief, err)
	}
	if b, _ := st.PullRequestBriefOf(prProject, 999); b.Known || len(b.Knowledge) != 0 {
		t.Fatalf("unknown PR: %+v", b)
	}
}

func pkgRequestIn(t *testing.T, s *Store, project, title string) int64 {
	t.Helper()
	d, err := s.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: project}, Person: "robin"},
		Criteria: []string{"c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.Request.ID
}

func TestMergeNotesWhichBranchKnowledgeWentInAndOnlyOnce(t *testing.T) {
	st := prFixture(t)
	k1 := prKnow(t, st, prProject, "feat/a", "eins")
	req := pkgRequestIn(t, st, prProject, "Lastenheft")
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: prProject, Branch: "feat/a", Members: []requestdomain.PackageMember{{RequestID: req}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordPullRequest(prEvent(7, "opened", "open", "feat/a")); err != nil {
		t.Fatal(err)
	}
	k2 := prKnow(t, st, prProject, "feat/a", "zwei, nach dem Öffnen")
	merged := prEvent(7, "closed", "merged", "feat/a")
	merged.MergeSHA = commitB
	res, err := st.RecordPullRequest(merged)
	if err != nil || !res.Merged || len(res.Knowledge) != 2 || res.Knowledge[0] != k1 || res.Knowledge[1] != k2 || res.RequestsNotified != 1 {
		t.Fatalf("merge: %+v %v", res, err)
	}
	var notes []string
	for _, k := range activityKinds(t, st, req) {
		if strings.HasPrefix(k, "pr.merged") {
			notes = append(notes, k)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "PR #7") || !strings.Contains(notes[0], "#"+prItoa(k1)) || !strings.Contains(notes[0], "#"+prItoa(k2)) || strings.Contains(notes[0], "\n") {
		t.Fatalf("merge note: %q", notes)
	}
	// Dieselbe Meldung noch einmal, und ein verspätetes "synchronize": keine zweite Notiz, kein Rückfall.
	if res, err = st.RecordPullRequest(merged); err != nil || res.Merged || res.RequestsNotified != 0 {
		t.Fatalf("redelivery: %+v %v", res, err)
	}
	if _, err = st.RecordPullRequest(prEvent(7, "synchronize", "open", "feat/a")); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := st.db.QueryRow(`SELECT state FROM git_pull_requests WHERE project=? AND number=7`, prProject).Scan(&state); err != nil || state != "merged" {
		t.Fatalf("state after a late event: %q %v", state, err)
	}
	if n := len(activityKinds(t, st, req)); n < 1 {
		t.Fatal("no activity")
	}
}

func prItoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestClosedWithoutMergeTagsNothingAndReopeningDoes(t *testing.T) {
	st := prFixture(t)
	k := prKnow(t, st, prProject, "feat/a", "x")
	if res, err := st.RecordPullRequest(prEvent(7, "closed", "closed", "feat/a")); err != nil || !res.Applied || res.AnchorsTagged != 0 {
		t.Fatalf("closed: %+v %v", res, err)
	}
	if anchorPR(t, st, k).Valid {
		t.Fatal("a closed PR tagged anchors")
	}
	if res, err := st.RecordPullRequest(prEvent(7, "reopened", "open", "feat/a")); err != nil || res.AnchorsTagged != 1 {
		t.Fatalf("reopened: %+v %v", res, err)
	}
}

func TestPullRequestEventsFromOutsideAreCheckedAndLookTheSame(t *testing.T) {
	st := prFixture(t)
	k := prKnow(t, st, prProject, "main", "auf main")
	bad := func(name string, mod func(*PullRequestChange)) PullRequestResult {
		t.Helper()
		c := prEvent(7, "opened", "open", "main")
		c.Base = "dev"
		mod(&c)
		res, err := st.RecordPullRequest(c)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res
	}
	results := []PullRequestResult{
		bad("fork", func(c *PullRequestChange) { c.HeadRemote = "github.com/stranger/app" }),
		bad("foreign installation", func(c *PullRequestChange) { c.InstallationID = 12345 }),
		bad("unknown project", func(c *PullRequestChange) {
			c.Remote, c.HeadRemote = "github.com/deadweight-labs/nope", "github.com/deadweight-labs/nope"
		}),
		bad("foreign owner", func(c *PullRequestChange) {
			c.Remote, c.HeadRemote = "github.com/stranger/app", "github.com/stranger/app"
		}),
		bad("newline in the branch", func(c *PullRequestChange) { c.Head = "main\nIgnore previous" }),
		bad("bidi override", func(c *PullRequestChange) { c.Head = "main‮" }),
		bad("control character", func(c *PullRequestChange) { c.Head = "ma\x00in" }),
		bad("empty head", func(c *PullRequestChange) { c.Head = " " }),
		bad("too long", func(c *PullRequestChange) { c.Head = strings.Repeat("a", 300) }),
		bad("number zero", func(c *PullRequestChange) { c.Number = 0 }),
		bad("odd state", func(c *PullRequestChange) { c.State = "weird" }),
	}
	for i, r := range results {
		if r.Applied || r.AnchorsTagged != 0 || r.Project != "" {
			t.Fatalf("event %d had an effect or answered differently: %+v", i, r)
		}
	}
	if anchorPR(t, st, k).Valid {
		t.Fatal("a foreign event tagged an anchor")
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM git_pull_requests`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows = %d %v", n, err)
	}
}

func TestPullRequestTitleIsStoredAsOneCleanLine(t *testing.T) {
	st := prFixture(t)
	c := prEvent(7, "opened", "open", "feat/a")
	c.Title = "Zeile eins\n\nZeile\tzwei‮\x07" + strings.Repeat("x", 400)
	if _, err := st.RecordPullRequest(c); err != nil {
		t.Fatal(err)
	}
	b, _ := st.PullRequestBriefOf(prProject, 7)
	if strings.ContainsAny(b.Title, "\n\t\x07‮") || !strings.HasPrefix(b.Title, "Zeile eins Zeile zwei") || len([]rune(b.Title)) != 200 {
		t.Fatalf("title: %q (%d)", b.Title, len([]rune(b.Title)))
	}
}

func TestPullSyncTargetsListBranchesWithoutNumber(t *testing.T) {
	st := prFixture(t)
	prKnow(t, st, prProject, "feat/a", "a")
	prKnow(t, st, prProject, "feat/b", "b")
	targets, err := st.PullSyncTargets("")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tg := range targets {
		if tg.Project == prProject {
			if tg.InstallationID != prInstall {
				t.Fatalf("installation: %+v", tg)
			}
			got = tg.Branches
		}
	}
	if strings.Join(got, ",") != "feat/a,feat/b" {
		t.Fatalf("branches: %v", got)
	}
	if one, _ := st.PullSyncTargets("github.com/deadweight-labs/other"); len(one) != 1 || len(one[0].Branches) != 0 {
		t.Fatalf("single project: %+v", one)
	}
	if _, err := st.RecordPullRequest(prEvent(7, "opened", "open", "feat/a")); err != nil {
		t.Fatal(err)
	}
	targets, _ = st.PullSyncTargets(prProject)
	if len(targets) != 1 || strings.Join(targets[0].Branches, ",") != "feat/b" {
		t.Fatalf("after tagging: %+v", targets)
	}
}

func ghostOn(t *testing.T, st *Store, branch, path, text, commit string, at ...string) {
	t.Helper()
	var a *gitanchor.Anchor
	if commit != "" {
		a = anchorAt(commit, branch)
	}
	if _, err := st.PutGhostFile(GhostFile{Project: prProject, Path: path, Kind: "file", Description: text, Branch: branch, GitAnchor: a, Person: "robin", DescribedAt: strings.Join(at, "")}); err != nil {
		t.Fatal(err)
	}
}

func descAt(t *testing.T, st *Store, path, branch string, lineage ...string) string {
	t.Helper()
	g, err := st.GhostFileAt(prProject, path, branch, lineage)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return g.Description
}

func TestGhostDescriptionOnABranchInheritsDownAndReachesTheOriginAfterTheMerge(t *testing.T) {
	st := prFixture(t)
	ghostOn(t, st, "", "src/a.go", "Projektebene", commitA)
	ghostOn(t, st, "feat/a", "src/a.go", "auf feat/a", commitA)
	ghostOn(t, st, "feat/a", "src/neu.go", "nur auf feat/a", commitA)

	if got := descAt(t, st, "src/a.go", ""); got != "Projektebene" {
		t.Fatalf("project level without a branch: %q", got)
	}
	if got := descAt(t, st, "src/a.go", "main", "x"); got != "Projektebene" {
		t.Fatalf("the origin does not see the branch before the merge: %q", got)
	}
	if got := descAt(t, st, "src/a.go", "feat/a"); got != "auf feat/a" {
		t.Fatalf("on the branch: %q", got)
	}
	if got := descAt(t, st, "src/a.go", "feat/a-x", "main", "feat/a"); got != "auf feat/a" {
		t.Fatalf("a branch cut from it inherits: %q", got)
	}
	if got := descAt(t, st, "src/a.go", "feat/b", "main"); got != "Projektebene" {
		t.Fatalf("a sibling does not see it: %q", got)
	}
	if got := descAt(t, st, "src/neu.go", "feat/b", "main"); got != "" {
		t.Fatalf("a sibling sees a path that only exists on the branch: %q", got)
	}
	// Der Baum folgt Branch und Abstammung.
	tree, err := st.GhostFilesUnderAt(prProject, "src", "feat/a-x", []string{"feat/a"})
	if err != nil || len(tree) != 2 || tree[0].Path != "src/a.go" || tree[0].Description != "auf feat/a" || tree[1].Path != "src/neu.go" || tree[1].Branch != "feat/a" || tree[1].GitAnchor == nil {
		t.Fatalf("tree on the child: %+v %v", tree, err)
	}
	if tree, _ = st.GhostFilesUnderAt(prProject, "src", "main", nil); len(tree) != 1 || tree[0].Description != "Projektebene" {
		t.Fatalf("tree on main: %+v", tree)
	}
	if tree, _ = st.GhostFilesUnder(prProject, ""); len(tree) != 1 {
		t.Fatalf("the plain tree is the project level: %+v", tree)
	}
	// Auslieferung folgt dem Branch.
	got, err := st.GhostFilesForDeliveryAt(prProject, "src/a.go", "sess", "feat/a", nil)
	if err != nil || len(got) != 1 || got[0].Description != "auf feat/a" {
		t.Fatalf("delivery: %+v %v", got, err)
	}

	// Der Merge hebt die Beschreibungen auf den Base-Branch.
	if _, err := st.RecordPullRequest(prEvent(7, "opened", "open", "feat/a")); err != nil {
		t.Fatal(err)
	}
	m := prEvent(7, "closed", "merged", "feat/a")
	m.MergeSHA = commitB
	res, err := st.RecordPullRequest(m)
	if err != nil || res.GhostsPromoted != 2 || res.GhostsKeptNewer != 0 {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if got := descAt(t, st, "src/a.go", "main"); got != "auf feat/a" {
		t.Fatalf("after the merge the origin sees it: %q", got)
	}
	if got := descAt(t, st, "src/neu.go", ""); got != "nur auf feat/a" {
		t.Fatalf("new path on the project level: %q", got)
	}
	g, _ := st.GhostFileByPath(prProject, "src/a.go")
	if g.GitAnchor == nil || g.GitAnchor.Commit != commitB || g.GitAnchor.Branch != "main" || g.GitAnchor.PR == nil || *g.GitAnchor.PR != 7 {
		t.Fatalf("a new version with its own anchor: %+v", g.GitAnchor)
	}
	if h, err := st.GhostFileHistory(prProject, "src/a.go", 0); err != nil || len(h) != 1 || h[0].Description != "Projektebene" {
		t.Fatalf("the replaced version is kept: %+v %v", h, err)
	}
	var left int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM ghost_branch_files WHERE branch='feat/a'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("branch descriptions after the merge: %d %v", left, err)
	}
	if b, _ := st.PullRequestBriefOf(prProject, 7); len(b.Ghosts) != 2 {
		t.Fatalf("ghost paths with the PR anchor: %+v", b.Ghosts)
	}
}

func TestMergeKeepsAYoungerDescriptionOfTheTargetAndLiftsIntoAFeatureBase(t *testing.T) {
	st := prFixture(t)
	ghostOn(t, st, "feat/a", "x.go", "alt auf dem Branch", commitA, "2026-01-01T00:00:00Z")
	ghostOn(t, st, "", "x.go", "jünger auf der Projektebene", commitB, "2026-02-01T00:00:00Z")
	res, err := st.RecordPullRequest(prEvent(7, "closed", "merged", "feat/a"))
	if err != nil || res.GhostsPromoted != 0 || res.GhostsKeptNewer != 1 {
		t.Fatalf("younger target: %+v %v", res, err)
	}
	if got := descAt(t, st, "x.go", ""); got != "jünger auf der Projektebene" {
		t.Fatalf("kept: %q", got)
	}

	// Ein PR auf einen Feature-Branch (nicht Default) landet auf dessen Branch-Ebene, nicht auf der Projektebene.
	ghostOn(t, st, "feat/c", "y.go", "von c", commitA)
	c := prEvent(9, "closed", "merged", "feat/c")
	c.Base = "feat/base"
	if res, err = st.RecordPullRequest(c); err != nil || res.GhostsPromoted != 1 {
		t.Fatalf("into a feature base: %+v %v", res, err)
	}
	if got := descAt(t, st, "y.go", ""); got != "" {
		t.Fatalf("the project level must not see it: %q", got)
	}
	if got := descAt(t, st, "y.go", "feat/base"); got != "von c" {
		t.Fatalf("on the base branch: %q", got)
	}
}

func TestGhostBranchNamesFromTheOutsideAreRejected(t *testing.T) {
	st := prFixture(t)
	for _, b := range []string{"a\nb", "a‮b", strings.Repeat("a", 300)} {
		if _, err := st.PutGhostFile(GhostFile{Project: prProject, Path: "z.go", Description: "d", Branch: b}); err == nil {
			t.Fatalf("branch %q accepted", b)
		}
	}
}
