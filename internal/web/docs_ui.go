package web

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// docRow is one document in the list of a repository.
type docRow struct {
	Title, Slug, Href, Kind string
	Rev                     int
	At, Person              string
	Actor                   actorView
}

type docsView struct {
	Rows []docRow
}

type docRevRow struct {
	N                                   int
	Message, Person, At, Href, DiffHref string
	Actor                               actorView
	Current                             bool
}

// docDiffLine is one line of a comparison: Op is add, del, ctx or skip (the
// unchanged lines left out between two changes).
type docDiffLine struct {
	Op       string
	Text     string
	Old, New int
}

type docDiffView struct {
	From, To       int
	Lines          []docDiffLine
	Added, Removed int
}

type docPageView struct {
	Title, Slug, Kind string
	Archived          bool
	// Rev is the revision shown, Head the newest; Old says they differ.
	Rev, Head int
	Old       bool
	// Raw is the Markdown source (ui-md.js renders it, as for comments),
	// Blocks the plain paragraphs a page without script shows.
	Raw    string
	Blocks []bodyBlock
	Revs   []docRevRow
	// Href is the permanent address of the document, Link the same with the
	// public origin when the server knows it (what "Copy link" copies),
	// RawHref its text.
	Href, Link, RawHref string
	Person, At          string
	Actor               actorView
	Diff                *docDiffView
	// CmpFrom and CmpTo are the revisions the compare form starts with.
	CmpFrom, CmpTo int
}

// docsHref is the list of documents of a project, docHref one document.
func docsHref(project string) string { return repoHref(project) + "/docs" }

func docHref(project, slug string) string {
	return docsHref(project) + "/" + url.PathEscape(slug)
}

// visibleDocs are the active documents of a project the viewer may read, in
// the order of the store (kind, then slug).
func (a *app) visibleDocs(pa *store.ProjectAccess, project string) []store.Document {
	ds, err := a.store.Documents(project, "", false)
	if err != nil {
		return nil
	}
	return keep(ds, 0, pa.CanSeeDocument)
}

// docCount is the number of active documents of the project the viewer may
// read, all of them (countText caps what the tab shows). The tab shows only
// when there is one.
func (a *app) docCount(pa *store.ProjectAccess, remote string) int {
	if pa == nil || pa.GateList(remote, store.ResDocument, true) != nil {
		return 0
	}
	return len(a.visibleDocs(pa, remote))
}

// docActors names the writer of each revision, by revision row id: names only,
// as in every list (REQ-620).
func (a *app) docActors(l lang, revs []store.DocumentRevision) map[int64]actorView {
	ids := make([]int64, 0, len(revs))
	for _, r := range revs {
		ids = append(ids, r.ID)
	}
	out := map[int64]actorView{}
	found, err := a.store.ActorsOf(actor.DocRevision, ids)
	if err != nil {
		return out
	}
	for _, r := range revs {
		ea, ok := found[r.ID]
		if !ok || !ea.IsAgent() || r.Person == "" {
			continue
		}
		out[r.ID] = actorView{Person: r.Person, Agent: true, Kind: harnessKind(ea.Harness),
			Label: possessive(l, r.Person) + " " + actor.Family(ea.Harness)}
	}
	return out
}

// docKindKeys name the kinds of document ctx doc knows; another kind shows as
// it is stored.
var docKindKeys = map[string]string{"spec": "docs.kind.spec", "plan": "docs.kind.plan",
	"investigation": "docs.kind.investigation", "report": "docs.kind.report", "other": "docs.kind.other"}

func docKindLabel(l lang, kind string) string {
	if key, ok := docKindKeys[kind]; ok {
		return l.msg(key)
	}
	return kind
}

