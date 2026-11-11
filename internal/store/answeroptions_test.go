package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// web ist die Sitzung eines Menschen im Browser: derselbe Principal wie ein Token,
// nur mit der Art der Web-Sitzung.
func (w *painWorld) web(who string) *ProjectAccess {
	acct, _ := accountNumericID(w.pa[who].principal.ID)
	return w.st.Access(Principal{ID: principalOfID(acct), Label: who, TokenKind: WebSessionKind})
}

func (w *painWorld) webCoord(who string) CoordAccess {
	acct, _ := accountNumericID(w.pa[who].principal.ID)
	return w.st.CoordinationFor(Principal{ID: principalOfID(acct), Label: who, TokenKind: WebSessionKind}, "")
}

func TestCleanAnswerOptions(t *testing.T) {
	long := strings.Repeat("ä", MaxOptionRunes)
	for _, c := range []struct {
		name string
		in   []string
		want []string
		code string
	}{
		{"none", nil, nil, ""},
		{"empty list", []string{}, nil, ""},
		{"two", []string{"Ja", "Nein"}, []string{"Ja", "Nein"}, ""},
		{"six", []string{"1", "2", "3", "4", "5", "6"}, []string{"1", "2", "3", "4", "5", "6"}, ""},
		{"one is not a choice", []string{"Ja"}, nil, "options_count"},
		{"seven", []string{"1", "2", "3", "4", "5", "6", "7"}, nil, "options_count"},
		{"empty option", []string{"Ja", " \t"}, nil, "option_empty"},
		{"only control characters", []string{"Ja", "\u0007​"}, nil, "option_empty"},
		{"a line break becomes a space", []string{"Ja,\nund mehr", "Nein"}, []string{"Ja, und mehr", "Nein"}, ""},
		{"control and bidi characters go", []string{"Ja‮!", "N\x00ein"}, []string{"Ja!", "Nein"}, ""},
		{"blanks are collapsed", []string{"  Ja   gern ", "Nein"}, []string{"Ja gern", "Nein"}, ""},
		{"duplicates, case does not matter", []string{"Ja", "ja"}, nil, "option_duplicate"},
		{"duplicates after cleaning", []string{"Ja gern", "Ja\n gern"}, nil, "option_duplicate"},
		{"exactly the longest", []string{long, "x"}, []string{long, "x"}, ""},
		{"too long", []string{long + "ä", "x"}, nil, "option_too_long"},
	} {
		got, err := CleanAnswerOptions(c.in)
		if c.code != "" {
			if ruleCode(err) != c.code {
				t.Errorf("%s: err %v, want %s", c.name, err, c.code)
			}
			continue
		}
		if err != nil || strings.Join(got, "|") != strings.Join(c.want, "|") || (c.want == nil) != (got == nil) {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
}

func TestTaskOptionsAreStoredCleanedAndOnlyForInputAndAction(t *testing.T) {
	w := newPainWorld(t)
	req := w.request(t, "Auftrag")
	mia := humantask.Actor{AccountID: 3, Person: "mia"}
	mk := func(kind string, opts ...string) (humantask.Task, error) {
		in := humantask.CreateInput{RequestID: req, Kind: kind, Title: "Frage", Everyone: true, Options: opts}
		if kind == "secret" || kind == "file" {
			in.Location = "vault"
		}
		return w.pa["mia"].CreateHumanTask(in, mia)
	}
	task, err := mk("input", "Variante A", "Variante\nB")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := w.st.answerOptionsFor(AnswerOnTask, []int64{task.ID})
	if err != nil || len(opts[task.ID]) != 2 || opts[task.ID][0] != (AnswerOption{Key: "1", Text: "Variante A"}) || opts[task.ID][1] != (AnswerOption{Key: "2", Text: "Variante B"}) {
		t.Fatalf("stored options: %+v %v", opts, err)
	}
	if _, err := mk("action", "Erledigt", "Später"); err != nil {
		t.Fatalf("action: %v", err)
	}
	for _, kind := range []string{"secret", "file"} {
		if _, err := mk(kind, "a", "b"); ruleCode(err) != "options_not_allowed" {
			t.Errorf("%s with options: %v", kind, err)
		}
	}
	for name, in := range map[string][]string{"one": {"a"}, "seven": {"1", "2", "3", "4", "5", "6", "7"}, "duplicate": {"a", "A"},
		"token": {"ghp_" + strings.Repeat("a", 36), "nein"}} {
		if _, err := mk("input", in...); err == nil {
			t.Errorf("%s: options were accepted", name)
		}
	}
	// Ein abgelehnter Aufruf legt weder Aufgabe noch Optionen an.
	var tasks, rows int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM human_tasks`).Scan(&tasks)
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM answer_options`).Scan(&rows)
	if tasks != 2 || rows != 4 {
		t.Errorf("tasks %d, option rows %d, want 2 and 4", tasks, rows)
	}
}

// Die Optionen stehen im Posteingang, aber nur dem, der die Aufgabe sehen darf.
func TestQuickAnswerOnATaskMarksItDoneWithTheTextAsNote(t *testing.T) {
	w := newPainWorld(t)
	req := w.request(t, "Auftrag")
	mia := humantask.Actor{AccountID: 3, Person: "mia"}
	task, err := w.pa["mia"].CreateHumanTask(humantask.CreateInput{RequestID: req, Kind: "input", Title: "Welche Variante?", Options: []string{"Variante A", "Variante B"}}, mia)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := w.pa["mia"].Inbox(CoordAccess{}, InboxFilter{Project: roleProject})
	if err != nil {
		t.Fatal(err)
	}
	var e *InboxEntry
	for i := range entries {
		if entries[i].ID == task.ID && entries[i].Kind == InboxTask {
			e = &entries[i]
		}
	}
	if e == nil || len(e.Options) != 2 || e.Options[1].Text != "Variante B" || e.Options[1].Key != "2" {
		t.Fatalf("inbox entry: %+v", e)
	}
	// Wer die Aufgabe nicht sehen darf, sieht auch die Optionen nicht.
	for _, who := range []string{"gus", "nora"} {
		list, _ := w.pa[who].Inbox(CoordAccess{}, InboxFilter{Project: roleProject})
		for _, x := range list {
			if x.ID == task.ID && x.Kind == InboxTask {
				t.Errorf("%s sees the task: %+v", who, x)
			}
		}
	}
	in := QuickAnswerInput{Kind: AnswerOnTask, ID: task.ID, Option: "2"}
	// Ein Token, auch das desselben Menschen, antwortet nicht.
	if _, err := w.pa["mia"].QuickAnswer(CoordAccess{}, in); ruleCode(err) != "web_session_required" {
		t.Fatalf("a token answered: %v", err)
	}
	if got, _ := w.st.TaskByID(task.ID); got.State != humantask.Open {
		t.Fatalf("a refused answer changed the task: %+v", got)
	}
	// Fremde und Gäste in der Web-Sitzung: dasselbe wie für eine Aufgabe, die es nicht gibt.
	_, errUnknown := w.web("nora").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnTask, ID: task.ID + 99, Option: "1"})
	for _, who := range []string{"nora", "gus"} {
		_, err := w.web(who).QuickAnswer(CoordAccess{}, in)
		if err == nil || !errors.Is(err, errUnknown) && err.Error() != errUnknown.Error() && !strings.Contains(err.Error(), "not found") && !errors.Is(err, ErrAccessNotFound) {
			t.Errorf("%s: %v (unknown task: %v)", who, err, errUnknown)
		}
	}
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnTask, ID: task.ID, Option: "3"}); ruleCode(err) != "option_not_found" {
		t.Fatalf("an option that is not offered: %v", err)
	}
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnTask, ID: task.ID, Option: ""}); ruleCode(err) != "option_not_found" {
		t.Fatalf("no option: %v", err)
	}
	res, err := w.web("mia").QuickAnswer(CoordAccess{}, in)
	if err != nil || res.Option.Text != "Variante B" {
		t.Fatalf("quick answer: %+v %v", res, err)
	}
	done, _ := w.st.TaskByID(task.ID)
	if done.State != humantask.Done || done.DoneNote != "Variante B" || done.DoneVia != "web" || done.DoneBy != "mia" {
		t.Fatalf("task after the answer: %+v", done)
	}
	marks, err := w.web("mia").QuickAnswers(CoordAccess{}, AnswerOnTask, []int64{task.ID, task.ID + 1})
	if err != nil || len(marks) != 1 || marks[task.ID].Text != "Variante B" || marks[task.ID].Person != "mia" || marks[task.ID].At == "" {
		t.Fatalf("marks: %+v %v", marks, err)
	}
	// Im Posteingang steht der erledigte Eintrag mit dem Vermerk, ohne Vorschläge.
	entries, _ = w.pa["mia"].Inbox(CoordAccess{}, InboxFilter{Project: roleProject, State: "done"})
	var found bool
	for _, x := range entries {
		if x.ID == task.ID && x.Kind == InboxTask {
			found = true
			if x.QuickAnswer == nil || x.QuickAnswer.Text != "Variante B" || len(x.Options) != 0 {
				t.Errorf("done entry: %+v", x)
			}
		}
	}
	if !found {
		t.Error("the answered task is not in the done list")
	}
	// Ein zweites Mal geht nicht.
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, in); ruleCode(err) != "task_not_open" {
		t.Fatalf("answering twice: %v", err)
	}
	// Eine Aufgabe ohne Vorschläge hat keine Schnellantwort.
	plain, _ := w.pa["mia"].CreateHumanTask(humantask.CreateInput{RequestID: req, Kind: "input", Title: "Ohne", Everyone: true}, mia)
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnTask, ID: plain.ID, Option: "1"}); ruleCode(err) != "option_not_found" {
		t.Fatalf("a task without options: %v", err)
	}
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: "nonsense", ID: 1, Option: "1"}); ruleCode(err) != "invalid_answer_subject" {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestQuickAnswerOnADecisionTakesTheVariantAndSaysWhy(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Idee")
	two := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Welche Richtung?", w.actor("mia"), withAspects(idea.AspectCost),
		withVariants(idea.VariantInput{Title: "Plan A"}, idea.VariantInput{Title: "Plan B"}))
	none := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Ohne Varianten?", w.actor("mia"), withAspects(idea.AspectCost))
	entries, err := w.pa["lena"].Inbox(CoordAccess{}, InboxFilter{Project: roleProject})
	if err != nil {
		t.Fatal(err)
	}
	opts := map[int64][]AnswerOption{}
	for _, e := range entries {
		if e.Kind == InboxDecision {
			opts[e.ID] = e.Options
		}
	}
	if len(opts[two.ID]) != 2 || opts[two.ID][0] != (AnswerOption{Key: "A", Text: "Plan A"}) || opts[two.ID][1].Key != "B" || len(opts[none.ID]) != 0 {
		t.Fatalf("options of the open decisions: %+v", opts)
	}
	in := QuickAnswerInput{Kind: AnswerOnDecision, ID: two.ID, Option: "B"}
	if _, err := w.pa["lena"].QuickAnswer(CoordAccess{}, in); ruleCode(err) != "web_session_required" {
		t.Fatalf("a token decided: %v", err)
	}
	if d, _ := w.pa["lena"].DecisionView(two.ID); d.State != idea.DecisionOpen {
		t.Fatalf("a refused answer closed the decision: %+v", d)
	}
	// Ein Gast schließt keine Entscheidung, auch nicht per Klick.
	if _, err := w.web("gus").QuickAnswer(CoordAccess{}, in); err == nil {
		t.Fatal("a guest decided")
	}
	if _, err := w.web("nora").QuickAnswer(CoordAccess{}, in); err == nil {
		t.Fatal("a stranger decided")
	}
	if _, err := w.web("lena").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnDecision, ID: two.ID, Option: "C"}); ruleCode(err) != "option_not_found" {
		t.Fatalf("a variant that does not exist: %v", err)
	}
	if _, err := w.web("lena").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnDecision, ID: none.ID, Option: "A"}); ruleCode(err) != "option_not_found" {
		t.Fatalf("a decision without variants: %v", err)
	}
	res, err := w.web("lena").QuickAnswer(CoordAccess{}, in)
	if err != nil || res.Option.Text != "Plan B" {
		t.Fatalf("quick answer: %+v %v", res, err)
	}
	d, _ := w.pa["lena"].DecisionView(two.ID)
	if d.State != idea.DecisionDecided || d.ChosenVariant != "B" || d.ByAgent || d.DecidedBy != "lena" || !strings.Contains(d.Reason, "per Schnellantwort") {
		t.Fatalf("decision after the answer: %+v", d)
	}
	if d.KnowledgeID == 0 {
		t.Error("deciding must leave the decision as knowledge")
	}
	marks, _ := w.web("lena").QuickAnswers(CoordAccess{}, AnswerOnDecision, []int64{two.ID})
	if marks[two.ID].Key != "B" || marks[two.ID].Text != "Plan B" {
		t.Fatalf("marks: %+v", marks)
	}
	entries, _ = w.pa["lena"].Inbox(CoordAccess{}, InboxFilter{Project: roleProject, State: "done"})
	var seen bool
	for _, e := range entries {
		if e.Kind == InboxDecision && e.ID == two.ID {
			seen = e.QuickAnswer != nil && e.QuickAnswer.Key == "B" && len(e.Options) == 0
		}
	}
	if !seen {
		t.Error("the closed decision does not carry the quick answer in the inbox")
	}
	if _, err := w.web("lena").QuickAnswer(CoordAccess{}, in); ruleCode(err) != "decision_not_open" {
		t.Fatalf("answering twice: %v", err)
	}
}

