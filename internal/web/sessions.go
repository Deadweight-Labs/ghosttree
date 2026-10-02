package web

import (
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// Sessions (REQ-435 P10): Liste mit Suche und Filtern, Trefferliste, lesbares
// Transkript. Jede Zahl, jeder Treffer und jeder Ausschnitt stammt aus der für
// den Betrachter lesbaren Menge (store.BrowseSessions / SearchTranscripts);
// diese Datei bildet keine zweite Zählung.

const (
	pageSize        = 50
	resultLines     = 12
	liveWindow      = 3 * time.Minute
	promptsShown    = 30
	maxQueryRunes   = 200
	titleArgRunes   = 110
	windowChunks    = 200
	maxRenderedRows = 5000
)

// ------------------------------------------------------------- Ansichtsmodelle

type chip struct {
	Href, Text string
}

type filterOption struct {
	Value, Label string
	Selected     bool
}

type filterSelect struct {
	Name, Label string // Label ist der Text der "alle"-Option
	Options     []filterOption
}

type kindTab struct {
	Label, Href string
	Current     bool
}

type listRow struct {
	Href, Title, Meta, Messages, Duration, Time, Stamp string
	Private, Live                                      bool
	Linked                                             []chip
}

type dayGroup struct {
	Label string
	Rows  []listRow
}

type partView struct {
	Text string
	Hit  bool
}

type hitView struct {
	Href, Kind, Label string
	Seq               int
	Parts             []partView
}

type groupView struct {
	PID, Href, Title, Meta, Count, Time string
	Hits                                []hitView
	MoreHref, MoreText                  string
}

type sessionsView struct {
	Q, Kind        string
	Searching      bool
	Guest          bool
	Selects        []filterSelect
	Mine, ShowMine bool
	MineHref       string
	EveryoneHref   string
	Tabs           []kindTab
	Days           []dayGroup
	Groups         []groupView
	Found          string
	SortLabel      string
	SortHref       string
	Next           string
	Empty          bool // keine Sitzung, nichts gefiltert
	NoMatch        bool // Filter ohne Zeilen
	NoResults      bool
	ClearHref      string
	ClearFilters   string
	Indexing       string
	FiltersOn      bool
}

type levelOption struct {
	Value, Label string
	Current      bool
}

type promptView struct {
	N                 int
	Text, Time, Tools string
	Hits              string
	Href              string
	Current           bool
}

type diffLineView struct {
	Class string
	Sign  string
	HTML  template.HTML
}

type diffView struct {
	Path  string
	Lines []diffLineView
}

type codeLine struct {
	N    int
	HTML template.HTML
}

type codeView struct {
	Label     string
	Total     string
	Head      []codeLine
	Rest      []codeLine
	RestLabel string
}

type blockView struct {
	Kind         string // user | assistant | thinking | tool | result | note
	ID           string
	Seq          int
	Time, Who    string
	Body         template.HTML
	Summary      string
	Open, Target bool
	Failed       bool
	Tool         string
	Arg          template.HTML
	Result       string
	Extra        []string // weitere Anker (Sequenz des Ergebnisses)
	Input        *codeView
	Output       *codeView
	Diffs        []diffView
	Stamp        string
	Notes        []chip
	NoteLabel    string
}

type levelForm struct {
	Action string
	Levels []levelOption
}

type sessionView struct {
	Missing             bool
	PID, Title          string
	Live                bool
	Meta                string
	BackResults         chip
	Created             []chip
	Share               *levelForm
	FindAction          string
	Q                   string
	Hidden              []chip // verborgene Felder (Name in Text, Wert in Href)
	HasFind             bool
	FindPos, FindCount  string
	PrevHref, NextHref  string
	ToolsOn, ThinkingOn bool
	ToolsHref           string
	ThinkingHref        string
	ExpandHref          string
	Expanded            bool
	Prompts             []promptView
	MorePrompts         chip
	Blocks              []blockView
	Earlier, Later      chip
	WindowError         bool
	RetryHref           string
	Empty               bool
}

// ------------------------------------------------------------------- Helfer

var kindKeys = []string{"", "messages", "output", "commands", "thinking"}

func kindLabelKey(k string) string {
	switch k {
	case "messages":
		return "sessions.kind.messages"
	case "output":
		return "sessions.kind.output"
	case "commands":
		return "sessions.kind.commands"
	case "thinking":
		return "sessions.kind.thinking"
	}
	return "sessions.kind.everything"
}

func agentName(harness string) string {
	switch harness {
	case "codex":
		return "Codex"
	case "claude-code":
		return "Claude Code"
	}
	return harness
}

func agentShort(harness string) string {
	switch harness {
	case "codex":
		return "codex"
	case "claude-code":
		return "claude"
	}
	return harness
}

func projectName(remote string) string {
	if remote == "" {
		return ""
	}
	return path.Base(remote)
}

// sessionMeta ist die Zeile unter dem Titel: Agent@Maschine, Projekt, Branch.
// Ohne Maschine (Gast) steht der Name der Plattform.
func sessionMeta(s store.Session) string {
	var parts []string
	if s.Scope.Machine != "" {
		parts = append(parts, agentShort(s.Harness)+"@"+s.Scope.Machine)
	} else {
		parts = append(parts, agentName(s.Harness))
	}
	if p := projectName(s.Scope.Project); p != "" {
		parts = append(parts, p)
	}
	if s.Scope.Branch != "" {
		parts = append(parts, s.Scope.Branch)
	}
	return strings.Join(parts, " · ")
}

func parseStamp(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func formatDuration(from, to string) string {
	a, b := parseStamp(from), parseStamp(to)
	if a.IsZero() || b.IsZero() || b.Before(a) {
		return ""
	}
	d := b.Sub(a)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

func dayLabel(t, now time.Time) string {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case day.Equal(today):
		return msg("sessions.today")
	case day.Equal(today.AddDate(0, 0, -1)):
		return msg("sessions.yesterday")
	case t.Year() == now.Year():
		return t.Format("Mon 2 Jan")
	}
	return t.Format("2 Jan 2006")
}

func clock(stamp string) string {
	if t := parseStamp(stamp); !t.IsZero() {
		return t.Format("15:04")
	}
	return ""
}

func sessionURL(pid string, q url.Values, fragment string) string {
	u := "/ui/sessions/" + pid
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	if fragment != "" {
		u += "#" + fragment
	}
	return u
}

// terms zerlegt eine Suche in die Wörter, die im Text markiert werden.
func highlightTerms(q string) []string {
	var out []string
	for f := range strings.FieldsSeq(strings.ToLower(q)) {
		f = strings.Trim(strings.ReplaceAll(f, `"`, ""), ".,;:!?()[]{}")
		if utf8.RuneCountInString(f) >= 2 {
			out = append(out, f)
		}
	}
	return out
}

type highlighter struct {
	re *regexp.Regexp
}

func newHighlighter(q string) highlighter {
	terms := highlightTerms(q)
	if len(terms) == 0 {
		return highlighter{}
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = regexp.QuoteMeta(t)
	}
	return highlighter{re: regexp.MustCompile(`(?i)` + strings.Join(quoted, "|"))}
}

// html escaped text and wraps matches in <mark>.
func (h highlighter) html(s string, current bool) template.HTML {
	if h.re == nil {
		return template.HTML(html.EscapeString(s))
	}
	open := "<mark>"
	if current {
		open = `<mark class="cur">`
	}
	var b strings.Builder
	last := 0
	for _, m := range h.re.FindAllStringIndex(s, -1) {
		if m[0] == m[1] {
			continue
		}
		b.WriteString(html.EscapeString(s[last:m[0]]))
		b.WriteString(open + html.EscapeString(s[m[0]:m[1]]) + "</mark>")
		last = m[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	return template.HTML(b.String())
}

func ellipsize(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// ------------------------------------------------------------------- Liste

func sessionFilterFromQuery(r *http.Request, guest bool) (store.SessionFilter, url.Values) {
	q := r.URL.Query()
	f := store.SessionFilter{
		Project: scope.NormalizeRemote(q.Get("project")),
		Harness: q.Get("agent"),
	}
	if !guest {
		f.Machine = strings.ToLower(strings.TrimSpace(q.Get("machine")))
		f.Mine = q.Get("owner") == "mine"
	}
	now := time.Now().UTC()
	switch q.Get("period") {
	case "today":
		f.Since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case "7d":
		f.Since = now.AddDate(0, 0, -7)
	case "30d":
		f.Since = now.AddDate(0, 0, -30)
	}
	keep := url.Values{}
	if f.Project != "" {
		keep.Set("project", f.Project)
	}
	if f.Harness != "" {
		keep.Set("agent", f.Harness)
	}
	if f.Machine != "" {
		keep.Set("machine", f.Machine)
	}
	if f.Mine {
		keep.Set("owner", "mine")
	}
	if p := q.Get("period"); !f.Since.IsZero() {
		keep.Set("period", p)
	}
	return f, keep
}

func facetSelect(name, label string, opts []store.FacetOption, chosen string, display func(string) string) filterSelect {
	sel := filterSelect{Name: name, Label: label}
	found := false
	for _, o := range opts {
		found = found || o.Value == chosen
		sel.Options = append(sel.Options, filterOption{Value: o.Value, Label: fmt.Sprintf("%s (%d)", display(o.Value), o.Count), Selected: o.Value == chosen})
	}
	if chosen != "" && !found {
		sel.Options = append(sel.Options, filterOption{Value: chosen, Label: fmt.Sprintf("%s (0)", display(chosen)), Selected: true})
	}
	return sel
}

func periodSelect(chosen string) filterSelect {
	sel := filterSelect{Name: "period", Label: msg("sessions.any_time")}
	for _, p := range []struct{ v, k string }{{"today", "sessions.period.today"}, {"7d", "sessions.period.7d"}, {"30d", "sessions.period.30d"}} {
		sel.Options = append(sel.Options, filterOption{Value: p.v, Label: msg(p.k), Selected: p.v == chosen})
	}
	return sel
}

func (a *app) sessionsPage(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	who := a.viewerOf(r, pa)
	guest := who.kind == viewerGuest
	filter, keep := sessionFilterFromQuery(r, guest)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) > maxQueryRunes {
		query = string([]rune(query)[:maxQueryRunes])
	}
	kind := r.URL.Query().Get("kind")
	if !slices.Contains(kindKeys, kind) {
		kind = ""
	}
	sortBy := "best"
	if r.URL.Query().Get("sort") == "newest" {
		sortBy = "newest"
	}
	cursor := r.URL.Query().Get("cursor")

	v := &sessionsView{Q: query, Kind: kind, Guest: guest, Searching: query != "", ShowMine: !guest, Mine: filter.Mine}
	v.FiltersOn = len(keep) > 0
	if !guest {
		if p := a.store.IndexProgress(); !p.Done {
			v.Indexing = msg("sessions.indexing", fmt.Sprintf("%d %%", p.Percent))
		}
	}
	now := time.Now().UTC()
	var facets store.Facets

	linkURL := func(extra url.Values) string {
		q := url.Values{}
		for k, vs := range keep {
			q[k] = vs
		}
		for k, vs := range extra {
			q[k] = vs
		}
		if len(q) == 0 {
			return "/ui/sessions"
		}
		return "/ui/sessions?" + q.Encode()
	}
	withQuery := func(extra url.Values) url.Values {
		q := url.Values{}
		if query != "" {
			q.Set("q", query)
		}
		if kind != "" {
			q.Set("kind", kind)
		}
		for k, vs := range extra {
			q[k] = vs
		}
		return q
	}

	if v.Searching {
		page, err := a.store.SearchTranscripts(pa, store.SearchQuery{Q: query, Kind: kind, Filter: filter, Sort: sortBy, Cursor: cursor, Limit: pageSize})
		if err != nil {
			http.Error(w, "search failed", http.StatusInternalServerError)
			return
		}
		facets = page.Facets
		v.Found = foundText(page.Matches, page.Sessions)
		for _, g := range page.Groups {
			v.Groups = append(v.Groups, a.groupView(g, query, kind, now))
		}
		if page.Next != "" {
			v.Next = linkURL(withQuery(url.Values{"cursor": {page.Next}, "sort": {sortBy}}))
		}
		other, cur := "newest", msg("sessions.sort_best")
		if sortBy == "newest" {
			other, cur = "best", msg("sessions.sort_newest")
		}
		v.SortLabel = msg("sessions.sort", cur)
		v.SortHref = linkURL(withQuery(url.Values{"sort": {other}}))
		v.NoResults = len(page.Groups) == 0
		for _, k := range kindKeys {
			ex := url.Values{"sort": {sortBy}}
			if k != "" {
				ex.Set("kind", k)
			}
			q := url.Values{"q": {query}}
			for key, vs := range ex {
				q[key] = vs
			}
			v.Tabs = append(v.Tabs, kindTab{Label: msg(kindLabelKey(k)), Href: linkURL(q), Current: k == kind})
		}
		v.ClearHref = linkURL(nil)
	} else {
		page, err := a.store.BrowseSessions(pa, filter, cursor, pageSize)
		if err != nil {
			http.Error(w, "listing failed", http.StatusInternalServerError)
			return
		}
		facets = page.Facets
		var readableIDs []int64
		for _, row := range page.Rows {
			if row.Readable {
				readableIDs = append(readableIDs, row.Session.ID)
			}
		}
		links, _ := a.store.SessionLinks(pa, readableIDs)
		var lastDay string
		for _, row := range page.Rows {
			lr := a.listRow(row, links[row.Session.ID], now)
			label := dayLabel(parseStamp(row.Session.LastSeenAt), now)
			if label != lastDay || len(v.Days) == 0 {
				v.Days = append(v.Days, dayGroup{Label: label})
				lastDay = label
			}
			v.Days[len(v.Days)-1].Rows = append(v.Days[len(v.Days)-1].Rows, lr)
		}
		if page.Next != "" {
			v.Next = linkURL(url.Values{"cursor": {page.Next}})
		}
		v.Empty = len(page.Rows) == 0 && !v.FiltersOn && cursor == ""
		v.NoMatch = len(page.Rows) == 0 && v.FiltersOn
		v.ClearFilters = "/ui/sessions"
	}

	chosenProject := filter.Project
	v.Selects = append(v.Selects, facetSelect("project", msg("sessions.all_projects"), facets.Projects, chosenProject, projectName))
	if !guest && len(facets.Machines) > 0 || filter.Machine != "" {
		v.Selects = append(v.Selects, facetSelect("machine", msg("sessions.all_machines"), facets.Machines, filter.Machine, func(s string) string { return s }))
	}
	v.Selects = append(v.Selects, facetSelect("agent", msg("sessions.any_agent"), facets.Harnesses, filter.Harness, agentName))
	v.Selects = append(v.Selects, periodSelect(r.URL.Query().Get("period")))
	if v.ShowMine {
		ev := url.Values{}
		mv := url.Values{"owner": {"mine"}}
		for k, vs := range keep {
			if k != "owner" {
				ev[k], mv[k] = vs, vs
			}
		}
		if query != "" {
			ev.Set("q", query)
			mv.Set("q", query)
		}
		v.EveryoneHref = "/ui/sessions"
		if len(ev) > 0 {
			v.EveryoneHref += "?" + ev.Encode()
		}
		v.MineHref = "/ui/sessions?" + mv.Encode()
	}
	a.renderBrowser(w, r, "sessions", pageData{Title: msg("nav.sessions"), SessionsV: v})
}

func foundText(matches, sessions int) string {
	m := msg("sessions.matches_many", matches)
	if matches == 1 {
		m = msg("sessions.matches_one")
	}
	s := msg("sessions.in_sessions_many", sessions)
	if sessions == 1 {
		s = msg("sessions.in_sessions_one")
	}
	return m + " " + s
}

func (a *app) listRow(row store.SessionRow, links []store.SessionLink, now time.Time) listRow {
	s := row.Session
	lr := listRow{
		Meta:     sessionMeta(s),
		Duration: formatDuration(s.StartedAt, s.LastSeenAt),
		Time:     clock(s.LastSeenAt),
		Stamp:    s.LastSeenAt,
		Private:  !row.Readable,
	}
	if t := parseStamp(s.LastSeenAt); !t.IsZero() && now.Sub(t) < liveWindow {
		lr.Live = true
		lr.Time = msg("sessions.live")
	}
	if !row.Readable {
		lr.Title = msg("sessions.private")
		return lr
	}
	lr.Href = "/ui/sessions/" + s.PublicID
	lr.Title = s.Title
	if lr.Title == "" {
		lr.Title = msg("sessions.untitled")
	}
	lr.Messages = strconv.Itoa(s.Messages)
	lr.Linked = linkChips(links)
	return lr
}

// linkChips fasst die Verknüpfungen für die Liste zusammen: bis zu zwei
// Requests, dazu die Zahl der Wissenseinträge.
func linkChips(links []store.SessionLink) []chip {
	var out []chip
	knowledge, requests := 0, 0
	for _, l := range links {
		switch l.Kind {
		case "request":
			requests++
			if requests <= 2 {
				out = append(out, chip{Href: "/ui/requests/" + strconv.FormatInt(l.ID, 10), Text: fmt.Sprintf("REQ-%d", l.ID)})
			}
		case "knowledge":
			knowledge++
		}
	}
	if requests > 2 {
		out = append(out, chip{Text: fmt.Sprintf("+%d", requests-2)})
	}
	if knowledge > 0 {
		out = append(out, chip{Href: "/ui/knowledge", Text: msg("sessions.n_knowledge", knowledge)})
	}
	return out
}

func hitLabel(kind, harness string) string {
	switch kind {
	case "user":
		return msg("sessions.who.you")
	case "assistant":
		if harness == "codex" {
			return "Codex"
		}
		return "Claude"
	case "thinking":
		return msg("sessions.who.thinking")
	case "command":
		return msg("sessions.who.command")
	case "output":
		return msg("sessions.who.output")
	}
	return kind
}

func (a *app) groupView(g store.SearchGroup, query, kind string, now time.Time) groupView {
	s := g.Session
	title := s.Title
	if title == "" {
		title = msg("sessions.untitled")
	}
	count := msg("sessions.matches_many", g.Total)
	if g.Total == 1 {
		count = msg("sessions.matches_one")
	}
	gv := groupView{PID: s.PublicID, Href: "/ui/sessions/" + s.PublicID, Title: title, Meta: sessionMeta(s), Count: count, Time: dayAndClock(s.LastSeenAt, now)}
	for _, h := range g.Hits {
		q := url.Values{"at": {strconv.Itoa(h.Seq)}, "q": {query}, "back": {"1"}}
		if kind != "" {
			q.Set("kind", kind)
		}
		hv := hitView{Href: sessionURL(s.PublicID, q, "c"+strconv.Itoa(h.Seq)), Kind: h.Kind, Label: hitLabel(h.Kind, s.Harness), Seq: h.Seq}
		for _, p := range h.Parts {
			hv.Parts = append(hv.Parts, partView{Text: p.Text, Hit: p.Hit})
		}
		gv.Hits = append(gv.Hits, hv)
	}
	if more := g.Total - len(g.Hits); more > 0 {
		q := url.Values{"q": {query}, "back": {"1"}}
		if kind != "" {
			q.Set("kind", kind)
		}
		gv.MoreHref = sessionURL(s.PublicID, q, "")
		gv.MoreText = msg("sessions.more_in_session", more)
	}
	return gv
}

func dayAndClock(stamp string, now time.Time) string {
	t := parseStamp(stamp)
	if t.IsZero() {
		return ""
	}
	label := dayLabel(t, now)
	return label + " " + t.Format("15:04")
}

// ------------------------------------------------------------------ Detail

func (a *app) sessionMissing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	a.renderBrowser(w, r, "session", pageData{Title: msg("nav.sessions"), SessionV: &sessionView{Missing: true}})
}

// lookupSession löst die Adresse auf. Eine alte laufende Nummer führt nur dann
// weiter, wenn der Betrachter die Session lesen darf; sonst ist sie nicht von
// einer unbekannten zu unterscheiden.
func (a *app) lookupSession(r *http.Request, pa *store.ProjectAccess) (sess store.Session, redirect string, ok bool) {
	raw := r.PathValue("id")
	if s, err := a.store.SessionByPublicID(raw); err == nil {
		if !pa.CanSeeTranscript(s) {
			return store.Session{}, "", false
		}
		return s, "", true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return store.Session{}, "", false
	}
	s, err := a.store.SessionByID(n)
	if err != nil || !pa.CanSeeTranscript(s) || s.PublicID == "" {
		return store.Session{}, "", false
	}
	target := "/ui/sessions/" + s.PublicID
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	return s, target, true
}

func (a *app) sessionPage(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	sess, redirect, ok := a.lookupSession(r, pa)
	if !ok {
		a.sessionMissing(w, r)
		return
	}
	if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}
	qv := r.URL.Query()
	q := strings.TrimSpace(qv.Get("q"))
	if utf8.RuneCountInString(q) > maxQueryRunes {
		q = string([]rune(q)[:maxQueryRunes])
	}
	at, _ := strconv.Atoi(qv.Get("at"))
	from := -1
	if f, err := strconv.Atoi(qv.Get("from")); err == nil && qv.Get("from") != "" {
		from = f
	}
	toolsOn, thinkingOn := qv.Get("tools") != "0", qv.Get("thinking") != "0"
	expanded := qv.Get("open") == "1"

	if !a.store.IndexProgress().Done {
		_ = a.store.IndexSession(sess.ID)
	}
	view := pa.SessionView(sess)
	v := &sessionView{
		PID: sess.PublicID, Title: sess.Title, Q: q, ToolsOn: toolsOn, ThinkingOn: thinkingOn, Expanded: expanded,
	}
	if v.Title == "" {
		v.Title = msg("sessions.untitled")
	}
	if t := parseStamp(sess.LastSeenAt); !t.IsZero() && time.Since(t) < liveWindow {
		v.Live = true
	}
	v.Meta = detailMeta(view)

	// Zustand der Seite, der in jedem Link erhalten bleibt.
	state := func(extra url.Values, drop ...string) url.Values {
		out := url.Values{}
		if q != "" {
			out.Set("q", q)
		}
		if !toolsOn {
			out.Set("tools", "0")
		}
		if !thinkingOn {
			out.Set("thinking", "0")
		}
		if expanded {
			out.Set("open", "1")
		}
		if qv.Get("back") == "1" {
			out.Set("back", "1")
		}
		for _, d := range drop {
			out.Del(d)
		}
		for k, vs := range extra {
			out[k] = vs
		}
		return out
	}
	sessURL := func(extra url.Values, fragment string, drop ...string) string {
		return sessionURL(sess.PublicID, state(extra, drop...), fragment)
	}

	if qv.Get("back") == "1" && q != "" {
		bq := url.Values{"q": {q}}
		if k := qv.Get("kind"); slices.Contains(kindKeys, k) && k != "" {
			bq.Set("kind", k)
		}
		v.BackResults = chip{Href: "/ui/sessions?" + bq.Encode(), Text: msg("sessions.back_to_results", q)}
	}

	// Freigabe: nur der Besitzer der Session und der Owner des Projekts.
	if pa.CanShareSession(sess) {
		form := &levelForm{Action: "/ui/sessions/" + sess.PublicID + "/share"}
		for _, l := range []struct{ v, k string }{{store.VisPrivate, "sessions.level.private"}, {store.VisProject, "sessions.level.project"}, {store.VisGuests, "sessions.level.guests"}} {
			form.Levels = append(form.Levels, levelOption{Value: l.v, Label: msg(l.k), Current: sess.Visibility == l.v})
		}
		v.Share = form
	}

	// Verknüpfungen: nur Ziele, die der Betrachter sehen darf.
	links, _ := a.store.SessionLinks(pa, []int64{sess.ID})
	created := links[sess.ID]
	for _, l := range created {
		v.Created = append(v.Created, linkChip(l))
	}

	// Suche in der Session: Positionen gelten für die ganze Session.
	hlKind := ""
	if thinkingOn {
		hlKind = "all"
	}
	var hits []int
	if q != "" {
		hits, _ = a.store.SessionHits(pa, sess, q, hlKind)
	}
	v.HasFind = q != ""
	v.FindAction = "/ui/sessions/" + sess.PublicID
	v.Hidden = nil
	for _, key := range []string{"tools", "thinking", "open", "back", "kind"} {
		if val := state(url.Values{"kind": {qv.Get("kind")}}).Get(key); val != "" {
			v.Hidden = append(v.Hidden, chip{Text: key, Href: val})
		}
	}
	cur := -1
	if q != "" && len(hits) > 0 {
		for i, h := range hits {
			if h == at {
				cur = i
			}
		}
		n := len(hits)
		v.FindCount = msg("sessions.find_count", n)
		if cur >= 0 {
			v.FindPos = fmt.Sprintf("%d / %d", cur+1, n)
		}
		next, prev := 0, n-1
		if cur >= 0 {
			next, prev = (cur+1)%n, (cur-1+n)%n
		}
		v.NextHref = sessURL(url.Values{"at": {strconv.Itoa(hits[next])}}, "c"+strconv.Itoa(hits[next]), "from")
		v.PrevHref = sessURL(url.Values{"at": {strconv.Itoa(hits[prev])}}, "c"+strconv.Itoa(hits[prev]), "from")
	} else if q != "" {
		v.FindCount = msg("sessions.find_none")
	}

	v.ToolsHref = sessURL(toggleParam("tools", toolsOn), "")
	v.ThinkingHref = sessURL(toggleParam("thinking", thinkingOn), "")
	if expanded {
		v.ExpandHref = sessURL(nil, "", "open")
	} else {
		v.ExpandHref = sessURL(url.Values{"open": {"1"}}, "")
	}

	// Fenster mit Transkript.
	around := 0
	if at > 0 {
		around = at
	}
	win, err := a.store.SessionWindow(sess.ID, from, around, windowChunks)
	if err != nil {
		v.WindowError = true
		v.RetryHref = sessURL(nil, "")
		a.renderBrowser(w, r, "session", pageData{Title: v.Title, SessionV: v})
		return
	}
	v.Empty = len(win.Chunks) == 0
	hl := newHighlighter(q)
	v.Blocks = buildBlocks(sess, win.Chunks, hl, at, expanded, toolsOn, thinkingOn, created)
	if win.Earlier {
		v.Earlier = chip{Href: sessURL(url.Values{"from": {strconv.Itoa(win.EarlierFrom)}}, "", "at"), Text: msg("sessions.show_earlier", windowChunks)}
	}
	if win.Later {
		v.Later = chip{Href: sessURL(url.Values{"from": {strconv.Itoa(win.LaterFrom)}}, "", "at"), Text: msg("sessions.show_later", windowChunks)}
	}

	// Gliederung: die Prompts der Session.
	outline, _ := a.store.SessionOutline(sess.ID)
	first, last := -1, -1
	if len(win.Chunks) > 0 {
		first, last = win.Chunks[0].Seq, win.Chunks[len(win.Chunks)-1].Seq
	}
	focus := at
	if focus == 0 && last >= 0 && from < 0 {
		focus = last
	} else if focus == 0 && first >= 0 {
		focus = first
	}
	allPrompts := qv.Get("prompts") == "all"
	for i, p := range outline {
		pv := promptView{N: i + 1, Text: ellipsize(p.Text, 140), Time: clock(p.Time), Tools: msg("sessions.tool_calls", p.ToolCalls)}
		if p.ToolCalls == 1 {
			pv.Tools = msg("sessions.tool_call_one")
		}
		nextSeq := 1 << 30
		if i+1 < len(outline) {
			nextSeq = outline[i+1].Seq
		}
		if q != "" {
			n := 0
			for _, h := range hits {
				if h >= p.Seq && h < nextSeq {
					n++
				}
			}
			if n > 0 {
				pv.Hits = msg("sessions.n_hits", n)
			}
		}
		pv.Current = p.Seq <= focus && focus < nextSeq
		if p.Seq >= first && p.Seq <= last && first >= 0 {
			pv.Href = "#c" + strconv.Itoa(p.Seq)
		} else {
			pv.Href = sessURL(url.Values{"at": {strconv.Itoa(p.Seq)}}, "c"+strconv.Itoa(p.Seq), "from")
		}
		if !allPrompts && i >= promptsShown && !pv.Current {
			continue
		}
		v.Prompts = append(v.Prompts, pv)
	}
	if !allPrompts && len(outline) > promptsShown {
		v.MorePrompts = chip{Href: sessURL(url.Values{"prompts": {"all"}}, ""), Text: msg("sessions.show_all_prompts", len(outline))}
	}
	a.renderBrowser(w, r, "session", pageData{Title: v.Title, SessionV: v})
}

func toggleParam(name string, on bool) url.Values {
	if on {
		return url.Values{name: {"0"}}
	}
	return url.Values{name: {"1"}}
}

func linkChip(l store.SessionLink) chip {
	switch l.Kind {
	case "request":
		return chip{Href: "/ui/requests/" + strconv.FormatInt(l.ID, 10), Text: fmt.Sprintf("REQ-%d %s", l.ID, ellipsize(l.Label, 40))}
	}
	return chip{Href: "/ui/knowledge", Text: msg("sessions.knowledge_ref", l.ID)}
}

func detailMeta(s store.Session) string {
	parts := []string{sessionMeta(s)}
	if t := parseStamp(s.StartedAt); !t.IsZero() {
		parts = append(parts, t.Format("2 Jan 15:04"))
	}
	if d := formatDuration(s.StartedAt, s.LastSeenAt); d != "" {
		parts = append(parts, d)
	}
	if s.Messages > 0 {
		parts = append(parts, msg("sessions.n_messages", s.Messages))
	}
	return strings.Join(parts, " · ")
}

// sessionShare stellt die Freigabestufe ein (POST, CSRF, interaktive Sitzung).
func (a *app) sessionShare(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	sess, redirect, ok := a.lookupSession(r, pa)
	if !ok || redirect != "" {
		a.sessionMissing(w, r)
		return
	}
	level := r.FormValue("level")
	switch level {
	case store.VisPrivate, store.VisProject, store.VisGuests:
	default:
		http.Error(w, "unknown level", http.StatusBadRequest)
		return
	}
	if err := a.store.SetSessionVisibility(sess.ID, pa, level); err != nil {
		switch {
		case errors.Is(err, store.ErrAccessForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, store.ErrAccessNotFound):
			a.sessionMissing(w, r)
		default:
			http.Error(w, "could not change the setting", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/ui/sessions/"+sess.PublicID, http.StatusSeeOther)
}

// --------------------------------------------------------- Blöcke aufbauen

var fenceRe = regexp.MustCompile("(?s)```[a-zA-Z0-9_+-]*\n?(.*?)(?:```|$)")

// messageHTML formatiert Fließtext: Absätze und umzäunte Codeblöcke.
func messageHTML(text string, hl highlighter, current bool) template.HTML {
	var b strings.Builder
	pos := 0
	para := func(s string) {
		for _, p := range strings.Split(strings.TrimSpace(s), "\n\n") {
			if strings.TrimSpace(p) != "" {
				b.WriteString("<p>" + inlineHTML(strings.TrimSpace(p), hl, current) + "</p>")
			}
		}
	}
	for _, m := range fenceRe.FindAllStringSubmatchIndex(text, -1) {
		para(text[pos:m[0]])
		code := strings.TrimRight(text[m[2]:m[3]], "\n")
		b.WriteString(`<pre class="code">` + string(hl.html(code, current)) + "</pre>")
		pos = m[1]
	}
	para(text[pos:])
	return template.HTML(b.String())
}

// inlineHTML formats `code` spans inside a paragraph.
func inlineHTML(p string, hl highlighter, current bool) string {
	parts := strings.Split(p, "`")
	if len(parts)%2 == 0 {
		// Unbalanced backticks stay as they are.
		return string(hl.html(p, current))
	}
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + string(hl.html(part, current)) + "</code>")
		} else {
			b.WriteString(string(hl.html(part, current)))
		}
	}
	return b.String()
}

func codeLines(text string, hl highlighter, current bool) []codeLine {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	out := make([]codeLine, len(lines))
	for i, l := range lines {
		out[i] = codeLine{N: i + 1, HTML: hl.html(l, current)}
	}
	return out
}

// outputCode zeigt die ersten Zeilen und legt den Rest hinter einen Schalter.
func outputCode(label, text string, hl highlighter, current, all bool) *codeView {
	lines := codeLines(text, hl, current)
	cv := &codeView{Label: label, Total: msg("sessions.n_lines", len(lines))}
	if len(lines) == 1 {
		cv.Total = msg("sessions.one_line")
	}
	if all || len(lines) <= resultLines+2 {
		cv.Head = lines
		return cv
	}
	cv.Head, cv.Rest = lines[:resultLines], lines[resultLines:]
	cv.RestLabel = msg("sessions.show_all_lines", len(lines))
	return cv
}

func diffStat(files []transcript.DiffFile) (add, del int) {
	for _, f := range files {
		for _, l := range f.Lines {
			switch l.Op {
			case '+':
				add++
			case '-':
				del++
			}
		}
	}
	return
}

func buildBlocks(sess store.Session, chunks []store.Chunk, hl highlighter, at int, expanded, toolsOn, thinkingOn bool, links []store.SessionLink) []blockView {
	var raw []transcript.Block
	for _, c := range chunks {
		parsed := transcript.Parse(sess.Harness, c.Seq, c.Raw).Blocks
		// A line the parser does not know but the collector extracted text from
		// still belongs in the transcript.
		if len(parsed) == 0 && strings.TrimSpace(c.Text) != "" && (c.Role == "user" || c.Role == "assistant") {
			kind := transcript.KindUser
			if c.Role == "assistant" {
				kind = transcript.KindAssistant
			}
			parsed = []transcript.Block{{Seq: c.Seq, Kind: kind, Text: strings.TrimSpace(c.Text)}}
		}
		raw = append(raw, parsed...)
	}
	blocks := transcript.Pair(raw)
	notes := slices.Clone(links)
	slices.SortStableFunc(notes, func(x, y store.SessionLink) int { return x.Seq - y.Seq })
	ni := 0
	if len(blocks) > 0 {
		// Notizen vor diesem Fenster stehen in einem anderen Fenster.
		for ni < len(notes) && notes[ni].Seq < blocks[0].Seq {
			ni++
		}
	}
	var out []blockView
	seen := map[int]bool{}
	for i, b := range blocks {
		if bv, ok := blockFor(sess, b, hl, at, expanded, toolsOn, thinkingOn); ok {
			// Several blocks can come from one stored line; the first carries
			// the address of the line, the others are reached through it.
			if seen[b.Seq] && bv.Kind != "tool" && bv.Kind != "result" {
				bv.ID = ""
			}
			seen[b.Seq] = true
			out = append(out, bv)
		}
		// Eine Notiz steht hinter dem Block, in dessen Spanne die Session das
		// Ziel angelegt oder gesichtet hat.
		next := 1 << 30
		if i+1 < len(blocks) {
			next = blocks[i+1].Seq
		}
		var chips []chip
		for ni < len(notes) && notes[ni].Seq < next {
			chips = append(chips, linkChip(notes[ni]))
			ni++
		}
		if len(chips) > 0 {
			out = append(out, blockView{Kind: "note", NoteLabel: msg("sessions.created"), Notes: chips})
		}
	}
	return out
}

func blockFor(sess store.Session, b transcript.Block, hl highlighter, at int, expanded, toolsOn, thinkingOn bool) (blockView, bool) {
	target := at > 0 && (b.Seq == at || (b.ResultSeq != 0 && b.ResultSeq == at))
	bv := blockView{Seq: b.Seq, ID: "c" + strconv.Itoa(b.Seq), Time: clock(b.Time), Stamp: b.Time, Target: target}
	switch b.Kind {
	case transcript.KindUser:
		bv.Kind, bv.Who = "user", msg("sessions.who.you")
		bv.Body = messageHTML(b.Text, hl, target)
	case transcript.KindAssistant:
		bv.Kind = "assistant"
		bv.Who = hitLabel("assistant", sess.Harness)
		bv.Body = messageHTML(b.Text, hl, target)
	case transcript.KindThinking:
		if !thinkingOn && !target {
			return bv, false
		}
		bv.Kind = "thinking"
		bv.Summary = msg("sessions.thinking_words", len(strings.Fields(b.Text)))
		bv.Open = expanded || target
		bv.Body = messageHTML(b.Text, hl, target)
	case transcript.KindTool, transcript.KindResult:
		if !toolsOn && !target {
			return bv, false
		}
		bv = toolBlock(b, hl, target, expanded)
		bv.Seq, bv.Time, bv.Stamp, bv.ID, bv.Target = b.Seq, clock(b.Time), b.Time, "c"+strconv.Itoa(b.Seq), target
		if b.ResultSeq != 0 && b.ResultSeq != b.Seq {
			bv.Extra = []string{"c" + strconv.Itoa(b.ResultSeq)}
		}
	default:
		return bv, false
	}
	return bv, true
}

func toolBlock(b transcript.Block, hl highlighter, target, expanded bool) blockView {
	bv := blockView{Kind: "tool", Tool: b.Tool, Open: target || expanded, Failed: b.Failed}
	if b.Kind == transcript.KindResult {
		bv.Kind = "result"
		bv.Tool = msg("sessions.who.output")
		bv.Result = resultSummary(b)
		if b.HasOutput {
			bv.Output = outputCode("", b.Output, hl, target, false)
			bv.Output.Label = msg("sessions.who.output")
		}
		return bv
	}
	arg := ellipsize(firstLine(b.Input), titleArgRunes)
	if add, del := diffStat(b.Diff); add+del > 0 {
		arg += fmt.Sprintf("  +%d −%d", add, del)
	}
	bv.Arg = hl.html(arg, false)
	bv.Result = resultSummary(b)
	switch {
	case len(b.Diff) > 0:
		for _, f := range b.Diff {
			dv := diffView{Path: f.Path}
			for _, l := range f.Lines {
				cls, sign := "dl", " "
				switch l.Op {
				case '+':
					cls, sign = "dl dl-add", "+"
				case '-':
					cls, sign = "dl dl-del", "−"
				case '@':
					cls, sign = "dl dl-hunk", "@"
				}
				dv.Lines = append(dv.Lines, diffLineView{Class: cls, Sign: sign, HTML: hl.html(l.Text, target)})
			}
			bv.Diffs = append(bv.Diffs, dv)
		}
	case b.Input != "" && strings.Contains(b.Input, "\n"):
		bv.Input = &codeView{Label: msg("sessions.who.command"), Head: codeLines(b.Input, hl, target)}
	case b.Input != "" && b.Tool == "Bash" || b.Tool == "exec_command":
		bv.Input = &codeView{Label: msg("sessions.who.command"), Head: codeLines(b.Input, hl, target)}
	}
	if b.HasOutput {
		bv.Output = outputCode(msg("sessions.who.output"), b.Output, hl, target, false)
	}
	return bv
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func resultSummary(b transcript.Block) string {
	switch {
	case b.Failed:
		return b.Status
	case !b.HasOutput:
		return ""
	}
	n := len(strings.Split(strings.TrimRight(b.Output, "\n"), "\n"))
	switch {
	case strings.TrimSpace(b.Output) == "":
		return msg("sessions.ok")
	case n == 1:
		return msg("sessions.one_line")
	}
	return msg("sessions.n_lines", n)
}
