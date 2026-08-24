package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type Knowledge struct {
	ID           int64           `json:"id"`
	Type         string          `json:"type"` // pitfall|decision|note|plan
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	Scope        scope.Axes      `json:"scope"`
	Activation   activation.Rule `json:"activation,omitempty"`
	Confidence   string          `json:"confidence"`              // quarantined|staged|trusted|verified
	Status       string          `json:"status"`                  // active|stale|deprecated|superseded
	Origin       string          `json:"origin"`                  // agent|distilled|human
	SupersededBy int64           `json:"superseded_by,omitempty"` // 0 = not superseded
	Person       string          `json:"person"`
	Harness      string          `json:"harness,omitempty"`
	SessionRef   string          `json:"session_ref,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

const knowledgeCols = `id, type, title, body, project, branch, machine,
	confidence, status, origin, superseded_by, person, harness, session_ref, created_at, updated_at`

// ftsQuery turns user input into a safe FTS5 expression: each token becomes a
// quoted phrase joined with AND. Quoting is what keeps FTS5 operator syntax
// (NEAR, ^, *, column filters) out of user input.
func ftsQuery(q string) string {
	var terms []string
	for _, f := range strings.Fields(q) {
		if f = strings.ReplaceAll(f, `"`, ""); f != "" {
			terms = append(terms, `"`+f+`"`)
		}
	}
	if len(terms) == 0 {
		return `""`
	}
	return strings.Join(terms, " AND ")
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
	res, err := tx.Exec(`INSERT INTO knowledge(type, title, body, project, branch, machine,
		confidence, status, origin, superseded_by, person, harness, session_ref, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		k.Type, k.Title, k.Body, k.Scope.Project, k.Scope.Branch, k.Scope.Machine,
		k.Confidence, k.Status, k.Origin, k.SupersededBy,
		k.Person, k.Harness, k.SessionRef, ts, ts)
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

var patchable = map[string]bool{"title": true, "body": true, "confidence": true,
	"status": true, "type": true, "origin": true, "superseded_by": true}

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
	for col := range patch {
		if !patchable[col] {
			return fmt.Errorf("field %q is not patchable", col)
		}
	}
	var sets []string
	var args []any
	for _, col := range []string{"type", "title", "body", "confidence", "status", "origin"} {
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
		ts := now()
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
	args = append(args, now(), id)
	if _, err := tx.Exec(`UPDATE knowledge SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE search_documents SET title=k.title,body=k.body,project=k.project,branch=k.branch,machine=k.machine
		FROM knowledge k WHERE search_documents.kind='knowledge' AND search_documents.domain_id=k.id AND k.id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyStaleness marks time-sensitive plans stale after they have gone
// untouched for maxAge. Durable decisions, pitfalls, and instructions never
// expire merely because time passed.
func (s *Store) ApplyStaleness(at time.Time, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, fmt.Errorf("staleness max age must be positive")
	}
	cutoff := at.UTC().Add(-maxAge).Format(time.RFC3339Nano)
	res, err := s.db.Exec(`UPDATE knowledge SET status='stale',updated_at=? WHERE type='plan' AND status='active' AND updated_at<?`, at.UTC().Format(time.RFC3339Nano), cutoff)
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
		ORDER BY `+trustOrder+`, created_at DESC, id DESC`, args...)
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
	s.recordKnowledgeUse(ks)
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
			&k.Person, &k.Harness, &k.SessionRef,
			&k.CreatedAt, &k.UpdatedAt); err != nil {
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
