package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The search by number (REQ-628): a term that is only a number finds the object
// with that number first, as a hit like the others, above the full-text hits.

// exactEnv is the pull request fixture with, in the project "shell", a request
// whose number is also the number of a pull request (so that #N is ambiguous), a
// knowledge entry with the same number, an idea, a pain and a task, and in the
// project "hidden", which only the owner may read, a request, an idea and a pull
// request (99).
type exactEnv struct {
	shellEnv
	N                             int64 // the request, the pull request and (if it reached the number) the knowledge entry
	Knowledge                     bool  // the knowledge entry has the number N
	Idea, Pain, Task              int64
	HiddenReq, HiddenIdea         int64
	ReqHref, PullHref, KnowHref   string
	IdeaHref, PainHref, TaskHref  string
	HiddenReqHref, HiddenPullHref string
}

func seedExact(t *testing.T) exactEnv {
	t.Helper()
	e, pa := pullsEnv(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	x := exactEnv{shellEnv: e}
	req := rqMake(t, e.St, rqSpec{Title: "exactfind request"})
	x.N = req.Request.ID
	if slices.Contains([]int64{12, 13, 14, 99}, x.N) {
		t.Fatalf("the request has the number of another pull request: %d", x.N)
	}
	res, err := e.St.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: shellProject, HeadRemote: shellProject, Number: x.N, Action: "opened",
		Title: "exactfind pull request", Head: "feat/exact", Base: "main", State: "open", Author: "alice-gh"})
	if err != nil || !res.Applied {
		t.Fatalf("record pull %d: %+v %v", x.N, res, err)
	}
	// The knowledge entry takes the number as far as the counter has not passed it.
	for i := 0; i < 20 && !x.Knowledge; i++ {
		id := knInsert(t, e.St, store.Knowledge{Title: "exactfind note", Body: "a note"})
		if id > x.N {
			break
		}
		x.Knowledge = id == x.N
	}
	actor := idea.Actor{AccountID: 1, Person: "alice"}
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "exactfind idea", Body: "text", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "exactfind pain", Body: "text", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := pa.CreateHumanTask(humantask.CreateInput{RequestID: x.N, Project: shellProject, Kind: "action", Title: "exactfind task", Everyone: true}, humantask.Actor{AccountID: 1, Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	x.Idea, x.Pain, x.Task = i.ID, p.Pain.ID, tk.ID
	hidden := rqMake(t, e.St, rqSpec{Title: "SECRET exact request", Project: shellHiddenProject})
	hi, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellHiddenProject, Title: "SECRET exact idea", Body: "text", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	x.HiddenReq, x.HiddenIdea = hidden.Request.ID, hi.ID
	x.ReqHref, x.PullHref = requestURL(x.N), "/ui/x/shell/pulls/"+itoa(x.N)
	x.IdeaHref, x.PainHref, x.TaskHref = "/ui/x/shell/ideas/"+itoa(x.Idea), painURL(x.Pain), "/ui/x/shell/tasks/"+itoa(x.Task)
	x.HiddenReqHref, x.HiddenPullHref = requestURL(x.HiddenReq), "/ui/x/hidden/pulls/99"
	return x
}

var exactRowRE = regexp.MustCompile(`(?s)<div class="hit hit-exact"[^>]*>.*?<a class="ht rl" href="([^"]*)"`)

// exactOf are the addresses of the exact hits of a page, hitsOf of all hits, both in order.
func exactOf(page string) []string {
	var out []string
	for _, m := range exactRowRE.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

func hitsOf(page string) []string {
	var out []string
	for _, m := range hitHrefRE.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

func exactSearch(t *testing.T, x exactEnv, c *http.Client, q string, extra ...string) string {
	t.Helper()
	u := "/ui/search?q=" + url.QueryEscape(q)
	for _, e := range extra {
		u += "&" + e
	}
	code, page := fetchPage(t, c, x.Base+u)
	if code != http.StatusOK {
		t.Fatalf("search %q = %d", q, code)
	}
	return page
}

func TestSearchByNumberShowsTheObjectAsTheFirstHitInEveryForm(t *testing.T) {
	x := seedExact(t)
	n := itoa(x.N)
	for _, tc := range []struct {
		q    string
		want string
	}{
		{"#" + n, x.ReqHref},
		{"  #" + n + "  ", x.ReqHref},
		{"REQ-" + n, x.ReqHref},
		{"req-" + n, x.ReqHref},
		{" Req-" + n + " ", x.ReqHref},
		{"pr:" + n, x.PullHref},
		{"PR:" + n, x.PullHref},
		{"pr:#" + n, x.PullHref},
		{"IDEA-" + itoa(x.Idea), x.IdeaHref},
		{"idea-" + itoa(x.Idea), x.IdeaHref},
		{"PAIN-" + itoa(x.Pain), x.PainHref},
		{"pain-" + itoa(x.Pain), x.PainHref},
		{"TASK-" + itoa(x.Task), x.TaskHref},
		{"task-" + itoa(x.Task), x.TaskHref},
	} {
		page := exactSearch(t, x, x.Owner, tc.q)
		hits := hitsOf(page)
		if len(hits) == 0 || hits[0] != tc.want {
			t.Errorf("%q: the first hit is %v, want %s", tc.q, hits, tc.want)
		}
		if ex := exactOf(page); len(ex) == 0 || ex[0] != tc.want {
			t.Errorf("%q: the exact hits are %v, want %s first", tc.q, ex, tc.want)
		}
		mustContain(t, tc.q, page, `<span class="chip-exact">Exact</span>`)
	}
}

func TestSearchByHashNumberPinsEveryKindThatHasTheNumber(t *testing.T) {
	x := seedExact(t)
	page := exactSearch(t, x, x.Owner, "#"+itoa(x.N))
	want := []string{x.ReqHref, x.PullHref}
	if x.Knowledge {
		want = append(want, knowledgeHref(shellProject, x.N))
	}
	if got := exactOf(page); !slices.Equal(got, want) {
		t.Errorf("#%d pins %v, want %v", x.N, got, want)
	}
	if got := hitsOf(page); len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Errorf("#%d: the pinned hits are not the first ones: %v", x.N, got)
	}
	if !x.Knowledge {
		t.Log("the knowledge entry did not reach the number: the three-kind case is covered by the links test")
	}
	// A number that names a kind pins only that kind: pr:N does not pin the request.
	if got := exactOf(exactSearch(t, x, x.Owner, "pr:"+itoa(x.N))); !slices.Equal(got, []string{x.PullHref}) {
		t.Errorf("pr:%d pins %v", x.N, got)
	}
	if got := exactOf(exactSearch(t, x, x.Owner, "REQ-"+itoa(x.N))); !slices.Equal(got, []string{x.ReqHref}) {
		t.Errorf("REQ-%d pins %v", x.N, got)
	}
}

func TestSearchByNumberFindsNothingForANumberThatDoesNotExist(t *testing.T) {
	x := seedExact(t)
	for _, q := range []string{"#987654", "REQ-987654", "pr:987654", "PAIN-987654", "TASK-987654", "IDEA-987654"} {
		page := exactSearch(t, x, x.Owner, q)
		if got := exactOf(page); len(got) != 0 {
			t.Errorf("%q pins %v", q, got)
		}
		mustNotContain(t, q, page, "hit-exact", "Exact</span>")
	}
	// What is more than a number is a search, not a lookup.
	if got := exactOf(exactSearch(t, x, x.Owner, "REQ-"+itoa(x.N)+" exactfind")); len(got) != 0 {
		t.Errorf("a number with a word pins %v", got)
	}
}

func TestSearchByNumberShowsOnlyWhatTheViewerMayRead(t *testing.T) {
	x := seedExact(t)
	hidden := []string{"REQ-" + itoa(x.HiddenReq), "#" + itoa(x.HiddenReq), "IDEA-" + itoa(x.HiddenIdea), "pr:99", "#99"}
	// The owner reads the hidden project.
	for _, q := range hidden {
		if len(exactOf(exactSearch(t, x, x.Owner, q))) == 0 {
			t.Errorf("owner: %q finds nothing", q)
		}
	}
	// A member and a guest have no role there: for them it is as unknown as a number
	// that does not exist, and the page says the same as for that number.
	for name, c := range map[string]*http.Client{"member": x.Member, "guest": x.Guest} {
		for _, q := range hidden {
			// The ideas of the project of the viewer have other numbers than the hidden one; only
			// a number that a readable object also has may show.
			page := exactSearch(t, x, c, q)
			mustNotContain(t, name+" "+q, page, "SECRET")
			for _, h := range exactOf(page) {
				if h == x.HiddenReqHref || h == x.HiddenPullHref || strings.Contains(h, "/hidden/") {
					t.Errorf("%s: %q pins the object of the hidden project: %s", name, q, h)
				}
			}
		}
		page := exactSearch(t, x, c, "pr:99")
		if len(exactOf(page)) != 0 {
			t.Errorf("%s: pr:99 pins %v", name, exactOf(page))
		}
		if name == "member" {
			// The page of a hidden number says what the page of a missing one says.
			empty := regexp.MustCompile(`<div class="box-empty">.*?</div>`)
			_, missing := fetchPage(t, c, x.Base+"/ui/search?q="+url.QueryEscape("pr:987654"))
			got := strings.ReplaceAll(empty.FindString(page), "pr:99", "pr:N")
			want := strings.ReplaceAll(empty.FindString(missing), "pr:987654", "pr:N")
			if got == "" || got != want {
				t.Errorf("member: the page of a hidden number says %q, the page of a missing one %q", got, want)
			}
		}
	}
	// Narrowed to the hidden project, the owner finds no object of the other one.
	page := exactSearch(t, x, x.Owner, "REQ-"+itoa(x.N), "project="+url.QueryEscape(shellHiddenProject))
	if got := exactOf(page); len(got) != 0 {
		t.Errorf("the search of the hidden project pins an object of another project: %v", got)
	}
	// ... and to the project of the object it does.
	page = exactSearch(t, x, x.Owner, "REQ-"+itoa(x.N), "project="+url.QueryEscape(shellProject))
	if got := exactOf(page); !slices.Equal(got, []string{x.ReqHref}) {
		t.Errorf("the search of the project pins %v", got)
	}
}

// A guest has no tasks and pains in the search, so a number that names one is no hit.
func TestSearchByNumberGivesAGuestOnlyTheKindsHeSearches(t *testing.T) {
	x := seedExact(t)
	for _, q := range []string{"TASK-" + itoa(x.Task), "PAIN-" + itoa(x.Pain)} {
		if got := exactOf(exactSearch(t, x, x.Guest, q)); len(got) != 0 {
			t.Errorf("guest: %q pins %v", q, got)
		}
		if len(exactOf(exactSearch(t, x, x.Owner, q))) != 1 {
			t.Errorf("owner: %q does not pin its object", q)
		}
	}
	for q, want := range map[string]string{"REQ-" + itoa(x.N): x.ReqHref, "IDEA-" + itoa(x.Idea): x.IdeaHref, "pr:" + itoa(x.N): x.PullHref} {
		if got := exactOf(exactSearch(t, x, x.Guest, q)); !slices.Equal(got, []string{want}) {
			t.Errorf("guest: %q pins %v, want %s", q, got, want)
		}
	}
}

// What the number named is shown above the groups and not a second time in them,
// on the page and in the stream of the live page alike.
func TestSearchByNumberDoesNotShowTheSameObjectTwice(t *testing.T) {
	x := seedExact(t)
	probe := rqMake(t, x.St, rqSpec{Title: "probe"})
	// Two requests whose titles say REQ-<number of the first>: the full-text search
	// finds both by that word, and the first is also the exact hit.
	next := probe.Request.ID + 1
	word := "REQ-" + itoa(next)
	kernel := rqMake(t, x.St, rqSpec{Title: "kernelword " + word})
	follow := rqMake(t, x.St, rqSpec{Title: "kernelword follows " + word})
	if kernel.Request.ID != next {
		t.Fatalf("the next request has the number %d, not %d", kernel.Request.ID, next)
	}
	kh, fh := requestURL(kernel.Request.ID), requestURL(follow.Request.ID)
	for name, page := range map[string]string{"page": exactSearch(t, x, x.Owner, word), "frame+stream": ""} {
		if name != "page" {
			continue
		}
		hits := hitsOf(page)
		if !slices.Equal(hits, []string{kh, fh}) {
			t.Errorf("%s: hits %v, want the exact one first and the other once: %v", name, hits, []string{kh, fh})
		}
		mustContain(t, name, page, `data-total="1"`, "1 hit")
	}
	var body strings.Builder
	for _, ev := range readSearchStream(t, x.Owner, x.Base+"/ui/search/stream?q="+url.QueryEscape(word)) {
		body.WriteString(ev.Body)
	}
	if got := hitsOf(body.String()); !slices.Equal(got, []string{fh}) {
		t.Errorf("stream: group hits %v, want only %s", got, fh)
	}
}

func TestSearchLiveAndPageAndRefsReadTheNumberTheSameWay(t *testing.T) {
	x := seedExact(t)
	n := itoa(x.N)
	for _, q := range []string{"#" + n, "REQ-" + n, "pr:" + n, "PAIN-" + itoa(x.Pain), "#" + itoa(x.HiddenReq)} {
		for name, c := range map[string]*http.Client{"owner": x.Owner, "member": x.Member, "guest": x.Guest} {
			page := exactSearch(t, x, c, q)
			frame := exactSearch(t, x, c, q, "live=1")
			// The frame still opens the stream, whatever it pins above it.
			if !strings.Contains(frame, "data-search-live=") {
				t.Errorf("%s %q: the live frame with an exact hit has no stream", name, q)
			}
			if !slices.Equal(exactOf(frame), exactOf(page)) {
				t.Errorf("%s %q: the live frame pins %v, the page %v", name, q, exactOf(frame), exactOf(page))
			}
			// The stream never repeats a pinned object in its groups.
			var body strings.Builder
			for _, ev := range readSearchStream(t, c, x.Base+"/ui/search/stream?q="+url.QueryEscape(q)) {
				body.WriteString(ev.Body)
			}
			for _, h := range exactOf(page) {
				if strings.Contains(body.String(), `href="`+h+`"`) {
					t.Errorf("%s %q: the stream shows %s again in a group", name, q, h)
				}
			}
			if !slices.Equal(hitsOf(body.String()), hitsOf(strings.Join(groupBodies(page), ""))) {
				t.Errorf("%s %q: the stream and the page show other hits in the groups", name, q)
			}
		}
	}
	// /ui/refs reads the same terms: the owner finds the request and the pull request
	// by their short forms, the guest does not find what lies in the hidden project.
	refs := func(c *http.Client, project string, rs ...string) []links.Info {
		t.Helper()
		_, body := getRefs(t, x.shellEnv, c, url.Values{"ref": rs, "project": {project}})
		var out struct{ Refs []links.Info }
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		return out.Refs
	}
	got := refs(x.Owner, shellProject, "REQ-"+n, "pr:"+n, " PAIN-"+itoa(x.Pain)+" ", "task-"+itoa(x.Task))
	for i, g := range got {
		if !g.Gefunden {
			t.Errorf("refs: entry %d is not found: %+v", i, g)
		}
	}
	for _, g := range refs(x.Guest, shellProject, "REQ-"+itoa(x.HiddenReq), "pr:99", "TASK-"+itoa(x.Task)) {
		if g.Gefunden {
			t.Errorf("refs: the guest finds %+v", g)
		}
	}
}

// groupBodies are the groups of a page, one string each.
func groupBodies(page string) []string {
	var out []string
	for _, part := range strings.Split(page, `<div class="hg" data-group="`)[1:] {
		out = append(out, part)
	}
	return out
}

func TestListFiltersReadTheNumberLikeTheSearch(t *testing.T) {
	// The qualifier pr: of the issue list.
	for q, want := range map[string]int64{"pr:12": 12, "pr:#12": 12, "PR:12": 12, "pr: 12": 0} {
		iq := parseIssueQuery(q)
		if iq.PR != want {
			t.Errorf("issue list %q: PR = %d, want %d", q, iq.PR, want)
		}
	}
	if iq := parseIssueQuery("pr:abc pr:0"); iq.PR != 0 || len(iq.Unsupported) != 2 {
		t.Errorf("a pr: that is no number is unsupported: %+v", iq)
	}
	// The word of the pull request list: #12, pr:12 and the plain number are the number of the pull request.
	pq := parsePullQuery("pr:12")
	if len(pq.Unsupported) != 0 || pq.Text != "pr:12" {
		t.Errorf("pull list pr:12: %+v", pq)
	}
	for _, w := range []string{"#12", "pr:12", "PR:#12", "pull:12", "12"} {
		if !pullNumberWord(w, 12) || pullNumberWord(w, 13) {
			t.Errorf("pull list %q is not exactly the number 12", w)
		}
	}
	for _, w := range []string{"REQ-12", "task-12", "#12a", "x12"} {
		if pullNumberWord(w, 12) {
			t.Errorf("pull list %q is the number of a pull request", w)
		}
	}
	// On the page: the list finds the pull request by each form.
	x := seedExact(t)
	for _, q := range []string{"#12", "pr:12", "PR:12"} {
		_, page := fetchPage(t, x.Owner, x.Base+"/ui/x/shell/pulls?q="+url.QueryEscape("is:any "+q))
		mustContain(t, "pull list "+q, page, `href="/ui/x/shell/pulls/12"`)
		mustNotContain(t, "pull list "+q, page, `href="/ui/x/shell/pulls/13"`, "Unsupported")
	}
}
