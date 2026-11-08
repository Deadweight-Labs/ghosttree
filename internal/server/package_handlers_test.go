package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func pkgJSON(t *testing.T, body string) requestdomain.PackageDetail {
	t.Helper()
	return decode[requestdomain.PackageDetail](t, body)
}

func memberRoles(d requestdomain.PackageDetail) map[int64]string {
	out := map[int64]string{}
	for _, m := range d.Members {
		out[m.RequestID] = m.Role
	}
	return out
}

// Der volle Weg über die API: anlegen, erweitern, nach Branch (mit Erbe) und PR
// abfragen, mit Grund entfernen.
func TestPackageAPIBindsBranchAndPullRequest(t *testing.T) {
	f := accessAPI(t, true)
	mia, robin := f.id["r-mia"], f.id["r-robin"]

	created := pkgJSON(t, f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{
		"project": accProject, "branch": "feat/x", "name": "Das Vorhaben",
		"requests": []map[string]any{{"request_id": mia}, {"request_id": robin, "role": "optional"}},
	}))
	if created.State != "open" || created.Name != "Das Vorhaben" || memberRoles(created)[mia] != "pflicht" || memberRoles(created)[robin] != "optional" {
		t.Fatalf("created = %+v", created)
	}
	f.expect(t, "mia", 409, "POST", "/api/packages", map[string]any{"project": accProject, "branch": "feat/x"})
	f.expect(t, "mia", 404, "POST", "/api/packages", map[string]any{"project": accProject, "branch": "feat/y",
		"requests": []map[string]any{{"request_id": f.id["r-other"]}}})
	f.expect(t, "mia", 400, "POST", "/api/packages", map[string]any{"project": accProject, "branch": "feat/y",
		"requests": []map[string]any{{"request_id": mia, "role": "wichtig"}}})

	q := func(extra string) string { return "/api/packages?project=" + accProject + extra }
	// Der abgezweigte Branch erbt; die Kette ist wiederholt statt verbunden übergeben.
	child := pkgJSON(t, f.expect(t, "mia", 200, "GET", q("&branch=feat/child&lineage=main&lineage=feat/x"), nil))
	if child.ID != created.ID || child.InheritedFrom != "feat/x" || len(child.Members) != 2 {
		t.Fatalf("child = %+v", child)
	}
	own := pkgJSON(t, f.expect(t, "mia", 200, "GET", q("&branch=feat/x"), nil))
	if own.InheritedFrom != "" || own.Members[0].Title == "" {
		t.Fatalf("own = %+v", own)
	}
	f.expect(t, "mia", 404, "GET", q("&branch=feat/unrelated&lineage=main"), nil)
	f.expect(t, "mia", 400, "GET", q(""), nil)
	f.expect(t, "mia", 400, "GET", q("&pr=abc"), nil)

	// PR binden und darüber finden.
	f.expect(t, "mia", 200, "PATCH", idPath("/api/packages/%d", created.ID), map[string]any{"pr": 42})
	byPR := pkgJSON(t, f.expect(t, "mia", 200, "GET", q("&pr=42"), nil))
	if byPR.ID != created.ID || byPR.PR != 42 {
		t.Fatalf("by pr = %+v", byPR)
	}
	f.expect(t, "mia", 404, "GET", q("&pr=43"), nil)

	// Entfernen braucht einen Grund, und der steht im Log des Requests.
	removePath := idPath("/api/packages/%d/requests/remove", created.ID)
	f.expect(t, "mia", 400, "POST", removePath, map[string]any{"request_id": robin})
	f.expect(t, "mia", 200, "POST", removePath, map[string]any{"request_id": robin, "reason": "anderer Branch"})
	detail := decode[requestdomain.Detail](t, f.expect(t, "mia", 200, "GET", idPath("/api/requests/%d", robin), nil))
	logged := false
	for _, a := range detail.Activity {
		logged = logged || (a.Kind == "package.removed" && strings.Contains(a.Data, "anderer Branch"))
	}
	if !logged {
		t.Fatalf("removal not in the activity list: %+v", detail.Activity)
	}
	back := pkgJSON(t, f.expect(t, "mia", 200, "POST", idPath("/api/packages/%d/requests", created.ID),
		map[string]any{"requests": []map[string]any{{"request_id": robin}}, "reason": "doch dabei"}))
	if len(back.Members) != 2 {
		t.Fatalf("after add = %+v", back)
	}
	f.expect(t, "mia", 404, "GET", "/api/packages/9999", nil)
}

