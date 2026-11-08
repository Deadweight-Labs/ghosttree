package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func pngData(t testing.TB, size int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, size, size))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func ideaPath(format string, id int64) string { return idPath(format, id) }

var accountOfName = map[string]string{"robin": "person:1", "lena": "person:2", "mia": "person:3", "rex": "person:4", "gus": "person:5", "nora": "person:6"}

// webCall führt einen Handler als Mensch in einer interaktiven Websitzung aus
// (TokenKind web). Über die HTTP-Schnittstelle gibt es keine solche Sitzung: dort
// kommt jeder Aufruf mit einem Token; die Weboberfläche ruft den Store direkt, und
// dieser Weg bildet das nach.
func (f *accessAPIFixture) webCall(t *testing.T, who string, handler func(*api, http.ResponseWriter, *http.Request), method, path string, id int64, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, rd)
	if id != 0 {
		rq.SetPathValue("id", idPath("%d", id))
	}
	p := store.Principal{ID: accountOfName[who], Label: who, TokenKind: store.WebSessionKind}
	ctx := context.WithValue(rq.Context(), personKey{}, p)
	rq = rq.WithContext(withAccessHolder(ctx, f.st, p))
	rec := httptest.NewRecorder()
	handler(newAPI(f.st), rec, rq)
	return rec.Code, rec.Body.String()
}

func (f *accessAPIFixture) conceptRev(ideaID int64) int {
	var n int
	f.st.DB().QueryRow(`SELECT COALESCE(MAX(revision),0) FROM idea_texts WHERE idea_id=? AND part='concept'`, ideaID).Scan(&n)
	return n
}

func (f *accessAPIFixture) webExpect(t *testing.T, who string, want int, handler func(*api, http.ResponseWriter, *http.Request), method, path string, id int64, body any) string {
	t.Helper()
	code, out := f.webCall(t, who, handler, method, path, id, body)
	if code != want {
		t.Errorf("web %s %s %s = %d (%s), want %d", who, method, path, code, strings.TrimSpace(out), want)
	}
	return out
}