// docsPage lists the documents of the repository in the address.
func (a *app) docsPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	rv := repoFrom(r)
	if rv == nil {
		http.NotFound(w, r)
		return
	}
	project := rv.Project
	pa := a.access(r)
	hidden, stop := a.gateList(w, r, pa, project, store.ResDocument)
	if stop {
		return
	}
	v := &docsView{}
	if !hidden {
		docs := a.visibleDocs(pa, project)
		ids := make([]int64, len(docs))
		for i, d := range docs {
			ids[i] = d.ID
		}
		heads, _ := a.store.DocumentHeads(ids)
		var revs []store.DocumentRevision
		for _, h := range heads {
			revs = append(revs, h)
		}
		actors := a.docActors(l, revs)
		for _, d := range docs {
			row := docRow{Title: d.Title, Slug: d.Slug, Href: docHref(project, d.Slug), Kind: docKindLabel(l, d.Kind),
				Rev: d.HeadRevision, At: d.UpdatedAt, Person: d.Person}
			if h, ok := heads[d.ID]; ok {
				row.At, row.Person, row.Actor = h.CreatedAt, h.Person, actors[h.ID]
			}
			v.Rows = append(v.Rows, row)
		}
	}
	a.renderBrowser(w, r, "docs", pageData{Title: l.msg("repo.tab.docs"), Project: project, DocsV: v})
}

