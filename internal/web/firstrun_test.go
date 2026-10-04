package web

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A person with no role in a project still sees the agent they started there,
// named for people, and never one of somebody else's.
func TestAgentsPageShowsTheViewersOwnAgentWithoutAProjectRole(t *testing.T) {
	e := ovEnv(t)
	// nora belongs to the organization but holds no role in any project.
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	org, _ := e.St.CreateOrg("person:1", "Beta", "beta")
	code, _, _ := e.St.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := e.St.AcceptInvitation("person:6", code); err != nil {
		t.Fatal(err)
	}
	nora := loginInteractive(t, e.Srv, e.St, "nora")
	room := store.RoomKeyForProject(shellProject)
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:nora-laptop:aaaaaaaa-1111", PrincipalID: "person:6", Provider: "claude",
		DisplayName: "claude:nora-laptop:aaaaaaaa-1111", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "cli:alices-box", PrincipalID: "person:1", Provider: "ctx-cli",
		DisplayName: "cli:alices-box", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, nora, e.Base+"/ui/agents")
	if !strings.Contains(page, "Claude · nora · nora-laptop") {
		t.Fatalf("own agent missing or unreadable: %s", page)
	}
	if !strings.Contains(page, `title="claude:nora-laptop:aaaaaaaa-1111"`) {
		t.Error("the raw ID should be in a title")
	}
	if strings.Contains(page, "alices-box") || strings.Contains(page, "CLI ·") {
		t.Error("somebody else's agent is listed")
	}
	_, ov := fetchPage(t, nora, e.Base+"/ui/overview")
	if !strings.Contains(ov, "Claude · nora · nora-laptop") {
		t.Error("the overview lacks the own agent")
	}
}

func TestAgentNamesAreReadable(t *testing.T) {
	cases := []struct {
		agent store.CoordAgent
		want  string
	}{
		{store.CoordAgent{Owner: "anna", ExternalID: "claude:freund-laptop:aaaaaaaa-1111", Provider: "claude", DisplayName: "claude:freund-laptop:aaaaaaaa-1111"}, "Claude · anna · freund-laptop"},
		{store.CoordAgent{Owner: "anna", ExternalID: "cli:freund-laptop", Provider: "ctx-cli", DisplayName: "cli:freund-laptop"}, "CLI · anna · freund-laptop"},
		{store.CoordAgent{Owner: "anna", ExternalID: "claude:h:uuid", Provider: "claude", DisplayName: "claude"}, "Claude · anna · h"},
		{store.CoordAgent{Owner: "anna", ExternalID: "codex:h:uuid", Provider: "codex"}, "Codex · anna · h"},
		{store.CoordAgent{Owner: "anna", ExternalID: "claude:h:uuid", Provider: "claude", DisplayName: "reviewer-bot"}, "reviewer-bot"},
		// Session UUIDs are no names, spelled with or without a prefix.
		{store.CoordAgent{Owner: "Robin", ExternalID: "11111111-aaaa-4bbb-8ccc-000000000001", Provider: "claude", DisplayName: "11111111-aaaa-4bbb-8ccc-000000000001"}, "Claude · Robin"},
		{store.CoordAgent{Owner: "Robin", ExternalID: "x", Provider: "claude", DisplayName: "11111111-aaaa-4bbb-8ccc-000000000001"}, "Claude · Robin"},
		{store.CoordAgent{ExternalID: "claude:5b1a2c3d", Provider: "unknown", DisplayName: "claude:5b1a2c3d"}, "Claude"},
		{store.CoordAgent{Owner: "Robin", ExternalID: "derived:mainex:4242", Provider: "unidentified-harness", DisplayName: "derived:mainex:4242"}, "Agent · Robin · mainex"},
		{store.CoordAgent{Owner: "Robin", ExternalID: "abc", Provider: "claude", DisplayName: "0123456789abcdef0123456789"}, "Claude · Robin"},
		{store.CoordAgent{Owner: "Robin", ExternalID: "abc", Provider: "weird", DisplayName: ""}, "Weird · Robin"},
		// A name chosen by a person stays, with or without an owner.
		{store.CoordAgent{Owner: "Robin", ExternalID: "abc", Provider: "claude", DisplayName: "Docs reviewer"}, "Docs reviewer"},
	}
	for _, c := range cases {
		if got := peerName(c.agent); got != c.want {
			t.Errorf("peerName(%s) = %q, want %q", c.agent.ExternalID, got, c.want)
		}
	}
}

