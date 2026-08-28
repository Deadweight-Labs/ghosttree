package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type Knowledge struct {
	ID             int64           `json:"id"`
	Type           string          `json:"type"` // pitfall|decision|note|plan
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	Scope          scope.Axes      `json:"scope"`
	Activation     activation.Rule `json:"activation,omitempty"`
	Confidence     string          `json:"confidence"`              // quarantined|staged|trusted|verified
	Status         string          `json:"status"`                  // active|stale|deprecated|superseded
	Origin         string          `json:"origin"`                  // agent|distilled|human
	SupersededBy   int64           `json:"superseded_by,omitempty"` // 0 = not superseded
	Person         string          `json:"person"`
	ConfirmedBy    string          `json:"confirmed_by,omitempty"`
	LastModifiedBy string          `json:"last_modified_by,omitempty"`
	Harness        string          `json:"harness,omitempty"`
	SessionRef     string          `json:"session_ref,omitempty"`
	// ObservedAt is when the entry was seen; CreatedAt is when it was written
	// down. For anything a person or an agent typed the two coincide. For a
	// distilled entry they do not, and the difference is what tells a reader
	// whether "as of today" in the body means today.
	ObservedAt string `json:"observed_at,omitempty"`
	// RegressionState sagt, womit ein behobener Fehler abgesichert ist. Ein
	// Pitfall hilft, solange ein Agent ihn liest; ein Test hilft immer. Leer
	// heisst, niemand hat es beurteilt — was etwas anderes ist als
	// "not_applicable", die ausgesprochene Entscheidung, dass hier nichts zu
	// testen war. Siehe RegressionStates.
	RegressionState string `json:"regression_state,omitempty"`
	RegressionTest  string `json:"regression_test,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// KnowledgeProvenance is the compact attribution shared by every read view.
// An empty person changes nothing, while a verified entry without its confirmer
// is deliberately not given a guessed one.
func KnowledgeProvenance(k Knowledge) string {
	var parts []string
	if k.Person != "" {
		parts = append(parts, "by "+k.Person)
	}
	// Wer zuletzt geändert hat, gehört an dieselbe Stelle wie der Urheber —
	// sonst behält ein Eintrag den fremden Namen und liest sich als dessen
	// Aussage, obwohl jemand anderes ihn umgeschrieben hat. Genau der Befund,
	// aus dem REQ-181 entstand; ihn nur in der Historie festzuhalten hiesse, ihn
	// dort zu verstecken, wo niemand nachsieht.
	if k.LastModifiedBy != "" && k.LastModifiedBy != k.Person {
		parts = append(parts, "last edited by "+k.LastModifiedBy)
	}
	if k.Confidence == "verified" && k.ConfirmedBy != "" {
		parts = append(parts, "confirmed by "+k.ConfirmedBy)
	}
	return strings.Join(parts, "; ")
}

const knowledgeCols = `id, type, title, body, project, branch, machine,
	confidence, status, origin, superseded_by, person, confirmed_by, last_modified_by, harness, session_ref,
	observed_at, regression_state, regression_test, created_at, updated_at`

// ftsQuery turns user input into a safe FTS5 expression: each token becomes a
// quoted phrase, and the phrases are joined with OR. Quoting is what keeps FTS5
// operator syntax (NEAR, ^, *, column filters) out of user input.
//
// They used to be joined with AND, which required every word of a query to
// appear in one document. Measured on 2026-08-24: "Ghost Tree Parallelbaum
// Bootstrap Auslöser Rückfallweg" found nothing because the terms live in two
// entries, while "Parallelbaum" alone found one immediately. An empty result is
// indistinguishable from "there is nothing", so the caller concludes the archive
// is empty rather than that the query was too narrow — and stops asking.
//
// OR needs the ranking to do the work instead, which bm25 already does: a
// document matching four of five terms outranks one matching one. What OR
// cannot survive is ordinary words, which match everything and drown the signal.
// Those are dropped first, and a query left with nothing but ordinary words is
// not a search at all — see isListingQuery.
func ftsQuery(q string) string {
	terms := searchTerms(q)
	if len(terms) == 0 {
		return `""`
	}
	for i, t := range terms {
		terms[i] = `"` + t + `"`
	}
	return strings.Join(terms, " OR ")
}

// searchTerms keeps the words of a query that carry intent.
func searchTerms(q string) []string {
	var terms []string
	for f := range strings.FieldsSeq(strings.ToLower(q)) {
		f = strings.Trim(strings.ReplaceAll(f, `"`, ""), ".,;:!?()[]{}")
		if f == "" || commonWords[f] || len([]rune(f)) < 2 {
			continue
		}
		terms = append(terms, f)
	}
	return terms
}

