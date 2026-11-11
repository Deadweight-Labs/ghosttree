package web

import (
	"bytes"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Suggestions under the search fields (REQ-599 AC-10, REQ-628 AC-1). The field
// of the header (and of the search page) asks /ui/search/suggest with what is
// typed so far, and Ctrl+K or "/" jumps into it. The answer is the list that
// drops down: first the object a number names (#217, REQ-12, pr:5), found by
// the same reading and with the same rights as the search page (exactHits),
// then the qualifier that is being typed, completed, and last the line that
// searches for the whole text. The keys and values are the search language of
// internal/query, so a suggestion is always something the search understands.

const (
	suggestHitsMax   = 4
	suggestValuesMax = 8
)

// suggestKeys are the qualifiers that are offered, in the order they are listed.
// A guest is not offered the ones that would name what a guest may not read
// (guestQualifiers).
var suggestKeys = []string{
	query.KeyIs, query.KeyKind, query.KeyType, query.KeyRepo, query.KeyBranch, query.KeyPackage,
	query.KeyAuthor, query.KeyFor, query.KeyLabel, query.KeyArea, query.KeyCreated, query.KeyUpdated, query.KeyPR,
}

// suggestKeyLabels are the catalog keys of the words beside the keys, spelled
// out so that the catalog test finds them.
var suggestKeyLabels = map[string]string{
	query.KeyIs: "search.key.is", query.KeyKind: "search.key.kind", query.KeyType: "search.key.type", query.KeyRepo: "search.key.repo",
	query.KeyBranch: "search.key.branch", query.KeyPackage: "search.key.package", query.KeyAuthor: "search.key.author", query.KeyFor: "search.key.for",
	query.KeyLabel: "search.key.label", query.KeyArea: "search.key.area", query.KeyCreated: "search.key.created", query.KeyUpdated: "search.key.updated",
	query.KeyPR: "search.key.pr", "priority": "search.key.priority",
}

// suggestList is what the filter field of a list offers: the qualifiers that list
// reads (parseIssueQuery, parsePullQuery) with the values of its own, and no jump to
// an object or search for the whole text, because the field filters the list and
// goes nowhere else. A guest is offered less in every list (guestQualifiers).
type suggestList struct {
	keys []string
	// states are the values of is:.
	states []string
	// packageOpen says which packages the list can show: an issue list the open ones,
	// a pull list the ones with a pull request.
	packageOpen func(p requestdomain.Package) bool
	// pullHeads: branch: offers the heads of the pull requests, not every branch.
	pullHeads bool
}

// suggestLists are the lists that carry the field, by the name the field gives
// in data-suggest-list.
var suggestLists = map[string]suggestList{
	"issues": {
		keys:        []string{query.KeyIs, query.KeyType, "priority", query.KeyLabel, query.KeyAuthor, query.KeyBranch, query.KeyPackage},
		states:      []string{"open", "closed", "done", "all", "issue", "idea", "pain"},
		packageOpen: func(p requestdomain.Package) bool { return p.State == requestdomain.PackageOpen },
	},
	"pulls": {
		keys:        []string{query.KeyIs, query.KeyAuthor, query.KeyBranch, query.KeyPackage},
		states:      []string{"open", "merged", "closed", "all"},
		packageOpen: func(p requestdomain.Package) bool { return p.PR > 0 },
		pullHeads:   true,
	},
}

// suggestStates are the values of is: that tell open from finished work.
var suggestStates = []string{"open", "closed", "done"}

// suggestTypes are the types of an issue.
var suggestTypes = []string{"feature", "change", "bug", "investigation"}

// suggestKindValues are the values of kind: the kinds the web search has groups
// for; a guest gets no tasks, pains or sessions.
var suggestKindValues = []string{query.KindRequest, query.KindIdea, query.KindKnowledge, query.KindDocument, query.KindPain, query.KindTask, query.KindSession}

// suggestSpans are the ages created: and updated: are offered with.
var suggestSpans = []string{"1d", "7d", "30d"}

// suggestListOf is the list the request names with list=, nil for the field of the
// header. ok is false for a name that is none.
func suggestListOf(r *http.Request) (list *suggestList, ok bool) {
	name := r.URL.Query().Get("list")
	if name == "" {
		return nil, true
	}
	l, ok := suggestLists[name]
	if !ok {
		return nil, false
	}
	return &l, true
}

// suggestRow is one line of the list. Kind "hit" leads to an object, "qual"
// replaces the word being typed by Insert, "all" searches for the whole text.
type suggestRow struct {
	Kind, Href, Insert string
	Title, Lead, Mark  string
	RefKind            string
	RefN               int64
	PRState            string
}

type suggestView struct{ Rows []suggestRow }

// searchSuggest is GET /ui/search/suggest?q=<line>&project=<remote>[&list=issues|pulls].
// It answers the rows as list items, empty when there is nothing to suggest. project
// is the repository of the page the field sits on; the viewer must be able to read
// it, otherwise it counts as not given. With list the field is the filter of that list
// of the repository: only the qualifiers the list reads, with its own values, and
// nothing without a repository the viewer can read.
func (a *app) searchSuggest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	l := a.langFor(r)
	pa := a.access(r)
	guest := a.viewerOf(r, pa).kind == viewerGuest
	line := r.URL.Query().Get("q")
	if utf8.RuneCountInString(line) > maxQueryRunes {
		line = string([]rune(line)[:maxQueryRunes])
	}
	project := r.URL.Query().Get("project")
	if project != "" && pa.Role(project).Role == "" {
		project = ""
	}
	list, known := suggestListOf(r)
	if !known || (list != nil && project == "") {
		return
	}
	// The same rule as parseSearchLine: with a repository on the page a guest is
	// who does not see all its branches there, whatever the roles elsewhere are.
	if project != "" {
		guest = !pa.SeesAllBranches(project)
	}
	var rows []suggestRow
	if trimmed := strings.TrimSpace(line); trimmed != "" {
		if term, ok := links.ParseNumber(trimmed); ok && list == nil {
			kinds := []string{"requests", "ideas", "knowledge", "docs"}
			if !guest {
				kinds = append(kinds, "tasks", "pains")
			}
			for _, h := range a.exactHits(l, pa, term, "", kinds, overviewNow().UTC()) {
				if len(rows) == suggestHitsMax {
					break
				}
				rows = append(rows, suggestRow{Kind: "hit", Href: h.Href, Title: h.Title, Lead: h.Lead, Mark: h.Mark, RefKind: h.RefKind, RefN: h.RefN, PRState: h.PRState})
			}
		}
		rows = append(rows, a.suggestQualifiers(r, pa, l, guest, project, line, list)...)
		if list == nil && utf8.RuneCountInString(trimmed) >= minSearchRunes {
			rows = append(rows, suggestRow{Kind: "all", Href: "/ui/search?q=" + url.QueryEscape(trimmed), Mark: "search", Title: l.msg("search.suggest_for", trimmed)})
		}
	}
	if len(rows) == 0 {
		return
	}
	var b bytes.Buffer
	if err := l.page().ExecuteTemplate(&b, "searchsuggest", suggestView{Rows: rows}); err != nil {
		a.log().Error("search_suggest", "error", err.Error())
		return
	}
	_, _ = w.Write(b.Bytes())
}

