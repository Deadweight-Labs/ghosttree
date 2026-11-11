package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const pullShaA = "aaaaaaa1111111111111111111111111111111aa"

// seedPulls legt drei PRs an (offen, gemergt, geschlossen) und, vor ihnen, zwei
// Wissenseinträge auf dem Branch des ersten, damit der Brief etwas trägt.
func (f *accessAPIFixture) seedPulls(t *testing.T) {
	t.Helper()
	anchor := &gitanchor.Anchor{Commit: pullShaA, Branch: "feat/x"}
	for _, k := range []struct{ title, confidence string }{{"auf dem PR", "trusted"}, {"noch nicht geprüft", "staged"}} {
		if _, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: k.title, Body: "text", Person: "robin", Confidence: k.confidence,
			Scope: scope.Axes{Project: accProject, Branch: "feat/x"}, GitAnchor: anchor}); err != nil {
			t.Fatal(err)
		}
	}
	f.seedPull(t) // PR 12, offen, feat/x → main, Installation 77
	for _, c := range []store.PullRequestChange{
		{Number: 12, Action: "edited", Title: "Der PR", Head: "feat/x", Base: "main", State: "open", Author: "mia-gh"},
		{Number: 13, Action: "closed", Title: "Gemergt", Head: "feat/y", Base: "main", State: "merged", Author: "Lena-GH"},
		{Number: 14, Action: "closed", Title: "Verworfen", Head: "feat/x", Base: "main", State: "closed", Author: "mia-gh"},
	} {
		c.InstallationID, c.Remote, c.HeadRemote = 77, accProject, accProject
		if _, err := f.st.RecordPullRequest(c); err != nil {
			t.Fatal(err)
		}
	}
}

func pullsURL(extra string) string {
	return "/api/pulls?project=" + url.QueryEscape(accProject) + extra
}

func TestPullListFiltersAndPages(t *testing.T) {
	f := accessAPI(t, true)
	f.seedPulls(t)
	list := func(extra string) client.PullListOut {
		t.Helper()
		return decode[client.PullListOut](t, f.expect(t, "gus", 200, "GET", pullsURL(extra), nil))
	}
	all := list("")
	if all.Total != 3 || len(all.Items) != 3 || all.Counts.Open != 1 || all.Counts.Merged != 1 || all.Counts.Closed != 1 {
		t.Fatalf("all: %+v", all)
	}
	for _, it := range all.Items {
		if !strings.HasSuffix(it.Link, fmt.Sprintf("/ui/dw/p/pulls/%d", it.Number)) {
			t.Errorf("link %q", it.Link)
		}
	}
	for state, want := range map[string]int64{"open": 12, "merged": 13, "closed": 14} {
		got := list("&state=" + state)
		if got.Total != 1 || len(got.Items) != 1 || got.Items[0].Number != want || got.Items[0].State != state {
			t.Errorf("state %s: %+v", state, got)
		}
		// Die Zahlen auf den Reitern hängen nicht am Zustandsfilter.
		if got.Counts.Open+got.Counts.Merged+got.Counts.Closed != 3 {
			t.Errorf("counts under state %s: %+v", state, got.Counts)
		}
	}
	if got := list("&state=all"); got.Total != 3 {
		t.Errorf("state all: %+v", got)
	}
	if got := list("&branch=feat/x"); got.Total != 2 || got.Counts.Merged != 0 {
		t.Errorf("branch: %+v", got)
	}
	// Der Autor gilt ohne Rücksicht auf die Schreibweise.
	if got := list("&author=lena-gh"); got.Total != 1 || got.Items[0].Number != 13 || got.Items[0].Author != "Lena-GH" {
		t.Errorf("author: %+v", got)
	}
	if got := list("&author=mia-gh&branch=feat/x&state=closed"); got.Total != 1 || got.Items[0].Number != 14 {
		t.Errorf("combined: %+v", got)
	}
	if got := list("&author=niemand"); got.Total != 0 || len(got.Items) != 0 {
		t.Errorf("nobody: %+v", got)
	}
	// Seiten.
	p1, p2 := list("&limit=2"), list("&limit=2&offset=2")
	if len(p1.Items) != 2 || len(p2.Items) != 1 || p1.Total != 3 || p2.Total != 3 || p1.Limit != 2 || p2.Offset != 2 {
		t.Errorf("pages: %+v %+v", p1, p2)
	}
	seen := map[int64]bool{}
	for _, it := range append(p1.Items, p2.Items...) {
		seen[it.Number] = true
	}
	if len(seen) != 3 {
		t.Errorf("pages overlap or miss: %v", seen)
	}
	if got := list("&limit=100000"); got.Limit != store.PullMaxLimit {
		t.Errorf("limit not capped: %d", got.Limit)
	}
	// Eingaben, die nichts bedeuten, sind 400, nicht "alles".
	f.expect(t, "gus", 400, "GET", pullsURL("&state=weird"), nil)
	f.expect(t, "gus", 400, "GET", "/api/pulls", nil)
}

