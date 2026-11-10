package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Comments and reactions on an entry of knowledge (decision 11 of the UI review):
// the page has the same area as an issue, and the rights are those of a comment
// on an issue: members write, a guest reads and sees numbers only, an entry the
// viewer may not read is as unknown as one that does not exist.
func TestKnowledgeEntryTakesCommentsAndReactionsWithTheRightsOfAnIssue(t *testing.T) {
	e := ovEnv(t)
	id := knInsert(t, e.St, store.Knowledge{Title: "Lock order", Body: "Take the store lock first."})
	page := e.Base + "/ui/x/shell/knowledge/" + itoa(id)
	post := func(c *http.Client, kid int64, body string) *http.Response {
		token := renderedCSRFToken(t, c, e.Base+"/ui/overview")
		return sameOriginPostForm(t, c, e.Base+"/ui/knowledge/"+itoa(kid)+"/comment", url.Values{"csrf_token": {token}, "body": {body}})
	}

	_, before := fetchPage(t, e.Member, page)
	if !strings.Contains(before, `action="/ui/knowledge/`+itoa(id)+`/comment"`) || !strings.Contains(before, `class="react add"`) {
		t.Fatalf("a member sees no box to comment or no button to react:\n%s", before)
	}
	resp := post(e.Member, id, "Does this hold for **nested** locks?")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/knowledge/"+itoa(id)+"?comment=added#comment-") {
		t.Fatalf("member comment -> %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// The comment is on the page, with the count; a reaction counts under the text and under the comment.
	status, _, row := reactPost(t, e, e.Owner, url.Values{"kind": {"knowledge"}, "id": {itoa(id)}, "reaction": {"heart"}})
	if status != http.StatusOK || !strings.Contains(row, `</span>1</button>`) {
		t.Fatalf("reaction on the entry -> %d %s", status, row)
	}
	_, after := fetchPage(t, e.Guest, page)
	for _, want := range []string{"Does this hold for", "Comments", `id="comments"`} {
		if !strings.Contains(after, want) {
			t.Errorf("the guest does not see %q", want)
		}
	}
	if strings.Contains(after, `action="/ui/knowledge/`+itoa(id)+`/comment"`) || strings.Contains(after, `class="react add"`) {
		t.Error("a guest is offered to comment or to react")
	}
	if !strings.Contains(after, `class="react"`) {
		t.Error("a guest does not see the numbers of the reactions")
	}
	// A guest writes nothing, whatever the form says.
	resp = post(e.Guest, id, "guest comment")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest comment -> %d, want 403", resp.StatusCode)
	}
	if status, _, _ := reactPost(t, e, e.Guest, url.Values{"kind": {"knowledge"}, "id": {itoa(id)}, "reaction": {"heart"}}); status != http.StatusForbidden {
		t.Errorf("guest reaction -> %d, want 403", status)
	}
	// An empty comment goes back to the entry with the message over the form, like on an issue (formfail.go).
	resp = post(asBrowser(e.Member), id, "   ")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/knowledge/"+itoa(id)) || strings.Contains(resp.Header.Get("Location"), "?") {
		t.Errorf("empty comment -> %d %s", resp.StatusCode, resp.Header.Get("Location"))
	} else if _, failed := followRefusal(t, e.Member, e.Base, resp); !strings.Contains(failed, "A comment needs text.") {
		t.Errorf("the refusal is not shown over the form: %.300s", failed)
	}
}

// An entry of a project the viewer has no role in takes neither a comment nor a
// reaction, and answers as one that does not exist; an entry without a project
// has no place for rights and takes none.
func TestKnowledgeCommentsNeverReachAnEntryTheViewerMayNotRead(t *testing.T) {
	e := ovEnv(t)
	secret := knInsert(t, e.St, store.Knowledge{Title: "SECRET entry", Scope: scopeOf(shellHiddenProject)})
	// knInsert would put an entry without a project into the shell project.
	global, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: "Global entry", Body: "body", Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	token := renderedCSRFToken(t, e.Member, e.Base+"/ui/overview")
	for name, id := range map[string]int64{"hidden project": secret, "no project": global} {
		resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/knowledge/"+itoa(id)+"/comment", url.Values{"csrf_token": {token}, "body": {"hello"}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: comment -> %d, want 404", name, resp.StatusCode)
		}
		if status, _, _ := reactPost(t, e, e.Member, url.Values{"kind": {"knowledge"}, "id": {itoa(id)}, "reaction": {"heart"}}); status != http.StatusNotFound {
			t.Errorf("%s: reaction -> %d, want 404", name, status)
		}
	}
}
