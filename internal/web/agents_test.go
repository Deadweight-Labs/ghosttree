package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

// agentsSeedFor returns the public address of the agent's session.
func agentsSeedFor(t *testing.T, st *store.Store, principal, project, id, session, reqTitle, post string) string {
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
	sess, err := st.SessionByID(sid)
	if err != nil {
		t.Fatal(err)
	}
	return sess.PublicID
}

func TestAgentsPageShowsRequestPostMachineAndLinks(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "Roles first", "migrating the roles table")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/agents")
	for _, want := range []string{"worker-sess-a", "@laptop", "Roles first", "migrating the roles table", "feat/sess-a", "REQ-1",
		`href="/ui/coord?room=` + url.QueryEscape(store.RoomKeyForProject(shellProject)) + `"`, `href="/ui/sessions/`, `href="/ui/requests/1"`, `aria-current="page"`} {
		if !strings.Contains(page, want) {
			t.Errorf("member agents page lacks %q", want)
		}
	}
	if strings.Contains(page, "ov-empty") {
		t.Error("member sees the empty state next to an agent")
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
	hiddenPID := agentsSeedFor(t, e.St, "person:1", shellHiddenProject, "claude:secret-host:bbbb", "sess-h", "SECRET-REQ", "SECRET-POST")
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
	for _, leak := range []string{"SECRET", "secret-host", shellHiddenProject, hiddenPID} {
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

func TestAgentsPageReadersSeeProjectAgentsWithoutOwnAgentInTheRoom(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "Roles first", "migrating")
	// Neither the owner nor the lead has an agent in the room; both read agents.
	for name, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "reviewer": e.Reviewer} {
		_, page := fetchPage(t, c, e.Base+"/ui/agents")
		if !strings.Contains(page, "worker-sess-a") || strings.Contains(page, "No agents yet.") {
			t.Errorf("%s does not see the agents of the project", name)
		}
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/agents")
	if strings.Contains(guest, "worker-sess-a") {
		t.Error("guest sees an agent")
	}
	// A reader of another project sees none of it.
	other := agentsSeedFor(t, e.St, "person:1", shellHiddenProject, "claude:other-host:dddd", "sess-b", "OTHER-REQ", "OTHER-POST")
	_, anna := fetchPage(t, e.Member, e.Base+"/ui/agents")
	for _, leak := range []string{"worker-sess-b", "other-host", "OTHER-REQ", "OTHER-POST", other} {
		if strings.Contains(anna, leak) {
			t.Errorf("member of project A sees project B: %q", leak)
		}
	}
}

func TestAgentsPageRequestOfAHiddenProjectStaysOut(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "Visible work", "post")
	sess, err := e.St.SessionByID(1)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "HIDDEN-REQ-TITLE", Scope: scope.Axes{Project: shellHiddenProject}, Person: "anna"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(detail.Request.ID, sess.ID, "related", "anna"); err != nil {
		t.Fatal(err)
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/agents")
	if strings.Contains(member, "HIDDEN-REQ-TITLE") || strings.Contains(member, "/ui/requests/"+strconv.FormatInt(detail.Request.ID, 10)) {
		t.Error("member sees a request of a project they cannot read")
	}
	if !strings.Contains(member, "worker-sess-a") {
		t.Error("the agent itself is gone")
	}
}

func TestAgentsPagePrefersPrimaryWork(t *testing.T) {
	e := ovEnv(t)
	id := "claude:laptop:aaaa"
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:2", Person: "anna", Provider: "claude",
		DisplayName: "worker-p", SessionID: "sess-p", RoomKey: store.RoomKeyForProject(shellProject)}); err != nil {
		t.Fatal(err)
	}
	sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "sess-p", AccountID: store.AccountIDOf("person:2"), Scope: scope.Axes{Project: shellProject}})
	if err != nil {
		t.Fatal(err)
	}
	// The related work starts first and so has the lower id.
	for _, w := range []struct{ title, role string }{{"Related work", "related"}, {"Primary work", "primary"}} {
		detail, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: w.title, Scope: scope.Axes{Project: shellProject}, Person: "anna"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.St.StartRequestWork(detail.Request.ID, sid, w.role, "anna"); err != nil {
			t.Fatal(err)
		}
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if !strings.Contains(page, "Primary work") || strings.Contains(page, "Related work") {
		t.Fatalf("page does not show the primary work: %s", page)
	}
}

func TestAgentsPageSessionLinkFollowsTheSessionVisibility(t *testing.T) {
	e := ovEnv(t)
	// The agent belongs to the owner (person:1); anna only reads the project.
	pid := agentsSeedFor(t, e.St, "person:1", shellProject, "claude:laptop:aaaa", "sess-own", "Owner work", "post")
	_, anna := fetchPage(t, e.Member, e.Base+"/ui/agents")
	if strings.Contains(anna, "/ui/sessions/") || strings.Contains(anna, pid) {
		t.Error("member gets a link to a session that is not shared")
	}
	if !strings.Contains(anna, "worker-sess-own") {
		t.Fatal("agent missing")
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if !strings.Contains(owner, `href="/ui/sessions/`+pid+`"`) {
		t.Error("owner lacks the link to the own session")
	}
}

func TestAgentsPageHiddenProjectFilterEqualsUnknownProject(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa", "sess-a", "Visible work", "post")
	agentsSeedFor(t, e.St, "person:1", shellHiddenProject, "claude:secret-host:bbbb", "sess-h", "SECRET-REQ", "SECRET-POST")
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		sh, hidden := fetchPage(t, c, e.Base+"/ui/agents?project="+url.QueryEscape(shellHiddenProject))
		su, unknown := fetchPage(t, c, e.Base+"/ui/agents?project="+url.QueryEscape("github.com/x/nowhere"))
		if sh != su || strings.ReplaceAll(hidden, url.QueryEscape(shellHiddenProject), "P") != strings.ReplaceAll(unknown, url.QueryEscape("github.com/x/nowhere"), "P") {
			t.Errorf("%s: a hidden project answers differently from an unknown one", name)
		}
		if strings.Contains(hidden, "SECRET") {
			t.Errorf("%s: hidden project filter leaks", name)
		}
	}
}

func TestAgentsPageGuestAnswerIsTheSameWithAndWithoutHiddenData(t *testing.T) {
	plain := ovEnv(t)
	full := ovEnv(t)
	agentsSeedFor(t, full.St, "person:2", shellProject, "claude:laptop:aaaa", "sess-a", "Visible work", "post")
	agentsSeedFor(t, full.St, "person:1", shellHiddenProject, "claude:secret-host:bbbb", "sess-h", "SECRET-REQ", "SECRET-POST")
	_, a := fetchPage(t, plain.Guest, plain.Base+"/ui/agents")
	_, b := fetchPage(t, full.Guest, full.Base+"/ui/agents")
	volatile := regexp.MustCompile(`value="[0-9a-f]{64}"`)
	norm := func(page, base string) string {
		return volatile.ReplaceAllString(strings.ReplaceAll(page, strings.TrimPrefix(base, "http://"), "HOST"), `value="CSRF"`)
	}
	if norm(a, plain.Base) != norm(b, full.Base) {
		t.Errorf("the guest page differs with hidden data present:\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(a, "Nothing here yet.") || strings.Contains(a, "No agents yet.") || strings.Contains(a, "connect=1") {
		t.Error("guest empty state is not the neutral one")
	}
}
