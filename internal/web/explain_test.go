package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fakeModel is the language model of these tests: it records what it was asked
// and answers with a fixed text.
type fakeModel struct {
	mu    sync.Mutex
	reply string
	users []string
	calls int
}

func (m *fakeModel) Complete(_ context.Context, _ string, msgs []llm.Message, _ int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.users = append(m.users, msgs[0].Content)
	return m.reply, nil
}

type explainHolder struct{ svc *explain.Service }

// withExplainer makes every web app of this test explain with the given model
// (nil: none) in the given language.
func withExplainer(t *testing.T, client llm.Client, language string, limits explain.Limits) *explainHolder {
	t.Helper()
	h := &explainHolder{}
	testWebOptions = []Option{WithLanguage(language), func(a *app) {
		h.svc = explain.New(a.store, client, explain.Config{Language: language, Model: "test-model", Limits: limits})
		a.explain = h.svc
	}}
	t.Cleanup(func() { testWebOptions = nil })
	return h
}

const (
	deNote = "Der Wächter zählt Runden ohne neuen Inhalt. Zwei Agenten, die nur Messwerte tauschen, würden ausgebremst. Empfehlung: Freigeben, denn die Grenze ist echt."
	enNote = "The guard counts rounds without new content. Two agents that only trade measurements would be slowed down. Recommendation: approve, the limit is real."
)

func agentNote(t *testing.T, st *store.Store, kind string, id int64, text string) {
	t.Helper()
	if err := st.SaveAgentExplanation(kind, id, text); err != nil {
		t.Fatal(err)
	}
}