// Wer darf was: Gäste lesen, schreiben nicht; wer das Projekt nicht sieht,
// bekommt für ein Paket dieselbe Antwort wie für eines, das es nicht gibt.
func TestPackageAPIFollowsTheRequestRoles(t *testing.T) {
	f := accessAPI(t, true)
	pkg := pkgJSON(t, f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{
		"project": accProject, "branch": "feat/r", "requests": []map[string]any{{"request_id": f.id["r-mia"]}}}))
	one, review := idPath("/api/packages/%d", pkg.ID), idPath("/api/packages/%d/review", pkg.ID)
	for _, who := range []string{"robin", "lena", "mia", "rex", "gus"} {
		f.expect(t, who, 200, "GET", one, nil)
		f.expect(t, who, 200, "GET", review, nil)
		f.expect(t, who, 200, "GET", "/api/packages?project="+accProject+"&branch=feat/r", nil)
	}
	f.expect(t, "nora", 404, "GET", one, nil)
	f.expect(t, "nora", 404, "GET", review, nil)
	f.expect(t, "nora", 404, "GET", idPath("/api/packages/%d", pkg.ID+100), nil)

	f.expect(t, "gus", 403, "PATCH", one, map[string]any{"name": "x"})
	f.expect(t, "gus", 403, "POST", one+"/requests", map[string]any{"requests": []map[string]any{{"request_id": f.id["r-robin"]}}})
	f.expect(t, "gus", 403, "POST", one+"/requests/remove", map[string]any{"request_id": f.id["r-mia"], "reason": "x"})
	f.expect(t, "gus", 403, "POST", "/api/packages", map[string]any{"project": accProject, "branch": "feat/g"})
	f.expect(t, "gus", 403, "POST", "/api/packages/import-branch-relations", map[string]any{"project": accProject})
	f.expect(t, "nora", 404, "PATCH", one, map[string]any{"name": "x"})
	f.expect(t, "nora", 404, "POST", one+"/requests", map[string]any{"requests": []map[string]any{{"request_id": f.id["r-robin"]}}})
	f.expect(t, "rex", 200, "PATCH", one, map[string]any{"name": "von rex"})
}

