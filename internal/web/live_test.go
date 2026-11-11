package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// liveFixture is an issue with a session that works on it: the session belongs
// to alice and is shared with the project.
type liveFixture struct {
	e     shellEnv
	id    int64
	work  int64
	issue string
	ext   string
}

func newLiveFixture(t *testing.T) liveFixture {
	t.Helper()
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Live subject", Criteria: []string{"one"}})
	sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "ext-live", AccountID: 1, Scope: scope.Axes{Project: shellProject, Machine: "mainex"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetSessionVisibility(sid, e.St.Access(store.Principal{ID: "person:1", Label: "alice"}), store.VisProject); err != nil {
		t.Fatal(err)
	}
	w, _, err := e.St.StartRequestWork(d.Request.ID, sid, "primary", "alice")
	if err != nil {
		t.Fatal(err)
	}
	return liveFixture{e: e, id: d.Request.ID, work: w.ID, issue: "/ui/x/shell/issues/" + itoa(d.Request.ID), ext: "ext-live"}
}

func (f liveFixture) touch(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := f.e.St.RecordPathActivity([]store.PathActivity{{Project: shellProject, SessionExternalID: f.ext, Tool: "Edit", Path: path, Writes: true, Quality: store.ActivityObserved, At: at.UTC().Format(time.RFC3339), AccountID: 1}}); err != nil {
		t.Fatal(err)
	}
}

func atNow(t *testing.T, at time.Time) {
	t.Helper()
	prev := overviewNow
	overviewNow = func() time.Time { return at }
	t.Cleanup(func() { overviewNow = prev })
}

func liveBoxes(page string) int { return strings.Count(page, `<div class="live-box"`) }

// An agent with active work on an issue and a fresh sign of life is shown at
// work: in the head and the sidebar of the issue, as a pulse in the list.
func TestLiveWorkIsShownOnTheIssueAndInTheList(t *testing.T) {
	f := newLiveFixture(t)
	f.touch(t, "internal/web/live.go", time.Now().Add(-20*time.Second))
	_, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue)
	if n := liveBoxes(page); n != 2 {
		t.Fatalf("the issue shows %d live boxes (head and sidebar), want 2", n)
	}
	for _, want := range []string{"claude@mainex", "is working on this right now", "internal/web/live.go", `href="/ui/sessions/`, `class="live"`} {
		if !strings.Contains(page, want) {
			t.Errorf("issue page lacks %q", want)
		}
	}
	if !strings.Contains(page, `data-live-ids="`+itoa(f.id)+`"`) {
		t.Error("the page does not name its issue for the live stream")
	}
	_, list := fetchPage(t, f.e.Owner, f.e.Base+"/ui/x/shell/issues")
	row := list[strings.Index(list, `data-live-row="`+itoa(f.id)+`"`):]
	row = row[:strings.Index(row, "</span></div></div>")]
	if !strings.Contains(row, `class="live"`) || !strings.Contains(row, "claude@mainex is working on this right now") {
		t.Errorf("the list row has no live agent: %s", row)
	}
}

// Without active work there is nothing to show, however fresh the file touches
// are: finishing the work takes the note away.
func TestLiveWorkIsGoneWhenTheWorkEnds(t *testing.T) {
	f := newLiveFixture(t)
	f.touch(t, "internal/web/live.go", time.Now())
	if _, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue); liveBoxes(page) != 2 {
		t.Fatal("the fixture is not live")
	}
	if _, err := f.e.St.FinishRequestWork(f.work, "paused", "later", "alice"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue)
	if liveBoxes(page) != 0 || strings.Contains(page, "is working on this right now") {
		t.Error("an issue without active work still says that somebody works on it")
	}
	if !strings.Contains(page, `data-live-head="`+itoa(f.id)+`"`) {
		t.Error("the container for the stream is gone, so a later start could not appear")
	}
	_, list := fetchPage(t, f.e.Owner, f.e.Base+"/ui/x/shell/issues")
	if strings.Contains(list, `class="live"`) {
		t.Error("the list still shows the agent as live")
	}
}

// Active work whose last sign of life is older than the window is listed in the
// sidebar with a quiet dot, but nobody is said to work there right now.
func TestLiveWorkGoesStaleWhenTheLastSignIsOld(t *testing.T) {
	f := newLiveFixture(t)
	f.touch(t, "internal/web/live.go", time.Now())
	atNow(t, time.Now().Add(store.ActivityFreshTTL-30*time.Second))
	if _, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue); liveBoxes(page) != 2 {
		t.Error("within the window the agent is not shown as live")
	}
	atNow(t, time.Now().Add(store.ActivityFreshTTL+time.Minute+time.Hour))
	_, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue)
	if liveBoxes(page) != 0 || strings.Contains(page, `class="live"`) {
		t.Error("an agent whose last sign is old is still shown as working")
	}
	if !strings.Contains(page, "claude@mainex") || !strings.Contains(page, `class="dot push idle"`) {
		t.Error("the stale session vanished from the sidebar or is not marked quiet")
	}
}