type questionWorld struct {
	*painWorld
	room     string
	author   CoordAccess // robin, ein Token
	asked    int64
	question AttentionItem
}

// newQuestionWorld: robin stellt in einem Gruppenraum eine Frage an mia und ein
// Blocker-Hinweis ohne Frage-Absicht; die Frage trägt zwei Vorschläge.
func newQuestionWorld(t *testing.T, options ...string) *questionWorld {
	t.Helper()
	w := &questionWorld{painWorld: newPainWorld(t)}
	group, err := w.st.CreateCoordGroup(GroupInput{Label: "release", Creator: "person:1", Members: []string{"person:1", "person:3", "person:4"}})
	if err != nil {
		t.Fatal(err)
	}
	w.room = group.Key
	w.author = w.st.CoordinationFor(Principal{ID: "person:1", Label: "robin"}, "")
	w.asked, err = w.author.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: w.room, ClientID: "q1", Body: "Können wir ausliefern?",
		Intent: IntentQuestion, Mentions: []string{"person:3"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) > 0 {
		if err := w.st.SaveAnswerOptions(AnswerOnMessage, w.asked, options); err != nil {
			t.Fatal(err)
		}
	}
	items, err := w.webCoord("mia").Attention()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.MessageID == w.asked {
			w.question = it
		}
	}
	if w.question.ID == 0 {
		t.Fatalf("no attention item: %+v", items)
	}
	return w
}

