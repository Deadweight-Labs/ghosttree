package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const anchorProject = "github.com/dw/anchors"

func anchorTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// anchorCommit legt eine neue Datei an, committet sie und liefert den Hash.
func anchorCommit(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	anchorTestGit(t, dir, "add", name)
	anchorTestGit(t, dir, "commit", "-q", "-m", name)
	return anchorTestGit(t, dir, "rev-parse", "HEAD")
}

// Der ganze Weg: ein Client im Arbeitsverzeichnis eines echten Repos schreibt,
// der Server speichert den Anker je Fassung, die Suche filtert danach.
func TestWritesCarryTheGitAnchorFromTheWorkingDirectory(t *testing.T) {
	srv, token := newTestServer(t)
	dir := t.TempDir()
	anchorTestGit(t, dir, "init", "-q", "-b", "feat-x")
	first := anchorCommit(t, dir, "a.txt")

	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	c.SetAnchorSource(func() gitanchor.Anchor { return collector.ResolveAnchor(dir) })
	ax := scope.Axes{Project: anchorProject}
	write := func(title string) store.Knowledge {
		t.Helper()
		k, err := c.Remember(store.Knowledge{Type: "note", Title: title, Body: "gemeinsames stichwort " + title, Scope: ax}, ax)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	k1 := write("erster")
	if k1.GitAnchor == nil || k1.GitAnchor.Commit != first || k1.GitAnchor.Branch != "feat-x" || k1.GitAnchor.Dirty {
		t.Fatalf("anchor of the first write: %+v, want commit %s on feat-x", k1.GitAnchor, first)
	}

	// HEAD wandert zwischen zwei Schreibzugriffen: der zweite trägt den neuen
	// Stand, der erste behält seinen.
	second := anchorCommit(t, dir, "b.txt")
	k2 := write("zweiter")
	if k2.GitAnchor == nil || k2.GitAnchor.Commit != second {
		t.Fatalf("anchor of the second write: %+v, want %s", k2.GitAnchor, second)
	}
	again, err := c.KnowledgeByID(k1.ID)
	if err != nil || again.GitAnchor == nil || again.GitAnchor.Commit != first {
		t.Fatalf("the first entry changed its anchor: %+v (%v)", again.GitAnchor, err)
	}

	titlesAt := func(f client.AnchorFilter) string {
		t.Helper()
		res, err := c.SearchAnchored("stichwort", "knowledge", ax, 20, true, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, k := range res.Knowledge {
			out = append(out, k.Title)
		}
		return strings.Join(out, ",")
	}
	if got := titlesAt(client.AnchorFilter{Commit: first[:7]}); got != "erster" {
		t.Errorf("commit filter on the first commit: %q", got)
	}
	if got := titlesAt(client.AnchorFilter{Commit: second}); got != "zweiter" {
		t.Errorf("commit filter on the second, full hash: %q", got)
	}
	if got := titlesAt(client.AnchorFilter{PR: 12}); got != "" {
		t.Errorf("pr filter before any pull request is known: %q", got)
	}
	if got := titlesAt(client.AnchorFilter{}); !strings.Contains(got, "erster") || !strings.Contains(got, "zweiter") {
		t.Errorf("no filter: %q", got)
	}

	// Eine Beschreibung bekommt denselben Anker, und die Dateisuche filtert danach.
	if _, err := c.PutGhost(store.GhostFile{Project: anchorProject, Path: "internal/a.go", Description: "beschreibt eine datei", ContentSHA: "x"}); err != nil {
		t.Fatal(err)
	}
	gs, err := c.SearchGhostsAnchored("datei", anchorProject, 10, client.AnchorFilter{Commit: second[:8]})
	if err != nil || len(gs) != 1 || gs[0].GitAnchor == nil || gs[0].GitAnchor.Commit != second {
		t.Fatalf("ghost search by commit: %+v (%v)", gs, err)
	}
	if gs, err = c.SearchGhostsAnchored("datei", anchorProject, 10, client.AnchorFilter{Commit: first[:8]}); err != nil || len(gs) != 0 {
		t.Fatalf("ghost search by another commit: %+v (%v)", gs, err)
	}

	// Request: Anlegen und Abschluss tragen den Stand.
	d, err := c.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "mit Anker", Scope: ax, Origin: "agent"},
		Criteria: []string{"eins"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetRequestCriterion(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test"}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetRequest(d.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ev := got.Criteria[0].Evidence; len(ev) != 1 || ev[0].GitAnchor == nil || ev[0].GitAnchor.Commit != second {
		t.Fatalf("criterion evidence anchor: %+v", ev)
	}
}

// Ohne Quelle (Hooks, Verwaltungsbefehle), ohne Repo und ohne Git geht kein
// Anker mit, und der Schreibzugriff gelingt trotzdem.
func TestWritesWithoutAGitAnchorStillSucceed(t *testing.T) {
	srv, token := newTestServer(t)
	cfg := config.Config{ServerURL: srv.URL, Token: token}
	ax := scope.Axes{Project: anchorProject}
	write := func(c *client.Client) store.Knowledge {
		t.Helper()
		k, err := c.Remember(store.Knowledge{Type: "note", Title: "t", Body: "x", Scope: ax}, ax)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	if k := write(client.New(cfg)); k.GitAnchor != nil {
		t.Fatalf("a client without a source sent an anchor: %+v", k.GitAnchor)
	}
	noRepo := client.New(cfg)
	noRepo.SetAnchorSource(func() gitanchor.Anchor { return collector.ResolveAnchor(t.TempDir()) })
	if k := write(noRepo); k.GitAnchor != nil {
		t.Fatalf("a directory outside a repository produced an anchor: %+v", k.GitAnchor)
	}
	noGit := client.New(cfg)
	noGit.SetAnchorSource(func() gitanchor.Anchor { return gitanchor.Anchor{} })
	if k := write(noGit); k.GitAnchor != nil {
		t.Fatalf("an empty anchor was stored: %+v", k.GitAnchor)
	}
}

// Ein Feld git_anchor im Body zählt nicht, auch die PR-Nummer im Header nicht:
// gespeichert wird nur, was der Client gemessen hat, und nur den Stand.
func TestTheBodyAndThePRNumberAreNeverTrusted(t *testing.T) {
	srv, token := newTestServer(t)
	post := func(header string) store.Knowledge {
		t.Helper()
		body := `{"type":"note","title":"t","body":"x","scope":{"project":"` + anchorProject + `"},` +
			`"git_anchor":{"commit":"` + strings.Repeat("c", 40) + `","pr":99}}`
		req, err := http.NewRequest("POST", srv.URL+"/api/knowledge", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set(gitanchor.Header, header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		var k store.Knowledge
		if err := json.NewDecoder(resp.Body).Decode(&k); err != nil {
			t.Fatal(err)
		}
		return k
	}
	if k := post(""); k.GitAnchor != nil {
		t.Fatalf("an anchor from the body was stored: %+v", k.GitAnchor)
	}
	pr := int64(5)
	header := gitanchor.Encode(gitanchor.Anchor{Commit: strings.Repeat("d", 40), Branch: "b", PR: &pr})
	k := post(header)
	if k.GitAnchor == nil || k.GitAnchor.Commit != strings.Repeat("d", 40) || k.GitAnchor.PR != nil {
		t.Fatalf("header anchor: %+v", k.GitAnchor)
	}
	if k := post("kein-base64-%%%"); k.GitAnchor != nil {
		t.Fatalf("a broken header produced an anchor: %+v", k.GitAnchor)
	}
}

func TestAnAnchorFilterThatIsNotUsableIsRejected(t *testing.T) {
	srv, token := newTestServer(t)
	for _, q := range []string{"commit=abc", "commit=nothex1", "pr=0", "pr=x"} {
		req, _ := http.NewRequest("GET", srv.URL+"/api/search?q=x&kind=knowledge&"+q, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: a filter that is ignored answers a different question", q, resp.StatusCode)
		}
	}
}
