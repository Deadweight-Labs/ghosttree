package web

import (
	"database/sql"
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Wissen und Prüfung (REQ-435 P5). Jede Zeile stammt aus der für den Betrachter
// lesbaren Menge (CanSeeKnowledge, KnowledgeView, ReadableEvidence); Zähler und
// Verweise entstehen nur aus dieser Menge. Aktionen prüfen das Recht am
// einzelnen Eintrag auf dem Server; die Knöpfe der Seite sind nur die Folge
// dieser Prüfung, nie ihr Ersatz.

const (
	knowledgeShown   = 100
	reviewShown      = 50
	excerptRunes     = 260
	reviewBodyRunes  = 1600
	evidenceShown    = 4
	historyShown     = 20
	knowledgeMaxBody = 100_000
)

var (
	knowledgeTypes = []string{"pitfall", "decision", "note", "plan"}
	knowledgeLevel = []string{"verified", "trusted", "staged"}
)

type KnowledgeRow struct {
	ID, Href, Title, Project string
	Type, TypeLabel          string
	Level, LevelLabel        string
	LevelTitle, TypeTitle    string
	Stale                    bool
	Excerpt                  string
	Age                      string
	Source                   string
	// Old: the entry has waited a week or more for its decision.
	Old bool
	// Num is the number of the entry, Person who wrote it, Area the main area of
	// its origin, Git the anchor (commit, pull request, branch) it was written at.
	Num          int64
	Person, Area string
	Git          *anchorView
	// Stamp is the time of the entry as RFC 3339, for the relative time tag.
	Stamp string
	// Tone is the colour class of the type label.
	Tone string
}

// knowledgeTones colours the type label of an entry in the list and on the page.
var knowledgeTones = map[string]string{"pitfall": "warn", "decision": "acc", "note": "neu", "plan": "don", "instruction": "ok"}

// knSeg is one entry of the segment "Entries | Review | Agent context".
type knSeg struct {
	Kind, Label, Href, Count string
	Current                  bool
}

// knTab is one type tab of the list head: "All 48", "Pitfall 12".
type knTab struct {
	Label, Href, Count string
	Current            bool
}

type knowledgeView struct {
	Q, Type, Level, Project string
	Rows                    []KnowledgeRow
	Guest, Reviewer         bool
	Empty, NoMatch          bool
	ClearHref               string
	Segs                    []knSeg
	Tabs                    []knTab
	Menus                   []issueMenu
}

type evidenceRow struct {
	Quote, Href string
}

type historyRow struct {
	Title, By, ReplacedBy, Age string
	Blocks                     []bodyBlock
	// Sha and Branch are the git anchor the version had while it held.
	Sha, Branch string
}

// closureRow says how and why an entry left circulation: the label (retired,
// replaced by), the entry that replaced it, who, when, and the reason.
type closureRow struct {
	Label, Href, Title, By, Age, Reason string
}

type knowledgeCard struct {
	KnowledgeRow
	Blocks      []bodyBlock
	Person      string
	ConfirmedBy string
	Origin      string
	// Explain is the block "Kurz erklärt"; From is the line above it that says
	// which project and which context the entry comes from.
	Explain    *explainView
	From       *originView
	Seen       string
	Evidence   []evidenceRow
	MoreEvid   bool
	Proof      *proofView
	CanApprove bool
	CanReject  bool
	// Retire: the same action as Reject, on an entry that was already decided.
	Retire     bool
	CanRestore bool
	CanEdit    bool
	RawBody    string
	// Token, Next und Filter gehören den Formularen der Karte: CSRF-Token, die
	// Seite, auf die nach der Entscheidung zurückgeführt wird, und die Projektwahl.
	Token, Next, Filter string
}

type proofView struct {
	Source, Digest, Run, Quote string
}

type resultLine struct {
	Text                string
	ID                  string
	CanRestore          bool
	Kind                string
	Was                 string
	Token, Next, Filter string
}

// reviewGroup is the pending entries of one project: the first few shown, the
// rest behind one line, oldest first.
type reviewGroup struct {
	Label, Remote string
	Shown, More   []knowledgeCard
	Count         int
}

// reviewShownPerGroup is how many cards of a project show before the line
// "N more in project".
const reviewShownPerGroup = 3

type reviewView struct {
	Cards  []knowledgeCard
	Groups []reviewGroup
	// Titled: the group heads are shown, because more than one project is listed.
	Titled bool
	Result *resultLine
	Empty  bool
	Guest  bool
	// Garden is the gardener's morning report; OnlyGarden narrows the page to it.
	Garden     *gardenView
	OnlyGarden bool
	// Rows are notes and proposals as one list; Tabs and Segs the tabs of its head
	// and the segment above it; Source is the tab that is open ("", gardener, agents).
	Rows   []rvRow
	Tabs   []knTab
	Segs   []knSeg
	Source string
	// Held are the entries held back since their project came into existence,
	// in the projects where the viewer may release them.
	Held []heldRow
}

// heldRow is one held entry with what its kind is called and where it leads
// back to after a decision.
type heldRow struct {
	store.HeldEntry
	KindLabel string
	Back      string
	// Body is the entry's text, only on the preview page of a reviewer.
	Body string
}

var heldKindKeys = map[string]string{
	store.HeldKnowledge: "review.held.knowledge", store.HeldRequest: "review.held.request", store.HeldPain: "review.held.pain",
	store.HeldIdea: "review.held.idea", store.HeldDocument: "review.held.document",
}

type knowledgeItemView struct {
	Card knowledgeCard
	// Actor is who wrote the entry (REQ-620).
	Actor   actorView
	History []historyRow
	Result  *resultLine
	Types   []filterOption
	Back    string
	// EditOpen öffnet das Formular, wenn die Seite über "Bearbeiten" der
	// Prüfkarte erreicht wurde.
	EditOpen bool
	// EditBody is the field of the body in the edit form: write and preview, the toolbar.
	EditBody composerView
	// Move ist das Verschieben-Formular (nil ohne Recht zu ändern); Moves das
	// Protokoll bisheriger Verschiebungen.
	Move    *moveView
	Moves   []moveRow
	Closure *closureRow
	// Pains are the pains this entry motivates, as far as the viewer may read them.
	Pains []requestPainLine
	// The page of the new look: revisions, timeline and the side column.
	Revs     []knRevision
	Timeline []tlItem
	Side     knSide
	// Panel names the form that is open ("move"), from ?panel=.
	Panel string
	// Comments, the reactions under the text and the box to write a comment, as on
	// an issue (fillKnowledgeComments); CommentsN counts the comments not deleted.
	Comments      []tlItem
	CommentsN     int
	Reactions     *reactionsView
	Composer      *composerView
	CommentResult *requestResult
}

// ----------------------------------------------------------------- Textblöcke

// excerpt ist der erste Absatz, gekürzt; fehlt er, die erste Zeile Code.
func excerpt(s string) string {
	blocks := bodyBlocks(s, 0)
	text := ""
	for _, b := range blocks {
		if b.Kind == "" || b.Kind == "h" || b.Kind == "list" || b.Kind == "quote" {
			lines := strings.Split(b.Text, "\n")
			for i, l := range lines {
				lines[i] = mdPlain(l)
			}
			text = strings.Join(lines, " ")
			break
		}
	}
	if text == "" && len(blocks) > 0 {
		text, _, _ = strings.Cut(blocks[0].Text, "\n")
	}
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "`", "")), " ")
	if utf8.RuneCountInString(text) > excerptRunes {
		text = strings.TrimRight(string([]rune(text)[:excerptRunes]), " ,.;:") + "…"
	}
	return text
}