func (w *questionWorld) replies(t *testing.T) []CoordMessage {
	t.Helper()
	msgs, err := w.author.Messages(DestinationRoom, w.room, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []CoordMessage
	for _, m := range msgs {
		if m.ReplyTo == w.asked {
			out = append(out, m)
		}
	}
	return out
}

func TestQuickAnswerToAQuestionInARoomIsAReplyOfThePerson(t *testing.T) {
	w := newQuestionWorld(t, "Ja, ausliefern", "Nein, noch warten", "Erst mit Lena sprechen")
	entries, err := w.pa["mia"].Inbox(w.webCoord("mia"), InboxFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var e *InboxEntry
	for i := range entries {
		if entries[i].Kind == InboxAttention && entries[i].MessageID == w.asked {
			e = &entries[i]
		}
	}
	if e == nil || len(e.Options) != 3 || e.Options[0] != (AnswerOption{Key: "1", Text: "Ja, ausliefern"}) {
		t.Fatalf("inbox entry: %+v", e)
	}
	// Ein menschlicher Absender hat keinen Agentennamen.
	if e.ProposedBy != "" {
		t.Errorf("proposed by %q", e.ProposedBy)
	}
	in := QuickAnswerInput{Kind: "attention", ID: w.question.ID, Option: "2"}
	// Ein Token antwortet nicht, auch nicht der Empfänger mit seinem eigenen Token.
	tokenCoord := w.st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "")
	if _, err := w.pa["mia"].QuickAnswer(tokenCoord, in); ruleCode(err) != "web_session_required" {
		t.Fatalf("a token answered: %v", err)
	}
	if len(w.replies(t)) != 0 {
		t.Fatal("a refused answer sent a message")
	}
	// Wer nicht der Empfänger ist (auch der Fragende selbst), findet die Frage nicht.
	if _, err := w.web("robin").QuickAnswer(w.webCoord("robin"), in); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("the author answered his own question: %v", err)
	}
	if _, err := w.web("rex").QuickAnswer(w.webCoord("rex"), in); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("a bystander answered: %v", err)
	}
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, in); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("without a room access: %v", err)
	}
	if _, err := w.web("mia").QuickAnswer(w.webCoord("mia"), QuickAnswerInput{Kind: "attention", ID: w.question.ID, Option: "7"}); ruleCode(err) != "option_not_found" {
		t.Fatalf("an option that is not offered: %v", err)
	}
	if len(w.replies(t)) != 0 {
		t.Fatal("a refused answer sent a message")
	}
	res, err := w.web("mia").QuickAnswer(w.webCoord("mia"), in)
	if err != nil || res.MessageID == 0 || res.Option.Text != "Nein, noch warten" {
		t.Fatalf("quick answer: %+v %v", res, err)
	}
	replies := w.replies(t)
	if len(replies) != 1 || replies[0].ID != res.MessageID || replies[0].Body != "Nein, noch warten" || replies[0].AuthorKind != AuthorHuman ||
		replies[0].AuthorPrincipalID != "person:3" || replies[0].ReplyTo != w.asked {
		t.Fatalf("the reply: %+v", replies)
	}
	// Die Antwort ist als Schnellantwort vermerkt, die Frage beantwortet.
	marks, _ := w.web("mia").QuickAnswers(w.webCoord("mia"), AnswerOnMessage, []int64{res.MessageID, w.asked})
	if len(marks) != 1 || marks[res.MessageID].Key != "2" {
		t.Fatalf("marks on messages: %+v", marks)
	}
	entries, _ = w.pa["mia"].Inbox(w.webCoord("mia"), InboxFilter{State: "all"})
	var done bool
	for _, x := range entries {
		if x.Kind == InboxAttention && x.MessageID == w.asked {
			done = x.State == InboxDone && x.How == AttentionResolved && x.QuickAnswer != nil && x.QuickAnswer.Text == "Nein, noch warten" && len(x.Options) == 0
		}
	}
	if !done {
		t.Fatalf("inbox after the answer: %+v", entries)
	}
	if _, err := w.web("mia").QuickAnswer(w.webCoord("mia"), in); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("answering twice: %v", err)
	}
	if len(w.replies(t)) != 1 {
		t.Fatal("a second answer sent a second message")
	}
}

