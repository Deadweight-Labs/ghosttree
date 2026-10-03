package web

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type rqSpec struct {
	Title, Desc, Type, Priority, Project, Person string
	Criteria                                     []string
}

func rqMake(t *testing.T, st *store.Store, s rqSpec) requestdomain.Detail {
	t.Helper()
	if s.Project == "" {
		s.Project = shellProject
	}
	if s.Type == "" {
		s.Type = "feature"
	}
	if s.Person == "" {
		s.Person = "alice"
	}
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: s.Type, Title: s.Title, Description: s.Desc,
		Priority: s.Priority, Scope: scope.Axes{Project: s.Project}, Person: s.Person}, Criteria: s.Criteria})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func rqPath(id int64, rest string) string { return "/ui/requests/" + strconv.FormatInt(id, 10) + rest }

func rqPost(t *testing.T, e shellEnv, c *http.Client, id int64, rest string, form url.Values) *http.Response {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/requests"))
	return sameOriginPostForm(t, c, e.Base+rqPath(id, rest), form)
}

func TestRequestListShowsTitleNumberKindPriorityProgressAndAge(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Ship the exporter", Desc: "Export all.", Type: "bug", Priority: "hoch", Criteria: []string{"a", "b", "c"}})
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	rqMake(t, e.St, rqSpec{Title: "Plain feature", Type: "feature", Priority: "mittel"})
	if _, _, err := e.St.StartRequestWork(d.Request.ID, e.id["anna-project"], "primary", "anna"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(d.Request.ID, e.id["alice-private"], "related", "alice"); err != nil {
		t.Fatal(err)
	}
	code, page := e.get(t, e.Member, "/ui/requests")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"Ship the exporter", "REQ-" + strconv.FormatInt(d.Request.ID, 10), "Bug", "hoch", "1/3", `data-pct="33"`,
		`href="/ui/requests/` + strconv.FormatInt(d.Request.ID, 10) + `"`, `/ui/sessions/` + e.pid["anna-project"], "/static/requests.css", `aria-current="page"`} {
		if !strings.Contains(page, want) {
			t.Errorf("member list lacks %q", want)
		}
	}
	if strings.Contains(page, e.pid["alice-private"]) {
		t.Error("the list links a session the viewer may not read")
	}
	if strings.Contains(page, "<style") || strings.Contains(page, " style=") || strings.Contains(page, "onclick") {
		t.Error("requests page breaks the CSP")
	}
	for query, want := range map[string]string{"type=bug": "Ship the exporter", "priority=mittel": "Plain feature", "q=exporter": "Ship the exporter"} {
		_, narrowed := e.get(t, e.Member, "/ui/requests?"+query)
		other := "Plain feature"
		if want == other {
			other = "Ship the exporter"
		}
		if !strings.Contains(narrowed, want) || strings.Contains(narrowed, other) {
			t.Errorf("filter %s does not narrow the list", query)
		}
	}
	_, prio := e.get(t, e.Member, "/ui/requests?priority=hoch")
	if !strings.Contains(prio, `<option value="mittel">`) {
		t.Error("the priority filter lost the other priorities")
	}
}