// isListingQuery reports that a query asks to see what there is rather than to
// find something specific. "was ist noch zu tun" contains no word about the
// subject matter, and answering it with whichever entry happens to contain all
// five words is worse than answering it with the list — which is what `gh issue
// list` does when given no argument.
func isListingQuery(q string) bool { return len(searchTerms(q)) == 0 }

type SearchIntent string

const (
	SearchFullText    SearchIntent = "full_text"
	SearchInventory   SearchIntent = "inventory"
	SearchInterrupted SearchIntent = "interrupted_work"
)

// ClassifySearch trennt eine Suche nach einem Gegenstand von Fragen nach dem
// Zustand des Arbeitsbestands. Diese Grenze wird absichtlich an einer festen
// Stichprobe echter Anfragen geprüft: FTS kann Ähnlichkeit liefern, aber nicht
// beantworten, ob eine Arbeit offen oder unterbrochen ist.
func ClassifySearch(q string) SearchIntent {
	words := queryWords(q)
	workContext := hasWord(words, "arbeit", "arbeiten", "gearbeitet", "work", "worked", "working", "faden", "auftrag", "task")
	explicitState := hasWord(words, "unterbrochen", "liegengeblieben", "unfertig", "unfinished", "interrupted")
	unfinishedPhrase := hasWord(words, "nicht", "ohne", "not", "never") &&
		hasWord(words, "fertig", "abgeschlossen", "ende", "finished", "done")
	leftOff := words["leave"] && words["off"]
	if (workContext && (explicitState || unfinishedPhrase)) || leftOff {
		return SearchInterrupted
	}
	terms := searchTerms(q)
	if len(terms) == 0 {
		return SearchInventory
	}
	for _, term := range terms {
		if !inventoryWords[term] {
			return SearchFullText
		}
	}
	return SearchInventory
}

func queryWords(q string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		words[word] = true
	}
	return words
}

func hasWord(words map[string]bool, candidates ...string) bool {
	for _, candidate := range candidates {
		if words[candidate] {
			return true
		}
	}
	return false
}

var inventoryWords = map[string]bool{
	"aufgaben": true, "aufträge": true, "bestand": true, "offen": true, "offene": true,
	"offenen": true, "übrig": true, "zeige": true, "zeig": true, "liste": true,
	"list": true, "open": true, "tasks": true, "work": true,
}

