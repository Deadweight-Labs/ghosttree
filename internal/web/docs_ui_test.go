package web

import (
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type docsEnv struct {
	shellEnv
	shown, hidden store.Document
}

// newDocsEnv: a document in the shell project (owner, member and guest have a
// role there) with two revisions, the second written by alice's Claude, and
// one in the hidden project, which only alice belongs to.
func newDocsEnv(t *testing.T) docsEnv {
	t.Helper()
	e := ovEnv(t)
	d, err := e.St.CreateDocument(store.Document{Project: shellProject, Slug: "zebra-report", Kind: "report", Title: "Zebra report", Person: "alice"},
		"# Zebra report\n\n## Findings\n\nThe zebra crossing is REQ-1.\n\n## Next\n\nNothing yet.\n", "first draft")
	if err != nil {
		t.Fatal(err)
	}
	d, err = e.St.PushRevision(d.ID, 1, "# Zebra report\n\n## Findings\n\nThe zebra crossing is REQ-1.\n\n## Next\n\nShip the stripes.\n", "decide", "alice")
	if err != nil {
		t.Fatal(err)
	}
	head, err := e.St.DocumentRevision(d.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.DocRevision, head.ID, actor.Actor{Kind: actor.Agent, AccountID: 1, Harness: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	h, err := e.St.CreateDocument(store.Document{Project: shellHiddenProject, Slug: "secret-plan", Kind: "plan", Title: "SECRET plan", Person: "alice"}, "zebra secret\n", "m")
	if err != nil {
		t.Fatal(err)
	}
	return docsEnv{shellEnv: e, shown: d, hidden: h}
}

// The documents of a project follow the rights on the project's knowledge
// (ProjectAccess): owner, member and guest read them; whoever has no role in
// the project gets 404 for the list, the document, its text and its diff,
// exactly as for a document that does not exist.
func TestDocsRightsByRole(t *testing.T) {
	e := newDocsEnv(t)
	readers := map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest}
	for name, c := range readers {
		code, page := fetchPage(t, c, e.Base+"/ui/x/shell")
		if code != http.StatusOK || !strings.Contains(page, `href="/ui/x/shell/docs"`) {
			t.Errorf("%s: repository page %d without the docs tab", name, code)
		}
		code, page = fetchPage(t, c, e.Base+"/ui/x/shell/docs")
		if code != http.StatusOK || !strings.Contains(page, "Zebra report") || !strings.Contains(page, `href="/ui/x/shell/docs/zebra-report"`) {
			t.Errorf("%s: list %d, document missing", name, code)
		}
		if strings.Contains(page, "SECRET") {
			t.Errorf("%s: list shows a document of another project", name)
		}
		code, page = fetchPage(t, c, e.Base+"/ui/x/shell/docs/zebra-report")
		if code != http.StatusOK || !strings.Contains(page, "Ship the stripes.") || !strings.Contains(page, "data-md=") {
			t.Errorf("%s: document page %d", name, code)
		}
		resp, err := c.Get(e.Base + "/ui/x/shell/docs/zebra-report?raw=1&rev=1")
		if err != nil {
			t.Fatal(err)
		}
		raw := body(t, resp)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || !strings.Contains(raw, "Nothing yet.") {
			t.Errorf("%s: raw %d %q %q", name, resp.StatusCode, resp.Header.Get("Content-Type"), raw)
		}
		if code, _ := fetchPage(t, c, e.Base+"/ui/x/shell/docs/zebra-report?from=1&to=2"); code != http.StatusOK {
			t.Errorf("%s: diff %d", name, code)
		}
	}
	_, unknown := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/docs/no-such-doc")
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		for _, path := range []string{"/ui/x/hidden/docs", "/ui/x/hidden/docs/secret-plan", "/ui/x/hidden/docs/secret-plan?raw=1", "/ui/x/hidden/docs/secret-plan?from=1&to=1"} {
			code, page := fetchPage(t, c, e.Base+path)
			if code != http.StatusNotFound || strings.Contains(page, "SECRET") || strings.Contains(page, "zebra secret") {
				t.Errorf("%s (no role in hidden) %s = %d", name, path, code)
			}
		}
	}
	if code, page := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/docs/secret-plan"); stripCSRF(page) != stripCSRF(unknown) || code != http.StatusNotFound {
		t.Error("a hidden document answers differently from one that does not exist")
	}
	if code, page := fetchPage(t, e.Owner, e.Base+"/ui/x/hidden/docs/secret-plan"); code != http.StatusOK || !strings.Contains(page, "SECRET plan") {
		t.Errorf("owner of hidden: %d", code)
	}
	// Out of range: a revision that does not exist.
	if code, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/docs/zebra-report?rev=9"); code != http.StatusNotFound {
		t.Errorf("rev=9: %d", code)
	}
}

