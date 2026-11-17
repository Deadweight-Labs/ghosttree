package web

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// inboxBody is the part of an inbox page that holds the rows, for failure messages.
func inboxBody(page string) string {
	if i := strings.Index(page, `<section class="box"`); i >= 0 {
		return page[i:min(len(page), i+3500)]
	}
	return page[max(0, len(page)-2500):]
}

// rowOf is the first row of an inbox page, for failure messages.
func rowOf(page string) string {
	i := strings.Index(page, `class="row nrow`)
	if i < 0 {
		return "(no row)"
	}
	return page[i:min(len(page), i+1800)]
}

// askAnna: alice (owner) asks anna in the project room as an "ask" with the
// @name in the words, the way the composer does; the id of the message comes back.
func askAnna(t *testing.T, e shellEnv, body string) (room string, id int64) {
	t.Helper()
	inboxPeople(t, e)
	room = store.RoomKeyForProject(shellProject)
	resp := tpPost(t, e, e.Owner, "/ui/coord/send", url.Values{"room": {room}, "intent": {"question"}, "body": {body}})
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("ask -> %d, want it sent", resp.StatusCode)
	}
	anna := e.St.CoordinationFor(store.Principal{ID: "person:2", Label: "anna", TokenKind: store.WebSessionKind}, "")
	items, err := anna.Attention()
	if err != nil || len(items) != 1 {
		t.Fatalf("anna finds the question waiting: %+v %v", items, err)
	}
	return room, items[0].MessageID
}

// REQ-689 AC-1 (R01): a question from one person to another names the person who
// asked in the inbox of the one asked, and the confirmation of the answer names
// them too; "Someone" is for nobody the viewer may know.
func TestInboxQuestionFromAPersonNamesTheAsker(t *testing.T) {
	e := ovEnv(t)
	room, mid := askAnna(t, e, "@anna Soll das Timeout 30 oder 60 Sekunden sein?")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(page, "Someone") {
		t.Errorf("the inbox of the person asked names \"Someone\":\n%s", inboxBody(page))
	}
	for _, want := range []string{`class="top2">alice`, "Reply to alice", `name="to" value="alice"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the inbox row of the question lacks %q:\n%s", want, inboxBody(page))
		}
	}
	resp := tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {room}, "reply_to": {itoa(mid)}, "to": {"alice"}, "body": {"60 Sekunden."}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/ui/inbox?sent=alice" {
		t.Fatalf("reply -> %d %q", resp.StatusCode, loc)
	}
	_, done := fetchPage(t, e.Member, e.Base+"/ui/inbox?sent=alice")
	if !strings.Contains(done, "Reply sent to alice.") || strings.Contains(done, "Someone") {
		t.Errorf("the confirmation does not name the person asked:\n%.2000s", done)
	}
}

// threadOfQuestion starts a thread on the question, as the asker does from the message.
func threadOfQuestion(t *testing.T, e shellEnv, mid int64) int64 {
	t.Helper()
	alice := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	thread, err := alice.PromoteRoomMessageToTaskThread(mid, "Timeout", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return thread
}

// REQ-689 AC-2 (R01): the answer to a question that has a thread is a post in that
// thread, so the asker finds it where they follow the question: the thread does not
// say "No replies yet" under a question that reads "Answered", and Answered leads to
// the reply.
func TestAnswerToAQuestionWithAThreadLandsInTheThread(t *testing.T) {
	e := ovEnv(t)
	room, mid := askAnna(t, e, "@anna Soll das Timeout 30 oder 60 Sekunden sein?")
	thread := threadOfQuestion(t, e, mid)
	resp := tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {room}, "reply_to": {itoa(mid)}, "to": {"alice"}, "body": {"Nimm 60 Sekunden."}})
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("reply -> %d", resp.StatusCode)
	}
	if _, inbox := fetchPage(t, e.Member, e.Base+"/ui/inbox"); strings.Contains(inbox, "Soll das Timeout") {
		t.Errorf("the answered question comes back under \"Said to you\":\n%s", inboxBody(inbox))
	}
	threadURL := e.Base + "/ui/coord?room=" + url.QueryEscape(room) + "&thread=" + itoa(thread)
	page := fetchBody(t, e.Owner, threadURL)
	if !strings.Contains(page, "Nimm 60 Sekunden.") {
		t.Errorf("the answer is not in the thread:\n%s", page[max(0, len(page)-3000):])
	}
	if strings.Contains(page, "No replies yet.") {
		t.Errorf("the thread of an answered question says \"No replies yet.\"")
	}
	if !strings.Contains(page, "Answered") {
		t.Errorf("the question does not read Answered")
	}
	roomPage := fetchBody(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(roomPage, `class="ask-state"><a href="/ui/coord?room=`) {
		t.Errorf("Answered does not lead to the answer:\n%s", roomPage[max(0, len(roomPage)-4000):])
	}
}