func (s *Store) InsertKnowledge(k Knowledge) (int64, error) {
	if err := activation.ValidateRule(k.Activation); err != nil {
		return 0, err
	}
	if k.Type != "instruction" && len(k.Activation.Paths) > 0 {
		return 0, fmt.Errorf("activation requires instruction, got %s", k.Type)
	}
	if k.Origin == "" {
		k.Origin = "agent"
	}
	if k.Confidence == "" {
		// A distilled claim starts untrusted until evidence and recurrence
		// raise it; anything an agent or a human wrote deliberately does not.
		if k.Origin == "distilled" {
			k.Confidence = "quarantined"
		} else {
			k.Confidence = "trusted"
		}
	}
	if k.Status == "" {
		k.Status = "active"
	}
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if k.ObservedAt == "" {
		// Nobody is reporting an older sighting here: the writer is looking at
		// the thing as they write it.
		k.ObservedAt = ts
	}
	res, err := tx.Exec(`INSERT INTO knowledge(type, title, body, project, branch, machine,
		confidence, status, origin, superseded_by, person, confirmed_by, last_modified_by, harness, session_ref, observed_at, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		k.Type, k.Title, k.Body, k.Scope.Project, k.Scope.Branch, k.Scope.Machine,
		k.Confidence, k.Status, k.Origin, k.SupersededBy,
		k.Person, k.ConfirmedBy, k.Person, k.Harness, k.SessionRef, k.ObservedAt, ts, ts)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO search_documents(kind,domain_id,title,body,project,branch,machine) VALUES('knowledge',?,?,?,?,?,?)`, id, k.Title, k.Body, k.Scope.Project, k.Scope.Branch, k.Scope.Machine); err != nil {
		return 0, err
	}
	for _, pattern := range k.Activation.Paths {
		if _, err := tx.Exec(`INSERT INTO instruction_activation_path(knowledge_id,pattern) VALUES(?,?)`, id, pattern); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// SetActivation replaces every activation gate for an instruction in one
// transaction. Empty rules make the instruction unconditional.
func (s *Store) SetActivation(id int64, rule activation.Rule) error {
	if err := activation.ValidateRule(rule); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var typ string
	if err := tx.QueryRow(`SELECT type FROM knowledge WHERE id = ?`, id).Scan(&typ); err != nil {
		return err
	}
	if typ != "instruction" {
		return fmt.Errorf("knowledge %d is %q, not instruction", id, typ)
	}
	if _, err := tx.Exec(`DELETE FROM instruction_activation_path WHERE knowledge_id = ?`, id); err != nil {
		return err
	}
	for _, pattern := range rule.Paths {
		if _, err := tx.Exec(`INSERT INTO instruction_activation_path(knowledge_id, pattern) VALUES(?,?)`, id, pattern); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// observed_at is patchable so that reconfirming an entry is something a reader
// can say. Editing the text is not the same statement — a typo fix is not a
// claim that the content is still true — so nothing infers it from an update.
var patchable = map[string]bool{"title": true, "body": true, "confidence": true,
	"status": true, "type": true, "origin": true, "superseded_by": true, "observed_at": true,
	"confirmed_by": true}

// PendingKnowledge lists what awaits a decision. project narrows the queue:
// a flat list is fine for eleven entries and unusable at the several hundred a
// full distiller run produces, and judging findings is easier one repository at
// a time than in a stream that jumps between them.
func (s *Store) PendingKnowledge(project string, limit int) ([]Knowledge, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge
		WHERE ((status = 'active' AND confidence IN ('quarantined','staged')) OR status = 'stale')
		  AND (? = '' OR project = ?)
		ORDER BY created_at DESC, id DESC LIMIT ?`, project, project, limit)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}

func (s *Store) UpdateKnowledge(id int64, patch map[string]string) error {
	return s.UpdateKnowledgeBy(id, patch, "")
}

// UpdateKnowledgeBy archives the current version before applying a correction.
// The author remains on the live entry, while the authenticated editor is
// recorded both on the new head and on the archived version it replaced.
func (s *Store) UpdateKnowledgeBy(id int64, patch map[string]string, editor string) error {
	for col := range patch {
		if !patchable[col] {
			return fmt.Errorf("field %q is not patchable", col)
		}
	}
	var sets []string
	var args []any
	for _, col := range []string{"type", "title", "body", "confidence", "status", "origin", "observed_at", "confirmed_by"} {
		if v, ok := patch[col]; ok {
			sets = append(sets, col+" = ?")
			args = append(args, v)
		}
	}
	if len(sets) == 0 && patch["superseded_by"] == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if err := archiveKnowledgeTx(tx, id, editor, ts); err != nil {
		return err
	}
	if raw, ok := patch["superseded_by"]; ok {
		target, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || target == 0 || target == id {
			return fmt.Errorf("invalid superseded_by %q", raw)
		}
		seen := map[int64]bool{id: true}
		for {
			if seen[target] {
				return fmt.Errorf("supersession would create a cycle")
			}
			seen[target] = true
			var next int64
			if err := tx.QueryRow(`SELECT superseded_by FROM knowledge WHERE id=?`, target).Scan(&next); err != nil {
				return err
			}
			if next == 0 {
				break
			}
			target = next
		}
		if _, err := tx.Exec(`WITH RECURSIVE ancestors(id) AS (
			SELECT ? UNION ALL SELECT k.id FROM knowledge k JOIN ancestors a ON k.superseded_by=a.id
		) UPDATE knowledge SET status='superseded',superseded_by=?,updated_at=? WHERE id IN (SELECT id FROM ancestors)`, id, target, ts); err != nil {
			return err
		}
	}
	if typ, ok := patch["type"]; ok && typ != "instruction" {
		var gates int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM instruction_activation_path WHERE knowledge_id=?`, id).Scan(&gates); err != nil {
			return err
		}
		if gates > 0 {
			return fmt.Errorf("cannot change gated instruction to %s", typ)
		}
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, ts)
	if editor != "" {
		sets = append(sets, "last_modified_by = ?")
		args = append(args, editor)
	}
	args = append(args, id)
	if _, err := tx.Exec(`UPDATE knowledge SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE search_documents SET title=k.title,body=k.body,project=k.project,branch=k.branch,machine=k.machine
		FROM knowledge k WHERE search_documents.kind='knowledge' AND search_documents.domain_id=k.id AND k.id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// KnowledgeVersion is a complete prior version of an entry. ChangedBy says
// who caused this version to be replaced; Person remains its original author.
type KnowledgeVersion struct {
	ID          int64  `json:"id"`
	KnowledgeID int64  `json:"knowledge_id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Person      string `json:"person"`
	ChangedBy   string `json:"changed_by"`
	ChangedAt   string `json:"changed_at"`
}

func archiveKnowledgeTx(tx *sql.Tx, id int64, editor, changedAt string) error {
	_, err := tx.Exec(`INSERT INTO knowledge_versions
		(knowledge_id,type,title,body,person,changed_by,changed_at)
		SELECT id,type,title,body,person,?,? FROM knowledge WHERE id=?`,
		editor, changedAt, id)
	return err
}

func (s *Store) KnowledgeHistory(id int64) ([]KnowledgeVersion, error) {
	rows, err := s.db.Query(`SELECT id,knowledge_id,type,title,body,person,changed_by,changed_at
		FROM knowledge_versions WHERE knowledge_id=? ORDER BY changed_at DESC, id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeVersion
	for rows.Next() {
		var v KnowledgeVersion
		if err := rows.Scan(&v.ID, &v.KnowledgeID, &v.Type, &v.Title, &v.Body,
			&v.Person, &v.ChangedBy, &v.ChangedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// observationTime is how old an entry's content is. The fallback matters on a
// database whose backfill has not run yet: an empty observed_at would sort and
// age as the beginning of time, so it defers to the column that was used
// before rather than declaring the whole archive ancient.
const observationTime = `COALESCE(NULLIF(observed_at,''), updated_at)`

// ApplyStaleness marks time-sensitive plans stale once what they describe is
// older than maxAge. Durable decisions, pitfalls, and instructions never expire
// merely because time passed.
//
// The clock is the observation, not the last write. updated_at was the rule
// until 2026-08-24 and the distiller pushed it to the run time on every entry
// it filed — so a plan from a June session looked like it had been touched
// today. Editing an entry no longer resets its age either; saying it is still
// current is a separate act, and PATCH observed_at is how it is said.
func (s *Store) ApplyStaleness(at time.Time, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, fmt.Errorf("staleness max age must be positive")
	}
	cutoff := at.UTC().Add(-maxAge).Format(time.RFC3339Nano)
	res, err := s.db.Exec(`UPDATE knowledge SET status='stale',updated_at=? WHERE type='plan' AND status='active' AND `+observationTime+`<?`, at.UTC().Format(time.RFC3339Nano), cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) KnowledgeByID(id int64) (Knowledge, error) {
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE id = ?`, id)
	if err != nil {
		return Knowledge{}, err
	}
	ks, err := s.scanKnowledge(rows)
	if err != nil {
		return Knowledge{}, err
	}
	if len(ks) == 0 {
		return Knowledge{}, sql.ErrNoRows
	}
	return ks[0], nil
}

// trustOrder ranks the confidence tiers for reading. Kept as one expression so
// every read path sorts identically.
const trustOrder = `CASE confidence WHEN 'verified' THEN 0 WHEN 'trusted' THEN 1 ELSE 2 END`

// corroboration counts the independent sessions that back an entry, with a
// floor of one for anything a person or an agent chose to write down. Counting
// raw would rank every hand-written entry below every distilled one, because
// only distillation leaves transcript evidence — and "nobody quoted a chunk for
// it" is not the same as "nobody vouched for it".
const corroboration = `MAX(
	(SELECT COUNT(DISTINCT session_id) FROM knowledge_evidence e WHERE e.knowledge_id = knowledge.id),
	CASE origin WHEN 'distilled' THEN 0 ELSE 1 END)`

// deliveryOrder is what the bootstrap sorts by once the scope has been matched.
// created_at was the whole rule until 2026-08-24, which meant a budget-limited
// bootstrap shipped whatever the last distiller run had produced: releasing 136
// distilled items displaced the one entry in the archive with a measured effect.
// Recency is a tiebreaker, not a ranking.
//
// The tiebreaker reads observed_at, not created_at: the distiller files a June
// session and yesterday's in one batch, so created_at DESC ranked them by
// processing order, which says nothing about the entries.
const deliveryOrder = trustOrder + `, ` + corroboration + ` DESC, search_hits DESC, ` + observationTime + ` DESC, id DESC`

func (s *Store) KnowledgeForContext(ax scope.Axes) ([]Knowledge, error) {
	return s.KnowledgeForActivatedContext(ax, activation.Context{})
}

func (s *Store) KnowledgeForActivatedContext(ax scope.Axes, ctx activation.Context) ([]Knowledge, error) {
	return s.knowledgeForActivatedContext(ax, ctx, false)
}

func (s *Store) KnowledgeForActivatedPreview(ax scope.Axes, ctx activation.Context) ([]Knowledge, error) {
	return s.knowledgeForActivatedContext(ax, ctx, true)
}

func (s *Store) knowledgeForActivatedContext(ax scope.Axes, ctx activation.Context, includeStaged bool) ([]Knowledge, error) {
	ctx, err := activation.NormalizeContext(ctx)
	if err != nil {
		return nil, err
	}
	where, args := ax.UnionWhere()
	confidenceWhere := `confidence IN ('trusted','verified')`
	if includeStaged {
		confidenceWhere = `confidence != 'quarantined'`
	}
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge
		WHERE status = 'active' AND `+confidenceWhere+` AND `+where+`
		ORDER BY `+deliveryOrder, args...)
	if err != nil {
		return nil, err
	}
	ks, err := s.scanKnowledge(rows)
	if err != nil {
		return nil, err
	}
	out := ks[:0]
	for _, k := range ks {
		if k.Type != "instruction" || activation.Matches(k.Activation, ctx) {
			out = append(out, k)
		}
	}
	// Only the agent-facing path records use. The preview exists so an operator
	// can inspect staged material, and inspection that marks its subject as
	// recently used destroys the signal it was opened to read.
	if !includeStaged {
		s.recordKnowledgeUse(out)
	}
	return out, nil
}

// KnowledgeForProject returns every entry for a project, including archived
// cold storage. It is used to verify migration provenance before cleanup.
func (s *Store) KnowledgeForProject(project string) ([]Knowledge, error) {
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE project = ? ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}

// SearchKnowledge matches only the axes the caller set.
func (s *Store) SearchKnowledge(q string, filter scope.Axes, limit int) ([]Knowledge, error) {
	where, args := filter.FilterWhere()
	return s.searchKnowledge(q, where, args, limit)
}

// SearchAllKnowledge is the operator view, including entries hidden from
// agents because they are quarantined, deprecated, or archived.
func (s *Store) SearchAllKnowledge(q string, filter scope.Axes, limit int) ([]Knowledge, error) {
	where, args := filter.FilterWhere()
	if limit <= 0 {
		limit = 50
	}
	args = append([]any{ftsQuery(q)}, args...)
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+prefix(knowledgeCols, "k.")+`
		FROM knowledge_fts f JOIN knowledge k ON k.id=f.rowid
		WHERE knowledge_fts MATCH ? AND `+where+` ORDER BY f.rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}

// SearchKnowledgeForContext searches the same union a session reads: global,
// machine, project and their combinations. Without it, a session on a branch
// could not find global or project-level knowledge.
func (s *Store) SearchKnowledgeForContext(q string, ax scope.Axes, limit int) ([]Knowledge, error) {
	where, args := ax.UnionWhere()
	return s.searchKnowledge(q, where, args, limit)
}

func (s *Store) searchKnowledge(q, where string, args []any, limit int) ([]Knowledge, error) {
	if limit <= 0 {
		limit = 20
	}
	args = append([]any{ftsQuery(q)}, args...)
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+prefix(knowledgeCols, "k.")+`
		FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
		WHERE knowledge_fts MATCH ? AND `+where+`
		  AND k.status = 'active' AND k.confidence != 'quarantined'
		ORDER BY f.rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	ks, err := s.scanKnowledge(rows)
	if err != nil {
		return nil, err
	}
	s.recordKnowledgeSearchHit(ks)
	return ks, nil
}

// prefix qualifies a column list with a table alias.
func prefix(cols, p string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func (s *Store) scanKnowledge(rows *sql.Rows) ([]Knowledge, error) {
	defer rows.Close()
	out := []Knowledge{}
	for rows.Next() {
		var k Knowledge
		if err := rows.Scan(&k.ID, &k.Type, &k.Title, &k.Body,
			&k.Scope.Project, &k.Scope.Branch, &k.Scope.Machine,
			&k.Confidence, &k.Status, &k.Origin, &k.SupersededBy,
			&k.Person, &k.ConfirmedBy, &k.LastModifiedBy, &k.Harness, &k.SessionRef,
			&k.ObservedAt, &k.RegressionState, &k.RegressionTest, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Type != "instruction" {
			continue
		}
		pathRows, err := s.db.Query(`SELECT pattern FROM instruction_activation_path WHERE knowledge_id = ? ORDER BY pattern`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for pathRows.Next() {
			var pattern string
			if err := pathRows.Scan(&pattern); err != nil {
				pathRows.Close()
				return nil, err
			}
			out[i].Activation.Paths = append(out[i].Activation.Paths, pattern)
		}
		if err := pathRows.Close(); err != nil {
			return nil, err
		}
		sort.Strings(out[i].Activation.Paths)
	}
	return out, nil
}

// KnowledgeTitlesForPrompt lists the titles a distillation prompt should treat
// as already covered.
//
// Two exclusions, both learnt the hard way. Archived items are gone from the
// tree and have no business telling the model a finding is taken. And items
// evidenced by a session in this very submission must be left out: the prompt
// asks the model not to restate what exists, while reprocessing archives the
// previous run's items for those same sessions — so including them means the
// model declines to re-derive a finding that is about to be retired. That
// On a real project, that combination retired findings without restoring their
// substance.
//
// Items from other sessions still suppress. That is what the list is for.
//
// Each line carries its id, because suppressing a repeat is only half of what
// the list is for. The other half is letting the model say which entry a
// finding belongs to, so the same defect under a second name becomes evidence
// rather than a second entry.
//
// The exclusion is by authorship, matching what reprocessing actually retires.
// Keyed on evidence it would also hide entries these sessions merely
// corroborated — entries that are not going anywhere and that the model should
// still be able to point at.
func (s *Store) KnowledgeTitlesForPrompt(project string, excludeSessions []int64) ([]string, error) {
	query := `SELECT '#' || k.id || ' ' || k.title FROM knowledge k
		WHERE k.project = ? AND k.status = 'active'`
	args := []any{project}
	for _, id := range excludeSessions {
		query += ` AND k.session_ref NOT LIKE ?`
		args = append(args, "session:"+strconv.FormatInt(id, 10)+"#%")
	}
	rows, err := s.db.Query(query+` ORDER BY k.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return nil, err
		}
		out = append(out, title)
	}
	return out, rows.Err()
}
