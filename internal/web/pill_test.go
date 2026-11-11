package web

import (
	"encoding/json"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"html"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var pillDataPC = regexp.MustCompile(`data-pc="([^"]*)"`)

// One pill for everything: icon, quiet number, the title only in the long form,
// no tooltip next to the card, an address that is no link when it is unsafe.
func TestPillShapeIsTheSameForEveryKind(t *testing.T) {
	short := string(numberedPill(langDE, "request", 602, "/ui/x/issues/602", ""))
	long := string(numberedPill(langDE, "request", 602, "/ui/x/issues/602", "Pills überall"))
	for _, want := range []string{`class="ref pill k-req"`, `data-ref="REQ-602"`, `<span class="num">#602</span>`, `<svg class="i"`} {
		if !strings.Contains(short, want) {
			t.Errorf("short pill lacks %q: %s", want, short)
		}
	}
	if strings.Contains(short, `class="pt"`) || !strings.Contains(long, `<span class="pt">Pills überall</span>`) {
		t.Errorf("only the long form has the title:\n%s\n%s", short, long)
	}
	for _, kind := range []string{"pr", "commit", "branch", "know", "org", "machine", "url", "file", "folder"} {
		got := string(kindPill(langDE, kind, "text", "/x", pillCard{Title: "t"}))
		if strings.Contains(got, ` title="`) {
			t.Errorf("%s: a tooltip stands next to the card: %s", kind, got)
		}
		if !strings.Contains(got, `class="ref pill `+pillKinds[kind].Class) || !strings.Contains(got, `data-pc="`) {
			t.Errorf("%s: no shape or no card: %s", kind, got)
		}
	}
	if got := string(refPillHTML(langDE, "request", 1, "javascript:alert(1)")); strings.Contains(got, "javascript:") {
		t.Errorf("an unsafe address became a link: %s", got)
	}
	if got := string(refPillHTML(langDE, "request", 1, "")); !strings.HasPrefix(got, "<span") {
		t.Errorf("without an address the pill is no link: %s", got)
	}
}

// The card of a pill without a key is JSON in an attribute; the whole name of a
// branch stays in it even when the pill cuts it.
func TestBranchPillCarriesTheWholeNameOnItsCard(t *testing.T) {
	name := "feat/" + strings.Repeat("sehr-langer-branch-", 5) + "ende"
	got := string(branchPill(langDE, name, "/ui/x/pulls?q=branch"))
	m := pillDataPC.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("no card: %s", got)
	}
	var c pillCard
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &c); err != nil {
		t.Fatal(err)
	}
	if c.Title != name || c.Label != "Branch" {
		t.Errorf("card = %+v", c)
	}
	if !strings.Contains(got, `k-branch mono`) || !strings.Contains(got, `<span class="num">`+name+`</span>`) {
		t.Errorf("the pill lacks the name or the mono class: %s", got)
	}
}

// A pull request takes the colour of its state; one of unknown state is grey.
func TestPRPillTakesTheColourOfItsState(t *testing.T) {
	for state, want := range map[string]string{"open": "s-open", "merged": "s-merged", "closed": "s-closed", "": "s-unknown"} {
		got := string(prPill(langDE, 184, "/ui/x/pulls/184", state, "Titel", "a → b"))
		if !strings.Contains(got, want) || !strings.Contains(got, `<span class="num">#184</span>`) {
			t.Errorf("state %q: %s", state, got)
		}
	}
}

func TestURLPillReadsHostAndEndOfPath(t *testing.T) {
	long := "https://www.example.org/a/very/long/path/that/goes/on/and/on/" + strings.Repeat("x", 120)
	got := string(urlPill(langDE, long))
	if !strings.Contains(got, `<span class="num">example.org/…/on/`) || strings.Contains(got, `<span class="num">https://`) {
		t.Errorf("an outside address reads as host and end: %s", got)
	}
	if !strings.Contains(got, `rel="noopener noreferrer"`) || strings.Contains(got, "data-ref=") {
		t.Errorf("an outside address is an outside link without a key: %s", got)
	}
	pr := string(urlPill(langDE, "https://github.com/Deadweight-Labs/ghosttree/pull/163"))
	if !strings.Contains(pr, "k-pr") || !strings.Contains(pr, "#163") {
		t.Errorf("a GitHub pull request is a pull request pill: %s", pr)
	}
	// Nobody here has read its state, so it is not "open"; it leaves ghosttree.
	if !strings.Contains(pr, "s-unknown") || strings.Contains(pr, "s-open") || !strings.Contains(pr, `rel="noopener noreferrer"`) {
		t.Errorf("a GitHub link claims a state or does not leave ghosttree: %s", pr)
	}
}