func TestPullDetailCarriesPackageChecklistAndBrief(t *testing.T) {
	f := accessAPI(t, true)
	f.seedPulls(t)
	fid := decode[struct{ ID int64 }](t, f.expect(t, "mia", 201, "POST", "/api/features", map[string]any{"project": accProject, "name": "Pakete", "areas": []string{"internal/store/package.go"}})).ID
	// Der offene PR hat schon sein Paket (der Server legt es beim Öffnen an); die
	// Requests kommen von Hand hinein.
	pkg, err := f.st.PackageForPR(accProject, 12)
	if err != nil {
		t.Fatal(err)
	}
	name := "Paket"
	if _, err := f.st.UpdatePackage(pkg.ID, requestdomain.PackagePatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddPackageRequests(pkg.ID, []requestdomain.PackageMember{{RequestID: f.id["r-mia"], Role: requestdomain.RolePflicht}, {RequestID: f.id["r-robin"], Role: requestdomain.RoleOptional}}, "", "mia"); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "mia", 201, "POST", fmt.Sprintf("/api/requests/%d/criteria", f.id["r-mia"]), map[string]string{"description": "es geht"})
	f.expect(t, "mia", 200, "POST", "/api/features/pr-paths", map[string]any{"project": accProject, "pr": 12, "paths": []string{"internal/store/package.go"}})

	d := decode[client.PullDetailOut](t, f.expect(t, "gus", 200, "GET", "/api/pulls/12?project="+url.QueryEscape(accProject), nil))
	if d.Number != 12 || d.Title != "Der PR" || d.State != "open" || d.Head != "feat/x" || d.Base != "main" || d.Author != "mia-gh" {
		t.Fatalf("info: %+v", d.PullRequestInfo)
	}
	if d.Package == nil || d.Package.PR != 12 || len(d.Package.Members) != 2 {
		t.Fatalf("package: %+v", d.Package)
	}
	if d.Package.Features != nil {
		t.Errorf("the feature items must not be repeated in the package: %+v", d.Package.Features)
	}
	var withCriteria bool
	for _, m := range d.Package.Members {
		if len(m.Criteria) > 0 {
			withCriteria = true
		}
	}
	if !withCriteria {
		t.Errorf("the review view must carry the criteria: %+v", d.Package.Members)
	}
	ck := d.Checklist
	if !ck.HasPackage || ck.RequiredTotal != 1 || ck.RequiredOpen != 1 || ck.FeaturesOpen != 1 {
		t.Fatalf("checklist: %+v", ck)
	}
	if len(d.Features) != 1 || d.Features[0].Feature != fid || d.Features[0].State != requestdomain.FeatureCheckOpen {
		t.Fatalf("features: %+v", d.Features)
	}
	// Der Brief: der Gast sieht nur das Geprüfte, das Mitglied beides.
	if len(d.Brief.Knowledge) != 1 || d.Brief.Knowledge[0].Title != "auf dem PR" {
		t.Errorf("guest brief: %+v", d.Brief.Knowledge)
	}
	dm := decode[client.PullDetailOut](t, f.expect(t, "mia", 200, "GET", "/api/pulls/12?project="+url.QueryEscape(accProject), nil))
	if len(dm.Brief.Knowledge) != 2 {
		t.Errorf("member brief: %+v", dm.Brief.Knowledge)
	}
	// Link und Karte gehören zu genau diesem PR.
	if !strings.HasSuffix(d.Link, "/ui/dw/p/pulls/12") || d.Card.Kind != "Pull request" || d.Card.Ref != "#12" || d.Card.URL != d.Link {
		t.Errorf("link %q card %+v", d.Link, d.Card)
	}
	// Ein PR ohne Paket: leere Checkliste, kein Paket, die Features bleiben leer.
	nopkg := decode[client.PullDetailOut](t, f.expect(t, "gus", 200, "GET", "/api/pulls/13?project="+url.QueryEscape(accProject), nil))
	if nopkg.Package != nil || nopkg.Checklist.HasPackage || len(nopkg.Features) != 0 {
		t.Errorf("pr without a package: %+v", nopkg)
	}
	// Die Feature-Punkte stehen auch ohne Paket, sobald Pfade gemeldet sind.
	f.expect(t, "mia", 200, "POST", "/api/features/pr-paths", map[string]any{"project": accProject, "pr": 13, "paths": []string{"internal/store/package.go"}})
	nopkg = decode[client.PullDetailOut](t, f.expect(t, "gus", 200, "GET", "/api/pulls/13?project="+url.QueryEscape(accProject), nil))
	if nopkg.Package != nil || len(nopkg.Features) != 1 || nopkg.Checklist.FeaturesOpen != 1 {
		t.Errorf("pr without a package but with paths: %+v", nopkg)
	}
}

