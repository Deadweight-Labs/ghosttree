package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	docwork "github.com/Deadweight-Labs/ghosttree/internal/doc"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-603 Kriterium 1 am CLI: jeder Befehl, der genau ein Objekt beantwortet,
// zeigt Link und Karte, ohne Login im Link.
func TestRequestWriteCommandsPrintTheCard(t *testing.T) {
	_, base := openFixture(t)
	want := []string{"Issue REQ-1: Kurzwege", "link: " + base + "/issues/1"}
	check := func(what string, code int, out string, extra ...string) {
		t.Helper()
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s", what, code, out)
		}
		for _, w := range append(append([]string{}, want...), extra...) {
			if !strings.Contains(out, w) {
				t.Errorf("%s lacks %q:\n%s", what, w, out)
			}
		}
		if strings.Contains(out, "token=") || strings.Contains(out, "?token") {
			t.Errorf("%s carries a login in a link", what)
		}
	}
	code, out := painTaskRun(t, "request", "ac", "add", "1", "noch eins")
	check("ac add", code, out)
	code, out = painTaskRun(t, "request", "ac", "add", "1", "und eins", "--json")
	if code != 0 || !strings.Contains(out, `"card":{"kind":"Issue","ref":"REQ-1"`) {
		t.Errorf("ac add json: %d %s", code, out)
	}
	for _, id := range []string{"1", "2", "3", "4"} {
		code, out = painTaskRun(t, "request", "ac", "met", "1", id, "--evidence-kind", "commit", "--evidence-ref", "abcdef1")
		check("ac met "+id, code, out)
	}
	code, out = painTaskRun(t, "request", "done", "1", "--evidence-kind", "commit", "--evidence-ref", "abcdef1")
	check("done", code, out, "state: done")
}

func TestPackageKnowledgeRunbookPresetAndSessionCommandsPrintTheCard(t *testing.T) {
	st, base := openFixture(t)
	root := strings.TrimSuffix(base, "/acme/ghosttree")
	have := func(what string, code int, out string, wants ...string) {
		t.Helper()
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s", what, code, out)
		}
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s lacks %q:\n%s", what, w, out)
			}
		}
	}
	code, out := painTaskRun(t, "package", "create", "--project", cliHere, "--branch", "feat/cards", "--name", "Karten", "1")
	have("package create", code, out, "Checklist PKG-1: Karten", "link: "+base+"/packages/1")
	code, out = painTaskRun(t, "package", "show", "--package", "1", "--json")
	have("package show json", code, out, `"card":{"kind":"Checklist","ref":"PKG-1"`)

	kid, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "Merke", Body: "b", Scope: scope.Axes{Project: cliHere}, Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	code, out = painTaskRun(t, "knowledge", "show", itoa(kid))
	have("knowledge show", code, out, "Knowledge #1: Merke", "link: "+base+"/knowledge/1")
	code, out = painTaskRun(t, "knowledge", "retire", itoa(kid), "--reason", "stimmt nicht mehr")
	have("knowledge retire", code, out, "retired #1", "Knowledge #1", "link: "+base+"/knowledge/1")

	code, out = painTaskRun(t, "search", "--project", cliHere, "--save", "pr-bugs", "--line", "type:bug pr:{pr}")
	have("search --save", code, out, "saved pr-bugs", "Search preset:pr-bugs", "link: "+root+"/presets/me/pr-bugs")
	code, out = painTaskRun(t, "search", "--project", cliHere, "--preset", "branch-offen", "--branch", "feat/cards")
	have("search --preset", code, out, "Search preset:branch-offen", "link: "+root+"/presets/repo/branch-offen?branch=feat%2Fcards&project=acme%2Fghosttree")

	sid, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s-card", AccountID: 1, Scope: scope.Axes{Project: cliHere}, Title: "Karten"})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := st.ListSessions(scope.Axes{Project: cliHere}, 5)
	if err != nil || len(listed) != 1 {
		t.Fatalf("sessions %v %v", listed, err)
	}
	// The card is found through the project of the working copy.
	t.Chdir(coordRepo(t, "https://github.com/acme/ghosttree.git"))
	code, out = painTaskRun(t, "session", "share", itoa(sid))
	have("session share", code, out, "shared with the members", "Session "+listed[0].PublicID, "link: "+base+"/sessions/"+listed[0].PublicID)

	file := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(file, []byte(rbCLISpec), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = painTaskRun(t, "runbook", "write", "rollout", "--file", file, "--project", cliHere)
	have("runbook write", code, out, "Runbook rollout", "link: "+base+"/runbooks/rollout")
	code, out = painTaskRun(t, "runbook", "show", "rollout", "--project", cliHere)
	have("runbook show", code, out, "Runbook rollout", "link: "+base+"/runbooks/rollout")
}

func TestCoordSendPrintsTheMessageCard(t *testing.T) {
	_, cfg := coordCLIEnv(t)
	repo := coordRepo(t, "https://github.com/x/card.git")
	var out bytes.Buffer
	if code := run([]string{"coord", "send", "Rollout ist durch", repo}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	room := "project%3Agithub.com%2Fx%2Fcard"
	for _, w := range []string{"stored as 1 in project:github.com/x/card", "Message #1: Rollout ist durch", "link: " + cfg.ServerURL + "/ui/coord?room=" + room} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("coord send lacks %q:\n%s", w, out.String())
		}
	}
}

func TestDocCommandsPrintTheCard(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("alice")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	project := "github.com/x/y"
	card := "link: " + srv.URL + "/ui/x/y/docs/design"
	root := t.TempDir()
	if code := docNew(root, "", []string{"spec", "design"}, &bytes.Buffer{}); code != 0 {
		t.Fatal("new failed")
	}
	state, _ := docwork.LoadState(root)
	if err := docwork.WriteFile(root, state["design"].Path, "# Entwurf\n"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := docPush(root, project, c, "design", "erste Fassung", false, &out); code != 0 {
		t.Fatalf("push: %s", out.String())
	}
	for _, w := range []string{"design rev 1", "Document design: Entwurf", "state: active r1", card} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("push lacks %q:\n%s", w, out.String())
		}
	}
	out.Reset()
	if code := docPull(root, project, c, "design", true, &out); code != 0 || !strings.Contains(out.String(), card) {
		t.Errorf("pull: %s", out.String())
	}
	// Der Text allein bleibt auf stdout, die Karte geht daneben.
	var side bytes.Buffer
	old := docShowCardOut
	docShowCardOut = &side
	t.Cleanup(func() { docShowCardOut = old })
	out.Reset()
	if code := docShow(project, c, "design", 0, &out); code != 0 || out.String() != "# Entwurf\n" {
		t.Errorf("show must print the text alone: %q", out.String())
	}
	if !strings.Contains(side.String(), card) {
		t.Errorf("show card: %q", side.String())
	}
	out.Reset()
	if code := docClose(project, c, "design", &out); code != 0 || !strings.Contains(out.String(), "design archived") || !strings.Contains(out.String(), card) {
		t.Errorf("close: %s", out.String())
	}
}
