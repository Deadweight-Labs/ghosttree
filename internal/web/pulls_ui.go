package web

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Pull Requests in der Oberfläche (REQ-599): der Reiter „Pull Requests“ eines
// Repositorys mit der Liste und die Seite eines PRs unter
// /ui/{org}/{repo}/pulls/{n}. Alles liest über ProjectAccess; ein PR, den der
// Betrachter nicht lesen darf, ist hier so unbekannt wie einer, den es nicht
// gibt. Was GitHub dem Server nicht meldet (Beschreibung, Reviewer, Checks,
// Merge-Schaltfläche), steht nicht auf der Seite und wird nicht ersetzt: dafür
// führt „Auf GitHub öffnen“ in den Seitenkopf.

const (
	pullsPerPage     = 30
	pullsTextScan    = 500
	pullSideRequests = 6
	pullKnowledgeMax = 100
	pullQueryMax     = 300
	pullDefaultQuery = "is:pr is:open"
)

var pullIsStates = []string{"open", "merged", "closed", "all", "any"}

// pullQuery ist der Suchtext als die Filter, die der Server hat.
type pullQuery struct {
	// State: open, merged, closed; leer heißt alle.
	State, Branch, Author string
	// Package ist die Nummer des Arbeitspakets, auf das die Liste begrenzt ist
	// (package:N); Number dann die Nummer seines PRs, NoPull: das Paket gibt es
	// nicht (für den Betrachter) oder hat keinen PR, die Liste ist leer.
	Package, Number int64
	NoPull          bool
	// Text bleibt für die Suche im Titel übrig.
	Text        string
	Unsupported []string
}

// parsePullQuery liest is:, branch: und author:. Jedes andere Wort:Wert wird als
// nicht unterstützt gemeldet und gehört nicht zum Text.
func parsePullQuery(q string) pullQuery {
	pq := pullQuery{State: store.PullOpen}
	var text []string
	for _, tok := range strings.Fields(q) {
		key, val, ok := strings.Cut(tok, ":")
		key = strings.ToLower(key)
		if _, isNumber := links.ParseNumber(tok); isNumber || !ok || val == "" || !qualifierKey.MatchString(key) || strings.HasPrefix(val, "//") {
			// A number (#217, pr:217) is a word of the text: pullMatches reads it.
			text = append(text, tok)
			continue
		}
		valid := true
		switch key {
		case "is":
			switch v := strings.ToLower(val); v {
			case "pr", "pull":
			case store.PullOpen, store.PullMerged, store.PullClosed:
				pq.State = v
			case "all", "any":
				pq.State = ""
			default:
				valid = false
			}
		case "branch":
			pq.Branch = val
		case "author":
			pq.Author = val
		case "package":
			pq.Package, valid = parsePackageTerm(val)
		default:
			valid = false
		}
		if !valid {
			pq.Unsupported = append(pq.Unsupported, tok)
		}
	}
	pq.Text = strings.Join(text, " ")
	return pq
}

// pullMatches: jedes Wort steht im Titel, in einem Branch, den der Betrachter
// sehen darf, oder ist die Nummer. Ein Branch, dessen Namen er nicht sehen
// darf, zählt nicht mit: sonst ließe sich über die Suche erraten, wie er heißt.
func pullMatches(p store.PullSummary, text string, visible branchVisible) bool {
	hay := p.Title
	if visible(p.Head) {
		hay += " " + p.Head
	}
	if visible(p.Base) {
		hay += " " + p.Base
	}
	hay = strings.ToLower(hay)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		if pullNumberWord(w, p.Number) {
			continue
		}
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// pullNumberWord says whether the word is the number of the pull request: #217
// or pr:217 as the search reads it (links.ParseNumber), or the plain number.
func pullNumberWord(w string, number int64) bool {
	if t, ok := links.ParseNumber(w); ok {
		return t.N == number && t.Is(links.KindPull)
	}
	return w == strconv.FormatInt(number, 10)
}

type pullsView struct {
	Q, Base        string
	Tabs           []issueStateTab
	Menus          []issueMenu
	Unsupported    []string
	Rows           []uiRow
	Older          string
	Empty, NoMatch bool
	EmptyKey       string
	ClearHref      string
	// GitHub und GitHubTip: „Auf GitHub öffnen“ für die Liste. NewHref ist der
	// Weg, auf GitHub einen neuen PR zu beginnen.
	GitHub, GitHubTip, NewHref string
}

// pullConventional liest die Art aus dem Anfang des Titels („feat(web): …“).
var pullConventional = regexp.MustCompile(`^([a-z]+)(?:\([^)]*\))?!?:`)

var pullKindKeys = map[string]string{"feat": "pulls.kind.feat", "fix": "pulls.kind.fix", "test": "pulls.kind.test", "docs": "pulls.kind.docs",
	"chore": "pulls.kind.chore", "ci": "pulls.kind.ci", "refactor": "pulls.kind.refactor", "perf": "pulls.kind.perf", "build": "pulls.kind.build",
	"merge": "pulls.kind.merge", "release": "pulls.kind.release", "revert": "pulls.kind.revert"}

func pullKind(l lang, title string) string {
	if m := pullConventional.FindStringSubmatch(title); m != nil {
		if key, ok := pullKindKeys[m[1]]; ok {
			return l.msg(key)
		}
		return ""
	}
	if first, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(title)), " "); first == "release" {
		return l.msg(pullKindKeys["release"])
	}
	return ""
}

