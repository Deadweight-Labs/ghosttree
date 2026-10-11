package server

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestWritableProjectsFollowTheWriteMatrix(t *testing.T) {
	f := accessAPI(t, true)
	list := func(who string) []string {
		t.Helper()
		var out []string
		if err := json.Unmarshal([]byte(f.expect(t, who, 200, "GET", "/api/projects/writable", nil)), &out); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out)
		return out
	}
	if got := list("robin"); !slices.Equal(got, []string{accProject, accOther}) {
		t.Errorf("org owner writes to every project of the org, got %v", got)
	}
	if got := list("mia"); !slices.Equal(got, []string{accProject}) {
		t.Errorf("a member writes only where she has a role, got %v", got)
	}
	if got := list("gus"); len(got) != 0 {
		t.Errorf("a guest writes nowhere, got %v", got)
	}
	if got := list("nora"); len(got) != 0 {
		t.Errorf("a stranger writes nowhere, got %v", got)
	}
}

// Without write right the target must look exactly like one that does not exist.
func TestMoveTargetWithoutWriteRightIsIndistinguishableFromUnknown(t *testing.T) {
	f := accessAPI(t, true)
	mia := f.id["k-mia"]
	moves := []struct{ name, path string }{
		{"knowledge", idPath("/api/knowledge/%d/move", mia)},
		{"request", idPath("/api/requests/%d/move", f.id["r-mia"])},
	}
	for _, m := range moves {
		foreign, body1 := f.call(t, "mia", "POST", m.path, map[string]any{"project": accOther})
		unknown, body2 := f.call(t, "mia", "POST", m.path, map[string]any{"project": "github.com/nobody/nothing"})
		if foreign != 404 || unknown != 404 || body1 != body2 {
			t.Errorf("%s: foreign project %d %q, unknown project %d %q: they must be the same answer", m.name, foreign, body1, unknown, body2)
		}
	}
	// Nothing moved.
	k, err := f.st.KnowledgeByID(mia)
	if err != nil || k.Scope.Project != accProject {
		t.Fatalf("entry moved or lost: %+v %v", k, err)
	}
}

func TestMoveKnowledgeToProjectMachineAndGlobalIsLogged(t *testing.T) {
	f := accessAPI(t, true)
	id := f.id["k-mia"]
	path := idPath("/api/knowledge/%d/move", id)

	// A short name resolves among the projects the account can write to.
	f.expect(t, "robin", 200, "POST", path, map[string]any{"project": "q"})
	k, _ := f.st.KnowledgeByID(id)
	if k.Scope.Project != accOther || k.Scope.Machine != "" {
		t.Fatalf("after project move: %+v", k.Scope)
	}
	f.expect(t, "robin", 200, "POST", path, map[string]any{"machine": "Media-VM"})
	k, _ = f.st.KnowledgeByID(id)
	if k.Scope.Project != "" || k.Scope.Machine != "media-vm" {
		t.Fatalf("after machine move: %+v", k.Scope)
	}
	f.expect(t, "robin", 200, "POST", path, map[string]any{"global": true})
	k, _ = f.st.KnowledgeByID(id)
	if !k.Scope.IsGlobal() {
		t.Fatalf("after global move: %+v", k.Scope)
	}
	f.expect(t, "robin", 409, "POST", path, map[string]any{"global": true}) // already there
	f.expect(t, "robin", 400, "POST", path, map[string]any{"global": true, "machine": "x"})
	f.expect(t, "robin", 400, "POST", path, map[string]any{})

	moves, err := f.st.KnowledgeMoves(id)
	if err != nil || len(moves) != 3 {
		t.Fatalf("moves = %+v, %v", moves, err)
	}
	if moves[2].From != accProject || moves[2].To != accOther || moves[2].MovedBy != "robin" || moves[0].To != "global" {
		t.Errorf("the log does not say who moved what where: %+v", moves)
	}
}

func TestMoveKnowledgeNeedsWriteRightInSourceAndTarget(t *testing.T) {
	f := accessAPI(t, true)
	// Source: a member may not edit another member's entry.
	f.expect(t, "mia", 403, "POST", idPath("/api/knowledge/%d/move", f.id["k-robin-staged"]), map[string]any{"global": true})
	// Source in a project she has no role in: it does not exist for her.
	f.expect(t, "mia", 404, "POST", idPath("/api/knowledge/%d/move", f.id["k-other"]), map[string]any{"global": true})
	// Target: her own entry may go where she may write, not where she may not.
	f.expect(t, "mia", 404, "POST", idPath("/api/knowledge/%d/move", f.id["k-mia"]), map[string]any{"project": accOther})
	// A guest cannot move at all.
	f.expect(t, "gus", 403, "POST", idPath("/api/knowledge/%d/move", f.id["k-mia"]), map[string]any{"global": true})
	// A machine another account owns stays taken.
	f.expect(t, "lena", 409, "POST", idPath("/api/knowledge/%d/move", f.id["k-mia"]), map[string]any{"machine": "mia-box"})
	k, _ := f.st.KnowledgeByID(f.id["k-mia"])
	if k.Scope.Project != accProject {
		t.Fatalf("a refused move changed the entry: %+v", k.Scope)
	}
}

