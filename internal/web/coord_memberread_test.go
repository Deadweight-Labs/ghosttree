package web

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// memberReadSeed puts an agent of person:2 with one post into the shell project
// and one into the hidden project. Neither owner, lead nor reviewer has an agent.
func memberReadSeed(t *testing.T, e shellEnv) (room, hiddenRoom string) {
	t.Helper()
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "Roles first", "migrating the roles table")
	agentsSeedFor(t, e.St, "person:1", shellHiddenProject, "claude:secret-host:bbbb", "sess-h", "SECRET-REQ", "SECRET-POST")
	return store.RoomKeyForProject(shellProject), store.RoomKeyForProject(shellHiddenProject)
}

var regexpTabOn = regexp.MustCompile(`<a class="rtab on"[^>]*>.*?</a>`)

func postRoom(t *testing.T, e shellEnv, c *http.Client, path, room string, extra url.Values) int {
	t.Helper()
	form := url.Values{"room": {room}, "csrf_token": {renderedCSRFToken(t, c, e.Base+"/ui/coord?room="+url.QueryEscape(room))}}
	for k, v := range extra {
		form[k] = v
	}
	resp := sameOriginPostForm(t, c, e.Base+path, form)
	resp.Body.Close()
	return resp.StatusCode
}

func TestProjectRoomIsReadByOwnerLeadReviewerWithoutOwnAgent(t *testing.T) {
	for name, enforce := range map[string]bool{"enforced": true, "log mode": false} {
		t.Run(name, func(t *testing.T) {
			e := shellWebAll(t)
			e.St.SetAccessMode(store.AccessMode{Enforce: enforce})
			room, _ := memberReadSeed(t, e)
			for who, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "reviewer": e.Reviewer, "member": e.Member} {
				status, page := fetchPage(t, c, e.Base+"/ui/coord?room="+url.QueryEscape(room))
				if status != 200 || !strings.Contains(page, "migrating the roles table") {
					t.Errorf("%s: status %d, room unreadable", who, status)
				}
			}
			status, page := fetchPage(t, e.Guest, e.Base+"/ui/coord?room="+url.QueryEscape(room))
			if status != http.StatusForbidden || strings.Contains(page, "migrating the roles table") {
				t.Errorf("guest status %d", status)
			}
		})
	}
}

// A refused room is exactly one answer: the same status and bytes for a room
// that does not exist and for one in a project the viewer may not see (#2447).
func TestProjectRoomStaysClosedToStrangersAndOtherProjects(t *testing.T) {
	e := ovEnv(t)
	room, hidden := memberReadSeed(t, e)
	if _, err := e.St.AddPerson("sven"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "sven")
	cases := []struct {
		who  string
		c    *http.Client
		room string
	}{{"stranger", stranger, room}, {"stranger in hidden", stranger, hidden}, {"member of project A in room B", e.Member, hidden}, {"lead of project A in room B", e.Lead, hidden}}
	for _, tc := range cases {
		status, page := fetchPage(t, tc.c, e.Base+"/ui/coord?room="+url.QueryEscape(tc.room))
		if status != http.StatusNotFound || strings.Contains(page, "migrating") || strings.Contains(page, "SECRET") {
			t.Errorf("%s: status %d", tc.who, status)
		}
		missStatus, missing := fetchPage(t, tc.c, e.Base+"/ui/coord?room="+url.QueryEscape("project:no/such-room"))
		if missStatus != http.StatusNotFound || page != missing {
			t.Errorf("%s: hidden room answer differs from a missing room (%d vs %d)", tc.who, status, missStatus)
		}
		if !strings.Contains(page, messages["coord.notfound.title"]) || strings.Contains(page, "coordination target not found") {
			t.Errorf("%s: not the designed not-found page", tc.who)
		}
	}
	// Writing is closed the same way.
	form := url.Values{"room": {hidden}, "body": {"hi"}, "csrf_token": {renderedCSRFToken(t, e.Member, e.Base+"/ui/overview")}}
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/coord/send", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("post into a hidden room: %d", resp.StatusCode)
	}
}