var pullStateKeys = map[string]string{store.PullOpen: "pulls.state.open", store.PullMerged: "pulls.state.merged", store.PullClosed: "pulls.state.closed"}

// pullStLabelKeys name the state for screen readers on a row of the list.
var pullStLabelKeys = map[string]string{store.PullOpen: "pulls.st.open", store.PullMerged: "pulls.st.merged", store.PullClosed: "pulls.st.closed"}

// pullEmptyKeys are the empty lines of the list by the state it shows.
var pullEmptyKeys = map[string]string{store.PullOpen: "pulls.empty.open", store.PullMerged: "pulls.empty.merged", store.PullClosed: "pulls.empty.closed", "": "pulls.empty.all"}

// pullState sind Symbol und Farbe eines Zustands in der Liste und im Seitenkopf.
func pullStateLook(state string) (icon, tone, pill string) {
	switch state {
	case store.PullMerged:
		return "merge", "merged", "done"
	case store.PullClosed:
		return "pr", "drop", "gray"
	}
	return "pr", "open", "open"
}

// pullError antwortet auf einen Fehler der Store-Aufrufe: unbekannt und
// unlesbar als 404, fehlendes Recht als 403.
func (a *app) pullError(w http.ResponseWriter, r *http.Request, err error) {
	// A rule error (a hash that is none, one that is ambiguous) is unknown here too.
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrAccessNotFound) || requestdomain.Code(err) != "" {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrAccessForbidden) {
		a.accessDenied(w, r, err)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// repoDetailRequest ist die Anfrage einer Detailseite im Repository: das
// Projekt aus der Adresse, der Kopf des Repositorys mit dem offenen Reiter.
func (a *app) repoDetailRequest(r *http.Request, pa *store.ProjectAccess, remote, tab string) (*http.Request, *repoView) {
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, tab)
	return r2.WithContext(context.WithValue(r2.Context(), repoCtxKey{}, view)), view
}

// pullCount ist die Zahl der offenen PRs für den Reiter.
func (a *app) pullCount(pa *store.ProjectAccess, remote string) string {
	list, err := pa.PullRequests(remote, store.PullListFilter{State: store.PullOpen, Limit: 1})
	if err != nil {
		return ""
	}
	return countText(list.Counts.Open)
}

// pullRows holt die Seite der Liste. Mit Suchtext im Titel liest sie bis
// pullsTextScan PRs aller Zustände und zählt die Reiter aus den Treffern.
func pullRows(pa *store.ProjectAccess, project string, pq pullQuery, offset int, visible branchVisible) (items []store.PullSummary, counts store.PullCounts, total int, err error) {
	f := store.PullListFilter{State: pq.State, Branch: pq.Branch, Author: pq.Author}
	if pq.NoPull {
		return nil, store.PullCounts{}, 0, nil
	}
	if pq.Text == "" && pq.Number == 0 {
		f.Limit, f.Offset = pullsPerPage, offset
		list, err := pa.PullRequests(project, f)
		return list.Items, list.Counts, list.Total, err
	}
	f.State = ""
	var inState []store.PullSummary
	for off := 0; off < pullsTextScan; off += store.PullMaxLimit {
		f.Limit, f.Offset = store.PullMaxLimit, off
		list, err := pa.PullRequests(project, f)
		if err != nil {
			return nil, counts, 0, err
		}
		for _, it := range list.Items {
			if pq.Number != 0 && it.Number != pq.Number || !pullMatches(it, pq.Text, visible) {
				continue
			}
			switch it.State {
			case store.PullOpen:
				counts.Open++
			case store.PullMerged:
				counts.Merged++
			case store.PullClosed:
				counts.Closed++
			}
			if pq.State == "" || it.State == pq.State {
				inState = append(inState, it)
			}
		}
		if len(list.Items) == 0 || off+len(list.Items) >= list.Total {
			break
		}
	}
	total = len(inState)
	if offset >= total {
		return nil, counts, total, nil
	}
	return inState[offset:min(offset+pullsPerPage, total)], counts, total, nil
}

// pullPackage ist, was ein Arbeitspaket zu einer Zeile der Liste beiträgt.
type pullPackage struct {
	Done, Total int
	Waiting     []string
}

