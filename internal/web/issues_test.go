package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var issuesTagRE = regexp.MustCompile(`(?s)<[^>]*>`)

// issuesVisibleText is what a person reads of a page: no markup, so no attribute.
func issuesVisibleText(page string) string { return issuesTagRE.ReplaceAllString(page, " ") }

func TestParseIssueQueryMapsToTodaysFiltersAndMarksTheRest(t *testing.T) {
	iq := parseIssueQuery("is:closed type:Bug branch:feat/x pr:#12 commit:abcdef1 priority:hoch label:ui author:robin for:robin export csv https://x.y")
	if iq.Segment != segIssue || iq.State != "closed" || iq.Type != "bug" || iq.Branch != "feat/x" || iq.PR != 12 || iq.Commit != "abcdef1" || iq.Priority != "hoch" || iq.Label != "ui" || iq.Author != "robin" {
		t.Errorf("filters: %+v", iq)
	}
	if iq.Text != "export csv https://x.y" {
		t.Errorf("free text = %q", iq.Text)
	}
	if got := strings.Join(iq.Unsupported, " "); got != "for:robin" {
		t.Errorf("unsupported = %q", got)
	}
	bad := parseIssueQuery("is:weird type:nonsense pr:x commit:zz")
	if len(bad.Unsupported) != 4 || bad.State != "open" || bad.Type != "" {
		t.Errorf("invalid values are not reported: %+v", bad)
	}
	if p := parseIssueQuery("is:pain"); p.Segment != segPain {
		t.Errorf("segment %q", p.Segment)
	}
	if got := withQualifier("is:issue is:open type:bug x", dropIs(isStates...), "is:closed"); got != "is:issue type:bug x is:closed" {
		t.Errorf("withQualifier = %q", got)
	}
}

func TestIssuesTabFiltersBySearchText(t *testing.T) {
	e := ovEnv(t)
	rqMake(t, e.St, rqSpec{Title: "Alpha exporter", Type: "feature", Criteria: []string{"one", "two"}})
	rqMake(t, e.St, rqSpec{Title: "Beta crash", Type: "bug"})
	done := rqMake(t, e.St, rqSpec{Title: "Gamma finished", Type: "feature"})
	if err := e.St.CompleteRequest(done.Request.ID, requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	get := func(q string) string {
		t.Helper()
		status, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape(q))
		if status != http.StatusOK {
			t.Fatalf("q=%q status=%d", q, status)
		}
		return page
	}
	page := get("")
	if !strings.Contains(page, "Alpha exporter") || !strings.Contains(page, "Beta crash") || strings.Contains(page, "Gamma finished") {
		t.Error("the default list is not the open issues")
	}
	if !strings.Contains(page, `data-seg="issue"`) || !strings.Contains(page, `data-seg="idea"`) || !strings.Contains(page, `data-seg="pain"`) {
		t.Error("the segments Issues, Ideas and Pains are missing")
	}
	if page := get("type:bug"); strings.Contains(page, "Alpha exporter") || !strings.Contains(page, "Beta crash") {
		t.Error("type:bug does not filter")
	}
	if page := get("exporter"); strings.Contains(page, "Beta crash") || !strings.Contains(page, "Alpha exporter") {
		t.Error("free text does not filter")
	}
	if page := get("is:closed"); !strings.Contains(page, "Gamma finished") || strings.Contains(page, "Alpha exporter") {
		t.Error("is:closed does not show the closed issues")
	}
	page = get("for:robin type:bug")
	if !strings.Contains(page, `is-unsupported"`) || !strings.Contains(page, "<code>for:robin</code>") || !strings.Contains(page, "Beta crash") {
		t.Error("an unsupported qualifier is not marked, or the supported one is lost")
	}
	if !strings.Contains(page, `value="for:robin type:bug"`) {
		t.Error("the search text is not kept as typed")
	}
	if page := get("is:idea"); !strings.Contains(page, `class="empty"`) || strings.Contains(page, "Alpha exporter") {
		t.Error("the ideas segment is not an empty line")
	}
	// Progress of the criteria and the row meta without a prefix.
	text := issuesVisibleText(get(""))
	if strings.Contains(text, "REQ-") || strings.Contains(text, "PAIN-") {
		t.Errorf("an identifier shows its prefix: %s", text)
	}
}

func TestIssuesPainsSegmentUsesTheSameList(t *testing.T) {
	e := ovEnv(t)
	if _, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Slow restart", Actor: pain.Actor{AccountID: 1, Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	status, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:pain is:open"))
	if status != http.StatusOK || !strings.Contains(page, "Slow restart") || !strings.Contains(page, `class="row irow"`) {
		t.Fatalf("pain row missing: status=%d", status)
	}
	if strings.Contains(issuesVisibleText(page), "PAIN-") || !strings.Contains(page, `<span class="rn">#`) {
		t.Error("the list row shows the number muted and without prefix")
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:pain type:bug"))
	if !strings.Contains(page, "<code>type:bug</code>") {
		t.Error("a qualifier pains cannot filter by is not marked")
	}
	resp, err := e.Owner.Get(e.Base + "/ui/x/shell/pains")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if want := "/ui/x/shell/issues?q=is%3Apain+is%3Aopen"; resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
		t.Errorf("the old pains tab -> %d %q, want 302 %q", resp.StatusCode, resp.Header.Get("Location"), want)
	}
}

