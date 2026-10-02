package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestThinkingIsOffUntilAsked(t *testing.T) {
	e := seedSessions(t)
	base := "/ui/sessions/" + e.pid["anna-project"]
	_, page := e.get(t, e.Owner, base)
	if strings.Contains(page, `<details class="think"`) {
		t.Error("thinking blocks are shown by default")
	}
	if !strings.Contains(page, "thinking=1") {
		t.Error("no link to turn thinking on")
	}
	_, page = e.get(t, e.Owner, base+"?thinking=1")
	if !strings.Contains(page, `<details class="think"`) {
		t.Error("thinking=1 does not show thinking")
	}
	// A search the viewer scoped to thinking finds thinking, even while the blocks are hidden.
	_, page = e.get(t, e.Owner, base+"?q=suspicious&kind=thinking")
	if strings.Contains(page, "No matches") || !regexp.MustCompile(`find-pos|find-count`).MatchString(page) {
		t.Error("kind=thinking is ignored by the in-session find")
	}
	// The default find does not look into thinking.
	_, page = e.get(t, e.Owner, base+"?q=suspicious")
	if !strings.Contains(page, "No matches") {
		t.Error("the default find searches thinking")
	}
}

func TestGuestsSeeNoCounts(t *testing.T) {
	e := seedSessions(t)
	for _, u := range []string{"/ui/sessions", "/ui/sessions?q=zebrafish"} {
		_, page := e.get(t, e.Guest, u)
		if m := regexp.MustCompile(`<option[^>]*>[^<]*\(\d+\)</option>`).FindString(page); m != "" {
			t.Errorf("guest %s has a facet count: %s", u, m)
		}
		if regexp.MustCompile(`\d+ matches?|in \d+ sessions?`).MatchString(page) {
			t.Errorf("guest %s has a match count", u)
		}
	}
	_, page := e.get(t, e.Member, "/ui/sessions?q=zebrafish")
	if !regexp.MustCompile(`\d+ matches?`).MatchString(page) {
		t.Error("member lost the match count")
	}
	_, page = e.get(t, e.Member, "/ui/sessions")
	if !regexp.MustCompile(`<option[^>]*>[^<]*\(\d+\)</option>`).MatchString(page) {
		t.Error("member lost the facet counts")
	}
}

func bigTranscript(blocks, lines int) []store.Chunk {
	var out []store.Chunk
	for i := 0; i < blocks; i++ {
		id := fmt.Sprintf("big%d", i)
		out = append(out,
			store.Chunk{Seq: 2 * i, Raw: aLine(bash(id, "ls -la"))},
			store.Chunk{Seq: 2*i + 1, Raw: rLine(id, strings.Repeat("a line of tool output\n", lines), false)})
	}
	return out
}

func TestRenderingIsBoundedPerBlockAndPerPage(t *testing.T) {
	e := seedSessions(t)
	id, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "huge", AccountID: 1, Scope: scope.Axes{Project: shellProject}})
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("word ", 200000) // 1 MB of prose in one message
	chunks := append([]store.Chunk{{Seq: 0, Raw: uLine(huge)}}, bigTranscript(0, 0)...)
	if err := e.St.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	sess, _ := e.St.SessionByID(id)
	_, page := e.get(t, e.Owner, "/ui/sessions/"+sess.PublicID)
	if len(page) > 600<<10 {
		t.Errorf("a 1 MB message renders %d bytes", len(page))
	}
	if !strings.Contains(page, "truncated") {
		t.Error("cut text is not marked")
	}

	long, _ := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "long-out", AccountID: 1, Scope: scope.Axes{Project: shellProject}})
	if err := e.St.AppendChunks(long, bigTranscript(1, 50000)); err != nil {
		t.Fatal(err)
	}
	ls, _ := e.St.SessionByID(long)
	_, page = e.get(t, e.Owner, "/ui/sessions/"+ls.PublicID)
	if n := strings.Count(page, `class="ol"`); n > 2010 {
		t.Errorf("one output block renders %d lines", n)
	}
	if !strings.Contains(page, "truncated") {
		t.Error("cut output is not marked")
	}

	many, _ := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "many-out", AccountID: 1, Scope: scope.Axes{Project: shellProject}})
	if err := e.St.AppendChunks(many, bigTranscript(90, 1500)); err != nil {
		t.Fatal(err)
	}
	ms, _ := e.St.SessionByID(many)
	_, page = e.get(t, e.Owner, "/ui/sessions/"+ms.PublicID+"?open=1&from=0")
	if n := strings.Count(page, `class="ol"`); n > maxRenderedRows {
		t.Errorf("a page renders %d rows, limit %d", n, maxRenderedRows)
	}
	later := regexp.MustCompile(`class="wlink" href="([^"]*from=\d+[^"]*)">Show \d+ later`).FindStringSubmatch(page)
	if later == nil {
		t.Fatal("a page cut at the row limit offers no way to continue")
	}
	_, next := e.get(t, e.Owner, strings.ReplaceAll(later[1], "&amp;", "&"))
	if !strings.Contains(next, `class="blk`) && !strings.Contains(next, `<details class="tool`) {
		t.Error("the continuation shows nothing")
	}
}