// pullPackages liest zu den PRs der Seite ihr Arbeitspaket: Fortschritt der
// Kriterien und wer auf eine offene Aufgabe wartet, soweit der Betrachter die
// Requests lesen darf.
func (a *app) pullPackages(pa *store.ProjectAccess, project string, items []store.PullSummary) map[int64]pullPackage {
	out := map[int64]pullPackage{}
	if len(items) == 0 {
		return out
	}
	tasks := a.openTasksByRequest(pa, project)
	for _, it := range items {
		d, err := a.store.PackageForPR(project, it.Number)
		if err != nil {
			continue
		}
		if p, ok := a.packageStanding(pa, d, tasks); ok {
			out[it.Number] = p
		}
	}
	return out
}

// branchVisible says whether the viewer may see the name of a branch: all of
// them from member up, the default branch alone for a guest (a branch name can
// give away what a guest may not read, as on the code page).
type branchVisible func(name string) bool

func (a *app) branchRule(r *http.Request, pa *store.ProjectAccess, project string) branchVisible {
	names, _ := a.codeBranches(r, pa, project)
	all := a.seesAllBranches(pa, project)
	return func(name string) bool { return all || slices.Contains(names, name) }
}

// hiddenBranchChip stands where the name of a branch the viewer may not see would be.
func hiddenBranchChip(l lang) template.HTML {
	return kindPill(l, "lock", l.msg("pulls.branch_hidden"), "", pillCard{})
}

// branchChipHTML is a branch as the pill of pill.go: cut when it is long, with its whole name on the card.
func branchChipHTML(l lang, label, href string) template.HTML {
	return branchPill(l, label, href)
}

// pullsPage ist /ui/{org}/{repo}/pulls.
func (a *app) pullsPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	view := repoFrom(r)
	project := view.Project
	pa := a.access(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if rs := []rune(q); len(rs) > pullQueryMax {
		q = string(rs[:pullQueryMax])
	}
	shown := q
	if shown == "" {
		shown = pullDefaultQuery
	}
	pq := parsePullQuery(shown)
	visible := a.branchRule(r, pa, project)
	if pq.Branch != "" && !a.seesAllBranches(pa, project) {
		// A guest has no branch filter: the filter would tell which branches exist.
		pq.Unsupported, pq.Branch = append(pq.Unsupported, "branch:"+pq.Branch), ""
	}
	packages := packageChoices(pa, project)
	if pq.Package != 0 {
		p, ok := packageNumbered(packages, pq.Package)
		pq.Number, pq.NoPull = p.PR, !ok || p.PR == 0
	}
	offset := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && n > 0 {
		offset = min(n, 100000)
	}
	v := &pullsView{Q: shown, Base: view.Href + "/pulls", Unsupported: pq.Unsupported, ClearHref: view.Href + "/pulls"}
	href := func(text string) string {
		if text = strings.TrimSpace(text); text == "" {
			return v.Base
		}
		return v.Base + "?q=" + url.QueryEscape(text)
	}
	items, counts, total, err := pullRows(pa, project, pq, offset, visible)
	if err != nil {
		a.pullError(w, r, err)
		return
	}
	pkgs := a.pullPackages(pa, project, items)
	now := overviewNow().UTC()
	for _, it := range items {
		v.Rows = append(v.Rows, a.pullRow(l, v.Base, shown, it, pkgs[it.Number], now, visible))
	}
	if offset+len(items) < total {
		v.Older = v.Base + "?" + url.Values{"q": {shown}, "offset": {strconv.Itoa(offset + pullsPerPage)}}.Encode()
	}
	tab := func(icon, label string, count int, state string, show bool) {
		if !show {
			return
		}
		v.Tabs = append(v.Tabs, issueStateTab{Icon: icon, Label: label, Count: strconv.Itoa(count), Href: href(withQualifier(shown, dropIs(pullIsStates...), "is:"+state)), On: pq.State == state})
	}
	tab("pr", "pulls.tab.open", counts.Open, store.PullOpen, true)
	tab("merge", "pulls.tab.merged", counts.Merged, store.PullMerged, true)
	tab("drop", "pulls.tab.closed", counts.Closed, store.PullClosed, counts.Closed > 0 || pq.State == store.PullClosed)
	v.Menus = a.pullMenus(l, pa, project, shown, pq, href, visible)
	if m, ok := packageMenu(l, shown, pq.Package, packages, func(p requestdomain.PackageSummary) bool { return p.PR > 0 }, href); ok {
		v.Menus = append(v.Menus, m)
	}
	filtered := q != "" && q != pullDefaultQuery
	v.Empty = len(v.Rows) == 0 && !filtered && offset == 0 && len(pq.Unsupported) == 0
	v.NoMatch = len(v.Rows) == 0 && !v.Empty
	switch {
	case counts.Open+counts.Merged+counts.Closed == 0 && v.Empty:
		v.EmptyKey = "pulls.empty_none"
	case v.Empty:
		v.EmptyKey = pullEmptyKeys[pq.State]
	}
	if view.External != "" {
		v.GitHub, v.GitHubTip = view.External+"/pulls", l.msg("pulls.gh_list")
		if a.shellBaseFor(r).who.kind != viewerGuest {
			v.NewHref = view.External + "/compare"
		}
	}
	a.renderBrowser(w, r, "pulls", pageData{Title: l.msg("repo.tab.pulls"), Project: project, PullsV: v})
}

