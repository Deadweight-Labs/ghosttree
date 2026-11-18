package web

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-705: one way to show a text of a person. On a page it is the Markdown renderer (the box with data-md);
// as a line in a row, a card or a quote it is plain words made by stripMarkdown. These tests name every
// place that was found showing the raw marks.

func TestPlainTextIsOneFunctionForEveryLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Das ist **fett**, `code` und [ein Link](https://x.example/a).", "Das ist fett, code und ein Link."},
		{"## Titel\n\n> [!NOTE]\n> Achtung\n\n- [x] erledigt\n- offen", "Titel Achtung erledigt offen"},
		{"Schau ![Roter Block](ATT-3) an", "Schau Image: Roter Block an"},
		{"vorher <details><summary>Testlauf</summary>ok 0.4s</details> nachher", "vorher Testlauf ok 0.4s nachher"},
		{"| a | b |\n|---|:-:|\n| 1 | 2 |", "a · b 1 · 2"},
		{"eins\n\n---\n\nzwei", "eins zwei"},
		{"<https://x.example/p> und \\*kein Stern\\*", "https://x.example/p und *kein Stern*"},
		{"```go\nfmt.Println(1)\n```", "fmt.Println(1)"},
		{"snake_case_word und 2*3*4", "snake_case_word und 2*3*4"},
	}
	for _, c := range cases {
		if got := stripMarkdown(langEN, c.in, nil); got != c.want {
			t.Errorf("stripMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := stripMarkdown(langDE, "![](ATT-2)", nil); got != "Bild" {
		t.Errorf("a picture without words in German = %q", got)
	}
	if got := plainSnippet(langEN, "Erste **Zeile** mit vielen Wörtern darin und noch mehr", 25); !strings.HasSuffix(got, "…") || strings.Contains(got, "*") {
		t.Errorf("plainSnippet = %q", got)
	}
}

func TestPlainLinesOfTheLooseEndsShowNoMarks(t *testing.T) {
	// the first line of a pain or idea in a list
	if got := firstLineCut(langEN, "**Wichtig:** der `Spool` hängt\nzweite Zeile", 160); got != "Wichtig: der Spool hängt" {
		t.Errorf("firstLineCut = %q", got)
	}
	// the sentence under a title in a fold
	if got := leadSentence(langEN, "Der **Spool** übergibt `Jobs`. Zweiter Satz.", 200); got != "Der Spool übergibt Jobs." {
		t.Errorf("leadSentence = %q", got)
	}
	// the quote line of a reply in a room
	if got := (coordReplyView{Body: "Siehe [das Issue](/ui/x) und **das**", Lang: langEN}).Snippet(); got != "Siehe das Issue und das" {
		t.Errorf("Snippet = %q", got)
	}
	// the line a document hit shows
	if got := docSnippet(langEN, "# Kopf\n\nDer **Backoff** ist `2^n`.", []string{"backoff"}); got != "Der Backoff ist 2^n." {
		t.Errorf("docSnippet = %q", got)
	}
	// the excerpt of a knowledge row: a table and a section are no marks either
	if got := excerpt(langEN, "<details><summary>Mehr</summary>Text</details>\n\nDanach"); strings.Contains(got, "<") {
		t.Errorf("excerpt = %q", got)
	}
}

func TestRefCardsBriefIsPlainWords(t *testing.T) {
	e := ovEnv(t)
	a := newApp(e.St).(*appHandler).app
	d := rqMake(t, e.St, rqSpec{Title: "Karte", Desc: "Der **Spool** hängt, siehe [Log](https://x.example/l).\n\n- `a`\n- b", Criteria: []string{"eins"}})
	pa := e.St.Access(store.Principal{ID: "person:2", Label: "anna"})
	card, _, ok := a.resolveRefCard(langEN, pa, overviewNow().UTC(), "REQ", d.Request.ID)
	if !ok {
		t.Fatal("no card")
	}
	if card.Brief != "Der Spool hängt, siehe Log. a b" {
		t.Errorf("the brief of the card shows marks: %q", card.Brief)
	}
}

// The closing words of an issue stand once. The form writes them as the note of the change and as a comment;
// the browser sends line breaks as CRLF and the comment may be edited afterwards, and both used to make the
// entry of the history show the same text again, raw (S07).
func TestIssueCloseNoteStandsOnceAndIsRenderedByTheRenderer(t *testing.T) {
	e := ovEnv(t)
	text := "Beide **fett** erledigt. <details><summary>Testlauf</summary>ok</details>\r\n\r\n```go\r\nfmt.Println(1)\r\n```"
	closeIssue := func(mode string, form url.Values) (int64, string) {
		path := mode
		if mode == "complete" {
			path = "close" // the status row of the page: its words are the proof and the comment
			form.Set("mode", "complete")
		}
		d := rqMake(t, e.St, rqSpec{Title: "To finish " + mode, Criteria: []string{"One"}})
		if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "a41c7e2", Person: "alice"}); err != nil {
			t.Fatal(err)
		}
		form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
		sameOriginPostForm(t, e.Owner, e.Base+"/ui/requests/"+itoa(d.Request.ID)+"/"+path, form).Body.Close()
		_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(d.Request.ID))
		return d.Request.ID, page
	}
	entry := func(page, act string) string {
		i := strings.Index(page, `data-act="`+act+`"`)
		if i < 0 {
			t.Fatalf("no %s entry:\n%.2000s", act, page)
		}
		rest := page[i:]
		return rest[:strings.Index(rest, `<div class="cmt`)+1]
	}

	// With a comment of the same words (CRLF in the form): the entry says the line only.
	_, page := closeIssue("complete", url.Values{"evidence_kind": {"url"}, "reason": {text}})
	if e := entry(page, "request.done"); strings.Contains(e, "bd2") || strings.Contains(e, "Testlauf") {
		t.Errorf("the closing words stand twice, the first time in the entry: %s", e)
	}
	if !strings.Contains(page, `data-md="Beide **fett** erledigt.`) {
		t.Error("the comment is not given to the renderer")
	}

	// Without a comment (a drop with a reason): the words are shown, once, by the renderer, not as raw text.
	_, page = closeIssue("drop", url.Values{"reason": {text}})
	e1 := entry(page, "request.dropped")
	if !strings.Contains(e1, `<div class="bd2"><div class="kn-body prose" data-md="Beide **fett** erledigt. &lt;details&gt;`) {
		t.Errorf("the reason of a drop is not given to the renderer: %s", e1)
	}
	if strings.Contains(e1, "```go fmt.Println") {
		t.Errorf("the entry shows the raw words: %s", e1)
	}
}

