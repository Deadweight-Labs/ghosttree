package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-615, decision #4349: knowledge and runbooks can carry a project AND a
// machine. These tests go over the HTTP API, which every reader (search, briefing,
// hook, MCP, CLI, web) shares: the project role alone decides, the machine is
// where it applies.

// rmEntry files a project entry that names a machine, as mia (member of the
// project), and returns its id.
func rmEntry(t *testing.T, f *accessAPIFixture, title, machine string) int64 {
	t.Helper()
	out := f.expect(t, "mia", 200, "POST", "/api/knowledge", map[string]any{
		"type": "pitfall", "title": title, "body": "uniqueword " + title,
		"scope": scope.Axes{Project: accProject, Machine: machine},
	})
	var k store.Knowledge
	if err := json.Unmarshal([]byte(out), &k); err != nil || k.ID == 0 {
		t.Fatalf("created entry: %s %v", out, err)
	}
	if k.Scope.Project != accProject || k.Scope.Machine != machine {
		t.Fatalf("the entry lost an axis: %+v", k.Scope)
	}
	return k.ID
}

// Every read path follows the project role: detail, list, plain and qualified
// search, search in the union a session reads, the briefing and relevance.
func TestProjectEntryWithAMachineIsReadThroughTheProjectOnEveryPath(t *testing.T) {
	f := accessAPI(t, true)
	title := "buildhost deploy order"
	id := rmEntry(t, f, title, "buildhost-box")
	if err := f.st.ClaimMachine("buildhost-box", "person:6"); err != nil { // nora owns the machine, has no role
		t.Fatal(err)
	}
	paths := map[string]string{
		"search":                  "/api/search?q=uniqueword&kind=knowledge",
		"search by repo":          "/api/search?q=" + url.QueryEscape("uniqueword repo:dw/p kind:knowledge"),
		"search by type":          "/api/search?q=" + url.QueryEscape("uniqueword type:pitfall kind:knowledge"),
		"search, machine":         "/api/search?q=uniqueword&kind=knowledge&machine=buildhost-box",
		"search, union":           "/api/search?q=uniqueword&kind=knowledge&scope=union&project=" + accProject + "&machine=laptop",
		"list":                    "/api/knowledge?project=" + accProject,
		"list, deliverable":       "/api/knowledge?project=" + accProject + "&deliverable=1",
		"briefing, other machine": "/api/context/bootstrap?project=" + accProject + "&machine=laptop",
		"briefing, own machine":   "/api/context/bootstrap?project=" + accProject + "&machine=buildhost-box",
		"relevance":               "/api/context/relevant?project=" + accProject + "&machine=laptop&q=" + url.QueryEscape("buildhost deploy"),
	}
	for _, who := range []string{"robin", "lena", "mia", "rex"} {
		for name, path := range paths {
			if out := f.expect(t, who, 200, "GET", path, nil); !strings.Contains(out, title) {
				t.Errorf("%s (a member): %s does not find the project entry with a machine", who, name)
			}
		}
	}
	// A guest reads released entries like any project entry.
	for _, name := range []string{"search", "list", "briefing, other machine"} {
		if out := f.expect(t, "gus", 200, "GET", paths[name], nil); !strings.Contains(out, title) {
			t.Errorf("gus (a guest): %s lacks the trusted project entry", name)
		}
	}
	f.expect(t, "gus", 200, "GET", idPath("/api/knowledge/%d", id), nil)
	// nora owns the machine and has no role in the project: nothing, on no path.
	for name, path := range paths {
		code, out := f.call(t, "nora", "GET", path, nil)
		if strings.Contains(out, title) || strings.Contains(out, `"body":"uniqueword`) {
			t.Errorf("nora (owner of the machine, no role): %s hands her the project entry (%d)", name, code)
		}
		if strings.HasPrefix(name, "search") {
			// The answer echoes the query, so it is the list that has to be empty.
			var res struct {
				Knowledge []store.Knowledge `json:"knowledge"`
			}
			if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Knowledge) != 0 {
				t.Errorf("nora: %s finds %d entries (%v)", name, len(res.Knowledge), err)
			}
		}
	}
	f.expect(t, "nora", 404, "GET", idPath("/api/knowledge/%d", id), nil)
	f.expect(t, "nora", 404, "PATCH", idPath("/api/knowledge/%d", id), map[string]string{"body": "x"})
}

