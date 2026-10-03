package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func knInsert(t *testing.T, st *store.Store, k store.Knowledge) int64 {
	t.Helper()
	if k.Scope.Project == "" {
		k.Scope.Project = shellProject
	}
	if k.Person == "" {
		k.Person = "alice"
	}
	if k.Type == "" {
		k.Type = "note"
	}
	if k.Body == "" {
		k.Body = "body"
	}
	id, err := st.InsertKnowledge(k)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func knPost(t *testing.T, c *http.Client, base, page, action string, form url.Values) *http.Response {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if form.Get("csrf_token") == "" {
		form.Set("csrf_token", renderedCSRFToken(t, c, base+page))
	}
	return sameOriginPostForm(t, c, base+action, form)
}

func TestKnowledgeListShowsKindStatusExcerptAndFilters(t *testing.T) {
	e := ovEnv(t)
	knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: "WAL grows unbounded", Body: "First paragraph about the WAL.\n\n```go\nrows.Close()\n```\n\nSecond paragraph.", Confidence: "verified"})
	knInsert(t, e.St, store.Knowledge{Type: "decision", Title: "Use SQLite", Confidence: "trusted"})
	knInsert(t, e.St, store.Knowledge{Type: "note", Title: "Staged idea", Confidence: "staged"})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/knowledge")
	for _, want := range []string{"WAL grows unbounded", "Use SQLite", "Staged idea", "Pitfall", "Decision", "Verified", "Trusted", "Staged",
		"First paragraph about the WAL.", `class="kn-title" href="/ui/knowledge/`, `/static/knowledge.css`, `aria-current="page"`} {
		if !strings.Contains(page, want) {
			t.Errorf("member list lacks %q", want)
		}
	}
	if strings.Contains(page, "rows.Close()") {
		t.Error("the list shows the code block, not just the excerpt")
	}
	if strings.Contains(page, "<style") || strings.Contains(page, " style=") || strings.Contains(page, "onclick") {
		t.Error("knowledge page breaks the CSP")
	}
	_, byType := fetchPage(t, e.Member, e.Base+"/ui/knowledge?type=decision")
	if !strings.Contains(byType, "Use SQLite") || strings.Contains(byType, "WAL grows unbounded") {
		t.Error("kind filter does not narrow the list")
	}
	_, byLevel := fetchPage(t, e.Member, e.Base+"/ui/knowledge?level=staged")
	if !strings.Contains(byLevel, "Staged idea") || strings.Contains(byLevel, "Use SQLite") {
		t.Error("status filter does not narrow the list")
	}
	_, byQuery := fetchPage(t, e.Member, e.Base+"/ui/knowledge?q=SQLite")
	if !strings.Contains(byQuery, "Use SQLite") || strings.Contains(byQuery, "Staged idea") {
		t.Error("search does not narrow the list")
	}
	_, none := fetchPage(t, e.Member, e.Base+"/ui/knowledge?q=nothingmatchesthis")
	if !strings.Contains(none, "Nothing matches.") || !strings.Contains(none, "Clear filters") {
		t.Error("no-match state lacks its line or action")
	}
}