// A message sent from an agent is not a sign of life; a poll is.
func TestAgentIsActiveOnlyWithASignOfLife(t *testing.T) {
	e := ovEnv(t)
	ovAgent(t, e.St, shellProject, "claude:lap:quiet", "quiet", "person:2")
	if _, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: store.RoomKeyForProject(shellProject),
		SenderExternalID: "claude:lap:quiet", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorAgent, ClientID: "c-q", Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/agents")
	if strings.Contains(page, "is-active") && strings.Contains(page, `ag-card is-active`) {
		t.Errorf("an agent that only posted a message counts as active: %s", page)
	}
	if !strings.Contains(page, `ag-card is-idle`) {
		t.Error("a recently registered silent agent should be idle")
	}
}

// Once a machine signed in, the overview says so and names the next step.
func TestOverviewShowsConnectedMachinesAndTheNextStep(t *testing.T) {
	e := ovEnv(t)
	if _, _, err := e.St.CreateDeviceToken("person:2", "anna-laptop"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/overview")
	for _, want := range []string{"anna-laptop", "Connected", "Start Claude or Codex in a project", "claude"} {
		if !strings.Contains(page, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	if strings.Contains(page, "No agents yet.") {
		t.Error("a connected machine still reads as no agents")
	}
	_, agents := fetchPage(t, e.Member, e.Base+"/ui/agents")
	if !strings.Contains(agents, "anna-laptop") || !strings.Contains(agents, "Start Claude or Codex in a project") {
		t.Error("the agents page lacks the machine or the next step")
	}
	// Somebody else's machine never shows.
	if _, _, err := e.St.CreateDeviceToken("person:1", "alice-box"); err != nil {
		t.Fatal(err)
	}
	_, again := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if strings.Contains(again, "alice-box") {
		t.Error("another person's machine is listed")
	}
	_, forced := fetchPage(t, e.Member, e.Base+"/ui/overview?connect=1")
	if !strings.Contains(forced, "anna-laptop is connected") || strings.Contains(forced, "Waiting for your machine") {
		t.Error("the connect page does not switch to connected")
	}
}

var knowledgeIDs = map[string]int64{}

func insertKnowledge(t *testing.T, st *store.Store, title, confidence string) {
	t.Helper()
	id, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "body", Scope: scope.Axes{Project: shellProject}, Person: "alice", Confidence: confidence})
	if err != nil {
		t.Fatal(err)
	}
	knowledgeIDs[title] = id
}

func knowledgeIDByTitle(t *testing.T, st *store.Store, title string) string {
	t.Helper()
	return strconv.FormatInt(knowledgeIDs[title], 10)
}

// Approve and Reject appear only where a decision is open.
func TestKnowledgeDecisionsOnlyWhereOneIsOpen(t *testing.T) {
	e := shellWebAll(t)
	insertKnowledge(t, e.St, "Settled", "trusted")
	insertKnowledge(t, e.St, "Confirmed", "verified")
	insertKnowledge(t, e.St, "Proposed", "staged")
	for title, open := range map[string]bool{"Settled": false, "Confirmed": false, "Proposed": true} {
		_, page := fetchPage(t, e.Owner, e.Base+"/ui/knowledge/"+knowledgeIDByTitle(t, e.St, title))
		if got := strings.Contains(page, "/approve"); got != open {
			t.Errorf("%s: approve shown = %v, want %v", title, got, open)
		}
		if !strings.Contains(page, "/reject") {
			t.Errorf("%s: an owner can always take an entry out of use", title)
		}
		if got := strings.Contains(page, ">Reject<"); got != open {
			t.Errorf("%s: Reject shown = %v, want %v", title, got, open)
		}
		if got := strings.Contains(page, ">Retire<"); got == open {
			t.Errorf("%s: Retire shown = %v, want %v", title, got, !open)
		}
		if !strings.Contains(page, `id="edit"`) {
			t.Errorf("%s: an owner should still be able to edit", title)
		}
	}
}

func TestKnowledgeStatusIsReadableAndExplainedInATitle(t *testing.T) {
	e := shellWebAll(t)
	ovKnowledge(t, e.St, shellProject, "Proposed", "staged")
	ovKnowledge(t, e.St, shellProject, "Settled", "trusted")
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/knowledge")
	for _, want := range []string{"Waiting for review", `class="kn-level" title="`, `class="kn-ico"`} {
		if !strings.Contains(page, want) {
			t.Errorf("knowledge list lacks %q", want)
		}
	}
	if strings.Contains(page, ">Staged<") || strings.Contains(page, "Staged\n") {
		t.Error("the raw status word is still shown")
	}
}

func TestContextPageHasEmptyStatesWithAnAction(t *testing.T) {
	e := shellWebAll(t)
	_, none := fetchPage(t, e.Owner, e.Base+"/ui/context")
	if !strings.Contains(none, "Pick a project") || !strings.Contains(none, `href="?project=github.com%2fx%2fshell"`) {
		t.Errorf("no project chosen: %s", none)
	}
	_, empty := fetchPage(t, e.Owner, e.Base+"/ui/context?project="+shellProject)
	if !strings.Contains(empty, "Nothing is given to agents yet.") || !strings.Contains(empty, "preview=1") {
		t.Error("an empty context lacks its line or its action")
	}
	if _, err := e.St.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Settled", Body: "body", Scope: scope.Axes{Project: shellProject}, Person: "alice", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	_, full := fetchPage(t, e.Owner, e.Base+"/ui/context?project="+shellProject)
	if !strings.Contains(full, "Settled") || strings.Contains(full, "Canonical project") {
		t.Error("the delivered context is missing or the old text field is back")
	}
}

// "Guest" next to the viewer's own agent tells them nothing; next to anyone
// else's it still does.
func TestGuestChipIsHiddenOnTheViewersOwnMessages(t *testing.T) {
	_, st, _, _ := roleWeb(t)
	if err := st.SetProjectRole("person:1", webRoleProject, "person:2", store.RoleGuest, false, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	room := store.RoomKeyForProject(webRoleProject)
	for _, id := range []string{"cli:mine", "cli:theirs"} {
		if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:2", Provider: "ctx-cli", DisplayName: id, RoomKey: room}); err != nil {
			t.Fatal(err)
		}
	}
	pres := []store.CoordMessagePresentation{
		{Message: store.CoordMessage{ID: 1, Sequence: 1, SenderExternalID: "cli:mine", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorAgent, Body: "mine"}},
		{Message: store.CoordMessage{ID: 2, Sequence: 2, SenderExternalID: "cli:theirs", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorAgent, Body: "theirs"}},
	}
	views := buildCoordMessageViews(pres, room, nil)
	applyMessageRoles(st, room, "person:2", views, pres)
	if views[0].SenderRole != "" || views[1].SenderRole != "" {
		t.Errorf("own guest messages carry a chip: %q %q", views[0].SenderRole, views[1].SenderRole)
	}
	views = buildCoordMessageViews(pres, room, nil)
	applyMessageRoles(st, room, "person:1", views, pres)
	if views[0].SenderRole != "guest" {
		t.Errorf("someone else's guest agent lost its chip: %q", views[0].SenderRole)
	}
}

func TestRoomsEmptyStateHasAnActionAndASingleRoomIsMarkedForMobile(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	nora := loginInteractive(t, e.Srv, e.St, "nora")
	_, empty := fetchPage(t, nora, e.Base+"/ui/coord")
	if !strings.Contains(empty, "No rooms yet.") || !strings.Contains(empty, `href="/ui/overview?connect=1"`) || !strings.Contains(empty, "Start Claude or Codex in a project") {
		t.Error("the empty rooms page lacks its line or its action")
	}
	if strings.Contains(empty, `data-sole-room="/`) {
		t.Error("an empty page offers a room to enter")
	}
	ovAgent(t, e.St, shellProject, "claude:h:one", "one", "person:2")
	_, single := fetchPage(t, e.Member, e.Base+"/ui/coord")
	if !strings.Contains(single, `data-sole-room="/ui/coord?room=`) {
		t.Error("a single room is not marked for the direct jump on small screens")
	}
}

func TestComposerModesExplainThemselves(t *testing.T) {
	e := ovEnv(t)
	ovAgent(t, e.St, shellProject, "claude:h:one", "one", "person:2")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(store.RoomKeyForProject(shellProject)))
	for _, want := range []string{">Message<", ">Standing order<", `title="Stays in force for the agents until ended"`, `title="Say something in the room"`} {
		if !strings.Contains(page, want) {
			t.Errorf("composer lacks %q", want)
		}
	}
	if strings.Contains(page, ">Directive<") {
		t.Error("the old mode name is still shown")
	}
}

