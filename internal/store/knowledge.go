package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

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

// SetRequestState records whether a wish was fulfilled. Marking something done
// requires evidence: a commit, a test or a plan line. Without it the claim is
// a guess, and a guessed "done" hides work that was never finished.
func (s *Store) SetRequestState(id int64, state, kind, ref, person string) error {
	if state == "done" && ref == "" {
		return fmt.Errorf("state done requires evidence_ref")
	}
	k, err := s.KnowledgeByID(id)
	if err != nil {
		return err
	}
	if k.Type != "request" {
		return fmt.Errorf("knowledge %d is %q, not request", id, k.Type)
	}
	_, err = s.db.Exec(`INSERT INTO request_resolution(knowledge_id, state, evidence_kind, evidence_ref, by_person, at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(knowledge_id) DO UPDATE SET
		  state = excluded.state, evidence_kind = excluded.evidence_kind,
		  evidence_ref = excluded.evidence_ref, by_person = excluded.by_person, at = excluded.at`,
		id, state, kind, ref, person, now())
	return err
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
	if k.Type != "instruction" && (len(k.Activation.Paths) > 0 || len(k.Activation.Tasks) > 0) {
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
	for _, pattern := range k.Activation.Paths {
		if _, err := tx.Exec(`INSERT INTO instruction_activation_path(knowledge_id,pattern) VALUES(?,?)`, id, pattern); err != nil {
			return 0, err
		}
	}
	for _, task := range k.Activation.Tasks {
		if _, err := tx.Exec(`INSERT INTO instruction_activation_task(knowledge_id,task) VALUES(?,?)`, id, task); err != nil {
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
	for _, table := range []string{"instruction_activation_path", "instruction_activation_task"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE knowledge_id = ?`, id); err != nil {
			return err
		}
	}
	for _, pattern := range rule.Paths {
		if _, err := tx.Exec(`INSERT INTO instruction_activation_path(knowledge_id, pattern) VALUES(?,?)`, id, pattern); err != nil {
			return err
		}
	}
	for _, task := range rule.Tasks {
		if _, err := tx.Exec(`INSERT INTO instruction_activation_task(knowledge_id, task) VALUES(?,?)`, id, task); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var patchable = map[string]bool{"title": true, "body": true, "confidence": true,
	"status": true, "type": true, "origin": true, "superseded_by": true}

// PendingKnowledge returns everything awaiting a human decision.
func (s *Store) PendingKnowledge(limit int) ([]Knowledge, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge
		WHERE status = 'active' AND confidence IN ('quarantined','staged')
		ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
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
	for _, col := range []string{"type", "title", "body", "confidence", "status", "origin", "superseded_by"} {
		if v, ok := patch[col]; ok {
			sets = append(sets, col+" = ?")
			args = append(args, v)
		}
	}
	if len(sets) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if typ, ok := patch["type"]; ok && typ != "instruction" {
		var gates int
		if err := tx.QueryRow(`SELECT
			(SELECT COUNT(*) FROM instruction_activation_path WHERE knowledge_id=?) +
			(SELECT COUNT(*) FROM instruction_activation_task WHERE knowledge_id=?)`, id, id).Scan(&gates); err != nil {
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
	return tx.Commit()
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
	ctx, err := activation.NormalizeContext(ctx)
	if err != nil {
		return nil, err
	}
	where, args := ax.UnionWhere()
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge
		WHERE status = 'active' AND confidence != 'quarantined' AND `+where+`
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
		  AND k.status != 'deprecated' AND k.confidence != 'quarantined'
		ORDER BY f.rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
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
		taskRows, err := s.db.Query(`SELECT task FROM instruction_activation_task WHERE knowledge_id = ? ORDER BY task`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for taskRows.Next() {
			var task string
			if err := taskRows.Scan(&task); err != nil {
				taskRows.Close()
				return nil, err
			}
			out[i].Activation.Tasks = append(out[i].Activation.Tasks, task)
		}
		if err := taskRows.Close(); err != nil {
			return nil, err
		}
		sort.Strings(out[i].Activation.Paths)
		sort.Strings(out[i].Activation.Tasks)
	}
	return out, nil
}