// REQ-689 AC-2 (R01): an answer that stood in the room before the thread was
// started is found from the thread: no "No replies yet" next to Answered, and
// Answered leads to it.
func TestThreadStartedAfterTheAnswerLeadsToIt(t *testing.T) {
	e := ovEnv(t)
	room, mid := askAnna(t, e, "@anna Soll das Timeout 30 oder 60 Sekunden sein?")
	resp := tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {room}, "reply_to": {itoa(mid)}, "to": {"alice"}, "body": {"Nimm 60 Sekunden."}})
	resp.Body.Close()
	thread := threadOfQuestion(t, e, mid)
	page := fetchBody(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(room)+"&thread="+itoa(thread))
	if strings.Contains(page, "No replies yet.") {
		t.Errorf("the thread of an answered question says \"No replies yet.\"")
	}
	if !strings.Contains(page, `class="ask-state"><a href="/ui/coord?around=`) {
		t.Errorf("Answered does not lead to the answer in the thread's anchor")
	}
}

// REQ-689 AC-3 (R02): a name written out after the handle is part of the resolved name, said
// once: "Mara Keller (Tech-Lead) Keller" and "Pia Novak Novak" were two names in one.
func TestMentionOfAMultiPartNameIsSaidOnce(t *testing.T) {
	l := langEN
	names := map[string]string{"Mara": "Mara Keller (Tech-Lead)", "Pia": "Pia Novak", "Lena": "Lena Berger (Tech-Lead)", "mara": "Mara Keller (Tech-Lead)"}
	for _, c := range []struct{ in, want string }{
		{"@Mara Keller REQ-2 (Webhook-Signatur): Brauchen wir das?", "Mara Keller (Tech-Lead) REQ-2 (Webhook-Signatur): Brauchen wir das?"},
		{"@Pia Novak, kannst du schauen?", "Pia Novak, kannst du schauen?"},
		{"@Lena Berger (Tech-Lead) schau mal", "Lena Berger (Tech-Lead) schau mal"},
		{"@lena berger (tech-lead) schau mal", "@lena berger (tech-lead) schau mal"},
		{"@Mara keller bitte", "Mara Keller (Tech-Lead) bitte"},
		{"@Mara Kellerman bitte", "Mara Keller (Tech-Lead) Kellerman bitte"},
		{"@Mara und @Pia Novak", "Mara Keller (Tech-Lead) und Pia Novak"},
		{"@Mara", "Mara Keller (Tech-Lead)"},
	} {
		if got := stripMarkdown(l, c.in, names); got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
}

// REQ-689 AC-3 (R02): "z. B." is no end of a sentence: a long question stays whole from its first
// word and does not begin in the middle of the sentence.
func TestQuestionTitleIsNotCutAtAnAbbreviation(t *testing.T) {
	long := "Soll das Timeout pro Umgebung einstellbar sein (z. B. Test länger, Produktion kurz) oder ein fester globaler Wert bleiben, weil sonst die Fehlersuche schwerer wird?"
	got := questionTitle(long, 110)
	if !strings.HasPrefix(got, "Soll das Timeout") || !strings.HasSuffix(got, "…") {
		t.Errorf("title %q begins mid-sentence or is not marked as cut", got)
	}
	whole := "Soll das Timeout pro Umgebung einstellbar sein (z. B. Test länger, Produktion kurz) oder fest?"
	if got := questionTitle(whole, 110); got != whole {
		t.Errorf("a question that fits is cut: %q", got)
	}
}

// REQ-689 AC-3 (R02): the row of a long question shows its start and unfolds the whole text.
func TestInboxQuestionRowUnfoldsTheWholeText(t *testing.T) {
	e := ovEnv(t)
	long := "@anna Soll das Timeout pro Umgebung einstellbar sein (z. B. Test länger, Produktion kurz) oder ein fester globaler Wert bleiben, weil sonst die Fehlersuche schwerer wird?"
	askAnna(t, e, long)
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	rows := rowOf(page)
	if !strings.Contains(rows, `class="lnk" href="`) || !strings.Contains(rows, ">Soll das Timeout pro Umgebung") {
		t.Errorf("the title does not begin with the question:\n%s", rows)
	}
	if !strings.Contains(rows, `<details class="tfull"><summary>Full text</summary><p>`) || !strings.Contains(rows, "weil sonst die Fehlersuche schwerer wird?</p>") {
		t.Errorf("the whole text cannot be unfolded:\n%s", rows)
	}
	short := "@anna Timeout 30 oder 60?"
	e2 := ovEnv(t)
	askAnna(t, e2, short)
	if _, page := fetchPage(t, e2.Member, e2.Base+"/ui/inbox"); strings.Contains(page, `class="tfull"`) {
		t.Errorf("a question that fits offers a second copy of itself")
	}
}

// REQ-689 AC-4 (N05): the preview says what the text says: no marks of a note box, a quote or a task
// list, also in a text that was stored as one line.
func TestInboxPreviewDropsNoteQuoteAndTaskMarkup(t *testing.T) {
	l := langEN
	const want = "Entwurf Das ist nur ein erster Entwurf, bitte Feedback. Idee festgehalten Mit Jonas abstimmen"
	for name, in := range map[string]string{
		"lines":     "Entwurf\n> [!NOTE]\n> Das ist nur ein erster Entwurf, bitte Feedback.\n\n- [x] Idee festgehalten\n- [ ] Mit Jonas abstimmen",
		"one line":  "Entwurf > [!NOTE] > Das ist nur ein erster Entwurf, bitte Feedback. - [x] Idee festgehalten - [ ] Mit Jonas abstimmen",
		"nested":    "Entwurf\n> > [!WARNING]\n> > Das ist nur ein erster Entwurf, bitte Feedback.\n* [X] Idee festgehalten\n+ [ ] Mit Jonas abstimmen",
		"numbering": "Entwurf\n> [!TIP] Das ist nur ein erster Entwurf, bitte Feedback.\n1. [x] Idee festgehalten\n2. [ ] Mit Jonas abstimmen",
	} {
		got := previewText(l, in, 220, nil)
		if got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if got := previewText(l, "5 > 3 und a - [x] bleibt", 220, nil); !strings.Contains(got, "5 > 3") {
		t.Errorf("a comparison lost its sign: %q", got)
	}
}

// REQ-689 AC-4 (N05): a text mention keeps its lines in the stored snippet, so the row can tell a quote
// from running text.
func TestTextMentionSnippetKeepsItsLines(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Spool begrenzen"})
	postForm(t, e, e.Member, "/ui/requests/"+itoa(d.Request.ID)+"/close", url.Values{"mode": {"comment"}, "reason": {"@lars Entwurf\n> [!NOTE]\n> Nur ein erster Entwurf.\n- [x] Idee festgehalten"}}).Body.Close()
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/inbox")
	for _, raw := range []string{"[!NOTE]", "[x]", "&gt; Nur"} {
		if strings.Contains(page, raw) {
			t.Errorf("the row of the mention shows %q:\n%s", raw, inboxBody(page))
		}
	}
	if !strings.Contains(page, "Entwurf Nur ein erster Entwurf. Idee festgehalten") {
		t.Errorf("the row does not read as plain text:\n%s", inboxBody(page))
	}
}

// REQ-689 AC-4 (P17, N13): a question that was answered is not "Waiting for you" any more, and the
// number on each tab is the number of rows the tab shows. The tick has a word beside it.
func TestInboxAnsweredQuestionLeavesAndTabsCountTheirRows(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	room, mid := askAnna(t, e, "@anna Soll das Timeout 30 oder 60 Sekunden sein?")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !strings.Contains(page, `<span>Done</span>`) {
		t.Errorf("the tick of a row has no word beside it:\n%s", inboxBody(page))
	}
	chip := regexp.MustCompile(`data-f="(\w+)"[^>]*>[^<]*<span class="counter">(\d+)</span>`)
	chips := chip.FindAllStringSubmatch(page, -1)
	if len(chips) != 5 {
		t.Fatalf("filter chips: %v", chips)
	}
	for _, c := range chips {
		path := "/ui/inbox"
		if c[1] != "alle" {
			path += "?f=" + c[1]
		}
		_, filtered := fetchPage(t, e.Member, e.Base+path)
		if got := strings.Count(filtered, `class="row nrow`); strconv.Itoa(got) != c[2] {
			t.Errorf("the tab %s says %s, its list has %d rows", c[1], c[2], got)
		}
	}
	resp := tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {room}, "reply_to": {itoa(mid)}, "to": {"alice"}, "body": {"60 Sekunden."}})
	resp.Body.Close()
	_, after := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(after, "Soll das Timeout") {
		t.Errorf("the answered question is still in the inbox:\n%s", inboxBody(after))
	}
	if !regexp.MustCompile(`data-f="fragen"[^>]*>[^<]*<span class="counter">0</span>`).MatchString(after) {
		t.Errorf("the tab Questions still counts the answered question")
	}
}