func TestSessionsListDropsTheLinkedColumnWhenNothingIsLinked(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "A request", "post")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/sessions")
	if strings.Contains(page, "Request or knowledge") == !strings.Contains(page, `class="c-linked"`) {
		t.Errorf("the column header and its cells disagree")
	}
	if strings.Contains(page, ">Linked<") {
		t.Error("the unexplained column title is back")
	}
}

// A pure guest sees no agents (not even their own, on purpose), and what an
// @-name they wrote did stays invisible: it must not tell a real hidden member
// from an invented one.
func TestPureGuestSeesNoAgentsAndNoOracle(t *testing.T) {
	e := ovEnv(t)
	room := store.RoomKeyForProject(shellProject)
	ovAgent(t, e.St, shellProject, "claude:hidden-peer:bbbb", "hidden-peer", "person:1")
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:gina-box:aaaa", PrincipalID: "person:3", Provider: "claude",
		DisplayName: "claude:gina-box:aaaa", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{}
	for _, target := range []string{"claude:hidden-peer:bbbb", "claude:invented:zzzz"} {
		guest := e.St.CoordinationFor(store.Principal{ID: "person:3", Label: "gina"}, "claude:gina-box:aaaa")
		_, _ = guest.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
			ClientID: "q-" + target, Body: "ping", Intent: store.IntentQuestion, Mentions: []string{target}})
		_, page := fetchPage(t, e.Guest, e.Base+"/ui/agents")
		pages[target] = stripCSRF(page)
	}
	// Deliberate: a pure guest sees no agents at all, not even their own.
	if pages["claude:hidden-peer:bbbb"] != pages["claude:invented:zzzz"] {
		t.Error("the guest page differs between a real hidden member and an invented name")
	}
	if strings.Contains(pages["claude:hidden-peer:bbbb"], "gina-box") {
		t.Errorf("a pure guest unexpectedly sees their own agent: %s", pages["claude:hidden-peer:bbbb"])
	}
	if strings.Contains(pages["claude:hidden-peer:bbbb"], "Waiting for") || strings.Contains(pages["claude:hidden-peer:bbbb"], "hidden-peer") {
		t.Errorf("a wait or a hidden agent shows: %s", pages["claude:hidden-peer:bbbb"])
	}
}

