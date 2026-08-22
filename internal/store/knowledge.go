package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type Knowledge struct {
	ID         int64      `json:"id"`
	Type       string     `json:"type"` // pitfall|decision|note|plan
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Scope      scope.Axes `json:"scope"`
	Confidence string     `json:"confidence"` // observation|verified
	Status     string     `json:"status"`     // active|stale|deprecated
	Person     string     `json:"person"`
	Harness    string     `json:"harness,omitempty"`
	SessionRef string     `json:"session_ref,omitempty"`
	CreatedAt  string     `json:"created_at"`
	UpdatedAt  string     `json:"updated_at"`
}

const knowledgeCols = `id, type, title, body, project, branch, machine,
	confidence, status, person, harness, session_ref, created_at, updated_at`

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
	if k.Confidence == "" {
		k.Confidence = "observation"
	}
	if k.Status == "" {
		k.Status = "active"
	}
	ts := now()
	res, err := s.db.Exec(`INSERT INTO knowledge(type, title, body, project, branch, machine,
		confidence, status, person, harness, session_ref, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		k.Type, k.Title, k.Body, k.Scope.Project, k.Scope.Branch, k.Scope.Machine,
		k.Confidence, k.Status, k.Person, k.Harness, k.SessionRef, ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

var patchable = map[string]bool{"title": true, "body": true, "confidence": true, "status": true, "type": true}

func (s *Store) UpdateKnowledge(id int64, patch map[string]string) error {
	for col := range patch {
		if !patchable[col] {
			return fmt.Errorf("field %q is not patchable", col)
		}
	}
	var sets []string
	var args []any
	for _, col := range []string{"type", "title", "body", "confidence", "status"} {
		if v, ok := patch[col]; ok {
			sets = append(sets, col+" = ?")
			args = append(args, v)
		}
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now(), id)
	_, err := s.db.Exec(`UPDATE knowledge SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

func (s *Store) KnowledgeByID(id int64) (Knowledge, error) {
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE id = ?`, id)
	if err != nil {
		return Knowledge{}, err
	}
	ks, err := scanKnowledge(rows)
	if err != nil {
		return Knowledge{}, err
	}
	if len(ks) == 0 {
		return Knowledge{}, sql.ErrNoRows
	}
	return ks[0], nil
}

func (s *Store) KnowledgeForContext(ax scope.Axes) ([]Knowledge, error) {
	where, args := ax.UnionWhere()
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge
		WHERE status = 'active' AND `+where+`
		ORDER BY (confidence = 'verified') DESC, created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	return scanKnowledge(rows)
}

// SearchKnowledge matches only the axes the caller set.
func (s *Store) SearchKnowledge(q string, filter scope.Axes, limit int) ([]Knowledge, error) {
	where, args := filter.FilterWhere()
	return s.searchKnowledge(q, where, args, limit)
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
		WHERE knowledge_fts MATCH ? AND `+where+` AND k.status != 'deprecated'
		ORDER BY f.rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanKnowledge(rows)
}

// prefix qualifies a column list with a table alias.
func prefix(cols, p string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func scanKnowledge(rows *sql.Rows) ([]Knowledge, error) {
	defer rows.Close()
	out := []Knowledge{}
	for rows.Next() {
		var k Knowledge
		if err := rows.Scan(&k.ID, &k.Type, &k.Title, &k.Body,
			&k.Scope.Project, &k.Scope.Branch, &k.Scope.Machine,
			&k.Confidence, &k.Status, &k.Person, &k.Harness, &k.SessionRef,
			&k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
