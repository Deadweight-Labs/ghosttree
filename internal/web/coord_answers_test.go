package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// askMember puts a question with proposed answers to person:2 (the member of the
// test environment) into the project room and returns the room and the message.
func askMember(t *testing.T, e shellEnv, client, body string, options ...string) (string, int64) {
	t.Helper()
	room := store.RoomKeyForProject(shellProject)
	ovAgent(t, e.St, shellProject, "claude:anna-box", "anna-agent", "person:2")
	id, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "person:1", ClientID: client, Body: body, Intent: store.IntentQuestion, Mentions: []string{"person:2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) > 0 {
		if err := e.St.SaveAnswerOptions(store.AnswerOnMessage, id, options); err != nil {
			t.Fatal(err)
		}
	}
	return room, id
}

// REQ-679 AC-2: a question that waits for the person stands once in the inbox, and
// the numbers of the chips are the rows of the page.
func TestInboxQuestionStandsOnceAndTheCountsAddUp(t *testing.T) {
	e := ovEnv(t)
	room, _ := askMember(t, e, "q-once", "Should the spool keep fifty entries?")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if n := strings.Count(page, "Should the spool keep fifty entries?"); n != 1 {
		t.Fatalf("the question stands %d times, want once:\n%s", n, page)
	}
	if strings.Contains(page, `id="mentioned"`) {
		t.Errorf("the question is said to the person a second time")
	}
	if n := strings.Count(page, `class="row nrow`); n != 1 {
		t.Errorf("%d rows, want 1", n)
	}
	// A plain mention in the same room is still said to the person, one row, and the
	// question does not count in its number.
	if _, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "person:1", ClientID: "fyi", Body: "@anna fyi the build is green", Mentions: []string{"person:2"}}); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if n := strings.Count(page, "Should the spool keep fifty entries?"); n != 1 {
		t.Errorf("the question stands %d times next to a mention", n)
	}
	if !strings.Contains(page, "fyi the build is green") || strings.Contains(page, "unread mentions") {
		t.Errorf("the mention besides the question is lost or counts the question: %s", page)
	}
	if n := strings.Count(page, `class="row nrow`); n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
	if m := regexp.MustCompile(`(?s)<span>All</span>.*?(\d+)</`).FindStringSubmatch(page); m != nil && m[1] != "2" {
		t.Errorf("chip All counts %s, want 2", m[1])
	}
}

// REQ-679 AC-1: the card of a question in the room offers the proposed answers as the
// inbox does; a click answers, closes the question and returns to the room.
func TestRoomCardOffersTheProposedAnswersAndClosesTheQuestion(t *testing.T) {
	e := ovEnv(t)
	room, msg := askMember(t, e, "q-room", "Which migration order?", "Separate", "One migration")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	for _, want := range []string{`class="rq4-opts"`, `action="/ui/inbox/answer"`, `name="kind" value="attention"`, `name="option" value="1"`, `name="option" value="2"`,
		"<span>Separate</span>", "<span>One migration</span>", `name="room" value="` + room + `"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the room card lacks %q", want)
		}
	}
	m := regexp.MustCompile(`name="kind" value="attention"><input type="hidden" name="id" value="(\d+)">`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no attention number on the answer buttons")
	}
	resp := tpPost(t, e, e.Member, "/ui/inbox/answer", url.Values{"kind": {"attention"}, "id": {m[1]}, "option": {"2"}, "next": {"1"}, "room": {room}})
	resp.Body.Close()
	if want := "/ui/coord?room=" + url.QueryEscape(room); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
		t.Fatalf("answer from the room -> %d %q, want 303 to %q", resp.StatusCode, resp.Header.Get("Location"), want)
	}
	_, page = fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(page, `class="rq4-opts"`) || strings.Contains(page, "just asked") {
		t.Errorf("the answered question still waits in the room")
	}
	if !strings.Contains(page, "One migration") || !strings.Contains(page, "Answered") {
		t.Errorf("the room does not show the answer and that the question is answered: %s", page)
	}
	_ = msg
	// The form can only lead back into the coordination page, whatever it says.
	resp = tpPost(t, e, e.Member, "/ui/inbox/answer", url.Values{"kind": {"attention"}, "id": {m[1]}, "option": {"1"}, "next": {"1"}, "room": {"https://evil.example/x"}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "" && !strings.HasPrefix(loc, "/ui/coord?room=") && !strings.HasPrefix(loc, "/ui/inbox") {
		t.Errorf("redirect to %q", loc)
	}
}

// A reply to the question, written in the room, closes it: the card does not wait
// for a second click on "Mark as answered".
func TestReplyInTheRoomClosesTheQuestion(t *testing.T) {
	e := ovEnv(t)
	room, msg := askMember(t, e, "q-reply", "Ship it?")
	resp := tpPost(t, e, e.Member, "/ui/coord/send", url.Values{"room": {room}, "body": {"Yes, ship it."}, "reply_to": {itoa(msg)}})
	resp.Body.Close()
	_, page := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(page, "just asked") || strings.Contains(page, "Mark as answered") {
		t.Errorf("a reply to the question left it open: %d %s", resp.StatusCode, page)
	}
}

// REQ-679 AC-4: a repo filter on a quiet repo says that more waits elsewhere and
// links to all of it, instead of showing what looks like an empty inbox.
func TestInboxFilterSaysWhatWaitsInOtherRepos(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Secret work", Project: shellHiddenProject})
	tpTask(t, e, shellHiddenProject, req.Request.ID, humantask.CreateInput{Kind: "action", Title: "Rotate the other key", Everyone: true})

	_, page := fetchPage(t, e.Owner, e.Base+"/ui/inbox?project="+url.QueryEscape(shellProject))
	mustContain(t, "filtered inbox", page, "One more entry waits in another repo.", `href="/ui/inbox?project="`, "Show all repos")
	if strings.Contains(page, "Rotate the other key") {
		t.Error("the filter shows the task of the other repo")
	}
	// Without the filter everything is there and nothing is said about elsewhere.
	_, all := fetchPage(t, e.Owner, e.Base+"/ui/inbox?project=")
	if !strings.Contains(all, "Rotate the other key") || strings.Contains(all, "waits in another repo") {
		t.Errorf("the unfiltered inbox lacks the task or talks about other repos: %s", all)
	}
	// A filter that hides nothing says nothing.
	_, own := fetchPage(t, e.Owner, e.Base+"/ui/inbox?project="+url.QueryEscape(shellHiddenProject))
	if strings.Contains(own, "waits in another repo") || strings.Contains(own, "more entries wait") {
		t.Error("a filter that hides nothing talks about other repos")
	}
}