// ---------------------------------------------------------------------- Zeilen

var typeKeys = map[string]string{
	"pitfall": "knowledge.type.pitfall", "decision": "knowledge.type.decision", "note": "knowledge.type.note",
	"plan": "knowledge.type.plan", "instruction": "knowledge.type.instruction",
}

var levelKeys = map[string]string{
	"verified": "knowledge.level.verified", "trusted": "knowledge.level.trusted",
	"staged": "knowledge.level.staged", "quarantined": "knowledge.level.quarantined",
}

var doneKeys = map[string]string{
	"approved": "knowledge.done.approved", "rejected": "knowledge.done.rejected",
	"released": "knowledge.done.released", "retired": "knowledge.done.retired", "restored": "knowledge.done.restored", "saved": "knowledge.done.saved",
	"merged": "knowledge.done.merged", "covered": "knowledge.done.covered", "untestable": "knowledge.done.untestable",
	"criterion": "knowledge.done.criterion", "described": "knowledge.done.described",
}

func typeLabel(l lang, typ string) string {
	if key, ok := typeKeys[typ]; ok {
		return l.msg(key)
	}
	return typ
}

// levelTitle is the tooltip of a status mark: what the status means for agents.
func levelTitle(l lang, level string) string {
	if key, ok := levelTitleKeys[level]; ok {
		return l.msg(key)
	}
	return ""
}

var levelTitleKeys = map[string]string{
	"verified": "knowledge.level.verified.title", "trusted": "knowledge.level.trusted.title",
	"staged": "knowledge.level.staged.title", "quarantined": "knowledge.level.quarantined.title",
}

func levelLabel(l lang, level string) string {
	if key, ok := levelKeys[level]; ok {
		return l.msg(key)
	}
	return level
}

func (a *app) knowledgeRowFor(l lang, pa *store.ProjectAccess, raw store.Knowledge, withProject bool) KnowledgeRow {
	k := pa.KnowledgeView(raw)
	row := KnowledgeRow{
		ID: strconv.FormatInt(k.ID, 10), Href: knowledgeHref(k.Scope.Project, k.ID), Title: k.Title,
		Type: k.Type, TypeLabel: typeLabel(l, k.Type), Level: k.Confidence, LevelLabel: levelLabel(l, k.Confidence), LevelTitle: levelTitle(l, k.Confidence), TypeTitle: typeLabel(l, k.Type),
		Stale: k.Status == "stale", Excerpt: excerpt(k.Body), Age: shortAge(l, overviewNow().UTC(), parseTime(firstNonEmpty(k.ObservedAt, k.CreatedAt))),
		Old: olderThan(firstNonEmpty(k.ObservedAt, k.CreatedAt), overviewNow().UTC(), reviewOldAfter),
		Num: k.ID, Person: k.Person, Tone: knowledgeTones[k.Type], Stamp: firstNonEmpty(k.ObservedAt, k.CreatedAt),
	}
	if k.OriginContext != nil {
		row.Area = k.OriginContext.Main()
	}
	row.Git = anchorViewOf(pa, k.Scope.Project, k.GitAnchor, originVia(k.OriginContext))
	if withProject && k.Scope.Project != "" {
		row.Project = path.Base(k.Scope.Project)
	}
	// Der Verweis kommt aus dem Rohwert: Nummer und Fundort bleiben im Server,
	// die Adresse entsteht nur, wenn der Betrachter das Transkript lesen darf.
	if pub, frag, ok := pa.SessionLink(raw.SessionRef); ok {
		row.Source = "/ui/sessions/" + pub + fragmentOf(frag)
	}
	return row
}

