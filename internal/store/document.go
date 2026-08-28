package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrRevisionConflict sagt, dass der Kopf nicht mehr die Basis ist, auf der
// der Aufrufer gearbeitet hat. Es wird nicht gemergt: die Entscheidung, was
// mit den beiden Fassungen geschieht, gehört dem Menschen davor.
var ErrRevisionConflict = errors.New("revision conflict")

type Document struct {
	ID           int64  `json:"id"`
	Project      string `json:"project"`
	Slug         string `json:"slug"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	HeadRevision int    `json:"head_revision"`
	Status       string `json:"status"`
	Person       string `json:"person"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type DocumentRevision struct {
	ID         int64  `json:"id"`
	DocumentID int64  `json:"document_id"`
	Revision   int    `json:"revision"`
	Body       string `json:"body"`
	Digest     string `json:"digest"`
	Message    string `json:"message"`
	Person     string `json:"person"`
	CreatedAt  string `json:"created_at"`
}

// Digest ist der Beweis, dass ein Text unterwegs nicht verändert wurde. Er
// steht redundant in document_revisions, damit --clean und der Byte-Rundlauf
// ihn prüfen können, ohne den Body zu laden.
func Digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateDocument(d Document, body, message string) (Document, error) {
	ts := now()
	if d.Status == "" {
		d.Status = "active"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO documents(project,slug,kind,title,head_revision,status,person,created_at,updated_at)
		VALUES(?,?,?,?,1,?,?,?,?)`, d.Project, d.Slug, d.Kind, d.Title, d.Status, d.Person, ts, ts)
	if err != nil {
		return Document{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Document{}, err
	}
	if err := insertRevisionTx(tx, id, 1, body, message, d.Person, ts); err != nil {
		return Document{}, err
	}
	if err := tx.Commit(); err != nil {
		return Document{}, err
	}
	d.ID, d.HeadRevision, d.CreatedAt, d.UpdatedAt = id, 1, ts, ts
	return d, nil
}

// PushRevision hebt den Kopf und schreibt die Fassung in derselben
// Transaktion. Getrennt ginge beides schief: das Update gelingt, der Insert
// scheitert, und der Kopf zeigt fortan auf eine Revision, die niemand lesen
// kann.
func (s *Store) PushRevision(id int64, base int, body, message, person string) (Document, error) {
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE documents SET head_revision=?, updated_at=? WHERE id=? AND head_revision=?`,
		base+1, ts, id, base)
	if err != nil {
		return Document{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Document{}, err
	}
	if n == 0 {
		return Document{}, ErrRevisionConflict
	}
	if err := insertRevisionTx(tx, id, base+1, body, message, person, ts); err != nil {
		return Document{}, err
	}
	if err := tx.Commit(); err != nil {
		return Document{}, err
	}
	return s.DocumentByID(id)
}

func insertRevisionTx(tx *sql.Tx, id int64, revision int, body, message, person, ts string) error {
	_, err := tx.Exec(`INSERT INTO document_revisions(document_id,revision,body,digest,message,person,created_at)
		VALUES(?,?,?,?,?,?,?)`, id, revision, body, Digest(body), message, person, ts)
	if err != nil {
		return fmt.Errorf("insert revision %d: %w", revision, err)
	}
	return nil
}

const documentColumns = `id,project,slug,kind,title,head_revision,status,person,created_at,updated_at`

func scanDocument(row interface{ Scan(...any) error }) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.Project, &d.Slug, &d.Kind, &d.Title,
		&d.HeadRevision, &d.Status, &d.Person, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

func (s *Store) DocumentByID(id int64) (Document, error) {
	return scanDocument(s.db.QueryRow(`SELECT `+documentColumns+` FROM documents WHERE id=?`, id))
}

func (s *Store) DocumentRevision(id int64, revision int) (DocumentRevision, error) {
	var r DocumentRevision
	err := s.db.QueryRow(`SELECT id,document_id,revision,body,digest,message,person,created_at
		FROM document_revisions WHERE document_id=? AND revision=?`, id, revision).
		Scan(&r.ID, &r.DocumentID, &r.Revision, &r.Body, &r.Digest, &r.Message, &r.Person, &r.CreatedAt)
	return r, err
}

func (s *Store) DocumentBySlug(project, slug string) (Document, error) {
	return scanDocument(s.db.QueryRow(`SELECT `+documentColumns+`
		FROM documents WHERE project=? AND slug=?`, project, slug))
}

func (s *Store) Documents(project, kind string, includeArchived bool) ([]Document, error) {
	q := `SELECT ` + documentColumns + ` FROM documents WHERE project=?`
	args := []any{project}
	if kind != "" {
		q += ` AND kind=?`
		args = append(args, kind)
	}
	if !includeArchived {
		q += ` AND status='active'`
	}
	q += ` ORDER BY kind, slug`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DocumentRevisions liefert das Log ohne Body: eine Übersicht über zwanzig
// Fassungen soll nicht zwanzig Dokumente in den Speicher ziehen. Wer den Text
// einer Fassung braucht, holt sie mit DocumentRevision einzeln.
func (s *Store) DocumentRevisions(id int64) ([]DocumentRevision, error) {
	rows, err := s.db.Query(`SELECT id,document_id,revision,digest,message,person,created_at
		FROM document_revisions WHERE document_id=? ORDER BY revision DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocumentRevision
	for rows.Next() {
		var r DocumentRevision
		if err := rows.Scan(&r.ID, &r.DocumentID, &r.Revision, &r.Digest,
			&r.Message, &r.Person, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// documentPatchable nennt abschliessend, was sich an einem Dokument ändern
// lässt, ohne dass eine Revision entsteht. `body` steht bewusst NICHT darin:
// Inhalt ändert sich ausschliesslich über PushRevision, sonst gäbe es einen
// zweiten Schreibweg am Compare-and-Swap vorbei — genau die Seitentür, wegen
// der Dokumente keine knowledge-Zeilen mehr sind.
var documentPatchable = map[string]bool{"slug": true, "title": true, "kind": true, "status": true}

func (s *Store) PatchDocument(id int64, patch map[string]string) error {
	var sets []string
	var args []any
	for _, col := range []string{"slug", "title", "kind", "status"} {
		if v, ok := patch[col]; ok {
			sets = append(sets, col+"=?")
			args = append(args, v)
		}
	}
	for col := range patch {
		if !documentPatchable[col] {
			return fmt.Errorf("field %q is not patchable on a document", col)
		}
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=?")
	args = append(args, now(), id)
	res, err := s.db.Exec(`UPDATE documents SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