// A project without documents has no docs tab; the address still answers with
// the empty list.
func TestDocsTabOnlyWithDocuments(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell")
	if strings.Contains(page, `href="/ui/x/shell/docs"`) {
		t.Error("docs tab without documents")
	}
	if code, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/docs"); code != http.StatusOK || !strings.Contains(page, "No documents yet.") {
		t.Errorf("empty list: %d", code)
	}
}

// The page names the latest writer as the lists do (REQ-620): alice's Claude
// with the agent mark; the revisions link to each version and to the diff of
// each with the one before; "Copy link" copies the permanent address.
func TestDocPageRevisionsActorAndDiff(t *testing.T) {
	e := newDocsEnv(t)
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/docs")
	if !strings.Contains(list, "Claude") || !strings.Contains(list, "botmark") {
		t.Error("list does not show the agent that wrote the head")
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/docs/zebra-report")
	for _, want := range []string{
		`href="/ui/x/shell/docs/zebra-report?rev=1"`,
		`href="/ui/x/shell/docs/zebra-report?from=1&amp;to=2"`,
		`href="/ui/x/shell/docs/zebra-report?raw=1"`,
		`data-ui-copy="#doc-link"`, `id="doc-link">/ui/x/shell/docs/zebra-report</code>`,
		"first draft", "decide", "Claude",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("document page lacks %s", want)
		}
	}
	_, old := fetchPage(t, e.Member, e.Base+"/ui/x/shell/docs/zebra-report?rev=1")
	if !strings.Contains(old, "Nothing yet.") || strings.Contains(old, "Ship the stripes.") {
		t.Error("rev=1 does not show the first revision")
	}
	_, diff := fetchPage(t, e.Member, e.Base+"/ui/x/shell/docs/zebra-report?from=2&to=1")
	if !strings.Contains(diff, `class="dl del"`) || !strings.Contains(diff, "Nothing yet.") || !strings.Contains(diff, `class="dl add"`) || !strings.Contains(diff, "Ship the stripes.") {
		t.Error("diff does not show the changed line on both sides")
	}
}

// Markdown is never HTML: the server writes the source escaped into data-md
// and its paragraphs as text; ui-md.js builds DOM nodes from it (see
// TestUiMdDropsScriptLinks). The raw text is text/plain that no browser runs.
func TestDocMarkdownIsEscapedAndRawIsInert(t *testing.T) {
	e := ovEnv(t)
	body := "# T\n\n## <img src=x onerror=alert(1)>\n\n## Two\n\n<script>alert(1)</script>\n\n[x](javascript:alert(1)) \"quoted\" & more\n"
	if _, err := e.St.CreateDocument(store.Document{Project: shellProject, Slug: "evil", Kind: "other", Title: "<b>Evil</b>", Person: "alice"}, body, "m"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/docs/evil")
	for _, bad := range []string{"<script>alert", "<img src=x", "<b>Evil</b>", `href="javascript:`} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the source is not in the page, escaped")
	}
	resp, err := e.Member.Get(e.Base + "/ui/x/shell/docs/evil?raw=1")
	if err != nil {
		t.Fatal(err)
	}
	raw := body2(t, resp)
	if raw != body {
		t.Errorf("raw text changed: %q", raw)
	}
	h := resp.Header
	if h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("raw headers: %v", h)
	}
}

func body2(t *testing.T, resp *http.Response) string { t.Helper(); return body(t, resp) }

// The documents appear in the search as a group of their own, with the line
// the words were found in, only for whoever may read them; is:doc narrows the
// page to them.
func TestSearchFindsDocumentsWithSnippet(t *testing.T) {
	e := newDocsEnv(t)
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		_, page := fetchPage(t, c, e.Base+"/ui/search?q="+url.QueryEscape("stripes"))
		if !strings.Contains(page, `href="/ui/x/shell/docs/zebra-report"`) || !strings.Contains(page, "Ship the stripes.") {
			t.Errorf("%s: document not found by its text", name)
		}
		_, page = fetchPage(t, c, e.Base+"/ui/search?q="+url.QueryEscape("zebra"))
		if strings.Contains(page, "SECRET") || strings.Contains(page, "secret-plan") {
			t.Errorf("%s finds a document of a project without role", name)
		}
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/search?q="+url.QueryEscape("is:doc zebra"))
	if !strings.Contains(page, "/ui/x/hidden/docs/secret-plan") || !strings.Contains(page, "/ui/x/shell/docs/zebra-report") {
		t.Error("owner: is:doc does not list both documents")
	}
	if strings.Contains(page, "search.err.kinds") {
		t.Error("is:doc is called a kind the page does not show")
	}
}

