package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// chatFixture is a project room with a signed-in person (interactive, so that
// the person's posts are human) and one agent.
func chatFixture(t *testing.T) (*httptest.Server, *store.Store, string, func(path string) string) {
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
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "claude:chat", PrincipalID: "person:1", Person: "robin",
		Provider: "claude", DisplayName: "claude@apps-vm", RoomKey: room,
	}); err != nil {
		t.Fatal(err)
	}
	return srv, st, room, func(path string) string { return coordPageBody(t, client, srv.URL+path) }
}

func appendAgent(t *testing.T, st *store.Store, room, id, body, intent string, mentions ...string) {
	t.Helper()
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:chat",
		ClientID: id, Body: body, Intent: intent, Mentions: mentions,
	}); err != nil {
		t.Fatal(err)
	}
}

func messageItem(t *testing.T, page string, sequence string) string {
	t.Helper()
	start := strings.Index(page, `id="message-`+sequence+`"`)
	if start < 0 {
		t.Fatalf("message %s not rendered", sequence)
	}
	start = strings.LastIndex(page[:start], "<li")
	end := strings.Index(page[start:], "</li>")
	return page[start : start+end]
}

func TestCoordRoomGroupsConsecutiveMessagesOfOneSender(t *testing.T) {
	_, st, room, get := chatFixture(t)
	appendAgent(t, st, room, "a1", "first", "")
	appendAgent(t, st, room, "a2", "second", "")
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "person:1",
		AuthorPrincipalID: "person:1", AuthorKind: store.AuthorHuman, ClientID: "h1", Body: "third",
	}); err != nil {
		t.Fatal(err)
	}
	appendAgent(t, st, room, "a3", "fourth", "")
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	classes := func(seq string) string {
		item := messageItem(t, page, seq)
		return item[:strings.Index(item, ">")]
	}
	if c := classes("1"); !strings.Contains(c, "coord-message-group-start") {
		t.Errorf("first message starts a group: %s", c)
	}
	if c := classes("2"); !strings.Contains(c, "coord-message-continuation") {
		t.Errorf("same sender continues the group: %s", c)
	}
	if c := classes("3"); !strings.Contains(c, "coord-message-group-start") || !strings.Contains(c, "is-own") {
		t.Errorf("a person's own message starts a new group and sits on the right: %s", c)
	}
	if c := classes("4"); !strings.Contains(c, "coord-message-group-start") {
		t.Errorf("the agent speaking again after someone else starts a group: %s", c)
	}
	// An agent shows as name plus @machine; a person as a round avatar.
	first := messageItem(t, page, "1")
	if !strings.Contains(first, "<strong>claude</strong>") || !strings.Contains(first, `<span class="cm-host">@apps-vm</span>`) || !strings.Contains(first, "av agent") {
		t.Errorf("agent identity: %s", first)
	}
	if own := messageItem(t, page, "3"); !strings.Contains(own, "<strong>You</strong>") || strings.Contains(own, "coord-message-avatar") {
		t.Errorf("own message: %s", own)
	}
}

func TestCoordRoomShowsADirectiveAsAPinnedPlateWithEnd(t *testing.T) {
	_, st, room, get := chatFixture(t)
	appendAgent(t, st, room, "a1", "before", "")
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin", TokenKind: store.WebSessionKind}, "")
	if _, err := access.CreateStanding(store.StandingInput{RoomKey: room, ClientID: "s1", Body: "Never push to main."}); err != nil {
		t.Fatal(err)
	}
	appendAgent(t, st, room, "a2", "after", "")
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	plate := messageItem(t, page, "2")
	for _, want := range []string{`class="slab clay"`, "Directive", "Never push to main.", "All agents in x/chat", "until ended", `action="/ui/coord/standing/end"`, ">End<"} {
		if !strings.Contains(plate, want) && !strings.Contains(plate, strings.ReplaceAll(want, "x/chat", "github.com/x/chat")) {
			t.Errorf("directive plate missing %q: %s", want, plate)
		}
	}
	if c := messageItem(t, page, "3"); !strings.Contains(c, "coord-message-group-start") {
		t.Error("a plate breaks the group")
	}
	// Once ended it stays in the history without an End action.
	standing, _ := st.StandingInstructions(room)
	if err := access.EndStanding(room, standing[0].MessageID); err != nil {
		t.Fatal(err)
	}
	ended := messageItem(t, get("/ui/coord?room="+url.QueryEscape(room)), "2")
	if strings.Contains(ended, `action="/ui/coord/standing/end"`) || !strings.Contains(ended, "Ended") || !strings.Contains(ended, "is-ended") {
		t.Errorf("an ended directive shows as ended: %s", ended)
	}
}

func TestCoordRoomShowsARequestToThePersonAsAnAnswerableCard(t *testing.T) {
	_, st, room, get := chatFixture(t)
	appendAgent(t, st, room, "q1", "Roles first or column first?", store.IntentQuestion, "person:1")
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	card := messageItem(t, page, "1")
	for _, want := range []string{`class="ask clay"`, "Asks robin", "Roles first or column first?", `name="action" value="answer"`, "Answered", ">Reply<", "just asked", `name="attention_id"`} {
		if !strings.Contains(card, want) {
			t.Errorf("request card missing %q: %s", want, card)
		}
	}
}

func TestCoordRoomShowsSystemMessagesAsASmallCenteredLine(t *testing.T) {
	_, st, room, get := chatFixture(t)
	if _, err := st.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: store.WaitCycleSender,
		AuthorPrincipalID: "system", AuthorKind: store.AuthorSystem, ClientID: "sys1", Body: "claude joined the room",
	}); err != nil {
		t.Fatal(err)
	}
	line := messageItem(t, get("/ui/coord?room="+url.QueryEscape(room)), "1")
	if !strings.Contains(line, `class="cm-event"`) || !strings.Contains(line, "claude joined the room") || strings.Contains(line, "cm-bubble") {
		t.Errorf("system line: %s", line)
	}
}

func TestCoordRoomBubblesEscapeTheirBody(t *testing.T) {
	_, st, room, get := chatFixture(t)
	appendAgent(t, st, room, "x1", `<img src=x onerror=alert(1)>`, "")
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	if strings.Contains(page, "<img src=x") || !strings.Contains(page, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Error("message bodies are escaped")
	}
}

func TestCoordComposerOffersTheThreeModesWithTheirOwnSubmit(t *testing.T) {
	_, _, room, get := chatFixture(t)
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	for _, want := range []string{
		`name="mode" value="message" checked`, `name="mode" value="directive"`, `name="mode" value="request"`,
		`formaction="/ui/coord/standing/create" name="confirm_scope" value="1"`,
		`name="intent" value="question"`, `<textarea id="coord-message-body" name="body"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("composer missing %q", want)
		}
	}
	if strings.Contains(page, `name="intent" value="question" checked`) {
		t.Error("no request kind is preselected; a plain message carries none")
	}
}

func TestCoordWaitingNamesTheAgeOfARequest(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for created, want := range map[string]string{
		"2026-10-02T11:59:40Z": "just asked",
		"2026-10-02T11:48:00Z": "waiting 12 min",
		"2026-10-02T09:00:00Z": "waiting 3 h",
		"2026-09-29T12:00:00Z": "waiting 3 d",
	} {
		if got := coordWaiting(created, now); got != want {
			t.Errorf("coordWaiting(%s) = %q, want %q", created, got, want)
		}
	}
}
