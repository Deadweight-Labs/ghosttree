package server

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestLabelRoutesRightsAndSameAnswerForUnknownAndForeign(t *testing.T) {
	f := accessAPI(t, true)
	// Anlegen: Mitglied ja, Gast 403, Fremder 409/404 wie bei den anderen Projekt-Routen.
	f.expect(t, "mia", 201, "POST", "/api/labels", map[string]any{"project": accProject, "name": "bug", "color": "#d73a4a", "description": "kaputt"})
	f.expect(t, "gus", 403, "POST", "/api/labels", map[string]any{"project": accProject, "name": "x"})
	f.expect(t, "mia", 409, "POST", "/api/labels", map[string]any{"project": accProject, "name": "BUG"})
	f.expect(t, "mia", 400, "POST", "/api/labels", map[string]any{"project": accProject, "name": "a\nb"})
	f.expect(t, "mia", 400, "POST", "/api/labels", map[string]any{"project": accProject, "name": "a,b"})
	f.expect(t, "mia", 400, "POST", "/api/labels", map[string]any{"project": accProject, "name": "ok", "color": "rot"})
	// Org-Label: nur Org-Owner.
	f.expect(t, "mia", 403, "POST", "/api/labels", map[string]any{"org": "alpha", "name": "docs"})
	f.expect(t, "nora", 404, "POST", "/api/labels", map[string]any{"org": "alpha", "name": "docs"})
	f.expect(t, "nora", 404, "POST", "/api/labels", map[string]any{"org": "gibt-es-nicht", "name": "docs"})
	f.expect(t, "robin", 201, "POST", "/api/labels", map[string]any{"org": "alpha", "name": "docs"})
	// Lesen: Gast ja, Fremder nicht.
	list := decode[[]store.Label](t, f.expect(t, "gus", 200, "GET", "/api/labels?project="+accProject, nil))
	if len(list) != 2 {
		t.Fatalf("labels: %+v", list)
	}
	f.expect(t, "nora", 404, "GET", "/api/labels?project="+accProject, nil)
	f.expect(t, "mia", 200, "GET", "/api/labels?org=alpha", nil)
	f.expect(t, "nora", 404, "GET", "/api/labels?org=alpha", nil)
	// Ändern und löschen: lead; unbekannt und fremd gleich.
	bug := list[0]
	f.expect(t, "mia", 403, "PATCH", idPath("/api/labels/%d", bug.ID), map[string]any{"color": "00ff00"})
	f.expect(t, "lena", 200, "PATCH", idPath("/api/labels/%d", bug.ID), map[string]any{"color": "00ff00"})
	a := f.expect(t, "nora", 404, "PATCH", idPath("/api/labels/%d", bug.ID), map[string]any{"color": "00ff00"})
	b := f.expect(t, "nora", 404, "PATCH", "/api/labels/99999", map[string]any{"color": "00ff00"})
	if a != b {
		t.Errorf("foreign and unknown differ: %q / %q", a, b)
	}
	f.expect(t, "mia", 403, "DELETE", idPath("/api/labels/%d", bug.ID), nil)
	f.expect(t, "nora", 404, "DELETE", idPath("/api/labels/%d", bug.ID), nil)
	f.expect(t, "lena", 204, "DELETE", idPath("/api/labels/%d", bug.ID), nil)
	f.expect(t, "lena", 404, "DELETE", idPath("/api/labels/%d", bug.ID), nil)
}

