package server

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// qWorld ist ein Projekt mit Objekten aller Arten, an denen jeder Qualifier
// etwas zu unterscheiden hat.
type qWorld struct {
	f *accessAPIFixture
	// Ids
	reqA, reqB, reqC, other int64
	critA                   int64
	pain1, pain2, pain3     int64
	idea1, idea2            int64
	taskC                   int64
	know                    int64
}

func (w *qWorld) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := w.f.st.DB().Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (w *qWorld) anchor(t *testing.T, commit, branch string, pr int64) int64 {
	t.Helper()
	var prv any
	if pr > 0 {
		prv = pr
	}
	res, err := w.f.st.DB().Exec(`INSERT INTO git_anchors(commit_sha,branch,merge_base,dirty,pr,captured_at) VALUES(?,?,'',0,?,'2026-11-05T10:00:00Z')`, commit, branch, prv)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (w *qWorld) origin(t *testing.T, area string) int64 {
	t.Helper()
	res, err := w.f.st.DB().Exec(`INSERT INTO origin_contexts(session_ref,captured_at,backfilled) VALUES('s',  '2026-11-04T10:00:00Z',0)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	w.exec(t, `INSERT INTO origin_areas(origin_id,area,weight) VALUES(?,?,3)`, id, area)
	return id
}

func newQWorld(t *testing.T) *qWorld {
	t.Helper()
	f := accessAPI(t, true)
	w := &qWorld{f: f, reqA: f.id["r-mia"], reqB: f.id["r-robin"], other: f.id["r-other"], know: f.id["k-mia"]}
	// Labels: bug und ux, ein Org-Label docs.
	f.expect(t, "lena", 201, "POST", "/api/labels", map[string]any{"project": accProject, "name": "bug", "color": "d73a4a"})
	f.expect(t, "lena", 201, "POST", "/api/labels", map[string]any{"project": accProject, "name": "ux"})
	f.expect(t, "robin", 201, "POST", "/api/labels", map[string]any{"org": "alpha", "name": "docs"})
	// Request A: Kriterium erfüllt mit Nachweis, abgeschlossen, Eltern B, blockiert B.
	f.expect(t, "mia", 201, "POST", idPath("/api/requests/%d/criteria", w.reqA), map[string]any{"description": "Suche liefert Treffer"})
	d := decode[requestdomain.Detail](t, f.expect(t, "mia", 200, "GET", idPath("/api/requests/%d", w.reqA), nil))
	w.critA = d.Criteria[0].ID
	f.expect(t, "mia", 200, "PATCH", idPath("/api/criteria/%d", w.critA), map[string]any{"state": "met", "evidence_kind": "commit", "evidence_ref": "abcdef1"})
	f.expect(t, "mia", 200, "POST", idPath("/api/requests/%d/complete", w.reqA), map[string]any{"evidence_kind": "commit", "evidence_ref": "abcdef1"})
	f.expect(t, "mia", 201, "POST", idPath("/api/requests/%d/relations", w.reqA), map[string]any{"kind": "parent", "other_request_id": w.reqB})
	f.expect(t, "mia", 201, "POST", idPath("/api/requests/%d/relations", w.reqA), map[string]any{"kind": "blocks", "other_request_id": w.reqB})
	// Request C: offen, auf Branch feat/y, mit Aufgabe.
	c := decode[requestdomain.Detail](t, f.expect(t, "mia", 201, "POST", "/api/requests", map[string]any{"project": accProject, "type": "bug", "title": "C kaputt", "description": "d"}))
	w.reqC = c.Request.ID
	pkg := decode[requestdomain.PackageDetail](t, f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{"project": accProject, "branch": "feat/y", "requests": []map[string]any{{"request_id": w.reqC}}}))
	_ = pkg
	pkgA := decode[requestdomain.PackageDetail](t, f.expect(t, "mia", 201, "POST", "/api/packages", map[string]any{"project": accProject, "branch": "feat/x", "requests": []map[string]any{{"request_id": w.reqA}}}))
	w.exec(t, `UPDATE work_packages SET pr=12 WHERE id=?`, pkgA.ID)
	task := f.expect(t, "mia", 201, "POST", "/api/tasks", map[string]any{"project": accProject, "kind": "input", "title": "Antwort holen", "request_id": w.reqC, "assignee": "mia"})
	w.taskC = decode[struct {
		ID int64 `json:"id"`
	}](t, task).ID
	// Pains und Ideen.
	mkPain := func(who, title string) int64 {
		return decode[pain.Detail](t, f.expect(t, who, 201, "POST", "/api/pains", map[string]any{"project": accProject, "title": title})).Pain.ID
	}
	w.pain1, w.pain2, w.pain3 = mkPain("mia", "Suche hakt"), mkPain("robin", "Build lahm"), mkPain("mia", "Menü springt")
	f.expect(t, "robin", 200, "POST", idPath("/api/pains/%d/state", w.pain2), map[string]any{"state": "resolved", "evidence_kind": "url", "evidence_ref": "https://x.example/1"})
	mkIdea := func(who, title string) int64 {
		return decode[idea.Idea](t, f.expect(t, who, 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": title, "body": "ein Satz"})).ID
	}
	w.idea1, w.idea2 = mkIdea("mia", "Dunkles Thema"), mkIdea("robin", "Schnellwahl")
	f.expect(t, "robin", 200, "POST", idPath("/api/ideas/%d/state", w.idea2), map[string]any{"state": "dismissed", "reason": "gibt es"})
	// Labels setzen.
	set := func(kind string, id int64, names ...string) {
		f.expect(t, "mia", 200, "POST", "/api/label-assignments", map[string]any{"kind": kind, "id": id, "add": names})
	}
	set("request", w.reqA, "bug")
	set("issue", w.reqC, "docs")
	set("pain", w.pain1, "bug")
	set("idea", w.idea1, "ux")
	// Anker und Kontexte.
	pr12 := w.anchor(t, "abcdef1234567", "feat/x", 12)
	y := w.anchor(t, "1111111aaaaaaa", "feat/y", 0)
	w.exec(t, `UPDATE request_activity SET anchor_id=? WHERE request_id=? AND kind='request.created'`, pr12, w.reqA)
	w.exec(t, `UPDATE request_evidence SET anchor_id=? WHERE request_id=?`, pr12, w.reqA)
	w.exec(t, `UPDATE knowledge SET anchor_id=? WHERE id=?`, pr12, w.know)
	// pain_events ist unveränderlich, auch gegen den Anker; der Test setzt ihn dort,
	// wo ein Client ihn beim Schreiben mitgeschickt hätte.
	w.exec(t, `DROP TRIGGER pain_events_no_update`)
	w.exec(t, `UPDATE pain_events SET anchor_id=? WHERE pain_id=? AND kind='created'`, pr12, w.pain1)
	w.exec(t, `UPDATE ideas SET anchor_id=? WHERE id=?`, pr12, w.idea1)
	w.exec(t, `UPDATE pain_events SET anchor_id=? WHERE pain_id=? AND kind='created'`, y, w.pain3)
	web := w.origin(t, "internal/web")
	w.exec(t, `UPDATE request_activity SET origin_id=? WHERE request_id=? AND kind='request.created'`, web, w.reqB)
	w.exec(t, `UPDATE pain_events SET origin_id=? WHERE pain_id=? AND kind='created'`, web, w.pain2)
	// Thema.
	f.expect(t, "mia", 200, "POST", "/api/topics", map[string]any{"project": accProject, "name": "Oberfläche", "prefixes": []string{"internal/web"}})
	return w
}

// refs bringt eine Antwort auf kurze Namen: K7 R3 P1 I2 C3.1 T4 F:a.go, sortiert.
func (w *qWorld) refs(t *testing.T, who, line string, extra ...string) []string {
	t.Helper()
	q := url.Values{"q": {line}, "project": {accProject}, "limit": {"50"}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return w.collect(t, who, "/api/search?"+q.Encode())
}

type qResult struct {
	Knowledge []store.Knowledge `json:"knowledge"`
	Requests  []struct {
		Request requestdomain.Request `json:"request"`
	} `json:"requests"`
	Pains    []pain.Pain          `json:"pains"`
	Ideas    []idea.Idea          `json:"ideas"`
	Criteria []store.CriterionHit `json:"criteria"`
	Tasks    []struct {
		ID int64 `json:"id"`
	} `json:"tasks"`
	Files []store.GhostFile `json:"files"`
}

func (w *qWorld) collect(t *testing.T, who, path string) []string {
	t.Helper()
	res := decode[qResult](t, w.f.expect(t, who, 200, "GET", path, nil))
	var got []string
	for _, k := range res.Knowledge {
		got = append(got, "K"+i64(k.ID))
	}
	for _, r := range res.Requests {
		got = append(got, "R"+i64(r.Request.ID))
	}
	for _, p := range res.Pains {
		got = append(got, "P"+i64(p.ID))
	}
	for _, i := range res.Ideas {
		got = append(got, "I"+i64(i.ID))
	}
	for _, c := range res.Criteria {
		got = append(got, "C"+i64(c.RequestID)+"."+i64(int64(c.Number)))
	}
	for _, tk := range res.Tasks {
		got = append(got, "T"+i64(tk.ID))
	}
	for _, g := range res.Files {
		got = append(got, "F:"+g.Path)
	}
	sort.Strings(got)
	return got
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

func want(t *testing.T, w *qWorld, who, line string, expect ...string) {
	t.Helper()
	sort.Strings(expect)
	got := w.refs(t, who, line)
	if strings.Join(got, " ") != strings.Join(expect, " ") {
		t.Errorf("%s: %q = %v, want %v", who, line, got, expect)
	}
}

func TestEveryQualifierOverTheSearchRoute(t *testing.T) {
	w := newQWorld(t)
	a, b, c := "R"+i64(w.reqA), "R"+i64(w.reqB), "R"+i64(w.reqC)
	p1, p2, p3 := "P"+i64(w.pain1), "P"+i64(w.pain2), "P"+i64(w.pain3)
	i1, i2 := "I"+i64(w.idea1), "I"+i64(w.idea2)
	ac := "C" + i64(w.reqA) + ".1"
	tk := "T" + i64(w.taskC)
	know := "K" + i64(w.know)

	// is:
	want(t, w, "mia", "kind:request is:open", b, c)
	want(t, w, "mia", "kind:request is:done", a)
	want(t, w, "mia", "is:pain", p1, p2, p3)
	want(t, w, "mia", "is:pain is:open", p1, p3)
	want(t, w, "mia", "is:pain is:resolved", p2)
	want(t, w, "mia", "is:idea", i1, i2)
	want(t, w, "mia", "is:idea is:done")
	want(t, w, "mia", "is:idea is:closed", i2)
	want(t, w, "mia", "is:idea is:dismissed", i2)
	want(t, w, "mia", "is:issue is:done", a)
	want(t, w, "mia", "is:knowledge pr:12", know)
	want(t, w, "mia", "kind:criterion", ac)
	want(t, w, "mia", "kind:task", tk)
	// -is:
	want(t, w, "mia", "kind:pain -is:open", p2)
	// type:
	want(t, w, "mia", "kind:request type:bug", c)
	want(t, w, "mia", "kind:request type:feature", a, b)
	want(t, w, "mia", "kind:request type:investigation")
	// Staged entries are left out of agent search unless the line asks for them.
	want(t, w, "mia", "kind:knowledge type:pitfall", know)
	want(t, w, "mia", "kind:knowledge type:pitfall is:staged", "K"+i64(w.f.id["k-robin-staged"]))
	// repo:
	want(t, w, "mia", "kind:request repo:dw/p", a, b, c)
	want(t, w, "mia", "kind:request repo:github.com/dw/p -is:open", a)
	// branch:
	want(t, w, "mia", "kind:request branch:feat/x", a)
	want(t, w, "mia", "branch:feat/y is:open", c, p3, tk)
	want(t, w, "mia", "kind:request -branch:feat/x", b, c)
	// commit:
	want(t, w, "mia", "commit:abcdef1", a, ac, know, p1, i1)
	want(t, w, "mia", "commit:abcdef1 kind:pain", p1)
	want(t, w, "mia", "commit:1111111 kind:pain", p3)
	want(t, w, "mia", "kind:pain -commit:abcdef1", p2, p3)
	// pr:
	want(t, w, "mia", "pr:12 kind:request", a)
	want(t, w, "mia", "pr:12 kind:knowledge", know)
	want(t, w, "mia", "pr:12 kind:criterion", ac)
	want(t, w, "mia", "pr:10..14 kind:pain", p1)
	want(t, w, "mia", "pr:13.. kind:pain")
	want(t, w, "mia", "pr:#12 kind:idea", i1)
	want(t, w, "mia", "kind:pain -pr:12", p2, p3)
	// package:
	pk := decode[[]requestdomain.PackageDetail](t, "["+w.f.expect(t, "mia", 200, "GET", "/api/packages?project="+accProject+"&branch=feat/x", nil)+"]")
	want(t, w, "mia", "kind:request package:"+i64(pk[0].ID), a)
	// req:/task:/pain:/idea:
	want(t, w, "mia", "req:"+i64(w.reqA), a, ac)
	want(t, w, "mia", "req:REQ-"+i64(w.reqA)+" kind:request", a)
	want(t, w, "mia", "task:"+i64(w.taskC), tk)
	want(t, w, "mia", "pain:"+i64(w.pain1), p1)
	want(t, w, "mia", "pain:"+i64(w.pain1)+".."+i64(w.pain2), p1, p2)
	want(t, w, "mia", "idea:"+i64(w.idea2), i2)
	want(t, w, "mia", "pain:"+i64(w.pain1)+" idea:"+i64(w.idea2), p1, i2)
	// parent:/blocks:
	want(t, w, "mia", "parent:"+i64(w.reqB), a)
	want(t, w, "mia", "blocks:"+i64(w.reqB), a)
	want(t, w, "mia", "parent:"+i64(w.reqA))
	// author:/for:
	want(t, w, "mia", "author:mia kind:request", a, c)
	want(t, w, "mia", "author:robin kind:pain", p2)
	want(t, w, "mia", "author:@me kind:idea", i1)
	want(t, w, "robin", "author:@me kind:idea", i2)
	want(t, w, "mia", "for:mia kind:task", tk)
	want(t, w, "mia", "for:@me kind:task", tk)
	want(t, w, "robin", "for:@me kind:task")
	// label:
	want(t, w, "mia", "label:bug", a, p1)
	want(t, w, "mia", "-label:bug kind:pain", p2, p3)
	want(t, w, "mia", "label:ux", i1)
	want(t, w, "mia", "label:docs", c)
	want(t, w, "mia", "label:bug,ux", a, p1, i1)
	want(t, w, "mia", "label:BUG kind:request", a)
	want(t, w, "mia", "-label:bug kind:request", b, c)
	// area:/topic:
	want(t, w, "mia", "area:internal/web kind:request", b)
	want(t, w, "mia", "area:internal kind:pain", p2)
	want(t, w, "mia", "area:internal/store kind:request")
	want(t, w, "mia", "-area:internal/web kind:request", a, c)
	want(t, w, "mia", "topic:oberfläche kind:request", b)
	want(t, w, "mia", "-topic:oberfläche kind:request", a, c)
	// created:/updated:
	want(t, w, "mia", "created:>2020-01-01 kind:idea", i1, i2)
	want(t, w, "mia", "created:<2020-01-01 kind:idea")
	want(t, w, "mia", "created:2020-01-01..2099-01-01 kind:pain", p1, p2, p3)
	want(t, w, "mia", "updated:>=2020-01-01 kind:pain", p1, p2, p3)
	want(t, w, "mia", "updated:<2020-01-01 kind:pain")
	want(t, w, "mia", "-created:<2020-01-01 kind:idea", i1, i2)
	// Freitext dazu.
	want(t, w, "mia", "dunkles is:idea", i1)
	want(t, w, "mia", "hakt is:pain", p1)
	want(t, w, "mia", "mia is:open kind:request")
	// since:/until: nach dem Entstehungskontext.
	want(t, w, "mia", "since:2026-11-04 until:2026-11-04 area:internal/web kind:request", b)
	want(t, w, "mia", "since:2026-11-05 area:internal/web kind:request")
}

func TestUnknownAndBrokenQualifiersAreVisibleErrors(t *testing.T) {
	w := newQWorld(t)
	for _, line := range []string{"flavor:sweet", "is:oen", "label:", "pr:abc", "commit:xyz", "created:gestern", "topic:gibtsnicht", "-nur", `label:"offen`} {
		out := w.f.expect(t, "mia", 400, "GET", "/api/search?project="+accProject+"&q="+url.QueryEscape(line), nil)
		if !strings.Contains(out, "invalid_query") {
			t.Errorf("%q: %s", line, out)
		}
	}
	out := w.f.expect(t, "mia", 400, "GET", "/api/search?project="+accProject+"&q="+url.QueryEscape("flavor:sweet"), nil)
	if !strings.Contains(out, "unknown qualifier") {
		t.Errorf("message: %s", out)
	}
	w.f.expect(t, "mia", 400, "GET", "/api/ghosts/search?project="+accProject+"&q="+url.QueryEscape("flavor:sweet"), nil)
}

func TestQualifierSearchRespectsVisibility(t *testing.T) {
	w := newQWorld(t)
	// Ein Fremder bekommt nichts, auch nicht mit Qualifiern, die auf Objekte zielen.
	for _, line := range []string{"is:pain", "is:idea", "kind:request", "label:bug", "branch:feat/y", "kind:task", "kind:criterion"} {
		if got := w.refs(t, "nora", line); len(got) != 0 {
			t.Errorf("stranger: %q = %v", line, got)
		}
	}
	// Ein Thema des Projekts nennt der Fremde nicht, und es gibt keinen Unterschied
	// zu einem unbekannten Thema in der Antwort für das Projekt, das er nicht sieht.
	code1, out1 := w.f.call(t, "nora", "GET", "/api/search?project="+accProject+"&q="+url.QueryEscape("topic:oberfläche"), nil)
	code2, out2 := w.f.call(t, "nora", "GET", "/api/search?project="+accProject+"&q="+url.QueryEscape("topic:nirgends"), nil)
	if code1 != 404 || code2 != 404 || out1 != out2 {
		t.Errorf("topic oracle: %d %q / %d %q", code1, out1, code2, out2)
	}
	// Der Gast sieht Pains, Ideen und Requests, aber keine Aufgaben, die ihm nicht gehören.
	if got := w.refs(t, "gus", "kind:task"); len(got) != 0 {
		t.Errorf("guest sees tasks: %v", got)
	}
	if got := w.refs(t, "gus", "is:pain"); len(got) != 3 {
		t.Errorf("guest pains: %v", got)
	}
	// Ein anderes Projekt: repo: wechselt das Projekt, die Rechte bleiben.
	if got := w.refs(t, "robin", "kind:request repo:dw/q"); len(got) != 1 || got[0] != "R"+i64(w.other) {
		t.Errorf("owner in the other project: %v", got)
	}
	if got := w.refs(t, "mia", "kind:request repo:dw/q"); len(got) != 0 {
		t.Errorf("mia has no role in the other project: %v", got)
	}
	if got := w.refs(t, "nora", "kind:request repo:dw/q"); len(got) != 0 {
		t.Errorf("stranger via repo: %v", got)
	}
	// parent: nennt keinen Request, den der Betrachter nicht lesen darf: der
	// andere Request im fremden Projekt wird nie als Eltern sichtbar.
	var hidden int64 = w.other
	w.exec(t, `INSERT INTO request_relations(request_id,other_request_id,kind,created_at) VALUES(?,?, 'parent','2026-11-05T10:00:00Z')`, w.reqC, hidden)
	if got := w.refs(t, "robin", "parent:"+i64(hidden)+" kind:request"); len(got) != 1 {
		t.Errorf("owner reads both requests: %v", got)
	}
	for _, who := range []string{"mia", "gus"} {
		if got := w.refs(t, who, "parent:"+i64(hidden)); len(got) != 0 {
			t.Errorf("%s sees a parent he cannot read: %v", who, got)
		}
	}
}

func TestQualifierSearchOnFilesAndPlainTextStaysAsBefore(t *testing.T) {
	w := newQWorld(t)
	// Dateien: Bereich über den Pfad, ohne Wörter.
	w.f.expect(t, "mia", 200, "POST", "/api/ghosts", map[string]any{"project": accProject, "path": "internal/web/x.go", "kind": "file", "description": "Seitenvorlage"})
	got := w.collect(t, "mia", "/api/search?project="+accProject+"&q="+url.QueryEscape("kind:file area:internal/web"))
	if len(got) != 1 || got[0] != "F:internal/web/x.go" {
		t.Errorf("files: %v", got)
	}
	out := w.f.expect(t, "mia", 200, "GET", "/api/ghosts/search?project="+accProject+"&q="+url.QueryEscape("area:internal/web Seitenvorlage"), nil)
	if !strings.Contains(out, "internal/web/x.go") {
		t.Errorf("ghosts search with a qualifier: %s", out)
	}
	out = w.f.expect(t, "mia", 200, "GET", "/api/ghosts/search?project="+accProject+"&q="+url.QueryEscape("label:bug Seitenvorlage"), nil)
	if strings.Contains(out, "internal/web/x.go") {
		t.Errorf("a file carries no label: %s", out)
	}
	// Ohne Qualifier bleibt die Antwort die alte: Wissen, Sessions, Requests.
	out = w.f.expect(t, "mia", 200, "GET", "/api/search?project="+accProject+"&q=pitfall", nil)
	if strings.Contains(out, `"pains"`) || strings.Contains(out, `"query"`) {
		t.Errorf("plain text search changed shape: %s", out)
	}
}