// A link handed out before a rename leads to the document under its new slug,
// with what the link asked for; for whoever may not read it, it stays unknown.
func TestRenamedDocumentRedirectsFromItsOldSlug(t *testing.T) {
	e := newDocsEnv(t)
	for _, d := range []store.Document{e.shown, e.hidden} {
		if err := e.St.PatchDocument(d.ID, map[string]string{"slug": d.Slug + "-v2"}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := e.Member.Get(e.Base + "/ui/x/shell/docs/zebra-report?rev=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/x/shell/docs/zebra-report-v2?rev=1" {
		t.Errorf("member: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/docs/secret-plan"); code != http.StatusNotFound {
		t.Errorf("member follows the old slug of a hidden document: %d", code)
	}
	resp, err = e.Owner.Get(e.Base + "/ui/x/hidden/docs/secret-plan")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/x/hidden/docs/secret-plan-v2" {
		t.Errorf("owner: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// Across projects, "show all" of the documents is this search narrowed to
// documents, not the list of the project of one hit.
func TestSearchShowAllDocumentsAcrossProjects(t *testing.T) {
	e := ovEnv(t)
	for i := 0; i < searchPerGroup+1; i++ {
		slug := "walrus-" + string(rune('a'+i))
		if _, err := e.St.CreateDocument(store.Document{Project: shellProject, Slug: slug, Kind: "other", Title: "Walrus " + slug, Person: "alice"}, "walrus text\n", "m"); err != nil {
			t.Fatal(err)
		}
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/search?q=walrus")
	if !strings.Contains(page, `href="/ui/search?kind=docs&amp;q=is%3Adoc&#43;walrus"`) {
		t.Error("show all does not narrow this search to documents")
	}
	_, page = fetchPage(t, e.Owner, e.Base+"/ui/search?q=walrus&project="+url.QueryEscape(shellProject))
	if !strings.Contains(page, `href="/ui/x/shell/docs"`) {
		t.Error("show all within a project is not its list of documents")
	}
}

func TestDocLineDiffKeepsContextAroundChanges(t *testing.T) {
	var a, b []string
	for i := 0; i < 20; i++ {
		a = append(a, "line "+string(rune('a'+i)))
	}
	b = append(b, a...)
	b[10] = "changed"
	lines, added, removed := docLineDiff(strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n")
	if added != 1 || removed != 1 {
		t.Fatalf("added %d removed %d", added, removed)
	}
	var ops []string
	for _, l := range lines {
		ops = append(ops, l.Op)
	}
	want := "skip ctx ctx ctx del add ctx ctx ctx skip"
	if strings.Join(ops, " ") != want {
		t.Errorf("ops = %v, want %s", ops, want)
	}
	if lines[4].Old != 11 || lines[5].New != 11 {
		t.Errorf("line numbers %+v %+v", lines[4], lines[5])
	}
	if l, a2, r2 := docLineDiff("same\n", "same\n"); len(l) != 0 || a2 != 0 || r2 != 0 {
		t.Errorf("same text: %+v", l)
	}
}

// ui-md.js turns only http(s) and relative addresses into links: javascript:,
// data: and other schemes stay text.
func TestUiMdDropsScriptLinks(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := string(mustReadEmbedded(t, "static/ui-md.js"))
	start := strings.Index(src, "  function safeUrl(")
	end := strings.Index(src, "  // Address of a reference")
	if start < 0 || end <= start {
		t.Fatal("safeUrl is not independently testable")
	}
	program := src[start:end] + `
const bad = ["javascript:alert(1)", "JaVaScRiPt:alert(1)", " javascript:alert(1)", "java\tscript:alert(1)", "data:text/html,x", "vbscript:x", "//evil.example", "\u0000javascript:x"];
for (const u of bad) if (safeUrl(u) !== null) throw new Error("allowed " + JSON.stringify(u));
for (const u of ["https://example.com/a", "/ui/x/shell/docs/a", "#h-one"]) if (safeUrl(u) === null) throw new Error("refused " + u);
`
	if out, err := exec.Command("node", "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
