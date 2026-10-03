package store

import (
	"database/sql"
	"fmt"
)

// ObjectRef sind die zwei Felder, die die Zugriffsprüfung von einem
// Auftragsobjekt braucht: sein Projekt und sein Autor.
type ObjectRef struct {
	Project string
	Person  string
}

// RequestRefKind benennt, welche Id RequestRef auf einen Auftrag zurückführt.
type RequestRefKind string

const (
	RefRequest   RequestRefKind = "request"
	RefCriterion RequestRefKind = "criterion"
	RefWork      RequestRefKind = "work"
	RefRelation  RequestRefKind = "relation"
)

// RequestRef löst eine Auftrags-, Kriterien-, Arbeits- oder Relations-Id auf
// Projekt und Autor des Auftrags auf. Eine leichte Abfrage, damit die Prüfung
// nicht den ganzen Auftrag lädt. sql.ErrNoRows, wenn es die Id nicht gibt.
func (s *Store) RequestRef(kind RequestRefKind, id int64) (ObjectRef, error) {
	if s.reader != nil {
		return s.reader.RequestRef(kind, id)
	}
	var q string
	switch kind {
	case RefRequest:
		q = `SELECT r.project, r.person FROM requests r WHERE r.id=?`
	case RefCriterion:
		q = `SELECT r.project, r.person FROM request_criteria c JOIN requests r ON r.id=c.request_id WHERE c.id=?`
	case RefWork:
		q = `SELECT r.project, r.person FROM request_work w JOIN requests r ON r.id=w.request_id WHERE w.id=?`
	case RefRelation:
		q = `SELECT r.project, r.person FROM request_relations x JOIN requests r ON r.id=x.request_id WHERE x.id=?`
	default:
		return ObjectRef{}, fmt.Errorf("unknown request ref kind %q", kind)
	}
	var ref ObjectRef
	err := s.db.QueryRow(q, id).Scan(&ref.Project, &ref.Person)
	if err == sql.ErrNoRows {
		return ObjectRef{}, sql.ErrNoRows
	}
	return ref, err
}

// RequestWorkSession nennt die Session einer Arbeit. sql.ErrNoRows, wenn es die
// Arbeit nicht gibt.
func (s *Store) RequestWorkSession(workID int64) (int64, error) {
	if s.reader != nil {
		return s.reader.RequestWorkSession(workID)
	}
	var sid int64
	err := s.db.QueryRow(`SELECT session_id FROM request_work WHERE id=?`, workID).Scan(&sid)
	return sid, err
}

// KnowledgeRef: Projekt-, Maschinen- und Autorangaben eines Eintrags ohne Text.
func (s *Store) KnowledgeRef(id int64) (Knowledge, error) {
	if s.reader != nil {
		return s.reader.KnowledgeRef(id)
	}
	var k Knowledge
	k.ID = id
	err := s.db.QueryRow(`SELECT project, branch, machine, confidence, person FROM knowledge WHERE id=?`, id).
		Scan(&k.Scope.Project, &k.Scope.Branch, &k.Scope.Machine, &k.Confidence, &k.Person)
	return k, err
}

// MigrationProject nennt das Projekt eines Migrationslaufs.
func (s *Store) MigrationProject(id int64) (string, error) {
	if s.reader != nil {
		return s.reader.MigrationProject(id)
	}
	var project string
	err := s.db.QueryRow(`SELECT project FROM migration_runs WHERE id=?`, id).Scan(&project)
	return project, err
}