func TestSharingWithGuestsAsksAboutSecrets(t *testing.T) {
	e := seedSessions(t)
	id, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "leaky", AccountID: 2, Scope: scope.Axes{Project: shellProject, Machine: "laptop"}})
	if err != nil {
		t.Fatal(err)
	}
	tok := "ghp_" + strings.Repeat("a1B2", 9)
	if err := e.St.AppendChunks(id, chunks(uLine("here is a key "+tok), uLine("and again "+tok), aLine(tb("fine"), bash("x", "echo "+tok)))); err != nil {
		t.Fatal(err)
	}
	sess, _ := e.St.SessionByID(id)
	post := func(form url.Values) (*http.Response, string) {
		token := renderedCSRFToken(t, e.Member, e.Base+"/ui/sessions")
		form.Set("csrf_token", token)
		resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/sessions/"+sess.PublicID+"/share", form)
		return resp, body(t, resp)
	}
	// Members need no question.
	if resp, _ := post(url.Values{"level": {store.VisProject}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("project level: %d", resp.StatusCode)
	}
	resp, _ := post(url.Values{"level": {store.VisGuests}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("no redirect to the confirmation: %d", resp.StatusCode)
	}
	_, page := e.get(t, e.Member, resp.Header.Get("Location"))
	if !strings.Contains(page, "3 possible secrets in this session. Share anyway?") {
		t.Fatalf("no confirmation: %.300s", page)
	}
	if got, _ := e.St.SessionByID(id); got.Visibility != store.VisProject {
		t.Errorf("level changed before the confirmation: %q", got.Visibility)
	}
	if strings.Contains(page, tok) {
		t.Error("the confirmation shows the secret")
	}
	if resp, _ := post(url.Values{"level": {store.VisGuests}, "confirm": {"1"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirmed: %d", resp.StatusCode)
	}
	if got, _ := e.St.SessionByID(id); got.Visibility != store.VisGuests {
		t.Errorf("level after confirmation = %q", got.Visibility)
	}
	// A clean session goes straight through.
	clean := e.pid["anna-private"]
	token := renderedCSRFToken(t, e.Member, e.Base+"/ui/sessions")
	r := sameOriginPostForm(t, e.Member, e.Base+"/ui/sessions/"+clean+"/share", url.Values{"csrf_token": {token}, "level": {store.VisGuests}})
	if r.StatusCode != http.StatusSeeOther {
		t.Errorf("clean session asked a question: %d", r.StatusCode)
	}
}

func TestReviewAndRequestLinkToSessionsByPublicAddress(t *testing.T) {
	e := seedSessions(t)
	k, err := e.St.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "evidence pitfall", Body: "b", Scope: scope.Axes{Project: shellProject}, Origin: "distilled", Confidence: "quarantined"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.AddEvidence(k, []store.Evidence{
		{SessionID: e.id["anna-project"], ChunkSeq: 4, Quote: "q1"},
		{SessionID: e.id["alice-private"], ChunkSeq: 2, Quote: "q2"},
	}); err != nil {
		t.Fatal(err)
	}
	_, page := e.get(t, e.Owner, "/ui/review")
	if !strings.Contains(page, `href="/ui/sessions/`+e.pid["anna-project"]+`#c4"`) {
		t.Errorf("review evidence does not link by public address: %s", regexp.MustCompile(`Session[^\n]{0,200}`).FindString(page))
	}
	if regexp.MustCompile(`/ui/sessions/\d+["#?]`).MatchString(page) {
		t.Error("review links a numeric session address")
	}
	// A reader who may not open the session gets no link to it.
	_, page = e.get(t, e.Reviewer, "/ui/review")
	if strings.Contains(page, e.pid["alice-private"]) || strings.Contains(page, `href="/ui/sessions/`+e.pid["anna-private"]) {
		t.Error("review links a session the viewer cannot read")
	}

	d, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "linked work", Scope: scope.Axes{Project: shellProject}, Person: "anna"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(d.Request.ID, e.id["anna-project"], "primary", "anna"); err != nil {
		t.Skipf("cannot start work: %v", err)
	}
	_, page = e.get(t, e.Owner, fmt.Sprintf("/ui/requests/%d", d.Request.ID))
	if !strings.Contains(page, `href="/ui/sessions/`+e.pid["anna-project"]+`"`) || regexp.MustCompile(`/ui/sessions/\d+["#?]`).MatchString(page) {
		t.Errorf("request page links sessions by number: %s", regexp.MustCompile(`Work sessions.{0,300}`).FindString(page))
	}
}
