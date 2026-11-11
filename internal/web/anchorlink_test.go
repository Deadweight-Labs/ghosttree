package web

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const anchorSha = "abcdef1234567890abcdef1234567890abcdef12"

// anchorEntries legt an jedem Eintragstyp einen Eintrag an, der an anchorSha auf
// feat/x geschrieben wurde, und meldet danach den PR 12 noch einmal, damit die
// Anker seine Nummer bekommen (Zeitpunkt „synchronize“).
type anchorEntries struct {
	Request, Pain, Idea, Feature, Knowledge int64
	Comment, WebComment                     int64
}

func anchorFixture(t *testing.T) (shellEnv, anchorEntries) {
	t.Helper()
	e, pa := pullsEnv(t)
	anchor := &gitanchor.Anchor{Commit: anchorSha, Branch: "feat/x"}
	var ids anchorEntries
	req := rqMake(t, e.St, rqSpec{Title: "Anchored issue", Criteria: []string{"one"}})
	ids.Request = req.Request.ID
	if err := e.St.SetCriterionState(req.Criteria[0].ID, "met", requestdomain.Evidence{RequestID: req.Request.ID, Kind: "commit", Ref: anchorSha, Person: "alice", GitAnchor: anchor}); err != nil {
		t.Fatal(err)
	}
	pn, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Anchored pain", Body: "b", Actor: pain.Actor{AccountID: 1, Person: "alice", GitAnchor: anchor}})
	if err != nil {
		t.Fatal(err)
	}
	ids.Pain = pn.Pain.ID
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Anchored idea", Body: "b", Actor: idea.Actor{AccountID: 1, Person: "alice", GitAnchor: anchor}})
	if err != nil {
		t.Fatal(err)
	}
	ids.Idea = i.ID
	f, err := pa.CreateFeature(store.CreateFeatureInput{Project: shellProject, Message: "first", Actor: feature.Actor{AccountID: 1, Person: "alice", GitAnchor: anchor},
		Content: feature.Patch{Name: nStr("Anchored feature"), Summary: nStr("s")}})
	if err != nil {
		t.Fatal(err)
	}
	ids.Feature = f.ID
	k, err := e.St.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Anchored knowledge", Body: "text", Person: "alice", Confidence: "trusted",
		Scope: scope.Axes{Project: shellProject, Branch: "feat/x"}, GitAnchor: anchor})
	if err != nil {
		t.Fatal(err)
	}
	ids.Knowledge = k
	c, err := pa.AddComment(store.CommentOnRequest, req.Request.ID, "A comment with an anchor", store.CommentActor{Person: "alice", GitAnchor: anchor})
	if err != nil {
		t.Fatal(err)
	}
	ids.Comment = c.ID
	w, err := pa.AddComment(store.CommentOnRequest, req.Request.ID, "A comment from the browser", store.CommentActor{Person: "alice", GitAnchor: gitanchor.Web()})
	if err != nil {
		t.Fatal(err)
	}
	ids.WebComment = w.ID
	res, err := e.St.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: shellProject, HeadRemote: shellProject, Number: 12, Action: "synchronize",
		Title: "feat(web): Pull requests tab", Head: "feat/x", Base: "main", DefaultBranch: "main", State: "open"})
	if err != nil || !res.Applied {
		t.Fatalf("synchronize: %+v %v", res, err)
	}
	return e, ids
}

func TestEveryEntryTypeShowsItsGitAnchorAsLinks(t *testing.T) {
	e, ids := anchorFixture(t)
	n := strconv.FormatInt
	commit := `href="/ui/x/shell/commits/` + anchorSha + `"`
	pull := `href="/ui/x/shell/pulls/12"`
	for name, path := range map[string]string{
		"issue":     "/ui/x/shell/issues/" + n(ids.Request, 10),
		"pain":      "/ui/x/shell/pains/" + n(ids.Pain, 10),
		"idea":      "/ui/x/shell/ideas/" + n(ids.Idea, 10),
		"feature":   "/ui/x/shell/features/" + n(ids.Feature, 10),
		"knowledge": "/ui/knowledge/" + n(ids.Knowledge, 10),
	} {
		status, page := fetchPage(t, e.Owner, e.Base+path)
		if status != http.StatusOK {
			t.Fatalf("%s: %d", name, status)
		}
		for _, want := range []string{commit, anchorSha[:7] + `</span></a>`, pull, `k-branch`, "feat/x"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s page lacks %q", name, want)
			}
		}
	}
}

func TestAnchorOfACommentAndTheWebMark(t *testing.T) {
	e, ids := anchorFixture(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+strconv.FormatInt(ids.Request, 10))
	c := strings.Index(page, `id="comment-`+strconv.FormatInt(ids.Comment, 10)+`"`)
	w := strings.Index(page, `id="comment-`+strconv.FormatInt(ids.WebComment, 10)+`"`)
	if c < 0 || w < 0 || c > w {
		t.Fatalf("comments not found: %d %d", c, w)
	}
	first, second := page[c:w], page[w:]
	if !strings.Contains(first, `/commits/`+anchorSha) || strings.Contains(first, `title="Written in the web interface`) {
		t.Errorf("the comment with an anchor shows its commit and not the web mark:\n%s", first)
	}
	if !strings.Contains(second, `title="Written in the web interface, no Git state"`) || strings.Contains(second, `/commits/`+anchorSha) {
		t.Errorf("the comment from the browser shows the web mark and no commit:\n%s", second)
	}
}

func TestAnchorHidesTheBranchFromAGuestAndKeepsTheLinks(t *testing.T) {
	e, ids := anchorFixture(t)
	for name, path := range map[string]string{
		"issue": "/ui/x/shell/issues/" + strconv.FormatInt(ids.Request, 10),
		"idea":  "/ui/x/shell/ideas/" + strconv.FormatInt(ids.Idea, 10),
	} {
		status, page := fetchPage(t, e.Guest, e.Base+path)
		if status != http.StatusOK {
			t.Fatalf("guest on %s: %d", name, status)
		}
		if strings.Contains(page, "feat/x") {
			t.Errorf("a guest sees the branch name on the %s page", name)
		}
		if !strings.Contains(page, `/commits/`+anchorSha) {
			t.Errorf("a guest loses the commit link on the %s page", name)
		}
	}
}
