package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestStripMarkdownLeavesWhatAPersonWouldRead(t *testing.T) {
	names := map[string]string{"jonas": "Jonas Brandt"}
	for _, c := range []struct{ in, en, de string }{
		{"Schau ![Kasten](ATT-1) an", "Schau Image: Kasten an", "Schau Bild: Kasten an"},
		{"![](ATT-2)", "Image", "Bild"},
		{"```go\nfmt.Println(1)\n```\nund weiter", "fmt.Println(1) und weiter", "fmt.Println(1) und weiter"},
		{"<details><summary>Notizen</summary>drin</details>", "Notizen drin", "Notizen drin"},
		{"## Titel\n- **fett** und _schräg_ mit `code` und [Link](https://x.test/a)", "Titel fett und schräg mit code und Link", "Titel fett und schräg mit code und Link"},
		{"> zitiert\n1. eins", "zitiert eins", "zitiert eins"},
		{"@jonas, kannst du? @unbekannt auch", "Jonas Brandt, kannst du? @unbekannt auch", "Jonas Brandt, kannst du? @unbekannt auch"},
		{"snake_case_word und 2*3*4", "snake_case_word und 2*3*4", "snake_case_word und 2*3*4"},
	} {
		if got := stripMarkdown(langEN, c.in, names); got != c.en {
			t.Errorf("en %q -> %q, want %q", c.in, got, c.en)
		}
		if got := stripMarkdown(langDE, c.in, names); got != c.de {
			t.Errorf("de %q -> %q, want %q", c.in, got, c.de)
		}
	}
}

// threadMention writes the case of the usability run: somebody mentions the member in a thread of the
// project room, with Markdown in the text. The room summary counts it, the room itself does not hold it.
func threadMention(t *testing.T, e shellEnv, body string) (room string, thread int64) {
	t.Helper()
	inboxPeople(t, e)
	room = store.RoomKeyForProject(shellProject)
	alice := e.St.CoordinationFor(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}, "")
	thread, err := alice.CreateThread(store.Thread{Project: shellProject, Title: "Release", Question: "Who ships?", Person: "alice", AuthorPrincipalID: "person:1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.ThreadPost(thread, store.CoordMessage{ClientID: "tm1", Body: body, Mentions: []string{"person:2"}}); err != nil {
		t.Fatal(err)
	}
	return room, thread
}

