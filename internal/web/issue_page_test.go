package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

func TestIssueHistoryRunsOldestFirstAndRequirementsLeadToTheirEntry(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Ordered", Desc: "Text.", Criteria: []string{"First requirement", "Second requirement"}})
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "a41c7e2", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.AddCriterion(d.Request.ID, "Third requirement", "alice"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(d.Request.ID))
	created := strings.Index(page, `data-act="request.created"`)
	met := strings.Index(page, `data-act="criterion.met"`)
	added := strings.Index(page, `data-act="criterion.added"`)
	if created < 0 || met < 0 || added < 0 || !(created < met && met < added) {
		t.Errorf("the history is not oldest first: created %d, met %d, added %d", created, met, added)
	}
	// The requirements sit in the first comment, above the history.
	box := strings.Index(page, `class="reqs"`)
	tl := strings.Index(page, `class="tlx"`)
	if box < 0 || tl < 0 || box > tl {
		t.Errorf("requirements are not inside the first comment: box %d, history %d", box, tl)
	}
	m := regexp.MustCompile(`id="criterion-1"[^>]*>.*?class="jump" href="#(event-\d+)"`).FindStringSubmatch(strings.ReplaceAll(page, "\n", " "))
	if m == nil {
		i := strings.Index(page, `id="criterion-1"`)
		t.Fatalf("requirement 1 has no link to the entry that met it: %s", page[i:i+1500])
	}
	if !strings.Contains(page, `id="`+m[1]+`"`) {
		t.Errorf("the entry %s is not in the history", m[1])
	}
	if !strings.Contains(page, `href="#criterion-1"`) {
		t.Error("the entry does not lead back to its requirement")
	}
	if strings.Contains(page, `id="criterion-2"`) && strings.Contains(strings.Split(page, `id="criterion-2"`)[1][:400], `class="jump"`) {
		t.Error("an open requirement links to an entry")
	}
}

func TestIssueComposerClosesOrDropsAndOnlyForThoseWhoMay(t *testing.T) {
	e := ovEnv(t)
	done := rqMake(t, e.St, rqSpec{Title: "To finish", Criteria: []string{"One"}})
	if err := e.St.SetCriterionState(done.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "a41c7e2", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	open := rqMake(t, e.St, rqSpec{Title: "Still open", Criteria: []string{"One"}})
	base := e.Base + "/ui/x/shell/issues/"
	_, page := fetchPage(t, e.Owner, base+itoa(done.Request.ID))
	for _, want := range []string{`action="/ui/requests/` + itoa(done.Request.ID) + `/close"`, "Close issue", "Close with comment", `name="reason"`, `name="evidence_ref"`, "Drop issue"} {
		if !strings.Contains(page, want) {
			t.Errorf("composer lacks %q", want)
		}
	}
	// While a requirement is open there is nothing to complete, only to drop.
	_, pageOpen := fetchPage(t, e.Owner, base+itoa(open.Request.ID))
	if strings.Contains(pageOpen, `data-val="complete"`) || !strings.Contains(pageOpen, `data-val="drop"`) {
		t.Error("an issue with an open requirement offers to close")
	}
	_, guest := fetchPage(t, e.Guest, base+itoa(done.Request.ID))
	if strings.Contains(guest, "/close\"") {
		t.Error("a guest gets the composer")
	}
	post := func(id int64, form url.Values) *http.Response {
		form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
		return sameOriginPostForm(t, e.Owner, e.Base+"/ui/requests/"+itoa(id)+"/close", form)
	}
	resp := post(done.Request.ID, url.Values{"mode": {"complete"}, "reason": {"Shipped with the release"}, "evidence_kind": {"url"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("complete -> %d", resp.StatusCode)
	}
	if d, err := e.St.RequestByID(done.Request.ID); err != nil || d.Request.State != "done" {
		t.Errorf("not completed: %+v %v", d.Request.State, err)
	}
	resp = post(open.Request.ID, url.Values{"mode": {"drop"}, "reason": {"Not needed any more"}})
	resp.Body.Close()
	if d, err := e.St.RequestByID(open.Request.ID); err != nil || d.Request.State != "dropped" {
		t.Errorf("not dropped: %+v %v", d.Request.State, err)
	}
	resp = post(open.Request.ID, url.Values{"mode": {"nonsense"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown mode -> %d, want 400", resp.StatusCode)
	}
}
