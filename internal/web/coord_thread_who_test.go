package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func twAgent(id, provider, label, machine, branch, seen string) coordParticipantView {
	return coordParticipantView{ID: id, Provider: provider, Label: label, Person: "robin", Machine: machine, Branch: branch, LastSeen: seen, Handle: "h-" + id}
}

func twRoom() []coordParticipantView {
	return []coordParticipantView{
		{ID: "person:1", Label: "Robin", Human: true, Current: true},
		{ID: "person:2", Label: "Anna", Human: true, Handle: "anna"},
		twAgent("claude:a", "claude", "Claude · Robin · mac · feat/a", "mac", "feat/a", "2026-11-17T10:00:00Z"),
		twAgent("claude:b", "claude", "Claude · Robin · box · feat/b", "box", "feat/b", "2026-11-17T10:05:00Z"),
		twAgent("claude:idle", "claude", "Claude · Robin · idle", "idle", "main", "2026-11-17T11:00:00Z"),
		twAgent("codex:c", "codex", "Codex · Robin", "", "", "2026-11-17T09:00:00Z"),
		twAgent("cli:x", "cli", "CLI · Robin · mac", "mac", "", "2026-11-17T09:30:00Z"),
	}
}

func twAuthors() []store.ThreadAuthor {
	return []store.ThreadAuthor{
		{Kind: store.AuthorHuman, PrincipalID: "person:1", Messages: 1},
		{Kind: store.AuthorAgent, PrincipalID: "person:1", SenderID: "claude:a", Messages: 2},
		{Kind: store.AuthorHuman, PrincipalID: "person:2", Messages: 1},
		{Kind: store.AuthorAgent, PrincipalID: "person:1", SenderID: "claude:b", Messages: 1},
		{Kind: store.AuthorAgent, PrincipalID: "person:1", SenderID: "codex:c", Messages: 1},
	}
}

// AC-701.2: one entry per person and one per agent with its person; the sessions
// of one agent on several machines and branches are one entry, and a tool of the
// room that wrote nothing in the thread is none.
func TestBuildThreadWhoIsOneEntryPerPersonAndAgent(t *testing.T) {
	who := buildThreadWho(langEN, twAuthors(), twRoom(), nil)
	var names []string
	for _, h := range who {
		if h.Human {
			names = append(names, h.Person.Label)
		} else {
			names = append(names, h.Title)
		}
	}
	if got := strings.Join(names, ","); got != "Robin,Anna,Claude,Codex" {
		t.Fatalf("who = %s, want people first and each agent once", got)
	}
	if who[0].Sub != "You" {
		t.Errorf("the viewer is marked: %q", who[0].Sub)
	}
	claude := who[2]
	if claude.Count != 2 || claude.Sub != "Agent of Robin" {
		t.Errorf("Claude = count %d sub %q, want two sessions of Robin's agent", claude.Count, claude.Sub)
	}
	for _, want := range []string{"mac", "box", "feat/a", "feat/b", "2 sessions"} {
		if !strings.Contains(claude.Tip, want) {
			t.Errorf("the tooltip of Claude lacks %q: %q", want, claude.Tip)
		}
	}
	for _, h := range who {
		if h.Title == "CLI" {
			t.Error("a tool that wrote nothing in the thread is not a participant")
		}
	}
}

// An author the room no longer lists still counts, named by what is known.
func TestBuildThreadWhoKeepsAnAuthorTheRoomNoLongerLists(t *testing.T) {
	authors := []store.ThreadAuthor{{Kind: store.AuthorAgent, PrincipalID: "person:1", SenderID: "codex:gone"}}
	who := buildThreadWho(langEN, authors, twRoom(), map[string]string{"codex:gone": "Codex · Robin · old", "person:1": "Robin"})
	if len(who) != 1 || who[0].Title != "Codex" || who[0].Sub != "Agent of Robin" {
		t.Fatalf("who = %+v", who)
	}
}

// An agent whose only name is its owner's is called an agent, not "robin · robin".
func TestThreadTitleNamesAnAgentByItsToolNotByItsOwner(t *testing.T) {
	p := coordParticipantView{ID: "agent:x", Label: "robin", Person: "robin"}
	if got := threadTitle(langEN, p); got != "Agent" {
		t.Errorf("title = %q", got)
	}
	if got := agentLabelOf(langEN, p); got != "Agent · robin" {
		t.Errorf("label = %q", got)
	}
}

