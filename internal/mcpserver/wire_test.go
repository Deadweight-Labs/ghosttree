package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect wires a real client to a real server over an in-memory transport.
//
// It exists because the handler tests do not cover the path an agent takes.
// They call handleSearch directly and skip schema validation entirely — so
// `query` could sit there marked required while the tool's own description told
// agents to omit it, and every test stayed green. A probe agent found that in
// one call. This is the cheap version of that agent.
func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "ghosttree", Version: "test"}, nil)
	s.Register(srv)
	serverT, clientT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// callTool returns the text an agent would see, and whether the call failed —
// either at the schema boundary or inside the handler.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error(), true
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func TestContextGetWithPathsReturnsGhostContextOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	for _, g := range []store.GhostFile{
		{Project: project, Path: "internal", Kind: "dir", Description: "der innere Bauplan"},
		{Project: project, Path: "internal/mcpserver", Kind: "dir", Description: "die MCP-Werkzeuge"},
	} {
		if _, err := st.PutGhostFile(g); err != nil {
			t.Fatal(err)
		}
	}
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})

	got, failed := callTool(t, session, "context_get",
		map[string]any{"paths": []string{"internal/mcpserver/mcpserver.go"}})
	if failed {
		t.Fatalf("Pfadkontext wurde am MCP-Draht abgewiesen: %s", got)
	}
	for _, want := range []string{"der innere Bauplan", "die MCP-Werkzeuge"} {
		if !strings.Contains(got, want) {
			t.Errorf("MCP-Antwort fehlt %q: %s", want, got)
		}
	}
}

func TestReadingOneEntryNeedsNothingButItsID(t *testing.T) {
	c, st := newTestClient(t)
	id := storeSpec(t, st)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})

	// Exactly the call the tool description asks for: an id and nothing else.
	got, failed := callTool(t, session, "context_search", map[string]any{"knowledge_id": id})
	if failed {
		t.Fatalf("reading by id was rejected: %s", got)
	}
	for _, want := range []string{"# Teil 1: Schemaänderungen", "- knowledge.origin: NEU", "```sql"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from the full text", want)
		}
	}
	if strings.Count(got, "\n") < 100 {
		t.Errorf("full text came back with %d line breaks", strings.Count(got, "\n"))
	}
}

// With neither an id nor words there is nothing to answer, and the refusal has
// to say which of the two is missing.
func TestSearchWithoutQueryOrIDSaysWhatIsMissing(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})

	got, failed := callTool(t, session, "context_search", map[string]any{})
	if !failed {
		t.Fatalf("an empty search was answered: %s", got)
	}
	for _, want := range []string{"query", "knowledge_id"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal does not name %q: %s", want, got)
		}
	}
}

func TestSearchingByWordsStillWorksOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	storeSpec(t, st)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})

	got, failed := callTool(t, session, "context_search",
		map[string]any{"query": "Tabellenneubau", "kind": "knowledge"})
	if failed {
		t.Fatalf("search failed: %s", got)
	}
	if !strings.Contains(got, "Spec Distiller Stück 1") {
		t.Fatalf("the entry was not found: %.300s", got)
	}
	if !strings.Contains(got, "knowledge_id") {
		t.Errorf("a shortened hit does not point at the full text: %.300s", got)
	}
}

func TestCurrentSessionDoesNotComeBackAsEvidenceOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	for _, externalID := range []string{"current-session", "older-session"} {
		id, err := st.UpsertSession(store.Session{Harness: "codex", ExternalID: externalID,
			Scope: scope.Axes{Project: project}})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "eigenbeleg", Raw: "{}"}}); err != nil {
			t.Fatal(err)
		}
	}
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}, sessionRef: "current-session"})

	for tool, args := range map[string]map[string]any{
		"context_search":   {"query": "eigenbeleg", "kind": "sessions"},
		"context_sessions": {"query": "eigenbeleg"},
	} {
		got, failed := callTool(t, session, tool, args)
		if failed {
			t.Fatalf("%s failed: %s", tool, got)
		}
		// Die Werkzeugausgabe nennt bewusst die interne numerische ID, nicht die
		// external_id. #1 ist die aktuelle und #2 die frühere echte Sitzung.
		if strings.Contains(got, "- #1 ") || !strings.Contains(got, "- #2 ") {
			t.Fatalf("%s: current session leaked or earlier evidence vanished: %s", tool, got)
		}
	}
}

