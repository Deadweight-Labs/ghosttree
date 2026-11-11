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
	// commitStatus lässt die Abfrage des Commits mit diesem Code scheitern; link ist
	// der Link-Kopf der ersten Seite und tail die Antwort auf ?page=.
	commitStatus int
	link         string
	tail         map[string]any
	// expires ist der Ablauf (RFC 3339), den die Token-Antwort nennt.
	expires string
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
		tok := map[string]string{"token": "itok-9"}
		if f.expires != "" {
			tok["expires_at"] = f.expires
		}
		_ = json.NewEncoder(w).Encode(tok)
	})
	record := func(r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
	}
	mux.HandleFunc("GET /repos/{o}/{r}/commits/{sha}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if f.commitStatus != 0 {
			http.Error(w, "no", f.commitStatus)
			return
		}
		if r.URL.Query().Get("page") != "" && f.tail != nil {
			_ = json.NewEncoder(w).Encode(f.tail)
			return
		}
		if f.link != "" {
			w.Header().Set("Link", f.link)
		}
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

// 403 ist das fehlende Recht der Installation, 404 und 422 betreffen nur den einen
// Commit (er kann nach einem Force-Push fehlen); so trennt der Aufrufer, ob er eine
// Gruppe für alle abschaltet oder nur diesen Commit überspringt.
func TestCommitFilesAndCITellAMissingRightFromAMissingCommit(t *testing.T) {
	for _, c := range []struct {
		status int
		want   error
	}{{http.StatusForbidden, ErrPermissionMissing}, {http.StatusNotFound, ErrCommitUnavailable}, {http.StatusUnprocessableEntity, ErrCommitUnavailable}} {
		f := newFakeDetails(t)
		f.commitStatus = c.status
		if _, err := f.app().CommitFiles(context.Background(), 5, "github.com/a/b", detailSHA); !errors.Is(err, c.want) {
			t.Errorf("files %d: got %v, want %v", c.status, err, c.want)
		}
	}
	// Ein 404 beim Token heißt: die Installation gibt es nicht mehr, das gilt für alle Commits.
	f := newFakeDetails(t)
	f.refuse["contents"] = http.StatusNotFound
	if _, err := f.app().CommitFiles(context.Background(), 5, "github.com/a/b", detailSHA); !errors.Is(err, ErrPermissionMissing) {
		t.Errorf("404 at the token: %v", err)
	}
}

func TestCommitFilesTotalComesFromTheLastPageWhenThereAreMorePages(t *testing.T) {
	f := newFakeDetails(t)
	page := func(n int) []map[string]any {
		var files []map[string]any
		for i := 0; i < n; i++ {
			files = append(files, map[string]any{"filename": fmt.Sprintf("f%d.go", i), "additions": 1, "deletions": 0})
		}
		return files
	}
	f.commit = map[string]any{"stats": map[string]any{"additions": 400, "deletions": 0}, "files": page(100)}
	f.tail = map[string]any{"files": page(37)}
	f.link = `<` + f.srv.URL + `/repos/a/b/commits/` + detailSHA + `?page=2>; rel="next", <` + f.srv.URL + `/repos/a/b/commits/` + detailSHA + `?page=4>; rel="last"`
	got, err := f.app().CommitFiles(context.Background(), 5, "github.com/a/b", detailSHA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3*100+37 || len(got.Files) != 100 {
		t.Fatalf("total %d (want 337), %d rows", got.Total, len(got.Files))
	}
	if len(f.paths) != 2 || !strings.HasSuffix(f.paths[1], "?page=4") {
		t.Errorf("only the last page is asked one more time: %v", f.paths)
	}
	// Ohne Link-Kopf bleibt es bei einer Abfrage.
	f2 := newFakeDetails(t)
	f2.commit = map[string]any{"files": page(5)}
	got, err = f2.app().CommitFiles(context.Background(), 5, "github.com/a/b", detailSHA)
	if err != nil || got.Total != 5 || len(f2.paths) != 1 {
		t.Fatalf("single page: %+v %v %v", got, err, f2.paths)
	}
	// Scheitert die letzte Seite, zeigt die Zahl der ersten: kein Fehler für eine Anzeige.
	f3 := newFakeDetails(t)
	f3.commit = f.commit
	f3.link = f.link
	got, err = f3.app().CommitFiles(context.Background(), 5, "github.com/a/b", detailSHA)
	if err != nil || len(got.Files) != 100 {
		t.Fatalf("failed tail: %+v %v", got, err)
	}
}

func TestLastPageReadsTheLinkHeader(t *testing.T) {
	for link, want := range map[string]int{
		"": 0,
		`<https://x/y?per_page=100&page=2>; rel="next"`:                                   0,
		`<https://x/y?page=2>; rel="next", <https://x/y?page=9>; rel="last"`:              9,
		`<https://x/y?per_page=100&page=3>; rel="last", <https://x/y?page=2>; rel="next"`: 3,
		`<https://x/y?page=99999999999999999999>; rel="last"`:                             0,
	} {
		if got := lastPage(link); got != want {
			t.Errorf("%q: got %d, want %d", link, got, want)
		}
	}
}

// Ein Lauf über viele Commits holt das Token einmal je Rechtesatz, nicht je Abruf.
func TestInstallationTokenIsReusedUntilItNearsItsExpiry(t *testing.T) {
	f := newFakeDetails(t)
	f.commit = map[string]any{"files": []map[string]any{}}
	f.expires = "2099-01-01T00:00:00Z"
	app := f.app()
	for i := 0; i < 3; i++ {
		if _, err := app.CommitFiles(context.Background(), 5, "github.com/a/b", detailSHA); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.tokenReq) != 1 {
		t.Fatalf("the token was fetched %d times, want once: %v", len(f.tokenReq), f.tokenReq)
	}
	// Ein anderer Rechtesatz oder eine andere Installation bekommt ein eigenes Token.
	if _, err := app.CommitCI(context.Background(), 5, "github.com/a/b", detailSHA); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CommitFiles(context.Background(), 6, "github.com/a/b", detailSHA); err != nil {
		t.Fatal(err)
	}
	if len(f.tokenReq) != 3 {
		t.Fatalf("other permissions or installation need their own token: %v", f.tokenReq)
	}
	// Ein Token ohne Ablauf oder kurz davor wird nicht aufgehoben.
	for _, expires := range []string{"", "2000-01-01T00:00:00Z"} {
		g := newFakeDetails(t)
		g.commit = map[string]any{"files": []map[string]any{}}
		g.expires = expires
		a := g.app()
		for i := 0; i < 2; i++ {
			if _, err := a.CommitFiles(context.Background(), 5, "github.com/a/b", detailSHA); err != nil {
				t.Fatal(err)
			}
		}
		if len(g.tokenReq) != 2 {
			t.Errorf("expires %q: the token was kept (%d fetches)", expires, len(g.tokenReq))
		}
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
