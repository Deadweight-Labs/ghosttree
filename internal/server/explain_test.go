package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const agentNote = "Der Wächter zählt Runden ohne neuen Inhalt. Das trifft zwei Agenten, die nur Messwerte tauschen. Empfehlung: freigeben."

func storedExplanation(t *testing.T, f *accessAPIFixture, kind string, id int64) explain.Explanation {
	t.Helper()
	e, ok, err := f.st.Explanation(kind, id)
	if err != nil || !ok {
		t.Fatalf("no explanation stored for %s %d (%v)", kind, id, err)
	}
	return e
}

func TestAgentExplanationIsStoredBesideEachKindOfObjectAndCountsAsCurrent(t *testing.T) {
	f := accessAPI(t, true)

	var k store.Knowledge
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "POST", "/api/knowledge", map[string]any{
		"type": "pitfall", "title": "Wächter", "body": "Zahlen zählen nicht.", "scope": map[string]string{"project": accProject}, "explanation": agentNote,
	})), &k); err != nil {
		t.Fatal(err)
	}
	if k.Explanation != "" {
		t.Error("the answer echoes the explanation as part of the entry")
	}
	e := storedExplanation(t, f, explain.KindKnowledge, k.ID)
	src, _ := f.st.ExplainSource(explain.KindKnowledge, k.ID)
	if e.Text != agentNote || e.Source != explain.SourceAgent || e.Project != accProject || e.ContentHash != src.Hash {
		t.Errorf("knowledge: %+v", e)
	}

	rbody := f.expect(t, "mia", 201, "POST", "/api/requests", map[string]any{"type": "feature", "title": "Etwas bauen", "project": accProject, "explanation": agentNote})
	var rd struct {
		Request struct{ ID int64 } `json:"request"`
	}
	if err := json.Unmarshal([]byte(rbody), &rd); err != nil {
		t.Fatal(err)
	}
	if e := storedExplanation(t, f, explain.KindRequest, rd.Request.ID); e.Source != explain.SourceAgent || e.Text != agentNote {
		t.Errorf("request: %+v", e)
	}

	pbody := f.expect(t, "mia", 201, "POST", "/api/pains", map[string]any{"project": accProject, "title": "Es hakt", "explanation": agentNote})
	var pd struct {
		Pain struct{ ID int64 } `json:"pain"`
	}
	if err := json.Unmarshal([]byte(pbody), &pd); err != nil {
		t.Fatal(err)
	}
	if e := storedExplanation(t, f, explain.KindPain, pd.Pain.ID); e.Text != agentNote {
		t.Errorf("pain: %+v", e)
	}

	task := decode[humantask.Task](t, f.expect(t, "mia", 201, "POST", "/api/tasks", map[string]any{
		"project": accProject, "kind": "input", "title": "Entscheiden", "request_id": rd.Request.ID, "explanation": agentNote}))
	if e := storedExplanation(t, f, explain.KindTask, task.ID); e.Text != agentNote {
		t.Errorf("task: %+v", e)
	}

	room := store.RoomKeyForProject(accProject)
	for _, who := range []string{"mia", "lena"} {
		if got := registerAgent(t, f, who, room); got != 200 {
			t.Fatalf("register %s: %d", who, got)
		}
	}
	var sent struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia", ClientID: "q1",
		Body: "Soll der Wächter scharf geschaltet werden?", Intent: store.IntentQuestion, Explanation: agentNote, Mentions: []string{"claude:lena"},
	})), &sent); err != nil {
		t.Fatal(err)
	}
	if e := storedExplanation(t, f, explain.KindQuestion, sent.ID); e.Text != agentNote || e.Project != accProject {
		t.Errorf("question: %+v", e)
	}
}

func TestExplanationWithASecretIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	f := accessAPI(t, true)
	leak := "Der Zugang ist sk-" + strings.Repeat("a1", 12) + " und liegt im Klartext vor, bitte beachten."
	out := f.expect(t, "mia", 422, "POST", "/api/requests", map[string]any{"type": "feature", "title": "Mit Leck", "project": accProject, "explanation": leak})
	if !strings.Contains(out, "secret_detected") || strings.Contains(out, "sk-a1") {
		t.Errorf("answer: %s", out)
	}
	if list := f.expect(t, "mia", 200, "GET", "/api/requests?project="+accProject+"&q=Leck", nil); strings.Contains(list, "Mit Leck") {
		t.Error("the request was created although its explanation was refused")
	}
	f.expect(t, "mia", 422, "POST", "/api/knowledge", map[string]any{"type": "note", "title": "Leck", "body": "b", "scope": map[string]string{"project": accProject}, "explanation": leak})
	f.expect(t, "mia", 422, "POST", "/api/pains", map[string]any{"project": accProject, "title": "Leck", "explanation": leak})
	f.expect(t, "mia", 400, "POST", "/api/pains", map[string]any{"project": accProject, "title": "Kurz", "explanation": "zu kurz"})
}

func TestExplanationOnAPlainMessageIsRefused(t *testing.T) {
	f := accessAPI(t, true)
	room := store.RoomKeyForProject(accProject)
	if got := registerAgent(t, f, "mia", room); got != 200 {
		t.Fatalf("register: %d", got)
	}
	out := f.expect(t, "mia", 400, "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia", ClientID: "plain",
		Body: "Nur zur Information.", Explanation: agentNote})
	if !strings.Contains(out, "explanation_not_allowed") {
		t.Errorf("answer: %s", out)
	}
}

func TestCreatingWithoutAnExplanationStoresNothing(t *testing.T) {
	f := accessAPI(t, true)
	body := f.expect(t, "mia", 201, "POST", "/api/pains", map[string]any{"project": accProject, "title": "Ohne Erklärung"})
	var pd struct {
		Pain struct{ ID int64 } `json:"pain"`
	}
	if err := json.Unmarshal([]byte(body), &pd); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.st.Explanation(explain.KindPain, pd.Pain.ID); ok {
		t.Error("an explanation appeared from nowhere")
	}
}

// A replayed idempotent create returns the request somebody else made; the
// text sent with the replay must not become that request's explanation.
func TestReplayedRequestCreateKeepsTheExistingExplanationState(t *testing.T) {
	f := accessAPI(t, true)
	first := f.expect(t, "mia", 201, "POST", "/api/requests", map[string]any{"type": "feature", "title": "Einmal", "project": accProject, "idempotency_key": "k-1"})
	var rd struct {
		Request struct{ ID int64 } `json:"request"`
	}
	if err := json.Unmarshal([]byte(first), &rd); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "mia", 201, "POST", "/api/requests", map[string]any{"type": "feature", "title": "Einmal", "project": accProject, "idempotency_key": "k-1", "explanation": agentNote})
	if _, ok, _ := f.st.Explanation(explain.KindRequest, rd.Request.ID); ok {
		t.Error("a replay stored an explanation for a request it did not create")
	}
}
