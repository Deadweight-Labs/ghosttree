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
	query.KeyPR: "search.key.pr",
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

// searchSuggest is GET /ui/search/suggest?q=<line>&project=<remote>. It answers
// the rows as list items, empty when there is nothing to suggest. project is the
// repository of the page the field sits on; the viewer must be able to read it,
// otherwise it counts as not given.
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
	var rows []suggestRow
	if trimmed := strings.TrimSpace(line); trimmed != "" {
		if term, ok := links.ParseNumber(trimmed); ok {
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
		rows = append(rows, a.suggestQualifiers(r, l, guest, project, line)...)
		if utf8.RuneCountInString(trimmed) >= minSearchRunes {
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
// is text and gets nothing.
func (a *app) suggestQualifiers(r *http.Request, l lang, guest bool, project, line string) []suggestRow {
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
		for _, k := range suggestKeys {
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
	var rows []suggestRow
	for _, c := range a.suggestValues(r, l, guest, project, key) {
		if !strings.HasPrefix(strings.ToLower(c.value), val) || strings.EqualFold(c.value, val) {
			continue
		}
		rows = append(rows, suggestRow{Kind: "qual", Insert: neg + key + ":" + c.value, Title: key + ":" + c.value, Lead: c.lead})
		if len(rows) == suggestValuesMax {
			break
		}
	}
	return rows
}

type suggestValue struct{ value, lead string }

// suggestValues are the values the key can be completed with. The ones that name
// something of a repository (branch, package, label) need the repository of the
// page and are read through the rights of the viewer.
func (a *app) suggestValues(r *http.Request, l lang, guest bool, project, key string) []suggestValue {
	plain := func(list ...string) []suggestValue {
		out := make([]suggestValue, 0, len(list))
		for _, v := range list {
			out = append(out, suggestValue{value: v})
		}
		return out
	}
	pa := a.access(r)
	switch key {
	case query.KeyIs:
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
		return plain("@me")
	case query.KeyFor:
		return plain("@me")
	case query.KeyCreated, query.KeyUpdated:
		return plain(suggestSpans...)
	case query.KeyRepo:
		var out []suggestValue
		for _, p := range visibleProjects(pa, "") {
			out = append(out, suggestValue{value: lastSegment(p)})
		}
		return out
	case query.KeyBranch:
		if project == "" {
			return nil
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
			out = append(out, suggestValue{value: strconv.FormatInt(p.ID, 10), lead: packageName(l, p.Package)})
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
