package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func getRefs(t *testing.T, e shellEnv, c *http.Client, q url.Values) (int, string) {
	t.Helper()
	return fetchPage(t, c, e.Base+"/ui/refs?"+q.Encode())
}

func TestUIRefsAnswersLikeTheAPIAndHidesWhatTheViewerMayNotRead(t *testing.T) {
	e, pa := listEnv(t)
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "A visible idea", Body: "Idea text.", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	visible := rqMake(t, e.St, rqSpec{Title: "Visible issue", Desc: "Text of it."})
	hidden := rqMake(t, e.St, rqSpec{Title: "SECRET-ISSUE", Project: shellHiddenProject})
	vref, iref, href := "REQ-"+itoa(visible.Request.ID), "IDEA-"+itoa(i.ID), "REQ-"+itoa(hidden.Request.ID)

	code, body := getRefs(t, e, e.Member, url.Values{"ref": {vref, iref, href, "REQ-99999"}})
	if code != http.StatusOK {
		t.Fatalf("refs = %d", code)
	}
	var out struct{ Refs []links.Info }
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Refs) != 4 {
		t.Fatalf("answer %q: %v", body, err)
	}
	if !out.Refs[0].Gefunden || out.Refs[0].Titel != "Visible issue" || !strings.HasPrefix(out.Refs[0].Link, "/ui/") || strings.Contains(out.Refs[0].Link, "://") {
		t.Errorf("issue entry: %+v", out.Refs[0])
	}
	if !out.Refs[1].Gefunden || out.Refs[1].Titel != "A visible idea" {
		t.Errorf("idea entry: %+v", out.Refs[1])
	}
	// Forbidden and unknown are the same entry.
	if out.Refs[2].Gefunden || out.Refs[3].Gefunden || out.Refs[2].Titel != "" || out.Refs[2].Kind != out.Refs[3].Kind || out.Refs[2].Link != "" {
		t.Errorf("forbidden %+v and unknown %+v differ from nothing", out.Refs[2], out.Refs[3])
	}
	if strings.Contains(body, "SECRET-ISSUE") {
		t.Error("the title of a forbidden issue left the server")
	}
	// Only unreadable ones: always the same 200 with an empty list.
	c1, b1 := getRefs(t, e, e.Member, url.Values{"ref": {href}})
	c2, b2 := getRefs(t, e, e.Member, url.Values{"ref": {"REQ-99999"}})
	if c1 != http.StatusOK || c2 != http.StatusOK || b1 != b2 || !strings.Contains(b1, `"refs":[]`) {
		t.Errorf("forbidden %d %q and unknown %d %q differ", c1, b1, c2, b2)
	}
	// A guest reads what a guest may read, not the idea of the members.
	if _, gb := getRefs(t, e, e.Guest, url.Values{"ref": {iref}}); strings.Contains(gb, "A visible idea") && !strings.Contains(gb, `"gefunden":true`) {
		t.Errorf("guest answer %q", gb)
	}
	// Limits and spelling.
	if code, _ := getRefs(t, e, e.Member, url.Values{}); code != http.StatusBadRequest {
		t.Errorf("no ref -> %d, want 400", code)
	}
	many := url.Values{}
	for n := 0; n < links.MaxPeekRefs+1; n++ {
		many.Add("ref", "REQ-1")
	}
	if code, _ := getRefs(t, e, e.Member, many); code != http.StatusBadRequest {
		t.Errorf("51 refs -> %d, want 400", code)
	}
	if code, _ := getRefs(t, e, e.Member, url.Values{"ref": {"nonsense"}}); code != http.StatusBadRequest {
		t.Errorf("a wrong spelling -> %d, want 400", code)
	}
}

func TestUIRefsNeedsASession(t *testing.T) {
	e := ovEnv(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(e.Base + "/ui/refs?ref=REQ-1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode == http.StatusOK {
		t.Errorf("without a session -> %d", resp.StatusCode)
	}
}

func TestHoverCardScriptAsksTheServerForUnknownReferences(t *testing.T) {
	raw, err := files.ReadFile("static/ui-refs.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/ui/refs", "lookup", "textContent", "inflight"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ui-refs.js lacks %q", want)
		}
	}
	if strings.Contains(string(raw), "innerHTML") {
		t.Error("the card is built from a string")
	}
}

func TestUIRefsPullSummaryHidesBranchesFromGuests(t *testing.T) {
	e, _ := pullsEnv(t)
	q := url.Values{"ref": {"pr:12"}, "project": {shellProject}}
	code, body := getRefs(t, e, e.Guest, q)
	if code != http.StatusOK || strings.Contains(body, "feat/x") {
		t.Errorf("guest pr card = %d %s", code, body)
	}
	if _, body = getRefs(t, e, e.Member, q); !strings.Contains(body, "feat/x") {
		t.Errorf("member pr card lacks the branch: %s", body)
	}
}
