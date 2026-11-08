package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func featPath(format string, id int64) string { return idPath(format, id) }

// Der ganze Weg eines Features über die API: anlegen, fortschreiben mit Git-Anker,
// Verweise, die Idee betrifft das Feature, die Checkliste eines PRs, Kontext,
// Suche, Seite aus den Features, und die Rechte von Gast und Fremdem.
func TestFeatureAPIWalkThrough(t *testing.T) {
	f := accessAPI(t, true)
	agent := f.id["s-mia"]
	// Anlegen: ein Mitglied darf, ein Gast nicht, ein Fremder kennt das Projekt nicht.
	body := map[string]any{"project": accProject, "name": "Arbeitspakete", "summary": "Ein Branch trägt seine Requests.", "behavior": "Pakete binden Requests an Branches.",
		"limits": "Kein Abgleich mit GitHub.", "cli": "ctx package", "mcp": "package_get", "api": "GET /api/packages", "areas": []string{"internal/request", "internal/store/package.go"},
		"message": "erste Fassung", "session_id": agent}
	f.expect(t, "gus", 403, "POST", "/api/features", body)
	f.expect(t, "nora", 409, "POST", "/api/features", body) // das Projekt gehört einer anderen Org, wie bei jedem Schreiben
	created := decode[feature.Feature](t, f.expect(t, "mia", 201, "POST", "/api/features", body))
	if created.HumanID() == "" || created.Revision != 1 || created.State != feature.Active || strings.Join(created.Areas, ",") != "internal/request,internal/store/package.go" {
		t.Fatalf("created: %+v", created)
	}
	fid := created.ID
	f.expect(t, "mia", 409, "POST", "/api/features", body) // der Name ist vergeben
	f.expect(t, "mia", 400, "POST", "/api/features", map[string]any{"project": accProject, "name": "Schlecht", "areas": []string{"../x"}})
	f.expect(t, "mia", 400, "POST", "/api/features", map[string]any{"project": accProject})

	// Lesen: der Gast darf, der Fremde bekommt dasselbe wie bei einer fehlenden Nummer.
	got := decode[feature.Feature](t, f.expect(t, "gus", 200, "GET", featPath("/api/features/%d", fid), nil))
	if got.Behavior != "Pakete binden Requests an Branches." || got.Versions != 1 {
		t.Fatalf("get: %+v", got)
	}
	stranger := f.expect(t, "nora", 404, "GET", featPath("/api/features/%d", fid), nil)
	missing := f.expect(t, "nora", 404, "GET", featPath("/api/features/%d", fid+99), nil)
	if strings.Replace(stranger, "FEAT-1", "FEAT-N", 1) != strings.Replace(strings.Replace(missing, "FEAT-100", "FEAT-N", 1), "FEAT-2", "FEAT-N", 1) && !strings.Contains(stranger, "feature_not_found") {
		t.Fatalf("a stranger and a missing feature answer differently: %s / %s", stranger, missing)
	}
	f.expect(t, "mia", 400, "GET", "/api/features/abc", nil)

	// Fortschreiben: Anker und Kontext kommen aus den Headern, nie aus dem Körper.
	anchor := gitanchor.Anchor{Commit: "abcdef1234567890abcdef1234567890abcdef12", Branch: "feat/pakete"}
	code, out := f.callWith(t, "mia", "POST", featPath("/api/features/%d/versions", fid), map[string]any{"behavior": "Pakete binden Requests an Branches und PRs.", "message": "PR dazu", "pr": 5, "session_id": agent},
		map[string]string{gitanchor.Header: gitanchor.Encode(anchor)})
	if code != 200 {
		t.Fatalf("update: %d %s", code, out)
	}
	res := decode[struct {
		Version   feature.Version `json:"version"`
		Unchanged bool            `json:"unchanged"`
	}](t, out)
	if res.Version.Revision != 2 || res.Unchanged || res.Version.PR != 5 || res.Version.GitAnchor == nil || res.Version.GitAnchor.Commit != anchor.Commit || !res.Version.ByAgent {
		t.Fatalf("version: %+v", res)
	}
	if again := decode[struct{ Unchanged bool }](t, f.expect(t, "mia", 200, "POST", featPath("/api/features/%d/versions", fid), map[string]any{"behavior": "Pakete binden Requests an Branches und PRs."})); !again.Unchanged {
		t.Fatal("the same text made a version")
	}
	f.expect(t, "gus", 403, "POST", featPath("/api/features/%d/versions", fid), map[string]any{"behavior": "x"})
	f.expect(t, "nora", 404, "POST", featPath("/api/features/%d/versions", fid), map[string]any{"behavior": "x"})
	f.expect(t, "mia", 409, "POST", featPath("/api/features/%d/versions", fid), map[string]any{"behavior": "neu", "base": 1})
	f.expect(t, "mia", 400, "POST", featPath("/api/features/%d/versions", fid), map[string]any{"limits": "a\u0007b"})
	vs := decode[[]feature.Version](t, f.expect(t, "gus", 200, "GET", featPath("/api/features/%d/versions", fid), nil))
	if len(vs) != 2 || vs[0].Behavior != "" || vs[1].PR != 5 || vs[1].Message != "PR dazu" {
		t.Fatalf("versions: %+v", vs)
	}
	full := decode[[]feature.Version](t, f.expect(t, "gus", 200, "GET", featPath("/api/features/%d/versions?full=1", fid), nil))
	if full[0].Behavior != "Pakete binden Requests an Branches." {
		t.Fatalf("full: %+v", full[0])
	}
	old := decode[feature.Version](t, f.expect(t, "gus", 200, "GET", featPath("/api/features/%d/version?revision=1", fid), nil))
	if old.Revision != 1 || old.Behavior != "Pakete binden Requests an Branches." {
		t.Fatalf("old version: %+v", old)
	}
	f.expect(t, "gus", 404, "GET", featPath("/api/features/%d/version?revision=9", fid), nil)

	// Verweise: ein Issue und eine Idee, die das Feature betrifft.
	iid := decode[idea.Idea](t, f.expect(t, "mia", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Pakete pro PR"})).ID
	f.expect(t, "mia", 200, "POST", featPath("/api/features/%d/links", fid), map[string]any{"kind": "request", "ref": f.id["r-mia"], "rel": "origin"})
	f.expect(t, "mia", 400, "POST", featPath("/api/features/%d/links", fid), map[string]any{"kind": "pain", "ref": 1})
	f.expect(t, "mia", 404, "POST", featPath("/api/features/%d/links", fid), map[string]any{"kind": "idea", "ref": 999, "rel": "affects"})
	f.expect(t, "gus", 403, "POST", featPath("/api/ideas/%d/features", iid), map[string]any{"feature": fid})
	f.expect(t, "nora", 404, "POST", featPath("/api/ideas/%d/features", iid), map[string]any{"feature": fid})
	withFeature := decode[idea.Idea](t, f.expect(t, "mia", 200, "POST", featPath("/api/ideas/%d/features", iid), map[string]any{"feature": fid}))
	if len(withFeature.Features) != 1 || withFeature.Features[0].ID != fid || withFeature.Features[0].Advanced {
		t.Fatalf("idea affects: %+v", withFeature.Features)
	}
	ws := decode[store.IdeaWorkshop](t, f.expect(t, "gus", 200, "GET", featPath("/api/ideas/%d/workshop", iid), nil))
	if len(ws.Affects) != 1 || ws.Affects[0].Current.Behavior != "Pakete binden Requests an Branches und PRs." {
		t.Fatalf("workshop shows the state next to the wish: %+v", ws.Affects)
	}
	// Die Umsetzung schreibt die Fassung fort.
	f.expect(t, "mia", 200, "POST", featPath("/api/features/%d/versions", fid), map[string]any{"limits": "Kein Abgleich.", "idea": iid, "requests": []int64{f.id["r-mia"]}})
	view := decode[feature.Feature](t, f.expect(t, "gus", 200, "GET", featPath("/api/features/%d", fid), nil))
	if len(view.Origin) != 1 || view.Origin[0].ID != f.id["r-mia"] || len(view.Affects) != 2 || view.Affects[0].ID != iid || !view.Affects[0].Advanced {
		t.Fatalf("refs: origin %+v affects %+v", view.Origin, view.Affects)
	}
	f.expect(t, "gus", 403, "DELETE", featPath("/api/ideas/%d/features/", iid)+itoaStr(fid), nil)
	f.expect(t, "mia", 200, "DELETE", featPath("/api/ideas/%d/features/", iid)+itoaStr(fid), nil)
	f.expect(t, "mia", 200, "DELETE", featPath("/api/features/%d/links?kind=request&ref=", fid)+itoaStr(f.id["r-mia"])+"&rel=origin", nil)

	// Die Checkliste eines PRs.
	paths := map[string]any{"project": accProject, "pr": 5, "paths": []string{"internal/request/package.go", "README.md", "../x"}}
	f.expect(t, "gus", 403, "POST", "/api/features/pr-paths", paths)
	f.expect(t, "nora", 404, "POST", "/api/features/pr-paths", paths)
	f.expect(t, "mia", 400, "POST", "/api/features/pr-paths", map[string]any{"project": accProject, "pr": 0})
	rep := decode[struct {
		Stored    int                          `json:"stored"`
		Dropped   int                          `json:"dropped"`
		Checklist []requestdomain.FeatureCheck `json:"checklist"`
	}](t, f.expect(t, "mia", 200, "POST", "/api/features/pr-paths", paths))
	if rep.Stored != 2 || rep.Dropped != 1 || len(rep.Checklist) != 1 || rep.Checklist[0].State != requestdomain.FeatureCheckUpdated {
		t.Fatalf("report: %+v", rep)
	}
	list := decode[[]requestdomain.FeatureCheck](t, f.expect(t, "gus", 200, "GET", "/api/features/checklist?project="+url.QueryEscape(accProject)+"&pr=6", nil))
	if len(list) != 0 {
		t.Fatalf("PR 6: %+v", list)
	}
	f.expect(t, "mia", 200, "POST", "/api/features/pr-paths", map[string]any{"project": accProject, "pr": 6, "paths": []string{"internal/request/x.go"}})
	list = decode[[]requestdomain.FeatureCheck](t, f.expect(t, "gus", 200, "GET", "/api/features/checklist?project="+url.QueryEscape(accProject)+"&pr=6", nil))
	if len(list) != 1 || list[0].State != requestdomain.FeatureCheckOpen || !strings.HasSuffix(list[0].Text, "Arbeitspakete: Beschreibung angepasst?") {
		t.Fatalf("PR 6 after the paths: %+v", list)
	}
	f.expect(t, "nora", 404, "GET", "/api/features/checklist?project="+url.QueryEscape(accProject)+"&pr=6", nil)
	f.expect(t, "gus", 403, "POST", featPath("/api/features/%d/checks", fid), map[string]any{"pr": 6, "note": "ok"})
	f.expect(t, "mia", 400, "POST", featPath("/api/features/%d/checks", fid), map[string]any{"pr": 6})
	f.expect(t, "mia", 200, "POST", featPath("/api/features/%d/checks", fid), map[string]any{"pr": 6, "note": "nur umbenannt"})
	list = decode[[]requestdomain.FeatureCheck](t, f.expect(t, "mia", 200, "GET", "/api/features/checklist?project="+url.QueryEscape(accProject)+"&pr=6", nil))
	if list[0].State != requestdomain.FeatureCheckUnchanged {
		t.Fatalf("confirmed: %+v", list)
	}

	// Kontext: kurz, mit Budget und einmal je Session und Fassung.
	ctxURL := "/api/features/context?project=" + url.QueryEscape(accProject) + "&path=internal/request/a.go&session=claude:x1"
	c1 := decode[store.FeatureContext](t, f.expect(t, "gus", 200, "GET", ctxURL, nil))
	c2 := decode[store.FeatureContext](t, f.expect(t, "gus", 200, "GET", ctxURL, nil))
	if c1.Shown != 1 || !strings.Contains(c1.Text, "Arbeitspakete") || c2.Shown != 0 || c2.Text != "" {
		t.Fatalf("context once per session: %+v / %+v", c1, c2)
	}
	if nope := decode[store.FeatureContext](t, f.expect(t, "nora", 200, "GET", ctxURL+"2", nil)); nope.Shown != 0 || nope.Text != "" || len(nope.Features) != 0 {
		t.Fatalf("a stranger gets a context: %+v", nope)
	}

	// Suche: Liste und Qualifier.
	l := decode[[]feature.Feature](t, f.expect(t, "gus", 200, "GET", "/api/features?project="+url.QueryEscape(accProject)+"&q=pakete&path=internal/store/package.go", nil))
	if len(l) != 1 || l[0].ID != fid {
		t.Fatalf("list: %+v", l)
	}
	f.expect(t, "nora", 404, "GET", "/api/features?project="+url.QueryEscape(accProject), nil)
	f.expect(t, "gus", 400, "GET", "/api/features", nil)
	hits := decode[struct {
		Features []feature.Feature `json:"features"`
		Ideas    []idea.Idea       `json:"ideas"`
	}](t, f.expect(t, "gus", 200, "GET", "/api/search?q="+url.QueryEscape("is:feature area:internal/request")+"&project="+url.QueryEscape(accProject), nil))
	if len(hits.Features) != 1 || hits.Features[0].ID != fid {
		t.Fatalf("is:feature area:...: %+v", hits)
	}
	hits = decode[struct {
		Features []feature.Feature `json:"features"`
		Ideas    []idea.Idea       `json:"ideas"`
	}](t, f.expect(t, "gus", 200, "GET", "/api/search?q="+url.QueryEscape("feature:"+itoaStr(fid))+"&project="+url.QueryEscape(accProject), nil))
	if len(hits.Features) != 1 {
		t.Fatalf("feature:N finds the feature: %+v", hits)
	}
	// Eine Idee, die es betrifft, taucht bei feature:N auf; ohne Verweis nicht mehr.
	f.expect(t, "mia", 200, "POST", featPath("/api/ideas/%d/features", iid), map[string]any{"feature": fid})
	hits = decode[struct {
		Features []feature.Feature `json:"features"`
		Ideas    []idea.Idea       `json:"ideas"`
	}](t, f.expect(t, "gus", 200, "GET", "/api/search?q="+url.QueryEscape("feature:"+itoaStr(fid))+"&project="+url.QueryEscape(accProject), nil))
	if len(hits.Ideas) != 1 || hits.Ideas[0].ID != iid {
		t.Fatalf("feature:N finds the idea that affects it: %+v", hits.Ideas)
	}
	if none := decode[struct {
		Features []feature.Feature `json:"features"`
	}](t, f.expect(t, "nora", 200, "GET", "/api/search?q="+url.QueryEscape("is:feature")+"&project="+url.QueryEscape(accProject), nil)); len(none.Features) != 0 {
		t.Fatalf("a stranger finds features: %+v", none.Features)
	}

	// Die Seite aus den Features.
	page := f.expect(t, "gus", 200, "GET", "/api/features/export?project="+url.QueryEscape(accProject), nil)
	if !strings.Contains(page, "- **Arbeitspakete**: Ein Branch trägt seine Requests.") || !strings.Contains(page, "Limits: Kein Abgleich.") {
		t.Fatalf("export:\n%s", page)
	}
	f.expect(t, "nora", 404, "GET", "/api/features/export?project="+url.QueryEscape(accProject), nil)

	// Einstellen: der Lead, nicht das Mitglied.
	f.expect(t, "mia", 403, "POST", featPath("/api/features/%d/state", fid), map[string]any{"state": "retired", "reason": "weg"})
	f.expect(t, "lena", 400, "POST", featPath("/api/features/%d/state", fid), map[string]any{"state": "retired"})
	if st := decode[feature.Feature](t, f.expect(t, "lena", 200, "POST", featPath("/api/features/%d/state", fid), map[string]any{"state": "retired", "reason": "weg"})); st.State != feature.Retired {
		t.Fatalf("retired: %+v", st)
	}
	f.expect(t, "mia", 409, "POST", featPath("/api/features/%d/versions", fid), map[string]any{"summary": "x"})
	if dead := decode[store.FeatureContext](t, f.expect(t, "gus", 200, "GET", ctxURL+"3", nil)); dead.Shown != 0 {
		t.Fatalf("a retired feature is in the context: %+v", dead)
	}
}

func itoaStr(n int64) string { return strconv.FormatInt(n, 10) }

// callWith ist call mit zusätzlichen Headern (der Git-Anker).
func (f *accessAPIFixture) callWith(t *testing.T, who, method, path string, body any, headers map[string]string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	rq.Header.Set("Authorization", "Bearer "+f.tok[who])
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		rq.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Eine Fassung ohne Konto-Zugriff auf das Projekt bleibt unsichtbar: auch in einem
// zweiten Projekt, in dem ein Mitglied nichts darf, gibt es das Feature nicht.
func TestAFeatureOfAnotherProjectIsInvisible(t *testing.T) {
	f := accessAPI(t, true)
	created := decode[feature.Feature](t, f.expect(t, "mia", 201, "POST", "/api/features", map[string]any{"project": accProject, "name": "Nur hier"}))
	// accOther gehört robin allein: mia sieht dort nichts und kann nichts anlegen.
	f.expect(t, "mia", 404, "GET", "/api/features?project="+url.QueryEscape(accOther), nil)
	f.expect(t, "mia", 404, "POST", "/api/features", map[string]any{"project": accOther, "name": "Dort"})
	// Ein Verweis auf eine Idee oder ein Issue aus dem anderen Projekt wird abgelehnt.
	f.expect(t, "mia", 400, "POST", featPath("/api/features/%d/links", created.ID), map[string]any{"kind": "request", "ref": f.id["r-other"], "rel": "origin"})
}

// Die Checkliste des PR-Pakets trägt die Punkte „Feature X: Beschreibung
// angepasst?“, in package_get wie in der Prüfansicht.
func TestThePackageOfAPullRequestShowsTheFeatureItems(t *testing.T) {
	f := accessAPI(t, true)
	fid := decode[feature.Feature](t, f.expect(t, "mia", 201, "POST", "/api/features", map[string]any{"project": accProject, "name": "Pakete", "areas": []string{"internal/store/package.go"}})).ID
	d, err := f.st.CreatePackage(requestdomain.PackageInput{Project: accProject, Branch: "feat/p", Name: "Paket", PR: 21, Members: []requestdomain.PackageMember{{RequestID: f.id["r-mia"]}}, Person: "mia"})
	if err != nil {
		t.Fatal(err)
	}
	pkgURL := featPath("/api/packages/%d", d.ID)
	if got := decode[requestdomain.PackageDetail](t, f.expect(t, "gus", 200, "GET", pkgURL, nil)); len(got.Features) != 0 {
		t.Fatalf("items before any path was reported: %+v", got.Features)
	}
	f.expect(t, "mia", 200, "POST", "/api/features/pr-paths", map[string]any{"project": accProject, "pr": 21, "paths": []string{"internal/store/package.go"}})
	for _, u := range []string{pkgURL, pkgURL + "/review", "/api/packages?project=" + url.QueryEscape(accProject) + "&pr=21&full=1"} {
		got := decode[requestdomain.PackageDetail](t, f.expect(t, "gus", 200, "GET", u, nil))
		if len(got.Features) != 1 || got.Features[0].Feature != fid || got.Features[0].State != requestdomain.FeatureCheckOpen ||
			got.Features[0].Text != "Feature FEAT-"+itoaStr(fid)+" Pakete: Beschreibung angepasst?" {
			t.Fatalf("%s: %+v", u, got.Features)
		}
	}
	f.expect(t, "mia", 200, "POST", featPath("/api/features/%d/versions", fid), map[string]any{"behavior": "neu", "pr": 21})
	if got := decode[requestdomain.PackageDetail](t, f.expect(t, "gus", 200, "GET", pkgURL, nil)); got.Features[0].State != requestdomain.FeatureCheckUpdated {
		t.Fatalf("after the version: %+v", got.Features)
	}
	f.expect(t, "nora", 404, "GET", pkgURL, nil)
}