// pullMenus sind die Auswahlen Autor und Branch; ihre Einträge schreiben ein
// Wort in den Suchtext.
func (a *app) pullMenus(l lang, pa *store.ProjectAccess, project, q string, pq pullQuery, href func(string) string, visible branchVisible) []issueMenu {
	all, err := pa.PullRequests(project, store.PullListFilter{Limit: store.PullMaxLimit})
	if err != nil {
		return nil
	}
	var authors, heads []string
	for _, it := range all.Items {
		if it.Author != "" && !strings.ContainsAny(it.Author, " \t:") && !slices.ContainsFunc(authors, func(s string) bool { return strings.EqualFold(s, it.Author) }) {
			authors = append(authors, it.Author)
		}
		if it.Head != "" && visible(it.Head) && !strings.ContainsAny(it.Head, " \t:") && !slices.Contains(heads, it.Head) {
			heads = append(heads, it.Head)
		}
	}
	menu := func(labelKey, anyKey, key, current string, names []string) (issueMenu, bool) {
		if current != "" && !slices.ContainsFunc(names, func(s string) bool { return strings.EqualFold(s, current) }) {
			names = append(names, current)
		}
		if len(names) == 0 {
			return issueMenu{}, false
		}
		slices.SortFunc(names, func(x, y string) int { return strings.Compare(strings.ToLower(x), strings.ToLower(y)) })
		m := issueMenu{Label: l.msg(labelKey), Active: current != ""}
		m.Items = append(m.Items, issueMenuItem{Label: l.msg(anyKey), Href: href(withQualifier(q, dropKey(key), "")), Current: current == ""})
		for _, n := range names {
			m.Items = append(m.Items, issueMenuItem{Label: n, Href: href(withQualifier(q, dropKey(key), key+":"+n)), Current: strings.EqualFold(current, n)})
		}
		return m, true
	}
	var out []issueMenu
	if m, ok := menu("pulls.filter.author", "pulls.filter.any_author", "author", pq.Author, authors); ok {
		out = append(out, m)
	}
	if m, ok := menu("pulls.filter.branch", "pulls.filter.any_branch", "branch", pq.Branch, heads); ok {
		out = append(out, m)
	}
	return out
}

// pullRow macht aus einer Zeile der Liste die Listenzeile: Symbol des Zustands,
// Art aus dem Titel, Autor, die beiden Branches; rechts, wer wartet, und der
// Fortschritt des Arbeitspakets.
func (a *app) pullRow(l lang, base, q string, it store.PullSummary, pkg pullPackage, now time.Time, visible branchVisible) uiRow {
	icon, tone, _ := pullStateLook(it.State)
	at, titleKey := it.OpenedAt, "pulls.opened_at"
	switch it.State {
	case store.PullMerged:
		at, titleKey = firstNonEmpty(it.MergedAt, it.UpdatedAt), "pulls.merged_at"
	case store.PullClosed:
		at, titleKey = it.UpdatedAt, "pulls.closed_at"
	}
	t := parseTime(at)
	row := uiRow{Icon: icon, Tone: tone, StateLabel: label(l, pullStLabelKeys, it.State), Num: "#" + strconv.FormatInt(it.Number, 10), Title: it.Title,
		Href: base + "/" + strconv.FormatInt(it.Number, 10), Kind: pullKind(l, it.Title), Age: shortAge(l, now, t), Person: it.Author}
	if !t.IsZero() {
		row.AgeTitle = l.msg(titleKey, l.dateTime(t.Local()))
	}
	switch {
	case it.Head == "":
	case visible(it.Head):
		row.Meta = append(row.Meta, branchChipHTML(l, it.Head, base+"?q="+url.QueryEscape(withQualifier(q, dropKey("branch"), "branch:"+it.Head))))
	default:
		row.Meta = append(row.Meta, hiddenBranchChip(l))
	}
	if it.Base != "" && visible(it.Base) {
		row.Meta = append(row.Meta, template.HTML(`<span class="mono">→ `+html.EscapeString(it.Base)+`</span>`))
	}
	row.Waiting = pkg.Waiting
	if pkg.Total > 0 {
		row.Progress = &rowProgress{Done: pkg.Done, Total: pkg.Total, Title: l.msg("pulls.progress", pkg.Done, pkg.Total)}
	}
	return row
}

// ---- Seite eines PRs ---------------------------------------------------------------

const (
	pullTabTalk      = "talk"
	pullTabChecklist = "checklist"
	pullTabKnowledge = "knowledge"
	pullTabCommits   = "commits"
)

type pullTab struct {
	Key, Label, Href, Icon, Count string
	Current                       bool
}

// pullCheck ist eine Zeile des Kastens „Stand der Prüfung“.
type pullCheck struct {
	Tone, Icon, Label, Text, LinkLabel, LinkHref string
}

type pullCriterion struct {
	Done, Waived, NoProof bool
	Text                  string
	Evidence              []evidenceLine
}

type pullTaskLine struct {
	N     int64
	Title string
}