// What a viewer may not read is not told by the stream either.
func TestLiveWorkIsNotToldToThoseWhoMayNotReadTheIssue(t *testing.T) {
	f := newLiveFixture(t)
	f.touch(t, "internal/web/live.go", time.Now())
	hidden := rqMake(t, f.e.St, rqSpec{Title: "SECRET-LIVE", Project: shellHiddenProject})
	snapshot := func(c *http.Client, ids string) map[string]liveSnapshot {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.e.Base+"/ui/live?ids="+ids, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("stream: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if rest, ok := strings.CutPrefix(line, "event: "); ok {
				event = rest
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok && event == "live" {
				out := map[string]liveSnapshot{}
				if err := json.Unmarshal([]byte(data), &out); err != nil {
					t.Fatalf("snapshot: %v", err)
				}
				return out
			}
		}
		t.Fatal("the stream sent no snapshot")
		return nil
	}
	ids := itoa(f.id) + "," + itoa(hidden.Request.ID)
	owner := snapshot(f.e.Owner, ids)
	if got := owner[itoa(f.id)]; !strings.Contains(got.Box, "claude@mainex") || !strings.Contains(got.Dot, `class="gav agent a-claude`) {
		t.Errorf("the owner's snapshot lacks the agent: %+v", got)
	}
	member := snapshot(f.e.Member, ids)
	if _, ok := member[itoa(hidden.Request.ID)]; ok {
		t.Error("the stream names an issue the member may not read")
	}
	if strings.Contains(string(mustJSON(t, member)), "SECRET") {
		t.Error("the snapshot leaks a hidden title")
	}
	// A guest, for whom the session is not shared, does not learn who works there.
	guest := snapshot(f.e.Guest, itoa(f.id))
	if strings.Contains(guest[itoa(f.id)].Box+guest[itoa(f.id)].Dot, "claude@mainex") {
		t.Error("an unreadable session is shown to a guest")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A stream keeps no old rights: whoever is removed from the project while the
// stream is open gets an empty box in the next round, not the agent any more.
func TestLiveStreamDropsRightsOfAMemberRemovedFromTheProject(t *testing.T) {
	old := liveStreamEvery
	liveStreamEvery = 50 * time.Millisecond
	t.Cleanup(func() { liveStreamEvery = old })
	f := newLiveFixture(t)
	f.touch(t, "internal/web/live.go", time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.e.Base+"/ui/live?ids="+itoa(f.id), nil)
	resp, err := f.e.Member.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	boxes := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				snap := map[string]liveSnapshot{}
				if json.Unmarshal([]byte(data), &snap) == nil {
					boxes <- snap[itoa(f.id)].Box
				}
			}
		}
		close(boxes)
	}()
	next := func() (string, bool) {
		select {
		case b, ok := <-boxes:
			return b, ok
		case <-time.After(3 * time.Second):
			return "", false
		}
	}
	if first, ok := next(); !ok || !strings.Contains(first, "claude@mainex") {
		t.Fatalf("the member does not see the agent at first: %q", first)
	}
	p, _ := f.e.St.ProjectByRemote(shellProject)
	if err := f.e.St.RemoveProjectRole("person:1", shellProject, "person:2", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if err := f.e.St.RemoveOrgMember("person:1", p.OrgID, "person:2"); err != nil {
		t.Fatal(err)
	}
	second, ok := next()
	if !ok || strings.Contains(second, "claude@mainex") {
		t.Errorf("after the removal the stream still tells who works there: %q (open %v)", second, ok)
	}
}

// Für Gäste geteilt: der Gast sieht, dass an dem Issue gearbeitet wird, aber nicht
// wer (Besitzer der Session), mit welcher Kennung, auf welcher Maschine oder an
// welcher Datei. Den Autor des Issues sieht er wie jeder Leser.
func TestGuestSeesASharedSessionOnTheIssueWithoutOwnerIdOrPath(t *testing.T) {
	f := newLiveFixture(t)
	if err := f.e.St.SetSessionVisibility(1, f.e.St.Access(store.Principal{ID: "person:1", Label: "alice"}), store.VisGuests); err != nil {
		t.Fatal(err)
	}
	f.touch(t, "internal/secret/plan.go", time.Now())
	_, page := fetchPage(t, f.e.Guest, f.e.Base+f.issue)
	for _, s := range []string{"ext-live", "mainex", "internal/secret/plan.go"} {
		if strings.Contains(page, s) {
			t.Errorf("the guest's issue page shows %q", s)
		}
	}
	// Gegenprobe: der Besitzer sieht die Maschine und die Datei.
	if _, own := fetchPage(t, f.e.Owner, f.e.Base+f.issue); !strings.Contains(own, "mainex") {
		t.Error("the owner's issue page lacks the machine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.e.Base+"/ui/live?ids="+itoa(f.id), nil)
	resp, err := f.e.Guest.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		for _, leak := range []string{"ext-live", "mainex", "internal/secret/plan.go", "alice"} {
			if strings.Contains(data, leak) {
				t.Errorf("the guest's live stream shows %q: %s", leak, data)
			}
		}
		break
	}
}
