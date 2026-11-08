package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func refsGet(t *testing.T, f *accessAPIFixture, who string, refs ...string) (int, string) {
	t.Helper()
	q := url.Values{}
	for _, r := range refs {
		q.Add("ref", r)
	}
	q.Set("project", accProject)
	return f.call(t, who, "GET", "/api/refs?"+q.Encode(), nil)
}

type refsAnswer struct {
	Refs []links.Info `json:"refs"`
}

func decodeRefs(t *testing.T, body string) []links.Info {
	t.Helper()
	var a refsAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return a.Refs
}

// post legt ein Objekt über die API an und liefert die Zahl im Feld key.
func (f *accessAPIFixture) post(t *testing.T, who, path string, body any, key string) int64 {
	t.Helper()
	code, out := f.call(t, who, "POST", path, body)
	if code/100 != 2 {
		t.Fatalf("%s %s = %d: %s", who, path, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%s: %v (%s)", path, err, out)
	}
	if inner, ok := m[strings.SplitN(key, ".", 2)[0]].(map[string]any); ok && strings.Contains(key, ".") {
		m = inner
		key = strings.SplitN(key, ".", 2)[1]
	}
	id, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s: no %s in %s", path, key, out)
	}
	return int64(id)
}

func (f *accessAPIFixture) seedRefObjects(t *testing.T) map[string]string {
	t.Helper()
	refs := map[string]string{}
	refs["req"] = fmt.Sprintf("REQ-%d", f.id["r-mia"])
	pn := f.post(t, "mia", "/api/pains", map[string]any{"project": accProject, "title": "Pain mit Titel", "body": "Es tut weh.\nZweite Zeile."}, "pain.id")
	refs["pain"] = fmt.Sprintf("PAIN-%d", pn)
	task := f.post(t, "mia", "/api/tasks", map[string]any{"project": accProject, "kind": "action", "title": "Mach das",
		"description": "Bitte tun", "location": "irgendwo", "everyone": true, "request_id": f.id["r-mia"]}, "id")
	refs["task"] = fmt.Sprintf("TASK-%d", task)
	idea := f.post(t, "mia", "/api/ideas", map[string]any{"project": accProject, "title": "Eine Idee", "body": "Wäre schön."}, "id")
	refs["idea"] = fmt.Sprintf("IDEA-%d", idea)
	feat := f.post(t, "lena", "/api/features", map[string]any{"project": accProject, "name": "Ein Feature", "summary": "Tut etwas.",
		"behavior": "Es tut es.", "limits": "Nicht mehr."}, "id")
	refs["feat"] = fmt.Sprintf("FEAT-%d", feat)
	dec := f.post(t, "mia", "/api/decisions", map[string]any{"subject": "idea", "id": idea, "question": "A oder B?", "detail": "Wegen der Kosten."}, "id")
	refs["dec"] = fmt.Sprintf("DEC-%d", dec)
	refs["know"] = fmt.Sprintf("#%d", f.id["k-mia"])
	refs["staged"] = fmt.Sprintf("#%d", f.id["k-robin-staged"])
	refs["machine"] = fmt.Sprintf("#%d", f.id["k-machine"])
	return refs
}