type pullMember struct {
	N                    int64
	Href, Title          string
	Optional, Open, Warn bool
	Progress             rowProgress
	Criteria             []pullCriterion
	Tasks                []pullTaskLine
	// Live is the agent that works on the issue right now (live.go).
	Live liveAgent
}

type pullFeatureLine struct {
	Name, Href, StateKey, Tone, Note string
	Revision                         int
	Done                             bool
}

type pullUncovered struct {
	Idea  int64
	Title string
	Href  string
	No    int
	Text  string
	Open  bool
}

type pullKnowLine struct {
	TypeKey, Class, Title, Href, Person string
}

type pullGhostLine struct{ Path, Href string }

type commitRow struct {
	SHA, Short, Title, Href, At, Who, AvKind, AuthorName string
	Entries                                              int
	PRs                                                  []commitPR
	Branches                                             []commitBranch
	// Co are the co-authors named in the commit message. CI is the state of its
	// checks (ok, bad or wait; empty when none is known), CITitle says it in words.
	// CodeHref is the code as it stood at this commit, on GitHub.
	Co                            []string
	CI, CIIcon, CITitle, CodeHref string
	CodeTip                       string
}

type commitPR struct{ Text, Href, Title string }

// sideRef is a number with its title in a column of facts.
type sideRef struct {
	N           int64
	Title, Href string
}

type commitBranch struct{ Name, Href string }

type pullStat struct {
	Done, Total, Knowledge int
}

type pullSide struct {
	Package, PackageNote string
	Requests             []sideRef
	RequestsMore         int
	Features             []sideRef
}

type pullView struct {
	N                          int64
	Title, Author              string
	Base                       string
	StateLabel, Pill, PillIcon string
	Verb, Into                 string
	HeadChip, BaseChip         template.HTML
	GitHub, GitHubTip          string
	// DoneHref is the quick filter "What was done here?" for this pull request.
	DoneHref      string
	Tabs          []pullTab
	Tab           string
	Items         []tlItem
	Checks        []pullCheck
	Stat          pullStat
	Members       []pullMember
	HasPackage    bool
	Features      []pullFeatureLine
	Uncovered     []pullUncovered
	Know          []pullKnowLine
	KnowMore      int
	Ghosts        []pullGhostLine
	Commits       []commitRow
	CommitsTotal  int
	Side          pullSide
	ChecklistHref string
	KnowledgeHref string
	// LiveIDs are the issues of the checklist, for the live stream.
	LiveIDs string
}