func TestRequestListStateFilterAndPaging(t *testing.T) {
	e := ovEnv(t)
	done := rqMake(t, e.St, rqSpec{Title: "Finished one"})
	if err := e.St.CompleteRequest(done.Request.ID, requestdomain.Evidence{Kind: "commit", Ref: "abc", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	dropped := rqMake(t, e.St, rqSpec{Title: "Dropped one"})
	if err := e.St.DropRequest(dropped.Request.ID, "not needed", "alice"); err != nil {
		t.Fatal(err)
	}
	rqMake(t, e.St, rqSpec{Title: "Still open"})
	for state, want := range map[string][]string{"": {"Still open"}, "done": {"Finished one"}, "dropped": {"Dropped one"}, "all": {"Still open", "Finished one", "Dropped one"}} {
		_, page := fetchPage(t, e.Owner, e.Base+"/ui/requests?state="+state)
		for _, title := range []string{"Still open", "Finished one", "Dropped one"} {
			has := strings.Contains(page, title)
			wanted := false
			for _, w := range want {
				wanted = wanted || w == title
			}
			if has != wanted {
				t.Errorf("state=%q: %q shown=%v want %v", state, title, has, wanted)
			}
		}
	}
	for i := 0; i < requestsShown+3; i++ {
		rqMake(t, e.St, rqSpec{Title: "Bulk " + strconv.Itoa(i)})
	}
	_, first := fetchPage(t, e.Owner, e.Base+"/ui/requests")
	m := regexp.MustCompile(`href="(/ui/requests\?[^"]*cursor=[^"]*)"`).FindStringSubmatch(first)
	if m == nil {
		t.Fatal("no link to older requests")
	}
	_, older := fetchPage(t, e.Owner, e.Base+strings.ReplaceAll(m[1], "&amp;", "&"))
	if !strings.Contains(older, "Still open") || strings.Contains(older, "Bulk 20") {
		t.Error("the older page does not continue the list")
	}
}

func TestRequestListEmptyIsOneLineAndOneAction(t *testing.T) {
	e := ovEnv(t)
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/requests")
	if !strings.Contains(owner, "No requests yet.") || strings.Count(owner, `href="/ui/overview?connect=1"`) != 1 {
		t.Error("empty list lacks the line or the single action")
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/requests")
	if !strings.Contains(guest, "Nothing here yet.") || strings.Contains(guest, "connect=1") {
		t.Error("guest empty state offers an action or lacks the line")
	}
	_, none := fetchPage(t, e.Owner, e.Base+"/ui/requests?q=nothingmatchesthis")
	if !strings.Contains(none, "Nothing matches.") || !strings.Contains(none, "Clear filters") {
		t.Error("no-match state lacks its line or action")
	}
}

func TestRequestListHidesWhatTheViewerMayNotRead(t *testing.T) {
	e := seedSessions(t)
	rqMake(t, e.St, rqSpec{Title: "Visible request"})
	rqMake(t, e.St, rqSpec{Title: "SECRET-REQ", Project: shellHiddenProject})
	hidden := rqMake(t, e.St, rqSpec{Title: "Open work", Criteria: []string{"x"}})
	for _, who := range []string{"anna-private", "alice-private"} {
		if _, _, err := e.St.StartRequestWork(hidden.Request.ID, e.id[who], "related", "x"); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "lead": e.Lead} {
		_, page := e.get(t, c, "/ui/requests")
		if strings.Contains(page, "SECRET") || strings.Contains(page, shellHiddenProject) {
			t.Errorf("%s list leaks the hidden project", name)
		}
		if !strings.Contains(page, "Visible request") {
			t.Errorf("%s list lacks the visible request", name)
		}
	}
	_, guest := e.get(t, e.Guest, "/ui/requests")
	for _, leak := range []string{e.pid["anna-private"], e.pid["alice-private"], "session:", `class="rq-working"`} {
		if strings.Contains(guest, leak) {
			t.Errorf("guest list leaks %q", leak)
		}
	}
	if _, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Global request", Priority: "global-prio", Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	rqMake(t, e.St, rqSpec{Title: "SECRET-PRIO", Priority: "secret-prio", Project: shellHiddenProject})
	hiddenCode, hiddenPage := e.get(t, e.Guest, "/ui/requests?project="+url.QueryEscape(shellHiddenProject))
	unknownCode, unknownPage := e.get(t, e.Guest, "/ui/requests?project="+url.QueryEscape("github.com/nobody/unknown"))
	if hiddenCode != http.StatusOK || unknownCode != http.StatusOK {
		t.Errorf("guest asking for hidden / unknown project = %d / %d, want 200 for both", hiddenCode, unknownCode)
	}
	if strings.Contains(hiddenPage, "SECRET") {
		t.Error("the hidden project's list shows its requests")
	}
	if strings.ReplaceAll(hiddenPage, url.QueryEscape(shellHiddenProject), "P") != strings.ReplaceAll(unknownPage, url.QueryEscape("github.com/nobody/unknown"), "P") &&
		strings.ReplaceAll(hiddenPage, shellHiddenProject, "P") != strings.ReplaceAll(unknownPage, "github.com/nobody/unknown", "P") {
		t.Error("a hidden project answers differently from an unknown one")
	}
	if !strings.Contains(hiddenPage, "Global request") || !strings.Contains(unknownPage, "Global request") {
		t.Error("the global request is missing from the hidden or unknown project's list")
	}
	if strings.Contains(hiddenPage, "secret-prio") {
		t.Error("the priority menu of a hidden project leaks its priorities")
	}
	_, owner := e.get(t, e.Owner, "/ui/requests")
	if !strings.Contains(owner, "SECRET-REQ") {
		t.Error("the owner does not see every project")
	}
	_, scoped := e.get(t, e.Owner, "/ui/requests?project="+url.QueryEscape(shellProject))
	if strings.Contains(scoped, "SECRET-REQ") {
		t.Error("the project selector does not narrow the list")
	}
}

func TestRequestDetailShowsDescriptionCriteriaProofWorkAndRelations(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Detail request", Desc: "First paragraph.\n\nSecond with `code`.", Priority: "hoch", Criteria: []string{"Exports run", "Docs updated"}})
	other := rqMake(t, e.St, rqSpec{Title: "Related one"})
	secret := rqMake(t, e.St, rqSpec{Title: "SECRET-OTHER", Project: shellHiddenProject})
	sess := strconv.FormatInt(e.id["anna-project"], 10)
	priv := strconv.FormatInt(e.id["alice-private"], 10)
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "session", Ref: "session:" + sess + "#3", Person: "anna"}); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetCriterionState(d.Criteria[1].ID, "waived", requestdomain.Evidence{Kind: "session", Ref: "session:" + priv + "#9", Person: "anna"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(d.Request.ID, e.id["anna-project"], "primary", "anna"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(d.Request.ID, e.id["alice-private"], "related", "alice"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []requestdomain.Relation{{Kind: "related", OtherRequestID: other.Request.ID}, {Kind: "blocks", OtherRequestID: secret.Request.ID}, {Kind: "external", ExternalRef: "https://example.com/x"}} {
		if _, err := e.St.AddRequestRelation(d.Request.ID, rel, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	code, page := e.get(t, e.Member, rqPath(d.Request.ID, ""))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"Detail request", "REQ-" + strconv.FormatInt(d.Request.ID, 10), "hoch", "First paragraph.", "<code>code</code>", "2/2",
		"AC-" + strconv.FormatInt(d.Request.ID, 10) + ".1", "Exports run", `href="/ui/sessions/` + e.pid["anna-project"] + `#c3"`,
		"Private session", "Working", "Related one", `href="/ui/requests/` + strconv.FormatInt(other.Request.ID, 10) + `"`, "https://example.com/x", "Criterion met", "Work started"} {
		if !strings.Contains(page, want) {
			t.Errorf("member detail lacks %q", want)
		}
	}
	for _, leak := range []string{"SECRET", "session:" + sess, "session:" + priv, e.pid["alice-private"], shellHiddenProject} {
		if strings.Contains(page, leak) {
			t.Errorf("member detail leaks %q", leak)
		}
	}
	if regexp.MustCompile(`/ui/sessions/\d+["?#]`).MatchString(page) {
		t.Error("a numeric session address is rendered")
	}
	if strings.Contains(page, "<style") || strings.Contains(page, " style=") {
		t.Error("detail page breaks the CSP")
	}
}

func TestRequestDetailForAGuestCarriesNoHiddenSessionAndNoNumbers(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Guest-visible", Criteria: []string{"One", "Two"}})
	hid, vis := strconv.FormatInt(e.id["anna-private"], 10), strconv.FormatInt(e.id["anna-guests"], 10)
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "session", Ref: "session:" + hid + "#4", Person: "anna"}); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetCriterionState(d.Criteria[1].ID, "met", requestdomain.Evidence{Kind: "session", Ref: "session:" + vis + "#5", Person: "anna"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(d.Request.ID, e.id["anna-private"], "primary", "anna"); err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		w, _, err := e.St.StartRequestWork(d.Request.ID, e.id["anna-guests"], "related", "anna")
		if err != nil {
			return err
		}
		_, err = e.St.FinishRequestWork(w.ID, "paused", "handoff-visible", "anna")
		return err
	}(); err != nil {
		t.Fatal(err)
	}
	code, page := e.get(t, e.Guest, rqPath(d.Request.ID, ""))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(page, `/ui/sessions/`+e.pid["anna-guests"]+`#c5`) || !strings.Contains(page, "handoff-visible") {
		t.Error("the guest lacks the readable session link or handoff")
	}
	for _, leak := range []string{e.pid["anna-private"], "session:", "Private session", "#c4", "/criteria/", "/correct", "/complete", "/drop", `name="csrf_token" value="` + "x"} {
		if strings.Contains(page, leak) {
			t.Errorf("guest detail leaks or offers %q", leak)
		}
	}
	// 2/2 criteria are met, but only one proof is readable: the hidden one is dropped, not blanked.
	if strings.Count(page, `class="rq-evidence"`) != 1 {
		t.Errorf("guest sees %d proofs, want 1", strings.Count(page, `class="rq-evidence"`))
	}
}

func TestRequestDetailOfAHiddenOrUnknownRequestIsNotFoundByteForByte(t *testing.T) {
	e := ovEnv(t)
	hidden := rqMake(t, e.St, rqSpec{Title: "SECRET-HIDDEN", Project: shellHiddenProject})
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "lead": e.Lead} {
		c1, b1 := fetchPage(t, c, e.Base+rqPath(hidden.Request.ID, ""))
		c2, b2 := fetchPage(t, c, e.Base+rqPath(hidden.Request.ID+1000, ""))
		if c1 != http.StatusNotFound || c1 != c2 || b1 != b2 || strings.Contains(b1, "SECRET") {
			t.Errorf("%s: hidden %d %q vs unknown %d %q", name, c1, b1, c2, b2)
		}
	}
	if code, _ := fetchPage(t, e.Owner, e.Base+rqPath(hidden.Request.ID, "")); code != 200 {
		t.Errorf("owner detail = %d", code)
	}
}

func TestRequestAddCriterionResolveWithProofAndResultLine(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Work it", Criteria: []string{"Existing"}})
	id := d.Request.ID
	// A member may work on, but not edit, a request of someone else.
	if resp := rqPost(t, e, e.Member, id, "/criteria", url.Values{"description": {"By anna"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member adding a criterion to a foreign request = %d", resp.StatusCode)
	}
	resp := rqPost(t, e, e.Lead, id, "/criteria", url.Values{"description": {"Brand new criterion"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("lead adding a criterion = %d", resp.StatusCode)
	}
	_, page := fetchPage(t, e.Lead, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(page, "Criterion added: Brand new criterion") || !strings.Contains(page, `role="status"`) {
		t.Error("no result line after adding")
	}
	if resp := rqPost(t, e, e.Lead, id, "/criteria", url.Values{"description": {"   "}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty criterion = %d", resp.StatusCode)
	}
	det, _ := e.St.RequestByID(id)
	if len(det.Criteria) != 2 {
		t.Fatalf("criteria = %d", len(det.Criteria))
	}
	first := det.Criteria[0].ID
	target := "/criteria/" + strconv.FormatInt(first, 10)
	for name, form := range map[string]url.Values{
		"no proof":       {"evidence_kind": {"commit"}, "evidence_ref": {" "}},
		"no kind":        {"evidence_ref": {"abc"}},
		"unknown kind":   {"evidence_kind": {"hearsay"}, "evidence_ref": {"abc"}},
		"missing fields": {},
	} {
		if resp := rqPost(t, e, e.Member, id, target, form); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if det, _ := e.St.RequestByID(id); det.Criteria[0].State != "open" {
		t.Fatal("a refused proof resolved the criterion")
	}
	resp = rqPost(t, e, e.Member, id, target, url.Values{"state": {"met"}, "evidence_kind": {"test"}, "evidence_ref": {"go test ./internal/web"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("member resolving = %d", resp.StatusCode)
	}
	det, _ = e.St.RequestByID(id)
	if det.Criteria[0].State != "met" || len(det.Criteria[0].Evidence) != 1 || det.Criteria[0].Evidence[0].Ref != "go test ./internal/web" || det.Criteria[0].Evidence[0].Person != "anna" {
		t.Errorf("criterion not recorded with proof and person: %+v", det.Criteria[0])
	}
	_, page = fetchPage(t, e.Member, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(page, "Met: Existing") || !strings.Contains(page, "go test ./internal/web") {
		t.Error("result line or proof missing after resolving")
	}
	// Resolving twice, or a criterion of another request, changes nothing.
	if resp := rqPost(t, e, e.Member, id, target, url.Values{"evidence_kind": {"test"}, "evidence_ref": {"again"}}); resp.StatusCode != http.StatusConflict {
		t.Errorf("resolving twice = %d", resp.StatusCode)
	}
	foreign := rqMake(t, e.St, rqSpec{Title: "Other", Criteria: []string{"theirs"}})
	if resp := rqPost(t, e, e.Member, id, "/criteria/"+strconv.FormatInt(foreign.Criteria[0].ID, 10), url.Values{"evidence_kind": {"test"}, "evidence_ref": {"x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a criterion of another request = %d", resp.StatusCode)
	}
	if d2, _ := e.St.RequestByID(foreign.Request.ID); d2.Criteria[0].State != "open" {
		t.Error("a criterion was resolved through another request")
	}
	// Waiving needs proof too.
	second := det.Criteria[1].ID
	resp = rqPost(t, e, e.Lead, id, "/criteria/"+strconv.FormatInt(second, 10), url.Values{"state": {"waived"}, "evidence_kind": {"decision"}, "evidence_ref": {"not needed"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("waive = %d", resp.StatusCode)
	}
	if det, _ = e.St.RequestByID(id); det.Criteria[1].State != "waived" {
		t.Error("criterion not waived")
	}
	// An invented result line shows nothing.
	_, fake := fetchPage(t, e.Owner, e.Base+rqPath(id, "?done=completed"))
	if strings.Contains(fake, "Completed.") {
		t.Error("a made-up address claims the request is completed")
	}
}

func TestRequestCorrectNeedsAReasonAndEditRight(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Old title", Desc: "Old text", Priority: "mittel", Type: "bug"})
	id := d.Request.ID
	form := func(m map[string]string) url.Values {
		v := url.Values{"title": {"Old title"}, "description": {"Old text"}, "type": {"bug"}, "priority": {"mittel"}, "reason": {"it was wrong"}}
		for k, val := range m {
			v.Set(k, val)
		}
		return v
	}
	if resp := rqPost(t, e, e.Member, id, "/correct", form(map[string]string{"title": "Hijack"})); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member correcting a foreign request = %d", resp.StatusCode)
	}
	if resp := rqPost(t, e, e.Guest, id, "/correct", form(map[string]string{"title": "Hijack"})); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest correcting = %d", resp.StatusCode)
	}
	for name, change := range map[string]map[string]string{
		"no reason": {"title": "New", "reason": " "}, "no change": {}, "empty title": {"title": " "},
		"bad type": {"type": "epic"},
	} {
		if resp := rqPost(t, e, e.Lead, id, "/correct", form(change)); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if got, _ := e.St.RequestByID(id); got.Request.Title != "Old title" {
		t.Fatal("a refused correction changed the request")
	}
	resp := rqPost(t, e, e.Lead, id, "/correct", form(map[string]string{"title": "New title", "description": "Para one\r\n\r\nPara two", "priority": "hoch", "type": "feature"}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("lead correcting = %d", resp.StatusCode)
	}
	got, _ := e.St.RequestByID(id)
	if got.Request.Title != "New title" || got.Request.Priority != "hoch" || got.Request.Type != "feature" || got.Request.Description != "Para one\n\nPara two" {
		t.Errorf("correction not stored: %+v", got.Request)
	}
	last := got.Activity[len(got.Activity)-1]
	if last.Kind != "request.corrected" || !strings.Contains(last.Data, "it was wrong") {
		t.Errorf("the reason is not in the activity: %+v", last)
	}
	_, page := fetchPage(t, e.Lead, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(page, "Saved.") || !strings.Contains(page, "New title") || !strings.Contains(page, "Corrected") {
		t.Error("result line, new title or history entry missing")
	}
	// The author may correct their own request as a member.
	own := rqMake(t, e.St, rqSpec{Title: "Anna's", Person: "anna"})
	if resp := rqPost(t, e, e.Member, own.Request.ID, "/correct", form(map[string]string{"title": "Anna's v2"})); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("author correcting own request = %d", resp.StatusCode)
	}
}

func TestRequestCompleteNeedsProofAndNoOpenCriterionAndDropNeedsAReason(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Finish me", Criteria: []string{"only one"}})
	id := d.Request.ID
	_, page := fetchPage(t, e.Lead, e.Base+rqPath(id, ""))
	if strings.Contains(page, rqPath(id, "/complete")) {
		t.Error("the complete form is offered while a criterion is open")
	}
	resp := rqPost(t, e, e.Lead, id, "/complete", url.Values{"evidence_kind": {"commit"}, "evidence_ref": {"abc123"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "fail=open_criteria") {
		t.Fatalf("completing with an open criterion = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, failed := fetchPage(t, e.Lead, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(failed, "Resolve the open criteria first.") {
		t.Error("no failure line for the open criterion")
	}
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "t", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	if resp := rqPost(t, e, e.Member, id, "/complete", url.Values{"evidence_kind": {"commit"}, "evidence_ref": {"abc"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member completing a foreign request = %d", resp.StatusCode)
	}
	if resp := rqPost(t, e, e.Lead, id, "/complete", url.Values{"evidence_kind": {"commit"}, "evidence_ref": {" "}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("completing without proof = %d", resp.StatusCode)
	}
	resp = rqPost(t, e, e.Lead, id, "/complete", url.Values{"evidence_kind": {"commit"}, "evidence_ref": {"abc123"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("completing = %d", resp.StatusCode)
	}
	got, _ := e.St.RequestByID(id)
	if got.Request.State != "done" {
		t.Errorf("state = %s", got.Request.State)
	}
	_, done := fetchPage(t, e.Lead, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(done, "Completed.") || strings.Contains(done, rqPath(id, "/drop")) || strings.Contains(done, rqPath(id, "/criteria\"")) {
		t.Error("completed page lacks the result or still offers closing actions")
	}
	if resp := rqPost(t, e, e.Lead, id, "/complete", url.Values{"evidence_kind": {"commit"}, "evidence_ref": {"again"}}); resp.StatusCode != http.StatusConflict {
		t.Errorf("completing twice = %d", resp.StatusCode)
	}

	drop := rqMake(t, e.St, rqSpec{Title: "Drop me"})
	if resp := rqPost(t, e, e.Lead, drop.Request.ID, "/drop", url.Values{"reason": {"  "}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("dropping without a reason = %d", resp.StatusCode)
	}
	if resp := rqPost(t, e, e.Guest, drop.Request.ID, "/drop", url.Values{"reason": {"nope"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest dropping = %d", resp.StatusCode)
	}
	resp = rqPost(t, e, e.Lead, drop.Request.ID, "/drop", url.Values{"reason": {"no longer wanted"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("dropping = %d", resp.StatusCode)
	}
	if got, _ := e.St.RequestByID(drop.Request.ID); got.Request.State != "dropped" {
		t.Error("request not dropped")
	}
	_, page = fetchPage(t, e.Lead, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(page, "Dropped.") || !strings.Contains(page, "no longer wanted") {
		t.Error("result line or reason missing after dropping")
	}
}

func TestRequestActionsOnHiddenRequestsAnswerLikeUnknownOnes(t *testing.T) {
	e := ovEnv(t)
	hidden := rqMake(t, e.St, rqSpec{Title: "SECRET-HIDDEN", Project: shellHiddenProject, Criteria: []string{"c"}})
	missing := hidden.Request.ID + 1000
	actions := map[string]url.Values{
		"/criteria": {"description": {"x"}},
		"/criteria/" + strconv.FormatInt(hidden.Criteria[0].ID, 10): {"evidence_kind": {"test"}, "evidence_ref": {"x"}},
		"/correct":  {"title": {"x"}, "description": {"x"}, "type": {"bug"}, "reason": {"x"}},
		"/complete": {"evidence_kind": {"test"}, "evidence_ref": {"x"}},
		"/drop":     {"reason": {"x"}},
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "lead": e.Lead} {
		for rest, form := range actions {
			r1 := rqPost(t, e, c, hidden.Request.ID, rest, cloneForm(form))
			b1 := body(t, r1)
			miss := rest
			if strings.HasPrefix(rest, "/criteria/") {
				miss = "/criteria/999999"
			}
			r2 := rqPost(t, e, c, missing, miss, cloneForm(form))
			b2 := body(t, r2)
			if r1.StatusCode != http.StatusNotFound || r1.StatusCode != r2.StatusCode || b1 != b2 {
				t.Errorf("%s %s: hidden %d %q vs unknown %d %q", name, rest, r1.StatusCode, b1, r2.StatusCode, b2)
			}
		}
	}
	got, _ := e.St.RequestByID(hidden.Request.ID)
	if got.Request.State != "open" || got.Request.Title != "SECRET-HIDDEN" || got.Criteria[0].State != "open" || len(got.Criteria) != 1 {
		t.Errorf("a refused request changed the hidden request: %+v", got)
	}
}

func TestRequestActionsNeedCSRFOriginAndAnInteractiveSession(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Guarded"})
	target := e.Base + rqPath(d.Request.ID, "/drop")
	for name, form := range map[string]url.Values{"missing": {"reason": {"x"}}, "wrong": {"csrf_token": {"nope"}, "reason": {"x"}}} {
		if resp := sameOriginPostForm(t, e.Owner, target, form); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s CSRF token: %d", name, resp.StatusCode)
		}
	}
	token := renderedCSRFToken(t, e.Owner, e.Base+"/ui/requests")
	form := url.Values{"csrf_token": {token}, "reason": {"x"}}
	req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := e.Owner.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin accepted: %v %v", resp, err)
	}
	req, _ = http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", e.Base)
	req.Header.Set("Authorization", "Bearer not-a-session")
	if resp, err := (&http.Client{}).Do(req); err != nil || resp.StatusCode == http.StatusSeeOther {
		t.Errorf("a request without the browser session was accepted: %v %v", resp, err)
	}
	if got, _ := e.St.RequestByID(d.Request.ID); got.Request.State != "open" {
		t.Fatal("a refused request dropped the request")
	}
	if resp := sameOriginPostForm(t, e.Owner, target, form); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("the interactive session with a valid token: %d", resp.StatusCode)
	}
	if got, _ := e.St.RequestByID(d.Request.ID); got.Request.State != "dropped" {
		t.Error("the interactive session could not drop")
	}
}

func TestRequestDetailOffersOnlyTheActionsTheViewerMayTake(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Who may", Criteria: []string{"c1"}})
	id := d.Request.ID
	for name, want := range map[string]struct {
		c                      *http.Client
		resolve, edit, closing bool
	}{
		"owner": {e.Owner, true, true, true}, "lead": {e.Lead, true, true, true},
		"member": {e.Member, true, false, false}, "guest": {e.Guest, false, false, false},
	} {
		_, page := fetchPage(t, want.c, e.Base+rqPath(id, ""))
		has := func(rest string) bool { return strings.Contains(page, `action="`+rqPath(id, rest)) }
		if has("/criteria/"+strconv.FormatInt(d.Criteria[0].ID, 10)) != want.resolve || has("/correct") != want.edit || has("/drop") != want.closing || has("/criteria\"") != want.edit {
			t.Errorf("%s sees the wrong actions", name)
		}
	}
	// Everything stays in the catalog, no scripts or inline styles.
	_, page := fetchPage(t, e.Owner, e.Base+rqPath(id, "?edit=1"))
	if !strings.Contains(page, `id="edit" open`) {
		t.Error("?edit=1 does not open the form")
	}
}

func rqBody(t *testing.T, e shellEnv, id int64) requestdomain.Request {
	t.Helper()
	got, err := e.St.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return got.Request
}

func rqCorrectForm(title, desc, reason string) url.Values {
	return url.Values{"title": {title}, "description": {desc}, "type": {"bug"}, "priority": {"mittel"}, "reason": {reason}}
}

func TestRequestCorrectAcceptsALongDescription(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Long", Desc: "short", Priority: "mittel", Type: "bug"})
	long := strings.Repeat("Zeile mit Umlauten äöü\r\n", 1500)
	resp := rqPost(t, e, e.Lead, d.Request.ID, "/correct", rqCorrectForm("Long", long, "more detail"))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a description over 4 KiB = %d", resp.StatusCode)
	}
	if got := rqBody(t, e, d.Request.ID).Description; got != strings.ReplaceAll(long, "\r\n", "\n") {
		t.Errorf("long description not stored (%d bytes)", len(got))
	}
	tooLong := strings.Repeat("x", maxTextBytes+1)
	if resp := rqPost(t, e, e.Lead, d.Request.ID, "/correct", rqCorrectForm("Long", tooLong, "too much")); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a description over the limit = %d", resp.StatusCode)
	}
}

func TestRequestCorrectMayEmptyTheDescriptionAndKeepsIndentation(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Keep", Desc: "old", Priority: "mittel", Type: "bug"})
	id := d.Request.ID
	code := "    indented code\n    more\n"
	if resp := rqPost(t, e, e.Lead, id, "/correct", rqCorrectForm("Keep", code, "add code")); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correct with code = %d", resp.StatusCode)
	}
	if got := rqBody(t, e, id).Description; got != code {
		t.Errorf("description was trimmed: %q", got)
	}
	// Only the description changed: the activity names no other field.
	got, _ := e.St.RequestByID(id)
	if last := got.Activity[len(got.Activity)-1]; !strings.HasPrefix(last.Data, "description — ") {
		t.Errorf("activity names more than the changed field: %q", last.Data)
	}
	// An unchanged form submit from a browser (CRLF) is no change.
	if resp := rqPost(t, e, e.Lead, id, "/correct", rqCorrectForm("Keep", strings.ReplaceAll(code, "\n", "\r\n"), "again")); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an unchanged description counted as a change: %d", resp.StatusCode)
	}
	if resp := rqPost(t, e, e.Lead, id, "/correct", rqCorrectForm("Keep", "", "no text needed")); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("emptying the description = %d", resp.StatusCode)
	}
	if got := rqBody(t, e, id).Description; got != "" {
		t.Errorf("description not emptied: %q", got)
	}
	_, page := fetchPage(t, e.Lead, e.Base+rqPath(id, "?edit=1"))
	if strings.Contains(page, `<textarea class="clay-field kn-textarea" name="description" required`) {
		t.Error("the description field is still required")
	}
}

func TestRequestCorrectChecksLengthsOnlyOnChangedFields(t *testing.T) {
	e := ovEnv(t)
	legacyTitle := strings.Repeat("t", maxTitleBytes+100)
	legacyPrio := strings.Repeat("p", maxPriority+10)
	d := rqMake(t, e.St, rqSpec{Title: legacyTitle, Desc: "old", Priority: legacyPrio, Type: "bug"})
	id := d.Request.ID
	form := url.Values{"title": {legacyTitle}, "description": {"new"}, "type": {"bug"}, "priority": {legacyPrio}, "reason": {"fix text"}}
	if resp := rqPost(t, e, e.Lead, id, "/correct", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting around legacy-long fields = %d", resp.StatusCode)
	}
	if got := rqBody(t, e, id); got.Description != "new" || got.Title != legacyTitle || got.Priority != legacyPrio {
		t.Errorf("legacy fields were touched: %d %d", len(got.Title), len(got.Priority))
	}
	form.Set("title", strings.Repeat("u", maxTitleBytes+1))
	form.Set("description", "newer")
	if resp := rqPost(t, e, e.Lead, id, "/correct", form); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a changed over-long title = %d", resp.StatusCode)
	}
}

func TestRequestCorrectWorksOnADoneRequest(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Finished", Desc: "old", Priority: "mittel", Type: "bug"})
	if err := e.St.CompleteRequest(d.Request.ID, requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	resp := rqPost(t, e, e.Lead, d.Request.ID, "/correct", rqCorrectForm("Finished", "better words", "typo"))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a done request = %d", resp.StatusCode)
	}
	if got := rqBody(t, e, d.Request.ID); got.Description != "better words" || got.State != "done" {
		t.Errorf("done request after correction: %+v", got)
	}
}

func rqActivityCount(page, label string) int {
	return strings.Count(page, `<span class="rq-act">`+label+`</span>`)
}

func TestRequestHistoryDropsRelationEntriesAboutHiddenCounterparts(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Linked"})
	visible := rqMake(t, e.St, rqSpec{Title: "Visible partner"})
	secret := rqMake(t, e.St, rqSpec{Title: "SECRET-PARTNER", Project: shellHiddenProject})
	hiddenKnowledge := knInsert(t, e.St, store.Knowledge{Title: "SECRET-KNOW", Body: "x", Scope: scope.Axes{Project: shellHiddenProject}, Confidence: "verified", Status: "active"})
	for _, rel := range []requestdomain.Relation{{Kind: "related", OtherRequestID: visible.Request.ID}, {Kind: "blocks", OtherRequestID: secret.Request.ID},
		{Kind: "knowledge", KnowledgeID: hiddenKnowledge}} {
		if _, err := e.St.AddRequestRelation(d.Request.ID, rel, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := e.St.RequestByID(d.Request.ID)
	for _, rel := range got.Relations {
		if rel.Kind == "blocks" {
			if err := e.St.RemoveRequestRelation(rel.ID, "alice", "wrong way round"); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, member := e.get(t, e.Member, rqPath(d.Request.ID, ""))
	if n := rqActivityCount(member, "Relation added"); n != 1 {
		t.Errorf("member sees %d relation-added entries, want 1", n)
	}
	if n := rqActivityCount(member, "Relation removed"); n != 0 {
		t.Errorf("member sees %d relation-removed entries about a hidden request, want 0", n)
	}
	for _, leak := range []string{"SECRET", "REQ-" + strconv.FormatInt(secret.Request.ID, 10)} {
		if strings.Contains(member, leak) {
			t.Errorf("member detail leaks %q", leak)
		}
	}
	_, owner := e.get(t, e.Owner, rqPath(d.Request.ID, ""))
	if n := rqActivityCount(owner, "Relation removed"); n != 1 {
		t.Errorf("owner sees %d relation-removed entries, want 1", n)
	}
}

func TestRequestDetailListsOnlyDiscussionsInReadableRooms(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Discussed"})
	for _, project := range []string{shellProject, shellHiddenProject} {
		if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:rooms-" + project, PrincipalID: "person:1", Person: "alice", Provider: "claude",
			RoomKey: store.RoomKeyForProject(project)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:anna-room", PrincipalID: "person:2", Person: "anna", Provider: "claude",
		RoomKey: store.RoomKeyForProject(shellProject)}); err != nil {
		t.Fatal(err)
	}
	other := rqMake(t, e.St, rqSpec{Title: "Hidden anchor", Project: shellHiddenProject})
	for title, project := range map[string]string{"Open topic": shellProject, "SECRET-THREAD": shellHiddenProject} {
		anchor := d.Request.HumanID()
		if project == shellHiddenProject {
			anchor = other.Request.HumanID()
		}
		owner := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice"}, "claude:rooms-"+project)
		id, err := owner.CreateTaskThreadInRoom(store.RoomKeyForProject(project), title, "why?", anchor)
		if err != nil {
			t.Fatal(err)
		}
		if project == shellHiddenProject {
			if err := e.St.LinkThread(store.ThreadLink{ThreadID: id, Kind: "request", ID: d.Request.HumanID()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "lead": e.Lead} {
		_, page := e.get(t, c, rqPath(d.Request.ID, ""))
		if strings.Contains(page, "SECRET") || strings.Contains(page, shellHiddenProject) {
			t.Errorf("%s sees a thread of a room it cannot read", name)
		}
	}
	_, member := e.get(t, e.Member, rqPath(d.Request.ID, ""))
	if !strings.Contains(member, "Open topic") {
		t.Error("member lacks the discussion in the readable room")
	}
}

func TestRequestPriorityMenuDoesNotDependOnThePage(t *testing.T) {
	e := seedSessions(t)
	rqMake(t, e.St, rqSpec{Title: "Oldest", Priority: "selten"})
	for i := 0; i < requestsShown+3; i++ {
		rqMake(t, e.St, rqSpec{Title: "Filler " + strconv.Itoa(i), Priority: "mittel"})
	}
	rqMake(t, e.St, rqSpec{Title: "Hidden prio", Priority: "GEHEIM", Project: shellHiddenProject})
	for _, path := range []string{"/ui/requests", "/ui/requests?priority=mittel"} {
		_, page := e.get(t, e.Member, path)
		if !strings.Contains(page, `value="selten"`) || !strings.Contains(page, `value="mittel"`) {
			t.Errorf("%s: the priority menu changes with the page", path)
		}
		if strings.Contains(page, "GEHEIM") {
			t.Errorf("%s: the menu offers a priority of a hidden project", path)
		}
	}
}

func TestRequestHistoryHasNoOracleForSameKindSameSecondRelations(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Linked"})
	visible := rqMake(t, e.St, rqSpec{Title: "Visible partner"})
	secret := rqMake(t, e.St, rqSpec{Title: "SECRET-PARTNER", Project: shellHiddenProject})
	for _, id := range []int64{visible.Request.ID, secret.Request.ID, visible.Request.ID} {
		if _, err := e.St.AddRequestRelation(d.Request.ID, requestdomain.Relation{Kind: "related", OtherRequestID: id}, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	// Same second for every entry, as in a fast import.
	if _, err := e.St.DB().Exec(`UPDATE request_relations SET created_at='2030-01-01T00:00:00.000Z' WHERE request_id=?`, d.Request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.DB().Exec(`UPDATE request_activity SET created_at='2030-01-01T00:00:00.000Z' WHERE request_id=? AND kind='relation.added'`, d.Request.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.St.RequestByID(d.Request.ID)
	for _, rel := range got.Relations {
		if rel.OtherRequestID == secret.Request.ID {
			if err := e.St.RemoveRequestRelation(rel.ID, "alice", "wrong way round"); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, member := e.get(t, e.Member, rqPath(d.Request.ID, ""))
	if n := rqActivityCount(member, "Relation added"); n != 2 {
		t.Errorf("member sees %d relation-added entries, want 2 (the hidden one is gone)", n)
	}
	_, owner := e.get(t, e.Owner, rqPath(d.Request.ID, ""))
	if n := rqActivityCount(owner, "Relation added"); n != 3 {
		t.Errorf("owner sees %d relation-added entries, want 3", n)
	}
	if n := rqActivityCount(owner, "Relation removed"); n != 1 {
		t.Errorf("owner sees %d relation-removed entries, want 1", n)
	}
}

func TestRequestHistoryLegacyRelationEntriesPairOncePerRelation(t *testing.T) {
	e := seedSessions(t)
	d := rqMake(t, e.St, rqSpec{Title: "Linked"})
	visible := rqMake(t, e.St, rqSpec{Title: "Visible partner"})
	secret := rqMake(t, e.St, rqSpec{Title: "SECRET-PARTNER", Project: shellHiddenProject})
	for _, id := range []int64{visible.Request.ID, secret.Request.ID} {
		if _, err := e.St.AddRequestRelation(d.Request.ID, requestdomain.Relation{Kind: "related", OtherRequestID: id}, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	// Old entries carried only the kind.
	if _, err := e.St.DB().Exec(`UPDATE request_activity SET data='related', created_at='2030-01-01T00:00:00.000Z' WHERE request_id=? AND kind='relation.added'`, d.Request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.DB().Exec(`UPDATE request_relations SET created_at='2030-01-01T00:00:00.000Z' WHERE request_id=?`, d.Request.ID); err != nil {
		t.Fatal(err)
	}
	_, member := e.get(t, e.Member, rqPath(d.Request.ID, ""))
	if n := rqActivityCount(member, "Relation added"); n != 1 {
		t.Errorf("member sees %d legacy relation-added entries, want 1", n)
	}
	_, owner := e.get(t, e.Owner, rqPath(d.Request.ID, ""))
	if n := rqActivityCount(owner, "Relation added"); n != 2 {
		t.Errorf("owner sees %d legacy relation-added entries, want 2", n)
	}
}

func TestRequestCorrectTooLargeIs413WithADesignedPage(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Big", Desc: "old", Priority: "mittel", Type: "bug"})
	resp := rqPost(t, e, e.Lead, d.Request.ID, "/correct", rqCorrectForm("Big", strings.Repeat("x", requestCorrectForm+1), "too much"))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversize correction = %d, want 413", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), msg("requests.toolarge.title")) || !strings.Contains(string(body), "<nav") {
		t.Errorf("413 answer is not the designed page: %.200s", body)
	}
	if got := rqBody(t, e, d.Request.ID).Description; got != "old" {
		t.Errorf("description changed to %d bytes", len(got))
	}
}
