package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	cliHere  = "github.com/acme/ghosttree"
	cliInfra = "github.com/acme/infra"
)

func moveCLIFixture(t *testing.T) *store.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddAccount("alice", "", true); err != nil {
		t.Fatal(err)
	}
	token, _, err := st.CreateToken("alice", store.TokenSpec{Label: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:1", "Acme", "acme"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{cliHere, cliInfra} {
		if _, err := st.EnsureProject("person:1", p); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: token, Machine: "mainex"}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestKnowledgeMoveCLI(t *testing.T) {
	st := moveCLIFixture(t)
	id, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "misfiled", Body: "b", Scope: scope.Axes{Project: cliHere}, Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	move := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(append([]string{"knowledge", "move", strconv.FormatInt(id, 10)}, args...), &out)
		return code, out.String()
	}
	if code, out := move("--project", "infra"); code != 0 || !strings.Contains(out, cliInfra) {
		t.Fatalf("project: %d %s", code, out)
	}
	if code, out := move("--machine", "media-vm"); code != 0 || !strings.Contains(out, "machine:media-vm") {
		t.Fatalf("machine: %d %s", code, out)
	}
	if code, out := move("--global"); code != 0 || !strings.Contains(out, "global") {
		t.Fatalf("global: %d %s", code, out)
	}
	if k, _ := st.KnowledgeByID(id); !k.Scope.IsGlobal() {
		t.Errorf("scope = %+v", k.Scope)
	}
	if code, _ := move(); code != 2 {
		t.Errorf("no target: %d", code)
	}
	if code, _ := move("--global", "--machine", "x"); code != 2 {
		t.Errorf("two targets: %d", code)
	}
	if code, out := move("--project", "nowhere"); code != 1 || !strings.Contains(out, "projects you can write to") {
		t.Errorf("unknown project: %d %s", code, out)
	}
	if moves, _ := st.KnowledgeMoves(id); len(moves) != 3 {
		t.Errorf("moves = %+v", moves)
	}
}

func TestRequestCLICreateTargetsAndMove(t *testing.T) {
	st := moveCLIFixture(t)
	var out bytes.Buffer
	if code := run([]string{"request", "create", "--type", "bug", "--title", "vpn flaps", "--project", "infra", "--json"}, &out); code != 0 {
		t.Fatalf("create: %d %s", code, out.String())
	}
	page, _ := st.SearchRequests(requestdomain.SearchFilter{Scope: scope.Axes{Project: cliInfra}, Limit: 10})
	if len(page.Results) != 1 {
		t.Fatalf("request not in infra: %+v", page)
	}
	id := page.Results[0].Request.ID

	out.Reset()
	if code := run([]string{"request", "move", strconv.FormatInt(id, 10), "--project", cliHere, "--reason", "wrong repo"}, &out); code != 0 {
		t.Fatalf("move: %d %s", code, out.String())
	}
	d, _ := st.RequestByID(id)
	if d.Request.Scope.Project != cliHere || d.Activity[len(d.Activity)-1].Kind != "request.moved" {
		t.Errorf("request = %+v", d)
	}
	out.Reset()
	if code := run([]string{"request", "move", strconv.FormatInt(id, 10), "--project", "nowhere"}, &out); code != 1 {
		t.Errorf("move to an unknown project: %d %s", code, out.String())
	}
}

// Round 2, B end to end: what the bootstrap would not hand out does not reach
// the files either.
func TestMirrorLeavesOutWhatIsNotDeliverable(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.AddAccount("bob", "", false); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	add := func(title, person string, ax scope.Axes) {
		t.Helper()
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: title, Body: "body " + title, Scope: ax, Person: person, Confidence: "trusted"}); err != nil {
			t.Fatal(err)
		}
	}
	add("ordinary-project-note", "alice", scope.Axes{Project: cliHere})
	// A note by another account for a machine its author does not own.
	add("planted-machine-note", "bob", scope.Axes{Machine: "vic-laptop"})
	// A project note by somebody who is no writer of the project.
	add("planted-project-note", "mallory", scope.Axes{Project: cliHere})
	repo := gitRepo(t)
	cfg, _ := config.Load()
	if err := WriteMirror(client.New(cfg), scope.Axes{Project: cliHere, Machine: "vic-laptop"}, repo); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	_ = filepath.WalkDir(filepath.Join(repo, ".ghosttree"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(path)
			all.Write(b)
		}
		return nil
	})
	if !strings.Contains(all.String(), "ordinary-project-note") {
		t.Fatal("the mirror lost ordinary knowledge")
	}
	for _, planted := range []string{"planted-machine-note", "planted-project-note"} {
		if strings.Contains(all.String(), planted) {
			t.Errorf("%s reached the mirror", planted)
		}
	}
}
