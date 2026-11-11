package server

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const pullProject = "github.com/alpha-labs/app"

func pullFixture(t *testing.T) orgFixture {
	t.Helper()
	f := newOrgFixture(t)
	org, err := f.st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ConnectSourceWithInstallation("person:1", org.ID, "github", "alpha-labs", 55, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ClaimProject("person:1", pullProject, "alpha"); err != nil {
		t.Fatal(err)
	}
	return f
}

func pullKnowledge(t *testing.T, f orgFixture, branch, title string) int64 {
	t.Helper()
	id, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "text", Person: "robin",
		Scope:     scope.Axes{Project: pullProject, Branch: branch},
		GitAnchor: &gitanchor.Anchor{Commit: "aaaaaaa1111111111111111111111111111111aa", Branch: branch}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPullKnowledgeListsWhatWasSaidOnThePullRequest(t *testing.T) {
	f := pullFixture(t)
	pullKnowledge(t, f, "feat/a", "auf dem PR")
	pullKnowledge(t, f, "feat/b", "anderer Branch")
	if _, err := f.st.RecordPullRequest(store.PullRequestChange{InstallationID: 55, Remote: pullProject, HeadRemote: pullProject,
		Number: 7, Action: "opened", Title: "PR", Head: "feat/a", Base: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
	path := "/api/pulls/7/knowledge?project=" + url.QueryEscape(pullProject)
	out := f.mustCall(t, 200, "GET", path, f.robin, nil)
	ks := out["knowledge"].([]any)
	if len(ks) != 1 || ks[0].(map[string]any)["title"] != "auf dem PR" || out["known"] != true || out["head"] != "feat/a" {
		t.Fatalf("brief: %v", out)
	}
	// Ein PR, den es nicht gibt, hat eine leere Liste.
	out = f.mustCall(t, 200, "GET", "/api/pulls/99/knowledge?project="+url.QueryEscape(pullProject), f.robin, nil)
	if len(out["knowledge"].([]any)) != 0 || out["known"] != false {
		t.Fatalf("unknown pr: %v", out)
	}
	f.mustCall(t, 400, "GET", "/api/pulls/0/knowledge?project="+url.QueryEscape(pullProject), f.robin, nil)
	f.mustCall(t, 400, "GET", "/api/pulls/7/knowledge", f.robin, nil)
	f.mustCall(t, 401, "GET", path, "", nil)
}

// Wer das Projekt nicht sehen darf, bekommt dieselbe Antwort wie für ein
// Projekt, das es nicht gibt.
func TestPullKnowledgeAnswersAnOutsiderLikeAnUnknownProject(t *testing.T) {
	f := pullFixture(t)
	pullKnowledge(t, f, "feat/a", "geheim")
	if _, err := f.st.RecordPullRequest(store.PullRequestChange{InstallationID: 55, Remote: pullProject, HeadRemote: pullProject,
		Number: 7, Action: "opened", Head: "feat/a", Base: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
	f.st.SetAccessMode(store.AccessMode{Enforce: true})
	known := f.call
	codeOutside, bodyOutside := known(t, "GET", "/api/pulls/7/knowledge?project="+url.QueryEscape(pullProject), f.carl, nil)
	codeUnknown, bodyUnknown := known(t, "GET", "/api/pulls/7/knowledge?project="+url.QueryEscape("github.com/alpha-labs/nothing"), f.carl, nil)
	if codeOutside != codeUnknown || len(bodyOutside) != len(bodyUnknown) {
		t.Fatalf("outsider got %d %v, unknown project got %d %v", codeOutside, bodyOutside, codeUnknown, bodyUnknown)
	}
	if ks, _ := bodyOutside["knowledge"].([]any); len(ks) != 0 {
		t.Fatalf("an outsider saw knowledge: %v", bodyOutside)
	}
	if out := f.mustCall(t, 200, "GET", "/api/pulls/7/knowledge?project="+url.QueryEscape(pullProject), f.robin, nil); len(out["knowledge"].([]any)) != 1 {
		t.Fatalf("the owner lost the list: %v", out)
	}
}

func TestGhostDescriptionsFollowTheBranchOverTheAPI(t *testing.T) {
	f := pullFixture(t)
	put := func(branch, text string, want int) {
		t.Helper()
		f.mustCall(t, want, "POST", "/api/ghosts", f.robin, map[string]any{"project": pullProject, "path": "src/a.go", "kind": "file", "description": text, "branch": branch})
	}
	put("", "Projektebene", 200)
	put("feat/a", "auf feat/a", 200)
	put("bad\nbranch", "x", 400)
	treeText := func(query string) string {
		t.Helper()
		resp := req(t, "GET", f.srv.URL+"/api/ghosts/tree?project="+url.QueryEscape(pullProject)+query, f.robin, nil)
		defer resp.Body.Close()
		var list []store.GhostFile
		if err := jsonDecode(resp.Body, &list); err != nil || len(list) != 1 {
			t.Fatalf("tree %q: %v %v", query, list, err)
		}
		return list[0].Description
	}
	if got := treeText(""); got != "Projektebene" {
		t.Fatalf("without a branch: %q", got)
	}
	if got := treeText("&branch=main&lineage=x"); got != "Projektebene" {
		t.Fatalf("the origin: %q", got)
	}
	if got := treeText("&branch=feat/a"); got != "auf feat/a" {
		t.Fatalf("the branch: %q", got)
	}
	if got := treeText("&branch=feat/a-x&lineage=main&lineage=feat/a"); got != "auf feat/a" {
		t.Fatalf("a branch cut from it: %q", got)
	}
	// Die Auslieferung des Hooks (GET /api/ghosts) folgt dem Branch ebenso.
	resp := req(t, "GET", f.srv.URL+"/api/ghosts?project="+url.QueryEscape(pullProject)+"&path=src/a.go&session=s1&branch=feat/a", f.robin, nil)
	defer resp.Body.Close()
	var delivered []store.GhostFile
	if err := jsonDecode(resp.Body, &delivered); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, g := range delivered {
		if g.Path == "src/a.go" && g.Description == "auf feat/a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("delivery: %+v", delivered)
	}
}

func jsonDecode(r interface{ Read([]byte) (int, error) }, v any) error {
	return json.NewDecoder(r).Decode(v)
}
