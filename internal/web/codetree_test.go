package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestMatchBranchTakesTheLongestKnownPrefix(t *testing.T) {
	known := []string{"main", "feat", "feat/git-native", "release/1.0"}
	for _, c := range []struct {
		segs     []string
		branch   string
		rest     []string
		matching bool
	}{
		{[]string{"main", "internal", "store"}, "main", []string{"internal", "store"}, true},
		{[]string{"feat", "git-native", "internal"}, "feat/git-native", []string{"internal"}, true},
		{[]string{"feat", "other", "x.go"}, "feat", []string{"other", "x.go"}, true},
		{[]string{"feat", "git-native"}, "feat/git-native", []string{}, true},
		{[]string{"release", "1.0", "a"}, "release/1.0", []string{"a"}, true},
		{[]string{"release", "2.0"}, "", nil, false},
		{[]string{"nothere", "internal"}, "", nil, false},
	} {
		b, rest, ok := matchBranch(known, c.segs)
		if ok != c.matching || b != c.branch || strings.Join(rest, "/") != strings.Join(c.rest, "/") {
			t.Errorf("matchBranch(%v) = %q %v %v, want %q %v %v", c.segs, b, rest, ok, c.branch, c.rest, c.matching)
		}
	}
}

func TestCleanPathRefusesWhatIsNoRepositoryPath(t *testing.T) {
	for _, bad := range [][]string{{"a", ".."}, {"."}, {"a", ""}, {"a\\b"}, {"a\x00"}, {"a\nb"}} {
		if _, ok := cleanPath(bad); ok {
			t.Errorf("cleanPath(%q) accepted", bad)
		}
	}
	if p, ok := cleanPath([]string{"internal", "store", "a.go"}); !ok || p != "internal/store/a.go" {
		t.Errorf("clean path: %q %v", p, ok)
	}
	if p, ok := cleanPath(nil); !ok || p != "" {
		t.Errorf("empty path is the top: %q %v", p, ok)
	}
}