func TestIssuePageShowsBlockerChecklistSidebarAndKeepsTheActions(t *testing.T) {
	e := ovEnv(t)
	parent := rqMake(t, e.St, rqSpec{Title: "Parent dach"})
	d := rqMake(t, e.St, rqSpec{Title: "Build the thing", Desc: "Long text.", Criteria: []string{"First requirement", "Second requirement"}})
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "a41c7e2", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.AddRequestRelation(d.Request.ID, requestdomain.Relation{Kind: "parent", OtherRequestID: parent.Request.ID}, "alice"); err != nil {
		t.Fatal(err)
	}
	tpTask(t, e, shellProject, d.Request.ID, humantask.CreateInput{Title: "Decide the three questions", Assignee: "alice"})
	for i, harness := range []string{"claude-code", "codex"} {
		sid, err := e.St.UpsertSession(store.Session{Harness: harness, ExternalID: "ext-" + harness, AccountID: 1, Scope: scope.Axes{Project: shellProject, Machine: "m"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.St.SetSessionVisibility(sid, e.St.Access(store.Principal{ID: "person:1", Label: "alice"}), store.VisProject); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.St.StartRequestWork(d.Request.ID, sid, []string{"primary", "related"}[i], "alice"); err != nil {
			t.Fatal(err)
		}
	}
	path := "/ui/x/shell/issues/" + itoa(d.Request.ID)
	status, page := fetchPage(t, e.Owner, e.Base+path)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, want := range []string{"Build the thing", `class="n" title="REQ-` + itoa(d.Request.ID) + `"`, `id="task-`, "Decide the three questions", `class="waits"`,
		`data-crit="met"`, `data-crit="open"`, "a41c7e2", "Parent dach", `title="Task TASK-`, `<a href="/ui/x/shell/issues" aria-current="page"`,
		`a-claude`, `a-codex`, `/ui/requests/` + itoa(d.Request.ID) + `/criteria/`} {
		if !strings.Contains(page, want) {
			t.Errorf("issue page lacks %q", want)
		}
	}
	// Running text (the history) names an object with the long pill ("REQ-1"), the
	// columns and the sidebar with the short form ("#1").
	if !strings.Contains(page, `class="ref pill k-req"`) || !strings.Contains(page, `class="nchip"`) {
		t.Error("the page lacks the long pill in the history or the short reference in the sidebar")
	}
	// The old address leads here, with what the action reports.
	resp, err := e.Owner.Get(e.Base + "/ui/requests/" + itoa(d.Request.ID) + "?edit=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != path+"?edit=1" {
		t.Errorf("the old address -> %d %q, want 302 %q", resp.StatusCode, resp.Header.Get("Location"), path+"?edit=1")
	}
	// The existing actions work from the new page and lead back to it.
	resp = rqPost(t, e, e.Owner, d.Request.ID, "/criteria/"+itoa(d.Criteria[1].ID), url.Values{"state": {"met"}, "evidence_kind": {"test"}, "evidence_ref": {"TestIt"}})
	resp.Body.Close()
	_, page = fetchPage(t, e.Owner, e.Base+path)
	if strings.Count(page, `data-crit="met"`) != 2 || !strings.Contains(page, "TestIt") {
		t.Error("marking a requirement as met does not show on the issue page")
	}
}

func TestIssuePagesHideWhatTheViewerMayNotSee(t *testing.T) {
	e := ovEnv(t)
	secret := rqMake(t, e.St, rqSpec{Title: "SECRET-REQ", Project: shellHiddenProject})
	pub := rqMake(t, e.St, rqSpec{Title: "Visible request"})
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		for _, path := range []string{"/ui/x/hidden/issues", "/ui/x/hidden/issues/" + itoa(secret.Request.ID), "/ui/x/shell/issues/" + itoa(secret.Request.ID), "/ui/requests/" + itoa(secret.Request.ID)} {
			if status, page := fetchPage(t, c, e.Base+path); status != http.StatusNotFound || strings.Contains(page, "SECRET-REQ") {
				t.Errorf("%s: GET %s = %d", name, path, status)
			}
		}
		status, page := fetchPage(t, c, e.Base+"/ui/x/shell/issues")
		if status != http.StatusOK || strings.Contains(page, "SECRET-REQ") || !strings.Contains(page, "Visible request") {
			t.Errorf("%s: list status=%d", name, status)
		}
		if status, _ := fetchPage(t, c, e.Base+"/ui/x/shell/issues/"+itoa(pub.Request.ID)); status != http.StatusOK {
			t.Errorf("%s: the visible issue answers %d", name, status)
		}
	}
}

func TestRepoTabsAreCodeIssuesPullsKnowledgeRunbooksAndAMenu(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell")
	row := page[strings.Index(page, `class="utabs"`):strings.Index(page, `class="tsep"`)]
	last := -1
	for _, key := range []string{"code", "issues", "pulls", "knowledge", "runbooks"} {
		i := strings.Index(row, `data-tab="`+key+`"`)
		if i < last || i < 0 {
			t.Errorf("tab %s is missing or out of order", key)
		}
		last = i
	}
	for _, key := range []string{"pains", "sessions", "rooms", "commits"} {
		if strings.Contains(row, `data-tab="`+key+`"`) {
			t.Errorf("tab %s stands in the row", key)
		}
	}
	menu := page[strings.Index(page, `class="tsep"`):]
	for _, key := range []string{"rooms", "sessions", "commits"} {
		if !strings.Contains(menu, `data-ic="`+key+`"`) {
			t.Errorf("the second group lacks %s as icon", key)
		}
	}
	if status, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/runbooks"); status != http.StatusOK || !strings.Contains(page, `data-tab="runbooks"`) || !strings.Contains(page, "ui-runbooks.css") {
		t.Errorf("runbooks tab: status=%d or not its own page", status)
	}
	// Without anything reported yet both tabs are pages of their own with one empty line.
	for tab, line := range map[string]string{"pulls": "No pull requests yet.", "commits": "No commits yet."} {
		status, empty := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/"+tab)
		if status != http.StatusOK || !strings.Contains(empty, line) || !strings.Contains(empty, `data-tab="`+tab+`"`) || !strings.Contains(empty, "ui-pulls.css") {
			t.Errorf("tab %s: status=%d or no empty line", tab, status)
		}
	}
}

// The history of an issue says who did what to which object, in sentences, and
// not in the labels of the old list ("Work started", "Relation added").
func TestIssueHistoryReadsAsSentencesWithObjects(t *testing.T) {
	e := ovEnv(t)
	parent := rqMake(t, e.St, rqSpec{Title: "Parent dach"})
	d := rqMake(t, e.St, rqSpec{Title: "Sentences", Criteria: []string{"First requirement"}})
	if _, err := e.St.AddRequestRelation(d.Request.ID, requestdomain.Relation{Kind: "parent", OtherRequestID: parent.Request.ID}, "robin"); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "a41c7e2", Person: "claude"}); err != nil {
		t.Fatal(err)
	}
	tpTask(t, e, shellProject, d.Request.ID, humantask.CreateInput{Title: "Decide it", Assignee: "alice"})
	sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "ext-s", AccountID: 1, Scope: scope.Axes{Project: shellProject, Machine: "mainex"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetSessionVisibility(sid, e.St.Access(store.Principal{ID: "person:1", Label: "alice"}), store.VisProject); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(d.Request.ID, sid, "primary", "claude"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(d.Request.ID))
	tl := page[strings.Index(page, `<div class="tlx">`):]
	text := issuesVisibleText(tl)
	for _, want := range []string{"robin set", "as the parent issue", "claude met", "Requirement 1", "created a task for alice", "claude started working", "created this issue"} {
		if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
			t.Errorf("the history lacks %q: %s", want, text)
		}
	}
	for _, old := range []string{"Work started", "Relation added", "Criterion met", "Task created"} {
		if strings.Contains(text, old) {
			t.Errorf("the history still has the label %q", old)
		}
	}
	// The object is a link or a reference, the proof sits behind the sentence.
	for _, want := range []string{`href="/ui/requests/` + itoa(parent.Request.ID) + `"`, `href="#criterion-1"`, `class="chip sha"`, `data-act="work.started"`, `data-act="criterion.met"`, `data-act="task.created"`} {
		if !strings.Contains(tl, want) {
			t.Errorf("the history lacks %q", want)
		}
	}
	// The same events read in German.
	l := langDE
	got := string(issueEvents(l, []requestdomain.Activity{{Kind: "relation.added", Person: "Robin", Data: "parent REQ-602"}, {Kind: "work.started", Person: "claude", Data: "role:primary"}}, nil, nil, "", func(string) string { return "jetzt" })[0].Sentence)
	if !strings.Contains(got, "hat") || !strings.Contains(got, "als Eltern-Issue gesetzt") || !strings.Contains(got, "REQ-602") {
		t.Errorf("German relation sentence: %s", got)
	}
	if got := string(issueEvents(l, []requestdomain.Activity{{Kind: "work.started", Person: "claude", Data: "role:primary"}}, nil, nil, "", func(string) string { return "jetzt" })[0].Sentence); got != "hat mit der Arbeit begonnen" {
		t.Errorf("German work sentence: %q", got)
	}
}
