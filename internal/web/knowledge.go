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
	Stale                    bool
	Excerpt                  string
	Age                      string
	Source                   string
}

type knowledgeView struct {
	Q, Type, Level, Project string
	Selects                 []filterSelect
	Rows                    []KnowledgeRow
	Guest, Reviewer         bool
	Pending                 string
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

type knowledgeCard struct {
	KnowledgeRow
	Blocks      []bodyBlock
	Person      string
	ConfirmedBy string
	Origin      string
	Seen        string
	Evidence    []evidenceRow
	MoreEvid    bool
	Proof       *proofView
	CanApprove  bool
	CanReject   bool
	CanRestore  bool
	CanEdit     bool
	RawBody     string
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

type reviewView struct {
	Cards  []knowledgeCard
	Result *resultLine
	Empty  bool
	Guest  bool
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
	"restored": "knowledge.done.restored", "saved": "knowledge.done.saved",
}

func typeLabel(typ string) string {
	if key, ok := typeKeys[typ]; ok {
		return msg(key)
	}
	return typ
}

func levelLabel(level string) string {
	if key, ok := levelKeys[level]; ok {
		return msg(key)
	}
	return level
}

func (a *app) knowledgeRowFor(pa *store.ProjectAccess, raw store.Knowledge, withProject bool) KnowledgeRow {
	k := pa.KnowledgeView(raw)
	row := KnowledgeRow{
		ID: strconv.FormatInt(k.ID, 10), Href: "/ui/knowledge/" + strconv.FormatInt(k.ID, 10), Title: k.Title,
		Type: k.Type, TypeLabel: typeLabel(k.Type), Level: k.Confidence, LevelLabel: levelLabel(k.Confidence),
		Stale: k.Status == "stale", Excerpt: excerpt(k.Body), Age: shortAge(overviewNow().UTC(), parseTime(firstNonEmpty(k.ObservedAt, k.CreatedAt))),
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
	live := k.Status == "active" || k.Status == "stale"
	canEdit := pa.CheckKnowledge(k, store.ActEdit) == nil
	card.CanApprove = live && approveChanges(k, canEdit) && pa.CheckKnowledge(k, store.ActVerify) == nil
	card.CanReject = canEdit && live
	card.CanRestore = canEdit && k.Status == "deprecated"
	card.CanEdit = canEdit && k.Status != "superseded"
}

// approveChanges: ob "übernehmen" an diesem Eintrag noch etwas ändert. Wer nur
// das Verified-Recht hat, setzt Stufe und Bestätiger; wieder aktiv wird ein
// veralteter Eintrag nur durch das Recht, den Status zu ändern.
func approveChanges(k store.Knowledge, canEdit bool) bool {
	return k.Confidence != "verified" || (k.Status == "stale" && canEdit)
}

func (a *app) cardFor(r *http.Request, pa *store.ProjectAccess, k store.Knowledge, withProject bool, blockRunes int, next, filter string) knowledgeCard {
	view := pa.KnowledgeView(k)
	card := knowledgeCard{
		KnowledgeRow: a.knowledgeRowFor(pa, k, withProject), Blocks: bodyBlocks(view.Body, blockRunes),
		Person: view.Person, ConfirmedBy: view.ConfirmedBy, Origin: view.Origin,
		Token: csrfOf(r), Next: next, Filter: filter,
	}
	capabilities(pa, k, &card)
	if card.CanEdit {
		card.RawBody = k.Body
	}
	return card
}

// typeOptions bietet die wählbaren Arten an und immer auch die aktuelle (etwa
// "instruction"), damit ein unveränderter Speichervorgang die Art behält.
func typeOptions(current string) []filterOption {
	out := make([]filterOption, 0, len(knowledgeTypes)+1)
	for _, t := range knowledgeTypes {
		out = append(out, filterOption{Value: t, Label: typeLabel(t), Selected: t == current})
	}
	if current != "" && !slices.Contains(knowledgeTypes, current) {
		out = append(out, filterOption{Value: current, Label: typeLabel(current), Selected: true})
	}
	return out
}

// ---------------------------------------------------------------------- Liste

func (a *app) knowledgePage(w http.ResponseWriter, r *http.Request) {
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
	var entries []store.Knowledge
	var err error
	if hidden {
		// Wie ein unbekanntes Projekt: keine Abfrage, leere Liste.
	} else if q != "" {
		entries, err = a.store.SearchAllKnowledge(q, scope.Axes{Project: project}, a.overfetch(knowledgeShown))
	} else {
		entries, err = a.store.BrowseKnowledge(project, typ, level, a.overfetch(knowledgeShown))
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	entries = keep(entries, knowledgeShown, func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && (k.Status == "active" || k.Status == "stale") &&
			(typ == "" || k.Type == typ) && (level == "" || k.Confidence == level)
	})
	who := a.shellBaseFor(r).who
	v := &knowledgeView{Q: q, Type: typ, Level: level, Project: project, Guest: who.kind == viewerGuest, Reviewer: who.reviewer}
	for _, k := range entries {
		v.Rows = append(v.Rows, a.knowledgeRowFor(pa, k, project == ""))
	}
	typeSel := filterSelect{Name: "type", Label: msg("knowledge.all_types"), Options: typeOptions(typ)}
	levelSel := filterSelect{Name: "level", Label: msg("knowledge.all_levels")}
	for _, l := range knowledgeLevel {
		levelSel.Options = append(levelSel.Options, filterOption{Value: l, Label: levelLabel(l), Selected: l == level})
	}
	v.Selects = []filterSelect{typeSel, levelSel}
	filtered := q != "" || typ != "" || level != ""
	v.Empty, v.NoMatch = len(v.Rows) == 0 && !filtered, len(v.Rows) == 0 && filtered
	v.ClearHref = "/ui/knowledge"
	if project != "" {
		v.ClearHref += "?project=" + url.QueryEscape(project)
	}
	if v.Reviewer && !hidden {
		pending, perr := a.store.PendingKnowledge(project, a.overfetch(reviewShown))
		if perr == nil {
			if n := len(keep(pending, reviewShown, pa.CanSeeKnowledge)); n > 0 {
				v.Pending = strconv.Itoa(n)
				if n >= reviewShown {
					v.Pending += "+"
				}
			}
		}
	}
	a.renderBrowser(w, r, "knowledge", pageData{Title: "Knowledge", Project: project, KnowledgeV: v})
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
	res := &resultLine{Token: csrfOf(r), Next: next, Filter: filter, Kind: kind, Text: msg(doneKeys[kind], pa.KnowledgeView(k).Title)}
	res.CanRestore = kind == "rejected" && k.Status == "deprecated" && pa.CheckKnowledge(k, store.ActEdit) == nil
	res.ID = strconv.FormatInt(id, 10)
	res.Was = restorableStatus(r.URL.Query().Get("was"))
	return res
}

func (a *app) knowledgeItemPage(w http.ResponseWriter, r *http.Request) {
	k, ok := a.lookupKnowledge(w, r, store.ActRead)
	if !ok {
		return
	}
	pa := a.access(r)
	card := a.cardFor(r, pa, k, true, 0, "item", "")
	v := &knowledgeItemView{Card: card, Types: typeOptions(k.Type), Back: "/ui/knowledge", EditOpen: card.CanEdit && r.URL.Query().Get("edit") == "1"}
	if k.Scope.Project != "" {
		v.Back += "?project=" + url.QueryEscape(k.Scope.Project)
	}
	a.fillEvidence(pa, k, &card, evidenceShown*3)
	v.Card = card
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
			v.History = append(v.History, historyRow{Title: h.Title, By: by, ReplacedBy: h.ChangedBy, Age: shortAge(now, parseTime(h.ChangedAt)), Blocks: bodyBlocks(h.Body, 0)})
		}
	}
	v.Result = a.resultFor(r, pa, "item", "")
	a.renderBrowser(w, r, "knowledgeitem", pageData{Title: k.Title, KnowledgeItemV: v})
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
	pa := a.access(r)
	project := a.projectParam(r)
	hidden, stop := a.gateList(w, r, pa, project, store.ResKnowledge)
	if stop {
		return
	}
	var entries []store.Knowledge
	if !hidden {
		var err error
		if entries, err = a.store.PendingKnowledge(project, a.overfetch(reviewShown)); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	entries = keep(entries, reviewShown, pa.CanSeeKnowledge)
	v := &reviewView{Guest: a.shellBaseFor(r).who.kind == viewerGuest, Result: a.resultFor(r, pa, "review", project)}
	for _, k := range entries {
		card := a.cardFor(r, pa, k, project == "", reviewBodyRunes, "review", project)
		a.fillEvidence(pa, k, &card, evidenceShown)
		v.Cards = append(v.Cards, card)
	}
	v.Empty = len(v.Cards) == 0
	a.renderBrowser(w, r, "review", pageData{Title: "Review", Project: project, ReviewV: v})
}

// reviewDecide setzt, was das Backend kennt: übernehmen (verified, mit dem
// Namen des Prüfers), ablehnen (deprecated) und zurückholen (active). Das
// Recht prüft der Server am Eintrag, wie PATCH /api/knowledge.
func (a *app) reviewDecide(w http.ResponseWriter, r *http.Request) {
	verdict := r.PathValue("verdict")
	var act store.Action
	var patch map[string]string
	var done string
	switch verdict {
	case "approve":
		act, done = store.ActVerify, "approved"
		patch = map[string]string{"confidence": "verified", "confirmed_by": personOf(r)}
	case "reject":
		act, done = store.ActEdit, "rejected"
		patch = map[string]string{"status": "deprecated"}
	case "restore":
		act, done = store.ActEdit, "restored"
		patch = map[string]string{"status": restorableStatus(r.FormValue("status"))}
	default:
		http.NotFound(w, r)
		return
	}
	k, ok := a.lookupKnowledge(w, r, act)
	if !ok {
		return
	}
	live := k.Status == "active" || k.Status == "stale"
	canEdit := a.access(r).CheckKnowledge(k, store.ActEdit) == nil
	if verdict == "approve" {
		if canEdit && k.Status == "stale" {
			patch["status"] = "active"
		}
		if !live || !approveChanges(k, canEdit) {
			http.Error(w, "nothing to change", http.StatusConflict)
			return
		}
	} else if (verdict == "restore" && k.Status != "deprecated") || (verdict == "reject" && !live) {
		http.Error(w, "nothing to change", http.StatusConflict)
		return
	}
	if err := a.store.UpdateKnowledgeBy(k.ID, patch, personOf(r)); err != nil {
		http.Error(w, "could not save", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, a.afterDecision(r, k, done), http.StatusSeeOther)
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
	if done == "rejected" {
		q.Set("was", restorableStatus(k.Status))
	}
	if r.FormValue("next") == "item" {
		return "/ui/knowledge/" + strconv.FormatInt(k.ID, 10) + "?" + q.Encode()
	}
	if p := scope.NormalizeRemote(r.FormValue("project")); p != "" {
		q.Set("project", p)
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
