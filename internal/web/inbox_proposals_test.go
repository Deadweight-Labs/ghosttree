package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestInboxOffersTheProposedAnswersOfATaskAndAnswersWithOne(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "input", Title: "Welche Variante?", Options: []string{"Variante A", "Variante B"}})
	plain := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "action", Title: "Ohne Vorschläge"})

	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	for _, want := range []string{`class="opts"`, "Suggestions", `action="/ui/inbox/answer"`, `name="kind" value="task"`, `name="id" value="` + itoa(task.ID) + `"`,
		`name="option" value="1"`, `name="option" value="2"`, "<span>Variante A</span></button>", "<span>Variante B</span></button>", "Own answer", `name="next" value="1"`} {
		if !strings.Contains(page, want) {
			t.Errorf("inbox lacks %q", want)
		}
	}
	if n := strings.Count(page, `class="opts"`); n != 1 {
		t.Errorf("%d rows offer proposals, want only the task that has some", n)
	}
	if strings.Contains(page, `name="id" value="`+itoa(plain.ID)+`"><input type="hidden" name="option"`) {
		t.Error("a task without proposals offers buttons")
	}

	// A click with script: the answer is JSON and the task is done with the text as its note.
	resp := tpPost(t, e, e.Member, "/ui/inbox/answer", url.Values{"kind": {"task"}, "id": {itoa(task.ID)}, "option": {"1"}})
	if out := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(out, `"Variante A"`) {
		t.Fatalf("answer -> %d %s", resp.StatusCode, out)
	}
	if got, _ := e.St.TaskByID(task.ID); got.State != humantask.Done || got.DoneNote != "Variante A" || got.DoneVia != "web" {
		t.Fatalf("task after the answer: %+v", got)
	}
	// The row is gone from what waits and, when finished entries are asked for,
	// says it was answered by quick answer.
	_, page = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(page, "Welche Variante?") {
		t.Error("an answered task still waits")
	}
	_, page = fetchPage(t, e.Member, e.Base+"/ui/inbox?done=1")
	if !strings.Contains(page, "by quick answer: “Variante A”") {
		t.Errorf("the finished entry does not say how it was answered: %s", page)
	}
}

func TestInboxProposalsOfAQuestionInARoomAnswerItAsThePerson(t *testing.T) {
	e := ovEnv(t)
	room, alice := inboxDM(t, e)
	sent, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "qopt", Body: "Which migration order?",
		Intent: store.IntentQuestion, Mentions: []string{"person:2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.SaveAnswerOptions(store.AnswerOnMessage, sent, []string{"Separate", "One migration"}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	m := regexp.MustCompile(`name="kind" value="attention"><input type="hidden" name="id" value="(\d+)"><input type="hidden" name="option" value="2"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, "<span>One migration</span></button>") {
		t.Fatalf("the question lacks its proposals: %s", page)
	}
	// "Own answer" opens the field of the row; the plain "Reply" button would only repeat it.
	waiting := waitingBox(page)
	if !strings.Contains(waiting, `data-reply aria-expanded="false">Own answer`) || strings.Contains(waiting, `data-reply aria-expanded="false"><svg`) {
		t.Error("the row offers its own answer through the proposals, not twice")
	}
	resp := tpPost(t, e, e.Member, "/ui/inbox/answer", url.Values{"kind": {"attention"}, "id": {m[1]}, "option": {"2"}, "next": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
		t.Fatalf("answer without script -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, page = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(waitingBox(page), "Which migration order?") {
		t.Error("an answered question still waits")
	}
	msgs, err := alice.Messages(store.DestinationRoom, room, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var answered bool
	for _, x := range msgs {
		if x.Body == "One migration" && x.ReplyTo == sent {
			answered = true
		}
	}
	if !answered {
		t.Errorf("the answer is not a reply to the question in the room: %+v", msgs)
	}
}

func TestInboxProposalsOnlyForAWebSessionThatMayAnswer(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "input", Title: "Welche Variante?", Options: []string{"Variante A", "Variante B"}})
	// A session from a pasted token reads the inbox but has no buttons.
	personal, _, err := e.St.CreateToken("anna", store.TokenSpec{Label: "bearer"})
	if err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, login(t, e.Srv, personal), e.Base+"/ui/inbox")
	if strings.Contains(page, `data-quick`) || strings.Contains(page, `/ui/inbox/answer`) {
		t.Error("a pasted-token session gets the answer buttons")
	}
	// A guest does not see the task at all.
	_, page = fetchPage(t, e.Guest, e.Base+"/ui/inbox")
	if strings.Contains(page, "Welche Variante?") || strings.Contains(page, `data-quick`) {
		t.Error("a guest sees the proposals of a task they may not see")
	}
}

