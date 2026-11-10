package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
)

// hookCalls counts what a hook asks a server, by method and path.
type hookCalls struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *hookCalls) add(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[r.Method+" "+r.URL.Path]++
}

func (c *hookCalls) of(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[key]
}

func preToolUse(t *testing.T, repo, session, tool string, input map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"session_id": session, "cwd": repo, "tool_name": tool, "tool_input": input})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdHookWith(bytes.NewReader(payload), []string{"pre-tool-use", "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("pre-tool-use exit %d", code)
	}
	event, ctx := decodeHook(t, &out)
	if event != "PreToolUse" {
		t.Fatalf("event = %q", event)
	}
	return ctx
}

// The hook in front of every tool call asked the server about runbooks every
// time. It now knows the command and path triggers and asks only for a call
// that one of them matches.
func TestToolHookAsksAboutRunbooksOnlyWhenATriggerMatches(t *testing.T) {
	var calls hookCalls
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.add(r)
		switch r.URL.Path {
		case "/api/runbook-triggers":
			w.Write([]byte(`{"sets":[{"commands":["scripts/build-dist.sh"]},{"paths":["deploy/**"]}]}`))
		case "/api/runbooks/triggered":
			w.Write([]byte("[runbook rollout, triggered by command scripts/build-dist.sh]\n"))
		default:
			w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()
	withConfig(t, srv.URL)
	repo := newRepo(t)

	for _, command := range []string{"go test ./... && git status", "ls -la", "git log --oneline -5"} {
		if ctx := preToolUse(t, repo, "s-perf", "Bash", map[string]any{"command": command}); ctx != "" {
			t.Fatalf("%q: context %q", command, ctx)
		}
	}
	if got := calls.of("POST /api/runbooks/triggered"); got != 0 {
		t.Fatalf("commands no trigger matches asked the server %d times", got)
	}
	if got := calls.of("GET /api/runbook-triggers"); got != 1 {
		t.Fatalf("the triggers were fetched %d times, want once", got)
	}

	ctx := preToolUse(t, repo, "s-perf", "Bash", map[string]any{"command": "cd x && scripts/build-dist.sh"})
	if !strings.Contains(ctx, "[runbook rollout, triggered by command") {
		t.Fatalf("a matching command did not bring the runbook: %q", ctx)
	}
	if err := os.MkdirAll(filepath.Join(repo, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	preToolUse(t, repo, "s-perf", "Edit", map[string]any{"file_path": filepath.Join(repo, "deploy", "prod.yaml")})
	if got := calls.of("POST /api/runbooks/triggered"); got != 2 {
		t.Fatalf("a matching command and a matching path asked the server %d times, want 2", got)
	}
	if got := calls.of("GET /api/runbook-triggers"); got != 1 {
		t.Fatalf("the triggers were fetched %d times, want once", got)
	}

	// A copy older than the limit is fetched again: that is how a runbook
	// approved in the meantime reaches the tool hook.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	path, err := runbookTriggerCachePath(cfg, collector.ResolveGitContext(repo).Project)
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := loadRunbookTriggerCache(path)
	if !ok || len(cached.Sets) != 2 {
		t.Fatalf("the copy was not kept: %+v %v", cached, ok)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the copy must be readable by its owner only: %v %v", info, err)
	}
	cached.FetchedAt = time.Now().Add(-runbookTriggerTTL - time.Second).Unix()
	saveRunbookTriggerCache(path, cached)
	preToolUse(t, repo, "s-perf", "Bash", map[string]any{"command": "ls"})
	if got := calls.of("GET /api/runbook-triggers"); got != 2 {
		t.Fatalf("an old copy was fetched %d times in all, want 2", got)
	}
}

// A server that does not know the route yet is asked about every call, as
// before, and about the route only once.
func TestToolHookAsksAnOlderServerAboutEveryCall(t *testing.T) {
	var calls hookCalls
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.add(r)
		switch r.URL.Path {
		case "/api/runbook-triggers":
			http.NotFound(w, r)
		case "/api/runbooks/triggered":
			w.Write([]byte("[runbook rollout, triggered by command ls]\n"))
		default:
			w.Write([]byte("[]"))
		}
	}))
	defer srv.Close()
	withConfig(t, srv.URL)
	repo := newRepo(t)

	for i := 0; i < 3; i++ {
		if ctx := preToolUse(t, repo, "s-old", "Bash", map[string]any{"command": "ls"}); !strings.Contains(ctx, "runbook rollout") {
			t.Fatalf("call %d: the older server's answer was lost: %q", i, ctx)
		}
	}
	if got := calls.of("POST /api/runbooks/triggered"); got != 3 {
		t.Fatalf("the older server was asked %d times, want 3", got)
	}
	if got := calls.of("GET /api/runbook-triggers"); got != 1 {
		t.Fatalf("the missing route was tried %d times, want once", got)
	}
}

// The local match is the server's own rule, including a path trigger that
// applies to the whole checkout.
func TestToolHookMatchesTriggersLikeTheServer(t *testing.T) {
	sets := []runbook.Triggers{{Commands: []string{"ssh prod"}}, {Paths: []string{"deploy/**"}}}
	for _, c := range []struct {
		in   runbook.Input
		want bool
	}{
		{runbook.Input{Command: "ssh prod uptime"}, true},
		{runbook.Input{Command: "ssh staging"}, false},
		{runbook.Input{Paths: []string{"deploy/prod.yaml"}}, true},
		{runbook.Input{Paths: []string{"internal/store/store.go"}}, false},
		{runbook.Input{Command: "ls", RepoPath: "deploy/charts"}, true},
	} {
		if got := anyTriggerMatches(sets, c.in); got != c.want {
			t.Errorf("%+v: match %v, want %v", c.in, got, c.want)
		}
	}
}