func TestReviewCardShowsProjectOriginAndTheExplanationBeforeTheOriginalText(t *testing.T) {
	withExplainer(t, nil, "de", explain.Limits{})
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Wächter scharf schalten"})
	body := "ORIGINALTEXT: Zahlen zählen nicht als Inhalt. Siehe REQ-" + strconv.FormatInt(req.Request.ID, 10) + ", Code in internal/coordloop.go, https://github.com/o/r/pull/77."
	id := knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: "Pure measurement exchanges", Body: body, Confidence: "staged"})
	agentNote(t, e.St, explain.KindKnowledge, id, deNote)

	for _, path := range []string{"/ui/review", "/ui/review?project=" + url.QueryEscape(shellProject)} {
		_, page := fetchPage(t, e.Owner, e.Base+path)
		for _, want := range []string{"Kurz erklärt", deNote, "vom Agenten", `class="ex-from ov-meta"`, ">shell<",
			"REQ-" + strconv.FormatInt(req.Request.ID, 10) + " Wächter scharf schalten", `href="/ui/requests/` + strconv.FormatInt(req.Request.ID, 10) + `"`,
			"<code>internal/coordloop.go</code>", `href="https://github.com/o/r/pull/77" rel="noopener noreferrer"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		if ex, orig := strings.Index(page, `class="ex"`), strings.Index(page, "ORIGINALTEXT"); ex < 0 || orig < 0 || ex > orig {
			t.Errorf("%s: the explanation must come before the original text (%d, %d)", path, ex, orig)
		}
		if strings.Contains(page, "ex-wait") {
			t.Errorf("%s: a placeholder although no model is configured", path)
		}
	}
}

func TestExplanationLanguageFollowsTheServerAndKeepsTheGeneratedMarkDiscreet(t *testing.T) {
	model := &fakeModel{reply: enNote}
	h := withExplainer(t, model, "en", explain.Limits{})
	e := ovEnv(t)
	id := knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: "Pure measurement exchanges", Body: "ORIGINALTEXT body", Confidence: "staged"})

	// The first look does not wait for the model: no block yet, only an
	// invisible placeholder and the script that fetches it later.
	_, first := fetchPage(t, e.Owner, e.Base+"/ui/review")
	if strings.Contains(first, enNote) || !strings.Contains(first, `data-explain-pending hidden`) || !strings.Contains(first, `/static/explain.js`) {
		t.Fatalf("first look: %s", first)
	}
	if strings.Contains(first, "class=\"ex\"") {
		t.Error("an empty block is rendered while waiting")
	}
	h.svc.Wait()
	_, second := fetchPage(t, e.Owner, e.Base+"/ui/review")
	for _, want := range []string{"In short", enNote, ">generated<"} {
		if !strings.Contains(second, want) {
			t.Errorf("second look lacks %q", want)
		}
	}
	if strings.Contains(second, "data-explain-pending") {
		t.Error("placeholder still there")
	}
	// A third look and the detail page do not ask the model again.
	fetchPage(t, e.Owner, e.Base+"/ui/knowledge/"+strconv.FormatInt(id, 10))
	h.svc.Wait()
	if model.calls != 1 {
		t.Errorf("%d model calls, want 1", model.calls)
	}
	// What the model got: the entry and its project, marked as data.
	if u := model.users[0]; !strings.Contains(u, "Pure measurement exchanges") || !strings.Contains(u, shellProject) || !strings.Contains(u, "<<<DATA") {
		t.Errorf("prompt: %s", u)
	}
}

func TestEveryObjectKindShowsTheBlockOnItsPage(t *testing.T) {
	withExplainer(t, nil, "de", explain.Limits{})
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Zahlung einbauen", Desc: "Die Beschreibung des Auftrags."})
	rid := req.Request.ID
	agentNote(t, e.St, explain.KindRequest, rid, "Erklärung zum Auftrag: es geht um die Zahlung. Empfehlung: loslegen.")

	kid := knInsert(t, e.St, store.Knowledge{Type: "decision", Title: "SQLite benutzen", Body: "Wegen der Einfachheit.", Confidence: "trusted"})
	agentNote(t, e.St, explain.KindKnowledge, kid, "Erklärung zur Entscheidung: SQLite bleibt. Empfehlung: nichts ändern.")

	d, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Lesen geht nur ganz", Body: "Jeder Lesezugriff holt alles.", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	agentNote(t, e.St, explain.KindPain, d.Pain.ID, "Erklärung zum Problem: es ist langsam bei großen Texten. Empfehlung: Fenster einführen.")

	task := tpTask(t, e, shellProject, rid, humantask.CreateInput{Kind: "secret", Title: "Schlüssel anlegen", Location: "Agent Vault: vendor/prod"})
	agentNote(t, e.St, explain.KindTask, task.ID, "Erklärung zur Aufgabe: ohne den Schlüssel kann nichts bezahlt werden. Empfehlung: heute anlegen.")

	for _, c := range []struct{ path, want string }{
		{"/ui/requests/" + strconv.FormatInt(rid, 10), "Erklärung zum Auftrag"},
		{"/ui/knowledge/" + strconv.FormatInt(kid, 10), "Erklärung zur Entscheidung"},
		{"/ui/pains/" + strconv.FormatInt(d.Pain.ID, 10), "Erklärung zum Problem"},
		{"/ui/tasks", "Erklärung zur Aufgabe"},
		{"/ui/overview", "Erklärung zur Aufgabe"},
	} {
		_, page := fetchPage(t, e.Member, e.Base+c.path)
		if !strings.Contains(page, c.want) || !strings.Contains(page, "Kurz erklärt") {
			t.Errorf("%s lacks the block %q", c.path, c.want)
		}
	}
}

func TestExplanationIsShownOnlyWhereTheObjectIsShown(t *testing.T) {
	withExplainer(t, nil, "de", explain.Limits{})
	e := ovEnv(t)
	hidden := rqMake(t, e.St, rqSpec{Title: "GEHEIMER AUFTRAG", Project: shellHiddenProject})
	agentNote(t, e.St, explain.KindRequest, hidden.Request.ID, "GEHEIME ERKLÄRUNG zum versteckten Auftrag, bitte nicht zeigen.")
	hk := knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: "GEHEIMES WISSEN", Body: "geheim", Confidence: "staged", Scope: scope.Axes{Project: shellHiddenProject}})
	agentNote(t, e.St, explain.KindKnowledge, hk, "GEHEIME ERKLÄRUNG zum versteckten Wissen, bitte nicht zeigen.")

	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		for _, path := range []string{"/ui/review", "/ui/knowledge", "/ui/requests", "/ui/overview",
			"/ui/requests/" + strconv.FormatInt(hidden.Request.ID, 10), "/ui/knowledge/" + strconv.FormatInt(hk, 10),
			"/ui/requests?project=" + url.QueryEscape(shellHiddenProject), "/ui/review?project=" + url.QueryEscape(shellHiddenProject)} {
			_, page := fetchPage(t, c, e.Base+path)
			if strings.Contains(page, "GEHEIME ERKLÄRUNG") || strings.Contains(page, "GEHEIMER AUFTRAG") {
				t.Errorf("%s sees the hidden explanation on %s", name, path)
			}
		}
	}
	// The owner of the hidden project sees it where the object is.
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/requests/"+strconv.FormatInt(hidden.Request.ID, 10))
	if !strings.Contains(page, "GEHEIME ERKLÄRUNG") {
		t.Error("the owner does not see the explanation of the request")
	}
}

func TestOutdatedAgentExplanationIsHiddenAfterTheEntryChanges(t *testing.T) {
	withExplainer(t, nil, "de", explain.Limits{})
	e := ovEnv(t)
	id := knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: "Alt", Body: "Erster Text.", Confidence: "staged"})
	agentNote(t, e.St, explain.KindKnowledge, id, deNote)
	if _, page := fetchPage(t, e.Owner, e.Base+"/ui/review"); !strings.Contains(page, deNote) {
		t.Fatal("explanation missing before the edit")
	}
	if err := e.St.UpdateKnowledge(id, map[string]string{"body": "Ein ganz anderer Text."}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/review")
	if strings.Contains(page, deNote) || strings.Contains(page, "Kurz erklärt") {
		t.Error("an explanation of the old text is still shown")
	}
	if !strings.Contains(page, "Ein ganz anderer Text.") {
		t.Error("the entry itself disappeared")
	}
}

func TestWithoutAModelPagesStayQuietAndShowAgentTextsOnly(t *testing.T) {
	withExplainer(t, nil, "en", explain.Limits{})
	e := ovEnv(t)
	knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: "No explanation here", Body: "text", Confidence: "staged"})
	status, page := fetchPage(t, e.Owner, e.Base+"/ui/review")
	if status != http.StatusOK || strings.Contains(page, "ex-wait") || strings.Contains(page, `class="ex"`) || strings.Contains(page, "explain.js") {
		t.Errorf("status %d, quiet page expected", status)
	}
	if !strings.Contains(page, "No explanation here") {
		t.Error("the entry is missing")
	}
}

func TestPlainServerWithoutAnExplainerKeepsWorking(t *testing.T) {
	e := ovEnv(t)
	id := knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: "Plain", Body: "text", Confidence: "staged"})
	agentNote(t, e.St, explain.KindKnowledge, id, deNote)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/review")
	if strings.Contains(page, deNote) || !strings.Contains(page, "Plain") {
		t.Error("without the explainer option the block must not appear")
	}
}

func TestQuestionExplanationOnTheRoomCardAndTheAttentionCard(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddAccount("robin", "", true); err != nil {
		t.Fatal(err)
	}
	var svc *explain.Service
	srv := httptest.NewServer(New(st, WithLanguage("de"), func(a *app) { svc = explain.New(a.store, nil, explain.Config{Language: "de"}); a.explain = svc }))
	t.Cleanup(srv.Close)
	client := loginInteractive(t, srv, st, "robin")
	room := store.RoomKeyForProject("github.com/x/chat")
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:chat", PrincipalID: "person:1", Person: "robin", Provider: "claude", DisplayName: "claude@apps-vm", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	id, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:chat",
		ClientID: "q1", Body: "Soll der Wächter scharf geschaltet werden?", Intent: store.IntentQuestion, Mentions: []string{"person:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAgentExplanation(explain.KindQuestion, id, "Erklärung zur Frage: der Wächter beobachtet nur. Empfehlung: erst Messwerte prüfen."); err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	card := messageItem(t, page, "1")
	if !strings.Contains(card, "Erklärung zur Frage") || !strings.Contains(card, "Kurz erklärt") {
		t.Errorf("room card: %s", card)
	}
	start := strings.Index(page, `aria-labelledby="coord-attention-items"`)
	if start < 0 {
		t.Fatal("attention list missing")
	}
	if section := page[start : start+strings.Index(page[start:], "</section>")]; !strings.Contains(section, "Erklärung zur Frage") {
		t.Errorf("attention card lacks the block: %s", section)
	}
}

func TestAdminSeesWhatExplanationsCostAndOthersDoNot(t *testing.T) {
	model := &fakeModel{reply: deNote}
	h := withExplainer(t, model, "de", explain.Limits{DailyCalls: 1, DailyTokens: 100000})
	e := ovEnv(t)
	for _, title := range []string{"Eins", "Zwei"} {
		knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: title, Body: "text " + title, Confidence: "staged"})
	}
	fetchPage(t, e.Owner, e.Base+"/ui/review")
	h.svc.Wait()
	fetchPage(t, e.Owner, e.Base+"/ui/review")
	h.svc.Wait()
	if model.calls != 1 {
		t.Fatalf("the daily cap of one call was not kept: %d", model.calls)
	}
	_, admin := fetchPage(t, e.Owner, e.Base+"/ui/orgs")
	for _, want := range []string{"Kurz erklärt", "1 von 1 Aufrufen heute", "Token heute", "test-model, Deutsch", "Tagesgrenze erreicht", "1 erzeugt"} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin block lacks %q", want)
		}
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/orgs")
	if strings.Contains(member, "Aufrufen heute") || strings.Contains(member, `id="explain"`) {
		t.Error("a member sees the usage")
	}
}

func TestAdminBlockSaysWhenNoModelIsConfigured(t *testing.T) {
	withExplainer(t, nil, "en", explain.Limits{})
	e := ovEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs")
	if !strings.Contains(page, "No model set up: only explanations written by agents are shown") {
		t.Error("the administrator is not told that nothing is generated")
	}
}

func TestExplainScriptAndStylesAreServedAndPollOnlyWhileWaiting(t *testing.T) {
	e := ovEnv(t)
	for _, f := range []string{"explain.js", "explain.css"} {
		if status, _ := fetchPage(t, e.Owner, e.Base+"/static/"+f); status != http.StatusOK {
			t.Errorf("%s: %d", f, status)
		}
	}
	_, js := fetchPage(t, e.Owner, e.Base+"/static/explain.js")
	for _, want := range []string{"data-explain-pending", "replaceWith", "getElementById"} {
		if !strings.Contains(js, want) {
			t.Errorf("script lacks %q", want)
		}
	}
}
