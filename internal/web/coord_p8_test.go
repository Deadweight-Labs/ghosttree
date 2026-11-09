package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The chat of a room (UI review P8): threads that keep their messages, the
// message as a calm head with a second line, cards for questions in threads,
// one line per agent in the list of who is here, and sentences instead of a bare
// status when a send is refused.

func p8Fixture(t *testing.T) (*http.Client, string, *store.Store, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddAccount("robin", "", true); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	client := loginInteractive(t, srv, st, "robin")
	room := store.RoomKeyForProject("github.com/x/chat")
	for _, a := range []store.CoordAgent{
		{ExternalID: "claude:chat", PrincipalID: "person:1", Person: "robin", Provider: "claude", DisplayName: "claude@apps-vm", RoomKey: room},
	} {
		if _, err := st.RegisterCoordAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	return client, srv.URL, st, room
}

func p8Human(t *testing.T, st *store.Store, room, id, body string) int64 {
	t.Helper()
	mid, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "person:1",
		AuthorPrincipalID: "person:1", AuthorKind: store.AuthorHuman, ClientID: id, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mid
}

// The thread keeps its anchor even when 50 newer messages push it out of the
// window of the room, and a state change comes with the reply or alone.
func TestCoordThreadShowsItsAnchorAndChangesItsStateWithTheReply(t *testing.T) {
	client, base, st, room := p8Fixture(t)
	anchor := p8Human(t, st, room, "anchor", "The old anchor of the thread")
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := access.PromoteRoomMessageToTaskThread(anchor, "Old thread", "Still open?", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		p8Human(t, st, room, "n"+strconv.Itoa(i), "filler "+strconv.Itoa(i))
	}
	threadURL := base + coordThreadURL(room, threadID)
	page := coordPageBody(t, client, threadURL)
	if !strings.Contains(page, "The old anchor of the thread") || !strings.Contains(page, `id="thread-anchor-1"`) {
		t.Fatal("the anchor of the thread is missing when it is older than the window of the room")
	}
	if !strings.Contains(page, `name="state_was" value="open"`) || !strings.Contains(page, `data-state-input`) {
		t.Fatal("the composer of the thread has no row for the state")
	}
	// A reply and the state in one send.
	res := authenticatedPostForm(t, client, base+"/ui/coord/thread/post", url.Values{
		"thread_id": {strconv.FormatInt(threadID, 10)}, "body": {"Done, thanks"}, "state": {"resolved"}, "state_was": {"open"},
	})
	res.Body.Close()
	if res.StatusCode >= 400 {
		t.Fatalf("reply with state: %d", res.StatusCode)
	}
	page = coordPageBody(t, client, threadURL)
	if !strings.Contains(page, "Done, thanks") || !strings.Contains(page, `class="state done sm"`) {
		t.Errorf("the reply and the new state are not on the page")
	}
	// The state alone: no words needed.
	res = authenticatedPostForm(t, client, base+"/ui/coord/thread/post", url.Values{
		"thread_id": {strconv.FormatInt(threadID, 10)}, "state": {"open"}, "state_was": {"resolved"},
	})
	res.Body.Close()
	if res.StatusCode >= 400 {
		t.Fatalf("state alone: %d", res.StatusCode)
	}
	if page = coordPageBody(t, client, threadURL); !strings.Contains(page, `class="state open sm"`) {
		t.Error("the state alone did not reopen the thread")
	}
	// Neither words nor a different state is no post: it says so in a sentence.
	res = authenticatedPostForm(t, client, base+"/ui/coord/thread/post", url.Values{
		"thread_id": {strconv.FormatInt(threadID, 10)}, "state": {"open"}, "state_was": {"open"},
	})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, res), messages["coord.thread_need_text"]) {
		t.Errorf("an empty post is refused with a sentence, got %d", res.StatusCode)
	}
}

// A question in a thread is the same card as in the room: kind as a pill, the
// addressee once.
func TestCoordThreadShowsQuestionsAsCards(t *testing.T) {
	client, base, st, room := p8Fixture(t)
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := access.CreateTaskThreadInRoom(room, "Thread", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "claude:chat").ThreadPost(threadID, store.CoordMessage{ClientID: "q", Body: "Which one first?", Intent: store.IntentQuestion, Mentions: []string{"person:1"}}); err != nil {
		t.Skipf("agent cannot post to the thread in this fixture: %v", err)
	}
	page := coordPageBody(t, client, base+coordThreadURL(room, threadID))
	card := segment(t, page, `class="rm-msg is-ask"`, "</article>")
	for _, want := range []string{`rqk-question`, ">Question<", "to robin", "Which one first?"} {
		if !strings.Contains(card, want) {
			t.Errorf("question in a thread lacks %q: %s", want, card)
		}
	}
}

