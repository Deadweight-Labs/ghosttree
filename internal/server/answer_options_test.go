package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type inboxAnswer struct {
	Kind       string `json:"kind"`
	ID         int64  `json:"id"`
	Reason     string `json:"reason"`
	MessageID  int64  `json:"message_id"`
	ProposedBy string `json:"proposed_by"`
	Options    []struct {
		Key  string `json:"key"`
		Text string `json:"text"`
	} `json:"options"`
}

func (f *accessAPIFixture) inboxOf(t *testing.T, who, agent string) []inboxAnswer {
	t.Helper()
	path := "/api/inbox?project=" + url.QueryEscape(accProject)
	if agent != "" {
		path += "&agent_external_id=" + url.QueryEscape(agent)
	}
	return decode[[]inboxAnswer](t, f.expect(t, who, 200, "GET", path, nil))
}

// questionFixture: der Agent von mia fragt den Agenten von lena (die Mitglieder des Projektraums).
func questionFixture(t *testing.T) *accessAPIFixture {
	t.Helper()
	f := accessAPI(t, true)
	room := store.RoomKeyForProject(accProject)
	for agent, principal := range map[string]string{"claude:mia": "person:3", "claude:lena": "person:2"} {
		if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: room, PrincipalID: principal, Person: strings.TrimPrefix(agent, "claude:")}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *accessAPIFixture) ask(t *testing.T, clientID, intent string, options []string, want int) string {
	t.Helper()
	return f.expect(t, "mia", want, "POST", "/api/coord/messages", map[string]any{
		"destination_kind": store.DestinationRoom, "destination_id": store.RoomKeyForProject(accProject), "sender_external_id": "claude:mia",
		"client_id": clientID, "body": "Welche Variante nehmen wir?", "intent": intent, "mentions": []string{"claude:lena"}, "options": options})
}

func TestAgentQuestionsCarryAnswerOptionsToTheInbox(t *testing.T) {
	f := questionFixture(t)
	f.ask(t, "q1", "question", []string{"Variante A", "Variante\nB"}, 200)
	inbox := f.inboxOf(t, "lena", "claude:lena")
	var q *inboxAnswer
	for i := range inbox {
		if inbox[i].Kind == "attention" {
			q = &inbox[i]
		}
	}
	if q == nil || q.Reason != "question" || len(q.Options) != 2 || q.Options[0].Key != "1" || q.Options[0].Text != "Variante A" || q.Options[1].Text != "Variante B" || q.ProposedBy != "claude" {
		t.Fatalf("the question in the inbox: %+v", inbox)
	}
	// Eine Frage ohne Vorschläge bleibt eine Frage ohne Vorschläge.
	f.ask(t, "q2", "question", nil, 200)
	count := 0
	for _, e := range f.inboxOf(t, "lena", "claude:lena") {
		if e.Kind != "attention" {
			continue
		}
		count++
		if e.MessageID != q.MessageID && len(e.Options) != 0 {
			t.Errorf("a question without proposals carries some: %+v", e)
		}
	}
	if count != 2 {
		t.Fatalf("two questions expected, got %d", count)
	}
	// Dieselbe Client-Id noch einmal (Wiederholung) ersetzt die Vorschläge nicht.
	f.ask(t, "q1", "question", []string{"Ganz", "Anders"}, 200)
	for _, e := range f.inboxOf(t, "lena", "claude:lena") {
		if e.MessageID == q.MessageID && (len(e.Options) != 2 || e.Options[0].Text != "Variante A") {
			t.Errorf("a replay changed the options: %+v", e)
		}
	}
	// Wer nicht der Empfänger ist, sieht die Frage und ihre Vorschläge nicht im Posteingang.
	for _, who := range []string{"mia", "rex", "gus"} {
		for _, e := range f.inboxOf(t, who, "claude:"+who) {
			if e.Kind == "attention" && len(e.Options) > 0 {
				t.Errorf("%s sees proposals of a question to someone else: %+v", who, e)
			}
		}
	}
}