func TestTaskExplanationAndStepsShowInTheInboxRowAndOnTheHoverCard(t *testing.T) {
	withExplainer(t, nil, "de", explain.Limits{})
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	long := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "action", Title: "GitHub verbinden",
		Steps: []humantask.Step{{Text: "Einstellungen öffnen", URL: "https://github.com/settings"}, {Text: "App installieren"}, {Text: "Schlüssel eintragen"}, {Text: "Prüfen"}, {Text: "Fertig melden"}}})
	agentNote(t, e.St, explain.KindTask, long.ID, "Ohne die Verbindung sieht ghosttree keine Pull Requests. Du musst die App einmal installieren.")
	short := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "action", Title: "Nur ein Satz"})
	agentNote(t, e.St, explain.KindTask, short.ID, "Der Server braucht einen neuen Schlüssel.")
	bare := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "action", Title: "Ohne alles"})

	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	for _, want := range []string{`<details class="tnote">`, `<span class="k">Kurz erklärt</span>`, `<span class="lead">Ohne die Verbindung sieht ghosttree keine Pull Requests.</span>`,
		`<p>Ohne die Verbindung sieht ghosttree keine Pull Requests. Du musst die App einmal installieren.</p>`, `<ol class="steps">`,
		`<input type="checkbox"><span><a href="https://github.com/settings" rel="noopener noreferrer">Einstellungen öffnen</a></span>`, `<span>Fertig melden</span>`,
		`<div class="tnote one"><span class="k">Kurz erklärt</span><span class="lead">Der Server braucht einen neuen Schlüssel.</span></div>`} {
		if !strings.Contains(page, want) {
			t.Errorf("inbox lacks %q", want)
		}
	}
	if n := strings.Count(page, `class="tnote`); n != 2 {
		t.Errorf("%d notes, want one per task that has something to say (the bare task has none)", n)
	}
	_ = bare

	// The hover card of the reference: the text and the first four steps, the rest counted.
	card := regexp.MustCompile(`(?s)<template data-ref-card="TASK-` + itoa(long.ID) + `".*?</template>`).FindString(page)
	for _, want := range []string{`<div class="pc-x"><span class="k">Kurz erklärt</span> Ohne die Verbindung`, `<ol class="pc-steps">`, `Einstellungen öffnen`, `Prüfen`, `1 weiterer Schritt`} {
		if !strings.Contains(card, want) {
			t.Errorf("the card of the task lacks %q: %s", want, card)
		}
	}
	if strings.Contains(card, "Fertig melden") {
		t.Error("the card lists more steps than it should")
	}
	// A viewer who may not read the task gets neither card nor note.
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/issues/"+itoa(req.Request.ID))
	if strings.Contains(guest, "Ohne die Verbindung") || strings.Contains(guest, "Einstellungen öffnen") {
		t.Error("a guest reads what a task explains")
	}
}

// waitingBox is the box "Waiting for you" of an inbox page (the message of the
// same question also lies in the box of direct messages until it is read).
func waitingBox(page string) string {
	start := strings.Index(page, `<section class="box" id="waiting"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(page[start:], "</section>")
	if end < 0 {
		return page[start:]
	}
	return page[start : start+end]
}