// docPage is one document: a revision (?rev=, the head without), its text as
// text/plain (?raw=1) or the comparison of two revisions (?from=&to=). A
// document the viewer may not read answers like one that does not exist.
func (a *app) docPage(w http.ResponseWriter, r *http.Request, remote, slug string) {
	l := a.langFor(r)
	pa := a.access(r)
	d, err := a.store.DocumentBySlug(remote, slug)
	if err != nil {
		// An address handed out before a rename leads to the document under its
		// new slug, for whoever may read it; for everyone else it stays unknown.
		if moved, err := a.store.DocumentMovedFrom(remote, slug); err == nil && pa.CanSeeDocument(moved) {
			http.Redirect(w, r, withRawQuery(docHref(remote, moved.Slug), r), http.StatusFound)
			return
		}
		pa.Filtered()
		http.NotFound(w, r)
		return
	}
	if !pa.CanSeeDocument(d) {
		pa.Filtered()
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	revNo := d.HeadRevision
	if s := q.Get("rev"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > d.HeadRevision {
			http.NotFound(w, r)
			return
		}
		revNo = n
	}
	rev, err := a.store.DocumentRevision(d.ID, revNo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if q.Get("raw") == "1" {
		writeDocRaw(w, rev.Body)
		return
	}
	base := docHref(remote, d.Slug)
	v := &docPageView{Title: d.Title, Slug: d.Slug, Kind: docKindLabel(l, d.Kind), Archived: d.Status != "active",
		Rev: revNo, Head: d.HeadRevision, Old: revNo != d.HeadRevision, Href: base, Person: rev.Person, At: rev.CreatedAt}
	v.Link = a.publicOrigin + base
	v.RawHref = base + "?raw=1"
	if v.Old {
		v.RawHref += "&rev=" + strconv.Itoa(revNo)
	}
	v.Actor = a.actorViewOf(l, pa, actor.DocRevision, rev.ID, rev.Person, true)
	if from, to, ok := docCompare(q, d.HeadRevision); ok {
		older, err1 := a.store.DocumentRevision(d.ID, from)
		newer, err2 := a.store.DocumentRevision(d.ID, to)
		if err1 != nil || err2 != nil {
			http.NotFound(w, r)
			return
		}
		lines, added, removed := docLineDiff(older.Body, newer.Body)
		v.Diff = &docDiffView{From: from, To: to, Lines: lines, Added: added, Removed: removed}
	} else {
		v.Raw = rev.Body
		v.Blocks = bodyBlocks(rev.Body, 0)
	}
	v.CmpFrom, v.CmpTo = max(1, revNo-1), max(revNo, min(2, d.HeadRevision))
	if v.Diff != nil {
		v.CmpFrom, v.CmpTo = v.Diff.From, v.Diff.To
	}
	revs, err := a.store.DocumentRevisions(d.ID)
	if err == nil {
		actors := a.docActors(l, revs)
		for _, x := range revs {
			row := docRevRow{N: x.Revision, Message: x.Message, Person: x.Person, At: x.CreatedAt, Actor: actors[x.ID],
				Href: base, Current: x.Revision == revNo && v.Diff == nil}
			if x.Revision != d.HeadRevision {
				row.Href = base + "?rev=" + strconv.Itoa(x.Revision)
			}
			if x.Revision > 1 {
				row.DiffHref = base + "?from=" + strconv.Itoa(x.Revision-1) + "&to=" + strconv.Itoa(x.Revision)
			}
			v.Revs = append(v.Revs, row)
		}
	}
	r = a.withRepoBand(r, remote, "docs")
	a.renderBrowser(w, r, "doc", pageData{Title: d.Title, Project: remote, DocV: v})
}

// docCompare reads ?from=&to= : two different revisions of the document, the
// older first whatever order the address names them in.
func docCompare(q url.Values, head int) (from, to int, ok bool) {
	if q.Get("from") == "" && q.Get("to") == "" {
		return 0, 0, false
	}
	from, err1 := strconv.Atoi(q.Get("from"))
	to, err2 := strconv.Atoi(q.Get("to"))
	if q.Get("to") == "" {
		to, err2 = head, nil
	}
	if err1 != nil || err2 != nil || from < 1 || to < 1 || from > head || to > head || from == to {
		return 0, 0, false
	}
	if from > to {
		from, to = to, from
	}
	return from, to, true
}

// writeDocRaw answers with the text of a revision exactly as stored. It is
// never a page: nosniff and a sandbox keep a browser from treating Markdown
// that contains markup as HTML.
func writeDocRaw(w http.ResponseWriter, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	h.Set("Cache-Control", "private, no-store")
	_, _ = io.WriteString(w, body)
}

// ---------------------------------------------------------------- diff

const (
	// docDiffCells caps the table of the comparison; a bigger change shows
	// the old lines as removed and the new ones as added.
	docDiffCells = 2_000_000
	docDiffCtx   = 3
)

// docLineDiff compares two texts line by line (longest common subsequence
// after the shared beginning and end are cut off) and keeps docDiffCtx
// unchanged lines around each change.
func docLineDiff(oldText, newText string) (lines []docDiffLine, added, removed int) {
	a, b := splitDocLines(oldText), splitDocLines(newText)
	ia, ib := lineIDs(a, b)
	p := 0
	for p < len(a) && p < len(b) && ia[p] == ib[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && ia[len(a)-1-s] == ib[len(b)-1-s] {
		s++
	}
	var all []docDiffLine
	oi, ni := 0, 0
	emit := func(op, text string) {
		l := docDiffLine{Op: op, Text: text}
		switch op {
		case "ctx":
			oi, ni = oi+1, ni+1
			l.Old, l.New = oi, ni
		case "del":
			oi++
			l.Old = oi
			removed++
		case "add":
			ni++
			l.New = ni
			added++
		}
		all = append(all, l)
	}
	for _, t := range a[:p] {
		emit("ctx", t)
	}
	am, bm := a[p:len(a)-s], b[p:len(b)-s]
	for _, op := range lcsLines(ia[p:len(a)-s], ib[p:len(b)-s]) {
		if op.op == "add" {
			emit("add", bm[op.j])
		} else {
			emit(op.op, am[op.i])
		}
	}
	for _, t := range a[len(a)-s:] {
		emit("ctx", t)
	}
	if added == 0 && removed == 0 {
		return nil, 0, 0
	}
	// Keep the context around changes, one skip line for each cut.
	near := make([]bool, len(all))
	for i, l := range all {
		if l.Op == "ctx" {
			continue
		}
		for k := max(0, i-docDiffCtx); k <= min(len(all)-1, i+docDiffCtx); k++ {
			near[k] = true
		}
	}
	gap := false
	for i, l := range all {
		if l.Op != "ctx" || near[i] {
			lines = append(lines, l)
			gap = false
			continue
		}
		if !gap {
			lines = append(lines, docDiffLine{Op: "skip"})
			gap = true
		}
	}
	return lines, added, removed
}

type lineOp struct {
	op   string
	i, j int
}

// lineIDs numbers the distinct lines of both texts, so that the comparison
// compares integers instead of strings in its inner loop.
func lineIDs(a, b []string) (ia, ib []int32) {
	ids := map[string]int32{}
	number := func(lines []string) []int32 {
		out := make([]int32, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = int32(len(ids))
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	return number(a), number(b)
}

func lcsLines(a, b []int32) []lineOp {
	n, m := len(a), len(b)
	var out []lineOp
	if n*m > docDiffCells {
		for i := range a {
			out = append(out, lineOp{"del", i, 0})
		}
		for j := range b {
			out = append(out, lineOp{"add", 0, j})
		}
		return out
	}
	w := m + 1
	t := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				t[i*w+j] = t[(i+1)*w+j+1] + 1
			case t[(i+1)*w+j] >= t[i*w+j+1]:
				t[i*w+j] = t[(i+1)*w+j]
			default:
				t[i*w+j] = t[i*w+j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, lineOp{"ctx", i, j})
			i, j = i+1, j+1
		case t[(i+1)*w+j] >= t[i*w+j+1]:
			out = append(out, lineOp{"del", i, j})
			i++
		default:
			out = append(out, lineOp{"add", i, j})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, lineOp{"del", i, 0})
	}
	for ; j < m; j++ {
		out = append(out, lineOp{"add", 0, j})
	}
	return out
}

func splitDocLines(s string) []string {
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// ---------------------------------------------------------------- search

// searchDocs is the group of documents on the search page: the active
// documents the viewer may read whose title, slug or text match, each with
// the line of its text the words are found in.
func (a *app) searchDocs(l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
	g := searchGroup{Key: "docs", Label: l.msg("repo.tab.docs"), Icon: "file"}
	text := strings.TrimSpace(sl.text)
	if !sl.wants(query.KindDocument) || text == "" {
		return g
	}
	if sl.project != "" && pa.GateList(sl.project, store.ResDocument, true) != nil {
		return g
	}
	pa.Filtered()
	keepDoc := pa.CanSeeDocument
	if sl.qualified {
		keepDoc = func(d store.Document) bool {
			return pa.CanSeeDocument(d) && sl.q.Match(query.Facts{Kind: query.KindDocument, Repo: d.Project, State: d.Status,
				Type: d.Kind, Author: d.Person, Created: d.CreatedAt, Updated: d.UpdatedAt})
		}
	}
	docs, err := st.SearchDocuments(text, store.DocumentSearch{Project: sl.project, Limit: a.overfetch(searchPerGroup + 1),
		Keep: keepDoc, Prefilter: pa.DocumentPrefilter()})
	if err != nil {
		return g
	}
	docs = keep(docs, 0, keepDoc)
	words := strings.Fields(strings.ToLower(text))
	for i, d := range docs {
		if i == searchPerGroup {
			// More hits: within one project its list of documents, across
			// projects this search narrowed to documents.
			if sl.project != "" {
				g.All = docsHref(sl.project)
			} else {
				g.All = "/ui/search?" + url.Values{"q": {"is:doc " + line}, "kind": {"docs"}}.Encode()
			}
			break
		}
		o := originOf(l, now, d.UpdatedAt, store.ObjectFacts{})
		hit := searchHit{Title: d.Title, Sub: docKindLabel(l, d.Kind) + " · " + d.Slug, Project: lastSegment(d.Project),
			Href: docHref(d.Project, d.Slug), Date: o.Date, Stamp: o.Stamp}
		if rev, err := st.DocumentRevision(d.ID, d.HeadRevision); err == nil {
			hit.Snip = docSnippet(rev.Body, words)
		}
		g.Hits = append(g.Hits, hit)
	}
	return g
}

// mdLinkRE is a Markdown link; a snippet keeps only its text.
var mdLinkRE = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// docSnippet is the first line of a text that holds one of the words, plain
// and cut to about 160 characters around it; empty when no line does.
func docSnippet(body string, words []string) string {
	fence := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			fence = !fence
			continue
		}
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		at := -1
		for _, w := range words {
			if i := strings.Index(low, w); i >= 0 && (at < 0 || i < at) {
				at = i
			}
		}
		if at < 0 {
			continue
		}
		t = strings.TrimSpace(strings.TrimLeft(t, "#>-*+ "))
		t = mdLinkRE.ReplaceAllString(t, "$1")
		t = strings.NewReplacer("**", "", "`", "").Replace(t)
		return clipAround(t, strings.ToLower(t), words, 160)
	}
	return ""
}

func clipAround(t, low string, words []string, limit int) string {
	if utf8.RuneCountInString(t) <= limit {
		return t
	}
	at := 0
	for _, w := range words {
		if i := strings.Index(low, w); i >= 0 {
			at = i
			break
		}
	}
	runes := []rune(t)
	pos := utf8.RuneCountInString(t[:min(at, len(t))])
	start := max(0, pos-limit/3)
	end := min(len(runes), start+limit)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