func TestReopenCriterionAPI(t *testing.T) {
	f := accessAPI(t, true)
	rid := f.id["r-mia"]
	c, err := f.st.AddCriterion(rid, "Es geht", "mia")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetCriterionState(c.ID, "met", requestdomain.Evidence{Kind: "test", Ref: "TestX", Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	path := idPath("/api/criteria/%d/reopen", c.ID)
	f.expect(t, "gus", 403, "POST", path, map[string]any{"reason": "x"})
	f.expect(t, "nora", 404, "POST", path, map[string]any{"reason": "x"})
	f.expect(t, "mia", 400, "POST", path, map[string]any{})
	f.expect(t, "mia", 200, "POST", path, map[string]any{"reason": "Test prüft nichts"})
	f.expect(t, "mia", 400, "POST", path, map[string]any{"reason": "nochmal"}) // schon offen
	detail := decode[requestdomain.Detail](t, f.expect(t, "mia", 200, "GET", idPath("/api/requests/%d", rid), nil))
	if detail.Criteria[0].State != "open" {
		t.Fatalf("criterion = %+v", detail.Criteria[0])
	}
}

func TestImportBranchRelationsAPI(t *testing.T) {
	f := accessAPI(t, true)
	rid := f.id["r-mia"]
	ref := "branch:" + accProject + "@feat/git-native"
	f.expect(t, "mia", 201, "POST", idPath("/api/requests/%d/relations", rid), map[string]any{"kind": "external", "external_ref": ref})
	f.expect(t, "gus", 403, "POST", "/api/packages/import-branch-relations", map[string]any{"project": accProject, "dry_run": true})
	f.expect(t, "mia", 400, "POST", "/api/packages/import-branch-relations", map[string]any{})
	dry := decode[requestdomain.ImportReport](t, f.expect(t, "mia", 200, "POST", "/api/packages/import-branch-relations", map[string]any{"project": accProject, "dry_run": true}))
	if !dry.DryRun || dry.Added != 1 {
		t.Fatalf("dry = %+v", dry)
	}
	f.expect(t, "mia", 404, "GET", "/api/packages?project="+accProject+"&branch=feat/git-native", nil)
	done := decode[requestdomain.ImportReport](t, f.expect(t, "mia", 200, "POST", "/api/packages/import-branch-relations", map[string]any{"project": accProject}))
	if done.DryRun || done.Added != 1 || len(done.PackagesCreated) != 1 {
		t.Fatalf("done = %+v", done)
	}
	got := pkgJSON(t, f.expect(t, "mia", 200, "GET", "/api/packages?project="+accProject+"&branch=feat/git-native", nil))
	if memberRoles(got)[rid] != "pflicht" {
		t.Fatalf("package = %+v", got)
	}
	// Die Relation bleibt stehen, markiert; ein zweiter Lauf findet nichts mehr.
	detail := decode[requestdomain.Detail](t, f.expect(t, "mia", 200, "GET", idPath("/api/requests/%d", rid), nil))
	if len(detail.Relations) != 1 || detail.Relations[0].PackageID != got.ID {
		t.Fatalf("relations = %+v, want the old one kept and marked", detail.Relations)
	}
	again := decode[requestdomain.ImportReport](t, f.expect(t, "mia", 200, "POST", "/api/packages/import-branch-relations", map[string]any{"project": accProject}))
	if len(again.Items) != 0 {
		t.Fatalf("second run = %+v", again)
	}
}

// heldRequest ist ein noch nicht übernommener, destillierter Request aus der
// privaten Session von rex: mia darf ihn weder lesen noch bearbeiten.
func heldRequest(t *testing.T, f *accessAPIFixture) int64 {
	t.Helper()
	d, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: "privater Wunsch von rex", Origin: "distilled",
		SessionRef: fmt.Sprintf("session:%d", f.id["s-rex"]), Scope: scope.Axes{Project: accProject}}})
	if err != nil {
		t.Fatal(err)
	}
	return d.Request.ID
}

