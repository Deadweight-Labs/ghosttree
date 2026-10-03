package store

import (
	"database/sql"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// Lesen der Sessions für die Weboberfläche (REQ-435 P10).
//
// Grundregel (#2447): zuerst die für den Betrachter lesbare Menge bilden
// (CanSeeSessionMeta für die Liste, CanSeeTranscript für Suche und Treffer),
// dann erst suchen, zählen, Facetten und Seitenränder daraus ableiten. Kein
// zweiter COUNT über den Gesamtbestand, kein Ausschnitt aus einem Chunk, den der
// Betrachter nicht lesen darf.

// SessionFilter engt die lesbare Menge ein.
type SessionFilter struct {
	Project string
	Machine string
	Harness string
	// Since: nur Sessions, zuletzt aktiv ab diesem Zeitpunkt (zero = alle).
	Since time.Time
	// Mine: nur eigene Sessions.
	Mine bool
}

type FacetOption struct {
	Value string
	Count int
}

// Facets zählt je Auswahl die Sessions der lesbaren Menge, die mit den anderen
// gesetzten Filtern übereinstimmen. Nie Gesamtzahlen des Bestands.
type Facets struct {
	Projects, Machines, Harnesses []FacetOption
}

// SessionRow ist eine Zeile der Liste. Readable sagt, ob das Transkript für den
// Betrachter lesbar ist; sonst ist die Zeile nur Metadaten (Titel, Zähler und
// Adresse sind dann leer).
type SessionRow struct {
	Session  Session
	Readable bool
}

type SessionListPage struct {
	Rows   []SessionRow
	Next   string
	Facets Facets
}

const (
	dimProject = iota
	dimMachine
	dimHarness
	dimTime
	dimMine
	dimAll = -1
)

func (a *ProjectAccess) matchFilter(s Session, f SessionFilter, skip int) bool {
	if skip != dimProject && f.Project != "" && s.Scope.Project != f.Project {
		return false
	}
	if skip != dimMachine && f.Machine != "" && a.guestView(s).Scope.Machine != f.Machine {
		return false
	}
	if skip != dimHarness && f.Harness != "" && s.Harness != f.Harness {
		return false
	}
	if skip != dimTime && !f.Since.IsZero() && s.LastSeenAt < f.Since.UTC().Format(time.RFC3339) {
		return false
	}
	if skip != dimMine && f.Mine && !a.OwnsSession(s) {
		return false
	}
	return true
}

// allSessions liest alle Sessionzeilen, neueste zuerst. Die Tabelle trägt kein
// raw und ist klein genug, um die lesbare Menge in Go zu bilden.
func (s *Store) allSessions() ([]Session, error) {
	rows, err := s.db.Query(`SELECT ` + sessionCols + ` FROM sessions ORDER BY last_seen_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	out, err := scanSessions(rows)
	if err != nil {
		return nil, err
	}
	return out, s.fillSessionOwners(out)
}

// guestView: wer in dem Projekt höchstens Gast ist und die Session nicht
// besitzt, sieht weder Maschine, Branch, Pfad noch Besitzer, nur Plattform und
// Projekt. Die laufende Nummer sieht ein Gast nie, auch bei der eigenen Session
// nicht: sie zählt alle Sessions des Servers (#2447). Ohne Durchsetzung sieht
// jeder alles (Log-Modus).
func (a *ProjectAccess) guestView(sess Session) Session {
	if !a.st.AccessEnforced() || RoleRank(a.Role(sess.Scope.Project).Role) >= 2 {
		return sess
	}
	sess.ID = 0
	if a.OwnsSession(sess) {
		return sess
	}
	sess.Scope.Machine, sess.Scope.Branch, sess.CWD = "", "", ""
	sess.Owner, sess.AccountID, sess.ExternalID = "", 0, ""
	return sess
}

// SeesSessionNumbers: der Betrachter darf die laufende Nummer der Sessions des
// Projekts kennen (Admin, Mitglied ab member, oder ohne Durchsetzung). Der Admin
// liest ohnehin jedes Transkript; ihm verrät die Nummer nichts Verborgenes.
func (a *ProjectAccess) SeesSessionNumbers(project string) bool {
	return !a.st.AccessEnforced() || a.IsAdmin() || RoleRank(a.Role(project).Role) >= 2
}

// GetsOwnSessionNumber: die Nummer der Session, die der Betrachter gerade selbst
// anlegt. Wer irgendwo Mitglied ist, kennt laufende Nummern ohnehin (auch die
// verborgener Sessions dazwischen); sie bei einer eigenen Session zurückzuhalten
// schützt nichts und lässt Sessions ohne Projekt (kein Git-Remote) oder in noch
// nicht beanspruchten Projekten für ältere Collector unerreichbar. Reine Gäste
// (und Konten ganz ohne Rolle) bekommen sie nie (#2447, #2482).
func (a *ProjectAccess) GetsOwnSessionNumber(project string) bool {
	if a.SeesSessionNumbers(project) {
		return true
	}
	for _, p := range a.Projects() {
		if RoleRank(a.Role(p).Role) >= 2 {
			return true
		}
	}
	return false
}

// ReadsAll: der Betrachter liest ohnehin jedes Transkript (Admin, oder ohne
// Durchsetzung). Nur dann darf eine Suche nach bm25 ordnen, das vom ganzen
// Index abhängt; für alle anderen wäre schon die Wahl der Ordnung ein Hinweis
// darauf, dass es Verborgenes gibt.
func (a *ProjectAccess) ReadsAll() bool {
	return !a.st.AccessEnforced() || a.IsAdmin()
}

func (a *ProjectAccess) isGuestOnly() bool {
	if !a.st.AccessEnforced() || a.IsAdmin() {
		return false
	}
	any := false
	for _, p := range a.Projects() {
		if RoleRank(a.Role(p).Role) >= 2 {
			return false
		}
		any = true
	}
	return any
}

func facetOptions(counts map[string]int) []FacetOption {
	out := make([]FacetOption, 0, len(counts))
	for v, n := range counts {
		if v != "" {
			out = append(out, FacetOption{Value: v, Count: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

// computeFacets zählt über cands, je Dimension ohne deren eigenen Filter.
func (a *ProjectAccess) computeFacets(cands []Session, f SessionFilter) Facets {
	proj, mach, harn := map[string]int{}, map[string]int{}, map[string]int{}
	for _, s := range cands {
		if a.matchFilter(s, f, dimProject) {
			proj[s.Scope.Project]++
		}
		if a.matchFilter(s, f, dimMachine) {
			mach[a.guestView(s).Scope.Machine]++
		}
		if a.matchFilter(s, f, dimHarness) {
			harn[s.Harness]++
		}
	}
	return Facets{Projects: facetOptions(proj), Machines: facetOptions(mach), Harnesses: facetOptions(harn)}
}

func (a *ProjectAccess) guestFacets(f Facets) Facets {
	f.Machines = nil
	return f
}

// BrowseSessions liefert eine Seite der Liste: alle Sessions, deren
// Metadaten der Betrachter sehen darf, neueste zuerst, mit Cursor.
func (s *Store) BrowseSessions(pa *ProjectAccess, f SessionFilter, cursor string, limit int) (SessionListPage, error) {
	if s.reader != nil {
		return s.reader.BrowseSessions(pa, f, cursor, limit)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	all, err := s.allSessions()
	if err != nil {
		return SessionListPage{}, err
	}
	var visible []Session
	for _, sess := range all {
		if pa.CanSeeSessionMeta(sess) {
			visible = append(visible, sess)
		}
	}
	page := SessionListPage{Facets: pa.computeFacets(visible, f)}
	if pa.isGuestOnly() {
		page.Facets = pa.guestFacets(page.Facets)
	}
	var kept []Session
	for _, sess := range visible {
		if pa.matchFilter(sess, f, dimAll) {
			kept = append(kept, sess)
		}
	}
	stamp := func(i int) string { return kept[i].LastSeenAt }
	start := timePosition(len(kept), cursor, stamp)
	end := min(start+limit, len(kept))
	for _, sess := range kept[start:end] {
		page.Rows = append(page.Rows, pa.row(sess))
	}
	if end < len(kept) && end > start {
		page.Next = encodeTimeCursor(stamp(end-1), countSameStamp(end, stamp))
	}
	return page, nil
}

// row bereitet eine Session für den Betrachter auf.
func (a *ProjectAccess) row(sess Session) SessionRow {
	readable := a.CanSeeTranscript(sess)
	if !readable {
		// Nur Metadaten: weder Titel (aus dem Inhalt abgeleitet) noch Zähler
		// noch eine Adresse, über die man hineinkäme.
		sess.Title, sess.Messages, sess.PublicID = "", 0, ""
	}
	return SessionRow{Session: a.guestView(sess), Readable: readable}
}

// Cursor der Liste: Zeitstempel der letzten gezeigten Zeile und wie viele
// Zeilen mit diesem Stempel schon gezeigt wurden. Beides stammt aus Zeilen, die
// der Betrachter sieht; keine interne Nummer, kein Rang (#2447).
func encodeTimeCursor(ts string, shown int) string { return "t|" + ts + "|" + strconv.Itoa(shown) }

func decodeTimeCursor(c string) (string, int, bool) {
	parts := strings.Split(c, "|")
	if len(parts) != 3 || parts[0] != "t" {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 0 {
		return "", 0, false
	}
	return parts[1], n, true
}

// countSameStamp zählt die Zeilen vor end mit dem Stempel der Zeile end-1.
func countSameStamp(end int, stamp func(i int) string) int {
	n := 0
	for i := end - 1; i >= 0 && stamp(i) == stamp(end-1); i-- {
		n++
	}
	return n
}

// timePosition findet den ersten Eintrag hinter dem Cursor in einer nach Zeit
// absteigend geordneten Liste. Ein unlesbarer Cursor beginnt von vorn.
func timePosition(n int, cursor string, stamp func(i int) string) int {
	ts, shown, ok := decodeTimeCursor(cursor)
	if !ok {
		return 0
	}
	i := sort.Search(n, func(i int) bool { return stamp(i) <= ts })
	for ; shown > 0 && i < n && stamp(i) == ts; shown-- {
		i++
	}
	return i
}

// ------------------------------------------------------------------- Suche

type SnippetPart struct {
	Text string
	Hit  bool
}

// SearchHit ist ein Treffer: Seq und Art ("user", "assistant", "thinking",
// "command", "output") und der Ausschnitt mit markierten Treffern.
type SearchHit struct {
	Seq   int
	Kind  string
	Parts []SnippetPart
}

type SearchGroup struct {
	Session Session
	Hits    []SearchHit
	// Total: alle Treffer dieser Session; Hits zeigt die besten drei.
	Total int
}

type SearchQuery struct {
	Q string
	// Kind: "" (Everything: Nachrichten, Befehle, Ergebnisse), "messages",
	// "output", "commands", "thinking" oder "all" (inklusive Denkblöcke).
	Kind   string
	Filter SessionFilter
	// Sort: "best" (Standard) oder "newest".
	Sort   string
	Cursor string
	Limit  int
}

type SearchPage struct {
	Groups []SearchGroup
	// Matches und Sessions zählen über alle Seiten, aber nur in der lesbaren
	// Menge und mit gesetzten Filtern.
	Matches, Sessions int
	Next              string
	Facets            Facets
}

var ftsColumns = []string{"text", "thinking", "tool_input", "tool_output"}

func kindColumns(kind string) []string {
	switch kind {
	case "messages":
		return []string{"text"}
	case "output":
		return []string{"tool_output"}
	case "commands":
		return []string{"tool_input"}
	case "thinking":
		return []string{"thinking"}
	case "all":
		return ftsColumns
	}
	return []string{"text", "tool_input", "tool_output"}
}

// matchExpr baut den FTS5-Ausdruck: alle Begriffe müssen im selben Chunk
// stehen, nur in den Feldern der gewählten Art. Leer, wenn kein Begriff übrig ist.
func matchExpr(q, kind string) string {
	terms := searchTerms(q)
	if len(terms) == 0 {
		return ""
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
		if len([]rune(t)) >= 3 {
			quoted[i] += "*"
		}
	}
	return "{" + strings.Join(kindColumns(kind), " ") + "} : (" + strings.Join(quoted, " ") + ")"
}

func idsJSON(sessions []Session) string {
	ids := make([]int64, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

type matchedSession struct {
	sess  Session
	rank  float64
	count int
}

// SearchTranscripts sucht in den Transkripten, die der Betrachter lesen darf.
func (s *Store) SearchTranscripts(pa *ProjectAccess, q SearchQuery) (SearchPage, error) {
	if s.reader != nil {
		return s.reader.SearchTranscripts(pa, q)
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 50
	}
	expr := matchExpr(q.Q, q.Kind)
	if expr == "" {
		return SearchPage{}, nil
	}
	all, err := s.allSessions()
	if err != nil {
		return SearchPage{}, err
	}
	var readable []Session
	byID := map[int64]Session{}
	for _, sess := range all {
		if pa.CanSeeTranscript(sess) {
			readable = append(readable, sess)
			byID[sess.ID] = sess
		}
	}
	if len(readable) == 0 {
		return SearchPage{}, nil
	}
	// bm25() gilt nur in der Abfrage, die den FTS-Index selbst liest; die
	// Verbindung zu chunk_index folgt deshalb in einer zweiten Stufe.
	// bm25 rechnet mit der Dokumenthäufigkeit des ganzen Index, also auch mit
	// verborgenen Sessions: wer nicht alles lesen darf, bekommt die Reihenfolge
	// nur aus der lesbaren Menge (Trefferzahl, dann Aktualität).
	restricted := !pa.ReadsAll()
	rankExpr := "0"
	if q.Sort != "newest" && !restricted {
		rankExpr = "bm25(sess_fts, 2.0, 0.5, 1.0, 0.5)"
	}
	rows, err := s.db.Query(`WITH h AS MATERIALIZED (SELECT rowid AS rid, `+rankExpr+` AS r FROM sess_fts WHERE sess_fts MATCH ?)
		SELECT ci.session_id, MIN(h.r), COUNT(*)
		FROM h JOIN chunk_index ci ON ci.chunk_id = h.rid
		WHERE ci.session_id IN (SELECT value FROM json_each(?))
		GROUP BY ci.session_id`, expr, idsJSON(readable))
	if err != nil {
		return SearchPage{}, err
	}
	var matched []matchedSession
	for rows.Next() {
		var id int64
		var m matchedSession
		if err := rows.Scan(&id, &m.rank, &m.count); err != nil {
			rows.Close()
			return SearchPage{}, err
		}
		m.sess = byID[id]
		matched = append(matched, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return SearchPage{}, err
	}
	cands := make([]Session, len(matched))
	for i, m := range matched {
		cands[i] = m.sess
	}
	page := SearchPage{Facets: pa.computeFacets(cands, q.Filter)}
	if pa.isGuestOnly() {
		page.Facets = pa.guestFacets(page.Facets)
	}
	var kept []matchedSession
	for _, m := range matched {
		if pa.matchFilter(m.sess, q.Filter, dimAll) {
			kept = append(kept, m)
			page.Matches += m.count
		}
	}
	page.Sessions = len(kept)
	newest := q.Sort == "newest"
	sort.Slice(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if newest {
			if a.sess.LastSeenAt != b.sess.LastSeenAt {
				return a.sess.LastSeenAt > b.sess.LastSeenAt
			}
			return a.sess.ID > b.sess.ID
		}
		if restricted {
			if a.count != b.count {
				return a.count > b.count
			}
			if a.sess.LastSeenAt != b.sess.LastSeenAt {
				return a.sess.LastSeenAt > b.sess.LastSeenAt
			}
		} else if a.rank != b.rank {
			return a.rank < b.rank
		}
		return a.sess.ID > b.sess.ID
	})
	start := 0
	if q.Cursor != "" {
		start = positionAfter(kept, q.Cursor, newest)
	}
	end := min(start+q.Limit, len(kept))
	pageRows := kept[start:end]
	if end < len(kept) && len(pageRows) > 0 {
		if newest {
			page.Next = encodeTimeCursor(kept[end-1].sess.LastSeenAt, countSameStamp(end, func(i int) string { return kept[i].sess.LastSeenAt }))
		} else {
			page.Next = encodeOffsetCursor(end)
		}
	}
	groups, err := s.hitsFor(pa, pageRows, expr, q.Kind)
	if err != nil {
		return SearchPage{}, err
	}
	page.Groups = groups
	return page, nil
}

// encodeOffsetCursor: Stelle in der Liste, die der Betrachter selbst sieht.
func encodeOffsetCursor(offset int) string { return "o|" + strconv.Itoa(offset) }

// positionAfter findet den ersten Eintrag hinter dem Cursor. Bei einem
// unlesbaren Cursor beginnt die Liste von vorn. Die Reihenfolge nach Rang
// hängt über die Dokumenthäufigkeit des Index von allen Texten ab; ein Rang
// selbst verlässt den Server nie.
func positionAfter(kept []matchedSession, cursor string, newest bool) int {
	if newest {
		return timePosition(len(kept), cursor, func(i int) string { return kept[i].sess.LastSeenAt })
	}
	parts := strings.Split(cursor, "|")
	if len(parts) != 2 || parts[0] != "o" {
		return 0
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return 0
	}
	return min(n, len(kept))
}

// hitsFor holt je Session die drei besten Treffer mit Ausschnitt.
func (s *Store) hitsFor(pa *ProjectAccess, page []matchedSession, expr, kind string) ([]SearchGroup, error) {
	if len(page) == 0 {
		return nil, nil
	}
	sessions := make([]Session, len(page))
	for i, m := range page {
		sessions[i] = m.sess
	}
	// Die drei ersten Treffer je Session, in Reihenfolge des Verlaufs.
	rows, err := s.db.Query(`SELECT ci.session_id, ci.chunk_id, ci.seq, ci.role, 0
		FROM sess_fts JOIN chunk_index ci ON ci.chunk_id = sess_fts.rowid
		WHERE sess_fts MATCH ? AND ci.session_id IN (SELECT value FROM json_each(?))
		ORDER BY ci.session_id, ci.seq`, expr, idsJSON(sessions))
	if err != nil {
		return nil, err
	}
	type pick struct {
		chunk int64
		seq   int
		role  string
	}
	picks := map[int64][]pick{}
	var chunkIDs []int64
	for rows.Next() {
		var sid int64
		var p pick
		var rank float64
		if err := rows.Scan(&sid, &p.chunk, &p.seq, &p.role, &rank); err != nil {
			rows.Close()
			return nil, err
		}
		if len(picks[sid]) < 3 {
			picks[sid] = append(picks[sid], p)
			chunkIDs = append(chunkIDs, p.chunk)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	chunkJSON, _ := json.Marshal(chunkIDs)
	snips := map[int64][4]string{}
	srows, err := s.db.Query(`SELECT rowid,
		snippet(sess_fts, 0, char(2), char(3), '…', 18), snippet(sess_fts, 1, char(2), char(3), '…', 18),
		snippet(sess_fts, 2, char(2), char(3), '…', 18), snippet(sess_fts, 3, char(2), char(3), '…', 18)
		FROM sess_fts WHERE sess_fts MATCH ? AND rowid IN (SELECT value FROM json_each(?))`, expr, string(chunkJSON))
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var id int64
		var c [4]string
		if err := srows.Scan(&id, &c[0], &c[1], &c[2], &c[3]); err != nil {
			srows.Close()
			return nil, err
		}
		snips[id] = c
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}
	allowed := kindColumns(kind)
	out := make([]SearchGroup, 0, len(page))
	for _, m := range page {
		g := SearchGroup{Session: pa.guestView(m.sess), Total: m.count}
		for _, p := range picks[m.sess.ID] {
			cols := snips[p.chunk]
			for i, name := range ftsColumns {
				if !slices.Contains(allowed, name) || !strings.Contains(cols[i], "\x02") {
					continue
				}
				g.Hits = append(g.Hits, SearchHit{Seq: p.seq, Kind: hitKind(name, p.role), Parts: snippetParts(cols[i])})
				break
			}
		}
		out = append(out, g)
	}
	return out, nil
}

func hitKind(column, role string) string {
	switch column {
	case "thinking":
		return "thinking"
	case "tool_input":
		return "command"
	case "tool_output":
		return "output"
	}
	if role == "user" {
		return "user"
	}
	return "assistant"
}

func snippetParts(s string) []SnippetPart {
	var parts []SnippetPart
	for len(s) > 0 {
		i := strings.IndexByte(s, 2)
		if i < 0 {
			parts = append(parts, SnippetPart{Text: cleanSnippet(s)})
			break
		}
		if i > 0 {
			parts = append(parts, SnippetPart{Text: cleanSnippet(s[:i])})
		}
		s = s[i+1:]
		j := strings.IndexByte(s, 3)
		if j < 0 {
			parts = append(parts, SnippetPart{Text: cleanSnippet(s), Hit: true})
			break
		}
		parts = append(parts, SnippetPart{Text: cleanSnippet(s[:j]), Hit: true})
		s = s[j+1:]
	}
	return parts
}

// -------------------------------------------------- Sitzung: Fenster & Co.

// Window ist ein Ausschnitt des Transkripts.
type Window struct {
	Chunks         []Chunk
	Earlier, Later bool
	Total          int
	// EarlierFrom und LaterFrom sind die Startsequenzen der Nachbarfenster
	// (nur gültig, wenn Earlier bzw. Later gesetzt ist).
	EarlierFrom, LaterFrom int
}

// SessionWindow liest bis zu limit Chunks ab from, mit from<0 die letzten
// limit Chunks, oder mit around>0 ein Fenster um diese Sequenz (die Hälfte
// davor).
func (s *Store) SessionWindow(id int64, from, around, limit int) (Window, error) {
	if s.reader != nil {
		return s.reader.SessionWindow(id, from, around, limit)
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if from < 0 && around <= 0 {
		var first sql.NullInt64
		if err := s.db.QueryRow(`SELECT MIN(seq) FROM (SELECT seq FROM session_chunks WHERE session_id=? ORDER BY seq DESC LIMIT ?)`, id, limit).Scan(&first); err != nil {
			return Window{}, err
		}
		from = int(first.Int64)
	}
	if around > 0 {
		var first sql.NullInt64
		if err := s.db.QueryRow(`SELECT MIN(seq) FROM (SELECT seq FROM session_chunks WHERE session_id=? AND seq < ? ORDER BY seq DESC LIMIT ?)`,
			id, around, limit/2).Scan(&first); err != nil {
			return Window{}, err
		}
		from = around
		if first.Valid {
			from = int(first.Int64)
		}
	}
	rows, err := s.db.Query(`SELECT seq, role, text, raw FROM session_chunks WHERE session_id=? AND seq >= ? ORDER BY seq LIMIT ?`, id, max(from, 0), limit)
	if err != nil {
		return Window{}, err
	}
	var w Window
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.Seq, &c.Role, &c.Text, &c.Raw); err != nil {
			rows.Close()
			return Window{}, err
		}
		w.Chunks = append(w.Chunks, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Window{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_chunks WHERE session_id=?`, id).Scan(&w.Total); err != nil {
		return Window{}, err
	}
	if len(w.Chunks) > 0 {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_chunks WHERE session_id=? AND seq < ?`, id, w.Chunks[0].Seq).Scan(&n); err != nil {
			return Window{}, err
		}
		w.Earlier = n > 0
		if w.Earlier {
			var start sql.NullInt64
			if err := s.db.QueryRow(`SELECT MIN(seq) FROM (SELECT seq FROM session_chunks WHERE session_id=? AND seq < ? ORDER BY seq DESC LIMIT ?)`,
				id, w.Chunks[0].Seq, limit).Scan(&start); err != nil {
				return Window{}, err
			}
			w.EarlierFrom = int(start.Int64)
		}
		w.LaterFrom = w.Chunks[len(w.Chunks)-1].Seq + 1
		w.Later = w.Chunks[len(w.Chunks)-1].Seq < s.maxSeq(id)
	}
	return w, nil
}

func (s *Store) maxSeq(id int64) int {
	var m sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(seq) FROM session_chunks WHERE session_id=?`, id).Scan(&m)
	return int(m.Int64)
}

// SessionHits listet die Sequenzen aller Treffer in einer Session (ganze
// Session, nicht nur das geladene Fenster), aufsteigend.
func (s *Store) SessionHits(pa *ProjectAccess, sess Session, q, kind string) ([]int, error) {
	if s.reader != nil {
		return s.reader.SessionHits(pa, sess, q, kind)
	}
	if !pa.CanSeeTranscript(sess) {
		return nil, ErrAccessNotFound
	}
	expr := matchExpr(q, kind)
	if expr == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT ci.seq FROM sess_fts JOIN chunk_index ci ON ci.chunk_id = sess_fts.rowid
		WHERE sess_fts MATCH ? AND ci.session_id = ? ORDER BY ci.seq`, expr, sess.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var seq int
		if err := rows.Scan(&seq); err != nil {
			return nil, err
		}
		out = append(out, seq)
	}
	return out, rows.Err()
}

// SessionOutline liefert die Nutzer-Prompts der Session mit der Zahl der
// Werkzeugaufrufe bis zum nächsten Prompt.
func (s *Store) SessionOutline(id int64) ([]transcript.Prompt, error) {
	if s.reader != nil {
		return s.reader.SessionOutline(id)
	}
	rows, err := s.db.Query(`SELECT seq, is_prompt, tool_calls, ts, CASE WHEN is_prompt=1 THEN text ELSE '' END
		FROM chunk_index WHERE session_id=? AND (is_prompt=1 OR tool_calls>0) ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rs []transcript.OutlineRow
	for rows.Next() {
		var r transcript.OutlineRow
		var prompt int
		if err := rows.Scan(&r.Seq, &prompt, &r.ToolCalls, &r.Time, &r.Text); err != nil {
			return nil, err
		}
		r.Prompt = prompt == 1
		rs = append(rs, r)
	}
	return transcript.Outline(rs), rows.Err()
}

// ------------------------------------------------------------- Verknüpfungen

// SessionLink verweist von einer Session auf einen Request oder Wissenseintrag.
type SessionLink struct {
	Kind  string // "request" | "knowledge"
	ID    int64
	Label string
	Seq   int
}

// SessionLinks nennt Requests und Wissen, die in den Sessions gesichtet wurden
// oder aus ihnen entstanden sind, nur soweit der Betrachter das Ziel sehen darf.
func (s *Store) SessionLinks(pa *ProjectAccess, sessionIDs []int64) (map[int64][]SessionLink, error) {
	if s.reader != nil {
		return s.reader.SessionLinks(pa, sessionIDs)
	}
	out := map[int64][]SessionLink{}
	if len(sessionIDs) == 0 {
		return out, nil
	}
	ids, _ := json.Marshal(sessionIDs)
	// Erst lesen, dann prüfen: die Rollenprüfung fragt selbst die Datenbank.
	type reqLink struct {
		sid     int64
		link    SessionLink
		project string
	}
	var reqs []reqLink
	rows, err := s.db.Query(`SELECT sg.session_id, r.id, r.title, r.project, MIN(sg.chunk_seq)
		FROM request_sightings sg JOIN requests r ON r.id = sg.request_id
		WHERE sg.session_id IN (SELECT value FROM json_each(?)) GROUP BY sg.session_id, r.id ORDER BY r.id`, string(ids))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l reqLink
		l.link.Kind = "request"
		if err := rows.Scan(&l.sid, &l.link.ID, &l.link.Label, &l.project, &l.link.Seq); err != nil {
			rows.Close()
			return nil, err
		}
		reqs = append(reqs, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type knowLink struct {
		sid  int64
		link SessionLink
		k    Knowledge
	}
	var knows []knowLink
	krows, err := s.db.Query(`SELECT ev.session_id, k.id, k.title, k.project, k.machine, k.confidence, k.person, MIN(ev.chunk_seq)
		FROM knowledge_evidence ev JOIN knowledge k ON k.id = ev.knowledge_id
		WHERE ev.session_id IN (SELECT value FROM json_each(?)) GROUP BY ev.session_id, k.id ORDER BY k.id`, string(ids))
	if err != nil {
		return nil, err
	}
	for krows.Next() {
		var l knowLink
		l.link.Kind = "knowledge"
		if err := krows.Scan(&l.sid, &l.link.ID, &l.link.Label, &l.k.Scope.Project, &l.k.Scope.Machine, &l.k.Confidence, &l.k.Person, &l.link.Seq); err != nil {
			krows.Close()
			return nil, err
		}
		knows = append(knows, l)
	}
	krows.Close()
	if err := krows.Err(); err != nil {
		return nil, err
	}
	for _, l := range reqs {
		if pa.CanSeeProject(l.project, ResRequest) {
			out[l.sid] = append(out[l.sid], l.link)
		}
	}
	for _, l := range knows {
		if pa.CanSeeKnowledge(l.k) {
			out[l.sid] = append(out[l.sid], l.link)
		}
	}
	return out, nil
}

// CanShareSession: der Betrachter darf die Freigabestufe der Session ändern
// (Besitzer der Session oder Owner des Projekts). Gilt auch im Log-Modus.
func (a *ProjectAccess) CanShareSession(s Session) bool {
	return a.Decide(s.Scope.Project, ResTranscript, ActShare, a.transcriptObject(s)).Allowed
}

// MetaView ist die Session für Metadatenlisten (API, Suche): Titel, Zähler und
// Adresse nur, wenn der Betrachter das Transkript lesen darf; Gäste ohne
// Maschine, Branch, Pfad und Besitzer.
func (a *ProjectAccess) MetaView(s Session) Session { return a.row(s).Session }

// MatchesAxes prüft Maschine und Branch gegen das, was der Betrachter von der
// Session sieht, nie gegen den Rohwert.
func (a *ProjectAccess) MatchesAxes(viewed Session, f scope.Axes) bool {
	return (f.Machine == "" || viewed.Scope.Machine == f.Machine) && (f.Branch == "" || viewed.Scope.Branch == f.Branch)
}

// SessionView bereitet die Metadaten einer Session für diesen Betrachter auf:
// ein Gast sieht weder Maschine, Branch, Pfad noch Besitzer.
func (a *ProjectAccess) SessionView(s Session) Session { return a.guestView(s) }

// SessionPrefilter ist die Menge lesbarer Transkripte als SQL-Bedingung auf
// sessions. Where leer: keine Einschränkung. Exact: die Bedingung deckt genau
// die Matrix ab, die Zeilenprüfung ist danach überflüssig.
type SessionPrefilter struct {
	Where string
	Args  []any
	Exact bool
	// Unrestricted: der Betrachter liest ohnehin alles (Admin, oder ohne
	// Durchsetzung); die Suche darf dann nach bm25 ordnen.
	Unrestricted bool
}

func (p SessionPrefilter) apply(where string, args []any) (string, []any) {
	if p.Where == "" {
		return where, args
	}
	return where + ` AND (` + p.Where + `)`, append(args, p.Args...)
}

// TranscriptPrefilter spiegelt die Lese-Regel für Transkripte (matrixAllows,
// decideGlobal, unbeanspruchte Projekte) als SQL: Besitzer, Gast-Stufe ab Gast,
// Freigabe ab member, alles ab lead, der Instanz-Admin zusätzlich in
// unbeanspruchten Projekten. Die Abfrage hängt damit nicht davon ab, wie viele
// verborgene Sessions es gibt (#2447). Ohne Durchsetzung gilt alles.
func (a *ProjectAccess) TranscriptPrefilter() SessionPrefilter {
	if !a.st.AccessEnforced() {
		return SessionPrefilter{Exact: true, Unrestricted: true}
	}
	if !a.valid {
		return SessionPrefilter{Where: `0`, Exact: true}
	}
	a.loadRoles()
	a.loadMachines()
	var guests, members, leads []string
	for remote, r := range a.roles {
		switch rank := RoleRank(r.Role); {
		case rank >= 3:
			leads = append(leads, remote)
		case rank == 2:
			members = append(members, remote)
		case rank == 1:
			guests = append(guests, remote)
		}
	}
	list := func(in []string) string {
		b, _ := json.Marshal(in)
		return string(b)
	}
	owner := a.owner
	q := `(CASE WHEN account_id = 0 THEN ? ELSE account_id END) = ?
		OR (visibility = 'guests' AND project IN (SELECT value FROM json_each(?)))
		OR ((shared = 1 OR COALESCE(visibility,'') NOT IN ('','private')) AND project IN (SELECT value FROM json_each(?)))
		OR project IN (SELECT value FROM json_each(?))`
	args := []any{owner, a.account, list(guests), list(members), list(leads)}
	if a.admin {
		q += ` OR (project != '' AND project NOT IN (SELECT remote FROM projects))`
	}
	return SessionPrefilter{Where: q, Args: args, Exact: true, Unrestricted: a.admin}
}
