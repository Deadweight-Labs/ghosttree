package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// gitAnchorSchema (REQ-597): ein Git-Anker je Schreibvorgang, nie geteilt.
//
// Eine Tabelle mit Fremdschlüssel-Spalte an jeder Revisionstabelle statt einer
// Anker-Spalte-Gruppe je Tabelle oder einer Zuordnungstabelle (Art, Id):
//   - Die Revision besitzt ihren Anker. Wird sie abgelöst, wandert die Zeile
//     samt anchor_id in die Historie (knowledge_versions, ghost_file_versions);
//     der Anker muss dabei nie umgeschrieben werden, er zeigt weiter auf
//     dieselbe Zeile.
//   - Eine Zuordnungstabelle (Art, Id) müsste beim Archivieren umgehängt werden,
//     und genau dort entstünden "falsche" Anker.
//   - Vier Spalten an acht Tabellen wären Streuung; die Form des Ankers soll
//     sich an einer Stelle ändern lassen (PR, später Remote).
//
// Je Schreibvorgang eine Zeile, auch bei gleichem Commit: captured_at gehört
// zum Vorgang, und das Zusammenfassen gleicher Anker wäre eine Optimierung,
// die erst bei einer Messung nötig wird.
//
// pr bleibt NULL, bis REQ-596 (GitHub-App) die Branch-Zuordnung liefert. Nur
// pr darf nachträglich von NULL auf einen Wert gesetzt werden (siehe Trigger):
// der Stand des Codes ist ab dem Schreiben festgeschrieben, die PR-Nummer wird
// erst später bekannt.
const gitAnchorSchema = `
CREATE TABLE IF NOT EXISTS git_anchors(
  id INTEGER PRIMARY KEY,
  commit_sha TEXT NOT NULL CHECK(commit_sha != ''),
  branch TEXT NOT NULL DEFAULT '',
  merge_base TEXT NOT NULL DEFAULT '',
  dirty INTEGER NOT NULL DEFAULT 0 CHECK(dirty IN (0,1)),
  pr INTEGER,
  captured_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS git_anchors_commit ON git_anchors(commit_sha);
CREATE INDEX IF NOT EXISTS git_anchors_pr ON git_anchors(pr) WHERE pr IS NOT NULL;
CREATE TRIGGER IF NOT EXISTS git_anchors_immutable
  BEFORE UPDATE OF commit_sha, branch, merge_base, dirty, captured_at ON git_anchors
  BEGIN SELECT RAISE(ABORT,'git_anchors is immutable'); END;
CREATE TRIGGER IF NOT EXISTS git_anchors_pr_once
  BEFORE UPDATE OF pr ON git_anchors WHEN old.pr IS NOT NULL
  BEGIN SELECT RAISE(ABORT,'git_anchors.pr is set once'); END;
CREATE TRIGGER IF NOT EXISTS git_anchors_no_delete
  BEFORE DELETE ON git_anchors
  BEGIN SELECT RAISE(ABORT,'git_anchors is immutable'); END;
`

// anchoredTables sind die Revisionstabellen, die einen Anker tragen. Die
// Spalte ist nullbar: Altbestand, destillierte Einträge, Web-Bearbeitungen und
// Clients ohne Git haben keinen, und NULL sagt genau das.
var anchoredTables = []string{
	"knowledge", "knowledge_versions",
	"ghost_files", "ghost_file_versions",
	"request_evidence", "request_activity",
	"pain_events", "coord_messages",
	// ideas (REQ-606): der Anker und der Kontext des Anlegens stehen an der Idee
	// selbst, die ihren Zustand später ändert, aber nie neu verankert wird.
	"ideas",
}