func fragmentOf(frag string) string {
	if _, err := strconv.Atoi(frag); err == nil {
		return "#c" + frag
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// capabilities setzt die Knöpfe einer Karte aus den Rechten am Eintrag.
func capabilities(pa *store.ProjectAccess, k store.Knowledge, card *knowledgeCard) {
	canEdit := pa.CheckKnowledge(k, store.ActEdit) == nil
	open := decisionOpen(k)
	card.CanApprove = open && approveChanges(k, canEdit) && (pa.CheckKnowledge(k, store.ActVerify) == nil || pa.CanReleaseKnowledge(k))
	live := k.Status == "active" || k.Status == "stale"
	card.CanReject = live && canEdit
	card.Retire = card.CanReject && !open
	card.CanRestore = canEdit && k.Status == "deprecated"
	card.CanEdit = canEdit && k.Status != "superseded"
}

// decisionOpen: someone still has to decide about this entry. A trusted or
// verified entry has been decided; it only offers Edit. A stale one asks for a
// look, a staged or quarantined one for a verdict.
func decisionOpen(k store.Knowledge) bool {
	if k.Status != "active" && k.Status != "stale" {
		return false
	}
	return k.Status == "stale" || k.Confidence == "staged" || k.Confidence == "quarantined"
}

// approveChanges: ob "übernehmen" an diesem Eintrag noch etwas ändert. Wer nur
// das Verified-Recht hat, setzt Stufe und Bestätiger; wieder aktiv wird ein
// veralteter Eintrag nur durch das Recht, den Status zu ändern.
func approveChanges(k store.Knowledge, canEdit bool) bool {
	return k.Confidence != "verified" || (k.Status == "stale" && canEdit)
}

func (a *app) cardFor(r *http.Request, pa *store.ProjectAccess, k store.Knowledge, withProject bool, blockRunes int, next, filter string) knowledgeCard {
	l := a.langFor(r)
	view := pa.KnowledgeView(k)
	card := knowledgeCard{
		KnowledgeRow: a.knowledgeRowFor(l, pa, k, withProject), Blocks: bodyBlocks(view.Body, blockRunes),
		Person: view.Person, ConfirmedBy: view.ConfirmedBy, Origin: view.Origin,
		Token: csrfOf(r), Next: next, Filter: filter,
	}
	capabilities(pa, k, &card)
	if card.CanEdit {
		card.RawBody = k.Body
	}
	// The header of a card in an all-projects list already names the project.
	fromProject := k.Scope.Project
	if withProject {
		fromProject = ""
	}
	card.From = a.originFor(l, pa, fromProject, card.Source, k.Title, k.Body)
	card.Explain = a.explainFor(l, explain.KindKnowledge, k.ID)
	return card
}

// typeOptions bietet die wählbaren Arten an und immer auch die aktuelle (etwa
// "instruction"), damit ein unveränderter Speichervorgang die Art behält.
func typeOptions(l lang, current string) []filterOption {
	out := make([]filterOption, 0, len(knowledgeTypes)+1)
	for _, t := range knowledgeTypes {
		out = append(out, filterOption{Value: t, Label: typeLabel(l, t), Selected: t == current})
	}
	if current != "" && !slices.Contains(knowledgeTypes, current) {
		out = append(out, filterOption{Value: current, Label: typeLabel(l, current), Selected: true})
	}
	return out
}

// ---------------------------------------------------------------------- Liste

func (a *app) knowledgePage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	typ, level := r.URL.Query().Get("type"), r.URL.Query().Get("level")
	area, author := r.URL.Query().Get("area"), r.URL.Query().Get("author")
	if !slices.Contains(knowledgeTypes, typ) {
		typ = ""
	}
	if !slices.Contains(knowledgeLevel, level) {
		level = ""
	}
	project := a.projectParam(r)
	r = a.repoScoped(r, project, "knowledge")
	pa := a.access(r)
	hidden, stop := a.gateList(w, r, pa, project, store.ResKnowledge)
	if stop {
		return
	}
	live := func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && (k.Status == "active" || k.Status == "stale")
	}
	fetch := func(typ, level string) ([]store.Knowledge, error) {
		if hidden {
			// Wie ein unbekanntes Projekt: keine Abfrage, leere Liste.
			return nil, nil
		}
		keepFn := func(k store.Knowledge) bool {
			return live(k) && (typ == "" || k.Type == typ) && (level == "" || k.Confidence == level)
		}
		var found []store.Knowledge
		var err error
		if q != "" {
			found, err = a.store.SearchKnowledgeWith(q, store.KnowledgeSearch{
				Axes: scope.Axes{Project: project}, Operator: true,
				Limit: a.overfetch(knowledgeShown), Viewer: pa.Viewer(), Keep: keepFn, Prefilter: pa.KnowledgePrefilter(),
			})
		} else {
			found, err = a.store.BrowseKnowledge(project, typ, level, a.overfetch(knowledgeShown), pa.Viewer())
		}
		return keep(found, knowledgeShown, keepFn), err
	}
	all, err := fetch("", "")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	entries := all
	if typ != "" || level != "" {
		if entries, err = fetch(typ, level); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	who := a.shellBaseFor(r).who
	v := &knowledgeView{Q: q, Type: typ, Level: level, Project: project, Guest: who.kind == viewerGuest, Reviewer: who.reviewer}
	rows := make([]KnowledgeRow, 0, len(entries))
	// The list read does not carry the git anchor and the origin; the entry read does.
	enrich := func(k store.Knowledge) store.Knowledge {
		if full, err := a.store.KnowledgeByID(k.ID); err == nil {
			k.GitAnchor, k.OriginContext = full.GitAnchor, full.OriginContext
		}
		return k
	}
	for _, k := range entries {
		rows = append(rows, a.knowledgeRowFor(l, pa, enrich(k), project == ""))
	}
	allRows := rows
	if typ != "" || level != "" {
		allRows = allRows[:0:0]
		for _, k := range all {
			allRows = append(allRows, a.knowledgeRowFor(l, pa, enrich(k), project == ""))
		}
	}
	// Bereich und Autor wählen aus dem, was die Liste kennt.
	var areas, authors []string
	for _, row := range allRows {
		if row.Area != "" && !slices.Contains(areas, row.Area) {
			areas = append(areas, row.Area)
		}
		if row.Person != "" && !slices.Contains(authors, row.Person) {
			authors = append(authors, row.Person)
		}
	}
	slices.Sort(areas)
	slices.Sort(authors)
	for _, row := range rows {
		if (area == "" || row.Area == area) && (author == "" || row.Person == author) {
			v.Rows = append(v.Rows, row)
		}
	}
	// Tabs: every type with its count among the entries the page knows.
	counts := map[string]int{}
	for _, row := range allRows {
		counts[row.Type]++
	}
	v.Tabs = append(v.Tabs, knTab{Label: l.msg("knowledge.tab.all"), Href: withQuery(r, map[string]string{"type": ""}), Count: countText(len(allRows)), Current: typ == ""})
	for _, t := range knowledgeTypes {
		v.Tabs = append(v.Tabs, knTab{Label: typeLabel(l, t), Href: withQuery(r, map[string]string{"type": t}), Count: countText(counts[t]), Current: typ == t})
	}
	status := issueMenu{Label: l.msg("knowledge.menu.level"), Active: level != ""}
	status.Items = append(status.Items, issueMenuItem{Label: l.msg("knowledge.all_levels"), Href: withQuery(r, map[string]string{"level": ""}), Current: level == ""})
	for _, lv := range knowledgeLevel {
		status.Items = append(status.Items, issueMenuItem{Label: levelLabel(l, lv), Href: withQuery(r, map[string]string{"level": lv}), Current: level == lv})
	}
	v.Menus = append(v.Menus, status)
	if len(areas) > 0 {
		m := issueMenu{Label: l.msg("knowledge.menu.area"), Active: area != ""}
		m.Items = append(m.Items, issueMenuItem{Label: l.msg("knowledge.menu.any_area"), Href: withQuery(r, map[string]string{"area": ""}), Current: area == ""})
		for _, name := range areas {
			m.Items = append(m.Items, issueMenuItem{Label: name, Href: withQuery(r, map[string]string{"area": name}), Current: area == name})
		}
		v.Menus = append(v.Menus, m)
	}
	if len(authors) > 0 {
		m := issueMenu{Label: l.msg("knowledge.menu.author"), Active: author != ""}
		m.Items = append(m.Items, issueMenuItem{Label: l.msg("knowledge.menu.any_author"), Href: withQuery(r, map[string]string{"author": ""}), Current: author == ""})
		for _, name := range authors {
			m.Items = append(m.Items, issueMenuItem{Label: name, Href: withQuery(r, map[string]string{"author": name}), Current: author == name})
		}
		v.Menus = append(v.Menus, m)
	}
	filtered := q != "" || typ != "" || level != "" || area != "" || author != ""
	v.Empty, v.NoMatch = len(v.Rows) == 0 && !filtered, len(v.Rows) == 0 && filtered
	v.ClearHref = knowledgeBase(r, project)
	v.Segs = a.knowledgeSegs(r, pa, l, project, "entries", countText(len(allRows)))
	a.renderBrowser(w, r, "knowledge", pageData{Title: l.msg("nav.knowledge"), Project: project, KnowledgeV: v})
}

// knowledgeBase is the address of the entry list: in the repository path when the
// page stands in one, else with the project as query.
func knowledgeBase(r *http.Request, project string) string {
	return knowledgeSub(r, project, "/ui/knowledge", "/knowledge")
}

func knowledgeSub(r *http.Request, project, global, sub string) string {
	if rv := repoFrom(r); rv != nil {
		return rv.Href + sub
	}
	return pathWith(global, "project", project)
}

// knowledgeSegs is the segment above the three knowledge pages. The count of the
// entries comes from the caller (it already has them); the number waiting for
// review is the one the viewer may see.
func (a *app) knowledgeSegs(r *http.Request, pa *store.ProjectAccess, l lang, project, current, entries string) []knSeg {
	// Only who may review has the other two pages; for everyone else the list stands alone.
	if !a.shellBaseFor(r).who.reviewer {
		return nil
	}
	pending := countText(a.pendingFor(pa, project))
	segs := []knSeg{
		{Kind: "entries", Label: l.msg("knowledge.seg.entries"), Href: knowledgeBase(r, project), Count: entries},
		{Kind: "review", Label: l.msg("knowledge.seg.review"), Href: knowledgeSub(r, project, "/ui/review", "/review"), Count: pending},
		{Kind: "context", Label: l.msg("knowledge.seg.context"), Href: knowledgeSub(r, project, "/ui/context", "/context")},
	}
	for i := range segs {
		segs[i].Current = segs[i].Kind == current
	}
	return segs
}

// --------------------------------------------------------------------- Detail

func (a *app) lookupKnowledge(w http.ResponseWriter, r *http.Request, act store.Action) (store.Knowledge, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.Knowledge{}, false
	}
	k, err := a.store.KnowledgeByID(id)
	if err == sql.ErrNoRows {
		a.access(r).Filtered()
		http.NotFound(w, r)
		return store.Knowledge{}, false
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return store.Knowledge{}, false
	}
	// Wer den Eintrag nicht lesen darf, bekommt bei jeder Aktion dieselbe
	// Antwort wie bei einer unbekannten Nummer, nicht "verboten".
	if act != store.ActRead && a.accessDenied(w, r, a.access(r).CheckKnowledge(k, store.ActRead)) {
		return store.Knowledge{}, false
	}
	if a.accessDenied(w, r, a.access(r).CheckKnowledge(k, act)) {
		return store.Knowledge{}, false
	}
	return k, true
}

