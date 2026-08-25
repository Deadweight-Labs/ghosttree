package store

import (
	"database/sql"
	"fmt"
)

// GhostVersion ist eine abgelöste Fassung einer Beschreibung. Sie trägt den
// Codestand mit, den sie beschrieb — sonst wäre später nicht zu sehen, welche
// Fassung der Datei jemand vor sich hatte, als er das schrieb.
type GhostVersion struct {
	ID          int64  `json:"id"`
	Project     string `json:"project"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	ContentSHA  string `json:"content_sha"`
	GitBlob     string `json:"git_blob,omitempty"`
	LineCount   int    `json:"line_count"`
	Person      string `json:"person,omitempty"`
	Harness     string `json:"harness,omitempty"`
	SessionRef  string `json:"session_ref,omitempty"`
	DescribedAt string `json:"described_at"`
	ReplacedAt  string `json:"replaced_at"`
	Reason      string `json:"reason"`
}

const versionCols = `id, project, path, kind, description, content_sha, git_blob,
	line_count, person, harness, session_ref, described_at, replaced_at, reason`

// archiveGhostFileTx legt die aktuelle Fassung eines Pfades in die Historie,
// wenn es eine gibt. Gibt es keine, passiert nichts: das erste Beschreiben
// verdrängt nichts, und eine Fassung, die nie gegolten hat, gehört nicht in die
// Historie.
//
// Nimmt die Transaktion entgegen, statt sich eine eigene zu holen: Aufheben und
// Ersetzen müssen zusammen gelingen oder zusammen ausbleiben.
func archiveGhostFileTx(tx *sql.Tx, project, path, at, reason string) error {
	_, err := tx.Exec(`INSERT INTO ghost_file_versions
		(project,path,kind,description,content_sha,git_blob,line_count,
		 person,harness,session_ref,described_at,replaced_at,reason)
		SELECT project,path,kind,description,content_sha,git_blob,line_count,
		       person,harness,session_ref,described_at,?,?
		FROM ghost_files WHERE project=? AND path=?`, at, reason, project, path)
	return err
}

// GhostFileHistory liefert die abgelösten Fassungen eines Pfades, neueste
// zuerst. limit <= 0 heisst: alle.
func (s *Store) GhostFileHistory(project, path string, limit int) ([]GhostVersion, error) {
	query := `SELECT ` + versionCols + ` FROM ghost_file_versions
		WHERE project=? AND path=? ORDER BY replaced_at DESC, id DESC`
	args := []any{project, path}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GhostVersion{}
	for rows.Next() {
		var v GhostVersion
		if err := rows.Scan(&v.ID, &v.Project, &v.Path, &v.Kind, &v.Description,
			&v.ContentSHA, &v.GitBlob, &v.LineCount, &v.Person, &v.Harness,
			&v.SessionRef, &v.DescribedAt, &v.ReplacedAt, &v.Reason); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GhostFileChain ist die Historie mit der aktuellen Fassung an der Spitze,
// erkennbar an ihrem leeren ReplacedAt. Wer wissen will, was sich geändert hat,
// braucht zu jeder abgelösten Fassung ihren Nachfolger — und der Nachfolger der
// neuesten abgelösten Fassung steht nicht in der Historie, sondern in
// ghost_files. limit begrenzt die abgelösten Fassungen; der Kopf zählt nicht
// mit, weil er kein Teil der Historie ist.
//
// Ein unbeschriebener Pfad hat eine leere Kette und keinen Fehler: das ist eine
// Antwort und keine Störung.
func (s *Store) GhostFileChain(project, path string, limit int) ([]GhostVersion, error) {
	hist, err := s.GhostFileHistory(project, path, limit)
	if err != nil {
		return nil, err
	}
	cur, err := s.GhostFileByPath(project, path)
	if err == sql.ErrNoRows {
		return hist, nil
	}
	if err != nil {
		return nil, err
	}
	head := GhostVersion{
		Project: cur.Project, Path: cur.Path, Kind: cur.Kind,
		Description: cur.Description, ContentSHA: cur.ContentSHA, GitBlob: cur.GitBlob,
		LineCount: cur.LineCount, Person: cur.Person, Harness: cur.Harness,
		SessionRef: cur.SessionRef, DescribedAt: cur.DescribedAt,
	}
	return append([]GhostVersion{head}, hist...), nil
}

// GhostHistoryCount ist die Zahl ohne den Text. Die Auslieferung im Hook nennt
// nur, DASS es Vorfassungen gibt — den Text dorthin zu kippen wäre genau das
// Kontextrauschen, gegen das die Entdopplung antritt.
func (s *Store) GhostHistoryCount(project, path string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM ghost_file_versions WHERE project=? AND path=?`,
		project, path).Scan(&n)
	return n, err
}

// MoveGhostFile hängt eine Beschreibung auf einen neuen Pfad um und nimmt ihre
// Historie mit. Ohne das Mitnehmen zerfiele die Geschichte einer Datei an jeder
// Umbenennung, und gerade die langlebigen Dateien werden umbenannt.
//
// Der Umzug selbst wird als Eintrag vermerkt. Er ist keine neue Erkenntnis,
// aber er soll nachvollziehbar sein: wer in einem Jahr fragt, warum die
// Beschreibung eines Pfades älter ist als der Pfad, findet hier die Antwort.
func (s *Store) MoveGhostFile(project, from, to string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ghost_files WHERE project=? AND path=?`,
		project, from).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("%q hat keine Beschreibung, die umziehen könnte", from)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ghost_files WHERE project=? AND path=?`,
		project, to).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return fmt.Errorf("%q hat schon eine Beschreibung; ein Umzug würde sie überschreiben", to)
	}

	ts := now()
	if _, err := tx.Exec(`INSERT INTO ghost_file_versions
		(project,path,kind,description,content_sha,git_blob,line_count,
		 person,harness,session_ref,described_at,replaced_at,reason)
		SELECT project,?,kind,'(verschoben von '||path||')',content_sha,git_blob,line_count,
		       person,harness,session_ref,described_at,?,'verschoben'
		FROM ghost_files WHERE project=? AND path=?`, to, ts, project, from); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE ghost_file_versions SET path=? WHERE project=? AND path=?`,
		to, project, from); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE ghost_files SET path=?, updated_at=? WHERE project=? AND path=?`,
		to, ts, project, from); err != nil {
		return err
	}
	return tx.Commit()
}