func TestRefusedRoomPagesAreDesignedAndLeadSomewhere(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	status, page := fetchPage(t, e.Guest, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if status != http.StatusForbidden || !strings.Contains(page, messages["coord.forbidden.title"]) || strings.Contains(page, "coordination target forbidden") {
		t.Fatalf("guest page: %d", status)
	}
	if strings.Contains(page, `href="/ui/coord"`) && !strings.Contains(page, messages["coord.forbidden.back"]) {
		t.Errorf("back link text does not match its target")
	}
	if !strings.Contains(page, `href="/ui/overview">`+messages["coord.forbidden.overview"]) {
		t.Errorf("a guest without rooms is not sent to the overview")
	}
}

// Reading and writing follow the project role (matrix 8.1, spec 7.5): owner,
// lead and reviewer without an own agent get the room tabs and the composer.
func TestOwnerWithoutAgentReadsTheRoomAndGetsTheComposer(t *testing.T) {
	e := ovEnv(t)
	room, hidden := memberReadSeed(t, e)
	for who, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "reviewer": e.Reviewer} {
		_, page := fetchPage(t, c, e.Base+"/ui/coord?room="+url.QueryEscape(room))
		on := regexpTabOn.FindAllString(page, -1)
		if len(on) != 1 || !strings.Contains(on[0], "x/shell") || !strings.Contains(on[0], `aria-current="page"`) {
			t.Errorf("%s: active tab: %v", who, on)
		}
		if who == "owner" && !strings.Contains(page, `href="/ui/coord?room=`+url.QueryEscape(hidden)+`"`) {
			t.Errorf("the other readable room is not a tab")
		}
		if strings.Contains(page, "coord-readonly") || strings.Contains(page, messages["coord.readonly"]) {
			t.Errorf("%s: read-only line despite write access", who)
		}
		for _, want := range []string{`class="coord-composer"`, "/ui/coord/thread/create", "/ui/coord/send"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: no %s", who, want)
			}
		}
	}
	_, page := fetchPage(t, e.Guest, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(page, `class="coord-composer"`) {
		t.Error("guest sees a composer")
	}
}

func TestMemberWithAgentGetsTheComposer(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	_, page := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(page, `class="coord-composer"`) || strings.Contains(page, "coord-readonly") {
		t.Errorf("a member with an own agent has no composer")
	}
	if !strings.Contains(page, "/ui/coord/thread/create") {
		t.Errorf("a member with an own agent cannot start a thread")
	}
}

func TestRoleGrantsWriteAccessToOwnersAndMembersNotGuests(t *testing.T) {
	for name, enforce := range map[string]bool{"enforced": true, "log mode": false} {
		t.Run(name, func(t *testing.T) {
			e := ovEnv(t)
			e.St.SetAccessMode(store.AccessMode{Enforce: enforce})
			room, _ := memberReadSeed(t, e)
			for who, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "reviewer": e.Reviewer} {
				if st := postRoom(t, e, c, "/ui/coord/send", room, url.Values{"body": {"post-by-" + who}}); st != http.StatusSeeOther {
					t.Errorf("%s send: %d, want 303", who, st)
				}
				if st := postRoom(t, e, c, "/ui/coord/thread/create", room, url.Values{"title": {"Thread by " + who}}); st != http.StatusSeeOther {
					t.Errorf("%s thread: %d, want 303", who, st)
				}
			}
			_, page := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))
			for _, want := range []string{"post-by-owner", "post-by-lead", "post-by-reviewer", "Thread by owner"} {
				if !strings.Contains(page, want) {
					t.Errorf("room does not show %q", want)
				}
			}
			if st := postRoom(t, e, e.Guest, "/ui/coord/send", room, url.Values{"body": {"nope"}}); st != http.StatusForbidden {
				t.Errorf("guest send: %d, want 403", st)
			}
			if st := postRoom(t, e, e.Guest, "/ui/coord/thread/create", room, url.Values{"title": {"Nope thread"}}); st != http.StatusForbidden {
				t.Errorf("guest thread: %d, want 403", st)
			}
			_, page = fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(room))
			if strings.Contains(page, "nope") || strings.Contains(page, "Nope thread") {
				t.Error("a refused write left a trace")
			}
			owner := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
			if !owner.CanPost(room) {
				t.Error("CanPost false for an owner")
			}
			// A bearer token of the same person is not a browser session.
			tok := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: "token"}, "")
			if tok.CanPost(room) {
				t.Error("CanPost true for a token principal")
			}
		})
	}
}

