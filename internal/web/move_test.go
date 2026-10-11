package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestKnowledgeMoveFormMovesToProjectMachineAndGlobal(t *testing.T) {
	e := seedSessions(t)
	id := knInsert(t, e.St, store.Knowledge{Title: "Misfiled pitfall", Type: "pitfall"})
	path := "/ui/knowledge/" + strconv.FormatInt(id, 10)
	move := func(c *http.Client, form url.Values) *http.Response {
		return knPost(t, c, e.Base, path, path+"/move", form)
	}

	// The form is there for those who may edit, and offers where they may write.
	_, page := fetchPage(t, e.Owner, e.Base+path)
	for _, want := range []string{`action="` + path + `/move"`, `name="project"`, `name="machine"`, shellHiddenProject} {
		if !strings.Contains(page, want) {
			t.Errorf("owner page lacks %q", want)
		}
	}
	if _, member := fetchPage(t, e.Member, e.Base+path); strings.Contains(member, path+"/move") {
		t.Error("a member who may not edit sees the move form")
	}

	if resp := move(e.Owner, url.Values{"project": {shellHiddenProject}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("project move = %d", resp.StatusCode)
	}
	if k, _ := e.St.KnowledgeByID(id); k.Scope.Project != shellHiddenProject {
		t.Fatalf("scope = %+v", k.Scope)
	}
	if resp := move(e.Owner, url.Values{"machine": {"Media-VM"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("machine move = %d", resp.StatusCode)
	}
	if k, _ := e.St.KnowledgeByID(id); k.Scope.Project != "" || k.Scope.Machine != "media-vm" {
		t.Fatalf("scope = %+v", k.Scope)
	}
	if resp := move(e.Owner, url.Values{"project": {""}, "machine": {""}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("global move = %d", resp.StatusCode)
	}
	if k, _ := e.St.KnowledgeByID(id); !k.Scope.IsGlobal() {
		t.Fatalf("scope = %+v", k.Scope)
	}
	// Refusals: both targets, nothing to change, unknown project, no right.
	if resp := move(e.Owner, url.Values{"project": {shellProject}, "machine": {"x"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("two targets = %d", resp.StatusCode)
	}
	if resp := move(e.Owner, url.Values{}); resp.StatusCode != http.StatusConflict {
		t.Errorf("global to global = %d", resp.StatusCode)
	}
	if resp := move(e.Owner, url.Values{"project": {"github.com/nobody/x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d", resp.StatusCode)
	}
	if resp := move(e.Member, url.Values{"project": {shellProject}}); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Errorf("member move = %d", resp.StatusCode)
	}
	// The log says who moved it where.
	_, page = fetchPage(t, e.Owner, e.Base+path)
	for _, want := range []string{"Moves", shellProject + " → " + shellHiddenProject, "global"} {
		if !strings.Contains(page, want) {
			t.Errorf("the entry page lacks %q in its move log", want)
		}
	}
}

func TestRequestMoveFormMovesAndLogs(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Misfiled request"})
	id := d.Request.ID
	_, page := fetchPage(t, e.Owner, e.Base+rqPath(id, ""))
	for _, want := range []string{`action="` + rqPath(id, "/move") + `"`, `name="project"`, shellHiddenProject} {
		if !strings.Contains(page, want) {
			t.Errorf("owner page lacks %q", want)
		}
	}
	if resp := rqPost(t, e.shellEnv, e.Member, id, "/move", url.Values{"project": {shellProject}, "reason": {"x"}}); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Errorf("member move = %d", resp.StatusCode)
	}
	if resp := rqPost(t, e.shellEnv, e.Owner, id, "/move", url.Values{"project": {shellHiddenProject}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a move without a reason = %d", resp.StatusCode)
	}
	if resp := rqPost(t, e.shellEnv, e.Owner, id, "/move", url.Values{"project": {"github.com/nobody/x"}, "reason": {"x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d", resp.StatusCode)
	}
	resp := rqPost(t, e.shellEnv, e.Owner, id, "/move", url.Values{"project": {shellHiddenProject}, "reason": {"it is about the other repo"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "done=moved") {
		t.Fatalf("move = %d -> %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	got, _ := e.St.RequestByID(id)
	if got.Request.Scope.Project != shellHiddenProject {
		t.Fatalf("project = %q", got.Request.Scope.Project)
	}
	if resp := rqPost(t, e.shellEnv, e.Owner, id, "/move", url.Values{"project": {shellHiddenProject}, "reason": {"again"}}); resp.StatusCode != http.StatusConflict {
		t.Errorf("a move to where it is = %d", resp.StatusCode)
	}
	_, page = fetchPage(t, e.Owner, e.Base+resp.Header.Get("Location"))
	for _, want := range []string{"Moved", shellProject + " → " + shellHiddenProject} {
		if !strings.Contains(page, want) {
			t.Errorf("the activity log lacks %q", want)
		}
	}
}