// Two paths nothing is known about used to leave a line break between two
// empty answers. It was delivered as context and counted against the session.
func TestToolHookDeliversNothingForPathsWithoutAnAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runbook-triggers" {
			w.Write([]byte(`{"sets":[]}`))
			return
		}
		w.Write([]byte("[]"))
	}))
	defer srv.Close()
	withConfig(t, srv.URL)
	repo := newRepo(t)

	ctx := preToolUse(t, repo, "s-blank", "Bash", map[string]any{"command": "cat internal/store/knowledge.go internal/store/store.go"})
	if ctx != "" {
		t.Fatalf("context %q, want none", ctx)
	}
	budget := filepath.Join(os.Getenv("XDG_STATE_HOME"), "ghosttree", "context-budget")
	if entries, _ := os.ReadDir(budget); len(entries) != 0 {
		t.Fatalf("nothing was said, yet the session was charged: %d files in %s", len(entries), budget)
	}
}

// swapProjectionRefresh replaces the start of the background process and
// reports the checkouts it was started for.
func swapProjectionRefresh(t *testing.T, err error) *[]string {
	t.Helper()
	var started []string
	old := startProjectionRefresh
	startProjectionRefresh = func(root string) error {
		started = append(started, root)
		return err
	}
	t.Cleanup(func() { startProjectionRefresh = old })
	return &started
}

func sessionStart(t *testing.T, repo, session string) {
	t.Helper()
	payload := `{"cwd":` + strconv.Quote(repo) + `,"session_id":` + strconv.Quote(session) + `}`
	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(payload), []string{"session-start", "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("session-start exit %d", code)
	}
	if event, _ := decodeHook(t, &out); event != "SessionStart" {
		t.Fatalf("event = %q", event)
	}
}

// A session start waited for the files under .ghosttree/ to be rewritten, one
// request per document and several per discussion. It still writes them where
// there are none; where they exist it hands the work to a process of its own.
func TestSessionStartDoesNotWaitForFilesThatExist(t *testing.T) {
	moveCLIFixture(t)
	repo := newRepo(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "set-url", "origin", "https://"+cliHere+".git").CombinedOutput(); err != nil {
		t.Fatalf("git remote: %v %s", err, out)
	}
	index := filepath.Join(repo, ".ghosttree", "INDEX.md")
	started := swapProjectionRefresh(t, nil)

	sessionStart(t, repo, "s-files-1")
	if len(*started) != 0 {
		t.Fatalf("a checkout without the files must write them itself, started %v", *started)
	}
	first, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("the first session start left no files: %v", err)
	}

	const marker = "left as it was\n"
	if err := os.WriteFile(index, []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionStart(t, repo, "s-files-2")
	if len(*started) != 1 || !projectionExists((*started)[0]) {
		t.Fatalf("the second session start did not hand the files to the background: %v", *started)
	}
	if got, _ := os.ReadFile(index); string(got) != marker {
		t.Fatalf("the session start rewrote the files itself:\n%s", got)
	}

	// What that process does.
	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(""), []string{"refresh-files", (*started)[0]}, &out); code != 0 || out.Len() != 0 {
		t.Fatalf("refresh-files: exit %d, output %q", code, out.String())
	}
	got, _ := os.ReadFile(index)
	if string(got) == marker || len(got) == 0 || len(first) == 0 {
		t.Fatalf("refresh-files did not rewrite the files:\n%s", got)
	}
}

// When the process cannot be started the session start writes the files
// itself, as it always did.
func TestSessionStartWritesTheFilesWhenNothingElseCan(t *testing.T) {
	moveCLIFixture(t)
	repo := newRepo(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "set-url", "origin", "https://"+cliHere+".git").CombinedOutput(); err != nil {
		t.Fatalf("git remote: %v %s", err, out)
	}
	index := filepath.Join(repo, ".ghosttree", "INDEX.md")
	started := swapProjectionRefresh(t, errors.New("no process"))

	sessionStart(t, repo, "s-files-3")
	const marker = "stale\n"
	if err := os.WriteFile(index, []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionStart(t, repo, "s-files-4")
	if len(*started) != 1 {
		t.Fatalf("the start was tried %d times, want once", len(*started))
	}
	if got, _ := os.ReadFile(index); string(got) == marker || len(got) == 0 {
		t.Fatalf("the files stayed stale:\n%s", got)
	}
}

// Two writers build in the same temporary directory beside the target. The
// background refresh leaves a running writer alone; the one a session waits
// for takes its turn.
func TestOnlyOneWriterRewritesTheFilesOfACheckout(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	release, err := lockProjection(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockProjection(root, false); !errors.Is(err, errProjectionBusy) {
		t.Fatalf("a second writer got in: %v", err)
	}
	other, err := lockProjection(t.TempDir(), false)
	if err != nil {
		t.Fatalf("another checkout was held up: %v", err)
	}
	other()
	waited := make(chan struct{})
	go func() {
		defer close(waited)
		if again, err := lockProjection(root, true); err == nil {
			again()
		}
	}()
	select {
	case <-waited:
		t.Fatal("the waiting writer did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting writer never got its turn")
	}
}
