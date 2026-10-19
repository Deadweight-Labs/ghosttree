package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-457, criterion 3: everything the gardener writes is marked as the
// gardener's and is not trusted until a person confirms it; nothing is silently
// deleted, closed or overwritten. The tests below run the real command in agent
// mode, with a stand-in for Claude Code that answers with every kind of finding,
// against the real server and a real store, and record EVERY call that is not a
// read.

type recordedWrite struct {
	Method, Path string
	Body         []byte
	Status       int
}

type writeRecorder struct {
	inner  http.Handler
	mu     sync.Mutex
	writes []recordedWrite
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (r *writeRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		r.inner.ServeHTTP(w, req)
		return
	}
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
	r.inner.ServeHTTP(sw, req)
	r.mu.Lock()
	r.writes = append(r.writes, recordedWrite{Method: req.Method, Path: req.URL.Path, Body: body, Status: sw.code})
	r.mu.Unlock()
}

func (r *writeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.writes)
}

func (r *writeRecorder) since(n int) []recordedWrite {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedWrite(nil), r.writes[n:]...)
}

const provenanceProject = "github.com/acme/ghosttree"

var digits = regexp.MustCompile(`[0-9]+`)

// The only calls the gardener may make that are not reads, and what each is for:
// a proposal (a staged note), the agent that speaks for it in the project room,
// a thread for a contradiction, its links and its first post.
var allowedGardenWrites = regexp.MustCompile(`^POST /api/(knowledge|coord/agents|threads|threads/[0-9]+/links|coord/messages)$`)

type provenanceFixture struct {
	t        *testing.T
	srv      *httptest.Server
	st       *store.Store
	rec      *writeRecorder
	repo     *garden.Repo
	dir      string
	claude   string
	ids      map[string]int64
	request  int64
	gardener *client.Client
}