// A session on the machine WITHOUT the project does not read the project entry:
// that would be the leak in the other direction.
func TestProjectEntryWithAMachineDoesNotReachSessionsWithoutTheProject(t *testing.T) {
	f := accessAPI(t, true)
	title := "buildhost deploy order"
	rmEntry(t, f, title, "mia-box")
	for _, who := range []string{"mia", "robin"} {
		if out := f.expect(t, who, 200, "GET", "/api/context/bootstrap?machine=mia-box", nil); strings.Contains(out, title) {
			t.Errorf("%s: a session on the machine without the project is handed the project entry", who)
		}
		if out := f.expect(t, who, 200, "GET", "/api/context/relevant?machine=mia-box&q="+url.QueryEscape("buildhost deploy"), nil); strings.Contains(out, title) {
			t.Errorf("%s: relevance hands the project entry to a session without the project", who)
		}
	}
	// And machine-only knowledge stays private: mia's own machine entry is hers.
	if out := f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?machine=mia-box", nil); !strings.Contains(out, "mia machine pitfall") {
		t.Error("the owner is not handed her own machine knowledge")
	}
	if out := f.expect(t, "robin", 200, "GET", "/api/search?q=pitfall&kind=knowledge", nil); strings.Contains(out, "mia machine pitfall") {
		t.Errorf("machine-only knowledge of mia is found by the project owner: %s", out)
	}
}

// A machine entry that has no project is not made readable by writing a project
// next to it afterwards: moving it changes the scope, it does not add an axis.
func TestMachineOnlyKnowledgeStaysPrivateOverTheAPI(t *testing.T) {
	f := accessAPI(t, true)
	for _, who := range []string{"robin", "lena", "rex", "gus", "nora"} {
		if code, _ := f.call(t, who, "GET", idPath("/api/knowledge/%d", f.id["k-machine"]), nil); code != 404 {
			t.Errorf("%s reads mia's machine-only entry: %d", who, code)
		}
	}
	f.expect(t, "mia", 200, "GET", idPath("/api/knowledge/%d", f.id["k-machine"]), nil)
}

// The machine page lists the entries that name the machine, but only those the
// viewer may read through the project (or as owner, for entries without one).
func TestMachinePageListsProjectEntriesOnlyForWhoMayReadThem(t *testing.T) {
	f := accessAPI(t, true)
	if err := f.st.ClaimMachine("nora-box", "person:6"); err != nil { // nora: owner, no role in the project
		t.Fatal(err)
	}
	projectEntry := rmEntry(t, f, "deploy the project on nora-box", "nora-box")
	mine := rmEntry(t, f, "deploy the project on mia-box", "mia-box")
	type page struct {
		Name    string
		Entries []store.MachineEntry
	}
	fetch := func(who, machine string, want int) page {
		t.Helper()
		var p page
		out := f.expect(t, who, want, "GET", "/api/machines/"+machine, nil)
		if want == 200 {
			if err := json.Unmarshal([]byte(out), &p); err != nil {
				t.Fatalf("%s: %v (%s)", who, err, out)
			}
		}
		return p
	}
	has := func(p page, id int64) bool {
		for _, e := range p.Entries {
			if e.ID == id && e.Kind == store.EntryKnowledge {
				return true
			}
		}
		return false
	}
	// mia owns mia-box and has the project role: her page lists the entry, with its project.
	p := fetch("mia", "mia-box", 200)
	if !has(p, mine) {
		t.Errorf("the owner's machine page lacks the project entry: %+v", p.Entries)
	}
	for _, e := range p.Entries {
		if e.ID == mine && e.Project != accProject {
			t.Errorf("the entry does not name its project: %+v", e)
		}
	}
	// ... and her machine-only entry, as owner.
	if !has(p, f.id["k-machine"]) {
		t.Errorf("the owner's machine page lacks her machine-only entry: %+v", p.Entries)
	}
	// nora owns nora-box but has no role in the project: the page exists for her,
	// the project entry is not on it.
	p = fetch("nora", "nora-box", 200)
	if has(p, projectEntry) {
		t.Errorf("a person without a role in the project reads its entry on her machine page: %+v", p.Entries)
	}
	// Somebody who has no part in the machine gets "not found", not a list.
	fetch("rex", "mia-box", 404)
	fetch("rex", "nora-box", 404)
}