// An organization member without a project role sees their own agent there,
// but never a wait derived from whom they wrote to.
func TestOwnAgentWithoutRoleShowsNoWaitState(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	org, _ := e.St.CreateOrg("person:1", "Beta", "beta")
	code, _, _ := e.St.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := e.St.AcceptInvitation("person:6", code); err != nil {
		t.Fatal(err)
	}
	nora := loginInteractive(t, e.Srv, e.St, "nora")
	room := store.RoomKeyForProject(shellProject)
	ovAgent(t, e.St, shellProject, "claude:hidden-peer:bbbb", "hidden-peer", "person:1")
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:nora-laptop:aaaa", PrincipalID: "person:6", Provider: "claude",
		DisplayName: "claude:nora-laptop:aaaa", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	me := e.St.CoordinationFor(store.Principal{ID: "person:6", Label: "nora"}, "claude:nora-laptop:aaaa")
	_, _ = me.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
		ClientID: "q1", Body: "ping", Intent: store.IntentQuestion, Mentions: []string{"claude:hidden-peer:bbbb"}})
	_, page := fetchPage(t, nora, e.Base+"/ui/agents")
	if !strings.Contains(page, "Claude · nora · nora-laptop") {
		t.Fatalf("own agent missing: %s", page)
	}
	if strings.Contains(page, "Waiting for") {
		t.Errorf("a derived wait shows for an agent in a room the viewer may not list")
	}
}

func TestDecidedKnowledgeCanBeRetiredAndUndone(t *testing.T) {
	e := ovEnv(t)
	insertKnowledge(t, e.St, "Settled", "trusted")
	id := knowledgeIDByTitle(t, e.St, "Settled")
	// People without the right to edit see no such action.
	if _, other := fetchPage(t, e.Guest, e.Base+"/ui/knowledge/"+id); strings.Contains(other, "/reject") {
		t.Errorf("a guest is offered Retire: %s", strings.SplitAfter(other, "/reject")[0][len(strings.SplitAfter(other, "/reject")[0])-200:])
	}
	resp := knPost(t, e.Owner, e.Base, "/ui/knowledge/"+id, "/ui/review/"+id+"/reject", url.Values{"next": {"item"}})
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "done=retired") {
		t.Fatalf("redirect %q does not say the entry was retired (status %d)", loc, resp.StatusCode)
	}
	_, after := fetchPage(t, e.Owner, e.Base+loc)
	if !strings.Contains(after, "Retired: Settled") || !strings.Contains(after, ">Undo<") {
		t.Errorf("retiring does not read as retiring, or cannot be undone: %s", after)
	}
}