func TestKnowledgeEmptyIsOneLineAndOneAction(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/knowledge")
	if !strings.Contains(page, "No knowledge yet.") || strings.Count(page, `href="/ui/overview?connect=1"`) != 1 {
		t.Error("empty knowledge page lacks the line or the single action")
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/knowledge")
	if !strings.Contains(guest, "Nothing here yet.") || strings.Contains(guest, "connect=1") {
		t.Error("guest empty state offers an action or lacks the line")
	}
	_, review := fetchPage(t, e.Owner, e.Base+"/ui/review")
	if !strings.Contains(review, "Nothing to review.") {
		t.Error("empty review queue lacks its line")
	}
}

func TestKnowledgeGuestSeesOnlyReleasedEntriesAndNoSessionNumbers(t *testing.T) {
	e := seedSessions(t)
	trusted := knInsert(t, e.St, store.Knowledge{Title: "Released lesson", Confidence: "trusted", SessionRef: "session:" + strconv.FormatInt(e.id["anna-private"], 10) + "#3"})
	knInsert(t, e.St, store.Knowledge{Title: "SECRET-STAGED", Confidence: "staged"})
	knInsert(t, e.St, store.Knowledge{Title: "SECRET-HIDDEN", Confidence: "trusted", Scope: scope.Axes{Project: shellHiddenProject}})
	_, page := e.get(t, e.Guest, "/ui/knowledge")
	if !strings.Contains(page, "Released lesson") {
		t.Fatal("guest does not see the released entry")
	}
	for _, leak := range []string{"SECRET", "session:", e.pid["anna-private"], "/ui/review", `data-nav="knowledge-review"`} {
		if strings.Contains(page, leak) {
			t.Errorf("guest list leaks %q", leak)
		}
	}
	if code, _ := e.get(t, e.Guest, "/ui/knowledge/"+strconv.FormatInt(trusted, 10)); code != 200 {
		t.Errorf("guest detail of a released entry = %d", code)
	}
	if regexp.MustCompile(`/ui/sessions/`).MatchString(page) {
		t.Error("guest gets a link to a session it cannot read")
	}
	// The member reads that session and gets the link, never the number.
	_, member := e.get(t, e.Owner, "/ui/knowledge")
	if !strings.Contains(member, `href="/ui/sessions/`+e.pid["anna-private"]+`#c3"`) || strings.Contains(member, "session:") {
		t.Error("the owner link to the readable source is missing or shows the reference")
	}
	secret := knInsert(t, e.St, store.Knowledge{Title: "SECRET-STAGED-2", Confidence: "staged"})
	if code, page := e.get(t, e.Guest, "/ui/knowledge/"+strconv.FormatInt(secret, 10)); code != 404 || strings.Contains(page, "SECRET") {
		t.Errorf("guest detail of an unreleased entry = %d", code)
	}
	hidden := knInsert(t, e.St, store.Knowledge{Title: "SECRET-HIDDEN-2", Confidence: "trusted", Scope: scope.Axes{Project: shellHiddenProject}})
	if code, _ := e.get(t, e.Member, "/ui/knowledge/"+strconv.FormatInt(hidden, 10)); code != 404 {
		t.Errorf("member detail of a hidden project = %d", code)
	}
}

func TestReviewShowsEvidenceAndOnlyTheActionsTheViewerMayTake(t *testing.T) {
	e := seedSessions(t)
	id := knInsert(t, e.St, store.Knowledge{Type: "pitfall", Title: "Pending finding", Body: "Check the lock.\n\nSecond.", Origin: "distilled", Confidence: "quarantined"})
	if err := e.St.AddEvidence(id, []store.Evidence{{SessionID: e.id["anna-project"], ChunkSeq: 2, Quote: "the lock was held"}, {SessionID: e.id["alice-private"], ChunkSeq: 5, Quote: "SECRET-QUOTE"}}); err != nil {
		t.Fatal(err)
	}
	_, owner := e.get(t, e.Owner, "/ui/review")
	for _, want := range []string{"Pending finding", "Check the lock.", "the lock was held", `/ui/review/` + strconv.FormatInt(id, 10) + `/approve`, `/ui/review/` + strconv.FormatInt(id, 10) + `/reject`, `name="csrf_token"`, "Quarantined", "Approve", "Reject", "Edit"} {
		if !strings.Contains(owner, want) {
			t.Errorf("owner review lacks %q", want)
		}
	}
	_, reviewer := e.get(t, e.Reviewer, "/ui/review")
	if !strings.Contains(reviewer, "/approve") || !strings.Contains(reviewer, "the lock was held") || strings.Contains(reviewer, e.pid["alice-private"]) {
		t.Error("reviewer sees the wrong actions or links the unreadable session")
	}
	if strings.Contains(reviewer, "/reject") {
		t.Error("reviewer sees reject without the right to edit")
	}
	_, member := e.get(t, e.Member, "/ui/review")
	if strings.Contains(member, "/approve") || strings.Contains(member, "/reject") || strings.Contains(member, `href="#edit"`) {
		t.Error("a plain member gets decision buttons")
	}
	if regexp.MustCompile(`/ui/sessions/\d+["#?]`).MatchString(owner) {
		t.Error("review links a session by number")
	}
}

func TestReviewApproveRejectUndoAndEdit(t *testing.T) {
	e := ovEnv(t)
	a := knInsert(t, e.St, store.Knowledge{Title: "Approve me", Confidence: "staged"})
	r := knInsert(t, e.St, store.Knowledge{Title: "Reject me", Confidence: "staged"})
	act := func(c *http.Client, id int64, verb string, extra url.Values) *http.Response {
		form := url.Values{"next": {"review"}}
		for k, v := range extra {
			form[k] = v
		}
		return knPost(t, c, e.Base, "/ui/review", "/ui/review/"+strconv.FormatInt(id, 10)+"/"+verb, form)
	}
	resp := act(e.Reviewer, a, "approve", nil)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/ui/review?done=approved&k="+strconv.FormatInt(a, 10)) {
		t.Fatalf("approve = %d -> %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if k, _ := e.St.KnowledgeByID(a); k.Confidence != "verified" || k.Status != "active" || k.ConfirmedBy != "rita" {
		t.Errorf("after approve: %q/%q confirmed by %q", k.Confidence, k.Status, k.ConfirmedBy)
	}
	_, page := fetchPage(t, e.Reviewer, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(page, `role="status"`) || !strings.Contains(page, "Approved: Approve me") {
		t.Error("no visible result after approving")
	}
	if strings.Contains(page, ">Approve me<") && strings.Contains(page, `/approve`) && strings.Contains(page, `kn-card`) {
		t.Error("an approved entry stays in the queue")
	}
	// Reject is the owner's (edit right); the page offers Undo afterwards.
	resp = act(e.Owner, r, "reject", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("reject = %d", resp.StatusCode)
	}
	if k, _ := e.St.KnowledgeByID(r); k.Status != "deprecated" {
		t.Errorf("after reject: status %q", k.Status)
	}
	_, page = fetchPage(t, e.Owner, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(page, "Rejected: Reject me") || !strings.Contains(page, "/restore") || !strings.Contains(page, "Undo") {
		t.Error("rejecting shows no result with an undo")
	}
	if resp = act(e.Owner, r, "restore", nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("restore = %d", resp.StatusCode)
	}
	if k, _ := e.St.KnowledgeByID(r); k.Status != "active" || k.Confidence != "staged" {
		t.Errorf("after undo: %q/%q", k.Status, k.Confidence)
	}
	// What a viewer may not do stays undone.
	if resp = act(e.Member, r, "approve", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member approve = %d", resp.StatusCode)
	}
	if resp = act(e.Reviewer, r, "reject", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("reviewer reject = %d", resp.StatusCode)
	}
	if resp = act(e.Owner, r, "explode", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown verdict = %d", resp.StatusCode)
	}
	if k, _ := e.St.KnowledgeByID(r); k.Status != "active" {
		t.Error("a refused action changed the entry")
	}

	// Edit from the detail page keeps history.
	path := "/ui/knowledge/" + strconv.FormatInt(r, 10)
	resp = knPost(t, e.Owner, e.Base, path, path+"/edit", url.Values{"title": {"Reject me, better"}, "body": {"New body"}, "type": {"plan"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "done=saved") {
		t.Fatalf("edit = %d -> %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if k, _ := e.St.KnowledgeByID(r); k.Title != "Reject me, better" || k.Body != "New body" || k.Type != "plan" || k.LastModifiedBy != "alice" {
		t.Errorf("after edit: %+v", k)
	}
	_, detail := fetchPage(t, e.Owner, e.Base+resp.Header.Get("Location"))
	for _, want := range []string{"Reject me, better", "Saved: Reject me, better", "New body", "History", "Reject me", `id="edit"`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if resp = knPost(t, e.Owner, e.Base, path, path+"/edit", url.Values{"title": {""}, "body": {"x"}, "type": {"note"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty title = %d", resp.StatusCode)
	}
	if resp = knPost(t, e.Member, e.Base, path, path+"/edit", url.Values{"title": {"hijack"}, "body": {"x"}, "type": {"note"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member edit = %d", resp.StatusCode)
	}
}

func TestReviewActionsNeedCSRFAndAnInteractiveSession(t *testing.T) {
	e := ovEnv(t)
	id := knInsert(t, e.St, store.Knowledge{Title: "Guarded", Confidence: "staged"})
	target := e.Base + "/ui/review/" + strconv.FormatInt(id, 10) + "/approve"
	for name, form := range map[string]url.Values{"missing": {}, "wrong": {"csrf_token": {"nope"}}} {
		if resp := sameOriginPostForm(t, e.Owner, target, form); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s CSRF token: %d", name, resp.StatusCode)
		}
	}
	token := renderedCSRFToken(t, e.Owner, e.Base+"/ui/review")
	req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(url.Values{"csrf_token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := e.Owner.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin accepted: %v %v", resp, err)
	}
	if k, _ := e.St.KnowledgeByID(id); k.Confidence != "staged" {
		t.Error("a refused request approved the entry")
	}
}

func TestBodyBlocksSplitParagraphsAndCode(t *testing.T) {
	got := bodyBlocks("one\n\ntwo line\nthree\n\n```sh\nls -l\n```\nafter", 0)
	if len(got) != 4 || got[0].Text != "one" || got[1].Text != "two line\nthree" || !got[2].Code || got[2].Text != "ls -l" || got[3].Text != "after" {
		t.Errorf("blocks = %+v", got)
	}
	if out := bodyBlocks(strings.Repeat("word ", 100), 20); len(out) != 1 || !strings.HasSuffix(out[0].Text, "…") {
		t.Errorf("cut = %+v", out)
	}
	if p := bodyBlocks("run `ls` now", 0)[0].Parts; len(p) != 3 || !p[1].Code || p[1].Text != "ls" {
		t.Errorf("inline code = %+v", p)
	}
	if excerpt("```\ncode only\n```") != "code only" {
		t.Error("code-only excerpt")
	}
}
