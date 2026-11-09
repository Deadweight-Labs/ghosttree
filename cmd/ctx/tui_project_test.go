package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/tui"
)

func problemOf(t *testing.T, err error) tui.Problem {
	t.Helper()
	var pe *tui.ProblemError
	if !errors.As(err, &pe) {
		t.Fatalf("not a problem: %v", err)
	}
	return pe.Kind
}

func TestClassifyErrSpeaksInProblems(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want tui.Problem
	}{
		{&client.APIError{Status: 400, Code: "project_required"}, tui.ProblemNoProject},
		{&client.APIError{Status: 404, Code: "project_not_found"}, tui.ProblemNoProject},
		{&client.StatusError{Status: 404}, tui.ProblemNoProject},
		{&client.APIError{Status: 403, Code: "forbidden"}, tui.ProblemForbidden},
		{&client.StatusError{Status: 401}, tui.ProblemSignedOut},
		{&client.StatusError{Status: 502}, tui.ProblemOffline},
		{fmt.Errorf("get: %w", context.DeadlineExceeded), tui.ProblemOffline},
		{&client.APIError{Status: 409, Code: "odd"}, tui.ProblemOther},
	} {
		if got := problemOf(t, classifyErr(tc.err)); got != tc.want {
			t.Errorf("%v: %v, want %v", tc.err, got, tc.want)
		}
	}
	if classifyErr(nil) != nil {
		t.Fatal("nil became an error")
	}
	// Input errors keep their words, transport errors do not show.
	if got := friendlyErr(&client.APIError{Status: 404, Code: "preset_not_found", Message: "no saved search \x1b[2Jnamed x"}).Error(); strings.ContainsRune(got, '\x1b') || !strings.Contains(got, "no saved search") {
		t.Fatalf("friendly %q", got)
	}
	if got := friendlyErr(&client.StatusError{Method: "GET", Path: "/api/x", Status: 400, Body: "raw"}); strings.Contains(got.Error(), "GET") {
		t.Fatalf("raw status reached the user: %v", got)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestFolderInfoForTheFourKindsOfFolder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	plain := filepath.Join(root, "plain")
	noRemote := filepath.Join(root, "noremote")
	withRemote := filepath.Join(root, "remote", "sub")
	for _, d := range []string{plain, noRemote, withRemote} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, noRemote, "init", "-q")
	gitIn(t, filepath.Join(root, "remote"), "init", "-q")
	gitIn(t, filepath.Join(root, "remote"), "remote", "add", "origin", "https://github.com/someone/side.git")

	for _, tc := range []struct {
		dir    string
		git    bool
		remote string
	}{
		{plain, false, ""},
		{noRemote, true, ""},
		{withRemote, true, "github.com/someone/side"},
	} {
		t.Chdir(tc.dir)
		_, git, remote, linked := folderInfo()
		if git != tc.git || remote != tc.remote || linked {
			t.Errorf("%s: git %v remote %q linked %v", tc.dir, git, remote, linked)
		}
	}
}

func TestOpenRequestsWithoutProjectIsNoProjectNotEverything(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	_, err := newCLIBackend().List(context.Background(), tui.ListRequests)
	if problemOf(t, err) != tui.ProblemNoProject || calls != 0 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

func TestMessagesOfARoomThatDoesNotExistAreEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"coordination target not found"}`)
	}))
	defer srv.Close()
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/someone/side.git")
	t.Chdir(dir)
	items, err := newCLIBackend().List(context.Background(), tui.ListRoom)
	if err != nil || len(items) != 0 {
		t.Fatalf("items %v err %v", items, err)
	}
}

// Nothing the TUI lists may write: a write in an unknown repository puts it
// into an organization (PAIN-67). Every list of both modes is asked in a
// repository the server does not know, and every request must be a GET.
func TestEveryListOnlyReads(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/projects":
			fmt.Fprint(w, `[{"remote":"github.com/someone/side","org":"o"}]`)
		case "/api/projects/writable", "/api/inbox", "/api/knowledge/pending", "/api/pains", "/api/tasks":
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		}
	}))
	defer srv.Close()
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/someone/side.git")
	t.Chdir(dir)
	b := newCLIBackend()
	for k := tui.ListRequests; k <= tui.ListSuggestions; k++ {
		_, _ = b.List(context.Background(), k)
	}
	_, _ = b.CheckProject(context.Background(), "github.com/someone/side")
	_, _ = b.Projects(context.Background())
	_, _, _ = b.ProjectPreview(context.Background(), "github.com/someone/side")
	if len(methods) == 0 {
		t.Fatal("no request seen")
	}
	for _, m := range methods {
		if !strings.HasPrefix(m, "GET ") {
			t.Fatalf("a list wrote: %v", methods)
		}
	}
}

func TestCompleteAndChangeDir(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"alpha", "alpine", "beta", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "alfile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	b := newCLIBackend()
	got := b.CompleteDir(filepath.Join(root, "al"))
	if len(got) != 2 || !strings.HasSuffix(got[0], "alpha/") || !strings.HasSuffix(got[1], "alpine/") {
		t.Fatalf("completion %v", got)
	}
	if got := b.CompleteDir(root + "/"); len(got) != 3 {
		t.Fatalf("listing %v (hidden folders and files stay out)", got)
	}
	t.Chdir(t.TempDir())
	if err := b.ChangeDir(filepath.Join(root, "beta")); err != nil {
		t.Fatal(err)
	}
	if wd, _ := os.Getwd(); wd != filepath.Join(root, "beta") {
		t.Fatalf("cwd %s", wd)
	}
	if err := b.ChangeDir(filepath.Join(root, "missing")); err == nil {
		t.Fatal("changed into a missing folder")
	}
	if err := b.ChangeDir(filepath.Join(root, "alfile")); err == nil {
		t.Fatal("changed into a file")
	}
}

func TestClaimPlanNeedsARemoteAndClaimExplainsRefusals(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/orgs":
			fmt.Fprint(w, `[{"id":1,"slug":"dwl","name":"Deadweight Labs","role":"owner","default":true}]`)
		case "/api/projects/claim":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"code":"not_org_owner","message":"only owners"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if err := config.Save(config.Config{ServerURL: srv.URL, Token: "t", Machine: "m"}); err != nil {
		t.Fatal(err)
	}
	b := newCLIBackend()
	dir := t.TempDir()
	t.Chdir(dir)
	var ce *tui.ClaimError
	if _, err := b.ClaimPlan(context.Background()); !errors.As(err, &ce) || ce.Code != "no_remote" {
		t.Fatalf("plan without remote: %v", err)
	}
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "remote", "add", "origin", "git@github.com:someone/side.git")
	plan, err := b.ClaimPlan(context.Background())
	if err != nil || plan.Remote != "github.com/someone/side" || len(plan.Orgs) != 1 || !plan.Orgs[0].Default || plan.Orgs[0].Role != "owner" {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	if _, err := b.Claim(context.Background(), plan.Remote, "dwl"); !errors.As(err, &ce) || ce.Code != "not_org_owner" {
		t.Fatalf("claim: %v", err)
	}
}