func newProvenanceFixture(t *testing.T) *provenanceFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(dir, "absent.json"))
	t.Setenv("GHOSTTREE_GARDEN_KEY", "")

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// alice is the person who owns the project; the gardener is an account of its
	// own with the plain role of a member, and not a reviewer.
	if _, err := st.AddAccount("alice", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAccount("gardener", "", false); err != nil {
		t.Fatal(err)
	}
	org, err := st.CreateOrg("person:1", "Acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:1", provenanceProject); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", provenanceProject, "person:2", store.RoleMember, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	// The server names the gardener's account, as GHOSTTREE_GARDENER_ACCOUNT does
	// for ctx serve; without it the server refuses everything of the gardener's.
	st.SetGardenerAccount("gardener")
	token, _, err := st.CreateToken("gardener", store.TokenSpec{Label: "garden"})
	if err != nil {
		t.Fatal(err)
	}

	rec := &writeRecorder{inner: server.New(st)}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	cfg := config.Config{ServerURL: srv.URL, Token: token, Machine: "orax"}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	// The checkout: a file the entries name, and a test the gardener can point to.
	repoDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"cmd/x.go":      "package cmd\n",
		"cmd/x_test.go": "package cmd\n\nimport \"testing\"\n\nfunc TestHoldsX(t *testing.T) {}\n",
	} {
		if err := os.WriteFile(filepath.Join(repoDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "init"},
		{"remote", "add", "origin", "https://" + provenanceProject + ".git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo, err := garden.OpenRepo(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	fx := &provenanceFixture{t: t, srv: srv, st: st, rec: rec, repo: repo, dir: dir, ids: map[string]int64{}, gardener: client.New(cfg)}
	put := func(name, typ, title, body string) {
		id, err := st.InsertKnowledge(store.Knowledge{Type: typ, Title: title, Body: body, Scope: scope.Axes{Project: provenanceProject},
			Person: "alice", Confidence: "trusted", ObservedAt: "2026-01-01T00:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		fx.ids[name] = id
	}
	put("where_a", "decision", "Der Dienst läuft auf orax", "Siehe cmd/x.go. Der Dienst läuft auf orax und nirgends sonst.")
	put("where_b", "pitfall", "Der Dienst läuft auf apps-vm", "Siehe cmd/x.go. Der Dienst läuft auf apps-vm, nicht auf orax.")
	put("dup_a", "pitfall", "Die Datenbank liegt in einer Datei", "Siehe cmd/x.go. Die Datenbank liegt in einer einzigen SQLite-Datei.")
	put("dup_b", "pitfall", "Eine Datei hält die Datenbank", "Siehe cmd/x.go. Die Datenbank liegt in einer einzigen SQLite-Datei, mehr gibt es nicht.")
	put("tested", "pitfall", "Ein Pitfall mit Test", "Siehe cmd/x.go. Der Fall ist durch einen Test belegt.")
	put("untested", "pitfall", "Ein Pitfall ohne Test", "Siehe cmd/x.go. Dafür gibt es noch keinen Test.")
	put("tool", "pitfall", "Ein Pitfall über ein Werkzeug", "Siehe cmd/x.go. Das Werkzeug verhält sich so.")
	if d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Offener Auftrag", Description: "d",
		Scope: scope.Axes{Project: provenanceProject}, Person: "alice"}, Criteria: []string{"Das Kriterium ist offen"}}); err != nil {
		t.Fatal(err)
	} else {
		fx.request = d.Request.ID
	}

	// The stand-in for Claude Code: it reads the files, then answers with a stale
	// verdict, a contradiction, a duplicate and all three kinds of regression answer.
	ids := fx.ids
	quote := func(id int64, text string) map[string]any { return map[string]any{"id": id, "text": text} }
	verdicts := []map[string]any{}
	for _, id := range ids {
		v := map[string]any{"id": id, "verdict": "holds", "reason": "gilt weiter"}
		if id == ids["where_b"] {
			v = map[string]any{"id": id, "verdict": "stale", "reason": "überholt", "evidence": []string{"cmd/x.go:1"}}
		}
		verdicts = append(verdicts, v)
	}
	answer, _ := json.Marshal(map[string]any{
		"verdicts": verdicts,
		"relations": []map[string]any{
			{"kind": "contradiction", "ids": []int64{ids["where_a"], ids["where_b"]}, "reason": "Zwei Orte für denselben Dienst.",
				"quotes": []map[string]any{quote(ids["where_a"], "Der Dienst läuft auf orax und nirgends sonst"), quote(ids["where_b"], "Der Dienst läuft auf apps-vm, nicht auf orax")}},
			{"kind": "duplicate", "ids": []int64{ids["dup_a"], ids["dup_b"]}, "reason": "Beide sagen, dass die Datenbank in einer Datei liegt.",
				"quotes": []map[string]any{quote(ids["dup_a"], "Die Datenbank liegt in einer einzigen SQLite-Datei"), quote(ids["dup_b"], "Die Datenbank liegt in einer einzigen SQLite-Datei, mehr gibt es nicht")}},
		},
		"regression": []map[string]any{
			{"id": ids["tested"], "kind": "existing", "file": "cmd/x_test.go", "test": "TestHoldsX", "reason": "Er prüft genau das."},
			{"id": ids["untested"], "kind": "sketch", "file": "cmd/y_test.go", "test": "TestNewY", "steps": "lege den Zustand an\nrufe die Funktion auf\nprüfe das Ergebnis"},
			{"id": ids["tool"], "kind": "untestable", "reason": "Eine Eigenschaft des Werkzeugs, nichts im Code."},
		},
	})
	read := func(n int, name string) string {
		return fmt.Sprintf(`{"type":"assistant","message":{"id":"m%d","content":[{"type":"tool_use","id":"t%d","name":"Read","input":{"file_path":%q}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
			n, n, filepath.Join(repoDir, name))
	}
	result, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 3, "result": string(answer),
		"usage": map[string]int{"input_tokens": 30, "output_tokens": 40}})
	fx.claude = filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat > /dev/null\ncat <<'EOF'\n" + read(1, "cmd/x.go") + "\n" + read(2, "cmd/x_test.go") + "\n" + string(result) + "\nEOF\n"
	if err := os.WriteFile(fx.claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return fx
}

// run starts the gardener the way the nightly unit does, with --propose.
func (fx *provenanceFixture) run() string {
	fx.t.Helper()
	var out bytes.Buffer
	args := []string{"check-knowledge", "--project", provenanceProject, "--repo", fx.repo.Root, "--claude", fx.claude,
		"--claude-dir", filepath.Join(fx.dir, "sessions"), "--sessions", "1", "--propose", "--no-mcp"}
	if code := cmdGarden(args, &out); code != 0 {
		fx.t.Fatalf("exit %d:\n%s", code, out.String())
	}
	return out.String()
}

func (fx *provenanceFixture) knowledge() map[int64]store.Knowledge {
	fx.t.Helper()
	all, err := fx.st.KnowledgeForProject(provenanceProject)
	if err != nil {
		fx.t.Fatal(err)
	}
	out := map[int64]store.Knowledge{}
	for _, k := range all {
		out[k.ID] = k
	}
	return out
}

func (fx *provenanceFixture) count(query string, args ...any) int {
	fx.t.Helper()
	var n int
	if err := fx.st.DB().QueryRow(query, args...).Scan(&n); err != nil {
		fx.t.Fatal(err)
	}
	return n
}

func TestGardenerWritesOnlyMarkedStagedProposalsAndThreadsAndNothingThatDecides(t *testing.T) {
	fx := newProvenanceFixture(t)
	before := fx.knowledge()
	out := fx.run()
	writes := fx.rec.since(0)

	// (c) Every call that is not a read is one of a short list; nothing patches,
	// supersedes, retires, judges, sets a thread's state, sets a criterion or
	// closes a request. A new kind of call fails here until somebody decides it belongs.
	counts := map[string]int{}
	for _, w := range writes {
		call := w.Method + " " + w.Path
		if !allowedGardenWrites.MatchString(call) {
			t.Errorf("the gardener made a call that is not on the list: %s %s", call, w.Body)
		}
		if w.Status >= 300 {
			t.Errorf("%s answered %d", call, w.Status)
		}
		counts[digits.ReplaceAllString(call, "N")]++
	}
	want := map[string]int{"POST /api/knowledge": 5, "POST /api/coord/agents": 1, "POST /api/threads": 1, "POST /api/threads/N/links": 1, "POST /api/coord/messages": 1}
	for call, n := range want {
		if counts[call] != n {
			t.Errorf("%s was called %d times, want %d (%v)", call, counts[call], n, counts)
		}
	}

	// (a) Every entry it wrote is a note of the gardener, staged, never verified,
	// and does not replace anything.
	notes := 0
	for _, w := range writes {
		if w.Path != "/api/knowledge" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body, &body); err != nil {
			t.Fatal(err)
		}
		notes++
		if body["harness"] != "gardener" || body["confidence"] != "staged" || body["type"] != "note" || body["status"] != "active" || body["origin"] == "human" {
			t.Errorf("a proposal is not marked as the gardener's and staged: %s", w.Body)
		}
		if _, ok := body["supersedes"]; ok {
			t.Errorf("a proposal supersedes something: %s", w.Body)
		}
		if ref, _ := body["session_ref"].(string); !strings.HasPrefix(ref, "gardener:") {
			t.Errorf("a proposal has no gardener reference: %s", w.Body)
		}
	}
	after := fx.knowledge()
	if notes != 5 || len(after) != len(before)+5 {
		t.Fatalf("%d proposals written, %d entries new", notes, len(after)-len(before))
	}
	for id, k := range after {
		if _, old := before[id]; old {
			continue
		}
		if k.Harness != "gardener" || k.Confidence != "staged" || k.Status != "active" || k.Person != "gardener" || k.ConfirmedBy != "" || k.RegressionState != "" {
			t.Errorf("new entry #%d: %+v", id, k)
		}
	}
	// What it checked is exactly as it was: no status, wording, verdict or successor changed.
	for id, k := range before {
		a := after[id]
		if a.Confidence != k.Confidence || a.Status != k.Status || a.Title != k.Title || a.Body != k.Body || a.RegressionState != k.RegressionState || a.RegressionTest != k.RegressionTest || a.SupersededBy != 0 || a.UpdatedAt != k.UpdatedAt {
			t.Errorf("entry #%d was changed:\nbefore %+v\nafter  %+v", id, k, a)
		}
	}
	for q, what := range map[string]string{
		`SELECT COUNT(*) FROM knowledge_relations`:   "relations (supersede, merge)",
		`SELECT COUNT(*) FROM knowledge_retirements`: "retirements",
		`SELECT COUNT(*) FROM knowledge_versions`:    "archived versions (an entry was rewritten)",
	} {
		if n := fx.count(q); n != 0 {
			t.Errorf("%d %s were written", n, what)
		}
	}

	// (b) The thread and its post belong to the gardener's account and say so.
	var title, state, person, author string
	var archived int
	if err := fx.st.DB().QueryRow(`SELECT title,state,archived,person,author_principal_id FROM threads`).Scan(&title, &state, &archived, &person, &author); err != nil {
		t.Fatalf("no thread: %v", err)
	}
	if !strings.HasPrefix(title, "[Gärtner] ") || state != store.ThreadOpen || archived != 0 || person != "gardener" || author != "person:2" {
		t.Errorf("thread = %q state %s archived %d by %s (%s)", title, state, archived, person, author)
	}
	if n := fx.count(`SELECT COUNT(*) FROM threads`); n != 1 {
		t.Errorf("%d threads", n)
	}
	if n := fx.count(`SELECT COUNT(*) FROM thread_links WHERE object_kind='knowledge' AND object_id IN (?,?)`, fx.ids["where_a"], fx.ids["where_b"]); n != 2 {
		t.Errorf("the thread is linked to %d of the two entries", n)
	}
	var sender, post string
	var postAuthor string
	if err := fx.st.DB().QueryRow(`SELECT sender_external_id,author_principal_id,body FROM coord_messages WHERE destination_kind=?`, store.DestinationDiscussion).Scan(&sender, &postAuthor, &post); err != nil {
		t.Fatalf("no first post: %v", err)
	}
	if sender != garden.ThreadAgentID(provenanceProject) || postAuthor != "person:2" || !strings.Contains(post, "Herkunft gardener, nicht bestätigt") ||
		!strings.Contains(post, `Zitat: "Der Dienst läuft auf orax und nirgends sonst"`) {
		t.Errorf("first post by %s (%s):\n%s", sender, postAuthor, post)
	}
	// Criteria and requests: untouched.
	d, err := fx.st.RequestByID(fx.request)
	if err != nil || d.Request.State != "open" || len(d.Criteria) != 1 || d.Criteria[0].State != "open" {
		t.Errorf("the request was touched: %+v %v", d, err)
	}
	// The contradiction is a thread, not a note; the report names what became of it.
	if !strings.Contains(out, "thread 1 opened") || !strings.Contains(out, "5 proposals filed") {
		t.Errorf("output:\n%s", out)
	}
	for _, k := range after {
		if k.Harness == "gardener" && strings.HasPrefix(k.SessionRef, "gardener:relation:contradiction:") {
			t.Errorf("a contradiction was filed as a note although its thread opened: %s", k.Title)
		}
	}
}

// The same run again, a thread closed by a person, an entry changed: nothing is
// duplicated, nothing reopened, and only a change in an entry asks again.
func TestGardenerRunsAreIdempotentAndRespectWhatPeopleDecided(t *testing.T) {
	fx := newProvenanceFixture(t)
	fx.run()
	first := fx.rec.count()

	fx.run()
	for _, w := range fx.rec.since(first) {
		// Only the gardener's own registration in the room is repeated; it is how it reads the threads.
		if w.Path != "/api/coord/agents" {
			t.Errorf("the second run wrote again: %s %s %s", w.Method, w.Path, w.Body)
		}
	}
	if n := fx.count(`SELECT COUNT(*) FROM threads`); n != 1 {
		t.Fatalf("%d threads after the second run", n)
	}
	if n := len(fx.knowledge()); n != 7+5 {
		t.Fatalf("%d entries after the second run", n)
	}

	// A person closes the thread. The gardener neither reopens it nor opens another.
	if _, err := fx.st.DB().Exec(`UPDATE threads SET state='resolved', resolved_at='2026-10-20T08:00:00Z' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	before := fx.rec.count()
	fx.run()
	for _, w := range fx.rec.since(before) {
		if w.Path != "/api/coord/agents" {
			t.Errorf("the run after the decision wrote: %s %s", w.Method, w.Path)
		}
	}
	var state string
	if err := fx.st.DB().QueryRow(`SELECT state FROM threads WHERE id=1`).Scan(&state); err != nil || state != store.ThreadResolved {
		t.Errorf("thread 1 is %q (%v), a person closed it", state, err)
	}
	if n := fx.count(`SELECT COUNT(*) FROM threads`); n != 1 {
		t.Errorf("%d threads: a closed one was answered with a new one while the entries are unchanged", n)
	}

	// One of the entries changes: the question may be asked again, as a new thread.
	b := fx.knowledge()[fx.ids["where_b"]]
	if err := fx.st.UpdateKnowledgeBy(b.ID, map[string]string{"body": b.Body + " Seit dem Umzug."}, "alice"); err != nil {
		t.Fatal(err)
	}
	fx.run()
	if n := fx.count(`SELECT COUNT(*) FROM threads`); n != 2 {
		t.Errorf("%d threads after a changed entry, want the old one and a new one", n)
	}
	if err := fx.st.DB().QueryRow(`SELECT state FROM threads WHERE id=1`).Scan(&state); err != nil || state != store.ThreadResolved {
		t.Errorf("the old thread is %q (%v), it must stay as the person left it", state, err)
	}
}

// (d) What changes anything is a person's step on the review page. The account the
// gardener runs as, a member without the review right, cannot take any of them
// itself, whatever the program does: the server refuses.
func TestGardenerAccountCannotTakeTheStepsThatArePeoplesOnTheServer(t *testing.T) {
	fx := newProvenanceFixture(t)
	fx.run()
	c := fx.gardener
	ids := fx.ids
	if err := c.SetRegressionCover(ids["tested"], "covered", "cmd/x_test.go:TestHoldsX"); err == nil {
		t.Error("the gardener's account recorded a regression verdict")
	}
	if _, err := c.SupersedeKnowledge(ids["dup_b"], []int64{ids["dup_a"]}, "Gärtner-Vorschlag", "cli"); err == nil {
		t.Error("the gardener's account superseded an entry")
	}
	if _, err := c.RetireKnowledge(ids["dup_a"], "weg"); err == nil {
		t.Error("the gardener's account retired an entry")
	}
	if err := c.PatchKnowledge(ids["where_b"], map[string]string{"status": "deprecated"}); err == nil {
		t.Error("the gardener's account deprecated an entry")
	}
	if err := c.PatchKnowledge(ids["where_b"], map[string]string{"confidence": "verified"}); err == nil {
		t.Error("the gardener's account verified an entry")
	}
	for _, id := range ids {
		k, _ := fx.st.KnowledgeByID(id)
		if k.Status != "active" || k.Confidence != "trusted" || k.RegressionState != "" || k.SupersededBy != 0 {
			t.Errorf("entry #%d changed through the gardener's account: %+v", id, k)
		}
	}
}

// (a), from the server's side: the marks of the gardener are the gardener's alone.
// The account of a person, an owner included, cannot file a note that looks like
// the gardener's, with the real client and the real server; and the gardener run
// of an installation that has not named the account is refused at its first note.
func TestOnlyTheGardenersAccountCanFileNotesThatLookLikeTheGardeners(t *testing.T) {
	fx := newProvenanceFixture(t)
	aliceToken, _, err := fx.st.CreateToken("alice", store.TokenSpec{Label: "t"})
	if err != nil {
		t.Fatal(err)
	}
	alice := client.New(config.Config{ServerURL: fx.serverURL(), Token: aliceToken, Machine: "mainex"})
	forged := store.Knowledge{Type: "note", Title: "[Gärtner] Test vorhanden? #5: x", Body: "Test: cmd/x_test.go:TestHoldsX", Scope: scope.Axes{Project: provenanceProject},
		Confidence: "staged", Origin: "distilled", Harness: "gardener", SessionRef: "gardener:regression:5:existing"}
	if _, err := alice.Remember(forged, scope.Axes{Project: provenanceProject}); err == nil || !strings.Contains(err.Error(), "gardener_reserved") {
		t.Errorf("the owner filed a note that looks like the gardener's: %v", err)
	}
	if _, err := fx.gardener.Remember(forged, scope.Axes{Project: provenanceProject}); err != nil {
		t.Errorf("the gardener's own account is refused: %v", err)
	}

	// The same run, on a server that names no gardener account: the first proposal is refused,
	// and nothing is filed as the gardener's.
	fx2 := newProvenanceFixture(t)
	fx2.st.SetGardenerAccount("")
	var out bytes.Buffer
	args := []string{"check-knowledge", "--project", provenanceProject, "--repo", fx2.repo.Root, "--claude", fx2.claude,
		"--claude-dir", filepath.Join(fx2.dir, "sessions"), "--sessions", "1", "--propose", "--no-mcp"}
	if code := cmdGarden(args, &out); code == 0 {
		t.Errorf("a run without a named gardener account went through:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "gardener_reserved") {
		t.Errorf("the refusal is not shown:\n%s", out.String())
	}
	for _, k := range fx2.knowledge() {
		if k.Harness == "gardener" {
			t.Errorf("an entry of the gardener's was filed: %+v", k)
		}
	}
}

func (fx *provenanceFixture) serverURL() string { return fx.srv.URL }
