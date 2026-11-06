package web

import (
	"database/sql"
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

type bodyBlock struct {
	Code bool
	Text string
	// Parts zerlegt einen Absatz an `Rückwärtsstrichen` in Text und Code.
	Parts []inlinePart
}

type inlinePart struct {
	Code bool
	Text string
}

func inlineParts(text string) []inlinePart {
	var out []inlinePart
	for i, seg := range strings.Split(text, "`") {
		if seg != "" {
			out = append(out, inlinePart{Code: i%2 == 1, Text: seg})
		}
	}
	return out
}

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
}

type knowledgeView struct {
	Q, Type, Level, Project string
	Selects                 []filterSelect
	Rows                    []KnowledgeRow
	Guest, Reviewer         bool
	Empty, NoMatch          bool
	ClearHref               string
}

type evidenceRow struct {
	Quote, Href string
}

type historyRow struct {
	Title, By, ReplacedBy, Age string
	Blocks                     []bodyBlock
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
}

type knowledgeItemView struct {
	Card    knowledgeCard
	History []historyRow
	Result  *resultLine
	Types   []filterOption
	Back    string
	// EditOpen öffnet das Formular, wenn die Seite über "Bearbeiten" der
	// Prüfkarte erreicht wurde.
	EditOpen bool
	// Move ist das Verschieben-Formular (nil ohne Recht zu ändern); Moves das
	// Protokoll bisheriger Verschiebungen.
	Move    *moveView
	Moves   []moveRow
	Closure *closureRow
	// Pains are the pains this entry motivates, as far as the viewer may read them.
	Pains []requestPainLine
}

// ----------------------------------------------------------------- Textblöcke

// bodyBlocks teilt einen Text in Absätze und Codeblöcke (```). max kürzt auf
// so viele Zeichen und hängt "…" an (0 = nicht kürzen).
func bodyBlocks(s string, limit int) []bodyBlock {
	var out []bodyBlock
	budget := limit
	clipped := false
	add := func(b bodyBlock) {
		if clipped || strings.TrimSpace(b.Text) == "" {
			return
		}
		if limit > 0 {
			n := utf8.RuneCountInString(b.Text)
			if n > budget {
				b.Text = strings.TrimRight(string([]rune(b.Text)[:max(budget, 0)]), " \n") + "…"
				clipped = true
				b.Parts = inlineParts(b.Text)
				out = append(out, b)
				return
			}
			budget -= n
		}
		if !b.Code {
			b.Parts = inlineParts(b.Text)
		}
		out = append(out, b)
	}
	for i, seg := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "```") {
		if i%2 == 1 {
			// Die erste Zeile eines Blocks ist die Sprache, wenn sie ein einzelnes Wort ist.
			if head, rest, ok := strings.Cut(seg, "\n"); ok && !strings.ContainsAny(strings.TrimSpace(head), " \t") && len(strings.TrimSpace(head)) <= 16 {
				seg = rest
			}
			add(bodyBlock{Code: true, Text: strings.Trim(seg, "\n")})
			continue
		}
		for _, para := range strings.Split(seg, "\n\n") {
			add(bodyBlock{Text: strings.TrimSpace(para)})
		}
	}
	return out
}

