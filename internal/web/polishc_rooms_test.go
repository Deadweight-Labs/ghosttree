package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-688 AC-4 (N26): the quick answer to a mention in a comment is a comment on
// the object it stands in, not a loose message in the room.
func TestQuickAnswerToAMentionInAComment(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Spool begrenzen"})
	id := d.Request.ID
	postForm(t, e, e.Member, "/ui/requests/"+itoa(id)+"/close", url.Values{"mode": {"comment"}, "reason": {"Bitte @lars schau auf den 401."}}).Body.Close()
	lars := e.St.Access(store.Principal{ID: "person:4", Label: "lars"})
	list, err := lars.TextMentions(shellProject)
	if err != nil || len(list) != 1 {
		t.Fatalf("mentions of lars: %+v %v", list, err)
	}
	inbox := fetchBody(t, e.Lead, e.Base+"/ui/inbox")
	if !strings.Contains(inbox, `action="/ui/inbox/mention-reply"`) || !strings.Contains(inbox, `name="id" value="`+itoa(list[0].ID)+`"`) {
		t.Fatalf("the mention of a comment offers no reply that goes back to the comment:\n%.2000s", inbox)
	}
	room := store.RoomKeyForProject(shellProject)
	before := roomMessageCount(t, e, room)
	resp := tpPost(t, e, e.Lead, "/ui/inbox/mention-reply", url.Values{"id": {itoa(list[0].ID)}, "body": {"Ich schaue mir den 401 an."}, "to": {"anna"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox?sent=anna" {
		t.Fatalf("reply -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if after := roomMessageCount(t, e, room); after != before {
		t.Errorf("the answer is a loose message in the room (%d -> %d messages)", before, after)
	}
	if page := fetchBody(t, e.Lead, e.Base+"/ui/x/shell/issues/"+itoa(id)); !strings.Contains(page, "Ich schaue mir den 401 an.") {
		t.Errorf("the answer is not a comment on the issue:\n%.1500s", page)
	}
	if left, _ := lars.TextMentions(shellProject); len(left) != 0 {
		t.Errorf("an answered mention stays in the inbox: %+v", left)
	}
	// Somebody else's mention cannot be answered through this door.
	resp = tpPost(t, e, e.Reviewer, "/ui/inbox/mention-reply", url.Values{"id": {itoa(list[0].ID)}, "body": {"x"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a foreign mention answered with %d, want 404", resp.StatusCode)
	}
}

func roomMessageCount(t *testing.T, e shellEnv, room string) int {
	t.Helper()
	member := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	page, _, err := member.MessagePresentationWindow(store.DestinationRoom, room, store.LatestWindow(100))
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Messages)
}

// REQ-688 AC-4 (N06): the people of the project are in the "To" menu of the
// room, before the agents, also when they never joined the room.
func TestRoomToMenuOffersThePeopleOfTheProjectFirst(t *testing.T) {
	e := ovEnv(t)
	inboxPeople(t, e)
	room := store.RoomKeyForProject(shellProject)
	page := fetchBody(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	start := strings.Index(page, `class="dd" data-pick`)
	if start < 0 {
		t.Fatalf("no To menu on the room page")
	}
	pop := page[start : start+strings.Index(page[start:], `data-pick-input`)]
	person, agent := strings.Index(pop, `data-label="anna"`), strings.Index(pop, `data-label="anna-agent`)
	if person < 0 {
		t.Fatalf("a person of the project is missing in the To menu:\n%s", pop)
	}
	if agent >= 0 && person > agent {
		t.Errorf("an agent stands before the person in the To menu")
	}
}

// REQ-688 AC-4 (N26): a resolved thread says who resolved it.
func TestResolvedThreadShowsWhoResolvedIt(t *testing.T) {
	e := ovEnv(t)
	room, thread := threadMention(t, e, "@anna can you take the release?")
	resp := tpPost(t, e, e.Owner, "/ui/coord/thread/state", url.Values{"thread_id": {itoa(thread)}, "state": {"resolved"}, "room": {room}})
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("resolve -> %d", resp.StatusCode)
	}
	page := fetchBody(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room)+"&thread="+itoa(thread))
	if !strings.Contains(page, "alice marked this thread resolved") {
		t.Errorf("the line does not name who resolved it:\n%.800s", page)
	}
	if !strings.Contains(page, `class="coord-thread-event cte-resolved"`) || !strings.Contains(page, "marked this thread resolved") {
		t.Errorf("the thread does not say who resolved it:\n%.600s", page)
	}
}

// REQ-688 AC-4 (N06): a question to a person written as @name in the words reaches
// that person, and the person shows as waiting for it, with nobody picked under "To".
func TestAskWithAnAtNameAsTheOnlyAddresseeReachesThePerson(t *testing.T) {
	e := ovEnv(t)
	room := store.RoomKeyForProject(shellProject)
	resp := tpPost(t, e, e.Member, "/ui/coord/send", url.Values{"room": {room}, "intent": {"question"}, "body": {"@lars Soll das Timeout 30 oder 60 Sekunden sein?"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("ask -> %d, want it sent", resp.StatusCode)
	}
	lars := e.St.CoordinationFor(store.Principal{ID: "person:4", Label: "lars", TokenKind: store.WebSessionKind}, "")
	items, err := lars.Attention()
	if err != nil || len(items) != 1 || !items[0].IsRecipient {
		t.Fatalf("lars finds the question waiting: %+v %v", items, err)
	}
}