func TestLabelAssignmentRoutesUseTheRightsOfTheObject(t *testing.T) {
	f := accessAPI(t, true)
	f.expect(t, "lena", 201, "POST", "/api/labels", map[string]any{"project": accProject, "name": "bug"})
	f.expect(t, "robin", 201, "POST", "/api/labels", map[string]any{"project": accOther, "name": "geheim"})
	req := f.id["r-mia"]
	set := func(who string, want int, kind string, id int64, add ...string) string {
		t.Helper()
		return f.expect(t, who, want, "POST", "/api/label-assignments", map[string]any{"kind": kind, "id": id, "add": add})
	}
	set("mia", 200, "issue", req, "bug")
	set("mia", 200, "request", req, "bug")
	set("gus", 403, "request", req, "bug")
	// Unbekanntes Objekt und fremdes Objekt: dieselbe Antwort.
	unknown := set("nora", 404, "request", 99999, "bug")
	foreign := set("nora", 404, "request", req, "bug")
	if unknown != foreign {
		t.Errorf("unknown and foreign differ: %q / %q", unknown, foreign)
	}
	set("mia", 404, "request", f.id["r-other"], "bug")
	set("mia", 404, "request", 99999, "bug")
	// Ein Label eines anderen Projekts gilt wie ein unbekannter Name.
	one := set("mia", 404, "request", req, "geheim")
	two := set("mia", 404, "request", req, "gibt-es-nicht")
	if strings.ReplaceAll(one, "geheim", "X") != strings.ReplaceAll(two, "gibt-es-nicht", "X") {
		t.Errorf("foreign label and unknown name differ: %q / %q", one, two)
	}
	f.expect(t, "mia", 400, "POST", "/api/label-assignments", map[string]any{"kind": "request", "id": req})
	f.expect(t, "mia", 400, "POST", "/api/label-assignments", map[string]any{"kind": "session", "id": 1, "add": []string{"bug"}})
	got := decode[[]store.Label](t, f.expect(t, "gus", 200, "GET", idPath("/api/label-assignments?kind=request&id=%d", req), nil))
	if len(got) != 1 || got[0].Name != "bug" {
		t.Errorf("labels of the request: %+v", got)
	}
	f.expect(t, "nora", 404, "GET", idPath("/api/label-assignments?kind=request&id=%d", req), nil)
	// Der Request zeigt seine Labels.
	d := decode[requestdomain.Detail](t, f.expect(t, "gus", 200, "GET", idPath("/api/requests/%d", req), nil))
	if len(d.Labels) != 1 || d.Labels[0].Name != "bug" {
		t.Errorf("request detail labels: %+v", d.Labels)
	}
	f.expect(t, "mia", 200, "POST", "/api/label-assignments", map[string]any{"kind": "request", "id": req, "remove": []string{"bug"}})
	if got := decode[[]store.Label](t, f.expect(t, "gus", 200, "GET", idPath("/api/label-assignments?kind=request&id=%d", req), nil)); len(got) != 0 {
		t.Errorf("after remove: %+v", got)
	}
}

func TestTopicRoutes(t *testing.T) {
	f := accessAPI(t, true)
	f.expect(t, "mia", 200, "POST", "/api/topics", map[string]any{"project": accProject, "name": "Suche", "prefixes": []string{"internal/embed", "internal/store/embeddings"}})
	f.expect(t, "gus", 403, "POST", "/api/topics", map[string]any{"project": accProject, "name": "x", "prefixes": []string{"a"}})
	f.expect(t, "mia", 400, "POST", "/api/topics", map[string]any{"project": accProject, "name": "x", "prefixes": []string{"/abs"}})
	f.expect(t, "mia", 400, "POST", "/api/topics", map[string]any{"project": accProject, "name": "x"})
	list := decode[[]store.Topic](t, f.expect(t, "gus", 200, "GET", "/api/topics?project="+accProject, nil))
	if len(list) != 1 || len(list[0].Prefixes) != 2 {
		t.Fatalf("topics: %+v", list)
	}
	f.expect(t, "nora", 404, "GET", "/api/topics?project="+accProject, nil)
	f.expect(t, "mia", 403, "DELETE", "/api/topics?project="+accProject+"&name=Suche", nil)
	f.expect(t, "lena", 204, "DELETE", "/api/topics?project="+accProject+"&name=suche", nil)
	f.expect(t, "lena", 404, "DELETE", "/api/topics?project="+accProject+"&name=suche", nil)
}