func TestMoveRequestWritesTheActivityLogAndLeavesTheOldLedger(t *testing.T) {
	f := accessAPI(t, true)
	id := f.id["r-robin"]
	// Mia may not edit robin's request.
	f.expect(t, "mia", 403, "POST", idPath("/api/requests/%d/move", id), map[string]any{"project": accProject})
	f.expect(t, "robin", 200, "POST", idPath("/api/requests/%d/move", id), map[string]any{"project": "q", "reason": "belongs to the other project"})
	d, err := f.st.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Request.Scope.Project != accOther {
		t.Fatalf("request is in %q", d.Request.Scope.Project)
	}
	last := d.Activity[len(d.Activity)-1]
	if last.Kind != "request.moved" || last.Person != "robin" || !strings.Contains(last.Data, accProject) || !strings.Contains(last.Data, accOther) || !strings.Contains(last.Data, "belongs to the other project") {
		t.Errorf("activity = %+v", last)
	}
	var page requestdomain.SearchPage
	if err := json.Unmarshal([]byte(f.expect(t, "robin", 200, "GET", "/api/requests/search?project="+accProject+"&state=open&limit=25", nil)), &page); err != nil {
		t.Fatal(err)
	}
	for _, h := range page.Results {
		if h.Request.ID == id {
			t.Errorf("the moved request is still in the old project's list")
		}
	}
	// A request cannot be moved to nowhere.
	f.expect(t, "robin", 400, "POST", idPath("/api/requests/%d/move", id), map[string]any{"project": ""})
}

func TestRequestWithoutProjectIsNotCreated(t *testing.T) {
	f := accessAPI(t, true)
	code, body := f.call(t, "robin", "POST", "/api/requests", map[string]any{"type": "feature", "title": "nowhere"})
	if code != 400 || !strings.Contains(body, "project_required") {
		t.Fatalf("a request without a project: %d %s", code, body)
	}
}

// The 31 global requests used to appear in the ledger of every project.
func TestProjectlessRequestsLeaveEveryProjectLedger(t *testing.T) {
	f := accessAPI(t, true)
	if _, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "orphan from a projectless session", Person: "robin"}}); err != nil {
		t.Fatal(err)
	}
	var page requestdomain.SearchPage
	body := f.expect(t, "robin", 200, "GET", "/api/requests/search?project="+accProject+"&state=open&limit=25", nil)
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Results) == 0 {
		t.Fatal("the project ledger is empty")
	}
	for _, h := range page.Results {
		if h.Request.Scope.Project == "" {
			t.Errorf("projectless request in the project ledger: %+v", h.Request)
		}
	}
	// The bootstrap counts only the project's own requests: two are open in p.
	boot := f.expect(t, "robin", 200, "GET", "/api/context/bootstrap?project="+accProject, nil)
	if !strings.Contains(boot, "2 open requests") {
		t.Errorf("bootstrap counts the orphan too: %s", boot)
	}
	// Without a project nothing narrows the ledger: the orphan is still findable.
	body = f.expect(t, "robin", 200, "GET", "/api/requests/search?q=orphan", nil)
	if !strings.Contains(body, "orphan from a projectless session") {
		t.Errorf("the orphan is nowhere: %s", body)
	}
}

// A machine nobody ever ran on (an ssh target) takes notes, and a session on it
// receives them.
func TestNoteForAMachineWithoutSessionsReachesSessionsOnIt(t *testing.T) {
	f := accessAPI(t, true)
	title := "media-vm: transcoder crashes on hevc"
	f.expect(t, "mia", 200, "POST", "/api/knowledge", map[string]any{
		"type": "pitfall", "title": title, "body": "use the software path",
		"scope": scope.Axes{Machine: "media-vm"},
	})
	if !strings.Contains(f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?machine=media-vm", nil), title) {
		t.Error("the note is not in the bootstrap of a session on media-vm")
	}
	if strings.Contains(f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?machine=other-box", nil), title) {
		t.Error("the note leaked to another machine")
	}
	// Somebody else's note about a machine nobody claimed is not theirs to read.
	if strings.Contains(f.expect(t, "rex", 200, "GET", "/api/context/bootstrap?machine=media-vm", nil), title) {
		t.Error("a stranger read a note about an unclaimed machine")
	}
	// Once she claims the machine it stays hers.
	if err := f.st.ClaimMachine("media-vm", "person:3"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?machine=media-vm", nil), title) {
		t.Error("the note vanished when the machine was claimed")
	}
}