// pullDetail ist /ui/{org}/{repo}/pulls/{n}.
func (a *app) pullDetail(w http.ResponseWriter, r *http.Request, remote, id string) {
	l := a.langFor(r)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	d, err := pa.PullRequestDetail(remote, n)
	if err != nil {
		a.pullError(w, r, err)
		return
	}
	r2, view := a.repoDetailRequest(r, pa, remote, "pulls")
	cl, err := pa.PullCommits(remote, n)
	if err != nil {
		a.pullError(w, r, err)
		return
	}
	names, def := a.codeBranches(r2, pa, remote)
	visible := a.branchRule(r2, pa, remote)
	links := codeLinks{base: view.Href, def: def}
	now := overviewNow().UTC()
	base := view.Href + "/pulls/" + strconv.FormatInt(n, 10)
	pill, _ := pullPillOf(d.State)
	icon, _, _ := pullStateLook(d.State)
	v := &pullView{N: n, Title: d.Title, Author: d.Author, Base: view.Href + "/pulls", StateLabel: label(l, pullStateKeys, d.State), Pill: pill, PillIcon: icon,
		ChecklistHref: base + "?tab=" + pullTabChecklist + "#tabs", KnowledgeHref: base + "?tab=" + pullTabKnowledge + "#tabs"}
	v.HeadChip = hiddenBranchChip(l)
	if visible(d.Head) {
		v.HeadChip = branchChipHTML(l, d.Head, v.Base+"?q="+url.QueryEscape("is:any branch:"+d.Head))
	}
	if d.Base != "" && !visible(d.Base) {
		v.BaseChip = hiddenBranchChip(l)
	} else if d.Base != "" {
		baseHref := ""
		if slices.Contains(names, d.Base) {
			baseHref = links.tree(d.Base, "")
		}
		v.BaseChip = branchChipHTML(l, d.Base, baseHref)
	}
	count := d.Commits
	if count == 0 {
		count = len(cl.Items)
	}
	switch {
	case d.State == store.PullOpen && count > 0:
		v.Verb = l.msg("pulls.meta.wants", count)
	case d.State == store.PullOpen:
		v.Verb = l.msg("pulls.meta.wants_nocount")
	case count > 0:
		v.Verb = l.msg("pulls.meta.proposed", count)
	default:
		v.Verb = l.msg("pulls.meta.proposed_nocount")
	}
	v.Into = l.msg("pulls.meta.into")
	if view.External != "" {
		v.GitHub, v.GitHubTip = view.External+"/pull/"+strconv.FormatInt(n, 10), l.msg("pulls.gh_pull", n)
	}
	v.DoneHref = pathWith("/ui/search", "project", remote, "preset", "pr-ergebnis", "pr", strconv.FormatInt(n, 10))

	v.HasPackage = d.Checklist.HasPackage
	a.pullChecklist(l, pa, view, d, v)
	v.Checks = pullChecks(l, d, v)
	for _, k := range d.Brief.Knowledge {
		if len(v.Know) >= pullKnowledgeMax {
			v.KnowMore = len(d.Brief.Knowledge) - len(v.Know)
			break
		}
		v.Know = append(v.Know, pullKnowLine{TypeKey: knowTypeKey(k.Type), Class: knowTypeClass(k.Type), Title: k.Title, Href: knowledgeHref(remote, k.ID), Person: k.Person})
	}
	for _, p := range d.Brief.Ghosts {
		if len(v.Ghosts) >= pullKnowledgeMax {
			break
		}
		v.Ghosts = append(v.Ghosts, pullGhostLine{Path: p, Href: links.blob(def, p)})
	}
	v.Stat.Knowledge = len(d.Brief.Knowledge)
	seeAll := a.seesAllBranches(pa, remote)
	for _, c := range cl.Items {
		v.Commits = append(v.Commits, a.commitRowOf(l, view, c, names, seeAll, now))
	}
	v.CommitsTotal = len(v.Commits)
	v.Items = a.pullItems(l, view, d, cl.Items, v)
	// What was said in the project room at the pull request or at one of its
	// commits stands between the events by time.
	if said, err := pa.PullMessages(remote, n); err == nil && len(said) > 0 {
		visible := a.branchRule(r2, pa, remote)
		var msgs []tlItem
		for _, e := range said {
			it := commitItem(l, e, commitEntryHref(view, def, visible, e), now)
			it.ID, it.at = "message-"+strconv.FormatInt(e.ID, 10), parseTime(e.At)
			msgs = append(msgs, it)
		}
		v.Items = mergeTimeline(v.Items, msgs)
	}

	tab := r.URL.Query().Get("tab")
	if !slices.Contains([]string{pullTabTalk, pullTabChecklist, pullTabKnowledge, pullTabCommits}, tab) {
		tab = pullTabTalk
	}
	v.Tab = tab
	mk := func(key, label, icon string, cnt int) {
		t := pullTab{Key: key, Label: label, Icon: icon, Href: base + "?tab=" + key + "#tabs", Current: key == tab}
		if key == pullTabTalk {
			t.Href = base + "#tabs"
		}
		if cnt > 0 {
			t.Count = strconv.Itoa(cnt)
		}
		v.Tabs = append(v.Tabs, t)
	}
	// The conversation counts what people said (comments and room messages), not the events between them.
	said := 0
	for _, it := range v.Items {
		if it.Comment != nil || strings.HasPrefix(it.ID, "message-") {
			said++
		}
	}
	mk(pullTabTalk, "pulls.tab.talk", "chat", said)
	mk(pullTabChecklist, "pulls.tab.checklist", "tasks", len(v.Members)+len(v.Features)+len(v.Uncovered))
	mk(pullTabKnowledge, "pulls.tab.knowledge", "book", len(v.Know)+v.KnowMore)
	mk(pullTabCommits, "pulls.tab.commits", "commit", count)
	a.renderBrowser(w, r2, "pull", pageData{Title: "#" + strconv.FormatInt(n, 10) + " " + d.Title, Project: remote, PullV: v})
}

func pullPillOf(state string) (string, string) {
	_, _, pill := pullStateLook(state)
	return pill, pullStateKeys[state]
}

func knowTypeClass(t string) string {
	switch t {
	case "decision":
		return "acc"
	case "pitfall":
		return "warn"
	case "plan":
		return "don"
	}
	return "neu"
}

// commitRowOf macht aus einem Commit der Liste eine Zeile.
func (a *app) commitRowOf(l lang, view *repoView, c store.CommitInfo, names []string, seeAll bool, now time.Time) commitRow {
	row := commitRow{SHA: c.SHA, Short: shortSHA(c.SHA), Title: c.Title, Href: view.Href + "/commits/" + c.SHA, At: c.At, Entries: c.Entries}
	row.Who = firstNonEmpty(c.Author, c.AuthorName)
	if row.Who != "" {
		row.AvKind = actorKind(row.Who)
	}
	row.Co = c.CoAuthors
	row.CI, row.CIIcon, row.CITitle = commitCI(l, c.CI)
	if view.External != "" {
		row.CodeHref, row.CodeTip = view.External+"/tree/"+c.SHA, l.msg("commit.code_tip", row.Short)
	}
	for _, n := range c.PRs {
		p := commitPR{Text: "#" + strconv.FormatInt(n, 10), Href: view.Href + "/pulls/" + strconv.FormatInt(n, 10), Title: l.msg("commits.pr_title", n)}
		row.PRs = append(row.PRs, p)
	}
	for _, b := range c.Branches {
		// Ein Gast sieht nur den Standard-Branch: ein Branchname kann verraten, was er nicht lesen darf.
		if seeAll || slices.Contains(names, b) {
			row.Branches = append(row.Branches, commitBranch{Name: b, Href: view.Href + "/commits?branch=" + url.QueryEscape(b)})
		}
	}
	return row
}