// The composer offers the ones of the thread first and an agent once; the session
// being answered stands for its agent, else the one seen last.
func TestThreadAddresseesAreOneAgentEachAndTheThreadFirst(t *testing.T) {
	room := twRoom()
	who := buildThreadWho(langEN, twAuthors(), room, nil)
	ids := func(list []coordParticipantView) string {
		var out []string
		for _, p := range list {
			out = append(out, p.ID)
		}
		return strings.Join(out, ",")
	}
	got := threadAddressees(langEN, who, room, "")
	// Anna; then Claude (the session seen last), Codex, and from the rest of the room the CLI.
	if want := "person:2,claude:idle,codex:c,cli:x"; ids(got) != want {
		t.Fatalf("addressees = %s, want %s", ids(got), want)
	}
	if got[1].Label != "Claude · Robin" {
		t.Errorf("an agent is named by tool and person: %q", got[1].Label)
	}
	if got := threadAddresseesIDs(langEN, who, room, "claude:a"); got != "person:2,claude:a,codex:c,cli:x" {
		t.Errorf("the session being answered stands for its agent: %s", got)
	}
}

func threadAddresseesIDs(l lang, who []coordHereView, room []coordParticipantView, preferred string) string {
	var out []string
	for _, p := range threadAddressees(l, who, room, preferred) {
		out = append(out, p.ID)
	}
	return strings.Join(out, ",")
}

// AC-701.3: waiting is told in a sentence, for two different participants only; sessions of
// one agent waiting on each other are no waiting on each other.
func TestBuildThreadWaitLines(t *testing.T) {
	room := twRoom()
	since := time.Now().UTC().Format(time.RFC3339)
	cycle := func(members ...string) []store.WaitCycle { return []store.WaitCycle{{Members: members, Since: since}} }

	if got := buildThreadWaitLines(langEN, cycle("claude:a", "claude:b"), room, nil); len(got) != 0 {
		t.Fatalf("two sessions of one agent are no waiting on each other: %v", got)
	}
	// The room list drops a session that registered twice under one label (dedupeAgents): it is
	// still the one agent, however it is found.
	var withoutB []coordParticipantView
	for _, p := range room {
		if p.ID != "claude:b" {
			withoutB = append(withoutB, p)
		}
	}
	labels := map[string]string{"claude:b": "Claude · Robin · box · feat/b"}
	if got := buildThreadWaitLines(langEN, cycle("claude:a", "claude:b"), withoutB, labels); len(got) != 0 {
		t.Fatalf("a session the room list dropped waits on its own agent: %v", got)
	}
	got := buildThreadWaitLines(langEN, cycle("claude:a", "codex:c"), room, nil)
	re := regexp.MustCompile(`^Claude \(Robin\) and Codex \(Robin\) are waiting on each other · since \d\d:\d\d$`)
	if len(got) != 1 || !re.MatchString(got[0]) {
		t.Fatalf("sentence = %q", got)
	}
	// Three sessions of two agents are two agents.
	if got := buildThreadWaitLines(langEN, cycle("claude:a", "claude:b", "codex:c"), room, nil); len(got) != 1 || !re.MatchString(got[0]) {
		t.Fatalf("three sessions, two agents: %q", got)
	}
	de := buildThreadWaitLines(langDE, cycle("claude:a", "codex:c"), room, nil)
	if len(de) != 1 || !strings.Contains(de[0], "Claude (Robin) und Codex (Robin) warten aufeinander · seit ") {
		t.Fatalf("German sentence = %q", de)
	}
	for _, line := range append(got, de...) {
		if strings.Contains(line, "mutual wait") || strings.Contains(line, "review") {
			t.Errorf("debug text in %q", line)
		}
	}
}

func TestCoordShortWhen(t *testing.T) {
	now := time.Now()
	if got := coordShortWhen(now.UTC().Format(time.RFC3339)); got != now.Format("15:04") {
		t.Errorf("today = %q, want the time", got)
	}
	old := time.Date(2026, 1, 2, 9, 30, 0, 0, time.Local)
	if got := coordShortWhen(old.UTC().Format(time.RFC3339)); got != "02.01. 09:30" {
		t.Errorf("another day = %q", got)
	}
}