func (a *app) resultFor(r *http.Request, pa *store.ProjectAccess, next, filter string) *resultLine {
	l := a.langFor(r)
	kind := r.URL.Query().Get("done")
	if _, ok := doneKeys[kind]; !ok {
		return nil
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("k"), 10, 64)
	if err != nil {
		return nil
	}
	k, err := a.store.KnowledgeByID(id)
	if err != nil || !pa.CanSeeKnowledge(k) {
		return nil
	}
	res := &resultLine{Token: csrfOf(r), Next: next, Filter: filter, Kind: kind, Text: l.msg(doneKeys[kind], pa.KnowledgeView(k).Title)}
	res.CanRestore = (kind == "rejected" || kind == "retired") && k.Status == "deprecated" && pa.CheckKnowledge(k, store.ActEdit) == nil
	res.ID = strconv.FormatInt(id, 10)
	res.Was = restorableStatus(r.URL.Query().Get("was"))
	return res
}

func (a *app) knowledgeItemPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	k, ok := a.lookupKnowledge(w, r, store.ActRead)
	if !ok {
		return
	}
	// The old address leads to the page in the repository of the entry.
	if r.PathValue("a") == "" && k.Scope.Project != "" && a.canOpenRepo(r, a.access(r), k.Scope.Project) {
		http.Redirect(w, r, withRawQuery(knowledgeHref(k.Scope.Project, k.ID), r), http.StatusFound)
		return
	}
	r = a.repoScoped(r, k.Scope.Project, "knowledge")
	pa := a.access(r)
	card := a.cardFor(r, pa, k, true, 0, "item", "")
	v := &knowledgeItemView{Card: card, Types: typeOptions(l, k.Type), Back: "/ui/knowledge", EditOpen: card.CanEdit && r.URL.Query().Get("edit") == "1"}
	v.EditBody = composerView{Name: "body", Value: card.RawBody, Placeholder: l.msg("knowledge.field_body"), NoButtons: true, Required: true, Tall: true}
	if k.Scope.Project != "" {
		v.Back += "?project=" + url.QueryEscape(k.Scope.Project)
	}
	a.fillEvidence(pa, k, &card, evidenceShown*3)
	v.Card = card
	if card.CanEdit {
		v.Move = a.knowledgeMoveView(r, pa, k)
	}
	v.Moves = a.knowledgeMoveRows(l, pa, k)
	v.Closure = a.closureRowFor(l, pa, k)
	for _, p := range pa.PainsFor("knowledge", k.ID) {
		v.Pains = append(v.Pains, requestPainLine{Href: painURL(p.ID), Title: p.Title, StateLabel: label(l, painStateKeys, p.State)})
	}
	// Frühere Fassungen tragen keine Vertrauensstufe und sind für Gäste tabu.
	if hist, err := a.store.KnowledgeHistory(k.ID); err == nil && pa.CanSeeKnowledgeHistory(k) {
		now := overviewNow().UTC()
		// hist läuft von neu nach alt. Der Autor einer Fassung ist, wer die
		// nächstältere, inhaltlich andere Fassung ersetzt hat; die älteste stammt von der Person.
		newer := store.KnowledgeVersion{Type: k.Type, Title: k.Title, Body: k.Body}
		for i, h := range hist {
			by := h.Person
			for _, o := range hist[i+1:] {
				if o.Type != h.Type || o.Title != h.Title || o.Body != h.Body {
					by = o.ChangedBy
					break
				}
			}
			unchanged := h.Type == newer.Type && h.Title == newer.Title && h.Body == newer.Body
			newer = h
			if unchanged || len(v.History) >= historyShown {
				continue
			}
			row := historyRow{Title: h.Title, By: by, ReplacedBy: h.ChangedBy, Age: shortAge(l, now, parseTime(h.ChangedAt)), Blocks: bodyBlocks(h.Body, 0)}
			if h.GitAnchor != nil && !h.GitAnchor.IsZero() {
				row.Sha, row.Branch = h.GitAnchor.Short(), pa.BranchView(k.Scope.Project, h.GitAnchor.Branch)
			}
			v.History = append(v.History, row)
		}
	}
	v.Result = a.resultFor(r, pa, "item", "")
	v.Panel = r.URL.Query().Get("panel")
	a.fillKnowledgeItem(l, pa.KnowledgeView(k), v)
	a.fillKnowledgeComments(r, l, pa, k, v)
	v.Actor = a.actorViewOf(l, pa, actor.Knowledge, k.ID, v.Card.Person, true)
	a.renderBrowser(w, r, "knowledgeitem", pageData{Title: k.Title, KnowledgeItemV: v})
}

