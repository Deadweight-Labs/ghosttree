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
	"time"
)

// fakeCommits ist ein GitHub, das nur die Commits eines PRs liefert (und das
// Installations-Token dazu). Es hält fest, was die App angefragt hat.
type fakeCommits struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	tokenReq []map[string]any
	auths    []string
	paths    []string
	total    int
	status   int
	headers  map[string]string
	tokenSt  int
}

func newFakeCommits(t *testing.T) *fakeCommits {
	f := &fakeCommits{t: t, status: 200, tokenSt: http.StatusCreated}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.tokenReq = append(f.tokenReq, body)
		f.mu.Unlock()
		if f.tokenSt != http.StatusCreated {
			http.Error(w, "unprocessable", f.tokenSt)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "itok-5"})
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{n}/commits", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		for k, v := range f.headers {
			w.Header().Set(k, v)
		}
		if f.status != 200 {
			http.Error(w, "no", f.status)
			return
		}
		var page int
		_, _ = fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		if page == 0 {
			page = 1
		}
		var out []map[string]any
		for i := (page - 1) * 100; i < f.total && i < page*100; i++ {
			c := map[string]any{
				"sha":     fmt.Sprintf("%040x", i+1),
				"parents": []map[string]any{{"sha": fmt.Sprintf("%040x", i)}},
				"commit": map[string]any{
					"message":   fmt.Sprintf("commit %d\n\nlonger text that must not travel", i),
					"author":    map[string]any{"name": "Mia M."},
					"committer": map[string]any{"date": "2026-11-07T10:00:00Z"},
				},
				"author": map[string]any{"login": "mia-gh"},
			}
			if i == 1 {
				c["author"] = nil // GitHub kennt den Autor nicht
			}
			out = append(out, c)
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCommits) app() *GitHubApp {
	return NewGitHubApp(AppConfig{AppID: 77, Slug: "s", PrivateKey: appKey(f.t), WebhookSecret: "x", ClientID: "i", ClientSecret: "c", WebBase: f.srv.URL, APIBase: f.srv.URL})
}