func TestIdeaRoutes(t *testing.T) {
	f := accessAPI(t, true)
	post := func(who string, want int, body map[string]any) idea.Idea {
		t.Helper()
		out := f.expect(t, who, want, "POST", "/api/ideas", body)
		if want != 201 {
			return idea.Idea{}
		}
		return decode[idea.Idea](t, out)
	}
	i := post("mia", 201, map[string]any{"project": accProject, "title": "Dunkles Thema", "body": "ein Satz"})
	if i.ID == 0 || i.State != "open" || i.HumanID() != "IDEA-1" {
		t.Fatalf("idea: %+v", i)
	}
	post("mia", 400, map[string]any{"project": accProject, "title": ""})
	post("mia", 400, map[string]any{"project": accProject, "title": "a\nb"})
	post("gus", 403, map[string]any{"project": accProject, "title": "x"})
	out := f.expect(t, "lena", 409, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "dunkles thema"})
	if !strings.Contains(out, "idea_duplicate") || !strings.Contains(out, "IDEA-1") {
		t.Errorf("duplicate: %s", out)
	}
	post("lena", 201, map[string]any{"project": accProject, "title": "dunkles thema", "allow_duplicate": true})
	// Lesen: Gast ja, Fremder: unbekannt und fremd gleich.
	f.expect(t, "gus", 200, "GET", idPath("/api/ideas/%d", i.ID), nil)
	a := f.expect(t, "nora", 404, "GET", idPath("/api/ideas/%d", i.ID), nil)
	b := f.expect(t, "nora", 404, "GET", "/api/ideas/99999", nil)
	if strings.ReplaceAll(a, "IDEA-1", "IDEA-N") != strings.ReplaceAll(b, "IDEA-99999", "IDEA-N") {
		t.Errorf("foreign and unknown differ: %q / %q", a, b)
	}
	list := decode[[]idea.Idea](t, f.expect(t, "gus", 200, "GET", "/api/ideas?project="+accProject, nil))
	if len(list) != 2 {
		t.Errorf("ideas: %+v", list)
	}
	f.expect(t, "nora", 404, "GET", "/api/ideas?project="+accProject, nil)
	// Zustand.
	f.expect(t, "gus", 403, "POST", idPath("/api/ideas/%d/state", i.ID), map[string]any{"state": "dismissed", "reason": "x"})
	f.expect(t, "nora", 404, "POST", idPath("/api/ideas/%d/state", i.ID), map[string]any{"state": "dismissed", "reason": "x"})
	f.expect(t, "mia", 400, "POST", idPath("/api/ideas/%d/state", i.ID), map[string]any{"state": "dismissed"})
	d := decode[idea.Idea](t, f.expect(t, "mia", 200, "POST", idPath("/api/ideas/%d/state", i.ID), map[string]any{"state": "dismissed", "reason": "gibt es"}))
	if d.State != "dismissed" || d.Reason != "gibt es" {
		t.Errorf("dismissed: %+v", d)
	}
	f.expect(t, "mia", 200, "POST", idPath("/api/ideas/%d/state", i.ID), map[string]any{"state": "open"})
	// Verknüpfen: der Request muss lesbar und bearbeitbar sein.
	req := f.id["r-mia"]
	f.expect(t, "nora", 404, "POST", idPath("/api/ideas/%d/requests", i.ID), map[string]any{"request_id": req})
	f.expect(t, "mia", 404, "POST", idPath("/api/ideas/%d/requests", i.ID), map[string]any{"request_id": 99999})
	f.expect(t, "mia", 404, "POST", idPath("/api/ideas/%d/requests", i.ID), map[string]any{"request_id": f.id["r-other"]})
	got := decode[idea.Idea](t, f.expect(t, "mia", 200, "POST", idPath("/api/ideas/%d/requests", i.ID), map[string]any{"request_id": req}))
	if got.State != "accepted" || len(got.Requests) != 1 || got.Requests[0] != req {
		t.Errorf("linked: %+v", got)
	}
	// Beidseitig sichtbar: der Request nennt seine Idee.
	rd := decode[requestdomain.Detail](t, f.expect(t, "gus", 200, "GET", idPath("/api/requests/%d", req), nil))
	if len(rd.Ideas) != 1 || rd.Ideas[0].ID != i.ID {
		t.Errorf("request ideas: %+v", rd.Ideas)
	}
	// Anker und Kontext kommen nur aus dem Header, nie aus dem Body.
	body := map[string]any{"project": accProject, "title": "Mit Anker", "git_anchor": map[string]any{"commit": "deadbeefdeadbeef"},
		"origin_context": map[string]any{"session_ref": "x", "areas": []map[string]any{{"path": "internal/geheim", "weight": 9}}}}
	made := decode[idea.Idea](t, f.expect(t, "mia", 201, "POST", "/api/ideas", body))
	if made.GitAnchor != nil || made.OriginContext != nil {
		t.Errorf("anchor or context taken from the body: %+v / %+v", made.GitAnchor, made.OriginContext)
	}
}

