package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestCoordOrderByActivityAndLanding(t *testing.T) {
	mk := func(key, at string) store.CoordRoomSummary {
		return store.CoordRoomSummary{Room: store.CoordRoom{Key: key, Kind: store.RoomProject}, LastMessageAt: at}
	}
	in := []store.CoordRoomSummary{mk("project:a", ""), mk("project:b", "2026-10-16T10:00:00Z"), mk("project:c", "2026-10-16T12:00:00Z")}
	ordered := coordOrderByActivity(in)
	if ordered[0].Room.Key != "project:c" || ordered[1].Room.Key != "project:b" || ordered[2].Room.Key != "project:a" {
		t.Fatalf("order = %v", ordered)
	}
	if in[0].Room.Key != "project:a" {
		t.Error("the input is not reordered in place")
	}
	sidebar := buildCoordSidebar(langEN, ordered, "person:1", "", nil)
	if got := coordLandingRoom(ordered, sidebar); got != "project:c" {
		t.Errorf("landing = %q, want the room with the latest event", got)
	}
	// A room the viewer does not see in the tab bar is never the landing.
	if got := coordLandingRoom(ordered, coordSidebarView{Projects: sidebar.Projects[1:]}); got != "project:b" {
		t.Errorf("landing = %q, want the newest visible room", got)
	}
	if got := coordLandingRoom(nil, coordSidebarView{}); got != "" {
		t.Errorf("landing without rooms = %q", got)
	}
}

