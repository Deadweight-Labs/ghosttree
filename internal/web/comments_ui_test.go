package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func postForm(t *testing.T, e shellEnv, c *http.Client, path string, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, c, e.Base+path, form)
	resp.Body.Close()
	return resp
}

func commentsOf(t *testing.T, e shellEnv, kind string, id int64) []store.Comment {
	t.Helper()
	list, err := e.St.Access(store.Principal{ID: "person:1"}).Comments(kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestIssueComposerWritesACommentAndOnlyWhoMayComment(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Talk about it"})
	id := d.Request.ID
	base := e.Base + "/ui/x/shell/issues/" + itoa(id)
	_, page := fetchPage(t, e.Owner, base)
	for _, want := range []string{`data-mode="comment"`, "Comment", "Close issue"} {
		if !strings.Contains(page, want) {
			t.Errorf("composer lacks %q", want)
		}
	}
	for name, c := range map[string]*http.Client{"guest": e.Guest} {
		if _, p := fetchPage(t, c, base); strings.Contains(p, "/close\"") {
			t.Errorf("%s gets the composer", name)
		}
		resp := postForm(t, e, c, "/ui/requests/"+itoa(id)+"/close", url.Values{"mode": {"comment"}, "reason": {"Let me in"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s comments -> %d, want 403", name, resp.StatusCode)
		}
	}
	// A member who may not edit the issue can still comment, but not close it.
	_, mp := fetchPage(t, e.Member, base)
	if !strings.Contains(mp, `action="/ui/requests/`+itoa(id)+`/close"`) {
		t.Error("a member gets no composer")
	}
	resp := postForm(t, e, e.Member, "/ui/requests/"+itoa(id)+"/close", url.Values{"mode": {"comment"}, "reason": {"I have a **thought**."}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "#comment-") {
		t.Fatalf("comment -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	list := commentsOf(t, e, store.CommentOnRequest, id)
	if len(list) != 1 || list[0].Person != "anna" || list[0].Via != "web" {
		t.Fatalf("comments: %+v", list)
	}
	if d, _ := e.St.RequestByID(id); d.Request.State != "open" {
		t.Errorf("a comment changed the state: %s", d.Request.State)
	}
	_, after := fetchPage(t, e.Owner, base+"?comment=added")
	anchor := `id="comment-` + itoa(list[0].ID) + `"`
	for _, want := range []string{anchor, "I have a", "commented", "Comment added."} {
		if !strings.Contains(after, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// An empty comment goes back to the page of the issue; the refusal is over the form
	// and the form data is carried for the script (formfail.go).
	if resp := postForm(t, e, asBrowser(e.Owner), "/ui/requests/"+itoa(id)+"/close", url.Values{"mode": {"comment"}, "reason": {"  "}}); resp.StatusCode != http.StatusSeeOther || strings.Contains(resp.Header.Get("Location"), "?") {
		t.Errorf("empty comment -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	} else if _, failed := followRefusal(t, e.Owner, e.Base, resp); !strings.Contains(failed, `class="callout is-danger"`) || !strings.Contains(failed, "A comment needs text.") || !strings.Contains(failed, `id="gt-flash"`) {
		t.Errorf("the refusal is not shown over the form: %.300s", failed)
	}
	// A foreign project's issue is as unknown as a missing one.
	other := rqMake(t, e.St, rqSpec{Title: "Hidden", Project: shellHiddenProject})
	if resp := postForm(t, e, e.Member, "/ui/requests/"+itoa(other.Request.ID)+"/close", url.Values{"mode": {"comment"}, "reason": {"x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("comment on a foreign issue -> %d, want 404", resp.StatusCode)
	}
}

func TestClosingWithTextWritesTheCommentToo(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Drop me", Criteria: []string{"One"}})
	resp := postForm(t, e, e.Owner, "/ui/requests/"+itoa(d.Request.ID)+"/close", url.Values{"mode": {"drop"}, "reason": {"Not needed any more"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("drop -> %d", resp.StatusCode)
	}
	if got, _ := e.St.RequestByID(d.Request.ID); got.Request.State != "dropped" {
		t.Fatalf("state %s", got.Request.State)
	}
	list := commentsOf(t, e, store.CommentOnRequest, d.Request.ID)
	if len(list) != 1 || list[0].Body != "Not needed any more" {
		t.Fatalf("the text was not kept as a comment: %+v", list)
	}
	// A dropped issue can still be commented on.
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(d.Request.ID))
	if !strings.Contains(page, `data-mode="comment"`) || strings.Contains(page, "Drop issue") {
		t.Error("a closed issue should offer comments only")
	}
}

func TestCommentsAreEditedWithVersionsAndDeletedSoftly(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Edit me"})
	id := d.Request.ID
	if resp := postForm(t, e, e.Member, "/ui/requests/"+itoa(id)+"/close", url.Values{"mode": {"comment"}, "reason": {"First draft"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatal(resp.StatusCode)
	}
	c := commentsOf(t, e, store.CommentOnRequest, id)[0]
	cid := itoa(c.ID)
	page := e.Base + "/ui/x/shell/issues/" + itoa(id)
	// Edit link and the open form only for the author.
	_, mine := fetchPage(t, e.Member, page)
	_, others := fetchPage(t, e.Lead, page)
	if !strings.Contains(mine, "edit-comment="+cid) {
		t.Error("the author gets no edit link")
	}
	if strings.Contains(others, "edit-comment="+cid) {
		t.Error("somebody else gets an edit link")
	}
	if !strings.Contains(others, `action="/ui/comments/`+cid+`/delete"`) {
		t.Error("a lead gets no delete button")
	}
	_, editing := fetchPage(t, e.Member, page+"?edit-comment="+cid)
	if !strings.Contains(editing, `action="/ui/comments/`+cid+`/edit"`) || !strings.Contains(editing, "First draft") {
		t.Error("the edit form is not open")
	}
	_, foreign := fetchPage(t, e.Lead, page+"?edit-comment="+cid)
	if strings.Contains(foreign, `action="/ui/comments/`+cid+`/edit"`) {
		t.Error("the edit form opens for somebody else")
	}
	if resp := postForm(t, e, e.Lead, "/ui/comments/"+cid+"/edit", url.Values{"body": {"Hijacked"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a lead edits another's comment -> %d, want 403", resp.StatusCode)
	}
	if resp := postForm(t, e, e.Member, "/ui/comments/"+cid+"/edit", url.Values{"body": {"Second draft"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "comment=edited") {
		t.Fatalf("edit -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, edited := fetchPage(t, e.Owner, page)
	for _, want := range []string{"Second draft", "edited", "Earlier versions", "First draft", "Version 1"} {
		if !strings.Contains(edited, want) {
			t.Errorf("an edited comment lacks %q", want)
		}
	}
	if resp := postForm(t, e, e.Member, "/ui/comments/"+cid+"/edit", url.Values{"body": {" "}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "comment=empty") {
		t.Errorf("empty edit -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Delete: a member who is neither author nor lead may not; the lead may.
	if resp := postForm(t, e, e.Reviewer, "/ui/comments/"+cid+"/delete", url.Values{}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a stranger deletes -> %d, want 403", resp.StatusCode)
	}
	if resp := postForm(t, e, e.Lead, "/ui/comments/"+cid+"/delete", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("lead deletes -> %d", resp.StatusCode)
	}
	_, gone := fetchPage(t, e.Owner, page)
	if !strings.Contains(gone, "This comment was deleted.") || strings.Contains(gone, "Second draft") || strings.Contains(gone, "First draft") {
		t.Error("a deleted comment shows its text or no placeholder")
	}
	if resp := postForm(t, e, e.Member, "/ui/comments/9999/edit", url.Values{"body": {"x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown comment -> %d, want 404", resp.StatusCode)
	}
}

func TestIdeaAndPainTakeCommentsInTheirTimeline(t *testing.T) {
	e, pa := listEnv(t)
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "An idea", Body: "Wish.", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	ip := "/ui/x/shell/ideas/" + itoa(i.ID)
	resp := postForm(t, e, e.Owner, "/ui/ideas/"+itoa(i.ID)+"/state", url.Values{"mode": {"comment"}, "reason": {"What about voice?"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "#comment-") {
		t.Fatalf("idea comment -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := pa.IdeaView(i.ID); got.State != idea.Open {
		t.Errorf("a comment changed the idea: %s", got.State)
	}
	_, page := fetchPage(t, e.Owner, e.Base+ip)
	if !strings.Contains(page, "What about voice?") || !strings.Contains(page, `class="cbox`) {
		t.Error("the idea page lacks the comment")
	}
	resp = postForm(t, e, e.Owner, "/ui/ideas/"+itoa(i.ID)+"/state", url.Values{"mode": {"dismissed"}, "reason": {"Later"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatal(resp.StatusCode)
	}
	if n := len(commentsOf(t, e, store.CommentOnIdea, i.ID)); n != 2 {
		t.Errorf("dismissing with text left %d comments, want 2", n)
	}
	if resp := postForm(t, e, e.Guest, "/ui/ideas/"+itoa(i.ID)+"/state", url.Values{"mode": {"comment"}, "reason": {"hm"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a guest comments on an idea -> %d, want 403", resp.StatusCode)
	}

	p, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Slow start", Body: "b", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	pp := "/ui/x/shell/pains/" + itoa(p.Pain.ID)
	resp = postForm(t, e, e.Member, "/ui/pains/"+itoa(p.Pain.ID)+"/state", url.Values{"mode": {"comment"}, "note": {"Seen it too."}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("pain comment -> %d", resp.StatusCode)
	}
	_, ppage := fetchPage(t, e.Owner, e.Base+pp)
	if !strings.Contains(ppage, "Seen it too.") || !strings.Contains(ppage, `data-mode="comment"`) {
		t.Error("the pain page lacks the comment or the comment mode")
	}
	if got, _ := tpOwner(e).PainDetailView(p.Pain.ID); got.Pain.State != pain.Active {
		t.Errorf("a comment changed the pain: %s", got.Pain.State)
	}
	if resp := postForm(t, e, e.Guest, "/ui/pains/"+itoa(p.Pain.ID)+"/state", url.Values{"mode": {"comment"}, "note": {"hm"}}); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Errorf("a guest comments on a pain -> %d", resp.StatusCode)
	}
}

// The status select writes its words twice, as the note of the change and as a
// comment. The timeline shows them once, in the comment.
func TestClosingWithTextShowsTheTextOnceInTheTimeline(t *testing.T) {
	e := ovEnv(t)
	base := e.Base + "/ui/x/shell/issues/"
	// The comment also carries its raw text in data-md for the script; what the
	// reader sees are the text nodes.
	count := func(page, text string) int { return strings.Count(page, ">"+text+"<") }

	dropped := rqMake(t, e.St, rqSpec{Title: "Drop me", Criteria: []string{"One"}})
	postForm(t, e, e.Owner, "/ui/requests/"+itoa(dropped.Request.ID)+"/close", url.Values{"mode": {"drop"}, "reason": {"Not needed any more"}})
	_, page := fetchPage(t, e.Owner, base+itoa(dropped.Request.ID))
	if n := count(page, "Not needed any more"); n != 1 {
		t.Errorf("a dropped issue shows its reason %d times, want once", n)
	}
	if !strings.Contains(page, `data-act="request.dropped"`) || !strings.Contains(page, `id="comment-`) {
		t.Error("the drop entry and the comment must both stay in the timeline")
	}

	done := rqMake(t, e.St, rqSpec{Title: "To finish", Criteria: []string{"One"}})
	if err := e.St.SetCriterionState(done.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "a41c7e2", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	postForm(t, e, e.Owner, "/ui/requests/"+itoa(done.Request.ID)+"/close", url.Values{"mode": {"complete"}, "reason": {"Shipped with the release"}, "evidence_kind": {"url"}})
	_, page = fetchPage(t, e.Owner, base+itoa(done.Request.ID))
	if n := count(page, "Shipped with the release"); n != 1 {
		t.Errorf("a completed issue shows its note %d times, want once", n)
	}

	// Another proof than the words: both texts differ, both stay.
	both := rqMake(t, e.St, rqSpec{Title: "Both", Criteria: []string{"One"}})
	if err := e.St.SetCriterionState(both.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "b52d8f3", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	postForm(t, e, e.Owner, "/ui/requests/"+itoa(both.Request.ID)+"/close", url.Values{"mode": {"complete"}, "reason": {"Rolled out on Friday"}, "evidence_kind": {"url"}, "evidence_ref": {"https://example.org/release-notes"}})
	_, page = fetchPage(t, e.Owner, base+itoa(both.Request.ID))
	if !strings.Contains(page, "https://example.org/release-notes") || count(page, "Rolled out on Friday") != 1 {
		t.Error("a note that differs from the comment stays beside it")
	}
}