func (f *accessAPIFixture) seedPull(t *testing.T) {
	t.Helper()
	p, known := f.st.ProjectByRemote(accProject)
	if !known {
		t.Fatal("project unknown")
	}
	if _, err := f.st.ConnectSourceWithInstallation("person:1", p.OrgID, "github", "dw", 77, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: accProject, HeadRemote: accProject,
		Number: 12, Action: "opened", Title: "Der PR", Head: "feat/x", Base: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
}

func TestRefsReturnsHoverCardsForEveryKind(t *testing.T) {
	f := accessAPI(t, true)
	refs := f.seedRefObjects(t)
	f.seedPull(t)
	all := []string{refs["req"], refs["pain"], refs["task"], refs["idea"], refs["feat"], refs["dec"], refs["know"], "pr:12"}
	code, body := refsGet(t, f, "mia", all...)
	if code != 200 {
		t.Fatalf("code %d: %s", code, body)
	}
	list := decodeRefs(t, body)
	if len(list) != len(all) {
		t.Fatalf("got %d entries, want %d: %s", len(list), len(all), body)
	}
	want := []struct{ kind, title string }{
		{"request", "mia request"}, {"pain", "Pain mit Titel"}, {"task", "Mach das"}, {"idea", "Eine Idee"},
		{"feature", "Ein Feature"}, {"decision", "A oder B?"}, {"knowledge", "mia pitfall"}, {"pull", "Der PR"},
	}
	for i, w := range want {
		g := list[i]
		if !g.Gefunden || g.Kind != w.kind || g.Titel != w.title || g.Ref == "" {
			t.Errorf("entry %d = %+v, want %s %q", i, g, w.kind, w.title)
		}
		if !strings.Contains(g.Link, "/ui/") || strings.Contains(strings.ToLower(g.Link), "token") {
			t.Errorf("entry %d link %q", i, g.Link)
		}
		if g.Zustand == "" {
			t.Errorf("entry %d has no state: %+v", i, g)
		}
	}
	// Die Kurzfassung ist einzeilig; der Autor kommt aus dem Objekt.
	if list[1].Kurzfassung != "Es tut weh. Zweite Zeile." || list[1].Autor != "mia" {
		t.Errorf("pain: %+v", list[1])
	}
	if !strings.HasSuffix(list[0].Link, fmt.Sprintf("/ui/dw/p/issues/%d", f.id["r-mia"])) {
		t.Errorf("request link %q", list[0].Link)
	}
	if !strings.HasSuffix(list[7].Link, "/ui/dw/p/pulls/12") || list[7].Zustand != "open" || list[7].Kurzfassung != "feat/x → main" {
		t.Errorf("pull: %+v", list[7])
	}
	// Mehrere Verweise in einem Aufruf: dieselbe Antwort wie einzeln.
	_, single := refsGet(t, f, "mia", refs["pain"])
	if one := decodeRefs(t, single); len(one) != 1 || one[0] != list[1] {
		t.Errorf("single %+v vs batched %+v", one, list[1])
	}
}

func TestRefsKurzfassungIsCut(t *testing.T) {
	f := accessAPI(t, true)
	long := strings.Repeat("Wort ", 200)
	id := f.post(t, "mia", "/api/pains", map[string]any{"project": accProject, "title": "lang", "body": long}, "pain.id")
	_, body := refsGet(t, f, "mia", fmt.Sprintf("PAIN-%d", id))
	got := decodeRefs(t, body)[0]
	if n := len([]rune(got.Kurzfassung)); n == 0 || n > 240 || !strings.HasSuffix(got.Kurzfassung, "…") {
		t.Errorf("kurzfassung has %d runes: %q", n, got.Kurzfassung)
	}
}

func TestRefsRightsAndOracle(t *testing.T) {
	f := accessAPI(t, true)
	refs := f.seedRefObjects(t)
	f.seedPull(t)

	// Fremder (nicht in der Org): nichts davon ist lesbar, und jede Antwort ist
	// dieselbe wie für eine Nummer, die es nicht gibt.
	unknown := map[string]string{"req": "REQ-99999", "pain": "PAIN-99999", "task": "TASK-99999", "idea": "IDEA-99999",
		"feat": "FEAT-99999", "dec": "DEC-99999", "know": "#99999", "pr": "pr:999"}
	forbidden := map[string]string{"req": refs["req"], "pain": refs["pain"], "task": refs["task"], "idea": refs["idea"],
		"feat": refs["feat"], "dec": refs["dec"], "know": refs["know"], "pr": "pr:12"}
	for kind, ref := range forbidden {
		codeF, bodyF := refsGet(t, f, "nora", ref)
		codeU, bodyU := refsGet(t, f, "nora", unknown[kind])
		if codeF != 404 || codeU != 404 || bodyF != bodyU {
			t.Errorf("%s: stranger got %d %q for a hidden object and %d %q for an unknown one", kind, codeF, bodyF, codeU, bodyU)
		}
	}
	// Gemischt: die Einträge für verborgen und für unbekannt sind gleich.
	codeM, bodyM := refsGet(t, f, "nora", refs["pain"], refs["req"])
	if codeM != 404 {
		t.Errorf("stranger, mixed: %d %s", codeM, bodyM)
	}
	_, mixed := refsGet(t, f, "mia", refs["pain"], "PAIN-99999")
	list := decodeRefs(t, mixed)
	if !list[0].Gefunden || list[1].Gefunden || list[1].Ref != "PAIN-99999" || list[1].Titel != "" || list[1].Link != "" {
		t.Errorf("mixed answer: %+v", list)
	}
	// Der Gast liest, was ein Gast lesen darf (vertrauenswürdiges Wissen,
	// Aufträge), nicht aber Wissen, das noch nicht geprüft ist.
	for _, ref := range []string{refs["know"], refs["req"]} {
		if code, body := refsGet(t, f, "gus", ref); code != 200 {
			t.Errorf("guest cannot read %s: %d %s", ref, code, body)
		}
	}
	codeS, bodyS := refsGet(t, f, "gus", refs["staged"])
	codeX, bodyX := refsGet(t, f, "gus", "#99999")
	if codeS != 404 || codeX != 404 || bodyS != bodyX {
		t.Errorf("guest, staged knowledge: %d %q vs unknown %d %q", codeS, bodyS, codeX, bodyX)
	}
	// Wissen an einer Maschine: nur der Besitzer der Maschine liest es.
	for _, who := range []string{"robin", "lena", "gus", "nora"} {
		code, body := refsGet(t, f, who, refs["machine"])
		codeU, bodyU := refsGet(t, f, who, "#99999")
		if code != 404 || body != bodyU || codeU != 404 {
			t.Errorf("%s reads machine-bound knowledge: %d %q", who, code, body)
		}
	}
	if code, _ := refsGet(t, f, "mia", refs["machine"]); code != 200 {
		t.Errorf("machine owner cannot read it: %d", code)
	}
	// Ein PR eines Projekts, das der Fragende nicht sieht, und ein PR ohne
	// project antworten wie ein unbekannter.
	for _, who := range []string{"robin", "mia", "gus"} {
		if code, body := f.call(t, who, "GET", "/api/refs?ref=pr:12", nil); code != 404 {
			t.Errorf("%s: pr without project = %d %s", who, code, body)
		}
	}
	// Ehemaliges Mitglied: aus dem Projekt entfernt, liest es nichts mehr.
	if err := f.st.RemoveProjectRole("person:1", accProject, "person:3", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	p, _ := f.st.ProjectByRemote(accProject)
	if err := f.st.RemoveOrgMember("person:1", p.OrgID, "person:3"); err != nil {
		t.Fatal(err)
	}
	for kind, ref := range forbidden {
		codeF, bodyF := refsGet(t, f, "mia", ref)
		_, bodyU := refsGet(t, f, "mia", unknown[kind])
		if codeF != 404 || bodyF != bodyU {
			t.Errorf("%s: former member got %d %q", kind, codeF, bodyF)
		}
	}
}

func TestRefsRejectsBadCalls(t *testing.T) {
	f := accessAPI(t, true)
	f.expect(t, "mia", 400, "GET", "/api/refs", nil)
	f.expect(t, "mia", 400, "GET", "/api/refs?ref=Blödsinn", nil)
	var many []string
	for i := 0; i < 51; i++ {
		many = append(many, fmt.Sprintf("REQ-%d", i+1000))
	}
	if code, _ := refsGet(t, f, "mia", many...); code != 400 {
		t.Errorf("51 references: %d", code)
	}
	// Genau 50 gehen; die Antwort hat 50 Einträge, von denen kein einziger lesbar sein muss, wenn sie fehlen.
	if code, _ := refsGet(t, f, "mia", many[:50]...); code != 404 {
		t.Errorf("50 unknown references: %d", code)
	}
	if code, _ := f.call(t, "", "GET", "/api/refs?ref=REQ-1", nil); code != 401 {
		t.Errorf("without token: %d", code)
	}
}