// Das Paket-Recht sagt nichts darüber, welche Requests der Aufrufer anfassen
// darf. Ein Request, den er nicht sehen darf, lässt sich weder hineinlegen noch
// herausnehmen, und die Antwort ist dieselbe wie für eine unbekannte Nummer:
// auch der Request eines fremden Projekts verrät sich nicht.
func TestPackageRefusesRequestsTheCallerMayNotTouch(t *testing.T) {
	f := accessAPI(t, true)
	held := heldRequest(t, f)
	pkg := pkgJSON(t, f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{
		"project": accProject, "branch": "feat/idor", "requests": []map[string]any{{"request_id": f.id["r-mia"]}}}))
	add := idPath("/api/packages/%d/requests", pkg.ID)
	remove := idPath("/api/packages/%d/requests/remove", pkg.ID)

	for name, probe := range map[string]func(id int64) (int, string){
		"add": func(id int64) (int, string) {
			return f.call(t, "mia", "POST", add, map[string]any{"requests": []map[string]any{{"request_id": id}}})
		},
		"remove": func(id int64) (int, string) {
			return f.call(t, "mia", "POST", remove, map[string]any{"request_id": id, "reason": "weg"})
		},
		"create": func(id int64) (int, string) {
			return f.call(t, "mia", "POST", "/api/packages", map[string]any{"project": accProject, "branch": fmt.Sprintf("feat/idor-%d", id),
				"requests": []map[string]any{{"request_id": id}}})
		},
	} {
		unknownCode, unknown := probe(99999)
		if unknownCode != 404 {
			t.Errorf("%s: unknown id answers %d %s", name, unknownCode, unknown)
		}
		for what, id := range map[string]int64{"held request": held, "request of another project": f.id["r-other"]} {
			if code, got := probe(id); code != unknownCode || got != unknown {
				t.Errorf("%s with a %s answers %d %q, want what an unknown id gets: %d %q", name, what, code, got, unknownCode, unknown)
			}
		}
	}
	// Nichts davon hat das Paket verändert.
	got := pkgJSON(t, f.expect(t, "mia", 200, "GET", idPath("/api/packages/%d", pkg.ID), nil))
	if len(got.Members) != 1 {
		t.Fatalf("members = %+v", got.Members)
	}

	// Ein Mitglied, das der Aufrufer nicht sehen darf (hineingelegt von jemandem
	// mit Recht), lässt sich auch nicht herausnehmen und fehlt in der Ansicht.
	if _, err := f.st.AddPackageRequests(pkg.ID, []requestdomain.PackageMember{{RequestID: held}}, "", "rex"); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.call(t, "mia", "POST", remove, map[string]any{"request_id": held, "reason": "weg"}); code != 404 {
		t.Errorf("removing a hidden member answers %d, want 404", code)
	}
	got = pkgJSON(t, f.expect(t, "mia", 200, "GET", idPath("/api/packages/%d?full=1", pkg.ID), nil))
	if _, in := memberRoles(got)[held]; in || len(got.Members) != 1 || strings.Contains(fmt.Sprint(got), "privater Wunsch") {
		t.Errorf("the view shows a request the caller may not read: %+v", got.Members)
	}
	if stored, _ := f.st.PackageByID(pkg.ID); len(stored.Members) != 2 {
		t.Errorf("the hidden member was removed: %+v", stored.Members)
	}
}

// Die Übernahme fasst nur Requests an, die der Aufrufer lesen und bearbeiten
// darf; die anderen bleiben unberührt und stehen nicht im Bericht.
func TestImportBranchRelationsSkipsWhatTheCallerMayNotEdit(t *testing.T) {
	f := accessAPI(t, true)
	held := heldRequest(t, f)
	ref := "branch:" + accProject + "@feat/mixed"
	for _, id := range []int64{f.id["r-mia"], held} {
		if _, err := f.st.AddRequestRelation(id, requestdomain.Relation{Kind: "external", ExternalRef: ref}, "robin"); err != nil {
			t.Fatal(err)
		}
	}
	report := decode[requestdomain.ImportReport](t, f.expect(t, "mia", 200, "POST", "/api/packages/import-branch-relations", map[string]any{"project": accProject}))
	if len(report.Items) != 1 || report.Items[0].RequestID != f.id["r-mia"] || strings.Contains(fmt.Sprint(report), fmt.Sprint(held)) {
		t.Fatalf("report = %+v", report)
	}
	pkg := pkgJSON(t, f.expect(t, "mia", 200, "GET", "/api/packages?project="+accProject+"&branch=feat/mixed", nil))
	if len(pkg.Members) != 1 {
		t.Fatalf("package = %+v", pkg)
	}
	if d, _ := f.st.RequestByID(held); len(d.Relations) != 1 || d.Relations[0].PackageID != 0 {
		t.Fatalf("a request the caller may not edit was touched: %+v", d.Relations)
	}
}

