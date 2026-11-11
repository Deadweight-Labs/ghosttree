package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// projectHistoryTables tragen eine Spalte project, gehören aber zu
// unveränderlichen Verläufen: versiegelte Context-Snapshots und die Ereignisse
// der Wissensrelationen halten fest, wie das Projekt damals hieß. Eine
// Umbenennung lässt sie stehen.
var projectHistoryTables = map[string]bool{
	"context_snapshots":         true,
	"context_snapshot_access":   true,
	"knowledge_relation_events": true,
	// ghost_path_revisions ist abgeleitet: Auslöser auf ghost_files führen den
	// Zähler je Pfad mit. renameProjectTx übernimmt ihn gesondert.
	"ghost_path_revisions": true,
}

// ErrProjectRenameBlocked sagt, dass eine Umbenennung nicht möglich ist, weil
// das Ziel schon belegt ist (Projektzeile oder Einträge unter dem neuen Namen).
// Nichts wurde geändert.
var ErrProjectRenameBlocked = errors.New("the new project name is already in use")

// projectTextTables nennt alle Tabellen mit einer Spalte project, außer den
// Verläufen. Sie werden aus dem Schema gelesen und nicht von Hand geführt: eine
// neue Tabelle mit project soll bei einer Umbenennung nicht still zurückbleiben.
func projectTextTables(q interface {
	Query(string, ...any) (*sql.Rows, error)
}) ([]string, error) {
	rows, err := q.Query(`SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) c
		WHERE m.type='table' AND c.name='project' ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !projectHistoryTables[name] && !strings.HasPrefix(name, "sqlite_") {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// renameProjectTx benennt die Projektzeile um und zieht jede Tabelle mit, die das
// Projekt als Text führt (Wissen, Requests, Pains, Ghost-Dateien, Threads,
// Sessions, Pull Requests, ...). Die Zeilen, die das Projekt über seine Id
// führen (Mitglieder, Freigabe, Organisation), folgen von selbst. Alles in der
// Transaktion des Aufrufers: ein Fehler lässt den alten Namen unberührt.
//
// Ist das Ziel belegt, bricht die Funktion mit ErrProjectRenameBlocked ab,
// bevor sie etwas ändert: zwei Projekte unter einem Namen zusammenzuführen ist
// keine Umbenennung.
func renameProjectTx(tx *sql.Tx, from, to string) error {
	if _, ok := projectTx(tx, from); !ok {
		return ErrProjectNotFound
	}
	if _, taken := projectTx(tx, to); taken {
		return ErrProjectRenameBlocked
	}
	tables, err := projectTextTables(tx)
	if err != nil {
		return err
	}
	for _, t := range tables {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM `+t+` WHERE project=?`, to).Scan(&n); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		if n > 0 {
			return ErrProjectRenameBlocked
		}
	}
	if _, err := tx.Exec(`UPDATE projects SET remote=? WHERE remote=?`, to, from); err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := tx.Exec(`UPDATE `+t+` SET project=? WHERE project=?`, to, from); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
	}
	// Der Revisionszähler je Pfad (Token für das Archivieren von Ghost-Dateien)
	// entsteht beim Umzug von ghost_files unter dem neuen Namen neu. Er darf nicht
	// zurückfallen: der neue Zähler übernimmt den alten Stand und geht darüber.
	if _, err := tx.Exec(`INSERT INTO ghost_path_revisions(project,path,revision)
		SELECT ?,path,revision+1 FROM ghost_path_revisions WHERE project=?
		ON CONFLICT(project,path) DO UPDATE SET revision=MAX(revision,excluded.revision)`, to, from); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ghost_path_revisions WHERE project=?`, from); err != nil {
		return err
	}
	return nil
}

// RenameInstallationRepo setzt eine Umbenennung um, die GitHub per Webhook
// meldet (repository, action renamed): Das bestehende Projekt zieht samt allem,
// was daran hängt, auf den neuen Namen um, statt dass unter dem neuen Namen ein
// leeres Projekt entsteht und das alte stehen bleibt. Die Liste der Quelle führt
// SyncInstallationRepos weiter (alten Namen raus, neuen rein).
//
// Liefert true, wenn ein Projekt umgezogen ist. false ohne Fehler heißt: nichts
// zu tun oder nicht zulässig, und der Aufrufer fährt mit der normalen
// Abgleichung fort. Das gilt für eine unbekannte Installation, Repos fremder
// Konten, ein Projekt, das es nicht gibt oder das einer anderen Organisation
// gehört als der Quelle, und für ein Ziel, das schon belegt ist.
func (s *Store) RenameInstallationRepo(installationID int64, from, to string) (bool, error) {
	if s.writer != nil {
		return queueValue(s, []any{installationID, from, to}, func(d *Store, p []any) (bool, error) {
			return d.RenameInstallationRepo(p[0].(int64), p[1].(string), p[2].(string))
		})
	}
	if installationID <= 0 {
		return false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id, orgID int64
	var kind, login string
	switch err := tx.QueryRow(`SELECT id, org_id, kind, login FROM git_sources WHERE installation_id=?`, installationID).Scan(&id, &orgID, &kind, &login); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	host, _ := sourceHost(kind)
	own := func(raw string) (string, bool) {
		remote, err := normalizeRemote(raw)
		return remote, err == nil && strings.HasPrefix(remote, host+"/"+login+"/")
	}
	oldRemote, okOld := own(from)
	newRemote, okNew := own(to)
	if !okOld || !okNew || oldRemote == newRemote {
		return false, nil
	}
	cur, known := projectTx(tx, oldRemote)
	if !known || cur.OrgID != orgID {
		return false, nil
	}
	switch err := renameProjectTx(tx, oldRemote, newRemote); {
	case errors.Is(err, ErrProjectRenameBlocked):
		return false, nil
	case err != nil:
		return false, err
	}
	if err := orgEvent(tx, orgID, "rename_project", "github", oldRemote, newRemote); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