func (a *app) closureRowFor(l lang, pa *store.ProjectAccess, k store.Knowledge) *closureRow {
	c := pa.KnowledgeClosure(k)
	if c == nil {
		return nil
	}
	row := &closureRow{Label: l.msg("knowledge.closure.retired"), By: c.By, Age: shortAge(l, overviewNow().UTC(), parseTime(c.At)), Reason: c.Reason}
	if c.Kind == "superseded" {
		next, err := a.store.KnowledgeByID(c.SupersededBy)
		if err != nil {
			return nil
		}
		row.Label = l.msg("knowledge.closure.superseded")
		row.Href = knowledgeHref(next.Scope.Project, next.ID)
		row.Title = pa.KnowledgeView(next).Title
	}
	return row
}

// fillEvidence hängt Belege, Wiederholungen und Herkunftsnachweis an eine
// Karte, alles aus der für den Betrachter lesbaren Menge.
func (a *app) fillEvidence(pa *store.ProjectAccess, k store.Knowledge, card *knowledgeCard, shown int) {
	evidence, err := a.store.EvidenceFor(k.ID)
	if err != nil {
		return
	}
	recurrence, err := a.store.Recurrence(k.ID)
	if err != nil {
		return
	}
	evidence, recurrence = pa.ReadableEvidence(k.Scope.Project, evidence, recurrence)
	if recurrence > 1 {
		card.Seen = strconv.Itoa(recurrence)
	}
	ids := make([]int64, 0, len(evidence))
	for _, e := range evidence {
		ids = append(ids, e.SessionID)
	}
	links := a.sessionLinks(pa, ids)
	for i, e := range evidence {
		if i >= shown {
			card.MoreEvid = true
			break
		}
		row := evidenceRow{Quote: e.Quote}
		if href := links[e.SessionID]; href != "" {
			row.Href = href + "#c" + strconv.Itoa(e.ChunkSeq)
		}
		card.Evidence = append(card.Evidence, row)
	}
	proof, err := a.store.MigrationEvidenceForKnowledge(k.ID)
	if err != nil {
		return
	}
	if view := pa.MigrationEvidenceView(k.Scope.Project, &proof); view != nil {
		card.Proof = &proofView{Source: view.Source, Digest: view.Digest, Run: strconv.FormatInt(view.RunID, 10), Quote: view.Quote}
	}
}