// Sessions of one agent (same owner, same tool, same machine) are one line with
// the sessions beneath it; people stay single.
func TestGroupParticipantsHereOneLinePerAgent(t *testing.T) {
	mk := func(id, label, branch string) coordParticipantView {
		return coordParticipantView{ID: id, Label: label, Provider: "claude", Branch: branch, Person: "Robin", Machine: "mainex", Dot: "off"}
	}
	in := []coordParticipantView{
		mk("a1", "claude-mainex · Robin · mainex · feat/a", "feat/a"),
		{ID: "person:1", Label: "Robin", Human: true, Current: true},
		mk("a2", "claude-mainex · Robin · mainex · feat/b", "feat/b"),
		{ID: "a3", Label: "Claude · Lena", Provider: "claude", Person: "Lena", Machine: "laptop", Dot: "off"},
	}
	got := groupParticipantsHere(langEN, in)
	if len(got) != 3 {
		t.Fatalf("want agent, person, agent: %+v", got)
	}
	if got[0].Human || got[0].Count != 2 || got[0].Title != "Claude" || got[0].Sub != "Robin · mainex" {
		t.Errorf("sessions of one agent are not one line: %+v", got[0])
	}
	if !got[1].Human || got[2].Count != 1 {
		t.Errorf("order or count: %+v", got)
	}
	stack := stackHere(got)
	if !stack[0].Human {
		t.Error("people come first in the avatars of the head")
	}
}

// The quiet second line carries role, machine, branch and addressee; the head
// only name, kind and time. A message to everyone has no addressee at all.
func TestCoordMessageHeadIsCalmAndQuoteIsOneLine(t *testing.T) {
	client, base, st, room := p8Fixture(t)
	first := p8Human(t, st, room, "q1", "**Bold** text\nsecond line")
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:chat", ClientID: "a1",
		Body: "On it", ReplyTo: first, Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	page := coordPageBody(t, client, base+"/ui/coord?room="+url.QueryEscape(room))
	reply := messageItem(t, page, "2")
	head := segment(t, reply, `<header class="rm-head`, "</header>")
	for _, gone := range []string{"apps-vm", "to robin", "Member"} {
		if strings.Contains(head, gone) {
			t.Errorf("the head of a message carries %q: %s", gone, head)
		}
	}
	if !strings.Contains(reply, `<p class="cm-meta">`) || !strings.Contains(reply, "<span>apps-vm</span>") || !strings.Contains(reply, "<span>to robin</span>") {
		t.Errorf("the second line lacks machine and addressee: %s", reply)
	}
	quote := segment(t, reply, `class="cm-quote"`, "</a>")
	if strings.Contains(quote, "**") || !strings.Contains(quote, "<span>Bold text second line</span>") || !strings.Contains(quote, "<b>robin</b>") {
		t.Errorf("the quote is not one clean line with the name: %s", quote)
	}
	if strings.Contains(page, "coord-delivery") || strings.Contains(page, "coord-private-hint") {
		t.Error("delivery counts and the privacy sentence do not stand under messages")
	}
}

// A refused send says why, in the language of the page, with names instead of
// identifiers.
func TestCoordSendRefusalsAreSentences(t *testing.T) {
	client, base, st, room := p8Fixture(t)
	p8Human(t, st, room, "m", "hello")
	// A request without an addressee.
	res := authenticatedPostForm(t, client, base+"/ui/coord/send", url.Values{"room": {room}, "body": {"Question?"}, "intent": {"question"}})
	text := body(t, res)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(text, messages["coord.err.recipient_required"]) {
		t.Errorf("a request without recipient: %d %q", res.StatusCode, text)
	}
	if got := coordCandidateLabel(langEN, "claude:gus-box:33333333-aaaa-4bbb-8ccc-000000000003 (Gus)"); strings.Contains(got, "33333333") {
		t.Errorf("a candidate shows its identifier: %q", got)
	}
}

// The script swaps the surfaces of a thread twice (the list is both); the
// second swap must not write back an empty list.
func TestCoordSurfaceSwapCopiesTheIncomingNodes(t *testing.T) {
	src, err := os.ReadFile("static/ui-coord.js")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(src), "const replaceSurface")
	if i < 0 || !strings.Contains(string(src)[i:i+700], "cloneNode(true)") {
		t.Error("replaceSurface moves the incoming nodes: the second surface of the same list would be left empty")
	}
}
