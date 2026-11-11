package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// inboxPeople makes alice and anna visible to each other: both have an agent in
// the project room.
func inboxPeople(t *testing.T, e shellEnv) {
	t.Helper()
	ovAgent(t, e.St, shellProject, "claude:anna-box", "anna-agent", "person:2")
	ovAgent(t, e.St, shellProject, "claude:alice-box", "alice-agent", "person:1")
}

// inboxDM puts a direct room between alice (person:1) and anna (person:2), the
// member whose inbox the tests read.
func inboxDM(t *testing.T, e shellEnv) (room string, alice store.CoordAccess) {
	t.Helper()
	inboxPeople(t, e)
	alice = e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	room = store.RoomKeyForDirect([]string{"person:1", "person:2"})
	if err := alice.EnsureDirect(store.CoordRoom{Key: room, Kind: store.RoomDirect, Members: []string{"person:1", "person:2"}}); err != nil {
		t.Fatal(err)
	}
	return room, alice
}

func TestInboxDirectSectionShowsTheMessageAndNoDirectLabel(t *testing.T) {
	e := ovEnv(t)
	room, alice := inboxDM(t, e)
	if _, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "dm1", Body: "Do you have a minute for the roles question?"}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	for _, want := range []string{"Do you have a minute for the roles question?", `action="/ui/inbox/reply"`, `name="room" value="` + room + `"`, `data-cat="direkt `, "Reply to alice"} {
		if !strings.Contains(page, want) {
			t.Errorf("inbox lacks %q", want)
		}
	}
	if strings.Contains(page, "messages you directly") || strings.Contains(page, "schreibt dir direkt") {
		t.Error("a direct message does not announce itself as direct")
	}
	// alice herself has nothing unread from anna.
	_, own := fetchPage(t, e.Owner, e.Base+"/ui/inbox")
	if strings.Contains(own, "Do you have a minute") {
		t.Error("the sender sees her own message as waiting")
	}
}

func TestInboxGroupMessageNamesTheGroupAndQuotesTheOwnMessage(t *testing.T) {
	e := ovEnv(t)
	inboxPeople(t, e)
	alice := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	anna := e.St.CoordinationFor(store.Principal{ID: "person:2", Label: "anna", TokenKind: store.WebSessionKind}, "")
	group, err := alice.CreateGroup(store.GroupInput{Label: "Release team", Members: []string{"person:1", "person:2"}})
	if err != nil {
		t.Fatal(err)
	}
	asked, err := anna.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: group.Key, ClientID: "g1", Body: "Will you approve the release once the checks are green?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: group.Key, ClientID: "g2", ReplyTo: asked, Body: "The release is ready, only your approval is missing."}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	for _, want := range []string{"Release team", `class="rquote"`, "<b>You</b> Will you approve the release", "The release is ready, only your approval is missing."} {
		if !strings.Contains(page, want) {
			t.Errorf("group row lacks %q", want)
		}
	}
	if !regexp.MustCompile(`<b>alice</b> in <a href="[^"]*">Release team</a>`).MatchString(page) {
		t.Error("the group row does not read \"alice in Release team\"")
	}
}

func TestInboxFilterNarrowsAndCountsStayWhole(t *testing.T) {
	e := ovEnv(t)
	room, alice := inboxDM(t, e)
	if _, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "dm1", Body: "Only a direct note"}); err != nil {
		t.Fatal(err)
	}
	_, direct := fetchPage(t, e.Member, e.Base+"/ui/inbox?f=direkt")
	if !strings.Contains(direct, "Only a direct note") || !strings.Contains(direct, `data-f="direkt" class="on"`) {
		t.Error("the direct filter hides the direct message")
	}
	if strings.Contains(direct, `id="waiting"`) {
		t.Error("the direct filter keeps the section of what waits")
	}
	if strings.Contains(direct, `id="direct"`) {
		t.Error("a direct message has a section of its own again")
	}
	_, tasks := fetchPage(t, e.Member, e.Base+"/ui/inbox?f=aufgaben")
	if strings.Contains(tasks, "Only a direct note") || !strings.Contains(tasks, "Nothing matches this filter.") {
		t.Error("the task filter shows the message or says nothing")
	}
	if !regexp.MustCompile(`data-f="alle"[^>]*>All<span class="counter">1</span>`).MatchString(tasks) {
		t.Errorf("the counts shrink with the filter: %s", tasks)
	}
	_, odd := fetchPage(t, e.Member, e.Base+"/ui/inbox?f=nonsense")
	if !strings.Contains(odd, `data-f="alle" class="on"`) {
		t.Error("an unknown filter is not read as all")
	}
}