// Vorschläge gehören zu einer Frage: an einem Blocker stehen sie nicht und
// führen zu nichts, und wer sie nachträglich setzt, überschreibt keine.
func TestAnswerOptionsBelongToQuestionsAndAreSetOnce(t *testing.T) {
	w := newQuestionWorld(t, "Ja", "Nein")
	if err := w.st.SaveAnswerOptions(AnswerOnMessage, w.asked, []string{"Ganz", "Anders"}); err != nil {
		t.Fatal(err)
	}
	opts, _ := w.st.answerOptionsFor(AnswerOnMessage, []int64{w.asked})
	if len(opts[w.asked]) != 2 || opts[w.asked][0].Text != "Ja" {
		t.Fatalf("the options were replaced: %+v", opts)
	}
	if err := w.st.SaveAnswerOptions("task", 1, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if rows := w.st.DB().QueryRow(`SELECT COUNT(*) FROM answer_options WHERE subject_kind='task'`); rows != nil {
		var n int
		rows.Scan(&n)
		if n != 0 {
			t.Errorf("task options were written through the message path: %d", n)
		}
	}
	blocker, err := w.author.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: w.room, ClientID: "b1", Body: "Rollout hängt", Intent: IntentBlocker, Mentions: []string{"person:3"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.SaveAnswerOptions(AnswerOnMessage, blocker, []string{"Ja", "Nein"}); err != nil {
		t.Fatal(err)
	}
	items, _ := w.webCoord("mia").Attention()
	var blockerItem AttentionItem
	for _, it := range items {
		if it.MessageID == blocker {
			blockerItem = it
		}
	}
	entries, _ := w.pa["mia"].Inbox(w.webCoord("mia"), InboxFilter{})
	for _, x := range entries {
		if x.Kind == InboxAttention && x.MessageID == blocker && len(x.Options) != 0 {
			t.Errorf("a blocker offers answers: %+v", x)
		}
	}
	if _, err := w.web("mia").QuickAnswer(w.webCoord("mia"), QuickAnswerInput{Kind: "attention", ID: blockerItem.ID, Option: "1"}); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("answering a blocker: %v", err)
	}
}

// QuickAnswers verrät Antworttext, Person und Zeit nur, wo der Betrachter den
// Gegenstand lesen darf.
func TestQuickAnswersAreOnlyShownWhereTheSubjectIsReadable(t *testing.T) {
	// Aufgabe
	w := newPainWorld(t)
	req := w.request(t, "Auftrag")
	task, err := w.pa["mia"].CreateHumanTask(humantask.CreateInput{RequestID: req, Kind: "input", Title: "Welche Variante?", Options: []string{"Variante A", "Variante B"}}, humantask.Actor{AccountID: 3, Person: "mia"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnTask, ID: task.ID, Option: "2"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.web("mia").QuickAnswers(CoordAccess{}, AnswerOnTask, []int64{task.ID}); len(m) != 1 {
		t.Fatalf("the owner sees %+v", m)
	}
	for _, who := range []string{"nora", "gus"} {
		if m, err := w.web(who).QuickAnswers(CoordAccess{}, AnswerOnTask, []int64{task.ID}); err != nil || len(m) != 0 {
			t.Errorf("%s reads the answer to a task he cannot see: %+v %v", who, m, err)
		}
	}
	// Entscheidung
	i := w.workshopIdea(t, "Idee")
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Welche Richtung?", w.actor("mia"), withAspects(idea.AspectCost),
		withVariants(idea.VariantInput{Title: "Plan A"}, idea.VariantInput{Title: "Plan B"}))
	if _, err := w.web("lena").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnDecision, ID: d.ID, Option: "B"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.web("lena").QuickAnswers(CoordAccess{}, AnswerOnDecision, []int64{d.ID}); len(m) != 1 {
		t.Fatalf("a member sees %+v", m)
	}
	if m, err := w.web("nora").QuickAnswers(CoordAccess{}, AnswerOnDecision, []int64{d.ID}); err != nil || len(m) != 0 {
		t.Errorf("a stranger reads the answer to a decision: %+v %v", m, err)
	}
	// Frage im Raum und Posteingangs-Eintrag
	q := newQuestionWorld(t, "Ja, ausliefern", "Nein, noch warten")
	res, err := q.web("mia").QuickAnswer(q.webCoord("mia"), QuickAnswerInput{Kind: "attention", ID: q.question.ID, Option: "2"})
	if err != nil {
		t.Fatal(err)
	}
	for kind, id := range map[string]int64{AnswerOnMessage: res.MessageID, AnswerOnAttention: q.question.ID} {
		if m, _ := q.web("mia").QuickAnswers(q.webCoord("mia"), kind, []int64{id}); len(m) != 1 {
			t.Errorf("the recipient does not see the %s answer: %+v", kind, m)
		}
		for _, who := range []string{"nora"} {
			if m, err := q.web(who).QuickAnswers(q.webCoord(who), kind, []int64{id}); err != nil || len(m) != 0 {
				t.Errorf("%s reads the %s answer of a room he cannot read: %+v %v", who, kind, m, err)
			}
		}
		if m, err := q.web("mia").QuickAnswers(CoordAccess{}, kind, []int64{id}); err != nil || len(m) != 0 {
			t.Errorf("without a room access the %s answer is shown: %+v %v", kind, m, err)
		}
	}
}

// Wird eine Aufgabe wieder geöffnet, gilt die Schnellantwort-Markierung der alten
// Erledigung nicht mehr; eine neue Antwort ersetzt sie.
func TestReopeningATaskDropsItsQuickAnswerMark(t *testing.T) {
	w := newPainWorld(t)
	req := w.request(t, "Auftrag")
	mia := humantask.Actor{AccountID: 3, Person: "mia"}
	task, err := w.pa["mia"].CreateHumanTask(humantask.CreateInput{RequestID: req, Kind: "input", Title: "Welche Variante?", Options: []string{"Variante A", "Variante B"}}, mia)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnTask, ID: task.ID, Option: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pa["mia"].ReopenHumanTask(task.ID, "doch noch offen", mia); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.web("mia").QuickAnswers(CoordAccess{}, AnswerOnTask, []int64{task.ID}); len(m) != 0 {
		t.Fatalf("the reopened task still carries the old mark: %+v", m)
	}
	// Die zweite Antwort steht dann so da, wie sie gegeben wurde.
	if _, err := w.web("mia").QuickAnswer(CoordAccess{}, QuickAnswerInput{Kind: AnswerOnTask, ID: task.ID, Option: "2"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.web("mia").QuickAnswers(CoordAccess{}, AnswerOnTask, []int64{task.ID}); len(m) != 1 || m[task.ID].Key != "2" || m[task.ID].Text != "Variante B" {
		t.Fatalf("the second answer: %+v", m)
	}
	// Auch ohne Wiedereröffnen ersetzt ein neuer Vermerk den alten.
	if err := w.st.commitQuickMark(AnswerOnTask, task.ID, "1", "Variante A", 3, "mia"); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.web("mia").QuickAnswers(CoordAccess{}, AnswerOnTask, []int64{task.ID}); m[task.ID].Key != "1" {
		t.Fatalf("the mark was not replaced: %+v", m)
	}
}

// Die Titel der Varianten gehen durch dieselbe Bereinigung wie die Optionen einer
// Nachricht: keine Format- und Bidi-Zeichen, eine Zeile.
func TestDecisionVariantTitlesAreCleanedLikeOptions(t *testing.T) {
	got := optionsOfDecision(idea.Decision{Variants: []idea.Variant{
		{Label: "A", Title: "Plan‮ A​"}, {Label: "B", Title: "Plan\nB\x00"}}})
	if len(got) != 2 || got[0].Text != "Plan A" || got[1].Text != "Plan B" {
		t.Fatalf("options: %+v", got)
	}
}

// Optionen und Schnellantwort-Vermerke gehen mit der Nachricht, wenn die Aufbewahrung
// sie löscht; eine gehaltene Nachricht behält sie.
func TestRetentionDropsAnswerOptionsAndMarksOfDeletedMessages(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	gone := oldMessage(t, s, room, "c1", "Antwort", 60)
	kept := oldMessage(t, s, room, "c2", "Grundlage", 60)
	if _, err := s.PromoteMessagesToThread(room, []int64{kept}, Thread{Project: "p", Title: "Thema"}, "robin"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{gone, kept} {
		if err := s.SaveAnswerOptions(AnswerOnMessage, id, []string{"Ja", "Nein"}); err != nil {
			t.Fatal(err)
		}
		if err := s.commitQuickMark(AnswerOnMessage, id, "1", "Ja", 3, "mia"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ApplyCoordRetention(RetentionCutoff(ChatRetention), RetentionCutoff(ActivityRetention)); err != nil {
		t.Fatal(err)
	}
	count := func(table string, id int64) int {
		var n int
		s.DB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE subject_kind='message' AND subject_id=?`, id).Scan(&n)
		return n
	}
	if count("answer_options", gone) != 0 || count("quick_answers", gone) != 0 {
		t.Errorf("a deleted message left options %d / marks %d", count("answer_options", gone), count("quick_answers", gone))
	}
	if count("answer_options", kept) != 2 || count("quick_answers", kept) != 1 {
		t.Errorf("a held message lost its options %d / marks %d", count("answer_options", kept), count("quick_answers", kept))
	}
}

// Wird ein doppeltes Issue beim Vereinheitlichen gelöscht, gehen seine Reaktionen mit.
func TestMergingDuplicateRequestsDropsTheirReactions(t *testing.T) {
	s := openTest(t)
	var ids []int64
	for _, project := range []string{"github.com/Example/Project", "github.com/example/project"} {
		d, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
			Type: "change", Title: "Doppelt", Description: "gleich", Scope: scope.Axes{Project: project}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.Request.ID)
		if err := s.commitReaction(ReactOnRequest, d.Request.ID, "+1", 3, "mia", true); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.CanonicalizeScopes(nil)
	if err != nil || report.RequestsMerged != 1 {
		t.Fatalf("merge: %+v %v", report, err)
	}
	var kept, dropped int
	s.DB().QueryRow(`SELECT COUNT(*) FROM reactions WHERE object_kind='request' AND object_id=?`, ids[0]).Scan(&kept)
	s.DB().QueryRow(`SELECT COUNT(*) FROM reactions WHERE object_kind='request' AND object_id=?`, ids[1]).Scan(&dropped)
	if kept != 1 || dropped != 0 {
		t.Errorf("reactions: survivor %d, deleted copy %d", kept, dropped)
	}
}