func TestGlobPrefixIsTheFixedBeginningOfAPattern(t *testing.T) {
	for in, want := range map[string]string{"internal/store/**": "internal/store", "internal/*_test.go": "internal", "*.go": "", "internal/st*": "internal", "cmd/ctx/main.go": "cmd/ctx/main.go"} {
		if got := globPrefix(in); got != want {
			t.Errorf("globPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// codeSeed fills a project with a small ghost tree, knowledge on two branches
// and a description with a history.
func codeSeed(t *testing.T, st *store.Store, project string) {
	t.Helper()
	anchor := func(sha, branch string) *gitanchor.Anchor { return &gitanchor.Anchor{Commit: sha, Branch: branch} }
	put := func(g store.GhostFile) {
		t.Helper()
		g.Project = project
		if _, err := st.PutGhostFile(g); err != nil {
			t.Fatal(err)
		}
	}
	put(store.GhostFile{Path: "", Kind: "dir", Description: "Root **description** with `code`."})
	put(store.GhostFile{Path: "internal", Kind: "dir", Description: "All server code."})
	put(store.GhostFile{Path: "internal/store/gitanchor.go", Kind: "file", Description: "Old text. Gone sentence.", Person: "robin", Harness: "claude-code", GitAnchor: anchor("aaaaaaa1", "main")})
	put(store.GhostFile{Path: "internal/store/gitanchor.go", Kind: "file", Description: "Holds branch and commit. <script>alert(1)</script>", Person: "robin", Harness: "claude-code", GitAnchor: anchor("bbbbbbb2", "feat/git-native"),
		OriginContext: &originctx.Context{Areas: []originctx.Area{{Path: "internal/store", Weight: 3}}}})
	put(store.GhostFile{Path: "internal/web/a.go", Kind: "file", Description: ""})
	put(store.GhostFile{Path: "README.md", Kind: "file", Description: "Product page."})
	know := func(typ, title, branch string, rule activation.Rule, oc *originctx.Context) {
		t.Helper()
		if _, err := st.InsertKnowledge(store.Knowledge{Type: typ, Title: title, Body: "b", Scope: scope.Axes{Project: project, Branch: branch}, Person: "alice", Confidence: "trusted",
			Activation: rule, OriginContext: oc}); err != nil {
			t.Fatal(err)
		}
	}
	know("pitfall", "Anchor pitfall", "feat/git-native", activation.Rule{}, &originctx.Context{
		Paths: []originctx.Path{{Path: "internal/store/gitanchor.go", Kind: originctx.KindNamed, Weight: 10}}, Areas: []originctx.Area{{Path: "internal/store", Weight: 10}}})
	know("note", "Area note", "", activation.Rule{}, &originctx.Context{Areas: []originctx.Area{{Path: "internal/store", Weight: 2}}})
	know("instruction", "Trigger rule", "", activation.Rule{Paths: []string{"internal/store/**"}}, nil)
	know("note", "Elsewhere", "", activation.Rule{}, &originctx.Context{Areas: []originctx.Area{{Path: "internal/web", Weight: 2}}})
}

func TestCodeTabTreeFolderAndFilePages(t *testing.T) {
	e := ovEnv(t)
	codeSeed(t, e.St, shellProject)

	// Top of the default branch: folders first, description, counters.
	status, top := fetchPage(t, e.Member, e.Base+"/ui/x/shell")
	if status != http.StatusOK {
		t.Fatalf("top: %d", status)
	}
	for _, want := range []string{`class="ct-btn"`, `<span class="ct-mono">main</span>`, `href="/ui/x/shell/tree/main/internal"`, "All server code.",
		`href="/ui/x/shell/blob/main/README.md"`, "Root <strong>description</strong> with <code>code</code>.", `id="ct-filter"`, `class="ct-sha"`} {
		if !strings.Contains(top, want) {
			t.Errorf("top lacks %q", want)
		}
	}
	// internal/ adds up the pitfall and the three other entries that name something below it (store and web).
	if !strings.Contains(top, `title="1 pitfall"`) || !strings.Contains(top, `title="4 knowledge entries"`) {
		t.Errorf("counters of internal/ are wrong:\n%s", top)
	}
	if strings.Index(top, "internal/") > strings.Index(top, "README.md") {
		t.Error("folders do not come first")
	}

	// A folder: crumb, its rows, its own description below.
	_, dir := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tree/main/internal/store")
	for _, want := range []string{`class="ct-path"`, `<b>store</b>`, `href="/ui/x/shell/tree/main/internal"`, "gitanchor.go", "Holds branch and commit."} {
		if !strings.Contains(dir, want) {
			t.Errorf("folder page lacks %q", want)
		}
	}
	if strings.Contains(dir, "<script>alert") {
		t.Error("description markup reached the page")
	}

	// A file without a description is one action.
	_, web := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tree/main/internal/web")
	if !strings.Contains(web, `href="/ui/x/shell/blob/main/internal/web/a.go#ct-edit">Write description`) {
		t.Errorf("an undescribed file offers no action:\n%s", web)
	}
	_, blank := fetchPage(t, e.Member, e.Base+"/ui/x/shell/blob/main/internal/web/a.go")
	if !strings.Contains(blank, `<details class="ct-edit" id="ct-edit" open>`) || !strings.Contains(blank, `context_describe_file path=&#34;internal/web/a.go&#34;`) {
		t.Errorf("empty description page lacks the open writing action:\n%s", blank)
	}

	// The file page: description, history with diff, knowledge by path, link to GitHub.
	status, blob := fetchPage(t, e.Member, e.Base+"/ui/x/shell/blob/main/internal/store/gitanchor.go")
	if status != http.StatusOK {
		t.Fatalf("file page: %d", status)
	}
	for _, want := range []string{"Holds branch and commit.", "&lt;script&gt;", "Revision 2", "Revision 1", "bbbbbbb", "aaaaaaa",
		`href="/ui/x/shell/commits/bbbbbbb2"`, "Gone sentence.", "Anchor pitfall", "Area note", "Trigger rule", "Area: internal/store",
		`href="https://github.com/x/shell/blob/main/internal/store/gitanchor.go"`, `is-cur" href="/ui/x/shell/blob/main/internal/store/gitanchor.go"`} {
		if !strings.Contains(blob, want) {
			t.Errorf("file page lacks %q", want)
		}
	}
	if strings.Contains(blob, "Elsewhere") {
		t.Error("the file page lists knowledge of another area")
	}
	if strings.Index(blob, "Anchor pitfall") > strings.Index(blob, "Area note") {
		t.Error("knowledge that names the path must come before area knowledge")
	}
}

func TestCodeTabBranchesWithSlashesAndRedirects(t *testing.T) {
	e := ovEnv(t)
	codeSeed(t, e.St, shellProject)
	// "feat" and "feat/git-native" both exist: the longer wins.
	if _, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: "short branch", Body: "b", Scope: scope.Axes{Project: shellProject, Branch: "feat"}, Person: "alice", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	_, long := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tree/feat/git-native/internal")
	if !strings.Contains(long, `<span class="ct-mono">feat/git-native</span>`) || !strings.Contains(long, "All server code.") {
		t.Errorf("slash branch not resolved:\n%s", long)
	}
	_, short := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tree/feat/internal")
	if !strings.Contains(short, `<span class="ct-mono">feat</span>`) {
		t.Errorf("the shorter branch is not found for feat/internal (path internal):\n%s", short)
	}
	// The switch lists branches with their link to the same place and no longer says what a branch inherits.
	for _, want := range []string{`href="/ui/x/shell/tree/feat/git-native/internal"`, `href="/ui/x/shell/tree/main/internal"`, "Default"} {
		if !strings.Contains(long, want) {
			t.Errorf("branch switch lacks %q", want)
		}
	}
	if strings.Contains(long, "inherits from") {
		t.Error("the branch switch still says what a branch inherits")
	}
	_, onFile := fetchPage(t, e.Member, e.Base+"/ui/x/shell/blob/feat/git-native/internal/store/gitanchor.go")
	if !strings.Contains(onFile, `href="/ui/x/shell/blob/main/internal/store/gitanchor.go" data-branch="main"`) {
		t.Error("on a file page the switch keeps the file")
	}

	redirects := map[string]string{
		"/ui/x/shell/tree": "/ui/x/shell",
		"/ui/x/shell/tree/main/internal/store/gitanchor.go": "/ui/x/shell/blob/main/internal/store/gitanchor.go",
		"/ui/x/shell/blob/main/internal":                    "/ui/x/shell/tree/main/internal",
		"/ui/x/shell/blob/main":                             "/ui/x/shell",
	}
	for path, want := range redirects {
		resp, err := e.Member.Get(e.Base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
			t.Errorf("%s -> %d %q, want 302 %q", path, resp.StatusCode, resp.Header.Get("Location"), want)
		}
	}
	for _, path := range []string{"/ui/x/shell/tree/nobranch/internal", "/ui/x/shell/tree/main/nothere", "/ui/x/shell/blob/main/internal/store/missing.go",
		"/ui/x/shell/tree/main/internal/%2e%2e"} {
		if status, _ := fetchPage(t, e.Member, e.Base+path); status != http.StatusNotFound && status != http.StatusMovedPermanently {
			t.Errorf("%s -> %d, want 404", path, status)
		}
	}
	// The host form of the address reaches the same pages.
	if status, _ := fetchPage(t, e.Member, e.Base+"/ui/github.com/x/shell/tree/main/internal"); status != http.StatusOK {
		t.Errorf("host form: %d", status)
	}
}

func TestCodeTabOfAHiddenProjectAnswersLikeAnUnknownOne(t *testing.T) {
	e := ovEnv(t)
	codeSeed(t, e.St, shellHiddenProject)
	for name, c := range map[string]*http.Client{"guest": e.Guest, "member": e.Member} {
		for _, tail := range []string{"", "/tree/main", "/tree/main/internal", "/blob/main/internal/store/gitanchor.go", "/tree/feat/git-native/internal"} {
			hs, hp := fetchPage(t, c, e.Base+"/ui/x/hidden"+tail)
			us, up := fetchPage(t, c, e.Base+"/ui/x/nothere"+tail)
			if hs != http.StatusNotFound || us != http.StatusNotFound || hp != up {
				t.Errorf("%s: %q hidden=%d unknown=%d same=%v", name, tail, hs, us, hp == up)
			}
			if strings.Contains(hp, "gitanchor") || strings.Contains(hp, "server code") {
				t.Errorf("%s: %q leaks the project", name, tail)
			}
		}
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/hidden/blob/main/internal/store/gitanchor.go"); status != http.StatusOK {
		t.Errorf("owner: %d", status)
	}
}

func TestCodeTabGuestSeesNoBranchNames(t *testing.T) {
	e := ovEnv(t)
	codeSeed(t, e.St, shellProject)
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell")
	if strings.Contains(guest, "feat/git-native") {
		t.Error("a guest sees a branch name that only knowledge carries")
	}
	if status, _ := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/tree/feat/git-native/internal"); status != http.StatusNotFound {
		t.Errorf("a guest reaches a branch it may not know: %d", status)
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/x/shell")
	if !strings.Contains(member, "feat/git-native") {
		t.Error("a member does not see the branch")
	}
}

func TestCodeTabShowsWhoWorkedOnAPathAsFarAsTheViewerMaySee(t *testing.T) {
	e := ovEnv(t)
	codeSeed(t, e.St, shellProject)
	if _, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "sess-1", Scope: scope.Axes{Project: shellProject, Machine: "laptop"},
		StartedAt: time.Now().UTC().Format(time.RFC3339), LastSeenAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordPathActivity([]store.PathActivity{{Project: shellProject, SessionExternalID: "sess-1", Tool: "Edit", Path: "internal/store/gitanchor.go",
		Writes: true, Quality: store.ActivityReported, At: time.Now().UTC().Format(time.RFC3339)}}); err != nil {
		t.Fatal(err)
	}
	// The session is private to its owner, the instance owner: nobody else sees a
	// row for it, not even a member.
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/blob/main/internal/store/gitanchor.go")
	if !strings.Contains(owner, "Last worked on here") || !strings.Contains(owner, "claude@laptop") {
		t.Errorf("owner does not see the work:\n%s", owner)
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/x/shell/blob/main/internal/store/gitanchor.go")
	if strings.Contains(member, "laptop") || strings.Contains(member, "Last worked on here") {
		t.Error("a member sees a private session of somebody else")
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/blob/main/internal/store/gitanchor.go")
	if strings.Contains(guest, "laptop") || strings.Contains(guest, "Last worked on here") {
		t.Error("a guest sees machine or sessions")
	}
}