func TestAnswerOptionsAreCheckedBeforeAnythingIsSent(t *testing.T) {
	f := questionFixture(t)
	for name, c := range map[string]struct {
		intent  string
		options []string
		code    string
	}{
		"one option":            {"question", []string{"a"}, "options_count"},
		"seven options":         {"question", []string{"1", "2", "3", "4", "5", "6", "7"}, "options_count"},
		"empty option":          {"question", []string{"a", "  "}, "option_empty"},
		"duplicate":             {"question", []string{"a", "A"}, "option_duplicate"},
		"too long":              {"question", []string{strings.Repeat("x", 121), "b"}, "option_too_long"},
		"no intent":             {"", []string{"a", "b"}, "options_not_allowed"},
		"a blocker is no ask":   {"blocker", []string{"a", "b"}, "options_not_allowed"},
		"approval is yes or no": {"approval", []string{"a", "b"}, "options_not_allowed"},
	} {
		out := f.ask(t, "bad-"+strings.ReplaceAll(name, " ", "-"), c.intent, c.options, 400)
		if !strings.Contains(out, c.code) {
			t.Errorf("%s: %s", name, out)
		}
	}
	// Nichts davon ist gesendet worden.
	for _, e := range f.inboxOf(t, "lena", "claude:lena") {
		if e.Kind == "attention" {
			t.Errorf("a refused question reached the inbox: %+v", e)
		}
	}
	f.ask(t, "good", "question", []string{"a", "b"}, 200)
}

// Antworten auf eine Frage an Menschen gibt der Mensch in der Web-Sitzung: die API
// weist jeden Token ab, auch den des Fragenden und den des Empfängers.
func TestAnsweringAQuestionToPeopleIsRefusedForEveryToken(t *testing.T) {
	f := questionFixture(t)
	f.ask(t, "q1", "question", []string{"Ja", "Nein"}, 200)
	var attention, message int64
	for _, e := range f.inboxOf(t, "lena", "claude:lena") {
		if e.Kind == "attention" {
			attention, message = e.ID, e.MessageID
		}
	}
	for _, who := range []string{"lena", "mia", "robin", "rex", "gus", "nora"} {
		for _, body := range []map[string]any{{"kind": "attention", "id": attention, "option": "1"}, {"kind": "task", "id": 1, "option": "1"}, {}} {
			code, out := f.call(t, who, "POST", "/api/inbox/answer", body)
			if code != 403 || !strings.Contains(out, "web_session_required") {
				t.Errorf("%s %v: %d %s", who, body, code, out)
			}
		}
	}
	noToken := req(t, "POST", f.srv.URL+"/api/inbox/answer", "", map[string]any{"kind": "attention", "id": attention, "option": "1"})
	noToken.Body.Close()
	if noToken.StatusCode != 401 {
		t.Errorf("no token: %d", noToken.StatusCode)
	}
	// Nichts wurde geantwortet: die Frage ist offen, es gibt keine Antwort im Raum.
	stillOpen := false
	for _, e := range f.inboxOf(t, "lena", "claude:lena") {
		stillOpen = stillOpen || (e.Kind == "attention" && e.ID == attention)
	}
	if !stillOpen {
		t.Error("the question was answered by a token")
	}
	msgs := f.expect(t, "mia", 200, "GET", "/api/coord/messages?destination_id="+store.RoomKeyForProject(accProject)+"&agent_external_id=claude:mia", nil)
	if strings.Contains(msgs, fmt.Sprintf(`"reply_to":%d`, message)) {
		t.Errorf("a reply to the question exists: %s", msgs)
	}
}