// commitCI says how the checks of a commit stand: a class, an icon and the words.
// Nothing is known when the state is empty, and then nothing is shown.
func commitCI(l lang, state string) (class, icon, title string) {
	switch state {
	case store.CommitCISuccess:
		return "ok", "check", l.msg("commit.ci.success")
	case store.CommitCIFailure:
		return "bad", "x", l.msg("commit.ci.failure")
	case store.CommitCIPending:
		return "wait", "clock", l.msg("commit.ci.pending")
	}
	return "", "", ""
}

// pullChecklist füllt Kennzahlen, Mitglieder, Feature-Punkte und offene
// Anforderungen aus dem Paket des PRs.
func (a *app) pullChecklist(l lang, pa *store.ProjectAccess, view *repoView, d store.PullDetail, v *pullView) {
	if d.Package != nil {
		ids := make([]int64, 0, len(d.Package.Members))
		for _, m := range d.Package.Members {
			ids = append(ids, m.RequestID)
		}
		prog, _ := a.store.CriteriaProgress(ids)
		live := a.liveByRequest(pa, ids, overviewNow().UTC(), l)
		idText := make([]string, len(ids))
		for i, id := range ids {
			idText[i] = strconv.FormatInt(id, 10)
		}
		v.LiveIDs = strings.Join(idText, ",")
		for _, m := range d.Package.Members {
			pm := pullMember{Live: live[m.RequestID], N: m.RequestID, Href: view.Href + "/issues/" + strconv.FormatInt(m.RequestID, 10), Title: m.Title, Optional: m.Role == requestdomain.RoleOptional,
				Open: m.Open > 0, Progress: rowProgress{Done: prog[m.RequestID].Done, Total: prog[m.RequestID].Total}}
			pm.Progress.Title = l.msg("pulls.progress", pm.Progress.Done, pm.Progress.Total)
			for _, c := range m.Criteria {
				pc := pullCriterion{Done: c.State == "met", Waived: c.State == "waived", Text: c.Description}
				for _, e := range c.Evidence {
					line := a.evidenceLineFor(l, pa, view.Project, e)
					if e.Kind == "commit" && commitRefPattern.MatchString(e.Ref) {
						line.Href = view.Href + "/commits/" + e.Ref
					}
					pc.Evidence = append(pc.Evidence, line)
				}
				pc.NoProof = c.State == "met" && len(c.Evidence) == 0
				pm.Warn = pm.Warn || pc.NoProof
				pm.Criteria = append(pm.Criteria, pc)
			}
			pm.Warn = pm.Warn && m.Open == 0
			for _, t := range m.Tasks {
				pm.Tasks = append(pm.Tasks, pullTaskLine{N: t.ID, Title: t.Title})
			}
			v.Members = append(v.Members, pm)
			v.Stat.Done += pm.Progress.Done
			v.Stat.Total += pm.Progress.Total
		}
		for _, u := range d.Package.Uncovered {
			v.Uncovered = append(v.Uncovered, pullUncovered{Idea: u.Idea, Title: u.IdeaTitle, Href: view.Href + "/ideas/" + strconv.FormatInt(u.Idea, 10), No: u.No, Text: u.Text, Open: u.State == "open"})
		}
	}
	for _, f := range d.Features {
		line := pullFeatureLine{Name: f.Name, Href: view.Href + "/features/" + strconv.FormatInt(f.Feature, 10), Revision: f.Revision, Note: f.Note}
		switch f.State {
		case requestdomain.FeatureCheckUpdated:
			line.StateKey, line.Tone, line.Done = "pulls.feature.updated", "ok", true
		case requestdomain.FeatureCheckUnchanged:
			line.StateKey, line.Tone, line.Done = "pulls.feature.unchanged", "neu", true
		default:
			line.StateKey, line.Tone = "pulls.feature.open", "warn"
		}
		v.Features = append(v.Features, line)
		v.Side.Features = append(v.Side.Features, sideRef{N: f.Feature, Title: f.Name, Href: line.Href})
	}
	if d.Package != nil {
		v.Side.Package = packageName(l, d.Package.Package)
		v.Side.PackageNote = l.msg("pulls.side.package_note", len(v.Members), v.Stat.Done, v.Stat.Total)
	}
	for i, m := range v.Members {
		if i >= pullSideRequests {
			v.Side.RequestsMore = len(v.Members) - pullSideRequests
			break
		}
		v.Side.Requests = append(v.Side.Requests, sideRef{N: m.N, Title: m.Title, Href: m.Href})
	}
}

var commitRefPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// pullChecks sind die Zeilen des Kastens „Stand der Prüfung“: nur, was
// ghosttree selbst über den PR weiß.
func pullChecks(l lang, d store.PullDetail, v *pullView) []pullCheck {
	var out []pullCheck
	add := func(ok bool, label, text string) {
		c := pullCheck{Tone: "warn", Icon: "tri", Label: label, Text: text, LinkLabel: l.msg("pulls.chk.link"), LinkHref: v.ChecklistHref}
		if ok {
			c.Tone, c.Icon = "ok", "done"
		}
		out = append(out, c)
	}
	c := d.Checklist
	if c.HasPackage {
		add(c.RequiredOpen == 0, packageName(l, d.Package.Package), l.msg("pulls.chk.criteria", v.Stat.Done, v.Stat.Total))
		if c.RequiredTotal > 0 && c.RequiredOpen > 0 {
			add(false, l.msg("pulls.chk.required"), l.msg("pulls.chk.required_open", c.RequiredOpen, c.RequiredTotal))
		}
	}
	if len(d.Features) > 0 {
		if c.FeaturesOpen > 0 {
			add(false, l.msg("pulls.chk.features"), l.msg("pulls.chk.features_open", c.FeaturesOpen))
		} else {
			add(true, l.msg("pulls.chk.features"), l.msg("pulls.chk.features_done"))
		}
	}
	if c.Uncovered > 0 {
		add(false, l.msg("pulls.chk.uncovered"), l.msg("pulls.chk.uncovered_n", c.Uncovered))
	}
	if c.HumanTasks > 0 {
		add(false, l.msg("pulls.chk.tasks"), l.msg("pulls.chk.tasks_n", c.HumanTasks))
	}
	return out
}

// pullItems ist die Zeitleiste der Unterhaltung: das Öffnen, die Commits, nach
// Autor und Tag zusammengefasst, und am Ende Merge oder Schließen.
func (a *app) pullItems(l lang, view *repoView, d store.PullDetail, commits []store.CommitInfo, v *pullView) []tlItem {
	now := overviewNow().UTC()
	var items []tlItem
	open := tlItem{ID: "opened", Act: "pull.opened", Who: d.Author, Icon: "pr", Head: template.HTML(html.EscapeString(l.msg("pulls.ev.opened"))), Age: shortAgo(l, now, parseTime(d.OpenedAt)), at: parseTime(d.OpenedAt)}
	if d.Author == "" {
		open.Tone = "ok"
	}
	items = append(items, open)
	type group struct {
		who  string
		day  string
		cs   []store.CommitInfo
		last time.Time
	}
	var groups []*group
	for _, c := range commits {
		who := firstNonEmpty(c.Author, c.AuthorName)
		t := parseTime(c.At)
		day := t.Local().Format("2006-01-02")
		if n := len(groups); n > 0 && groups[n-1].who == who && groups[n-1].day == day {
			groups[n-1].cs = append(groups[n-1].cs, c)
			groups[n-1].last = t
			continue
		}
		groups = append(groups, &group{who: who, day: day, cs: []store.CommitInfo{c}, last: t})
	}
	for i, g := range groups {
		key := "pulls.ev.commits"
		if len(g.cs) == 1 {
			key = "pulls.ev.commit_one"
		}
		head := html.EscapeString(l.msg(key, len(g.cs)))
		var right strings.Builder
		for j, c := range g.cs {
			if j == 3 {
				right.WriteString(`<span class="muted">` + html.EscapeString(l.msg("pulls.ev.more", len(g.cs)-3)) + `</span>`)
				break
			}
			short := shortSHA(c.SHA)
			right.WriteString(string(shaPill(l, short, view.Href+"/commits/"+c.SHA)))
		}
		it := tlItem{ID: "commits-" + strconv.Itoa(i), Act: "pull.commits", Who: g.who, Icon: "commit", Tone: "ok", Head: template.HTML(head), Right: template.HTML(right.String()), at: g.last}
		if !g.last.IsZero() {
			it.Age = shortAgo(l, now, g.last)
		}
		items = append(items, it)
	}
	switch d.State {
	case store.PullMerged:
		head := html.EscapeString(l.msg("pulls.ev.merged"))
		if d.Base != "" {
			head += " " + string(v.BaseChip)
		}
		items = append(items, tlItem{ID: "merged", Act: "pull.merged", Icon: "merge", Tone: "don", Head: template.HTML(head), Age: shortAgo(l, now, parseTime(firstNonEmpty(d.MergedAt, d.UpdatedAt))), at: parseTime(firstNonEmpty(d.MergedAt, d.UpdatedAt))})
	case store.PullClosed:
		items = append(items, tlItem{ID: "closed", Act: "pull.closed", Icon: "drop", Head: template.HTML(html.EscapeString(l.msg("pulls.ev.closed"))), Age: shortAgo(l, now, parseTime(d.UpdatedAt)), at: parseTime(d.UpdatedAt)})
	}
	// One rule with or without messages: by time. The opening no longer jumps.
	sort.SliceStable(items, func(i, j int) bool { return before(items[i].at, items[j].at) })
	return items
}