// Rechte: Fremder, ehemaliges Mitglied und Token ohne Konto sehen nichts, und
// "gibt es nicht" sieht von außen genauso aus wie "darfst du nicht".
func TestPullReadRightsAndOracle(t *testing.T) {
	f := accessAPI(t, true)
	f.seedPulls(t)
	detail := "/api/pulls/12?project=" + url.QueryEscape(accProject)
	f.expect(t, "robin", 200, "GET", detail, nil)
	f.expect(t, "gus", 200, "GET", detail, nil)
	f.expect(t, "gus", 200, "GET", pullsURL(""), nil)
	if code, _ := f.call(t, "mia", "GET", detail, nil); code != 200 {
		t.Fatalf("member: %d", code)
	}
	noToken := req(t, "GET", f.srv.URL+detail, "", nil)
	noToken.Body.Close()
	if noToken.StatusCode != 401 {
		t.Errorf("no token: %d", noToken.StatusCode)
	}
	// Fremder: dasselbe wie für ein Projekt, das es nicht gibt, und für einen PR, den es nicht gibt.
	unknownProject := "/api/pulls/12?project=" + url.QueryEscape("github.com/dw/none")
	codeA, bodyA := f.call(t, "nora", "GET", detail, nil)
	codeB, bodyB := f.call(t, "nora", "GET", unknownProject, nil)
	codeC, bodyC := f.call(t, "robin", "GET", "/api/pulls/999?project="+url.QueryEscape(accProject), nil)
	if codeA != 404 || codeA != codeB || bodyA != bodyB || codeC != 404 || bodyC != bodyA {
		t.Fatalf("oracle: outsider %d %s / unknown project %d %s / unknown PR %d %s", codeA, bodyA, codeB, bodyB, codeC, bodyC)
	}
	for _, path := range []string{pullsURL(""), "/api/pulls/12/commits?project=" + url.QueryEscape(accProject)} {
		a, ab := f.call(t, "nora", "GET", path, nil)
		u, ub := f.call(t, "nora", "GET", strings.Replace(path, url.QueryEscape(accProject), url.QueryEscape("github.com/dw/none"), 1), nil)
		if a != 404 || a != u || ab != ub {
			t.Errorf("%s: outsider %d %s, unknown %d %s", path, a, ab, u, ub)
		}
	}
	// Ehemaliges Mitglied.
	p, _ := f.st.ProjectByRemote(accProject)
	if err := f.st.RemoveProjectRole("person:1", accProject, "person:3", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RemoveOrgMember("person:1", p.OrgID, "person:3"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{detail, pullsURL(""), "/api/pulls/12/commits?project=" + url.QueryEscape(accProject)} {
		if code, out := f.call(t, "mia", "GET", path, nil); code != 404 {
			t.Errorf("former member %s: %d %s", path, code, out)
		}
	}
}

// Der Autor eines PRs wird nie von einem leeren Wert überschrieben.
func TestPullAuthorSurvivesEventsWithoutOne(t *testing.T) {
	f := accessAPI(t, true)
	f.seedPulls(t)
	if _, err := f.st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: accProject, HeadRemote: accProject, Number: 12,
		Action: "synchronize", Head: "feat/x", Base: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: accProject, HeadRemote: accProject, Number: 12,
		Action: "synchronize", Head: "feat/x", Base: "main", State: "open", Author: "evil\nlogin"}); err != nil {
		t.Fatal(err)
	}
	d := decode[client.PullDetailOut](t, f.expect(t, "gus", 200, "GET", "/api/pulls/12?project="+url.QueryEscape(accProject), nil))
	if d.Author != "mia-gh" {
		t.Fatalf("author: %q", d.Author)
	}
}
