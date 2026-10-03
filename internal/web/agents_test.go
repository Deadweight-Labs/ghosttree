package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// agentsSeed puts one working agent into a project: registered with its
// session, active on an open request, with one post in the room.
func agentsSeed(t *testing.T, st *store.Store, project, id, session, reqTitle, post string) {
	agentsSeedFor(t, st, "person:2", project, id, session, reqTitle, post)
}

func agentsSeedFor(t *testing.T, st *store.Store, principal, project, id, session, reqTitle, post string) {
	t.Helper()
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: principal, Person: "anna", Provider: "claude",
		DisplayName: "worker-" + session, Branch: "feat/" + session, SessionID: session, RoomKey: store.RoomKeyForProject(project)}); err != nil {
		t.Fatal(err)
	}
	sid, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: session, AccountID: store.AccountIDOf(principal), Scope: scope.Axes{Project: project}})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: reqTitle, Scope: scope.Axes{Project: project}, Person: "anna"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.StartRequestWork(detail.Request.ID, sid, "primary", "anna"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: store.RoomKeyForProject(project),
		SenderExternalID: id, AuthorPrincipalID: principal, AuthorKind: store.AuthorAgent, ClientID: "c-" + session, Body: post}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsPageShowsRequestPostMachineAndLinks(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "Roles first", "migrating the roles table")
	for name, c := range map[string]*http.Client{"member": e.Member} {
		_, page := fetchPage(t, c, e.Base+"/ui/agents")
		for _, want := range []string{"worker-sess-a", "@laptop", "Roles first", "migrating the roles table", "feat/sess-a", "REQ-1",
			`href="/ui/coord?room=` + url.QueryEscape(store.RoomKeyForProject(shellProject)) + `"`, `href="/ui/sessions/`, `href="/ui/requests/1"`, `aria-current="page"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s agents page lacks %q", name, want)
			}
		}
		if strings.Contains(page, "ov-empty") {
			t.Errorf("%s sees the empty state next to an agent", name)
		}
	}
}

func TestAgentsPageEmptyIsOneLineAndOneAction(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if !strings.Contains(page, "No agents yet.") || strings.Count(page, `href="/ui/overview?connect=1"`) != 1 {
		t.Errorf("empty agents page lacks the line or the single connect action")
	}
	if strings.Contains(page, "<style") || strings.Contains(page, " style=") || strings.Contains(page, "onclick") {
		t.Error("agents page breaks the CSP")
	}
}

func TestAgentsPageHidesWhatTheViewerMayNotSee(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "Visible work", "visible post")
	agentsSeedFor(t, e.St, "person:1", shellHiddenProject, "claude:secret-host:bbbb", "sess-h", "SECRET-REQ", "SECRET-POST")
	ovAgent(t, e.St, shellProject, "claude:owner-host:cccc", "owner-agent", "person:1")
	// anna is a plain member in the shell project only.
	_, anna := fetchPage(t, e.Member, e.Base+"/ui/agents")
	// the owner reads both projects
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	for _, want := range []string{"SECRET-REQ", "SECRET-POST", "secret-host", "Visible work"} {
		if !strings.Contains(owner, want) {
			t.Errorf("owner lacks %q", want)
		}
	}
	for _, leak := range []string{"SECRET", "secret-host", shellHiddenProject, "sess-h"} {
		if strings.Contains(anna, leak) {
			t.Errorf("member page leaks %q", leak)
		}
	}
	_, scoped := fetchPage(t, e.Owner, e.Base+"/ui/agents?project="+url.QueryEscape(shellProject))
	if strings.Contains(scoped, "SECRET") || !strings.Contains(scoped, "Visible work") {
		t.Error("the project selector does not narrow the agents page")
	}
}

func TestAgentsPageGuestSeesNoAgentsAndNoConnect(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "Guest must not see", "guest must not see this")
	status, page := fetchPage(t, e.Guest, e.Base+"/ui/agents")
	if status != 200 {
		t.Fatalf("guest status %d", status)
	}
	for _, leak := range []string{"worker-sess-a", "laptop", "Guest must not see", "guest must not see this", "REQ-", "sess-a", "connect=1", "/ui/sessions/"} {
		if strings.Contains(page, leak) {
			t.Errorf("guest agents page leaks %q", leak)
		}
	}
	if strings.Contains(page, `data-nav="agents"`) {
		t.Error("guest navigation offers the agents page")
	}
}
