package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var modeValueRE = regexp.MustCompile(`name="mode" value="([a-z]+)" data-mode-input`)

// The status row of a pain lists the states in their one order, the state the pain is in is the entry that
// only comments (and carries the mark), the old "Why" field is gone and what a state needs is marked.
func TestPainComposerListsTheStatesInOrderWithTheCurrentOneMarked(t *testing.T) {
	e := ovEnv(t)
	d, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Order of states", Body: "b", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(d.Pain.ID, 10)
	order := func(page string) []string {
		var out []string
		for _, m := range modeValueRE.FindAllStringSubmatch(page, -1) {
			out = append(out, m[1])
		}
		return out
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pains/"+id)
	if got := strings.Join(order(page), " "); got != "comment mitigated accepted resolved obsolete" {
		t.Errorf("an open pain: states %q", got)
	}
	if !strings.Contains(page, `<label class="cx-opt cur">`) || !strings.Contains(page, `<span class="cdot" aria-hidden="true"></span>Open<span class="sr"> current</span>`) {
		t.Error("the state the pain is in is not marked")
	}
	for _, gone := range []string{`name="why"`, `ddl`} {
		if strings.Contains(page, gone) {
			t.Errorf("the composer still has %q", gone)
		}
	}
	for _, want := range []string{`name="remaining"`, `name="evidence_ref"`, `class="req"`, `data-need`, `&#34;need&#34;:true`} {
		if !strings.Contains(page, want) {
			t.Errorf("what a state needs is not marked: lacks %q", want)
		}
	}
	// Mitigated: the current state moves in the row, the open one is a way back.
	resp := tpPost(t, e, e.Member, "/ui/pains/"+id+"/state", url.Values{"mode": {"mitigated"}, "remaining": {"Rest"}, "evidence_kind": {"commit"}, "evidence_ref": {"3f9a2c1"}})
	resp.Body.Close()
	_, page = fetchPage(t, e.Member, e.Base+"/ui/x/shell/pains/"+id)
	if got := strings.Join(order(page), " "); got != "active comment accepted resolved obsolete" {
		t.Errorf("a mitigated pain: states %q", got)
	}
}

// A proposal of the inbox is a line with its letter in front; a proposal without a letter is only text.
func TestInboxOptionHasALeadAndABodyOnlyWhenItCarriesALetter(t *testing.T) {
	for _, c := range []struct{ key, text, lead, body string }{
		{"A", "A: Freigabe je Eintrag", "A", "Freigabe je Eintrag"},
		{"B", "B. Nur die Maschine", "B", "Nur die Maschine"},
		{"C", "Wie A", "C", "Wie A"},
		{"1", "Getrennt", "1", "Getrennt"},
		{"yes", "Ja", "", "Ja"},
		{"", "Nur Text", "", "Nur Text"},
	} {
		o := inboxOption{Key: c.key, Text: c.text}
		if o.Lead() != c.lead || o.Body() != c.body {
			t.Errorf("%+v: lead %q body %q, want %q %q", c, o.Lead(), o.Body(), c.lead, c.body)
		}
	}
}

// "Mark as done" is the composer: the words are the note, and a report that is refused goes back to the page of
// the task (so the note is still there to be sent again), not to the inbox.
func TestTaskReportIsTheComposerAndARefusalGoesBackToTheTask(t *testing.T) {
	e, task, _ := taskFixture(t)
	path := "/ui/x/shell/tasks/" + itoa(task.ID)
	_, page := fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "task page", page, `class="cmt cx-cmt"`, `name="note"`, `data-wtab="p"`, `data-submit>Mark as done</button>`, `name="back" value="task"`, "A note for the agent")
	if strings.Contains(page, `tdone`) {
		t.Error("the one-line form is still there")
	}
	// A browser navigates with the form: the refusal goes back to the page it came from, with the reason over it
	// and the secret-like note not written onto the page again.
	navPost := func(back string) *http.Response {
		form := url.Values{"id": {itoa(task.ID)}, "back": {back}, "note": {"token ghp_" + strings.Repeat("Ab1", 5)}}
		form.Set("csrf_token", renderedCSRFToken(t, e.Member, e.Base+"/ui/overview"))
		req, err := http.NewRequest(http.MethodPost, e.Base+"/ui/tasks/done", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", e.Base)
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		resp, err := e.Member.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	resp := navPost("task")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != path {
		t.Fatalf("a refused report goes to %d %q, want 303 to the task page", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, refused := followRefusal(t, e.Member, e.Base, resp)
	if !strings.Contains(refused, `role="alert"`) {
		t.Error("the task page does not say why the report was refused")
	}
	if strings.Contains(refused, "ghp_Ab1") {
		t.Error("the secret-like note is written onto the page again")
	}
	// From the inbox it stays in the inbox.
	if resp := navPost("inbox"); !strings.HasPrefix(resp.Header.Get("Location"), "/ui/inbox") {
		t.Errorf("a refused report from the inbox goes to %q", resp.Header.Get("Location"))
	}
}

// The body of a knowledge entry is edited in the composer: write and preview, the toolbar, the words required.
func TestKnowledgeEditBodyIsTheComposer(t *testing.T) {
	e := ovEnv(t)
	id := knInsert(t, e.St, store.Knowledge{Title: "Fix me", Body: "Old *text*", Confidence: "staged"})
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/knowledge/"+strconv.FormatInt(id, 10)+"?edit=1")
	mustContain(t, "edit form", page, `class="cx tall"`, `data-wtab="p"`, `data-tool="bold"`, `name="body" required`, "Old *text*")
	if strings.Contains(page, `<textarea name="body" required rows="12">`) {
		t.Error("the bare text field is still there")
	}
}

// A requirement of an issue has its action at the right of its row, in a cell of its own; the field for the proof
// is part of the card that opens with it.
func TestRequirementActionStandsInItsOwnCellOfTheRow(t *testing.T) {
	e := ovEnv(t)
	open := rqMake(t, e.St, rqSpec{Title: "Still open", Criteria: []string{"One"}})
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(open.Request.ID))
	body := strings.Index(page, `<div class="tx">One</div>`)
	resolve := strings.Index(page, `<details class="resolve"><summary class="gbtn sm quiet">Mark as met</summary>`)
	if body < 0 || resolve < body {
		t.Fatalf("the action is not after the text of the requirement (text %d, action %d)", body, resolve)
	}
	row := page[body+len(`<div class="tx">One</div>`) : resolve]
	if !strings.HasSuffix(strings.TrimSpace(row), "</div>") {
		t.Error("the action is inside the body of the requirement, not in a cell of the row")
	}
	for _, want := range []string{`class="rf-b"`, `name="evidence_ref" required`, `name="state" value="met"`, `name="state" value="waived"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the proof card lacks %q", want)
		}
	}
}