// suggestQualifiers completes the word at the end of the line: a key while it is
// still being typed, a value once the colon is there. A word with a quote in it
// is text, and so is everything after an opening quote that is not closed yet:
// the words inside a phrase are not qualifiers.
func (a *app) suggestQualifiers(r *http.Request, pa *store.ProjectAccess, l lang, guest bool, project, line string, list *suggestList) []suggestRow {
	if strings.Count(line, `"`)%2 == 1 {
		return nil
	}
	token := line[strings.LastIndexAny(line, " \t")+1:]
	if strings.Contains(token, `"`) {
		return nil
	}
	neg := ""
	if strings.HasPrefix(token, "-") {
		neg, token = "-", token[1:]
	}
	key, val, colon := strings.Cut(strings.ToLower(token), ":")
	if !colon {
		var rows []suggestRow
		keys := suggestKeys
		if list != nil {
			keys = list.keys
		}
		for _, k := range keys {
			if (guest && slices.Contains(guestQualifiers, k)) || !strings.HasPrefix(k, key) {
				continue
			}
			rows = append(rows, suggestRow{Kind: "qual", Insert: neg + k + ":", Title: k + ":", Lead: l.msg(suggestKeyLabels[k])})
			if len(rows) == suggestValuesMax {
				break
			}
		}
		return rows
	}
	if guest && slices.Contains(guestQualifiers, key) {
		return nil
	}
	if list != nil && !slices.Contains(list.keys, key) {
		return nil
	}
	var rows []suggestRow
	for _, c := range a.suggestValues(r, pa, l, guest, project, key, list) {
		if !strings.HasPrefix(strings.ToLower(c.value), val) || strings.EqualFold(c.value, val) {
			continue
		}
		word := key + ":" + quoteValue(c.value)
		rows = append(rows, suggestRow{Kind: "qual", Insert: neg + word, Title: word, Lead: c.lead})
		if len(rows) == suggestValuesMax {
			break
		}
	}
	return rows
}