// Placement stays a required field: the model has to choose before the call is
// accepted at all, which is the point of asking.
func TestPlacementIsRequiredAtTheWire(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"}})

	got, failed := callTool(t, session, "context_remember",
		map[string]any{"type": "note", "title": "ohne Einordnung", "body": "egal"})
	if !failed {
		t.Fatalf("an entry was filed without a placement: %s", got)
	}
	if !strings.Contains(got, "scope_hint") {
		t.Errorf("the refusal does not name the missing field: %s", got)
	}

	got, failed = callTool(t, session, "context_remember",
		map[string]any{"type": "note", "title": "mit Einordnung", "body": "egal", "scope_hint": "project"})
	if failed {
		t.Fatalf("a placed entry was rejected: %s", got)
	}
	if !strings.Contains(got, "stored #") {
		t.Errorf("unexpected reply: %s", got)
	}
}

// The question that decides the placement has to reach the model before it
// calls, because the schema rejection itself is generic and carries none of it.
func TestThePlacementQuestionIsInTheSchema(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "context_remember" {
			continue
		}
		// The schema is whatever the wire carries, so read it the way a client
		// does rather than through the Go type it happened to be built from.
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		hint, ok := schema.Properties["scope_hint"]
		if !ok {
			t.Fatalf("context_remember has no scope_hint property: %s", raw)
		}
		for _, want := range []string{"merged or abandoned", "required"} {
			if !strings.Contains(hint.Description, want) {
				t.Errorf("scope_hint description does not carry %q: %s", want, hint.Description)
			}
		}
		if !slices.Contains(schema.Required, "scope_hint") {
			t.Errorf("scope_hint is not required, so a default creeps back in: %v", schema.Required)
		}
		if slices.Contains(schema.Required, "query") {
			t.Error("query is required somewhere it should not be")
		}
		return
	}
	t.Fatal("context_remember is not registered")
}

// all_branches promised width and delivered a blind spot: clearing the branch
// removes every branch clause, so the flag returned strictly fewer entries than
// the default and none of the branch-scoped ones. A probe agent reached for it
// to check whether a hidden entry existed at all, and would have been told no.
func TestAllBranchesWidensInsteadOfBlanking(t *testing.T) {
	c, st := newTestClient(t)
	for title, branch := range map[string]string{
		"Zweigwissen develop": "develop",
		"Zweigwissen feat-y":  "feat/y",
	} {
		if _, err := st.InsertKnowledge(store.Knowledge{
			Type: "note", Title: title, Body: "b", Confidence: "trusted",
			Scope: scope.Axes{Project: "github.com/x/y", Branch: branch},
		}); err != nil {
			t.Fatal(err)
		}
	}
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{
		Project: "github.com/x/y", Branch: "feat/x", Lineage: []string{"develop"},
	}})

	// The default still respects the line: ancestor yes, sibling no.
	got, failed := callTool(t, session, "context_search",
		map[string]any{"query": "Zweigwissen", "kind": "knowledge"})
	if failed {
		t.Fatalf("search failed: %s", got)
	}
	if !strings.Contains(got, "Zweigwissen develop") || strings.Contains(got, "Zweigwissen feat-y") {
		t.Errorf("default search does not follow the chain: %s", got)
	}

	// Asking for every branch has to show the sibling too.
	got, failed = callTool(t, session, "context_search",
		map[string]any{"query": "Zweigwissen", "kind": "knowledge", "all_branches": true})
	if failed {
		t.Fatalf("all_branches search failed: %s", got)
	}
	for _, want := range []string{"Zweigwissen develop", "Zweigwissen feat-y"} {
		if !strings.Contains(got, want) {
			t.Errorf("all_branches did not reveal %q: %s", want, got)
		}
	}
}

