package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os/exec"
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

// A pull request answers with what the script needs to draw its pill: the state as
// the store keeps it (the colour), the state in the words of the page, and the
// kind with its number.
func TestUIRefsPullAnswerCarriesStateAndLabelForThePill(t *testing.T) {
	e, _ := pullsEnv(t)
	code, body := getRefs(t, e, e.Member, url.Values{"ref": {"pr:13", "pr:12", "pr:999"}, "project": {shellProject}})
	if code != http.StatusOK {
		t.Fatalf("refs = %d %s", code, body)
	}
	var out struct {
		Refs []struct {
			links.Info
			Status, Label string
		}
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Refs) != 3 {
		t.Fatalf("answer %q: %v", body, err)
	}
	merged, open, none := out.Refs[0], out.Refs[1], out.Refs[2]
	if !merged.Gefunden || merged.Status != "merged" || merged.Zustand != "Merged" || merged.Label != "Pull request #13" || merged.Titel != "fix(store): count guests" || !strings.HasSuffix(merged.Link, "/pulls/13") {
		t.Errorf("merged pull request: %+v", merged)
	}
	if !open.Gefunden || open.Status != "open" || open.Zustand != "Open" {
		t.Errorf("open pull request: %+v", open)
	}
	if none.Gefunden || none.Status != "" || none.Label != "" || none.Titel != "" {
		t.Errorf("an unknown pull request answers with more than nothing: %+v", none)
	}
	// Without a project there is no pull request, only the miss.
	if _, b := getRefs(t, e, e.Member, url.Values{"ref": {"pr:13"}}); strings.Contains(b, "count guests") {
		t.Errorf("a pull request answered without its project: %s", b)
	}
	// A knowledge entry is named KN-n in the answer, not #n.
	kid := knInsert(t, e.St, store.Knowledge{Title: "Ein Wissenseintrag", Body: "Text."})
	_, kb := getRefs(t, e, e.Member, url.Values{"ref": {"KN-" + itoa(kid)}})
	if !strings.Contains(kb, `"ref":"KN-`+itoa(kid)+`"`) {
		t.Errorf("knowledge answer: %s", kb)
	}
}

// ui-refs.js asks for the pull request of a "#231" in a repository text and for the
// knowledge entry of that number in the same call, and takes the pull request when
// both exist. Its pill is drawn the way pill.go draws it. Run in node with a
// stand-in for GTPill, cut out of the script.
func TestUIRefsScriptDrawsThePullPillAndReadsAnswersInOrder(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := string(mustReadEmbeddedFile(t, "static/ui-refs.js"))
	i, j := strings.Index(src, "  var PR_STATE"), strings.Index(src, "  // resolve turns the marks")
	if i < 0 || j <= i {
		t.Fatal("prPill is no block of its own in ui-refs.js")
	}
	program := `
const window = { GTPill: { make: (spec) => spec } };
function safeLink(u) { return typeof u === "string" && u.charAt(0) === "/" && u.charAt(1) !== "/" ? u : ""; }
` + src[i:j] + `
const eq = (a, b, m) => { if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(m + ": " + JSON.stringify(a) + " != " + JSON.stringify(b)); };
const merged = prPill("13", { status: "merged", zustand: "Zusammengeführt", label: "Pull Request #13", titel: "T", kurzfassung: "feat/y → main", link: "/ui/x/y/pulls/13" });
eq(merged.cls, "k-pr s-merged", "merged colour");
eq(merged.icon, "pr", "icon");
eq(merged.text, "#13", "text");
eq(merged.href, "/ui/x/y/pulls/13", "href");
eq(merged.card, { l: "Pull Request #13", s: "Zusammengeführt", t: "done", h: "T", b: "feat/y → main" }, "card");
eq(prPill("1", { status: "open", zustand: "Offen", link: "/p" }).cls, "k-pr s-open", "open colour");
eq(prPill("1", { status: "closed", zustand: "Geschlossen", link: "/p" }).card.t, "bad", "closed tone");
const unknown = prPill("1", { status: "", zustand: "x", link: "https://evil.example/" });
eq(unknown.cls, "k-pr s-unknown", "unknown state");
eq(unknown.card.s, "", "no state word for an unknown state");
eq(unknown.href, "", "an address outside the site is no link");
`
	if out, err := exec.Command("node", "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{`"PR-"`, `"pr:"`, "&project=", "data-repo", "list.length !== keys.length"} {
		if !strings.Contains(src, want) {
			t.Errorf("ui-refs.js lacks %q", want)
		}
	}
}

// A page of a repository tells the script which repository its texts belong to, so
// that a "#231" in them can be that repository's pull request; a page of no
// repository does not.
func TestRepoPagesCarryTheirRepositoryForTheScript(t *testing.T) {
	e, _ := pullsEnv(t)
	_, repoPage := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pulls")
	if !strings.Contains(repoPage, `<body class="ui gt"`) || !strings.Contains(repoPage, ` data-repo="`+shellProject+`"`) {
		t.Errorf("the page of a repository carries no data-repo:\n%.600s", repoPage)
	}
	_, inbox := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(inbox, "data-repo") {
		t.Error("a page of no repository carries data-repo")
	}
}