type suggestValue struct{ value, lead string }

// quoteValue writes a value so that the search reads it as one: a comma would
// split it into alternatives, so such a value goes in quotes.
func quoteValue(v string) string {
	if strings.Contains(v, ",") {
		return `"` + v + `"`
	}
	return v
}

// suggestValues are the values the key can be completed with. The ones that name
// something of a repository (branch, package, label) need the repository of the
// page and are read through the rights of the viewer.
func (a *app) suggestValues(r *http.Request, pa *store.ProjectAccess, l lang, guest bool, project, key string, list *suggestList) []suggestValue {
	plain := func(list ...string) []suggestValue {
		out := make([]suggestValue, 0, len(list))
		for _, v := range list {
			out = append(out, suggestValue{value: v})
		}
		return out
	}
	switch key {
	case query.KeyIs:
		if list != nil {
			return plain(list.states...)
		}
		return plain(suggestStates...)
	case query.KeyKind:
		kinds := slices.Clone(suggestKindValues)
		if guest {
			kinds = slices.DeleteFunc(kinds, func(k string) bool { return k == query.KindPain || k == query.KindTask || k == query.KindSession })
		}
		return plain(kinds...)
	case query.KeyType:
		return plain(suggestTypes...)
	case query.KeyAuthor:
		if list != nil {
			// The lists compare the name, they do not know "@me".
			return nil
		}
		return plain("@me")
	case "priority":
		if list == nil || project == "" {
			return nil
		}
		known, err := a.store.RequestPriorities(pa.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}}))
		if err != nil {
			return nil
		}
		slices.Sort(known)
		var out []suggestValue
		for _, p := range slices.Compact(known) {
			if p != "" && !strings.ContainsAny(p, " \t:\"") {
				out = append(out, suggestValue{value: p})
			}
		}
		return out
	case query.KeyFor:
		return plain("@me")
	case query.KeyCreated, query.KeyUpdated:
		return plain(suggestSpans...)
	case query.KeyRepo:
		// owner/name is what repo: reads (repoRemoteOf adds github.com); a project of
		// another host keeps its host. The last segment alone would name a project
		// that does not exist.
		var out []suggestValue
		for _, p := range slices.Compact(visibleProjects(pa, "")) {
			out = append(out, suggestValue{value: strings.TrimPrefix(p, "github.com/")})
		}
		return out
	case query.KeyBranch:
		if project == "" {
			return nil
		}
		if list != nil && list.pullHeads {
			return a.suggestPullHeads(r, pa, project)
		}
		names, def := a.codeBranches(r, pa, project)
		out := make([]suggestValue, 0, len(names))
		for _, n := range names {
			if strings.ContainsAny(n, " \t\"") {
				continue
			}
			v := suggestValue{value: n}
			if n == def {
				v.lead = l.msg("code.branch_default")
			}
			out = append(out, v)
		}
		return out
	case query.KeyPackage:
		if project == "" {
			return nil
		}
		var out []suggestValue
		for _, p := range packageChoices(pa, project) {
			if list != nil && !list.packageOpen(p) {
				continue
			}
			out = append(out, suggestValue{value: strconv.FormatInt(p.ID, 10), lead: packageName(l, p)})
		}
		return out
	case query.KeyLabel:
		if project == "" {
			return nil
		}
		all, err := pa.ListLabels(project)
		if err != nil {
			return nil
		}
		var out []suggestValue
		for _, lb := range all {
			if !strings.ContainsAny(lb.Name, " \t\":") {
				out = append(out, suggestValue{value: lb.Name})
			}
		}
		return out
	}
	return nil
}

// suggestPullHeads are the head branches of the pull requests of the repository, the
// ones the branch menu of the pull list offers, and only those the viewer may see.
func (a *app) suggestPullHeads(r *http.Request, pa *store.ProjectAccess, project string) []suggestValue {
	all, err := pa.PullRequests(project, store.PullListFilter{Limit: store.PullMaxLimit})
	if err != nil {
		return nil
	}
	visible := a.branchRule(r, pa, project)
	var heads []string
	for _, it := range all.Items {
		if it.Head != "" && visible(it.Head) && !strings.ContainsAny(it.Head, " \t\"") && !slices.Contains(heads, it.Head) {
			heads = append(heads, it.Head)
		}
	}
	slices.Sort(heads)
	out := make([]suggestValue, 0, len(heads))
	for _, h := range heads {
		out = append(out, suggestValue{value: h})
	}
	return out
}