// excerpt ist der erste Absatz, gekürzt; fehlt er, die erste Zeile Code.
func excerpt(s string) string {
	blocks := bodyBlocks(s, 0)
	text := ""
	for _, b := range blocks {
		if !b.Code {
			text = b.Text
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
		ID: strconv.FormatInt(k.ID, 10), Href: "/ui/knowledge/" + strconv.FormatInt(k.ID, 10), Title: k.Title,
		Type: k.Type, TypeLabel: typeLabel(l, k.Type), Level: k.Confidence, LevelLabel: levelLabel(l, k.Confidence), LevelTitle: levelTitle(l, k.Confidence), TypeTitle: typeLabel(l, k.Type),
		Stale: k.Status == "stale", Excerpt: excerpt(k.Body), Age: shortAge(l, overviewNow().UTC(), parseTime(firstNonEmpty(k.ObservedAt, k.CreatedAt))),
		Old: olderThan(firstNonEmpty(k.ObservedAt, k.CreatedAt), overviewNow().UTC(), reviewOldAfter),
	}
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
	if !slices.Contains(knowledgeTypes, typ) {
		typ = ""
	}
	if !slices.Contains(knowledgeLevel, level) {
		level = ""
	}
	project := a.projectParam(r)
	pa := a.access(r)
	hidden, stop := a.gateList(w, r, pa, project, store.ResKnowledge)
	if stop {
		return
	}
	readable := func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && (k.Status == "active" || k.Status == "stale") &&
			(typ == "" || k.Type == typ) && (level == "" || k.Confidence == level)
	}
	var entries []store.Knowledge
	var err error
	if hidden {
		// Wie ein unbekanntes Projekt: keine Abfrage, leere Liste.
	} else if q != "" {
		entries, err = a.store.SearchKnowledgeWith(q, store.KnowledgeSearch{
			Axes: scope.Axes{Project: project}, Operator: true,
			Limit: a.overfetch(knowledgeShown), Viewer: pa.Viewer(), Keep: readable, Prefilter: pa.KnowledgePrefilter(),
		})
	} else {
		entries, err = a.store.BrowseKnowledge(project, typ, level, a.overfetch(knowledgeShown), pa.Viewer())
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	entries = keep(entries, knowledgeShown, readable)
	who := a.shellBaseFor(r).who
	v := &knowledgeView{Q: q, Type: typ, Level: level, Project: project, Guest: who.kind == viewerGuest, Reviewer: who.reviewer}
	for _, k := range entries {
		v.Rows = append(v.Rows, a.knowledgeRowFor(l, pa, k, project == ""))
	}
	typeSel := filterSelect{Name: "type", Label: l.msg("knowledge.all_types"), Options: typeOptions(l, typ)}
	levelSel := filterSelect{Name: "level", Label: l.msg("knowledge.all_levels")}
	for _, lv := range knowledgeLevel {
		levelSel.Options = append(levelSel.Options, filterOption{Value: lv, Label: levelLabel(l, lv), Selected: lv == level})
	}
	v.Selects = []filterSelect{typeSel, levelSel}
	filtered := q != "" || typ != "" || level != ""
	v.Empty, v.NoMatch = len(v.Rows) == 0 && !filtered, len(v.Rows) == 0 && filtered
	v.ClearHref = "/ui/knowledge"
	if project != "" {
		v.ClearHref += "?project=" + url.QueryEscape(project)
	}
	a.renderBrowser(w, r, "knowledge", pageData{Title: l.msg("nav.knowledge"), Project: project, KnowledgeV: v})
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
	pa := a.access(r)
	card := a.cardFor(r, pa, k, true, 0, "item", "")
	v := &knowledgeItemView{Card: card, Types: typeOptions(l, k.Type), Back: "/ui/knowledge", EditOpen: card.CanEdit && r.URL.Query().Get("edit") == "1"}
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
			v.History = append(v.History, historyRow{Title: h.Title, By: by, ReplacedBy: h.ChangedBy, Age: shortAge(l, now, parseTime(h.ChangedAt)), Blocks: bodyBlocks(h.Body, 0)})
		}
	}
	v.Result = a.resultFor(r, pa, "item", "")
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
		row.Href = "/ui/knowledge/" + strconv.FormatInt(next.ID, 10)
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
	v.OnlyGarden = r.URL.Query().Get("source") == "gardener"
	if v.OnlyGarden {
		entries = nil
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
	v.Empty = len(v.Cards) == 0 && v.Garden == nil
	a.renderBrowser(w, r, "review", pageData{Title: l.msg("nav.review"), Project: project, ReviewV: v})
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
	if err := a.store.UpdateKnowledgeBy(k.ID, plan.patch, personOf(r)); err != nil {
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
		if err := a.store.UpdateKnowledgeBy(d.k.ID, d.plan.patch, person); err != nil {
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
		if err := a.store.UpdateKnowledgeBy(k.ID, patch, personOf(r)); err != nil {
			http.Error(w, "could not save", http.StatusBadRequest)
			return
		}
	}
	q := url.Values{"done": {"saved"}, "k": {strconv.FormatInt(k.ID, 10)}}
	http.Redirect(w, r, "/ui/knowledge/"+strconv.FormatInt(k.ID, 10)+"?"+q.Encode(), http.StatusSeeOther)
}