func TestCoordRoomsPageLandsInTheRoomWithTheLatestEvent(t *testing.T) {
	srv, st, room, _ := chatFixture(t)
	other := store.RoomKeyForProject("github.com/x/other")
	materializeWebRoom(t, st, other)
	appendAgent(t, st, room, "m1", "newest event", "")
	client := loginInteractive(t, srv, st, "robin")
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Get(srv.URL + "/ui/coord")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != coordRoomURL(room, "", 0) {
		t.Fatalf("landing = %d %q, want the chat room", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestCoordDedupeAgentsKeepsTheLastSeenOfTwoIdenticalLabels(t *testing.T) {
	in := []coordParticipantView{
		{ID: "person:1", Label: "Robin", Human: true},
		{ID: "claude:old", Label: "Robin · mac · feat/x", Provider: "claude", LastSeen: "2026-10-16T09:00:00Z"},
		{ID: "claude:new", Label: "Robin · mac · feat/x", Provider: "claude", LastSeen: "2026-10-16T10:00:00Z"},
		{ID: "codex:one", Label: "Robin · mac · feat/x", Provider: "codex", LastSeen: "2026-10-16T08:00:00Z"},
	}
	out := dedupeAgents(in)
	ids := []string{}
	for _, p := range out {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "person:1,claude:new,codex:one" {
		t.Errorf("participants = %v", ids)
	}
}

func TestCoordHereOrdersAskingAgentsFirstAndPeopleLast(t *testing.T) {
	parts := []coordParticipantView{
		{ID: "person:2", Label: "Philipp", Human: true},
		{ID: "person:1", Label: "Robin", Human: true, Current: true},
		{ID: "claude:a", Label: "A"},
		{ID: "claude:b", Label: "B", AskURL: "/x"},
	}
	var got []string
	for _, p := range orderParticipantsHere(parts) {
		got = append(got, p.ID)
	}
	if strings.Join(got, ",") != "claude:b,claude:a,person:1,person:2" {
		t.Errorf("order = %v", got)
	}
}

func TestCoordPeerOnlyIsAgentToAgentWithoutIntent(t *testing.T) {
	for name, c := range map[string]struct {
		kind, intent string
		mentions     []string
		want         bool
	}{
		"agent to agent":    {store.AuthorAgent, "", []string{"claude:b"}, true},
		"to a person":       {store.AuthorAgent, "", []string{"person:1"}, false},
		"mixed":             {store.AuthorAgent, "", []string{"claude:b", "person:1"}, false},
		"a question":        {store.AuthorAgent, store.IntentQuestion, []string{"claude:b"}, false},
		"to the whole room": {store.AuthorAgent, "", nil, false},
		"a person writes":   {store.AuthorHuman, "", []string{"claude:b"}, false},
	} {
		if got := coordPeerOnly(c.kind, c.intent, c.mentions); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestExplainLeadIsTheFirstSentence(t *testing.T) {
	var none *explainView
	if none.RoomLead() != "" {
		t.Error("nil has no lead")
	}
	for in, want := range map[string]string{
		"Erst das. Dann das.":              "Erst das.",
		"Ohne Punkt":                       "Ohne Punkt",
		"Version 4.5 ist zu schwach. Rest": "Version 4.5 ist zu schwach.",
	} {
		if got := (&explainView{Text: in}).RoomLead(); got != want {
			t.Errorf("lead(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCoordRoomShowsWhoIsHereWithNoteAndBranchWithoutRightColumn(t *testing.T) {
	srv, st, room, get := chatFixture(t)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "claude:mac:1", PrincipalID: "person:1", Person: "robin", Machine: "mac", Branch: "feat/room",
		Provider: "claude", RoomKey: room,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetCoordWorkNote("claude:mac:1", "person:1", "Raum neu bauen"); err != nil {
		t.Fatal(err)
	}
	_ = srv
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	here := segment(t, page, `class="coord-participants rm-here clay"`, "</ul>")
	for _, want := range []string{"Raum neu bauen", "feat/room", `class="pp-title"`} {
		if !strings.Contains(here, want) {
			t.Errorf("who is here lacks %q: %s", want, here)
		}
	}
	for _, gone := range []string{`coord-attention-list`, `coord-drawer-toggle`, `coord-people`} {
		if strings.Contains(page, gone) {
			t.Errorf("the right column is gone, found %s", gone)
		}
	}
	// The sender line names the addressee in plain words.
	appendAgent(t, st, room, "m1", "an alle", "")
	page = get("/ui/coord?room=" + url.QueryEscape(room))
	if !strings.Contains(messageItem(t, page, "1"), `<span class="rm-to">to everyone</span>`) {
		t.Error("the message head names the addressee")
	}
}

func TestCoordAgentNameWithASlashIsNotTakenForAnID(t *testing.T) {
	_, st, room, get := chatFixture(t)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{
		ExternalID: "claude:mac:2", PrincipalID: "person:1", Person: "robin", Machine: "mac", Branch: "feat/room",
		Provider: "claude", RoomKey: room,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mac:2", ClientID: "s1", Body: "hallo"}); err != nil {
		t.Fatal(err)
	}
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	if !strings.Contains(messageItem(t, page, "1"), "feat/room") {
		t.Errorf("the sender keeps its label with the branch: %s", messageItem(t, page, "1"))
	}
}

func TestCoordEmptyRoomOffersTheActionNotAPickerOrDeadEnd(t *testing.T) {
	_, _, room, get := chatFixture(t)
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	empty := segment(t, page, `<li class="coord-empty">`, "</li>")
	if !strings.Contains(empty, "Nothing here yet.") || !strings.Contains(empty, `href="#coord-message-body"`) || !strings.Contains(empty, "Tell an agent something") {
		t.Errorf("empty room: %s", empty)
	}
	if !strings.Contains(page, `id="rm-here-title"`) {
		t.Error("who is here shows in an empty room")
	}
}

func TestCoordThreadComposerOffersTheAgentsChip(t *testing.T) {
	srv, st, room, _ := chatFixture(t)
	client := loginInteractive(t, srv, st, "robin")
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:two", PrincipalID: "person:1", Person: "robin", Provider: "claude", DisplayName: "two@box", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	res := authenticatedPostForm(t, client, srv.URL+"/ui/coord/thread/create", url.Values{"room": {room}, "title": {"T"}, "question": {"Q"}})
	res.Body.Close()
	loc := res.Header.Get("Location")
	if loc == "" {
		t.Fatalf("thread create status %d", res.StatusCode)
	}
	page := coordPageBody(t, client, srv.URL+loc)
	form := segment(t, page, `class="coord-thread-composer"`, "</form>")
	if !strings.Contains(form, `type="checkbox" name="mentions" value="all-agents"`) {
		t.Errorf("thread composer lacks the @agents chip: %s", form)
	}
}

func TestCoordRoomRendersInGermanWithoutFallbackKeys(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddAccount("robin", "", true); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, WithLanguage("de")))
	t.Cleanup(srv.Close)
	client := loginInteractive(t, srv, st, "robin")
	room := store.RoomKeyForProject("github.com/x/chat")
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:chat", PrincipalID: "person:1", Person: "robin", Provider: "claude", DisplayName: "claude@apps-vm", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	appendAgent(t, st, room, "m1", "frage", store.IntentQuestion, "person:1")
	page := coordPageBody(t, client, srv.URL+"/ui/coord?room="+url.QueryEscape(room))
	for _, want := range []string{"Wer ist da", "Agenten schreiben", "Alle im Raum", "Als Bitte senden", ">Senden<", "an robin"} {
		if !strings.Contains(page, want) {
			t.Errorf("German room lacks %q", want)
		}
	}
	for _, key := range []string{"room.who", "room.tell", "room.compose.to", "room.send_ask", "coord.to_all", "coord.to_agents"} {
		if strings.Contains(page, key) {
			t.Errorf("catalog key %s shows raw", key)
		}
	}
}

func TestCoordRoomActionsFollowTheViewersRights(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	// A guest of the organisation may not enter the room: the page carries nothing of it.
	status, page := fetchPage(t, e.Guest, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if status != http.StatusForbidden {
		t.Fatalf("guest status = %d, want 403", status)
	}
	for _, gone := range []string{`id="rm-here-title"`, `rm-compose`, "Roles first", "rm-here-act"} {
		if strings.Contains(page, gone) {
			t.Errorf("the refusal page carries %q", gone)
		}
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	for _, want := range []string{`href="#coord-message-body"`, `id="rm-here-title"`, `class="coord-composer rm-compose"`} {
		if !strings.Contains(owner, want) {
			t.Errorf("someone who may post lacks %q", want)
		}
	}
}