func TestInboxReplyPostsInTheRoomAndReadsIt(t *testing.T) {
	e := ovEnv(t)
	room, alice := inboxDM(t, e)
	id, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "dm1", Body: "Can you take the migration?"})
	if err != nil {
		t.Fatal(err)
	}
	resp := tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {room}, "reply_to": {itoa(id)}, "body": {"Yes, on it."}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
		t.Fatalf("reply -> %d %q, want 303 /ui/inbox", resp.StatusCode, resp.Header.Get("Location"))
	}
	anna := e.St.CoordinationFor(store.Principal{ID: "person:2", Label: "anna", TokenKind: store.WebSessionKind}, "")
	msgs, err := anna.Messages(store.DestinationRoom, room, 0, 10)
	if err != nil || len(msgs) != 2 || msgs[1].Body != "Yes, on it." || msgs[1].ReplyTo != id || msgs[1].AuthorPrincipalID != "person:2" {
		t.Fatalf("stored messages: %+v %v", msgs, err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(page, "Can you take the migration?") {
		t.Error("a conversation that was answered stays in the inbox")
	}
	// An empty reply and a foreign room are refused.
	resp = tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {room}, "body": {"  "}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty reply -> %d, want 400", resp.StatusCode)
	}
	resp = tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {store.RoomKeyForDirect([]string{"person:1", "person:4"})}, "body": {"hi"}})
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("reply into a room the viewer is not in -> %d", resp.StatusCode)
	}
}

func TestInboxQuestionCanBeAnsweredOrDismissedInPlace(t *testing.T) {
	e := ovEnv(t)
	room, alice := inboxDM(t, e)
	for i, body := range []string{"Which migration order do you want?", "Is the second order fine too?"} {
		if _, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "q" + itoa(int64(i)), Body: body, Intent: store.IntentQuestion, Mentions: []string{"person:2"}}); err != nil {
			t.Fatal(err)
		}
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	ids := regexp.MustCompile(`name="attention_id" value="(\d+)"`).FindAllStringSubmatch(page, -1)
	if len(ids) < 4 || !strings.Contains(page, `action="/ui/inbox/done"`) {
		t.Fatalf("a question lacks the reply field or the done button: %s", page)
	}
	first, second := ids[0][1], ""
	for _, m := range ids {
		if m[1] != first {
			second = m[1]
		}
	}
	resp := tpPost(t, e, e.Member, "/ui/inbox/done", url.Values{"attention_id": {first}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
		t.Fatalf("done -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp = tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {room}, "attention_id": {second}, "body": {"Second order."}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("answer -> %d", resp.StatusCode)
	}
	_, page = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(page, `name="attention_id"`) {
		t.Errorf("a question that was dismissed or answered stays: %s", page)
	}
	resp = tpPost(t, e, e.Member, "/ui/inbox/done", url.Values{"attention_id": {"x"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("done without an item -> %d, want 400", resp.StatusCode)
	}
}

func TestInboxGuestAndPastedTokenGetNoReplyForms(t *testing.T) {
	e := ovEnv(t)
	room, alice := inboxDM(t, e)
	if _, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "dm1", Body: "Private note"}); err != nil {
		t.Fatal(err)
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/inbox")
	if strings.Contains(guest, "Private note") {
		t.Error("a guest sees a direct message of two others")
	}
}

// REQ-599 AC-7: the inbox has two sections, what waits for the person and what
// was said to them; a direct message is said to them and stands in the second,
// beside the mentions in rooms. What nobody read for two weeks falls out of it.
func TestInboxHasOnlyWhatWaitsAndWhatWasSaidAndOldSayingsFallOut(t *testing.T) {
	e := ovEnv(t)
	room, alice := inboxDM(t, e)
	if _, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "dm-old", Body: "A note from the past"}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !strings.Contains(page, `id="mentioned"`) || !strings.Contains(page, "A note from the past") {
		t.Errorf("the direct message is not in the section of what was said:\n%s", page)
	}
	if strings.Contains(page, `id="direct"`) || strings.Contains(page, "Direct to you") {
		t.Error("a direct message has a section of its own")
	}
	if n := strings.Count(page, `<section class="box" id="`); n > 2 {
		t.Errorf("the inbox has %d sections, want at most two", n)
	}

	atNow(t, time.Now().Add((inboxMentionDays-2)*24*time.Hour))
	if _, page := fetchPage(t, e.Member, e.Base+"/ui/inbox"); !strings.Contains(page, "A note from the past") {
		t.Error("a message that is not yet two weeks old fell out")
	}
	atNow(t, time.Now().Add((inboxMentionDays+2)*24*time.Hour))
	_, late := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(late, "A note from the past") {
		t.Error("a message nobody read for more than two weeks is still in the inbox")
	}
}