func TestIssueCloseNoteIsNotShownAgainAfterTheCommentWasEdited(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Edited", Criteria: []string{"One"}})
	if err := e.St.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "a41c7e2", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"mode": {"complete"}, "evidence_kind": {"url"}, "reason": {"Erste Fassung"}}
	form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
	sameOriginPostForm(t, e.Owner, e.Base+"/ui/requests/"+itoa(d.Request.ID)+"/close", form).Body.Close()
	edit := url.Values{"body": {"Zweite Fassung"}}
	edit.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
	sameOriginPostForm(t, e.Owner, e.Base+"/ui/comments/1/edit", edit).Body.Close()
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(d.Request.ID))
	i := strings.Index(page, `data-act="request.done"`)
	rest := page[i:]
	if strings.Contains(rest[:strings.Index(rest, `<div class="cmt`)], "Erste Fassung") {
		t.Error("the entry shows the first version of an edited comment again")
	}
}

func TestRequirementTextGoesThroughTheRenderer(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Reqs", Criteria: []string{"`ctx login` läuft und **zeigt** den Code"}})
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/issues/"+itoa(d.Request.ID))
	if !strings.Contains(page, `<div class="tx"><div class="kn-body prose" data-md="`) || !strings.Contains(page, "<code>ctx login</code>") {
		t.Errorf("a requirement with code is not shown by the renderer:\n%s", page[strings.Index(page, `class="crit`):][:800])
	}
}

func TestStandingOrderTextGoesThroughTheRenderer(t *testing.T) {
	_, st, room, get := chatFixture(t)
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin", TokenKind: store.WebSessionKind}, "")
	if _, err := access.CreateStanding(store.StandingInput{RoomKey: room, ClientID: "s1", Body: "Never push to **main**, see `git log`."}); err != nil {
		t.Fatal(err)
	}
	appendAgent(t, st, room, "a2", "after", "")
	page := get("/ui/coord?room=" + url.QueryEscape(room))
	if !strings.Contains(page, `class="slab-t"><div class="kn-body prose" data-md="Never push to **main**, see `) {
		t.Error("the plate of a standing order shows the raw words")
	}
	if !strings.Contains(page, `class="coord-standing-t"><div class="kn-body prose" data-md="Never push to **main**`) {
		t.Error("the list of standing orders shows the raw words")
	}
}

func TestPausedAgentReasonGoesThroughTheRenderer(t *testing.T) {
	items := controlTimeline(langEN, store.AgentControl{Action: store.ControlPause, State: store.ControlRequested, Reason: "wegen **Release**", RequestedByLabel: "robin"})
	var body string
	for _, it := range items {
		body += string(it.Body)
	}
	if !strings.Contains(body, `data-md="wegen **Release**"`) {
		t.Errorf("the reason of a pause is not given to the renderer: %q", body)
	}
}

// The preview of the composer shows the whole text: no height cap and no inner scroll that cuts a note box at
// the bottom edge (P12). Only the chat composers, which share the window with the history, keep a cap.
func TestComposerPreviewGrowsWithItsText(t *testing.T) {
	css, err := os.ReadFile("static/ui-parts.css")
	if err != nil {
		t.Fatal(err)
	}
	rule := regexp.MustCompile(`& \.cx-pv \{[^}]*\}`).FindString(string(css))
	if rule == "" {
		t.Fatal("no rule for the preview")
	}
	if strings.Contains(rule, "max-height") || strings.Contains(rule, "overflow: auto") || strings.Contains(rule, "overflow-y") {
		t.Errorf("the preview is capped or scrolls inside itself: %s", rule)
	}
}