func TestMembersWithAgentWriteAsThemselves(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	if st := postRoom(t, e, e.Member, "/ui/coord/send", room, url.Values{"body": {"post-by-member"}}); st != http.StatusSeeOther {
		t.Fatalf("member posts: %d", st)
	}
	if st := postRoom(t, e, e.Member, "/ui/coord/standing/create", room, url.Values{"body": {"rule-of-member"}, "confirm_scope": {"1"}}); st != http.StatusSeeOther {
		t.Fatalf("member standing: %d", st)
	}
	if st := postRoom(t, e, e.Member, "/ui/coord/thread/create", room, url.Values{"title": {"Member thread"}}); st != http.StatusSeeOther {
		t.Fatalf("member thread: %d", st)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	for _, want := range []string{"post-by-member", "rule-of-member", "Member thread"} {
		if !strings.Contains(page, want) {
			t.Errorf("owner does not see %q", want)
		}
	}
	// A guest never writes.
	if st := postRoom(t, e, e.Guest, "/ui/coord/send", room, url.Values{"body": {"guest"}}); st != http.StatusForbidden {
		t.Errorf("guest post: %d", st)
	}
	// The owner ends any instruction through the role; no membership is needed.
	standing, err := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "").Standing(room)
	if err != nil || len(standing) != 1 {
		t.Fatalf("standing %v %v", len(standing), err)
	}
	if st := postRoom(t, e, e.Owner, "/ui/coord/standing/end", room, url.Values{"message_id": {standing[0].MessageID}}); st != http.StatusSeeOther {
		t.Errorf("owner ends without membership: %d", st)
	}
}

// Only a web session reads by role. Every other kind of token for the same
// account (personal, legacy, device, pasted, none) keeps the membership rule.
func TestOnlyAWebSessionReadsByRole(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	for _, kind := range []string{"personal", "legacy", "device", "paste", ""} {
		c := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: kind}, "")
		if _, err := c.Room(room); !errors.Is(err, store.ErrCoordForbidden) {
			t.Errorf("token kind %q reads the room: %v", kind, err)
		}
		rooms, err := c.Rooms()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rooms {
			if r.Key == room {
				t.Errorf("token kind %q lists the project room", kind)
			}
		}
	}
	// Real tokens of the account, as the server authenticates them.
	personal, _, err := e.St.CreateToken("alice", store.TokenSpec{Label: "bearer"})
	if err != nil {
		t.Fatal(err)
	}
	device, _, err := e.St.CreateDeviceToken("person:1", "box")
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"personal bearer": personal, "device": device} {
		p, ok := e.St.AuthenticatePrincipal(tok)
		if !ok {
			t.Fatalf("%s: unknown token", name)
		}
		if _, err := e.St.CoordinationFor(p, "").Room(room); !errors.Is(err, store.ErrCoordForbidden) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A session from a pasted token is not a web session either.
	pasted := login(t, e.Srv, personal)
	if status, page := fetchPage(t, pasted, e.Base+"/ui/coord?room="+url.QueryEscape(room)); status != http.StatusForbidden || strings.Contains(page, "migrating") {
		t.Errorf("pasted session: %d", status)
	}
}

// A machine room is never read by role: without membership it answers exactly
// like a room that does not exist (#2447), so host names cannot be enumerated.
func TestMachineRoomWithoutMembershipIsNotFound(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:box:cccc", PrincipalID: "person:2", Person: "anna", Provider: "claude",
		DisplayName: "box-agent", SessionID: "sess-m", RoomKey: store.RoomKeyForMachine("secret-box")}); err != nil {
		t.Fatal(err)
	}
	machine := store.RoomKeyForMachine("secret-box")
	missingKey := store.RoomKeyForMachine("no-such-box")
	for who, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "guest": e.Guest} {
		status, page := fetchPage(t, c, e.Base+"/ui/coord?room="+url.QueryEscape(machine))
		missStatus, missing := fetchPage(t, c, e.Base+"/ui/coord?room="+url.QueryEscape(missingKey))
		if status != http.StatusNotFound || missStatus != http.StatusNotFound || page != missing {
			t.Errorf("%s: machine room %d vs missing %d (identical: %v)", who, status, missStatus, page == missing)
		}
		if strings.Contains(page, "secret-box") {
			t.Errorf("%s: host name on the page", who)
		}
	}
	owner := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	if _, err := owner.Room(machine); !errors.Is(err, store.ErrCoordNotFound) {
		t.Errorf("owner Room: %v", err)
	}
	// Its member still reads it.
	if _, err := e.St.CoordinationFor(store.Principal{ID: "person:2", Label: "anna", TokenKind: store.WebSessionKind}, "").Room(machine); err != nil {
		t.Errorf("member of the machine room: %v", err)
	}
}

