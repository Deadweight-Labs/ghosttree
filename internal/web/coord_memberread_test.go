package web

import (
	"net/http"
	"net/url"
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
	}{{"stranger", stranger, room}, {"member of project A in room B", e.Member, hidden}, {"lead of project A in room B", e.Lead, hidden}}
	for _, tc := range cases {
		status, page := fetchPage(t, tc.c, e.Base+"/ui/coord?room="+url.QueryEscape(tc.room))
		if status == 200 || strings.Contains(page, "migrating") || strings.Contains(page, "SECRET-POST") {
			t.Errorf("%s: status %d reads the room", tc.who, status)
		}
	}
	// The refusal is a designed page with texts from messages.go, not bare text.
	status, page := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(hidden))
	if status != http.StatusForbidden && status != http.StatusNotFound {
		t.Fatalf("status %d", status)
	}
	if status == http.StatusForbidden && (!strings.Contains(page, messages["coord.forbidden.title"]) || strings.Contains(page, "coordination target forbidden")) {
		t.Errorf("forbidden room is not the designed page: %s", page)
	}
}

func TestProjectRoomWritingKeepsTheMembershipRule(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	owner := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	if _, err := owner.MessageWindow(store.DestinationRoom, room, store.LatestWindow(10)); err != nil {
		t.Fatalf("owner reads: %v", err)
	}
	_, err := owner.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "person:1",
		AuthorPrincipalID: "person:1", AuthorKind: store.AuthorHuman, ClientID: "w1", Body: "hello"})
	if err == nil {
		t.Error("an owner without an agent in the room posts")
	}
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
