package mcpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var queryTools = []string{"idea_record", "idea_get", "idea_search", "idea_update", "label", "topic", "search_preset"}

func TestQueryToolsAreRegisteredWithSchemas(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
	tools, err := listRegistered(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		if slices.Contains(queryTools, tool.Name) {
			seen++
			if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil {
				t.Errorf("%s lacks schema or annotations", tool.Name)
			}
		}
		if tool.Name == "context_search" && !strings.Contains(tool.Description, "Qualifiers") {
			t.Errorf("context_search does not teach the qualifiers: %s", tool.Description)
		}
	}
	if seen != len(queryTools) {
		t.Fatalf("found %d of %d tools", seen, len(queryTools))
	}
}

func TestIdeaToolsOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})
	out, failed := callTool(t, session, "idea_record", map[string]any{"title": "Dunkles Thema", "body": "ein Satz"})
	if failed || !strings.Contains(out, "IDEA-1") || !strings.Contains(out, `"state":"open"`) {
		t.Fatalf("idea_record: %v %s", failed, out)
	}
	// Duplikat-Suche vor dem Anlegen.
	if got, failed := callTool(t, session, "idea_record", map[string]any{"title": "dunkles thema"}); !failed || !strings.Contains(got, "IDEA-1") {
		t.Errorf("duplicate: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "idea_record", map[string]any{"title": "dunkles thema", "allow_duplicate": true}); failed {
		t.Errorf("allowed duplicate: %s", got)
	}
	if got, failed := callTool(t, session, "idea_record", map[string]any{"title": "x\ny"}); !failed {
		t.Errorf("two-line title: %s", got)
	}
	var requests int
	st.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&requests)
	if requests != 0 {
		t.Errorf("idea_record created requests: %d", requests)
	}
	if got, _ := callTool(t, session, "idea_search", map[string]any{"query": "dunkles"}); !strings.Contains(got, "IDEA-1") {
		t.Errorf("idea_search: %s", got)
	}
	// Verwerfen braucht einen Grund, verworfene bleiben suchbar.
	if got, failed := callTool(t, session, "idea_update", map[string]any{"id": 2, "action": "dismiss"}); !failed || !strings.Contains(got, "reason") {
		t.Errorf("dismiss without reason: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "idea_update", map[string]any{"id": 2, "action": "dismiss", "reason": "doppelt"}); failed || !strings.Contains(got, `"state":"dismissed"`) {
		t.Errorf("dismiss: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "idea_search", map[string]any{"state": "dismissed"}); !strings.Contains(got, "IDEA-2") {
		t.Errorf("dismissed ideas are searchable: %s", got)
	}
	// Daraus Issue machen.
	got, failed := callTool(t, session, "idea_update", map[string]any{"id": 1, "action": "promote", "criteria": []string{"Das Thema lässt sich umschalten"}})
	if failed || !strings.Contains(got, `"created_request":"REQ-1"`) || !strings.Contains(got, `"state":"accepted"`) || !strings.Contains(got, `"requests":["REQ-1"]`) {
		t.Fatalf("promote: %v %s", failed, got)
	}
	d, err := st.RequestByID(1)
	if err != nil || d.Request.Title != "Dunkles Thema" || !strings.Contains(d.Request.Description, "IDEA-1") || len(d.Criteria) != 1 {
		t.Fatalf("promoted request: %+v %v", d.Request, err)
	}
	// Beidseitig: request_get nennt die Idee nicht in der Struktur ohne Ansicht,
	// der Store zeigt den Link.
	if got, failed := callTool(t, session, "idea_get", map[string]any{"id": 1}); failed || !strings.Contains(got, "REQ-1") {
		t.Errorf("idea_get: %s", got)
	}
	// Verknüpfen eines vorhandenen Requests.
	r2, _ := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Zweiter", Scope: scope.Axes{Project: project}}})
	if got, failed := callTool(t, session, "idea_update", map[string]any{"id": 1, "action": "link_request", "request_id": r2.Request.ID}); failed || !strings.Contains(got, "REQ-2") {
		t.Errorf("link_request: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "idea_update", map[string]any{"id": 1, "action": "nonsense"}); !failed {
		t.Errorf("unknown action: %s", got)
	}
}

func TestLabelAndTopicToolsOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	req, _ := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "bug", Title: "Kaputt", Scope: scope.Axes{Project: project}}})
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})
	if got, failed := callTool(t, session, "label", map[string]any{"action": "create", "name": "bug", "color": "d73a4a", "description": "kaputt"}); failed {
		t.Fatalf("create: %s", got)
	}
	if got, failed := callTool(t, session, "label", map[string]any{"action": "create", "name": "a,b"}); !failed {
		t.Errorf("bad name: %s", got)
	}
	if got, failed := callTool(t, session, "label", map[string]any{"action": "add", "kind": "issue", "id": req.Request.ID, "labels": []string{"bug"}}); failed || !strings.Contains(got, "bug") {
		t.Errorf("add: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "label", map[string]any{"action": "add", "kind": "issue", "id": req.Request.ID, "labels": []string{"gibt-es-nicht"}}); !failed {
		t.Errorf("unknown label: %s", got)
	}
	if got, _ := callTool(t, session, "context_search", map[string]any{"query": "label:bug kind:requests"}); !strings.Contains(got, "REQ-1") || !strings.Contains(got, "query: label:bug kind:requests") {
		t.Errorf("label: search: %s", got)
	}
	if got, _ := callTool(t, session, "context_search", map[string]any{"query": "-label:bug kind:requests"}); strings.Contains(got, "REQ-1") {
		t.Errorf("-label: search: %s", got)
	}
	if got, failed := callTool(t, session, "label", map[string]any{"action": "list"}); failed || !strings.Contains(got, "d73a4a") {
		t.Errorf("list: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "label", map[string]any{"action": "update", "name": "bug", "new_name": "defekt"}); failed {
		t.Errorf("update: %s", got)
	}
	if got, failed := callTool(t, session, "label", map[string]any{"action": "remove", "kind": "request", "id": req.Request.ID, "labels": []string{"defekt"}}); failed {
		t.Errorf("remove: %s", got)
	}
	if got, failed := callTool(t, session, "label", map[string]any{"action": "delete", "name": "defekt"}); failed {
		t.Errorf("delete: %s", got)
	}
	// Themen.
	if got, failed := callTool(t, session, "topic", map[string]any{"action": "save", "name": "Suche", "prefixes": []string{"internal/embed", "internal/store/embeddings"}}); failed {
		t.Fatalf("topic save: %s", got)
	}
	if got, _ := callTool(t, session, "topic", map[string]any{"action": "list"}); !strings.Contains(got, "Suche") || !strings.Contains(got, "internal/embed") {
		t.Errorf("topic list: %s", got)
	}
	if got, failed := callTool(t, session, "context_search", map[string]any{"query": "topic:suche"}); failed {
		t.Errorf("topic: search: %s", got)
	}
	if got, failed := callTool(t, session, "context_search", map[string]any{"query": "topic:gibtsnicht"}); !failed || !strings.Contains(got, "topic") {
		t.Errorf("unknown topic: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "topic", map[string]any{"action": "delete", "name": "Suche"}); failed {
		t.Errorf("topic delete: %s", got)
	}
}

func TestContextSearchQualifiersAndErrors(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	for _, title := range []string{"Offener Request", "Anderer Request"} {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: project}}}); err != nil {
			t.Fatal(err)
		}
	}
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})
	if got, failed := callTool(t, session, "context_search", map[string]any{"query": "flavor:sweet"}); !failed || !strings.Contains(got, "unknown qualifier") {
		t.Errorf("unknown qualifier must fail visibly: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "context_search", map[string]any{"query": "is:open kind:requests offener"}); failed || !strings.Contains(got, "Offener Request") || strings.Contains(got, "Anderer") {
		t.Errorf("qualifier with words: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "context_search", map[string]any{"query": "is:pain"}); !strings.Contains(got, "no results") {
		t.Errorf("empty answer: %s", got)
	}
	if got, failed := callTool(t, session, "context_search", map[string]any{"query": "is:open", "kind": "wiki"}); !failed {
		t.Errorf("unknown kind: %s", got)
	}
}

func TestSearchPresetToolRunsTheBuiltinsAndSavedOnes(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Im PR erledigt", Scope: scope.Axes{Project: project}}, Criteria: []string{"geht"}})
	if err != nil {
		t.Fatal(err)
	}
	st.DB().Exec(`INSERT INTO git_anchors(commit_sha,branch,merge_base,dirty,pr,captured_at) VALUES('abcdef1234567','feat/x','',0,12,'2026-11-05T10:00:00Z')`)
	st.DB().Exec(`UPDATE request_activity SET anchor_id=1 WHERE request_id=? AND kind='request.created'`, d.Request.ID)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})
	got, failed := callTool(t, session, "search_preset", map[string]any{"action": "list"})
	for _, n := range []string{"pr-ergebnis", "branch-offen", "ordner", "seit", "fuer-mich"} {
		if failed || !strings.Contains(got, n) {
			t.Errorf("list lacks %s: %v %s", n, failed, got)
		}
	}
	got, failed = callTool(t, session, "search_preset", map[string]any{"action": "run", "name": "branch-offen", "branch": "feat/x"})
	if failed || !strings.Contains(got, "REQ-1") || !strings.Contains(got, "Im PR erledigt") {
		t.Errorf("branch-offen: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "search_preset", map[string]any{"action": "run", "name": "pr-ergebnis"}); !failed || !strings.Contains(got, "pr") {
		t.Errorf("missing argument: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "search_preset", map[string]any{"action": "save", "name": "pr-bugs", "queries": []string{"type:bug pr:{pr}"}, "description": "Bugs an einem PR"}); failed {
		t.Fatalf("save: %s", got)
	}
	if got, failed := callTool(t, session, "search_preset", map[string]any{"action": "save", "name": "kaputt", "queries": []string{"flavor:{pr}"}}); !failed {
		t.Errorf("a broken search was saved: %s", got)
	}
	if got, failed := callTool(t, session, "search_preset", map[string]any{"action": "run", "name": "pr-bugs", "pr": "12"}); failed {
		t.Errorf("saved run: %s", got)
	}
	if got, _ := callTool(t, session, "search_preset", map[string]any{"action": "list"}); !strings.Contains(got, "pr-bugs [user]") {
		t.Errorf("saved search listed: %s", got)
	}
}

func TestIdeaWorkshopToolsOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})
	// Anlegen mit allem, was der Agent schon hat: ohne Rückfrage in einem Aufruf.
	raw := "  Wörtlich,\r\nso gesagt.  \n"
	out, failed := callTool(t, session, "idea_record", map[string]any{"title": "Anmeldung per OAuth planen", "raw": raw, "raw_source": "Chat",
		"summary": "Kurz: Anmeldung ohne Passwort.", "concept": map[string]any{"about": "Anmeldung", "requirements": []string{"Anmelden ohne Passwort", "Rechtetest"}}})
	if failed || !strings.Contains(out, "IDEA-1") {
		t.Fatalf("idea_record with content: %v %s", failed, out)
	}
	got, _ := callTool(t, session, "idea_get", map[string]any{"id": 1})
	for _, want := range []string{`"phase":"concept"`, "Kurz: Anmeldung ohne Passwort.", "r1 Chat", `"about":"Anmeldung"`, "Rechtetest"} {
		if !strings.Contains(got, want) {
			t.Errorf("idea_get lacks %q: %s", want, got)
		}
	}
	// Die Rohfassung kommt unverändert zurück.
	var text IdeaTextOutput
	body, _ := callTool(t, session, "idea_text", map[string]any{"id": 1, "part": "raw"})
	if err := json.Unmarshal([]byte(strings.SplitN(body, "\n", 2)[0]), &text); err != nil || text.Body != raw {
		t.Fatalf("raw text: %q %v", body, err)
	}
	if got, failed := callTool(t, session, "idea_text", map[string]any{"id": 1, "part": "summary", "body": "Kurz, besser.", "base": 1}); failed || !strings.Contains(got, `"revision":2`) {
		t.Fatalf("summary version: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "idea_text", map[string]any{"id": 1, "part": "summary", "body": "Veraltet", "base": 1}); !failed || !strings.Contains(got, "revision") {
		t.Errorf("stale base: %v %s", failed, got)
	}
	// Offene Entscheidung mit Varianten, Stimme, Bild, Entscheiden.
	got, failed = callTool(t, session, "decision_open", map[string]any{"idea": 1, "question": "Welcher Anbieter", "aspects": []string{"security"},
		"variants": []map[string]any{{"title": "Eigener Server", "pro": "keine Abhängigkeit"}, {"title": "Fremddienst", "contra": "Abhängigkeit"}}})
	if failed || !strings.Contains(got, `"ref":"DEC-1"`) || !strings.Contains(got, `"needs_a_person":true`) || !strings.Contains(got, "link: ") {
		t.Fatalf("decision_open: %v %s", failed, got)
	}
	png := base64.StdEncoding.EncodeToString(testPNGBytes(t))
	if got, failed := callTool(t, session, "idea_attach", map[string]any{"idea": 1, "owner": "variant", "decision": 1, "variant": "A", "data_base64": png, "alt": "Mock"}); failed || !strings.Contains(got, `"ref":"ATT-1"`) {
		t.Fatalf("attach to a variant: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "idea_attach", map[string]any{"idea": 1, "data_base64": base64.StdEncoding.EncodeToString([]byte(`<svg onload="x"/>`))}); !failed {
		t.Errorf("svg: %s", got)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "mock.png")
	if err := os.WriteFile(file, testPNGBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, failed := callTool(t, session, "idea_attach", map[string]any{"idea": 1, "path": file}); failed || !strings.Contains(got, `"mime":"image/png"`) {
		t.Fatalf("attach by path: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "idea_attach", map[string]any{"idea": 1, "path": dir}); !failed {
		t.Errorf("a directory as image: %s", got)
	}
	if got, failed := callTool(t, session, "idea_attach", map[string]any{"idea": 1, "path": file, "data_base64": png}); !failed {
		t.Errorf("both path and data: %s", got)
	}
	if got, failed := callTool(t, session, "decision_update", map[string]any{"id": 1, "action": "vote", "variant": "A", "reason": "ich würde A nehmen, weil keine Abhängigkeit"}); failed || !strings.Contains(got, "ich würde A nehmen") {
		t.Fatalf("vote: %v %s", failed, got)
	}
	// Sicherheit verlangt einen Menschen; ein Token (MCP) ist keiner.
	if got, failed := callTool(t, session, "decision_update", map[string]any{"id": 1, "action": "close", "outcome": "decided", "variant": "A", "reason": "Hoheit behalten"}); !failed || !strings.Contains(got, "person") {
		t.Fatalf("a token closes a decision that needs a person: %v %s", failed, got)
	}
	// Die Weboberfläche ruft den Store als Websitzung auf.
	web := store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}
	if _, err := st.Access(web).CloseDecision(store.CloseDecisionInput{ID: 1, Outcome: "decided", Variant: "A", Reason: "Hoheit behalten", Actor: store.IdeaActorFor(web, "")}); err != nil {
		t.Fatalf("close as a web session: %v", err)
	}
	got, failed = callTool(t, session, "decision_list", map[string]any{"id": 1})
	if failed || !strings.Contains(got, `"state":"decided"`) || !strings.Contains(got, `"knowledge":"KN-`) {
		t.Fatalf("decision after closing: %v %s", failed, got)
	}
	concept, _ := callTool(t, session, "idea_concept", map[string]any{"id": 1})
	if !strings.Contains(concept, "DEC-1") || !strings.Contains(concept, "ATT-1") {
		t.Errorf("the decision is not in the concept: %s", concept)
	}
	var knowledge int
	st.DB().QueryRow(`SELECT COUNT(*) FROM knowledge WHERE type='decision' AND title LIKE 'DEC-1%'`).Scan(&knowledge)
	if knowledge != 1 {
		t.Errorf("decision knowledge entries: %d", knowledge)
	}
	// Vertagt kehrt mit Auslöser zurück; offene Rückfrage an der Idee.
	callTool(t, session, "decision_open", map[string]any{"idea": 1, "question": "Wie wird exportiert"})
	if got, failed := callTool(t, session, "decision_update", map[string]any{"id": 2, "action": "close", "outcome": "deferred"}); !failed || !strings.Contains(got, "trigger") {
		t.Errorf("deferral without a trigger: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "decision_update", map[string]any{"id": 2, "action": "close", "outcome": "deferred", "trigger": "wenn wir den Export bauen"}); failed || !strings.Contains(got, `"state":"deferred"`) {
		t.Errorf("defer: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "decision_list", map[string]any{"waiting_for_a_person": true}); strings.Contains(got, "DEC-2") {
		t.Errorf("a deferred decision is in the inbox: %s", got)
	}
	// Freigabe und Issues.
	if got, failed := callTool(t, session, "idea_update", map[string]any{"id": 1, "action": "issues"}); !failed || !strings.Contains(got, "approved") {
		t.Errorf("issues before approval: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "idea_update", map[string]any{"id": 1, "action": "phase", "phase": "approved"}); !failed || !strings.Contains(got, "person") {
		t.Fatalf("a token approves: %v %s", failed, got)
	}
	if _, err := st.Access(web).SetIdeaPhase(1, "approved", store.IdeaActorFor(web, ""), headRev(st, 1)); err != nil {
		t.Fatalf("approve as a web session: %v", err)
	}
	got, failed = callTool(t, session, "idea_update", map[string]any{"id": 1, "action": "issues", "branch": "feat/anmeldung",
		"issues": []map[string]any{{"title": "Login ohne Passwort", "requirements": []int{1, 2}}}})
	if failed || !strings.Contains(got, `"created_request":"REQ-1"`) || !strings.Contains(got, "PKG-1") || !strings.Contains(got, `"phase":"building"`) {
		t.Fatalf("issues: %v %s", failed, got)
	}
	d, _ := st.RequestByID(1)
	if len(d.Criteria) != 2 || d.Criteria[1].Description != "Rechtetest" {
		t.Fatalf("criteria from the requirements: %+v", d.Criteria)
	}
	// Eine Rückfrage aus der Umsetzung landet an der Idee.
	if got, failed := callTool(t, session, "decision_open", map[string]any{"via_request": 1, "question": "Was mit alten Sitzungen", "way": "human"}); failed || !strings.Contains(got, `"subject":"IDEA-1"`) {
		t.Fatalf("via_request: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "decision_open", map[string]any{"question": "ohne Bezug"}); !failed {
		t.Errorf("a question about nothing: %s", got)
	}
}

func testPNGBytes(t testing.TB) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Ein Pain motiviert eine Lösungsidee (REQ-606): anlegen mit motivated_by_pain,
// verknüpfen über pain_update, sichtbar in pain_get, idea_get, Suche und Karten.
func TestAPainMotivatesAnIdeaOverTheWire(t *testing.T) {
	c, _ := newTestClient(t)
	project := "github.com/x/y"
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})
	out, failed := callTool(t, session, "pain_record", map[string]any{"title": "Schreiben geht nur ganz oder gar nicht"})
	if failed || !strings.Contains(out, "PAIN-1") {
		t.Fatalf("pain_record: %v %s", failed, out)
	}
	// Die Lösungsidee anlegen und am Pain verknüpfen, in einem Aufruf.
	got, failed := callTool(t, session, "idea_record", map[string]any{"title": "Entwürfe speichern", "motivated_by_pain": 1})
	if failed || !strings.Contains(got, "IDEA-1") || !strings.Contains(got, "PAIN-1 Schreiben geht nur ganz oder gar nicht") || !strings.Contains(got, "motivated by PAIN-1") {
		t.Fatalf("idea_record with a pain: %v %s", failed, got)
	}
	got, _ = callTool(t, session, "pain_get", map[string]any{"id": 1})
	if !strings.Contains(got, "IDEA-1 Entwürfe speichern") || !strings.Contains(got, "solution idea IDEA-1") {
		t.Errorf("pain_get lacks the idea: %s", got)
	}
	got, _ = callTool(t, session, "idea_get", map[string]any{"id": 1})
	if !strings.Contains(got, `"motivated_by":["PAIN-1 Schreiben`) {
		t.Errorf("idea_get lacks the pain: %s", got)
	}
	// Eine bestehende Idee später verknüpfen.
	callTool(t, session, "pain_record", map[string]any{"title": "Zweites Problem ohne Lösung"})
	callTool(t, session, "idea_record", map[string]any{"title": "Ganz anderer Wunsch"})
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 2, "link_add": []map[string]any{{"idea_id": 2}}}); failed || !strings.Contains(got, "IDEA-2 Ganz anderer Wunsch") {
		t.Fatalf("pain_update link_add idea: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 2, "link_add": []map[string]any{{"idea_id": 99}}}); !failed || !strings.Contains(got, "target not found") {
		t.Errorf("an idea that does not exist: %v %s", failed, got)
	}
	// Die Suche zeigt beide Seiten.
	found, _ := callTool(t, session, "context_search", map[string]any{"query": "is:pain,idea", "kind": "all"})
	if !strings.Contains(found, "PAIN-1") || !strings.Contains(found, "→ IDEA-1") || !strings.Contains(found, "← PAIN-1") {
		t.Errorf("search lines: %s", found)
	}
}

func headRev(st *store.Store, ideaID int64) int {
	var n int
	st.DB().QueryRow(`SELECT COALESCE(MAX(revision),0) FROM idea_texts WHERE idea_id=? AND part='concept'`, ideaID).Scan(&n)
	return n
}
