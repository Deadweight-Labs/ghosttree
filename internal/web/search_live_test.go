package web

import (
	"bufio"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedSearchAll is seedSessions with one entry of every other kind that says
// zebrafish, in the project and in the project the others do not see.
func seedSearchAll(t *testing.T) sessEnv {
	t.Helper()
	e := seedSessions(t)
	owner := e.St.Access(store.Principal{ID: "person:1", Label: "alice"})
	for _, p := range []string{shellProject, shellHiddenProject} {
		tag := "open"
		if p == shellHiddenProject {
			tag = "SECRET"
		}
		req := rqMake(t, e.St, rqSpec{Title: "zebrafish request " + tag, Project: p})
		knInsert(t, e.St, store.Knowledge{Title: "zebrafish note " + tag, Body: "about the zebrafish", Confidence: "trusted", Scope: scope.Axes{Project: p}})
		if _, err := owner.CreateIdea(store.CreateIdeaInput{Project: p, Title: "zebrafish idea " + tag, Body: "zebrafish", Actor: idea.Actor{AccountID: 1, Person: "alice"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.CreatePain(store.CreatePainInput{Project: p, Title: "zebrafish pain " + tag, Body: "zebrafish", Actor: pain.Actor{AccountID: 1, Person: "alice"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.CreateHumanTask(humantask.CreateInput{RequestID: req.Request.ID, Project: p, Kind: "action", Title: "zebrafish task " + tag, Everyone: true},
			humantask.Actor{AccountID: 1, Person: "alice"}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.St.CreateDocument(store.Document{Project: p, Slug: "zebrafish-" + strings.ToLower(tag), Kind: "report", Title: "zebrafish document " + tag, Person: "alice"},
			"about the zebrafish\n", "m"); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

type streamEvent struct {
	Name string
	searchEvent
}

// readSearchStream reads the stream to its end.
func readSearchStream(t *testing.T, c *http.Client, u string) []streamEvent {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%s: status %d, %s", u, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var out []streamEvent
	var name string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev := streamEvent{Name: name}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.searchEvent); err != nil {
				t.Fatalf("%s: %v", line, err)
			}
			out = append(out, ev)
		}
	}
	if len(out) == 0 || out[len(out)-1].Name != "done" {
		t.Fatalf("%s: the stream did not end with done: %+v", u, out)
	}
	return out
}

var (
	hitHrefRE  = regexp.MustCompile(`<a class="ht rl" href="([^"]*)"`)
	kindRowRE  = regexp.MustCompile(`<a class="frow[^"]*" href="[^"]*">.*?<span class="nm">([^<]*)</span><span class="counter">([^<]*)</span></a>`)
	groupHeadR = regexp.MustCompile(`<div class="box-h"><h2 class="t">([^<]*)</h2>(?:<span class="sp"></span><a href="([^"]*)">)?`)
)

// pageParts is what a search shows: the rows of the kinds with their counters,
// the headings of the groups with their links, and the links of the hits.
func pageParts(page string) (rows, heads, hits []string) {
	for _, m := range kindRowRE.FindAllStringSubmatch(page, -1) {
		rows = append(rows, m[1]+"="+m[2])
	}
	for _, m := range groupHeadR.FindAllStringSubmatch(page, -1) {
		heads = append(heads, m[1]+" "+html.UnescapeString(m[2]))
	}
	for _, m := range hitHrefRE.FindAllStringSubmatch(page, -1) {
		hits = append(hits, html.UnescapeString(m[1]))
	}
	return rows, heads, hits
}

// The stream shows every viewer exactly what the page searched before it is
// sent shows him: the same kinds, counters, groups and hits, in the same order,
// and nothing of what he may not read.
func TestSearchStreamShowsEachViewerWhatThePageShows(t *testing.T) {
	e := seedSearchAll(t)
	viewers := []struct {
		name   string
		client *http.Client
	}{{"owner", e.Owner}, {"member", e.Member}, {"guest", e.Guest}, {"lead", e.Lead}, {"reviewer", e.Reviewer}}
	for _, q := range []string{"zebrafish", "zebrafish is:open", "zebrafish repo:" + strings.TrimPrefix(shellProject, "github.com/")} {
		for _, v := range viewers {
			code, page := e.get(t, v.client, "/ui/search?q="+url.QueryEscape(q))
			if code != http.StatusOK {
				t.Fatalf("%s %q: status %d", v.name, q, code)
			}
			wantRows, wantHeads, wantHits := pageParts(page)
			// "All" is not a kind: the page counts it, the script of the live page
			// adds it up from the events (they carry Total).
			if len(wantRows) == 0 || !strings.HasPrefix(wantRows[0], "All=") {
				t.Fatalf("%s %q: the first row of the page is not All: %v", v.name, q, wantRows)
			}
			wantAll := strings.TrimPrefix(wantRows[0], "All=")
			wantRows = wantRows[1:]
			events := readSearchStream(t, v.client, e.Base+"/ui/search/stream?q="+url.QueryEscape(q))
			byKey := map[string]streamEvent{}
			var keys []string
			for _, ev := range events {
				if ev.Name != "group" {
					continue
				}
				if ev.Slow {
					t.Fatalf("%s %q: %s ran out of time", v.name, q, ev.Key)
				}
				byKey[ev.Key] = ev
				keys = append(keys, ev.Key)
			}
			wantKeys := []string{"requests", "ideas", "knowledge", "docs", "sessions", "tasks", "pains"}
			if v.name == "guest" {
				wantKeys = wantKeys[:4]
			}
			slices.Sort(keys)
			sorted := slices.Clone(wantKeys)
			slices.Sort(sorted)
			if !slices.Equal(keys, sorted) {
				t.Fatalf("%s %q: the stream searched %v, want %v", v.name, q, keys, sorted)
			}
			var side, body strings.Builder
			for _, k := range wantKeys {
				side.WriteString(byKey[k].Side)
				body.WriteString(byKey[k].Body)
			}
			gotRows, _, _ := pageParts(side.String())
			_, gotHeads, gotHits := pageParts(body.String())
			if !slices.Equal(gotRows, wantRows) || !slices.Equal(gotHeads, wantHeads) || !slices.Equal(gotHits, wantHits) {
				t.Errorf("%s %q:\nstream %v\n       %v\n       %v\npage   %v\n       %v\n       %v", v.name, q, gotRows, gotHeads, gotHits, wantRows, wantHeads, wantHits)
			}
			sum := 0
			for _, k := range wantKeys {
				sum += byKey[k].Total
			}
			if wantAll != strconv.Itoa(sum) {
				t.Errorf("%s %q: the page counts %s hits in all, the events add up to %d", v.name, q, wantAll, sum)
			}
			if q == "zebrafish" && len(gotRows) != len(wantKeys) {
				t.Errorf("%s: the fixture does not reach every kind: %v", v.name, gotRows)
			}
			all := side.String() + body.String()
			if v.name != "owner" && strings.Contains(all, "SECRET") {
				t.Errorf("%s %q: the stream shows an entry of the hidden project", v.name, q)
			}
			for _, name := range []string{"anna-private", "alice-private", "hidden-project", "anna-project"} {
				if !strings.Contains(all, e.pid[name]) {
					continue
				}
				if !strings.Contains(page, e.pid[name]) {
					t.Errorf("%s %q: the stream links session %s, the page does not", v.name, q, name)
				}
			}
			// A private session is its owner's alone: alice is the owner, anna the member.
			for name, own := range map[string]string{"alice-private": "owner", "anna-private": "member"} {
				if v.name != own && strings.Contains(all, e.pid[name]) {
					t.Errorf("%s %q: the stream links the private session %s of another account", v.name, q, name)
				}
			}
		}
	}
}

// A kind that does not finish within its budget is given up and said to be slow,
// without hits; asked for alone it gets the long budget and its group.
func TestSearchStreamGivesUpAKindAfterItsBudget(t *testing.T) {
	e := seedSearchAll(t)
	old := searchKindBudget
	searchKindBudget = 0
	t.Cleanup(func() { searchKindBudget = old })
	events := readSearchStream(t, e.Owner, e.Base+"/ui/search/stream?q=zebrafish")
	n := 0
	for _, ev := range events {
		if ev.Name != "group" {
			continue
		}
		n++
		if !ev.Slow || ev.Side != "" || ev.Body != "" {
			t.Errorf("%s: %+v, want given up without hits", ev.Key, ev.searchEvent)
		}
	}
	if n != 7 {
		t.Fatalf("%d kinds said, want 7", n)
	}
	events = readSearchStream(t, e.Owner, e.Base+"/ui/search/stream?q=zebrafish&only=sessions")
	if len(events) != 2 || events[0].Key != "sessions" || events[0].Slow || !strings.Contains(events[0].Body, `class="ht rl"`) {
		t.Fatalf("only sessions: %+v", events)
	}
	_, page := e.get(t, e.Owner, "/ui/search?q=zebrafish&kind=sessions")
	_, want, wantHits := pageParts(page)
	_, got, gotHits := pageParts(events[0].Body)
	if !slices.Equal(got, want) || !slices.Equal(gotHits, wantHits) {
		t.Errorf("only sessions: %v %v, page %v %v", got, gotHits, want, wantHits)
	}
}

// Only the kinds of the viewer are searched, also when one is asked for alone,
// and a line that does not run searches nothing.
func TestSearchStreamSearchesOnlyWhatTheViewerGets(t *testing.T) {
	e := seedSearchAll(t)
	for _, u := range []string{
		"/ui/search/stream?q=zebrafish&only=sessions",
		"/ui/search/stream?q=zebrafish&only=tasks",
		"/ui/search/stream?q=zebrafish&only=nothing",
	} {
		if events := readSearchStream(t, e.Guest, e.Base+u); len(events) != 1 {
			t.Errorf("guest %s: %+v", u, events)
		}
	}
	for _, u := range []string{"/ui/search/stream?q=z", "/ui/search/stream?q=zebrafish+bogus:1", "/ui/search/stream?q=zebrafish+branch:feat/x"} {
		if events := readSearchStream(t, e.Guest, e.Base+u); len(events) != 1 {
			t.Errorf("guest %s: %+v", u, events)
		}
	}
	resp, err := http.Get(e.Base + "/ui/search/stream?q=zebrafish")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Error("the stream answers without a browser session")
	}
}

// The live page is the frame: progress, an empty place per kind of the viewer,
// the way back to the page that searches first, and no hit.
func TestSearchLivePageIsTheFrame(t *testing.T) {
	e := seedSearchAll(t)
	start := time.Now()
	_, page := e.get(t, e.Owner, "/ui/search?q=zebrafish&live=1")
	if !strings.Contains(page, `data-search-live="/ui/search/stream?q=zebrafish"`) || strings.Contains(page, `class="ht rl"`) {
		t.Fatalf("not the frame:\n%s", page)
	}
	if !strings.Contains(page, `<meta http-equiv="refresh" content="0;url=/ui/search?q=zebrafish">`) {
		t.Errorf("no way back without the page script:\n%s", page)
	}
	for _, k := range []string{"requests", "ideas", "knowledge", "docs", "sessions", "tasks", "pains"} {
		if !strings.Contains(page, `data-kind-side="`+k+`"`) || !strings.Contains(page, `data-kind-body="`+k+`"`) {
			t.Errorf("no place for %s", k)
		}
	}
	if !strings.Contains(page, `/static/ui-search.js`) {
		t.Error("the page script is not loaded")
	}
	_, guest := e.get(t, e.Guest, "/ui/search?q=zebrafish&live=1")
	if strings.Contains(guest, `data-kind-side="sessions"`) || !strings.Contains(guest, `data-total="4"`) {
		t.Errorf("the guest's frame has a place for sessions or the wrong count")
	}
	_, narrowed := e.get(t, e.Owner, "/ui/search?q=zebrafish&live=1&kind=pains")
	if strings.Contains(narrowed, `data-kind-body="sessions"`) || !strings.Contains(narrowed, `data-kind-body="pains"`) {
		t.Errorf("the frame narrowed to pains keeps the place of another kind")
	}
	if _, bad := e.get(t, e.Owner, "/ui/search?q=zebrafish+bogus:1&live=1"); strings.Contains(bad, "data-search-live") {
		t.Error("a line that does not run opens a stream")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("the frames took %v", time.Since(start))
	}
}

// A quick filter with a line that does not run shows no group at all, not the
// hits of the lines before it, on the page, the frame and the stream alike.
func TestSearchPresetWithABrokenLineShowsNothing(t *testing.T) {
	e := seedSearchAll(t)
	pa := e.St.Access(store.Principal{ID: "person:1", Label: "alice"})
	if _, err := pa.SavePreset(shellProject, "user", "mixed", "", []string{"zebrafish", "zebrafish topic:nosuchtopic"}); err != nil {
		t.Fatal(err)
	}
	base := "/ui/search?preset=mixed&project=" + url.QueryEscape(shellProject)
	_, page := e.get(t, e.Owner, base)
	if strings.Contains(page, `class="ht rl"`) || !strings.Contains(page, "nosuchtopic") {
		t.Errorf("the page shows hits or no notice:\n%s", page)
	}
	if _, frame := e.get(t, e.Owner, base+"&live=1"); strings.Contains(frame, "data-search-live") {
		t.Error("the frame opens a stream")
	}
	if events := readSearchStream(t, e.Owner, e.Base+strings.Replace(base, "/ui/search?", "/ui/search/stream?", 1)); len(events) != 1 {
		t.Errorf("the stream searched: %+v", events)
	}
}

// The live page narrowed to a kind marks that kind in the stream as the page does.
func TestSearchStreamMarksTheNarrowedKind(t *testing.T) {
	e := seedSearchAll(t)
	_, frame := e.get(t, e.Owner, "/ui/search?q=zebrafish&live=1&kind=pains")
	if !strings.Contains(frame, `data-search-live="/ui/search/stream?kind=pains&amp;q=zebrafish"`) {
		t.Fatalf("the stream of the frame forgets the kind:\n%s", frame)
	}
	for _, ev := range readSearchStream(t, e.Owner, e.Base+"/ui/search/stream?kind=pains&q=zebrafish") {
		if ev.Name != "group" || ev.Side == "" {
			continue
		}
		if on := strings.Contains(ev.Side, `class="frow on"`); on != (ev.Key == "pains") {
			t.Errorf("%s marked %v", ev.Key, on)
		}
	}
	// A kind the viewer does not get marks nothing and is not passed on.
	if _, frame := e.get(t, e.Guest, "/ui/search?q=zebrafish&live=1&kind=sessions"); strings.Contains(frame, "kind=sessions") {
		t.Error("the guest's frame passes on a kind he does not get")
	}
}