// ensureGitAnchors legt Tabelle und Spalten additiv an. Wie die anderen
// ensure*-Schritte darf er auf jeder Datenbank beliebig oft laufen.
func ensureGitAnchors(db *sql.DB) error {
	if _, err := db.Exec(gitAnchorSchema); err != nil {
		return err
	}
	for _, table := range anchoredTables {
		if err := ensureColumn(db, table, "anchor_id", `INTEGER REFERENCES git_anchors(id)`); err != nil {
			return fmt.Errorf("%s.anchor_id: %w", table, err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS ` + table + `_anchor ON ` + table + `(anchor_id) WHERE anchor_id IS NOT NULL`); err != nil {
			return err
		}
	}
	return nil
}

// insertAnchorTx speichert einen Anker und liefert seine Id für die
// Fremdschlüssel-Spalte; für einen leeren Anker nil, also NULL. Ein leerer
// Anker legt keine Zeile an.
func insertAnchorTx(tx txExec, a *gitanchor.Anchor) (any, error) {
	if a == nil || a.IsZero() {
		return nil, nil
	}
	dirty := 0
	if a.Dirty {
		dirty = 1
	}
	res, err := tx.Exec(`INSERT INTO git_anchors(commit_sha,branch,merge_base,dirty,pr,captured_at) VALUES(?,?,?,?,?,?)`,
		a.Commit, a.Branch, a.MergeBase, dirty, nil, now())
	if err != nil {
		return nil, err
	}
	return res.LastInsertId()
}

// anchorRowCols liest einen Anker aus einer Zeile, die ihn per LEFT JOIN
// mitbringt: die Spalten stehen in genau dieser Reihenfolge.
const anchorRowCols = `a.commit_sha, a.branch, a.merge_base, a.dirty, a.pr, a.captured_at`

func scanAnchor(commit, branch, mergeBase sql.NullString, dirty sql.NullInt64, pr sql.NullInt64, at sql.NullString) *gitanchor.Anchor {
	if !commit.Valid || commit.String == "" {
		return nil
	}
	a := &gitanchor.Anchor{Commit: commit.String, Branch: branch.String, MergeBase: mergeBase.String,
		Dirty: dirty.Int64 != 0, CapturedAt: at.String}
	if pr.Valid {
		n := pr.Int64
		a.PR = &n
	}
	return a
}

// anchorsFor liefert die Anker der Zeilen einer Tabelle, nach Zeilen-Id.
// Zeilen ohne Anker fehlen im Ergebnis. table kommt nur aus anchoredTables.
func (s *Store) anchorsFor(table string, ids []int64) (map[int64]*gitanchor.Anchor, error) {
	out := map[int64]*gitanchor.Anchor{}
	for len(ids) > 0 {
		part := ids
		if len(part) > 500 {
			part = part[:500]
		}
		ids = ids[len(part):]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT t.id, `+anchorRowCols+` FROM `+table+` t JOIN git_anchors a ON a.id=t.anchor_id
			WHERE t.id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var commit, branch, mb, at sql.NullString
			var dirty, pr sql.NullInt64
			if err := rows.Scan(&id, &commit, &branch, &mb, &dirty, &pr, &at); err != nil {
				rows.Close()
				return nil, err
			}
			if a := scanAnchor(commit, branch, mb, dirty, pr, at); a != nil {
				out[id] = a
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AnchorFilter sind die Lesefilter nach Git-Anker. Leer filtert nichts.
type AnchorFilter struct {
	// Commit ist ein Präfix des HEAD-Commits, mindestens gitanchor.MinPrefix
	// Hexzeichen. Ein kürzerer oder nicht-hexadezimaler Wert ergibt keine
	// Treffer statt aller (Valid).
	Commit string
	// PR filtert auf Einträge, deren Anker diese PR-Nummer trägt. Solange
	// niemand die Spalte füllt (REQ-596), liefert das nichts.
	PR int64
}

// Active sagt, ob überhaupt gefiltert wird.
func (f AnchorFilter) Active() bool { return f.Commit != "" || f.PR != 0 }

// Valid sagt, ob der Filter eine zulässige Angabe trägt. Ein ungültiger Filter
// gehört abgelehnt, nicht ignoriert: still alles zu liefern, wäre die falsche
// Antwort auf "was wurde zu diesem Commit gesagt".
func (f AnchorFilter) Valid() error {
	if f.Commit != "" && !gitanchor.ValidPrefix(gitanchor.Normalize(f.Commit)) {
		return fmt.Errorf("commit filter needs at least %d hexadecimal characters", gitanchor.MinPrefix)
	}
	if f.PR < 0 {
		return fmt.Errorf("invalid pr filter")
	}
	return nil
}

// Where ist die Bedingung auf einer Spalte anchorCol, die auf git_anchors.id
// zeigt, zum Anhängen mit AND. Leer ohne aktiven Filter.
func (f AnchorFilter) Where(anchorCol string) (string, []any) {
	if !f.Active() {
		return "", nil
	}
	var conds []string
	var args []any
	if f.Commit != "" {
		conds = append(conds, `commit_sha LIKE ?`)
		args = append(args, gitanchor.Normalize(f.Commit)+"%")
	}
	if f.PR != 0 {
		conds = append(conds, `pr = ?`)
		args = append(args, f.PR)
	}
	return anchorCol + ` IN (SELECT id FROM git_anchors WHERE ` + strings.Join(conds, " AND ") + `)`, args
}

// attachKnowledgeAnchors hängt jedem Eintrag den Anker seiner aktuellen
// Revision an.
func (s *Store) attachKnowledgeAnchors(ks []Knowledge) error {
	if len(ks) == 0 {
		return nil
	}
	ids := make([]int64, len(ks))
	for i, k := range ks {
		ids[i] = k.ID
	}
	anchors, err := s.anchorsFor("knowledge", ids)
	if err != nil {
		return err
	}
	for i := range ks {
		ks[i].GitAnchor = anchors[ks[i].ID]
	}
	return nil
}

// attachEvidenceAnchors hängt den Nachweisen ihren Anker an.
func (s *Store) attachEvidenceAnchors(es []requestdomain.Evidence) error {
	if len(es) == 0 {
		return nil
	}
	ids := make([]int64, len(es))
	for i, e := range es {
		ids[i] = e.ID
	}
	anchors, err := s.anchorsFor("request_evidence", ids)
	if err != nil {
		return err
	}
	for i := range es {
		es[i].GitAnchor = anchors[es[i].ID]
	}
	return nil
}

// attachActivityAnchors hängt den Ereignissen eines Requests ihren Anker an.
func (s *Store) attachActivityAnchors(as []requestdomain.Activity) error {
	if len(as) == 0 {
		return nil
	}
	ids := make([]int64, len(as))
	for i, a := range as {
		ids[i] = a.ID
	}
	anchors, err := s.anchorsFor("request_activity", ids)
	if err != nil {
		return err
	}
	for i := range as {
		as[i].GitAnchor = anchors[as[i].ID]
	}
	return nil
}
