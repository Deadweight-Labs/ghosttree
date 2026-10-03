package web

import (
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

func TestOwnerWithoutAgentSeesTheRoomTabAndComposer(t *testing.T) {
	e := ovEnv(t)
	room, hidden := memberReadSeed(t, e)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	on := regexpTabOn.FindAllString(page, -1)
	if len(on) != 1 || !strings.Contains(on[0], "x/shell") || !strings.Contains(on[0], `aria-current="page"`) {
		t.Errorf("active tab: %v", on)
	}
	if !strings.Contains(page, `href="/ui/coord?room=`+url.QueryEscape(hidden)+`"`) {
		t.Errorf("the other readable room is not a tab")
	}
	if !strings.Contains(page, `class="coord-composer"`) || strings.Contains(page, "coord-readonly") {
		t.Errorf("owner has no composer")
	}
}

func TestMembersWriteInTheProjectRoomWithoutOwnAgent(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	for who, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "reviewer": e.Reviewer} {
		body := "post-by-" + who
		if st := postRoom(t, e, c, "/ui/coord/send", room, url.Values{"body": {body}}); st != http.StatusSeeOther {
			t.Fatalf("%s posts: %d", who, st)
		}
		_, page := fetchPage(t, c, e.Base+"/ui/coord?room="+url.QueryEscape(room))
		if !strings.Contains(page, body) {
			t.Errorf("%s: own post not shown", who)
		}
	}
	// Sender and authority come from the person and the project role.
	owner := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	page, err := owner.MessageWindow(store.DestinationRoom, room, store.LatestWindow(20))
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	senderRoles := e.St.SenderRolesInProject(shellProject, page.Messages)
	for i, m := range page.Messages {
		if strings.HasPrefix(m.Body, "post-by-") {
			if m.AuthorKind != store.AuthorHuman || m.SenderExternalID != m.AuthorPrincipalID {
				t.Errorf("%s: author %q/%q", m.Body, m.AuthorKind, m.AuthorPrincipalID)
			}
			roles[m.Body] = senderRoles[i]
		}
	}
	for who, role := range map[string]string{"owner": store.RoleOwner, "lead": store.RoleLead, "reviewer": store.RoleMember} {
		if roles["post-by-"+who] != role {
			t.Errorf("%s: sender role %q, want %q", who, roles["post-by-"+who], role)
		}
	}
	// The chip shows in the browser.
	_, view := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(view, "post-by-lead") || !strings.Contains(view, `class="cm-role"`) {
		t.Errorf("role chip missing")
	}
	// A guest never writes through this path.
	if st := postRoom(t, e, e.Guest, "/ui/coord/send", room, url.Values{"body": {"guest"}}); st != http.StatusForbidden {
		t.Errorf("guest post: %d", st)
	}
}

func TestStandingFollowsRankForPeopleWithoutAgent(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	for who, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "member": e.Member} {
		st := postRoom(t, e, c, "/ui/coord/standing/create", room, url.Values{"body": {"rule-of-" + who}, "confirm_scope": {"1"}})
		if st != http.StatusSeeOther {
			t.Fatalf("%s directive: %d", who, st)
		}
	}
	if st := postRoom(t, e, e.Guest, "/ui/coord/standing/create", room, url.Values{"body": {"g"}, "confirm_scope": {"1"}}); st != http.StatusForbidden {
		t.Errorf("guest directive: %d", st)
	}
	// Ending keeps its rank rule: the member ends only their own.
	ownerAccess := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	standing, err := ownerAccess.Standing(room)
	if err != nil || len(standing) != 3 {
		t.Fatalf("standing %v %v", len(standing), err)
	}
	for _, s := range standing {
		st := postRoom(t, e, e.Member, "/ui/coord/standing/end", room, url.Values{"message_id": {s.MessageID}})
		own := strings.HasSuffix(s.Body, "member")
		if own && st != http.StatusSeeOther || !own && st != http.StatusForbidden {
			t.Errorf("member ends %q: %d", s.Body, st)
		}
	}
}

func TestMembersCreateThreadsWithoutOwnAgent(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	if st := postRoom(t, e, e.Owner, "/ui/coord/thread/create", room, url.Values{"title": {"Owner thread"}}); st != http.StatusSeeOther {
		t.Fatalf("owner new thread: %d", st)
	}
	if st := postRoom(t, e, e.Guest, "/ui/coord/thread/create", room, url.Values{"title": {"Guest thread"}}); st != http.StatusForbidden {
		t.Errorf("guest new thread: %d", st)
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(page, "Owner thread") {
		t.Errorf("lead does not see the thread")
	}
}

func TestPostingNeedsTheRoleNotAnAgentToken(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	msg := store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "w1", Body: "hello"}
	web := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	if _, err := web.Send(msg); err != nil {
		t.Errorf("owner without agent: %v", err)
	}
	if !web.CanPost(room) {
		t.Error("CanPost false for an owner")
	}
	guest := e.St.CoordinationFor(store.Principal{ID: "person:3", Label: "gina", TokenKind: store.WebSessionKind}, "")
	if guest.CanPost(room) {
		t.Error("CanPost true for a guest without an agent")
	}
	token := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: "agent"}, "")
	msg.ClientID = "w2"
	if _, err := token.Send(msg); err == nil {
		t.Error("a person token without an agent posts")
	}
}

func TestDirectMessagesToProjectAgents(t *testing.T) {
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
	// The agent in x/shell belongs to person:2 (member).
	for who, p := range map[string][2]string{"owner": {"person:1", "alice"}, "lead": {"person:4", "lars"}, "member": {"person:5", "rita"}} {
		if !has(web(p[0], p[1]), "person:2") && !has(web(p[0], p[1]), "claude:laptop:aaaa") {
			t.Errorf("%s without an agent cannot address the project agent", who)
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