func TestPullRequestCommitsAsksWithALimitedTokenAndFollowsPages(t *testing.T) {
	f := newFakeCommits(t)
	f.total = 130
	got, err := f.app().PullRequestCommits(context.Background(), 5, "github.com/Deadweight-Labs/GT", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 130 {
		t.Fatalf("got %d commits, want 130 over two pages", len(got))
	}
	if got[0].SHA != fmt.Sprintf("%040x", 1) || got[0].Title != "commit 0" || got[0].Author != "mia-gh" || got[0].AuthorName != "Mia M." || got[0].CommittedAt != "2026-11-07T10:00:00Z" {
		t.Errorf("first commit: %+v", got[0])
	}
	if got[1].Author != "" || got[1].AuthorName != "Mia M." {
		t.Errorf("an unknown author must come back without a login: %+v", got[1])
	}
	// Das Token ist auf "Pull requests: read" begrenzt, und nur das Token geht mit.
	if len(f.tokenReq) != 1 || fmt.Sprint(f.tokenReq[0]) != "map[permissions:map[pull_requests:read]]" {
		t.Errorf("token request: %v", f.tokenReq)
	}
	if len(f.paths) != 2 || !strings.HasPrefix(f.paths[0], "/repos/deadweight-labs/gt/pulls/12/commits?") || !strings.Contains(f.paths[1], "page=2") {
		t.Errorf("paths: %v", f.paths)
	}
	for _, a := range f.auths {
		if a != "Bearer itok-5" {
			t.Errorf("authorization %q", a)
		}
	}
}

func TestPullRequestCommitsStopsAtGitHubsOwnLimit(t *testing.T) {
	f := newFakeCommits(t)
	f.total = 1000
	got, err := f.app().PullRequestCommits(context.Background(), 5, "github.com/a/b", 1)
	if err != nil || len(got) != maxCommitPages*commitsPerPage {
		t.Fatalf("%d commits, err %v", len(got), err)
	}
}

func TestPullRequestCommitsErrorsAreClearAndNeverCarryTheToken(t *testing.T) {
	f := newFakeCommits(t)
	f.total = 3
	ctx := context.Background()
	reset := time.Now().Add(10 * time.Minute).Truncate(time.Second)

	f.status, f.headers = http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(reset.Unix())}
	_, err := f.app().PullRequestCommits(ctx, 5, "github.com/a/b", 1)
	var rl *RateLimitError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || !rl.Reset.Equal(reset) || errors.Is(err, ErrPermissionMissing) {
		t.Fatalf("rate limit: %v", err)
	}
	f.status, f.headers = http.StatusTooManyRequests, map[string]string{"Retry-After": "30"}
	_, err = f.app().PullRequestCommits(ctx, 5, "github.com/a/b", 1)
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || time.Until(rl.Reset) < 20*time.Second || time.Until(rl.Reset) > 40*time.Second {
		t.Fatalf("429: %v (reset %v)", err, rl)
	}
	// Ein 403 ohne Ratenlimit heißt: das Recht fehlt.
	f.status, f.headers = http.StatusForbidden, nil
	if _, err := f.app().PullRequestCommits(ctx, 5, "github.com/a/b", 1); !errors.Is(err, ErrPermissionMissing) || errors.Is(err, ErrRateLimited) {
		t.Fatalf("403: %v", err)
	}
	f.status = http.StatusNotFound
	if _, err := f.app().PullRequestCommits(ctx, 5, "github.com/a/b", 1); !errors.Is(err, ErrPermissionMissing) {
		t.Fatalf("404: %v", err)
	}
	f.status = http.StatusInternalServerError
	_, err = f.app().PullRequestCommits(ctx, 5, "github.com/a/b", 1)
	if err == nil || errors.Is(err, ErrPermissionMissing) || errors.Is(err, ErrRateLimited) {
		t.Fatalf("500: %v", err)
	}
	// Ein Token, das GitHub wegen fehlenden Rechts nicht ausstellt (422).
	f.status, f.tokenSt = 200, http.StatusUnprocessableEntity
	if _, err := f.app().PullRequestCommits(ctx, 5, "github.com/a/b", 1); !errors.Is(err, ErrPermissionMissing) {
		t.Fatalf("token 422: %v", err)
	}
	for _, e := range []error{err, rl} {
		if e != nil && (strings.Contains(e.Error(), "itok") || strings.Contains(e.Error(), "Bearer")) {
			t.Errorf("an error carries the token: %v", e)
		}
	}
	// Eingaben, die nichts bedeuten, gehen nie hinaus.
	f.tokenSt = http.StatusCreated
	before := len(f.paths)
	for _, bad := range []struct {
		repo string
		n    int64
	}{{"deadweight-labs/gt", 1}, {"github.com/a/b/c", 1}, {"github.com/a/b", 0}, {"github.com/a/b", -3}} {
		if _, err := f.app().PullRequestCommits(ctx, 5, bad.repo, bad.n); err == nil {
			t.Errorf("bad input %v must fail", bad)
		}
	}
	if len(f.paths) != before {
		t.Errorf("a bad input reached GitHub: %v", f.paths[before:])
	}
}

func TestPullEventCarriesTheAuthor(t *testing.T) {
	body := []byte(`{"action":"opened","number":7,"installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/GT","default_branch":"main"},
		"pull_request":{"number":7,"title":"x","state":"open","user":{"login":"mia-gh"},"head":{"ref":"feat/x","repo":{"full_name":"Deadweight-Labs/GT"}},"base":{"ref":"main"}}}`)
	ev, ok := ParsePullEvent("pull_request", body)
	if !ok || ev.Author != "mia-gh" || ev.Remote != "github.com/deadweight-labs/gt" {
		t.Fatalf("event: %+v ok=%v", ev, ok)
	}
}

func TestPullEventCarriesTheHeadCommit(t *testing.T) {
	body := []byte(`{"action":"synchronize","number":7,"installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/GT","default_branch":"main"},
		"pull_request":{"number":7,"title":"x","state":"open","user":{"login":"mia-gh"},"head":{"ref":"feat/x","sha":"` + detailSHA + `","repo":{"full_name":"Deadweight-Labs/GT"}},"base":{"ref":"main"}}}`)
	ev, ok := ParsePullEvent("pull_request", body)
	if !ok || ev.HeadSHA != detailSHA {
		t.Fatalf("event: %+v ok=%v", ev, ok)
	}
}