// --------------------------------------------------------------------- Prüfung

func (a *app) reviewPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	pa := a.access(r)
	project := a.projectParam(r)
	r = a.repoScoped(r, project, "knowledge")
	hidden, stop := a.gateList(w, r, pa, project, store.ResKnowledge)
	if stop {
		return
	}
	var entries []store.Knowledge
	if !hidden {
		var err error
		if entries, err = a.store.PendingKnowledge(project, a.overfetch(reviewShown), pa.Viewer()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	entries = keep(entries, reviewShown, pa.CanSeeKnowledge)
	v := &reviewView{Guest: a.shellBaseFor(r).who.kind == viewerGuest, Result: a.resultFor(r, pa, "review", project)}
	if v.Result == nil {
		v.Result = a.bulkResult(r)
	}
	// The gardener's notes lead the page as one block; the proposals of agents
	// follow, by project.
	var notes []store.Knowledge
	rest := entries[:0:0]
	for _, k := range entries {
		if k.Harness == gardenerHarness {
			notes = append(notes, k)
		} else {
			rest = append(rest, k)
		}
	}
	entries = rest
	gardenN, agentN := len(notes), len(entries)
	v.Source = r.URL.Query().Get("source")
	v.OnlyGarden = v.Source == "gardener"
	if v.OnlyGarden {
		entries = nil
	}
	if v.Source == "agents" {
		notes = nil
	}
	// Oldest first: what has waited longest is on top of its project.
	slices.SortStableFunc(entries, func(x, y store.Knowledge) int {
		return strings.Compare(firstNonEmpty(x.ObservedAt, x.CreatedAt), firstNonEmpty(y.ObservedAt, y.CreatedAt))
	})
	slices.SortStableFunc(notes, func(x, y store.Knowledge) int {
		return strings.Compare(firstNonEmpty(x.ObservedAt, x.CreatedAt), firstNonEmpty(y.ObservedAt, y.CreatedAt))
	})
	v.Garden = a.gardenBlock(r, pa, project, notes)
	byProject := map[string]*reviewGroup{}
	var order []string
	for _, k := range entries {
		card := a.cardFor(r, pa, k, project == "", reviewBodyRunes, "review", project)
		a.fillEvidence(pa, k, &card, evidenceShown)
		v.Cards = append(v.Cards, card)
		g := byProject[k.Scope.Project]
		if g == nil {
			g = &reviewGroup{Remote: k.Scope.Project, Label: lastSegment(k.Scope.Project)}
			byProject[k.Scope.Project] = g
			order = append(order, k.Scope.Project)
		}
		g.Count++
		g.Shown = append(g.Shown, card)
	}
	for _, remote := range order {
		g := byProject[remote]
		// One card behind "1 more" would hide a card only to show a line.
		if len(g.Shown) > reviewShownPerGroup+1 {
			g.Shown, g.More = g.Shown[:reviewShownPerGroup], g.Shown[reviewShownPerGroup:]
		}
		v.Groups = append(v.Groups, *g)
	}
	slices.SortStableFunc(v.Groups, func(x, y reviewGroup) int { return strings.Compare(x.Label, y.Label) })
	v.Titled = len(v.Groups) > 1 || project == ""
	v.Held = a.heldRows(pa, l, project)
	v.Empty = len(v.Cards) == 0 && v.Garden == nil
	v.Rows = reviewRows(l, v.Garden, v.Cards)
	v.Tabs = reviewTabs(r, l, gardenN, agentN, v.Source)
	v.Segs = a.knowledgeSegs(r, pa, l, project, "review", a.knowledgeCountText(pa, project))
	a.renderBrowser(w, r, "review", pageData{Title: l.msg("nav.review"), Project: project, ReviewV: v})
}

// heldRows lists the held entries of the project (or of every project of the
// viewer) where the viewer may decide them.
func (a *app) heldRows(pa *store.ProjectAccess, l lang, project string) []heldRow {
	projects := []string{project}
	if project == "" {
		projects = pa.Projects()
	}
	back := "/ui/review"
	if project != "" {
		back += "?project=" + url.QueryEscape(project)
	}
	var out []heldRow
	for _, p := range projects {
		list, err := pa.HeldEntries(p, false)
		if err != nil {
			continue
		}
		for _, h := range list {
			out = append(out, heldRow{HeldEntry: h, KindLabel: l.msg(heldKindKeys[h.Kind]), Back: back})
		}
		if len(out) >= reviewShown {
			break
		}
	}
	return out
}

// reviewHeldPreview shows a held entry to an owner, lead or reviewer of its
// project, so the decision is not blind. Only here, in the web: no other
// surface reads held text.
func (a *app) reviewHeldPreview(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := a.access(r).PreviewHeld(r.PathValue("kind"), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v := &heldRow{HeldEntry: p.HeldEntry, KindLabel: l.msg(heldKindKeys[p.Kind]), Back: "/ui/review#held", Body: p.Body}
	a.renderBrowser(w, r, "heldpreview", pageData{Title: p.Title, Project: p.Project, HeldV: v})
}

// reviewHeld releases or discards a held entry; the store checks the review
// right in its project.
func (a *app) reviewHeld(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	release := r.FormValue("release")
	if err != nil || (release != "1" && release != "0") {
		http.NotFound(w, r)
		return
	}
	switch err := a.store.DecideHeldEntry(browserPrincipal(r).ID, r.FormValue("kind"), id, release == "1"); {
	case errors.Is(err, store.ErrHeldNotFound), errors.Is(err, store.ErrNotReviewer):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The way back is one of two fixed addresses, never the form's value.
	back := "/ui/review"
	if r.FormValue("back") == "/ui/review#held" {
		back = "/ui/review#held"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// reviewPlan is what one decision changes: the right it needs, the patch, the
// word for the result line, and whether there is nothing left to change.
type reviewPlan struct {
	act      store.Action
	patch    map[string]string
	done     string
	conflict bool
}

// planDecision works out a verdict (approve, reject, restore) for one entry.
// Single decisions and the bulk action share it, so both ask the same right and
// refuse the same states.
func planDecision(pa *store.ProjectAccess, k store.Knowledge, verdict, person, status string) reviewPlan {
	var p reviewPlan
	switch verdict {
	case "approve":
		p.act, p.done = store.ActVerify, "approved"
		p.patch = map[string]string{"confidence": "verified", "confirmed_by": person}
		// Der Besitzer der privaten Herkunfts-Session gibt einen gehaltenen Fund
		// nur frei; er landet dann in der Queue der Prüfer.
		if pa.CanReleaseKnowledge(k) && pa.CheckKnowledge(k, store.ActVerify) != nil {
			p.act, p.done = store.ActRelease, "released"
			p.patch = map[string]string{"confidence": "staged", "confirmed_by": ""}
		}
	case "reject":
		p.act, p.done = store.ActEdit, "rejected"
		p.patch = map[string]string{"status": "deprecated"}
	case "restore":
		p.act, p.done = store.ActEdit, "restored"
		p.patch = map[string]string{"status": restorableStatus(status)}
	}
	live := k.Status == "active" || k.Status == "stale"
	canEdit := pa.CheckKnowledge(k, store.ActEdit) == nil
	if verdict == "reject" && !decisionOpen(k) {
		p.done = "retired"
	}
	if verdict == "approve" {
		if canEdit && k.Status == "stale" {
			p.patch["status"] = "active"
		}
		if !live || !approveChanges(k, canEdit) {
			p.conflict = true
		}
	} else if (verdict == "restore" && k.Status != "deprecated") || (verdict == "reject" && !live) {
		p.conflict = true
	}
	return p
}

// reviewDecide setzt, was das Backend kennt: übernehmen (verified, mit dem
// Namen des Prüfers), ablehnen (deprecated) und zurückholen (active). Das
// Recht prüft der Server am Eintrag, wie PATCH /api/knowledge.
func (a *app) reviewDecide(w http.ResponseWriter, r *http.Request) {
	verdict := r.PathValue("verdict")
	if verdict != "approve" && verdict != "reject" && verdict != "restore" {
		http.NotFound(w, r)
		return
	}
	// Wer den Eintrag nicht lesen darf, bekommt dieselbe Antwort wie bei einer
	// unbekannten Nummer; erst danach zählt das Recht der Aktion.
	k, ok := a.lookupKnowledge(w, r, store.ActRead)
	if !ok {
		return
	}
	pa := a.access(r)
	plan := planDecision(pa, k, verdict, personOf(r), r.FormValue("status"))
	if a.accessDenied(w, r, pa.CheckKnowledge(k, plan.act)) {
		return
	}
	if plan.conflict {
		http.Error(w, "nothing to change", http.StatusConflict)
		return
	}
	if err := a.store.UpdateKnowledgeFromWeb(k.ID, plan.patch, personOf(r)); err != nil {
		if a.secretRefused(w, r, err, "") {
			return
		}
		http.Error(w, "could not save", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, a.afterDecision(r, k, plan.done), http.StatusSeeOther)
}

// bulkMax is how many entries one bulk decision takes.
const bulkMax = 100

// reviewBulk approves or rejects the ticked entries in one go: all or none.
// Every entry is checked on its own first, with the right a single decision
// needs; one that the viewer may not read answers like an unknown number, one
// that may not be changed refuses the whole batch, and nothing is written.
func (a *app) reviewBulk(w http.ResponseWriter, r *http.Request) {
	verdict := r.FormValue("verdict")
	if verdict != "approve" && verdict != "reject" {
		http.NotFound(w, r)
		return
	}
	var ids []int64
	seen := map[int64]bool{}
	for _, raw := range r.Form["id"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > bulkMax {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	person := personOf(r)
	type decision struct {
		k    store.Knowledge
		plan reviewPlan
	}
	var todo []decision
	for _, id := range ids {
		k, err := a.store.KnowledgeByID(id)
		if err == sql.ErrNoRows {
			pa.Filtered()
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if a.accessDenied(w, r, pa.CheckKnowledge(k, store.ActRead)) {
			return
		}
		plan := planDecision(pa, k, verdict, person, "")
		if a.accessDenied(w, r, pa.CheckKnowledge(k, plan.act)) {
			return
		}
		if plan.conflict {
			http.Error(w, "nothing to change", http.StatusConflict)
			return
		}
		todo = append(todo, decision{k, plan})
	}
	for _, d := range todo {
		if err := a.store.UpdateKnowledgeFromWeb(d.k.ID, d.plan.patch, person); err != nil {
			http.Error(w, "could not save", http.StatusBadRequest)
			return
		}
	}
	q := url.Values{}
	if len(todo) > 0 {
		q.Set("done", "bulk_"+verdict)
		q.Set("n", strconv.Itoa(len(todo)))
	}
	if r.FormValue("source") == "gardener" {
		q.Set("source", "gardener")
	}
	if back := gardenBack(r, q); back != "" {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if p := scope.NormalizeRemote(r.FormValue("project")); p != "" {
		q.Set("project", p)
	}
	http.Redirect(w, r, "/ui/review"+queryOf(q), http.StatusSeeOther)
}

// restorableStatus: nur ein lebender Status wird wiederhergestellt, alles
// andere wird active.
func restorableStatus(s string) string {
	if s == "stale" {
		return s
	}
	return "active"
}

// afterDecision: zurück dorthin, wo entschieden wurde. Das Ziel wählt der
// Server aus einer festen Liste, nie aus einer mitgesandten Adresse.
func (a *app) afterDecision(r *http.Request, k store.Knowledge, done string) string {
	q := url.Values{"done": {done}, "k": {strconv.FormatInt(k.ID, 10)}}
	if done == "rejected" || done == "retired" {
		q.Set("was", restorableStatus(k.Status))
	}
	if r.FormValue("next") == "item" {
		return "/ui/knowledge/" + strconv.FormatInt(k.ID, 10) + "?" + q.Encode()
	}
	if back := gardenBack(r, q); back != "" {
		return back
	}
	if p := scope.NormalizeRemote(r.FormValue("project")); p != "" {
		q.Set("project", p)
	}
	if r.FormValue("source") == "gardener" {
		q.Set("source", "gardener")
	}
	return "/ui/review?" + q.Encode()
}

func (a *app) knowledgeEdit(w http.ResponseWriter, r *http.Request) {
	k, ok := a.lookupKnowledge(w, r, store.ActEdit)
	if !ok {
		return
	}
	// Ein Textfeld sendet CRLF; gespeichert und verglichen wird mit LF.
	unix := strings.NewReplacer("\r\n", "\n", "\r", "\n")
	title, body, typ := strings.TrimSpace(unix.Replace(r.FormValue("title"))), strings.TrimSpace(unix.Replace(r.FormValue("body"))), r.FormValue("type")
	if typ == "" {
		typ = k.Type
	}
	if title == "" || body == "" || len(body) > knowledgeMaxBody || len(title) > 500 || (typ != k.Type && !slices.Contains(knowledgeTypes, typ)) {
		http.Error(w, "invalid entry", http.StatusBadRequest)
		return
	}
	patch := map[string]string{}
	if title != k.Title {
		patch["title"] = title
	}
	if body != k.Body {
		patch["body"] = body
	}
	if typ != k.Type {
		patch["type"] = typ
	}
	if len(patch) > 0 {
		if err := a.store.UpdateKnowledgeFromWeb(k.ID, patch, personOf(r)); err != nil {
			if a.secretRefused(w, r, err, "") {
				return
			}
			http.Error(w, "could not save", http.StatusBadRequest)
			return
		}
	}
	q := url.Values{"done": {"saved"}, "k": {strconv.FormatInt(k.ID, 10)}}
	http.Redirect(w, r, "/ui/knowledge/"+strconv.FormatInt(k.ID, 10)+"?"+q.Encode(), http.StatusSeeOther)
}
