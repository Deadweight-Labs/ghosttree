package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const detailSHA = "aaaaaaa1111111111111111111111111111111aa"

// fakeDetails ist ein GitHub, das Commit, Check-Runs und Status liefert und
// festhält, mit welchen Rechten Token verlangt wurden.
type fakeDetails struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	tokenReq []string
	refuse   map[string]int // Rechte-Schlüssel -> Statuscode, mit dem das Token abgelehnt wird
	commit   map[string]any
	runs     []map[string]any
	status   map[string]any
	paths    []string
}

func newFakeDetails(t *testing.T) *fakeDetails {
	f := &fakeDetails{t: t, refuse: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Permissions map[string]string `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var keys []string
		for k := range body.Permissions {
			keys = append(keys, k)
		}
		key := strings.Join(sortedStrings(keys), "+")
		f.mu.Lock()
		f.tokenReq = append(f.tokenReq, key)
		f.mu.Unlock()
		if code := f.refuse[key]; code != 0 {
			http.Error(w, "no", code)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "itok-9"})
	})
	record := func(r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
	}
	mux.HandleFunc("GET /repos/{o}/{r}/commits/{sha}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(f.commit)
	})
	mux.HandleFunc("GET /repos/{o}/{r}/commits/{sha}/check-runs", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": f.runs})
	})
	mux.HandleFunc("GET /repos/{o}/{r}/commits/{sha}/status", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(f.status)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func (f *fakeDetails) app() *GitHubApp {
	return NewGitHubApp(AppConfig{AppID: 77, Slug: "s", PrivateKey: appKey(f.t), WebhookSecret: "x", ClientID: "i", ClientSecret: "c", WebBase: f.srv.URL, APIBase: f.srv.URL})
}

func TestPullRequestCommitsCarriesParentsAndTheWholeMessage(t *testing.T) {
	f := newFakeCommits(t)
	f.total = 1
	got, err := f.app().PullRequestCommits(context.Background(), 5, "github.com/a/b", 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if !strings.Contains(got[0].Message, "longer text") || got[0].Title != "commit 0" || len(got[0].Parents) != 1 || got[0].Parents[0] != fmt.Sprintf("%040x", 0) {
		t.Fatalf("message: %+v", got[0])
	}
}

func TestCommitFilesAsksWithContentsReadAndCapsTheList(t *testing.T) {
	f := newFakeDetails(t)
	var files []map[string]any
	for i := 0; i < maxCommitFiles+20; i++ {
		files = append(files, map[string]any{"filename": fmt.Sprintf("f%d.go", i), "additions": i, "deletions": 1})
	}
	f.commit = map[string]any{"stats": map[string]any{"additions": 900, "deletions": 40}, "files": files}
	got, err := f.app().CommitFiles(context.Background(), 5, "github.com/Deadweight-Labs/GT", detailSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != maxCommitFiles || got.Total != maxCommitFiles+20 || got.Additions != 900 || got.Deletions != 40 || got.Files[3].Path != "f3.go" || got.Files[3].Additions != 3 {
		t.Fatalf("files: %d total %d %+v", len(got.Files), got.Total, got.Files[3])
	}
	if len(f.tokenReq) != 1 || f.tokenReq[0] != "contents" {
		t.Errorf("the token must be limited to contents: %v", f.tokenReq)
	}
	if len(f.paths) != 1 || f.paths[0] != "/repos/deadweight-labs/gt/commits/"+detailSHA {
		t.Errorf("paths: %v", f.paths)
	}
}

func TestCommitFilesWithoutTheRightIsErrPermissionMissing(t *testing.T) {
	f := newFakeDetails(t)
	f.refuse["contents"] = http.StatusUnprocessableEntity
	if _, err := f.app().CommitFiles(context.Background(), 5, "github.com/a/b", detailSHA); !errors.Is(err, ErrPermissionMissing) {
		t.Fatalf("422 at the token: %v", err)
	}
}

func TestCommitFilesAndCIRefuseInputThatMeansNothing(t *testing.T) {
	f := newFakeDetails(t)
	for _, c := range []struct{ repo, sha string }{{"a/b", detailSHA}, {"github.com/a/b/c", detailSHA}, {"github.com/a/b", "main"}, {"github.com/a/b", "../x"}, {"github.com/a/b", ""}} {
		if _, err := f.app().CommitFiles(context.Background(), 5, c.repo, c.sha); err == nil {
			t.Errorf("files %v must fail", c)
		}
		if _, err := f.app().CommitCI(context.Background(), 5, c.repo, c.sha); err == nil {
			t.Errorf("ci %v must fail", c)
		}
	}
	if len(f.tokenReq) != 0 || len(f.paths) != 0 {
		t.Errorf("a bad input reached GitHub: %v %v", f.tokenReq, f.paths)
	}
}

func TestCommitCISumsUpCheckRunsAndStatuses(t *testing.T) {
	run := func(status, conclusion string) map[string]any {
		return map[string]any{"status": status, "conclusion": conclusion}
	}
	for _, c := range []struct {
		name   string
		runs   []map[string]any
		status map[string]any
		want   string
	}{
		{"nothing", nil, map[string]any{"state": "pending", "total_count": 0}, ""},
		{"all green", []map[string]any{run("completed", "success"), run("completed", "skipped"), run("completed", "neutral")}, map[string]any{"state": "success", "total_count": 0}, CISuccess},
		{"one running", []map[string]any{run("completed", "success"), run("in_progress", "")}, map[string]any{"total_count": 0}, CIPending},
		{"one failed beats running", []map[string]any{run("in_progress", ""), run("completed", "failure")}, map[string]any{"total_count": 0}, CIFailure},
		{"cancelled counts as failed", []map[string]any{run("completed", "cancelled")}, map[string]any{"total_count": 0}, CIFailure},
		{"status fails", []map[string]any{run("completed", "success")}, map[string]any{"state": "failure", "total_count": 2}, CIFailure},
		{"only a status", nil, map[string]any{"state": "success", "total_count": 1}, CISuccess},
		{"status pending", []map[string]any{run("completed", "success")}, map[string]any{"state": "pending", "total_count": 1}, CIPending},
	} {
		f := newFakeDetails(t)
		f.runs, f.status = c.runs, c.status
		got, err := f.app().CommitCI(context.Background(), 5, "github.com/a/b", detailSHA)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q (%v), want %q", c.name, got, err, c.want)
		}
		if len(f.tokenReq) != 1 || f.tokenReq[0] != "checks+statuses" {
			t.Errorf("%s: token %v", c.name, f.tokenReq)
		}
	}
}

func TestCommitCIFallsBackToChecksAloneAndNamesAMissingRight(t *testing.T) {
	f := newFakeDetails(t)
	f.refuse["checks+statuses"] = http.StatusUnprocessableEntity
	f.runs = []map[string]any{{"status": "completed", "conclusion": "success"}}
	got, err := f.app().CommitCI(context.Background(), 5, "github.com/a/b", detailSHA)
	if err != nil || got != CISuccess {
		t.Fatalf("checks alone: %q %v", got, err)
	}
	for _, p := range f.paths {
		if strings.HasSuffix(p, "/status") {
			t.Errorf("the statuses were asked although the token lacks the right: %v", f.paths)
		}
	}
	f.refuse["checks"] = http.StatusUnprocessableEntity
	if _, err := f.app().CommitCI(context.Background(), 5, "github.com/a/b", detailSHA); !errors.Is(err, ErrPermissionMissing) {
		t.Fatalf("no checks right: %v", err)
	}
}