// Writing: the project role decides, a machine name is no claim, and a share to
// organizations does not go with a project.
func TestWritingAProjectEntryWithAMachineOverTheAPI(t *testing.T) {
	f := accessAPI(t, true)
	// Guest and stranger may not file under the project, with or without a machine.
	for _, who := range []string{"gus", "nora"} {
		code, _ := f.call(t, who, "POST", "/api/knowledge", map[string]any{
			"type": "note", "title": "from " + who, "body": "x", "scope": scope.Axes{Project: accProject, Machine: "buildhost-box"},
		})
		if code != 403 && code != 404 && code != 409 {
			t.Errorf("%s filed a project entry with a machine: %d", who, code)
		}
	}
	// A member of the project may name a machine of another account.
	f.expect(t, "rex", 200, "POST", "/api/knowledge", map[string]any{
		"type": "note", "title": "rex about mia-box", "body": "x", "scope": scope.Axes{Project: accProject, Machine: "mia-box"},
	})
	// share_with_orgs belongs to machine-only entries.
	f.expect(t, "mia", 400, "POST", "/api/knowledge", map[string]any{
		"type": "note", "title": "shared", "body": "x", "scope": scope.Axes{Project: accProject, Machine: "mia-box"},
		"share_with_orgs": []string{"alpha"},
	})
	// Sharing it afterwards over the entry share route is refused as well.
	id := rmEntry(t, f, "project entry on mia-box", "mia-box")
	code, out := f.call(t, "mia", "PUT", idPath("/api/knowledge/%d/shares/alpha", id), nil)
	if code == 200 {
		t.Errorf("an entry with a project was shared through the machine: %s", out)
	}
}

// A runbook for a repository on a host is written with both axes, read through the
// project role, and not found by a person who only owns the machine.
func TestProjectRunbookForAMachineOverTheAPI(t *testing.T) {
	f := accessAPI(t, true)
	spec := map[string]any{"purpose": "deploy the project", "steps": []map[string]any{{"title": "run it", "command": "./deploy.sh", "check": "exit 0"}}}
	out := f.expect(t, "mia", 201, "POST", "/api/runbooks", map[string]any{
		"project": accProject, "machine": "buildhost-box", "name": "deploy", "spec": spec,
	})
	var w struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &w); err != nil || w.ID == 0 {
		t.Fatalf("write: %s %v", out, err)
	}
	if err := f.st.ClaimMachine("buildhost-box", "person:6"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"robin", "lena", "rex"} {
		f.expect(t, who, 200, "GET", idPath("/api/runbooks/%d", w.ID), nil)
	}
	f.expect(t, "nora", 404, "GET", idPath("/api/runbooks/%d", w.ID), nil)
	f.expect(t, "gus", 404, "GET", idPath("/api/runbooks/%d", w.ID), nil)
	// nora may not file one under the project on her machine.
	code, _ := f.call(t, "nora", "POST", "/api/runbooks", map[string]any{"project": accProject, "machine": "buildhost-box", "name": "other", "spec": spec})
	if code != 403 && code != 404 && code != 409 {
		t.Errorf("a person without a role filed a project runbook on her machine: %d", code)
	}
}