// REQ-684 AC-3: the row of a mention names who said it and how it begins, as plain text, also when it
// was said in a thread; an answer finishes it and is confirmed.
func TestInboxRowOfAThreadMentionNamesTheSenderAndTheTextAsPlainText(t *testing.T) {
	e := ovEnv(t)
	room, thread := threadMention(t, e, "@anna ![Kasten](ATT-1) schau mal ```go\nx := 1\n``` <details><summary>Notizen</summary>drin</details>")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	for _, want := range []string{"<b>alice</b>", "Image: Kasten schau mal x := 1 Notizen drin", "thread=" + itoa(thread), `name="thread" value="` + itoa(thread) + `"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the inbox lacks %q:\n%s", want, page)
		}
	}
	for _, raw := range []string{"![Kasten]", "```", "<details><summary>", "1 unread mention", "@anna"} {
		if strings.Contains(page, raw) {
			t.Errorf("the inbox row shows %q", raw)
		}
	}
	// Read: the row stands for the mentions in the threads of the room, reading it reads them.
	resp := tpPost(t, e, e.Member, "/ui/coord/read", url.Values{"room": {room}, "sequence": {"0"}, "threads": {"1"}, "back": {"inbox"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("read -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, after := fetchPage(t, e.Member, e.Base+"/ui/inbox"); strings.Contains(after, "schau mal") || strings.Contains(after, "unread mention") {
		t.Error("a mention in a thread stays in the inbox after it was read")
	}
}

func TestInboxReplyToAThreadMentionIsPostedInTheThreadAndConfirmed(t *testing.T) {
	e := ovEnv(t)
	room, thread := threadMention(t, e, "@anna can you take the release?")
	resp := tpPost(t, e, e.Member, "/ui/inbox/reply", url.Values{"room": {room}, "thread": {itoa(thread)}, "body": {"Yes, I take it."}, "to": {"alice"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox?sent=alice" {
		t.Fatalf("reply -> %d %q, want back to the inbox with the name", resp.StatusCode, resp.Header.Get("Location"))
	}
	member := e.St.CoordinationFor(store.Principal{ID: "person:2", Label: "anna", TokenKind: store.WebSessionKind}, "")
	page, _, err := member.MessagePresentationWindow(store.DestinationDiscussion, store.ThreadDestinationID(thread), store.LatestWindow(10))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(page.Messages); n != 2 || page.Messages[1].Body != "Yes, I take it." {
		t.Errorf("the answer is not in the thread: %+v", page.Messages)
	}
	_, inbox := fetchPage(t, e.Member, e.Base+"/ui/inbox?sent=alice")
	if !strings.Contains(inbox, "Reply sent to alice.") {
		t.Error("the inbox does not confirm the answer")
	}
	if strings.Contains(inbox, "can you take the release?") || strings.Contains(inbox, "unread mention") {
		t.Error("an answered mention stays in the inbox")
	}
}

// REQ-684 AC-2: the page of a room belongs to the repository of the room, and a #n there is read as the
// issue or pull request of that repository, never as a knowledge entry.
func TestRoomPageCarriesItsRepositoryAndReadsHashNumbersStrictly(t *testing.T) {
	e := ovEnv(t)
	room := store.RoomKeyForProject(shellProject)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(page, `data-repo="`+shellProject+`"`) || !strings.Contains(page, `data-hash="strict"`) {
		t.Errorf("the body of the room page lacks its repository or the strict reading of #n")
	}
	// Any other page of a repository keeps the old reading: its #1970 may still be an old knowledge entry.
	_, issues := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues")
	if !strings.Contains(issues, `data-repo="`+shellProject+`"`) || strings.Contains(issues, `data-hash="strict"`) {
		t.Errorf("the issue list has the wrong reading of #n")
	}
}

func TestRefsAskedForAnIssueOfARepositoryFindOnlyThatRepositorysIssue(t *testing.T) {
	e := ovEnv(t)
	mine := rqMake(t, e.St, rqSpec{Title: "In the shell project"})
	other := rqMake(t, e.St, rqSpec{Title: "In another project", Project: "github.com/x/other"})
	ask := func(ref, project string) string {
		_, body := fetchPage(t, e.Owner, e.Base+"/ui/refs?ref="+url.QueryEscape(ref)+"&project="+url.QueryEscape(project))
		return body
	}
	if body := ask("issue:"+itoa(mine.Request.ID), shellProject); !strings.Contains(body, `"gefunden":true`) || !strings.Contains(body, "In the shell project") {
		t.Errorf("the issue of the repository is not found: %s", body)
	}
	if body := ask("issue:"+itoa(other.Request.ID), shellProject); strings.Contains(body, "In another project") || strings.Contains(body, `"gefunden":true`) {
		t.Errorf("an issue of another repository is found as #n of this one: %s", body)
	}
	// REQ-n asks for the issue wherever it lies, as before.
	if body := ask("REQ-"+itoa(other.Request.ID), shellProject); !strings.Contains(body, "In another project") {
		t.Errorf("REQ-n no longer finds the issue: %s", body)
	}
}

// REQ-684 AC-3: whoever answers on the object a mention stands on has read it; the entry is done.
func TestTextMentionIsDoneWhenTheMentionedPersonAnswersOnTheObject(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Limit the spool"})
	if _, err := e.St.Access(store.Principal{ID: "person:2", Label: "anna"}).AddComment(store.CommentOnRequest, d.Request.ID, "@lars please look at this", store.CommentActor{Person: "anna"}); err != nil {
		t.Fatal(err)
	}
	if inbox := fetchBody(t, e.Lead, e.Base+"/ui/inbox"); !strings.Contains(inbox, "please look at this") {
		t.Fatalf("the mention is not in the inbox: %.800s", inbox)
	}
	if _, err := e.St.Access(store.Principal{ID: "person:4", Label: "lars"}).AddComment(store.CommentOnRequest, d.Request.ID, "Done, see the branch.", store.CommentActor{Person: "lars"}); err != nil {
		t.Fatal(err)
	}
	if inbox := fetchBody(t, e.Lead, e.Base+"/ui/inbox"); strings.Contains(inbox, "please look at this") {
		t.Error("an answered mention stays under Said to you")
	}
}
