package web

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestLabelClassSnapsToThePalette(t *testing.T) {
	if got := labelClass("0969da"); got != "lc-0969da" {
		t.Errorf("an exact colour keeps itself: %q", got)
	}
	if got := labelClass("#FBCA05"); got != "lc-fbca04" {
		t.Errorf("a near colour snaps to the nearest: %q", got)
	}
	if got := labelClass("nonsense"); got != "lc-none" {
		t.Errorf("no colour: %q", got)
	}
	css, err := os.ReadFile("static/ui-labels.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range labelPalette {
		if !strings.Contains(string(css), ".lb.lc-"+p+" ") {
			t.Errorf("ui-labels.css lacks a rule for %s", p)
		}
	}
}

func listEnv(t *testing.T) (shellEnv, *store.ProjectAccess) {
	t.Helper()
	e := ovEnv(t)
	return e, e.St.Access(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind})
}

func TestIssueListShowsLabelPillsAndSavedSearchesAndTheNewMenu(t *testing.T) {
	e, pa := listEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Labelled issue", Criteria: []string{"one"}})
	lb, err := pa.CreateLabel(store.LabelInput{Project: shellProject, Name: "ui", Color: "0969da", By: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SetLabels("request", req.Request.ID, []string{lb.Name}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SavePreset(shellProject, "user", "mine-to-review", "", []string{"is:issue is:open"}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues")
	for _, want := range []string{`class="lb lc-0969da"`, "Saved searches", "mine-to-review", "New issue", `href="/ui/x/shell/issues/new"`, `href="/ui/x/shell/issues/new?kind=idea"`, `href="/ui/x/shell/issues/new?kind=pain"`, "Save search"} {
		if !strings.Contains(page, want) {
			t.Errorf("issue list lacks %q", want)
		}
	}
	_, byLabel := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("label:ui"))
	if !strings.Contains(byLabel, "Labelled issue") {
		t.Error("the label filter drops the labelled issue")
	}
	_, other := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("label:nothing"))
	if strings.Contains(other, "Labelled issue") || strings.Contains(other, "is-unsupported") {
		t.Error("an unknown label should match nothing, not be unsupported")
	}
}

func TestIdeaAndPainListsShowTheirOwnRowsAndButtons(t *testing.T) {
	e, pa := listEnv(t)
	if _, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Weekly look back", Body: "Every Monday one page.\nSecond line.", Actor: idea.Actor{AccountID: 1, Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	d, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Restart takes too long", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SetPainState(d.Pain.ID, "mitigated", "Over 400 sessions it is slow again.", pain.Evidence{Kind: "commit", Ref: "abc1234"}, "", pain.Actor{AccountID: 1, Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	_, ideas := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:idea is:open"))
	for _, want := range []string{"Weekly look back", "Every Monday one page.", "New idea", "Taken up", "Dismissed", `/ui/x/shell/ideas/1`} {
		if !strings.Contains(ideas, want) {
			t.Errorf("idea list lacks %q", want)
		}
	}
	if strings.Contains(ideas, "Second line.") {
		t.Error("the idea row shows more than the first line")
	}
	_, pains := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:pain is:open"))
	for _, want := range []string{"Restart takes too long", "Stays open", "Over 400 sessions it is slow again.", "Mitigated", "New pain", "Status"} {
		if !strings.Contains(pains, want) {
			t.Errorf("pain list lacks %q", want)
		}
	}
	_, byStatus := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:pain is:open status:active"))
	if strings.Contains(byStatus, "Restart takes too long") {
		t.Error("the status filter keeps a mitigated pain under active")
	}
}

func TestSavedSearchIsKeptForThePersonAndAppearsInTheList(t *testing.T) {
	e, _ := listEnv(t)
	form := url.Values{"project": {shellProject}, "name": {"open-bugs"}, "q": {"type:bug is:open"}, "back": {"/ui/x/shell/issues"}}
	form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/searches", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save -> %d", resp.StatusCode)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues")
	if !strings.Contains(page, "open-bugs") {
		t.Error("the saved search is not in the list")
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/x/shell/issues")
	if strings.Contains(member, "open-bugs") {
		t.Error("a personal saved search shows for somebody else")
	}
	form.Set("name", "")
	resp = sameOriginPostForm(t, e.Owner, e.Base+"/ui/searches", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a search without a name -> %d, want 400", resp.StatusCode)
	}
}

func newIssuePost(t *testing.T, e shellEnv, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
	form.Set("project", shellProject)
	return sameOriginPostForm(t, e.Owner, e.Base+"/ui/issues/create", form)
}

func TestNewIssuePageAndCreate(t *testing.T) {
	e, pa := listEnv(t)
	lb, err := pa.CreateLabel(store.LabelInput{Project: shellProject, Name: "agenten-ux", Color: "f9d0c4", By: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/new")
	for _, want := range []string{"New issue", `action="/ui/issues/create"`, `name="criterion"`, "Requirements", "Work package", "New work package", "Create another afterwards", "Create issue", "agenten-ux"} {
		if !strings.Contains(page, want) {
			t.Errorf("new issue page lacks %q", want)
		}
	}
	if strings.Contains(page, "data-submit") {
		t.Error("the composer brings its own send button")
	}
	resp := newIssuePost(t, e, url.Values{"kind": {"issue"}, "title": {"Voice control"}, "body": {"Speak instead of typing."}, "type": {"change"},
		"criterion": {"Answer by voice", "  ", "Confirm before sending"}, "label": {lb.Name}, "package": {"new"}, "package_name": {"Voice"}, "package_branch": {"feat/voice"}, "role": {"optional"}})
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	m := regexp.MustCompile(`^/ui/x/shell/issues/(\d+)$`).FindStringSubmatch(loc)
	if resp.StatusCode != http.StatusSeeOther || m == nil {
		t.Fatalf("create -> %d %q", resp.StatusCode, loc)
	}
	_, issue := fetchPage(t, e.Owner, e.Base+loc)
	for _, want := range []string{"Voice control", "Answer by voice", "Confirm before sending"} {
		if !strings.Contains(issue, want) {
			t.Errorf("the new issue lacks %q", want)
		}
	}
	if strings.Contains(issue, "Answer by voice</") && strings.Count(issue, "Confirm before sending") == 0 {
		t.Error("requirements lost")
	}
	pkgs, err := pa.PackagesOfProject(shellProject, "open")
	if err != nil || len(pkgs) != 1 || pkgs[0].Name != "Voice" || pkgs[0].Branch != "feat/voice" {
		t.Fatalf("work package: %+v %v", pkgs, err)
	}
	labels, _ := e.St.LabelsFor("request", []int64{1})
	if len(labels[1]) != 1 || labels[1][0].Name != "agenten-ux" {
		t.Errorf("labels: %+v", labels)
	}
	// Without a title the form comes back with the reason and keeps what was typed.
	resp = newIssuePost(t, e, url.Values{"kind": {"issue"}, "title": {" "}, "body": {"Kept text"}})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Give it a title.") || !strings.Contains(body, "Kept text") {
		t.Errorf("empty title -> %d: %s", resp.StatusCode, body)
	}
	// "Create another" goes back to an empty form.
	resp = newIssuePost(t, e, url.Values{"kind": {"issue"}, "title": {"Second"}, "again": {"1"}})
	resp.Body.Close()
	if resp.Header.Get("Location") != "/ui/x/shell/issues/new" {
		t.Errorf("again -> %q", resp.Header.Get("Location"))
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}

func TestNewIdeaAndNewPainCreateTheirOwnKind(t *testing.T) {
	e, _ := listEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/new?kind=idea")
	if !strings.Contains(page, "New idea") || strings.Contains(page, `name="criterion"`) || strings.Contains(page, "Work package") {
		t.Errorf("the idea form shows issue parts: %s", page)
	}
	resp := newIssuePost(t, e, url.Values{"kind": {"idea"}, "title": {"Voice in the inbox"}, "body": {"Answer by voice."}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/ui/x/shell/ideas/") {
		t.Fatalf("idea -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp = newIssuePost(t, e, url.Values{"kind": {"pain"}, "title": {"Restart is slow"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/ui/x/shell/pains/") {
		t.Fatalf("pain -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// A guest cannot create.
	form := url.Values{"kind": {"issue"}, "title": {"x"}, "project": {shellProject}, "csrf_token": {renderedCSRFToken(t, e.Guest, e.Base+"/ui/overview")}}
	resp = sameOriginPostForm(t, e.Guest, e.Base+"/ui/issues/create", form)
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("a guest created an issue: %d", resp.StatusCode)
	}
}

func TestNewIssueTakesParentAndOriginAndShowsThem(t *testing.T) {
	e, pa := listEnv(t)
	parent := rqMake(t, e.St, rqSpec{Title: "Umbrella"})
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Wish for voice", Body: "Voice.", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/new?parent=REQ-"+itoa(parent.Request.ID))
	for _, want := range []string{`name="parent"`, `value="REQ-` + itoa(parent.Request.ID) + `"`, `name="derived_from"`, "Parent issue", "Derived from"} {
		if !strings.Contains(page, want) {
			t.Errorf("new issue page lacks %q", want)
		}
	}
	resp := newIssuePost(t, e, url.Values{"kind": {"issue"}, "title": {"Voice control"}, "parent": {"REQ-" + itoa(parent.Request.ID)},
		"derived_from": {"IDEA-" + itoa(i.ID) + ", REQ-" + itoa(parent.Request.ID)}})
	resp.Body.Close()
	m := regexp.MustCompile(`^/ui/x/shell/issues/(\d+)$`).FindStringSubmatch(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusSeeOther || m == nil {
		t.Fatalf("create -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, issue := fetchPage(t, e.Owner, e.Base+resp.Header.Get("Location"))
	for _, want := range []string{"Parent", "Umbrella", "Wish for voice", "Arose from"} {
		if !strings.Contains(issue, want) {
			t.Errorf("the new issue lacks %q", want)
		}
	}
	// An origin that is another issue is the new kind of relation, named in words.
	other := rqMake(t, e.St, rqSpec{Title: "The older one"})
	resp = newIssuePost(t, e, url.Values{"kind": {"issue"}, "title": {"Follow-up"}, "derived_from": {"REQ-" + itoa(other.Request.ID)}})
	resp.Body.Close()
	if _, follow := fetchPage(t, e.Owner, e.Base+resp.Header.Get("Location")); !strings.Contains(follow, "Derived from") || !strings.Contains(follow, "The older one") || strings.Contains(follow, ">derived<") {
		t.Error("the issue derived from another issue does not name it")
	}
	if strings.Contains(issue, ">derived<") {
		t.Error("the raw relation kind shows")
	}
	// Nothing is made when a target is wrong; foreign and unknown read the same.
	hidden := rqMake(t, e.St, rqSpec{Title: "Elsewhere", Project: shellHiddenProject})
	for _, form := range []url.Values{
		{"parent": {"REQ-" + itoa(hidden.Request.ID)}},
		{"parent": {"REQ-99999"}},
		{"derived_from": {"IDEA-99999"}},
	} {
		form.Set("kind", "issue")
		form.Set("title", "Must not exist")
		resp := newIssuePost(t, e, form)
		page := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "does not exist in this repository") {
			t.Errorf("a wrong target -> %d", resp.StatusCode)
		}
	}
	resp = newIssuePost(t, e, url.Values{"kind": {"issue"}, "title": {"Must not exist"}, "parent": {"not a ref"}})
	if page := body(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "Write the parent as REQ-12") {
		t.Errorf("a wrong spelling -> %d", resp.StatusCode)
	}
	if list, err := e.St.SearchRequestsWith(pa.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: shellProject}, State: "open", Limit: 50}), store.SearchAuto); err == nil {
		for _, r := range list.Results {
			if r.Request.Title == "Must not exist" {
				t.Error("an issue with a wrong target was made")
			}
		}
	}
	// A guest cannot use the fields to read what they may not.
	form := url.Values{"kind": {"issue"}, "title": {"x"}, "project": {shellProject}, "parent": {"REQ-" + itoa(parent.Request.ID)}, "csrf_token": {renderedCSRFToken(t, e.Guest, e.Base+"/ui/overview")}}
	if resp := sameOriginPostForm(t, e.Guest, e.Base+"/ui/issues/create", form); resp.StatusCode < 400 {
		t.Errorf("a guest created an issue: %d", resp.StatusCode)
	}
}