func TestFullTextRetrievalCarriesTheObservationTime(t *testing.T) {
	c, st := newTestClient(t)
	id, err := st.InsertKnowledge(store.Knowledge{
		Type: "pitfall", Title: "Alter Befund", Body: "Beobachtet lange vor der Ablage.",
		Scope: scope.Axes{Project: "github.com/x/y"}, Confidence: "trusted",
		ObservedAt: "2026-06-15T09:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})

	got, failed := callTool(t, session, "context_search", map[string]any{"knowledge_id": id})
	if failed {
		t.Fatalf("read failed: %s", got)
	}
	if !strings.Contains(got, "observed:2026-06-15") {
		t.Errorf("the reader cannot tell when this was seen: %.200s", got)
	}
}

// The bootstrap has to carry an inherited entry, and the wire is where that is
// worth checking: the axes are assembled in one place and read in another.
func TestBootstrapOverTheWireInheritsFromTheParentBranch(t *testing.T) {
	c, st := newTestClient(t)
	for title, branch := range map[string]string{
		"auf develop": "develop",
		"auf feat-y":  "feat/y",
	} {
		if _, err := st.InsertKnowledge(store.Knowledge{
			Type: "pitfall", Title: title, Body: "b", Confidence: "trusted",
			Scope: scope.Axes{Project: "github.com/x/y", Branch: branch},
		}); err != nil {
			t.Fatal(err)
		}
	}
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{
		Project: "github.com/x/y", Branch: "feat/x", Lineage: []string{"develop", "main"},
	}})

	got, failed := callTool(t, session, "context_get", map[string]any{})
	if failed {
		t.Fatalf("bootstrap failed: %s", got)
	}
	if !strings.Contains(got, "auf develop") {
		t.Errorf("feat/x does not inherit from develop: %s", got)
	}
	if strings.Contains(got, "auf feat-y") {
		t.Errorf("a sibling branch leaked into the bootstrap: %s", got)
	}
}

// gitRepoWithFile baut ein winziges, versioniertes Repo. Ghost-Schreibpfade
// brauchen es: trackedInRepo weist ab, was `git ls-files` nicht kennt, und ein
// Test ohne echtes Repo pruefte damit einen Weg, den es in Wirklichkeit nicht
// gibt.
func gitRepoWithFile(t *testing.T, name, content string) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", name)
	run("commit", "-m", "i")
	return repo
}

// Der Aufruf, den die Werkzeugbeschreibung verspricht, muss die
// Schemapruefung passieren — nicht nur den Handler erreichen. Genau hier stand
// schon einmal ein Pflichtfeld, waehrend die Beschreibung zum Weglassen
// aufforderte: vier gruene Tests, ein fuer Agenten unbenutzbares Werkzeug
// (#846).
func TestDescribeFileAcceptsNothingToSayWithoutDescription(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	repo := gitRepoWithFile(t, "foo.go", "package foo\n")
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}, repoRoot: repo})

	got, failed := callTool(t, session, "context_describe_file",
		map[string]any{"path": "foo.go", "nothing_to_say": true})
	if failed {
		t.Fatalf("nothing_to_say ohne description muss ein gueltiger Aufruf sein: %s", got)
	}
	if !strings.Contains(got, "nichts zu sagen") {
		t.Errorf("die Antwort soll den Zustand bestaetigen, got %q", got)
	}

	reviews, err := st.GhostReviewsUnder(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0].Path != "foo.go" {
		t.Fatalf("want one review on foo.go, got %+v", reviews)
	}
	// Der Blob ist der ganze Zweck: ohne ihn gaelte die Entscheidung jeder
	// kuenftigen Fassung der Datei.
	if reviews[0].GitBlob == "" {
		t.Error("das Review muss an den git-Blob gebunden sein")
	}
}

func TestDescribeFileStillRequiresOneOfBoth(t *testing.T) {
	c, _ := newTestClient(t)
	repo := gitRepoWithFile(t, "foo.go", "package foo\n")
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "p"}, repoRoot: repo})

	// Weder Beschreibung noch nothing_to_say: ein leerer Aufruf wuerde einen
	// Pfad still als erledigt buchen, ohne dass jemand hingesehen hat.
	got, failed := callTool(t, session, "context_describe_file", map[string]any{"path": "foo.go"})
	if !failed {
		t.Fatalf("ein Aufruf ohne beides muss abgewiesen werden, got %q", got)
	}
}

func TestDescribeFileRefusesBothAtOnce(t *testing.T) {
	c, _ := newTestClient(t)
	repo := gitRepoWithFile(t, "foo.go", "package foo\n")
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "p"}, repoRoot: repo})

	// Beides zugleich ist keine Kleinigkeit: der Aufrufer meint zwei
	// verschiedene Dinge, und stillschweigend eins davon zu waehlen hiesse,
	// entweder eine Beschreibung zu verwerfen oder einen Review zu erfinden.
	got, failed := callTool(t, session, "context_describe_file",
		map[string]any{"path": "foo.go", "description": "etwas", "nothing_to_say": true})
	if !failed {
		t.Fatalf("beides zugleich muss abgewiesen werden, got %q", got)
	}
}
