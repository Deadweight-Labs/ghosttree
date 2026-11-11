package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/query"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// SearchPreset ist eine gespeicherte Suche (REQ-600). Scope ist "user"
// (Ref = Konto), "project" (Ref = Remote) oder "org" (Ref = Org-Id).
type SearchPreset struct {
	ID          int64    `json:"id"`
	Scope       string   `json:"scope"`
	Ref         string   `json:"ref"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Queries     []string `json:"queries"`
	Args        []string `json:"args,omitempty"`
	CreatedBy   string   `json:"created_by,omitempty"`
	CreatedAt   string   `json:"created_at"`
	// UpdatedBy ist, wer die Suche zuletzt geändert hat; CreatedBy bleibt der Anleger.
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at"`
	// Builtin ist bei einer mitgelieferten Suche gesetzt; die steht nicht in der
	// Datenbank.
	Builtin bool `json:"builtin,omitempty"`
}

// PresetInput speichert eine Suche; gleicher Bereich und Name ersetzen.
type PresetInput struct {
	Scope       string
	Ref         string
	Name        string
	Description string
	Queries     []string
	By          string
	// MayReplace sagt, dass der Aufrufer die Suche eines anderen im Projekt oder in
	// der Org auch löschen dürfte (Lead, Org-Owner); nur dann ersetzt er sie.
	MayReplace bool
}

// ErrPresetConflict: unter dem Namen liegt die Suche eines anderen, und der Aufrufer
// dürfte sie nicht löschen.
var ErrPresetConflict = labelRule("preset_conflict", "a saved search of that name belongs to someone else", "choose another name, or ask a lead to change it")

const presetCols = `id,scope_kind,scope_ref,name,description,queries,created_by,created_at,updated_by,updated_at`

func scanPreset(row interface{ Scan(...any) error }) (SearchPreset, error) {
	var p SearchPreset
	var q string
	if err := row.Scan(&p.ID, &p.Scope, &p.Ref, &p.Name, &p.Description, &q, &p.CreatedBy, &p.CreatedAt, &p.UpdatedBy, &p.UpdatedAt); err != nil {
		return p, err
	}
	p.Queries = strings.Split(q, "\n")
	p.Args = query.ArgsOf(p.Queries)
	return p, nil
}

// SavePreset speichert eine Suche. Die Zeilen werden mit Beispielwerten als
// Suchzeile geprüft; ein Fehler dort steht im Ergebnis.
func (s *Store) SavePreset(in PresetInput) (SearchPreset, error) {
	in.Name = strings.TrimSpace(in.Name)
	if err := query.CheckPreset(in.Name, in.Queries); err != nil {
		return SearchPreset{}, labelRule("preset_invalid", err.Error(), "fix the search line; see the search language for the qualifiers")
	}
	desc, err := CleanLabelDescription(in.Description)
	if err != nil {
		return SearchPreset{}, err
	}
	in.Description = desc
	switch in.Scope {
	case "user", "org":
	case "project":
		in.Ref = scope.NormalizeRemote(in.Ref)
	default:
		return SearchPreset{}, labelRule("preset_scope_invalid", fmt.Sprintf("unknown scope %q", in.Scope), "use user, project or org")
	}
	if in.Ref == "" {
		return SearchPreset{}, labelRule("preset_scope_invalid", "a saved search needs an owner", "name the project or org")
	}
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (SearchPreset, error) { return d.SavePreset(p[0].(PresetInput)) })
	}
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return SearchPreset{}, err
	}
	defer tx.Rollback()
	var author string
	switch err := tx.QueryRow(`SELECT created_by FROM search_presets WHERE scope_kind=? AND scope_ref=? AND lower(name)=lower(?)`, in.Scope, in.Ref, in.Name).Scan(&author); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return SearchPreset{}, err
	case in.Scope != "user" && author != in.By && !in.MayReplace:
		return SearchPreset{}, ErrPresetConflict
	}
	// Beim Ersetzen bleibt der Anleger; wer ersetzt, steht in updated_by.
	if _, err = tx.Exec(`INSERT INTO search_presets(scope_kind,scope_ref,name,description,queries,created_by,created_at,updated_by,updated_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(scope_kind,scope_ref,lower(name)) DO UPDATE SET description=excluded.description, queries=excluded.queries, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		in.Scope, in.Ref, in.Name, in.Description, strings.Join(in.Queries, "\n"), in.By, ts, in.By, ts); err != nil {
		return SearchPreset{}, err
	}
	if err := tx.Commit(); err != nil {
		return SearchPreset{}, err
	}
	return scanPreset(s.db.QueryRow(`SELECT `+presetCols+` FROM search_presets WHERE scope_kind=? AND scope_ref=? AND lower(name)=lower(?)`, in.Scope, in.Ref, in.Name))
}

// PresetByID liefert eine gespeicherte Suche ohne Zugriffsprüfung.
func (s *Store) PresetByID(id int64) (SearchPreset, error) {
	if s.reader != nil {
		return s.reader.PresetByID(id)
	}
	p, err := scanPreset(s.db.QueryRow(`SELECT `+presetCols+` FROM search_presets WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return p, labelRule("preset_not_found", "no such saved search", "list the saved searches")
	}
	return p, err
}

// DeletePreset löscht eine gespeicherte Suche.
func (s *Store) DeletePreset(id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id}, func(d *Store, p []any) error { return d.DeletePreset(p[0].(int64)) })
	}
	_, err := s.db.Exec(`DELETE FROM search_presets WHERE id=?`, id)
	return err
}

// PresetsFor liefert die gespeicherten Suchen, die für ein Konto in einem
// Projekt gelten: seine persönlichen, die des Projekts und die der Org des
// Projekts. Die mitgelieferten gehören nicht dazu.
func (s *Store) PresetsFor(account int64, project string) ([]SearchPreset, error) {
	if s.reader != nil {
		return s.reader.PresetsFor(account, project)
	}
	project = scope.NormalizeRemote(project)
	rows, err := s.db.Query(`SELECT `+presetCols+` FROM search_presets
		WHERE (scope_kind='user' AND scope_ref=?)
		   OR (scope_kind='project' AND scope_ref=?)
		   OR (scope_kind='org' AND scope_ref=(SELECT CAST(org_id AS TEXT) FROM projects WHERE remote=?))
		ORDER BY CASE scope_kind WHEN 'user' THEN 0 WHEN 'project' THEN 1 ELSE 2 END, lower(name)`,
		fmt.Sprint(account), project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchPreset
	for rows.Next() {
		p, err := scanPreset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
