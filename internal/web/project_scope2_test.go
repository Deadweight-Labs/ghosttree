package web

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestMemberOfTwoProjectsSeesAllSessionsIncludingOnesWithoutProject(t *testing.T) {
	e := shellWebAll(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	if err := e.St.SetProjectRole("person:1", shellHiddenProject, "person:2", store.RoleMember, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	pid := map[string]string{}
	for name, project := range map[string]string{"in-shell": shellProject, "in-hidden": shellHiddenProject, "no-project": ""} {
		id, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "ext-" + name, AccountID: 2,
			Scope: scope.Axes{Project: project, Machine: "laptop"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.St.AppendChunks(id, work("zebrafish")); err != nil {
			t.Fatal(err)
		}
		sess, _ := e.St.SessionByID(id)
		pid[name] = sess.PublicID
	}
	for _, path := range []string{"/ui/sessions", "/ui/sessions?q=zebrafish"} {
		_, page := fetchPage(t, e.Member, e.Base+path)
		for name, id := range pid {
			if !strings.Contains(page, `href="/ui/sessions/`+id+`"`) {
				t.Errorf("GET %s lacks the session %s", path, name)
			}
		}
	}
	// Choosing a project narrows to it; project-less sessions belong to All only.
	_, page := fetchPage(t, e.Member, e.Base+"/ui/sessions?project="+shellProject)
	if !strings.Contains(page, pid["in-shell"]) || strings.Contains(page, pid["in-hidden"]) || strings.Contains(page, pid["no-project"]) {
		t.Error("a chosen project does not narrow the sessions")
	}
}

func TestRoomBadgeAndAttentionCountEveryProjectNotOnlyTheSelectedOne(t *testing.T) {
	e := shellWebAll(t)
	roomA, roomB := "project:"+shellHiddenProject, "project:"+shellProject
	materializeWebRoom(t, e.St, roomA)
	materializeWebRoom(t, e.St, roomB)
	// A question that mentions the owner waits in project B while A is selected.
	if _, err := e.St.AppendCoordMessage(store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: roomB,
		SenderExternalID: "reviewer", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorHuman,
		ClientID: "q", Body: "Bitte", Intent: store.IntentQuestion, Mentions: []string{"person:1"},
	}); err != nil {
		t.Fatal(err)
	}
	badge := regexp.MustCompile(`class="coord-count" aria-hidden="true">(\d+)</span>`)
	c := remembering(t, e.Base, e.Owner)
	open := "&room=" + url.QueryEscape(roomA)
	_, all := fetchPage(t, c, e.Base+"/ui/coord?project="+open)
	_, narrowed := fetchPage(t, c, e.Base+"/ui/coord?project="+shellHiddenProject+open)
	want := badge.FindStringSubmatch(all)
	if want == nil {
		t.Fatal("All projects shows no badge for the waiting question")
	}
	if got := badge.FindStringSubmatch(narrowed); got == nil || got[1] != want[1] {
		t.Errorf("badge with project A selected = %v, want %s", got, want[1])
	}
	if !strings.Contains(narrowed, "Bitte") && !strings.Contains(narrowed, url.QueryEscape(roomB)) {
		t.Error("the waiting question vanished from the attention section")
	}
	if strings.Contains(narrowed, `class="rtab" href="`+coordRoomURL(roomB, "", 0)) {
		t.Error("the other project's room is still in the list")
	}
}

func TestUnusableQueryFallsBackToTheCookieForHeaderAndPageAlike(t *testing.T) {
	e := shellWebAll(t)
	if err := e.St.SetProjectRole("person:1", shellHiddenProject, "person:2", store.RoleMember, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	rqMake(t, e.St, rqSpec{Title: "Shell request"})
	rqMake(t, e.St, rqSpec{Title: "Hidden request", Project: shellHiddenProject})
	c := remembering(t, e.Base, e.Member)
	fetchPage(t, c, e.Base+"/ui/overview?project="+shellProject)
	_, page := fetchPage(t, c, e.Base+"/ui/requests?project=github.com/x/nowhere")
	if got := selectedOption(page); got != shellProject {
		t.Errorf("header shows %q, want the cookie's %q", got, shellProject)
	}
	if !strings.Contains(page, "Shell request") || strings.Contains(page, "Hidden request") {
		t.Error("the page does not follow the header")
	}
	if strings.Contains(page, "nowhere") {
		t.Error("the unusable project name was echoed")
	}
	// Without a cookie the same query means All projects, in header and page.
	fresh := remembering(t, e.Base, e.Member)
	_, page = fetchPage(t, fresh, e.Base+"/ui/requests?project=github.com/x/nowhere")
	if selectedOption(page) != "" || !strings.Contains(page, "Shell request") || !strings.Contains(page, "Hidden request") {
		t.Error("an unusable query without a cookie is not All projects")
	}
}