// threadPageFixture: robin in a project room with sessions of one Claude on two
// machines, a Codex, a CLI that never writes, and a second person who writes.
func threadPageFixture(t *testing.T) (st *store.Store, room string, thread int64, page func() string) {
	t.Helper()
	srv, st, room, _ := chatFixture(t)
	client := loginInteractive(t, srv, st, "robin")
	reg := func(id, provider, name, machine, branch string) {
		if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:1", Person: "robin", Provider: provider, DisplayName: name, Machine: machine, Branch: branch, RoomKey: room}); err != nil {
			t.Fatal(err)
		}
	}
	reg("claude:mac:1", "claude", "Claude", "mac", "feat/a")
	reg("claude:box:2", "claude", "Claude", "box", "feat/b")
	reg("codex:one", "codex", "Codex", "mac", "")
	reg("cli:never", "cli", "CLI", "mac", "")
	if _, err := st.AddAccount("anna", "", false); err != nil {
		t.Fatal(err)
	}
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin", TokenKind: store.WebSessionKind}, "")
	id, err := access.CreateTaskThreadInRoom(room, "The thread", "What do we draw?", "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	say := func(sender, kind, author, body string) {
		n++
		m := store.CoordMessage{DestinationKind: store.DestinationDiscussion, DestinationID: store.ThreadDestinationID(id), SenderExternalID: sender, AuthorPrincipalID: author, AuthorKind: kind, ClientID: "m" + string(rune('a'+n)), Body: body}
		if _, err := st.AppendCoordMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	say("claude:mac:1", store.AuthorAgent, "person:1", "one")
	say("person:2", store.AuthorHuman, "person:2", "two")
	say("claude:box:2", store.AuthorAgent, "person:1", "three")
	say("codex:one", store.AuthorAgent, "person:1", "four")
	return st, room, id, func() string { return coordPageBody(t, client, srv.URL+coordThreadURL(room, id)) }
}

// AC-701.2 on the page: who wrote, each once; no session, no machine, no tool that stayed silent.
func TestCoordThreadPageListsWhoWroteOnce(t *testing.T) {
	_, _, _, page := threadPageFixture(t)
	html := page()
	who := segment(t, html, `class="coord-participants rm-here"`, "</ul>")
	if got := strings.Count(who, `rm-here-item is-human`); got != 2 {
		t.Errorf("people = %d, want robin and anna: %s", got, who)
	}
	if got := strings.Count(who, `rm-here-item is-agent`); got != 2 {
		t.Errorf("agents = %d, want one Claude and one Codex: %s", got, who)
	}
	for _, gone := range []string{">CLI<", "claude:mac:1", "codex:one", "cli:never"} {
		if strings.Contains(who, gone) {
			t.Errorf("the list shows %q", gone)
		}
	}
	// Machines and branches are the tooltip.
	if !strings.Contains(who, `title="`) || !strings.Contains(who, "feat/a") || !strings.Contains(who, "feat/b") || !strings.Contains(who, "2 sessions") {
		t.Errorf("the tooltip of the agent lacks machine, branch and sessions: %s", who)
	}
	// The old column is gone with its raw lists.
	for _, gone := range []string{`class="rtside`, `coord-wait-cycles`, "mutual wait"} {
		if strings.Contains(html, gone) {
			t.Errorf("the thread still carries %q", gone)
		}
	}
}

// AC-701.4: the composer shows the text field and one send button; to whom, what kind and the
// status are behind one button, and closed.
func TestCoordThreadComposerShowsOnlyTheFieldAndSendUntilOpened(t *testing.T) {
	_, _, _, page := threadPageFixture(t)
	form := segment(t, page(), `class="coord-thread-composer"`, "</form>")
	if strings.Count(form, "comp-send-message") != 1 {
		t.Errorf("one send button wanted: %s", form)
	}
	opts := segment(t, form, `<details class="dd top-menu cx-opts"`, "</details>")
	if strings.Contains(strings.SplitN(opts, ">", 2)[0], " open") {
		t.Error("the options stand open")
	}
	for _, want := range []string{`name="state"`, `name="mentions"`, `name="intent"`, `data-needs-to`, `data-val="all-agents"`, `data-wtab="p"`} {
		if !strings.Contains(opts, want) {
			t.Errorf("behind the button lacks %q", want)
		}
	}
	rest := strings.Replace(form, opts, "", 1)
	for _, gone := range []string{`name="state"`, `name="mentions"`, `name="intent"`, `data-pick`, `rm-ask`} {
		if strings.Contains(rest, gone) {
			t.Errorf("%q stands out in the open", gone)
		}
	}
	// To: one Claude, however many sessions it has.
	if got := strings.Count(opts, `data-label="Claude · `); got != 1 {
		t.Errorf("Claude is offered %d times, want once: %s", got, opts)
	}
	sugg := segment(t, form, `class="comp-suggest"`, "</ul>")
	if got := strings.Count(sugg, ">Claude · "); got != 1 {
		t.Errorf("Claude is suggested %d times, want once: %s", got, sugg)
	}
}