// Der ganze Weg einer großen Idee über die API: ablegen ohne Rückfrage, Rohfassung,
// Kurzfassung, Konzept, Diskussion, offene Entscheidung mit Varianten und Stimmen,
// Bild, Freigabe, Issues, Briefing.
func TestIdeaWorkshopAPIWalkThrough(t *testing.T) {
	f := accessAPI(t, true)
	agent := f.id["s-mia"]
	// Ablegen: eine Zeile, kein Pflichtfeld; der Vorschlag kommt einmal.
	created := decode[idea.Idea](t, f.expect(t, "mia", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Anmeldung per OAuth planen"}))
	if created.State != idea.Open || created.Phase != idea.PhaseRaw || !strings.Contains(created.Hint, "Optional") {
		t.Fatalf("created: %+v", created)
	}
	if again := decode[idea.Idea](t, f.expect(t, "mia", 200, "GET", ideaPath("/api/ideas/%d", created.ID), nil)); again.Hint != "" {
		t.Fatalf("the hint came again: %q", again.Hint)
	}
	iid := created.ID
	// Rohfassung und Kurzfassung: ein Agent hängt sie selbst an.
	raw := "  Wörtliche Sprachnachricht,\r\nmit allem Drum und Dran.  \n"
	out := decode[struct {
		Version   idea.Text `json:"version"`
		Unchanged bool      `json:"unchanged"`
	}](t, f.expect(t, "mia", 201, "POST", ideaPath("/api/ideas/%d/texts", iid), map[string]any{"part": "raw", "body": raw, "source": "Sprachnachricht", "session_id": agent}))
	if out.Version.Revision != 1 || !out.Version.ByAgent {
		t.Fatalf("raw: %+v", out)
	}
	f.expect(t, "mia", 200, "POST", ideaPath("/api/ideas/%d/texts", iid), map[string]any{"part": "raw", "body": raw})
	f.expect(t, "mia", 201, "POST", ideaPath("/api/ideas/%d/texts", iid), map[string]any{"part": "summary", "body": "Kurz: aus Ideen werden Konzepte.", "session_id": agent})
	got := decode[idea.Text](t, f.expect(t, "gus", 200, "GET", ideaPath("/api/ideas/%d/texts?part=raw", iid), nil))
	if got.Body != raw {
		t.Fatalf("raw version is not byte for byte: %q", got.Body)
	}
	f.expect(t, "gus", 403, "POST", ideaPath("/api/ideas/%d/texts", iid), map[string]any{"part": "raw", "body": "ein Gast"})
	f.expect(t, "nora", 404, "GET", ideaPath("/api/ideas/%d/texts?part=raw", iid), nil)
	f.expect(t, "mia", 400, "POST", ideaPath("/api/ideas/%d/texts", iid), map[string]any{"part": "raw", "body": "a\u0000b"})
	// Konzept.
	f.expect(t, "mia", 200, "POST", ideaPath("/api/ideas/%d/concept", iid), map[string]any{
		"about": "Anmeldung für ghosttree", "what": "OAuth und Login-Link", "requirements": []string{"Anmelden ohne Passwort", "Rechtetest"}, "session_id": agent})
	f.expect(t, "mia", 400, "POST", ideaPath("/api/ideas/%d/concept", iid), map[string]any{"requirements": []string{"zwei\nZeilen"}})
	f.expect(t, "gus", 200, "GET", ideaPath("/api/ideas/%d/concept", iid), nil)
	f.expect(t, "nora", 404, "GET", ideaPath("/api/ideas/%d/concept", iid), nil)
	// Offene Entscheidung mit Varianten, vom Agenten gestellt: Sicherheit verlangt einen Menschen.
	dec := decode[idea.Decision](t, f.expect(t, "mia", 201, "POST", "/api/decisions", map[string]any{
		"subject": "idea", "id": iid, "question": "Welcher Anbieter für die Anmeldung", "aspects": []string{"security", "external"}, "session_id": agent,
		"variants": []map[string]any{{"title": "Eigener Server", "pro": "keine Abhängigkeit"}, {"title": "Fremddienst", "contra": "Abhängigkeit"}}}))
	if dec.Way != idea.WayHuman || len(dec.Variants) != 2 || dec.State != idea.DecisionOpen {
		t.Fatalf("decision: %+v", dec)
	}
	did := dec.ID
	// Ein Token ist kein Mensch: mit oder ohne session_id kann es sie nicht schließen, auch nicht der Owner.
	f.expect(t, "mia", 403, "POST", ideaPath("/api/decisions/%d/close", did), map[string]any{"outcome": "decided", "variant": "A", "reason": "x", "session_id": agent})
	f.expect(t, "mia", 403, "POST", ideaPath("/api/decisions/%d/close", did), map[string]any{"outcome": "decided", "variant": "A", "reason": "x"})
	f.expect(t, "lena", 403, "POST", ideaPath("/api/decisions/%d/close", did), map[string]any{"outcome": "dropped", "reason": "x"})
	f.expect(t, "rex", 200, "POST", ideaPath("/api/decisions/%d/votes", did), map[string]any{"variant": "A", "reason": "ich würde A nehmen, weil keine Abhängigkeit"})
	f.expect(t, "gus", 403, "POST", ideaPath("/api/decisions/%d/votes", did), map[string]any{"variant": "A", "reason": "ich auch"})
	f.expect(t, "rex", 403, "POST", ideaPath("/api/decisions/%d/close", did), map[string]any{"outcome": "decided", "variant": "A", "reason": "x"})
	f.expect(t, "nora", 404, "GET", ideaPath("/api/decisions/%d", did), nil)
	f.expect(t, "nora", 404, "GET", "/api/decisions/9999", nil)
	// Posteingang: nur offene Entscheidungen der Art human.
	inbox := decode[[]idea.Decision](t, f.expect(t, "mia", 200, "GET", "/api/decisions?needs_human=1&project="+accProject, nil))
	if len(inbox) != 1 || inbox[0].ID != did {
		t.Fatalf("inbox: %+v", inbox)
	}
	f.expect(t, "nora", 404, "GET", "/api/decisions?needs_human=1&project="+accProject, nil)
	// Bild an Variante A, als Base64.
	att := decode[idea.AttachmentMeta](t, f.expect(t, "mia", 201, "POST", "/api/attachments", map[string]any{
		"subject": "idea", "id": iid, "owner": "variant", "decision_id": did, "variant": "A", "data": pngData(t, 4), "content_type": "image/png", "alt": "Mock A"}))
	if att.Mime != "image/png" || att.Owner != "variant" || att.URL == "" {
		t.Fatalf("attachment: %+v", att)
	}
	// Auslieferung: Bild, nosniff, fester Dateiname, nur für Berechtigte.
	resp := req(t, "GET", f.srv.URL+att.URL, f.tok["gus"], nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), `inline; filename="attachment-1.png"`) || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") ||
		!bytes.Equal(body, pngData(t, 4)) {
		t.Fatalf("delivery: %d %v", resp.StatusCode, resp.Header)
	}
	missing := f.expect(t, "nora", 404, "GET", att.URL, nil)
	other := f.expect(t, "nora", 404, "GET", "/api/attachments/777", nil)
	if strings.ReplaceAll(missing, "ATT-1", "ATT-N") != strings.ReplaceAll(other, "ATT-777", "ATT-N") {
		t.Fatalf("a stranger and a missing id answer differently: %s / %s", missing, other)
	}
	f.expect(t, "gus", 403, "POST", "/api/attachments", map[string]any{"subject": "idea", "id": iid, "owner": "idea", "data": pngData(t, 3)})
	f.expect(t, "nora", 404, "POST", "/api/attachments", map[string]any{"subject": "idea", "id": iid, "owner": "idea", "data": pngData(t, 3)})
	f.expect(t, "mia", 400, "POST", "/api/attachments", map[string]any{"subject": "idea", "id": iid, "owner": "idea", "data": []byte(`<svg onload="alert(1)"/>`), "content_type": "image/svg+xml"})
	f.expect(t, "mia", 413, "POST", "/api/attachments", map[string]any{"subject": "idea", "id": iid, "owner": "idea", "data": bytes.Repeat([]byte{1}, 7<<20)})
	// Entscheiden: der Owner, mit Begründung; die Variante samt Bild wandert ins Wissen.
	f.webExpect(t, "rex", 403, (*api).closeDecision, "POST", "/x", did, map[string]any{"outcome": "decided", "variant": "A", "reason": "x"})
	closed := decode[idea.Decision](t, f.webExpect(t, "mia", 200, (*api).closeDecision, "POST", "/x", did, map[string]any{"outcome": "decided", "variant": "A", "reason": "Sicherheitsfrage, wir behalten die Hoheit"}))
	if closed.State != idea.DecisionDecided || closed.KnowledgeID == 0 || closed.ChosenVariant != "A" || closed.ByAgent {
		t.Fatalf("closed: %+v", closed)
	}
	k := decode[store.Knowledge](t, f.expect(t, "mia", 200, "GET", ideaPath("/api/knowledge/%d", closed.KnowledgeID), nil))
	if k.Type != "decision" || !strings.Contains(k.Body, "Eigener Server") || !strings.Contains(k.Body, "ATT-1") {
		t.Fatalf("knowledge: %+v", k)
	}
	// Freigabe: nur Owner oder Maintainer.
	f.expect(t, "rex", 403, "POST", ideaPath("/api/ideas/%d/phase", iid), map[string]any{"phase": "approved"})
	f.expect(t, "gus", 403, "POST", ideaPath("/api/ideas/%d/phase", iid), map[string]any{"phase": "approved"})
	f.expect(t, "nora", 404, "POST", ideaPath("/api/ideas/%d/phase", iid), map[string]any{"phase": "approved"})
	f.expect(t, "mia", 400, "POST", ideaPath("/api/ideas/%d/issues", iid), map[string]any{})
	// Auch der Owner und der Maintainer kommen mit einem Token nicht durch: Freigeben ist ein Menschen-Schritt.
	f.expect(t, "mia", 403, "POST", ideaPath("/api/ideas/%d/phase", iid), map[string]any{"phase": "approved", "session_id": agent})
	f.expect(t, "lena", 403, "POST", ideaPath("/api/ideas/%d/phase", iid), map[string]any{"phase": "approved"})
	f.webExpect(t, "rex", 403, (*api).setIdeaPhase, "POST", "/x", iid, map[string]any{"phase": "approved", "concept_revision": f.conceptRev(iid)})
	// Ohne die gesehene Fassung gibt es keine Freigabe, mit einer veralteten auch nicht.
	f.webExpect(t, "lena", 400, (*api).setIdeaPhase, "POST", "/x", iid, map[string]any{"phase": "approved"})
	f.webExpect(t, "lena", 409, (*api).setIdeaPhase, "POST", "/x", iid, map[string]any{"phase": "approved", "concept_revision": f.conceptRev(iid) - 1})
	appr := decode[idea.Idea](t, f.webExpect(t, "lena", 200, (*api).setIdeaPhase, "POST", "/x", iid, map[string]any{"phase": "approved", "concept_revision": f.conceptRev(iid)}))
	if appr.Phase != idea.PhaseApproved {
		t.Fatalf("%+v", appr)
	}
	// Issues aus der Idee, mit Paket.
	f.expect(t, "rex", 403, "POST", ideaPath("/api/ideas/%d/issues", iid), map[string]any{})
	res := decode[store.IdeaIssuesResult](t, f.expect(t, "mia", 201, "POST", ideaPath("/api/ideas/%d/issues", iid), map[string]any{"branch": "feat/anmeldung", "package_name": "Anmeldung"}))
	if len(res.Requests) != 1 || res.Package == nil || res.Idea.Phase != idea.PhaseBuilding {
		t.Fatalf("issues: %+v", res)
	}
	// Briefing des Issues: Kurzfassung, Entscheidung, Verweis auf die Idee.
	brief := decode[map[string]string](t, f.expect(t, "gus", 200, "GET", ideaPath("/api/requests/%d/briefing", res.Requests[0]), nil))["briefing"]
	for _, want := range []string{"Kurz: aus Ideen werden Konzepte.", "Decisions already made", "Eigener Server", "do not ask again"} {
		if !strings.Contains(brief, want) {
			t.Errorf("briefing lacks %q:\n%s", want, brief)
		}
	}
	f.expect(t, "nora", 404, "GET", ideaPath("/api/requests/%d/briefing", res.Requests[0]), nil)
	// Rückfrage über das Issue landet an der Idee.
	q := decode[idea.Decision](t, f.expect(t, "mia", 201, "POST", "/api/decisions", map[string]any{"via_request": res.Requests[0], "question": "Was passiert mit alten Sitzungen", "session_id": agent}))
	if q.Subject != idea.SubjectIdea || q.SubjectID != iid {
		t.Fatalf("the question did not land on the idea: %+v", q)
	}
	// Arbeitsstart gibt das Briefing gleich mit.
	start := f.expect(t, "mia", 201, "POST", ideaPath("/api/requests/%d/work", res.Requests[0]), map[string]any{"session_id": agent, "role": "primary"})
	var sw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(start), &sw); err != nil || !strings.Contains(string(sw["idea_briefing"]), "Idea context") {
		t.Fatalf("start work answer lacks the idea briefing: %s", start)
	}
	// Die Idee nennt Verlauf und Anhänge in der Gesamtansicht.
	ws := decode[store.IdeaWorkshop](t, f.expect(t, "gus", 200, "GET", ideaPath("/api/ideas/%d/workshop", iid), nil))
	if ws.Summary == nil || ws.Concept == nil || len(ws.Raw) != 1 || len(ws.Decisions) != 2 || len(ws.Attachments) != 1 || ws.Idea.Phase != idea.PhaseBuilding {
		t.Fatalf("workshop: %+v", ws)
	}
	f.expect(t, "nora", 404, "GET", ideaPath("/api/ideas/%d/workshop", iid), nil)
}