// The room list asks for the roles once and a database failure is an error,
// not an empty list.
func TestRoomListByRoleIsOneAnswerForRoomsAndSummaries(t *testing.T) {
	e := ovEnv(t)
	room, hidden := memberReadSeed(t, e)
	lead := e.St.CoordinationFor(store.Principal{ID: "person:4", Label: "lars", TokenKind: store.WebSessionKind}, "")
	rooms, err := lead.Rooms()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, r := range rooms {
		keys[r.Key] = true
	}
	if !keys[room] || keys[hidden] {
		t.Errorf("lead rooms: %v", keys)
	}
	summaries, err := lead.RoomSummaries()
	if err != nil || len(summaries) != 1 || summaries[0].Room.Key != room {
		t.Errorf("lead summaries: %v %v", summaries, err)
	}
	guest := e.St.CoordinationFor(store.Principal{ID: "person:3", Label: "gina", TokenKind: store.WebSessionKind}, "")
	if rooms, err := guest.Rooms(); err != nil || len(rooms) != 0 {
		t.Errorf("guest rooms: %v %v", rooms, err)
	}
}

func TestDirectMessagesStayWithMembership(t *testing.T) {
	e := ovEnv(t)
	room, hidden := memberReadSeed(t, e)
	_ = room
	has := func(c store.CoordAccess, id string) bool {
		rs, err := c.Recipients()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			if r.PrincipalID == id {
				return true
			}
		}
		return false
	}
	web := func(id, label string) store.CoordAccess {
		return e.St.CoordinationFor(store.Principal{ID: id, Label: label, TokenKind: store.WebSessionKind}, "")
	}
	// Direct messages are unchanged: a room counts only where the viewer may
	// write, so the role alone does not make a project agent addressable.
	for who, p := range map[string][2]string{"owner": {"person:1", "alice"}, "lead": {"person:4", "lars"}, "member": {"person:5", "rita"}} {
		if has(web(p[0], p[1]), "person:2") || has(web(p[0], p[1]), "claude:laptop:aaaa") {
			t.Errorf("%s without an agent can address the project agent", who)
		}
	}
	if has(web("person:3", "gina"), "person:2") || has(web("person:3", "gina"), "claude:laptop:aaaa") {
		t.Error("guest can address the project agent")
	}
	if has(web("person:2", "anna"), "claude:secret-host:bbbb") {
		t.Error("a member of A can address an agent of project B")
	}
	_ = hidden
}

func TestAgentsPageShowsLastPostAndWorkingRoomLinkToOwnerWithoutRoomAgent(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	href := `href="/ui/coord?room=` + url.QueryEscape(room) + `"`
	for who, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "reviewer": e.Reviewer} {
		_, page := fetchPage(t, c, e.Base+"/ui/agents")
		if !strings.Contains(page, "migrating the roles table") || !strings.Contains(page, href) {
			t.Errorf("%s: last post or room link missing", who)
		}
		_, overview := fetchPage(t, c, e.Base+"/ui/overview")
		if !strings.Contains(overview, "worker-sess-a") {
			t.Errorf("%s: overview lacks the agent", who)
		}
		if status, _ := fetchPage(t, c, e.Base+"/ui/coord?room="+url.QueryEscape(room)); status != 200 {
			t.Errorf("%s: room link answers %d", who, status)
		}
	}
}

// Reply links are write controls: a viewer who may post (by role or by an own
// agent) gets them; the guest cannot open the room at all.
func TestOwnerWithoutAgentGetsReplyControls(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	if st := postRoom(t, e, e.Member, "/ui/coord/send", room, url.Values{"body": {"post-by-member"}}); st != http.StatusSeeOther {
		t.Fatalf("member posts: %d", st)
	}
	if st := postRoom(t, e, e.Member, "/ui/coord/thread/create", room, url.Values{"title": {"Member thread"}}); st != http.StatusSeeOther {
		t.Fatalf("member thread: %d", st)
	}
	threads, err := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "").RoomThreads(room)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads %d %v", len(threads), err)
	}
	roomURL := e.Base + "/ui/coord?room=" + url.QueryEscape(room)
	threadURL := e.Base + coordThreadURL(room, threads[0].Thread.ID)
	for _, target := range []string{roomURL, threadURL} {
		_, page := fetchPage(t, e.Owner, target)
		if !strings.Contains(page, "post-by-member") && !strings.Contains(page, "Member thread") {
			t.Fatalf("owner does not read %s", target)
		}
		if target == roomURL && !strings.Contains(page, "coord-reply-action") {
			t.Errorf("owner without an agent has no reply link on %s", target)
		}
		if strings.Contains(page, "/ui/coord/thread/state") {
			t.Errorf("owner who did not author the thread sees the state form on %s", target)
		}
	}
	_, page := fetchPage(t, e.Member, roomURL)
	if !strings.Contains(page, "coord-reply-action") {
		t.Error("member with an agent has no reply link")
	}
	_, page = fetchPage(t, e.Member, threadURL)
	if !strings.Contains(page, "/ui/coord/thread/state") {
		t.Error("member who authored the thread has no thread state form")
	}
}