// In a sentence of the history the pill is written out: the script adds the title.
func TestLongPillAsksTheScriptForItsTitle(t *testing.T) {
	long, short := string(longPill(langDE, "request", 5, "/ui/requests/5")), string(refPillHTML(langDE, "request", 5, "/ui/requests/5"))
	if !strings.Contains(long, ` data-long`) || strings.Contains(short, "data-long") {
		t.Errorf("long %s short %s", long, short)
	}
	ev := issueEvents(langDE, []requestdomain.Activity{{Kind: "relation.added", Data: "parent REQ-7"}, {Kind: "task.created", Data: "TASK-5 x text"}, {Kind: "pain.linked", Data: "PAIN-3"}}, nil, nil, "", func(string) string { return "" })
	var all string
	for _, e := range ev {
		all += string(e.Sentence)
	}
	for _, want := range []string{`data-ref="REQ-7" data-long`, `data-ref="TASK-5" data-long`, `data-ref="PAIN-3" data-long`} {
		if !strings.Contains(all, want) {
			t.Errorf("sentences lack %q: %s", want, all)
		}
	}
	got := painLinkTarget(langDE, "request:3") + painLinkTarget(langDE, "idea:5") + painLinkTarget(langDE, "knowledge:9") + painLinkTarget(langDE, "weird:x")
	for _, want := range []string{`data-ref="REQ-3" data-long`, `data-ref="IDEA-5" data-long`, `data-ref="KN-9" data-long`, "weird:x"} {
		if !strings.Contains(got, want) {
			t.Errorf("pain links lack %q: %s", want, got)
		}
	}
}

// A title with a reference in parentheses reads with the pill instead of the bare key.
func TestTitleRefsMakesPillsAndEscapes(t *testing.T) {
	got := string(titleRefs(langDE, "feat(web): Tabs (REQ-599) <b>"))
	if strings.Contains(got, "REQ-599") && !strings.Contains(got, `data-ref="REQ-599"`) {
		t.Errorf("bare key left: %s", got)
	}
	for _, want := range []string{`<span class="ref pill k-req"`, `<span class="num">#599</span>`, "&lt;b&gt;"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "Tabs (") || strings.Contains(got, ") &lt;") || strings.Contains(got, "<a ") {
		t.Errorf("the parentheses stay or the pill is a link inside a link: %s", got)
	}
	if plain := string(titleRefs(langDE, "Release 0.14.0 & more")); plain != "Release 0.14.0 &amp; more" {
		t.Errorf("plain title = %q", plain)
	}
}

// The signpost of a decision is the same in the server and in the script.
func TestDecisionIconIsTheSignpostEverywhere(t *testing.T) {
	const signpost = `M8 1.8v12.4M5.8 14.2h4.4`
	if !strings.Contains(mockIcons["decide"], signpost) || !strings.Contains(refKinds["decision"].Icon, signpost) {
		t.Errorf("the decision icon is not the signpost: %s", mockIcons["decide"])
	}
	const forks = `M8 14V8.4M8 8.4C8 6.4`
	for _, js := range []string{"static/ui-pill.js", "static/ui-refs.js", "static/ui-md.js"} {
		src := string(mustReadEmbeddedFile(t, js))
		if strings.Contains(src, forks) {
			t.Errorf("%s still draws the old decision icon", js)
		}
	}
	if !strings.Contains(string(mustReadEmbeddedFile(t, "static/ui-pill.js")), signpost) {
		t.Error("ui-pill.js lacks the signpost")
	}
}

func mustReadEmbeddedFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := files.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Knowledge has a card, but only for the viewer who may read the entry, and the
// card tells in words what it is about, never a code block.
func TestKnowledgeRefCardIsGatedAndHasNoCode(t *testing.T) {
	e := ovEnv(t)
	a := newApp(e.St).(*appHandler).app
	vis := knInsert(t, e.St, store.Knowledge{Title: "Sichtbares Wissen", Body: "Erster Satz.\n```go\nrows.Close()\n```"})
	hid := knInsert(t, e.St, store.Knowledge{Title: "VERSTECKTES-WISSEN", Scope: scope.Axes{Project: shellHiddenProject}})
	pa := e.St.Access(store.Principal{ID: "person:2", Label: "anna"})
	now := overviewNow().UTC()

	card, href, ok := a.resolveRefCard(langDE, pa, now, "KN", vis)
	if !ok || card.Title != "Sichtbares Wissen" || href == "" {
		t.Fatalf("visible knowledge: ok=%v card=%+v href=%q", ok, card, href)
	}
	if strings.Contains(card.Brief, "rows.Close") || !strings.Contains(card.Brief, "Erster Satz.") {
		t.Errorf("the card brief = %q", card.Brief)
	}
	if _, _, ok := a.resolveRefCard(langDE, pa, now, "KN", hid); ok {
		t.Error("a hidden knowledge entry has a card")
	}
	if _, _, ok := a.resolveRefCard(langDE, pa, now, "KN", 999999); ok {
		t.Error("an unknown knowledge entry has a card")
	}
}

// ui-md.js marks a "#1970" and leaves the decision to the server; nothing but
// ui-pill.js builds the pill, and no HTML is made from a text.
func TestMarkdownRendererLeavesHashNumbersToTheServer(t *testing.T) {
	src := string(mustReadEmbeddedFile(t, "static/ui-md.js"))
	for _, want := range []string{"data-maybe", "GTPill.ref", "GTRefs.resolve", "DEC|FEAT"} {
		if !strings.Contains(src, want) {
			t.Errorf("ui-md.js lacks %q", want)
		}
	}
	if strings.Contains(src, "innerHTML") {
		t.Error("ui-md.js builds HTML from a string")
	}
	// The cut points of the script test of the documents (#206): safeUrl, with the comment and
	// the patterns above it, stands on its own up to the comment of the next block.
	if i, j := strings.Index(src, "  // Allowed: http(s) and relative addresses."), strings.Index(src, "  // Address of a reference"); i < 0 || j <= i || !strings.Contains(src[i:j], "function safeUrl(") {
		t.Error("safeUrl is no longer a block of its own in ui-md.js")
	}
}

// The head of a task says who it waits for once: when the person who asked is the
// person who is waited for, the age stands behind the name and nobody appears twice.
func TestTaskHeadNamesTheWaitedForPersonOnce(t *testing.T) {
	e, task, _ := taskFixture(t)
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tasks/"+itoa(task.ID))
	i := strings.Index(page, `<div class="dmeta">`)
	if i < 0 {
		t.Fatal("no head line")
	}
	head := page[i : i+strings.Index(page[i:], "</div>")]
	names := regexp.MustCompile(`class="person">(?:<span class="waits".*?</span></span></span>|<span class="gav[^>]*>.*?</span>)([^<]+)<`).FindAllStringSubmatch(head, -1)
	seen := map[string]bool{}
	for _, m := range names {
		if seen[m[1]] {
			t.Errorf("%q stands twice in the head: %s", m[1], head)
		}
		seen[m[1]] = true
	}
	if strings.Contains(head, `wl-age`) && strings.Contains(head, "asked") {
		t.Errorf("the age is said twice: %s", head)
	}
}

// On a code host a bare #n is a pull request, so a knowledge entry is named KN-n in
// every pill, on the server and in the script; the other kinds keep #n.
func TestKnowledgePillSaysKNAndTheOthersSayHash(t *testing.T) {
	kn := string(numberedPill(langDE, "knowledge", 1970, "/ui/knowledge/1970", ""))
	for _, want := range []string{`data-ref="KN-1970"`, `<span class="num">KN-1970</span>`, `class="ref pill k-know"`} {
		if !strings.Contains(kn, want) {
			t.Errorf("knowledge pill lacks %q: %s", want, kn)
		}
	}
	if got := string(refPillHTML(langDE, "knowledge", 7, "")); !strings.Contains(got, `<span class="num">KN-7</span>`) {
		t.Errorf("refPillHTML of knowledge: %s", got)
	}
	if got := string(refPillHTML(langDE, "request", 7, "")); !strings.Contains(got, `<span class="num">#7</span>`) {
		t.Errorf("refPillHTML of a request: %s", got)
	}
	js := string(mustReadEmbeddedFile(t, "static/ui-pill.js"))
	if !strings.Contains(js, `m[1] === "KN" ? key : "#" + m[2]`) {
		t.Error("ui-pill.js does not write the knowledge pill as KN-n")
	}
}