func TestDecisionPolicyAndWayAreForMaintainersAndPeople(t *testing.T) {
	f := accessAPI(t, true)
	agent := f.id["s-mia"]
	i := decode[idea.Idea](t, f.expect(t, "mia", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Policy"}))
	f.expect(t, "mia", 403, "POST", "/api/decision-policy", map[string]any{"project": accProject, "mode": "human"})
	f.expect(t, "lena", 403, "POST", "/api/decision-policy", map[string]any{"project": accProject, "mode": "human"}) // ein Token ist kein Mensch
	f.webExpect(t, "mia", 403, (*api).setDecisionPolicy, "POST", "/x", 0, map[string]any{"project": accProject, "mode": "human"})
	f.webExpect(t, "lena", 200, (*api).setDecisionPolicy, "POST", "/x", 0, map[string]any{"project": accProject, "mode": "human"})
	f.expect(t, "gus", 200, "GET", "/api/decision-policy?project="+accProject, nil)
	f.expect(t, "nora", 404, "GET", "/api/decision-policy?project="+accProject, nil)
	d := decode[idea.Decision](t, f.expect(t, "mia", 201, "POST", "/api/decisions", map[string]any{"subject": "idea", "id": i.ID, "question": "Klein", "session_id": agent}))
	if d.Way != idea.WayHuman {
		t.Fatalf("%+v", d)
	}
	f.expect(t, "lena", 403, "POST", "/api/decision-policy", map[string]any{"project": accProject, "mode": "rule"})
	f.webExpect(t, "lena", 200, (*api).setDecisionPolicy, "POST", "/x", 0, map[string]any{"project": accProject, "mode": "rule"})
	e := decode[idea.Decision](t, f.expect(t, "mia", 201, "POST", "/api/decisions", map[string]any{"subject": "idea", "id": i.ID, "question": "Wieder die Regel", "session_id": agent}))
	if e.Way != idea.WayAgent {
		t.Fatalf("%+v", e)
	}
	// Die Art stellt nur ein Mensch mit Bearbeitungsrecht um.
	f.expect(t, "mia", 403, "POST", ideaPath("/api/decisions/%d/way", e.ID), map[string]any{"way": "human", "session_id": agent})
	f.expect(t, "mia", 403, "POST", ideaPath("/api/decisions/%d/way", e.ID), map[string]any{"way": "human"})
	f.webExpect(t, "rex", 403, (*api).setDecisionWay, "POST", "/x", e.ID, map[string]any{"way": "human"})
	f.webExpect(t, "mia", 200, (*api).setDecisionWay, "POST", "/x", e.ID, map[string]any{"way": "human"})
}

// Ein Token ohne session_id ist kein Mensch: es kann weder als Mensch entscheiden
// noch freigeben noch erlassen. Eine Websitzung kann es.
func TestATokenWithoutASessionIdIsNotAPerson(t *testing.T) {
	f := accessAPI(t, true)
	agent := f.id["s-mia"]
	i := decode[idea.Idea](t, f.expect(t, "mia", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Wer ist ein Mensch"}))
	f.expect(t, "mia", 200, "POST", ideaPath("/api/ideas/%d/concept", i.ID), map[string]any{"about": "x", "requirements": []string{"a"}})
	// Eine Frage, die einen Menschen verlangt, gestellt ohne session_id: das Token bleibt ein Agent.
	d := decode[idea.Decision](t, f.expect(t, "mia", 201, "POST", "/api/decisions", map[string]any{"subject": "idea", "id": i.ID, "question": "Rechte?", "aspects": []string{"security"}}))
	if d.Way != idea.WayHuman || d.ByAgent {
		t.Fatalf("%+v", d)
	}
	for _, withSession := range []bool{false, true} {
		body := map[string]any{"outcome": "decided", "answer": "ja", "reason": "weil"}
		phase := map[string]any{"phase": "approved"}
		waive := map[string]any{"requirement": 1, "reason": "x"}
		if withSession {
			body["session_id"], phase["session_id"] = agent, agent
		}
		f.expect(t, "mia", 403, "POST", ideaPath("/api/decisions/%d/close", d.ID), body)
		f.expect(t, "mia", 403, "POST", ideaPath("/api/ideas/%d/phase", i.ID), phase)
		f.expect(t, "mia", 403, "POST", ideaPath("/api/ideas/%d/requirements/waive", i.ID), waive)
	}
	var open idea.Decision
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", ideaPath("/api/decisions/%d", d.ID), nil)), &open); err != nil || open.State != idea.DecisionOpen {
		t.Fatalf("the token closed it: %+v %v", open, err)
	}
	// Dieselben Schritte in einer Websitzung.
	f.webExpect(t, "mia", 200, (*api).closeDecision, "POST", "/x", d.ID, map[string]any{"outcome": "decided", "answer": "ja", "reason": "weil"})
	got := decode[idea.Idea](t, f.webExpect(t, "mia", 200, (*api).setIdeaPhase, "POST", "/x", i.ID, map[string]any{"phase": "approved", "concept_revision": f.conceptRev(i.ID)}))
	if got.Phase != idea.PhaseApproved {
		t.Fatalf("%+v", got)
	}
	f.webExpect(t, "mia", 200, (*api).waiveRequirement, "POST", "/x", i.ID, map[string]any{"requirement": 1, "reason": "später"})
	// Die Entscheidung trägt, wer sie getroffen hat: eine Person, nicht der Agent.
	closed := decode[idea.Decision](t, f.expect(t, "mia", 200, "GET", ideaPath("/api/decisions/%d", d.ID), nil))
	if closed.ByAgent || closed.State != idea.DecisionDecided {
		t.Fatalf("%+v", closed)
	}
}
