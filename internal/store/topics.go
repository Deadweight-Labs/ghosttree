package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Topic ist ein benannter Bereich eines Projekts: eine Liste von Pfad-Präfixen
// (REQ-600). topic:name in der Suche wirkt wie mehrere area:-Filter.
type Topic struct {
	ID          int64    `json:"id"`
	Project     string   `json:"project"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Prefixes    []string `json:"prefixes"`
	CreatedBy   string   `json:"created_by,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// TopicInput legt ein Thema an oder ersetzt das gleichnamige.
type TopicInput struct {
	Project     string
	Name        string
	Description string
	Prefixes    []string
	By          string
}

// Maße.
const (
	MaxTopicPrefixes = 20
	MaxTopicName     = 40
)

// CleanTopicName prüft einen Themennamen: eine Zeile, ohne Zeichen, die in der
// Suchsprache etwas bedeuten.
func CleanTopicName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", labelRule("topic_name_required", "a topic needs a name", "give the topic a name")
	}
	if err := oneLineText(s, MaxTopicName); err != nil {
		return "", labelRule("topic_name_invalid", "topic name: "+err.Error(), "use a short one-line name")
	}
	if strings.ContainsAny(s, `,":`) || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "@") {
		return "", labelRule("topic_name_invalid", "a topic name cannot contain a comma, a quotation mark or a colon, or start with - or @", "these characters have a meaning in the search language")
	}
	return s, nil
}

// CleanTopicPrefixes normalisiert die Präfixe; mindestens eines, höchstens
// MaxTopicPrefixes, ohne Doppelte.
func CleanTopicPrefixes(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if err := oneLineText(p, 300); err != nil {
			return nil, labelRule("topic_prefix_invalid", "topic prefix: "+err.Error(), "use a path relative to the repository root")
		}
		n, err := originctx.NormalizeArea(p)
		if err != nil {
			return nil, labelRule("topic_prefix_invalid", err.Error(), "use a path relative to the repository root, such as internal/store")
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, labelRule("topic_prefix_required", "a topic needs at least one path prefix", "name the directories the topic covers")
	}
	if len(out) > MaxTopicPrefixes {
		return nil, labelRule("topic_prefix_invalid", fmt.Sprintf("a topic has at most %d path prefixes", MaxTopicPrefixes), "name the parent directories instead")
	}
	return out, nil
}

// SaveTopic legt ein Thema an. Gibt es im Projekt schon eines gleichen Namens,
// ersetzt es dessen Beschreibung und Präfixe.
func (s *Store) SaveTopic(in TopicInput) (Topic, error) {
	name, err := CleanTopicName(in.Name)
	if err != nil {
		return Topic{}, err
	}
	prefixes, err := CleanTopicPrefixes(in.Prefixes)
	if err != nil {
		return Topic{}, err
	}
	desc, err := CleanLabelDescription(in.Description)
	if err != nil {
		return Topic{}, err
	}
	in.Name, in.Prefixes, in.Description = name, prefixes, desc
	in.Project = scope.NormalizeRemote(in.Project)
	if in.Project == "" {
		return Topic{}, labelRule("project_required", "a topic belongs to a project", "name the project")
	}
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (Topic, error) { return d.SaveTopic(p[0].(TopicInput)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Topic{}, err
	}
	defer tx.Rollback()
	ts := now()
	var id int64
	err = tx.QueryRow(`SELECT id FROM topics WHERE project=? AND lower(name)=lower(?)`, in.Project, in.Name).Scan(&id)
	switch err {
	case nil:
		if _, err := tx.Exec(`UPDATE topics SET name=?,description=?,updated_at=? WHERE id=?`, in.Name, in.Description, ts, id); err != nil {
			return Topic{}, err
		}
		if _, err := tx.Exec(`DELETE FROM topic_prefixes WHERE topic_id=?`, id); err != nil {
			return Topic{}, err
		}
	case sql.ErrNoRows:
		res, err := tx.Exec(`INSERT INTO topics(project,name,description,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
			in.Project, in.Name, in.Description, in.By, ts, ts)
		if err != nil {
			return Topic{}, err
		}
		id, _ = res.LastInsertId()
	default:
		return Topic{}, err
	}
	for _, p := range in.Prefixes {
		if _, err := tx.Exec(`INSERT INTO topic_prefixes(topic_id,prefix) VALUES(?,?)`, id, p); err != nil {
			return Topic{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Topic{}, err
	}
	return loadTopic(s.db, id)
}

func loadTopic(q queryer, id int64) (Topic, error) {
	var t Topic
	err := q.QueryRow(`SELECT id,project,name,description,created_by,created_at,updated_at FROM topics WHERE id=?`, id).
		Scan(&t.ID, &t.Project, &t.Name, &t.Description, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	rows, err := q.Query(`SELECT prefix FROM topic_prefixes WHERE topic_id=? ORDER BY prefix`, id)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	t.Prefixes = []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return t, err
		}
		t.Prefixes = append(t.Prefixes, p)
	}
	return t, rows.Err()
}

// DeleteTopic löscht ein Thema; false, wenn es keines dieses Namens gab.
func (s *Store) DeleteTopic(project, name string) (bool, error) {
	project = scope.NormalizeRemote(project)
	if s.writer != nil {
		return queueValue(s, []any{project, name}, func(d *Store, p []any) (bool, error) {
			return d.DeleteTopic(p[0].(string), p[1].(string))
		})
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM topics WHERE project=? AND lower(name)=lower(?)`, project, strings.TrimSpace(name)).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM topic_prefixes WHERE topic_id=?`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM topics WHERE id=?`, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// TopicsOf liefert die Themen eines Projekts nach Name.
func (s *Store) TopicsOf(project string) ([]Topic, error) {
	if s.reader != nil {
		return s.reader.TopicsOf(project)
	}
	rows, err := s.db.Query(`SELECT id FROM topics WHERE project=? ORDER BY lower(name)`, scope.NormalizeRemote(project))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Topic{}
	for _, id := range ids {
		t, err := loadTopic(s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