func TestHumanTasksCarryAnswerOptionsOverTheAPI(t *testing.T) {
	f := accessAPI(t, true)
	create := func(kind string, options []string, want int) string {
		body := map[string]any{"project": accProject, "kind": kind, "title": "Was soll es werden?", "request_id": f.id["r-mia"], "everyone": true, "options": options}
		if kind == "secret" {
			body["location"] = "vault"
		}
		return f.expect(t, "mia", want, "POST", "/api/tasks", body)
	}
	task := decode[struct{ ID int64 }](t, create("input", []string{"Erste Wahl", "Zweite Wahl"}, 201)).ID
	create("secret", []string{"a", "b"}, 400)
	create("input", []string{"nur eine"}, 400)
	create("input", []string{"a", "a"}, 400)
	var seen bool
	for _, e := range f.inboxOf(t, "mia", "") {
		if e.Kind == "task" && e.ID == task {
			seen = len(e.Options) == 2 && e.Options[0].Text == "Erste Wahl" && e.Options[1].Key == "2"
		}
	}
	if !seen {
		t.Fatalf("the task does not show its options in the inbox: %+v", f.inboxOf(t, "mia", ""))
	}
	// Der Gast sieht die Aufgabe nicht, also auch ihre Vorschläge nicht.
	for _, e := range f.inboxOf(t, "gus", "") {
		if e.Kind == "task" && e.ID == task {
			t.Errorf("a guest sees the task: %+v", e)
		}
	}
	code, out := f.call(t, "mia", "POST", "/api/inbox/answer", map[string]any{"kind": "task", "id": task, "option": "1"})
	if code != 403 || !strings.Contains(out, "web_session_required") {
		t.Errorf("a token answered the task: %d %s", code, out)
	}
}

func (f *accessAPIFixture) askWithOrigin(t *testing.T, who, agent, room, clientID, origin string, mentions, options []string, want int) string {
	t.Helper()
	return f.expect(t, who, want, "POST", "/api/coord/messages", map[string]any{
		"destination_kind": store.DestinationRoom, "destination_id": room, "sender_external_id": agent,
		"client_id": clientID, "body": "Frage mit Herkunft", "intent": "question", "mentions": mentions,
		"options": options, "explanation": "Das ist eine Erklärung von " + agent + ". Sie beschreibt in einfachen Worten, worum es in dieser Frage geht und warum sie wichtig ist.", "origin_event_id": origin})
}

// Eine fremde origin_event_id darf keine Optionen, keine Erklärung und keinen
// Zustellstatus an eine Nachricht eines anderen hängen.
func TestForeignOriginEventIDCannotAttachOptionsToSomeoneElsesQuestion(t *testing.T) {
	f := questionFixture(t)
	room := store.RoomKeyForProject(accProject)
	f.askWithOrigin(t, "mia", "claude:mia", room, "orig-1", "ev-1", []string{"claude:lena"}, []string{"Variante A", "Variante B"}, 200)
	var miaMsg int64
	for _, e := range f.inboxOf(t, "lena", "claude:lena") {
		if e.Kind == "attention" {
			miaMsg = e.MessageID
		}
	}
	if miaMsg == 0 {
		t.Fatal("mias question is missing")
	}
	// rex ist Mitglied desselben Projekts, lena Empfängerin, nora hat eine eigene Organisation.
	if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:rex", Provider: "claude", RoomKey: room, PrincipalID: "person:4", Person: "rex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateOrg("person:6", "Nora", "nora-org"); err != nil {
		t.Fatal(err)
	}
	if _, err := claimInOrg(f.st, "person:6", "nora-proj"); err != nil {
		t.Fatal(err)
	}
	noraRoom := store.RoomKeyForProject("nora-proj")
	for _, ag := range []string{"claude:nora", "claude:nora2"} {
		if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: ag, Provider: "claude", RoomKey: noraRoom, PrincipalID: "person:6", Person: "nora"}); err != nil {
			t.Fatal(err)
		}
	}
	evil := []string{"Böse eins", "Böse zwei"}
	f.askWithOrigin(t, "rex", "claude:rex", room, "x-rex", "ev-1", []string{"claude:lena"}, evil, 409)
	f.askWithOrigin(t, "lena", "claude:lena", room, "x-lena", "ev-1", []string{"claude:mia"}, evil, 409)
	f.askWithOrigin(t, "nora", "claude:nora", noraRoom, "x-nora", "ev-1", []string{"claude:nora2"}, evil, 409)
	for _, e := range f.inboxOf(t, "lena", "claude:lena") {
		if e.MessageID == miaMsg && (len(e.Options) != 2 || e.Options[0].Text != "Variante A" || e.Options[1].Text != "Variante B") {
			t.Errorf("options of mias question were changed: %+v", e)
		}
	}
	// Die echte Wiederholung desselben Absenders bleibt idempotent und ersetzt nichts.
	f.askWithOrigin(t, "mia", "claude:mia", room, "orig-1-retry", "ev-1", []string{"claude:lena"}, []string{"Neu 1", "Neu 2"}, 200)
	n := 0
	for _, e := range f.inboxOf(t, "lena", "claude:lena") {
		if e.Kind != "attention" {
			continue
		}
		n++
		if e.MessageID != miaMsg || len(e.Options) != 2 || e.Options[0].Text != "Variante A" {
			t.Errorf("a replay changed or duplicated the question: %+v", e)
		}
	}
	if n != 1 {
		t.Errorf("one question expected, got %d", n)
	}
}