func TestIdeaSearchStatesAndIsIdea(t *testing.T) {
	f := accessAPI(t, true)
	f.expect(t, "mia", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Offen eins"})
	two := decode[idea.Idea](t, f.expect(t, "mia", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Verworfen zwei"}))
	f.expect(t, "mia", 200, "POST", idPath("/api/ideas/%d/state", two.ID), map[string]any{"state": "dismissed", "reason": "nein"})
	open := decode[[]idea.Idea](t, f.expect(t, "mia", 200, "GET", "/api/ideas?project="+accProject, nil))
	all := decode[[]idea.Idea](t, f.expect(t, "mia", 200, "GET", "/api/ideas?project="+accProject+"&state=all", nil))
	words := decode[[]idea.Idea](t, f.expect(t, "mia", 200, "GET", "/api/ideas?project="+accProject+"&state=all&q=verworfen", nil))
	if len(open) != 1 || len(all) != 2 || len(words) != 1 || words[0].ID != two.ID {
		t.Errorf("open=%d all=%d words=%d", len(open), len(all), len(words))
	}
	f.expect(t, "mia", 400, "GET", "/api/ideas?project="+accProject+"&state=kaputt", nil)
	var res struct {
		Ideas []idea.Idea `json:"ideas"`
	}
	json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", "/api/search?project="+accProject+"&q="+url.QueryEscape("is:idea idea:"+i64(two.ID)), nil)), &res)
	if len(res.Ideas) != 1 || res.Ideas[0].ID != two.ID {
		t.Errorf("is:idea idea:N: %+v", res.Ideas)
	}
}

func TestSavedSearchRoutesAndBuiltinList(t *testing.T) {
	f := accessAPI(t, true)
	list := decode[[]store.SearchPreset](t, f.expect(t, "mia", 200, "GET", "/api/search-presets?project="+accProject, nil))
	names := map[string]bool{}
	for _, p := range list {
		names[p.Name] = p.Builtin
	}
	for _, n := range []string{"pr-ergebnis", "branch-offen", "ordner", "seit", "fuer-mich"} {
		if !names[n] {
			t.Errorf("builtin %s missing from the list: %v", n, names)
		}
	}
	f.expect(t, "mia", 200, "POST", "/api/search-presets", map[string]any{"project": accProject, "name": "meine-bugs", "queries": []string{"label:bug is:open"}})
	f.expect(t, "mia", 400, "POST", "/api/search-presets", map[string]any{"project": accProject, "name": "kaputt", "queries": []string{"flavor:x"}})
	f.expect(t, "mia", 403, "POST", "/api/search-presets", map[string]any{"project": accProject, "scope": "org", "name": "x", "queries": []string{"is:open"}})
	f.expect(t, "gus", 200, "POST", "/api/search-presets", map[string]any{"project": accProject, "name": "gast", "queries": []string{"is:open"}})
	f.expect(t, "gus", 403, "POST", "/api/search-presets", map[string]any{"project": accProject, "scope": "project", "name": "gp", "queries": []string{"is:open"}})
	f.expect(t, "nora", 409, "POST", "/api/search-presets", map[string]any{"project": accProject, "name": "x", "queries": []string{"is:open"}})
	mine := decode[[]store.SearchPreset](t, f.expect(t, "mia", 200, "GET", "/api/search-presets?project="+accProject, nil))
	var id int64
	for _, p := range mine {
		if p.Name == "meine-bugs" {
			id = p.ID
		}
		if p.Name == "gast" {
			t.Errorf("a personal search leaked to another account")
		}
	}
	f.expect(t, "lena", 404, "DELETE", idPath("/api/search-presets/%d", id), nil)
	f.expect(t, "lena", 404, "DELETE", "/api/search-presets/99999", nil)
	f.expect(t, "mia", 204, "DELETE", idPath("/api/search-presets/%d", id), nil)
}

// presetRun führt eine Suche aus und liefert die kurzen Namen der Treffer.
func (w *qWorld) presetRun(t *testing.T, who string, want int, name string, args ...string) []string {
	t.Helper()
	q := url.Values{"project": {accProject}, "name": {name}, "limit": {"50"}}
	for i := 0; i+1 < len(args); i += 2 {
		q.Set(args[i], args[i+1])
	}
	out := w.f.expect(t, who, want, "GET", "/api/search-presets/run?"+q.Encode(), nil)
	if want != 200 {
		return nil
	}
	var env struct {
		Queries []string `json:"queries"`
		Result  qResult  `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	return flatten(env.Result)
}

func flatten(res qResult) []string {
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

func wantPreset(t *testing.T, got []string, expect ...string) {
	t.Helper()
	sort.Strings(expect)
	if strings.Join(got, " ") != strings.Join(expect, " ") {
		t.Errorf("got %v, want %v", got, expect)
	}
}

func TestPresetPrErgebnis(t *testing.T) {
	w := newQWorld(t)
	got := w.presetRun(t, "mia", 200, "pr-ergebnis", "pr", "12")
	// Das erfüllte Kriterium, der abgeschlossene Request, das Wissen, der Pain
	// und die Idee mit dem PR im Anker.
	wantPreset(t, got, "C"+i64(w.reqA)+".1", "R"+i64(w.reqA), "K"+i64(w.know), "P"+i64(w.pain1), "I"+i64(w.idea1))
	wantPreset(t, w.presetRun(t, "mia", 200, "pr-ergebnis", "pr", "99"))
	// Der Nachweis steht am Kriterium.
	out := w.f.expect(t, "mia", 200, "GET", "/api/search-presets/run?project="+accProject+"&name=pr-ergebnis&pr=12", nil)
	if !strings.Contains(out, `"ref":"abcdef1"`) {
		t.Errorf("the criterion carries its evidence: %s", out)
	}
	// Argumente: fehlendes, unbekanntes, eingeschleustes.
	w.presetRun(t, "mia", 400, "pr-ergebnis")
	w.presetRun(t, "mia", 400, "pr-ergebnis", "pr", "12", "branch", "x")
	w.presetRun(t, "mia", 400, "pr-ergebnis", "pr", "12 is:open")
	w.presetRun(t, "mia", 400, "pr-ergebnis", "pr", "abc")
	w.presetRun(t, "mia", 404, "gibt-es-nicht")
	// Ein Fremder bekommt nichts, der Gast nur, was er lesen darf (Wissen nur trusted).
	w.presetRun(t, "nora", 404, "pr-ergebnis", "pr", "12")
}

func TestPresetBranchOffen(t *testing.T) {
	w := newQWorld(t)
	got := w.presetRun(t, "mia", 200, "branch-offen", "branch", "feat/y")
	// Offene Requests, Pains und Aufgaben auf dem Branch; nicht der erledigte A.
	wantPreset(t, got, "R"+i64(w.reqC), "P"+i64(w.pain3), "T"+i64(w.taskC))
	// Auf feat/x ist der Request erledigt; offen sind nur Pain und Idee mit dem
	// Anker dieses Branches.
	wantPreset(t, w.presetRun(t, "mia", 200, "branch-offen", "branch", "feat/x"), "P"+i64(w.pain1), "I"+i64(w.idea1))
	w.presetRun(t, "nora", 404, "branch-offen", "branch", "feat/y")
}

func TestPresetOrdner(t *testing.T) {
	w := newQWorld(t)
	w.f.expect(t, "mia", 200, "POST", "/api/ghosts", map[string]any{"project": accProject, "path": "internal/web/x.go", "kind": "file", "description": "Seitenvorlage"})
	got := w.presetRun(t, "mia", 200, "ordner", "area", "internal/web")
	// Was dort entstand (der Request B, der Pain 2) und die Beschreibung der Datei.
	wantPreset(t, got, "R"+i64(w.reqB), "P"+i64(w.pain2), "F:internal/web/x.go")
	wantPreset(t, w.presetRun(t, "mia", 200, "ordner", "area", "internal/store"))
	w.presetRun(t, "mia", 400, "ordner", "area", "/etc")
}

func TestPresetSeit(t *testing.T) {
	w := newQWorld(t)
	all := w.presetRun(t, "mia", 200, "seit", "since", "2020-01-01")
	if len(all) < 10 {
		t.Errorf("everything changed since 2020: %v", all)
	}
	wantPreset(t, w.presetRun(t, "mia", 200, "seit", "since", "2099-01-01"))
	w.presetRun(t, "mia", 400, "seit", "since", "gestern")
}

func TestPresetFuerMich(t *testing.T) {
	w := newQWorld(t)
	got := w.presetRun(t, "mia", 200, "fuer-mich")
	// Die Aufgabe an mia, ihre offenen Pains und Ideen; ihre offenen Requests
	// (C) dazu. Nicht: der erledigte A, Pain und Idee anderer.
	wantPreset(t, got, "T"+i64(w.taskC), "P"+i64(w.pain1), "P"+i64(w.pain3), "I"+i64(w.idea1), "R"+i64(w.reqC))
	// Robin: nur seine offenen Dinge.
	wantPreset(t, w.presetRun(t, "robin", 200, "fuer-mich"), "R"+i64(w.reqB))
	// Eine eigene Suche gleichen Namens geht der mitgelieferten vor.
	w.f.expect(t, "mia", 200, "POST", "/api/search-presets", map[string]any{"project": accProject, "name": "fuer-mich", "queries": []string{"kind:idea is:open"}})
	wantPreset(t, w.presetRun(t, "mia", 200, "fuer-mich"), "I"+i64(w.idea1))
}

func TestSavedPresetWithPlaceholdersRuns(t *testing.T) {
	w := newQWorld(t)
	w.f.expect(t, "mia", 200, "POST", "/api/search-presets", map[string]any{"project": accProject, "scope": "project", "name": "bugs-am-pr", "queries": []string{"label:bug pr:{pr}"}})
	wantPreset(t, w.presetRun(t, "gus", 200, "bugs-am-pr", "pr", "12"), "R"+i64(w.reqA), "P"+i64(w.pain1))
}