// Das Briefing: eine Session auf dem Branch bekommt das Paket, eine auf einem
// abgezweigten Branch ebenso (geerbt), eine auf einem fremden keines. Offene
// Human-Tasks stehen dabei.
func TestBootstrapBriefsThePackageOfTheBranch(t *testing.T) {
	f := accessAPI(t, true)
	mia := f.id["r-mia"]
	if _, err := f.st.AddCriterion(mia, "erstes", "mia"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddCriterion(mia, "zweites", "mia"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddCriterion(f.id["r-robin"], "optional offen", "robin"); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{
		"project": accProject, "branch": "feat/b", "name": "Briefing-Paket",
		"requests": []map[string]any{{"request_id": mia}, {"request_id": f.id["r-robin"], "role": "optional"}}})
	f.newTask(t, "mia", map[string]any{"kind": "input", "title": "Entscheidung treffen", "request_id": mia})

	boot := func(who, query string) string {
		return f.expect(t, who, 200, "GET", "/api/context/bootstrap?project="+accProject+query, nil)
	}
	own := boot("mia", "&branch=feat/b")
	for _, want := range []string{"## Work package (ghosttree)", `PKG-1: Briefing-Paket, on branch feat/b`, "REQ-" + fmt.Sprint(mia) + " mia request: 2 open, 0 met", "(optional)", "Waiting on a human", "Entscheidung treffen"} {
		if !strings.Contains(own, want) {
			t.Errorf("own branch briefing misses %q:\n%s", want, own)
		}
	}
	inherited := boot("mia", "&branch=feat/b-child&"+url.Values{"lineage": {"main", "feat/b"}}.Encode())
	if !strings.Contains(inherited, "(inherited from feat/b)") || !strings.Contains(inherited, "Briefing-Paket") {
		t.Errorf("a branch cut from feat/b is not briefed with its package:\n%s", inherited)
	}
	if other := boot("mia", "&branch=feat/other&lineage=main"); strings.Contains(other, "Work package") {
		t.Errorf("an unrelated branch got a package:\n%s", other)
	}
	if none := boot("mia", ""); strings.Contains(none, "Work package") {
		t.Errorf("no branch, still a package:\n%s", none)
	}
	// Wer die Requests des Projekts nicht lesen darf, bekommt auch das Paket nicht.
	if got := boot("nora", "&branch=feat/b"); strings.Contains(got, "Briefing-Paket") {
		t.Errorf("a stranger is briefed with the package:\n%s", got)
	}
	// Ein geschlossenes Paket erscheint nicht mehr.
	f.expect(t, "mia", 200, "PATCH", "/api/packages/1", map[string]any{"state": "closed"})
	if closed := boot("mia", "&branch=feat/b"); strings.Contains(closed, "Work package") {
		t.Errorf("a closed package is still briefed:\n%s", closed)
	}
}

// Das Budget gilt für den ganzen Abschnitt: was nicht mehr passt, steht als
// Zahl da, nicht als Abschneiden mitten im Satz.
func TestPackageBriefingKeepsItsBudgetAndSaysWhatItLeftOut(t *testing.T) {
	d := requestdomain.PackageDetail{Package: requestdomain.Package{ID: 3, Name: "Groß", Branch: "feat/big"}}
	for i := 1; i <= 80; i++ {
		d.Members = append(d.Members, requestdomain.PackageMemberView{
			PackageMember: requestdomain.PackageMember{RequestID: int64(i), Role: requestdomain.RolePflicht},
			Title:         strings.Repeat("ein langer Titel ", 12), State: "open", Open: 2, Met: 1})
	}
	d.Members = append(d.Members, requestdomain.PackageMemberView{
		PackageMember: requestdomain.PackageMember{RequestID: 500, Role: requestdomain.RolePflicht},
		Title:         "fertig", State: "done", Met: 3})
	out := renderPackageBriefing(d, 1200)
	if len(out) > 1200+300 { // das Budget plus die festen Schlusszeilen
		t.Errorf("briefing is %d bytes for a budget of 1200", len(out))
	}
	if !strings.Contains(out, "81 requests, 160 open criteria, 1 with nothing open") || !strings.Contains(out, "more (package_get)") {
		t.Errorf("counts or the cut note are missing:\n%s", out)
	}
	if strings.Contains(out, "REQ-500") {
		t.Errorf("a finished request is listed:\n%s", out)
	}
}