// answerAs ruft den Handler von POST /api/inbox/answer mit dem Principal einer
// Web-Sitzung auf (wie ihn die Weboberfläche einsetzt; über die API-Authentifizierung
// kommt nie einer an).
func answerAs(t *testing.T, f *accessAPIFixture, principal store.Principal, body map[string]any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/inbox/answer", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(context.WithValue(r.Context(), personKey{}, principal))
	rec := httptest.NewRecorder()
	newAPI(f.st).inboxAnswerWebOnly(rec, r)
	return rec.Code, rec.Body.String()
}

// Eine Web-Sitzung beantwortet über die Route die Frage mit einem Vorschlag; ein Token
// (auch ohne Rumpf) bleibt bei 403.
func TestInboxAnswerRouteAnswersForWebSessionsOnly(t *testing.T) {
	f := questionFixture(t)
	// Die Frage geht an den Menschen (Konto lena), nicht an seinen Agenten.
	f.expect(t, "mia", 200, "POST", "/api/coord/messages", map[string]any{
		"destination_kind": store.DestinationRoom, "destination_id": store.RoomKeyForProject(accProject), "sender_external_id": "claude:mia",
		"client_id": "q1", "body": "Welche Variante nehmen wir?", "intent": "question", "mentions": []string{"person:2"}, "options": []string{"Ja", "Nein"}})
	items, err := f.st.CoordinationFor(store.Principal{ID: "person:2", Label: "lena", TokenKind: store.WebSessionKind}, "").Attention()
	if err != nil || len(items) != 1 {
		t.Fatalf("attention of lena: %+v %v", items, err)
	}
	attention, message := items[0].ID, items[0].MessageID
	body := map[string]any{"kind": "attention", "id": attention, "option": "2"}
	// Token-Principals: 403, die Frage bleibt offen.
	for _, p := range []store.Principal{{ID: "person:2", Label: "lena"}, {ID: "person:3", Label: "mia", TokenKind: "agent"}, {}} {
		if code, out := answerAs(t, f, p, body); code != 403 || !strings.Contains(out, "web_session_required") {
			t.Errorf("token principal %+v: %d %s", p, code, out)
		}
	}
	web := func(id, label string) store.Principal {
		return store.Principal{ID: id, Label: label, TokenKind: store.WebSessionKind}
	}
	// Wer nicht Empfänger ist, findet die Frage nicht; ein unbekannter Vorschlag ist 400.
	if code, out := answerAs(t, f, web("person:4", "rex"), body); code != 404 {
		t.Errorf("a bystander: %d %s", code, out)
	}
	if code, out := answerAs(t, f, web("person:2", "lena"), map[string]any{"kind": "attention", "id": attention, "option": "9"}); code != 400 || !strings.Contains(out, "option_not_found") {
		t.Errorf("an option that is not offered: %d %s", code, out)
	}
	code, out := answerAs(t, f, web("person:2", "lena"), body)
	if code != 200 || !strings.Contains(out, `"text":"Nein"`) || !strings.Contains(out, `"message_id"`) {
		t.Fatalf("the recipient in a web session: %d %s", code, out)
	}
	msgs := f.expect(t, "mia", 200, "GET", "/api/coord/messages?destination_id="+store.RoomKeyForProject(accProject)+"&agent_external_id=claude:mia", nil)
	if !strings.Contains(msgs, fmt.Sprintf(`"reply_to":%d`, message)) || !strings.Contains(msgs, `"body":"Nein"`) {
		t.Errorf("the reply is missing: %s", msgs)
	}
	if code, _ := answerAs(t, f, web("person:2", "lena"), body); code != 404 {
		t.Errorf("answering twice: %d", code)
	}
}