// AC-701.3 on the page: a real waiting of two agents is one sentence in the language of the page; the
// sessions of one agent waiting on each other say nothing.
func TestCoordThreadPageSaysWhoWaitsOnWhomInOneSentence(t *testing.T) {
	st, _, id, page := threadPageFixture(t)
	ask := func(from, to string) {
		acc := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, from)
		if _, err := acc.ThreadPost(id, store.CoordMessage{ClientID: "w-" + from + to, Body: "ping", Intent: store.IntentQuestion, Mentions: []string{to}}); err != nil {
			t.Fatal(err)
		}
	}
	ask("claude:mac:1", "claude:box:2")
	ask("claude:box:2", "claude:mac:1")
	if strings.Contains(page(), "coord-thread-wait") {
		t.Fatal("two sessions of one agent waiting on each other is no sentence")
	}
	ask("claude:mac:1", "codex:one")
	ask("codex:one", "claude:mac:1")
	html := page()
	line := segment(t, html, `<li class="coord-thread-wait"`, "</li>")
	if !strings.Contains(line, "Claude (robin) and Codex (robin) are waiting on each other · since ") {
		t.Errorf("sentence = %s", line)
	}
	if strings.Contains(html, "mutual wait") {
		t.Error("the debug text is on the page")
	}
}

// The status of a thread is changed in the head, with a real form for each of the states.
func TestCoordThreadHeadOffersTheStatusAsAMenu(t *testing.T) {
	_, _, _, page := threadPageFixture(t)
	html := page()
	menu := segment(t, html, `<details class="dd top-menu rtstate"`, "</details>")
	if !strings.Contains(menu, `action="/ui/coord/thread/state"`) {
		t.Errorf("the status menu does not post to the state route: %s", menu)
	}
	for _, v := range []string{"open", "resolved", "deferred"} {
		if !strings.Contains(menu, `name="state" value="`+v+`"`) {
			t.Errorf("status %s is not offered", v)
		}
	}
	// The guiding question stands in the head, closed.
	lead := segment(t, html, `<details class="rlead"`, "</details>")
	if strings.Contains(strings.SplitN(lead, ">", 2)[0], " open") || !strings.Contains(lead, "What do we draw?") {
		t.Errorf("the guiding question: %s", lead)
	}
}

// The thread page speaks the language of the interface: the sentence is German on a German page.
func TestCoordThreadPageSaysItInGerman(t *testing.T) {
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
	for _, a := range []struct{ id, provider, name string }{{"claude:one", "claude", "Claude"}, {"codex:one", "codex", "Codex"}} {
		if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: a.id, PrincipalID: "person:1", Person: "robin", Provider: a.provider, DisplayName: a.name, RoomKey: room}); err != nil {
			t.Fatal(err)
		}
	}
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin", TokenKind: store.WebSessionKind}, "")
	id, err := access.CreateTaskThreadInRoom(room, "Faden", "Frage?", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]string{{"claude:one", "codex:one"}, {"codex:one", "claude:one"}} {
		acc := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, p[0])
		if _, err := acc.ThreadPost(id, store.CoordMessage{ClientID: "w-" + p[0], Body: "ping", Intent: store.IntentQuestion, Mentions: []string{p[1]}}); err != nil {
			t.Fatal(err)
		}
	}
	html := coordPageBody(t, client, srv.URL+coordThreadURL(room, id))
	line := segment(t, html, `<li class="coord-thread-wait"`, "</li>")
	if !strings.Contains(line, "warten aufeinander · seit ") || strings.Contains(html, "mutual wait") {
		t.Errorf("German sentence = %s", line)
	}
	for _, want := range []string{">Beteiligte<", ">Leitfrage<"} {
		if !strings.Contains(html, want) {
			t.Errorf("German thread lacks %q", want)
		}
	}
}
