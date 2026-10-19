package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Aussagequalitäten einer Aktivität, wörtlich aus v1 §6. v2 hat den Gedanken
// nur als Prosa; die vier Werte fehlen dort (Wissenseintrag #2077).
//
// Sie zu vermischen ist der Fehler, vor dem beide Fassungen warnen: ein
// gestarteter Werkzeugaufruf ist eine ABSICHT, kein Ergebnis. Ein
// Dateisystem-Watcher sieht eine Änderung, ohne sie jemandem zuordnen zu
// können. Wo Daten fehlen, lautet die Aussage "unbekannt" — nicht "hat nichts
// verändert".
const (
	ActivityIntent     = "intent"
	ActivityReported   = "reported_success"
	ActivityObserved   = "observed_change"
	ActivityUnattached = "unattributed"
)

var activityQualities = map[string]bool{
	ActivityIntent: true, ActivityReported: true,
	ActivityObserved: true, ActivityUnattached: true,
}

// PathActivity ist ein Werkzeugaufruf mit Dateibezug, als abfragbare Zeile.
//
// Checkout ist der Grund, warum das mehr ist als eine Pfadliste. Derselbe
// Pfad im selben Checkout bedeutet mögliche gegenseitige Überschreibung;
// derselbe Pfad in zwei Worktrees bedeutet einen späteren Merge-Konflikt und
// sonst nichts. Wer beides verschmilzt, warnt entweder zu oft oder zu selten —
// und eine Warnung, die zu oft kommt, wird ignoriert, bevor sie einmal
// stimmt.
type PathActivity struct {
	ID                int64  `json:"id,omitempty"`
	Project           string `json:"project,omitempty"`
	SessionExternalID string `json:"session_external_id"`
	Checkout          string `json:"checkout,omitempty"`
	Tool              string `json:"tool"`
	Path              string `json:"path"`
	Writes            bool   `json:"writes,omitempty"`
	Quality           string `json:"quality"`
	At                string `json:"at,omitempty"`
	// AccountID stempelt der Server beim Schreiben aus dem Token; ein Client kann
	// ihn nicht setzen. 0 ist Altbestand und gehört dem Instanz-Owner. Nur so
	// lässt sich Aktivität einem Konto zuordnen, wenn eine Session-Kennung bei
	// einem anderen Harness unter fremdem Konto ebenfalls vorkommt.
	AccountID int64 `json:"-"`
}

// ensurePathActivityAccount baut eine alte path_activity um, die kein Konto
// kennt. SQLite kann eine UNIQUE-Klausel nicht ändern, und ohne das Konto in
// ihr könnte jemand mit einer erratenen Zeile die echte Zeile eines anderen
// still verdrängen (INSERT OR IGNORE).
func ensurePathActivityAccount(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(path_activity)`)
	if err != nil {
		return err
	}
	has := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dv sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dv, &pk); err != nil {
			rows.Close()
			return err
		}
		has = has || name == "account_id"
	}
	rows.Close()
	if has {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE path_activity_new(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL DEFAULT '',
  session_external_id TEXT NOT NULL,
  checkout TEXT NOT NULL DEFAULT '',
  tool TEXT NOT NULL,
  path TEXT NOT NULL,
  writes INTEGER NOT NULL DEFAULT 0,
  quality TEXT NOT NULL CHECK(quality IN ('intent','reported_success','observed_change','unattributed')),
  at TEXT NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  UNIQUE(account_id,session_external_id,tool,path,quality,at));
INSERT INTO path_activity_new(id,project,session_external_id,checkout,tool,path,writes,quality,at,account_id)
  SELECT id,project,session_external_id,checkout,tool,path,writes,quality,at,0 FROM path_activity;
DROP TABLE path_activity;
ALTER TABLE path_activity_new RENAME TO path_activity;
CREATE INDEX IF NOT EXISTS path_activity_path ON path_activity(project,path,at);
CREATE INDEX IF NOT EXISTS path_activity_session ON path_activity(session_external_id,at);`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecordPathActivity schreibt beobachtete Aktivität fort. Idempotent über
// (Session, Werkzeug, Pfad, Qualität, Zeit): der Collector liest Transkripte
// mehrfach, und jede erneute Verarbeitung derselben Zeile darf die Historie
// nicht verdoppeln.
func (s *Store) RecordPathActivity(events []PathActivity) error {
	if len(events) == 0 {
		return nil
	}
	for _, e := range events {
		if !activityQualities[e.Quality] {
			return fmt.Errorf("unknown activity quality %q", e.Quality)
		}
		if e.SessionExternalID == "" || e.Path == "" {
			return fmt.Errorf("activity needs a session and a path")
		}
	}
	if s.writer != nil {
		return queueWrite(s, []any{events}, func(d *Store, p []any) error {
			return d.RecordPathActivity(p[0].([]PathActivity))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		// Immer als UTC mit Z gespeichert: Vergleiche und Fenster laufen über
		// Text, und ein Wert mit Offset würde sie verfälschen.
		at := now()
		if t, err := time.Parse(time.RFC3339, e.At); err == nil {
			at = t.UTC().Format(time.RFC3339)
		}
		writes := 0
		if e.Writes {
			writes = 1
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO path_activity(
				project,session_external_id,checkout,tool,path,writes,quality,at,account_id)
			VALUES(?,?,?,?,?,?,?,?,?)`,
			e.Project, e.SessionExternalID, e.Checkout, e.Tool, e.Path, writes, e.Quality, at, e.AccountID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PathActivitySince beantwortet die Frage, um die es geht: wer hat in den
// letzten N Minuten an diesem Pfad gearbeitet?
//
// Ohne excludeSession bekäme ein Agent sich selbst als Konfliktpartner
// gemeldet — die häufigste Datei, an der jemand gerade arbeitet, ist die
// eigene.
func (s *Store) PathActivitySince(project, path, since, excludeSession string) ([]PathActivity, error) {
	if s.reader != nil {
		return s.reader.PathActivitySince(project, path, since, excludeSession)
	}
	query := `SELECT id,project,session_external_id,checkout,tool,path,writes,quality,at,account_id
		FROM path_activity WHERE path=? AND at>=?`
	args := []any{path, since}
	if project != "" {
		query += ` AND project=?`
		args = append(args, project)
	}
	if excludeSession != "" {
		query += ` AND session_external_id<>?`
		args = append(args, excludeSession)
	}
	query += ` ORDER BY at DESC, id DESC LIMIT 200`
	return s.scanPathActivity(query, args...)
}

// SessionPathActivity zeigt, woran EINE Session gearbeitet hat. Das ist die
// Detailansicht hinter einem Teilnehmer und der Grund, warum die Daten auch
// ohne zweiten Agenten nützen: wer allein arbeitet, will wissen, was die
// Session von heute Mittag angefasst hat.
func (s *Store) SessionPathActivity(sessionExternalID string, account int64, since string, limit int) ([]PathActivity, error) {
	if s.reader != nil {
		return s.reader.SessionPathActivity(sessionExternalID, account, since, limit)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Nur Zeilen dieses Kontos: dieselbe Kennung unter fremdem Konto ist nicht
	// diese Session.
	return s.scanPathActivity(`SELECT id,project,session_external_id,checkout,tool,path,writes,quality,at,account_id
		FROM path_activity WHERE session_external_id=? AND at>=?
		  AND (CASE WHEN account_id=0 THEN ? ELSE account_id END)=?
		ORDER BY at DESC, id DESC LIMIT ?`, sessionExternalID, since, instanceOwnerID(s.db), account, limit)
}

func (s *Store) scanPathActivity(query string, args ...any) ([]PathActivity, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PathActivity
	for rows.Next() {
		var a PathActivity
		var writes int
		if err := rows.Scan(&a.ID, &a.Project, &a.SessionExternalID, &a.Checkout,
			&a.Tool, &a.Path, &writes, &a.Quality, &a.At, &a.AccountID); err != nil {
			return nil, err
		}
		a.Writes = writes != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActivityWindow ist die Zeitgrenze für eine Überschneidungsfrage. Fünf
// Minuten, dreißig Minuten und die ganze Session sind die Fenster, die v1 §6
// vorschlägt; dreißig ist der Standard, weil eine Datei, die vor einer halben
// Stunde angefasst wurde, noch als offene Arbeit gelten kann.
func ActivityWindow(minutes int) string {
	if minutes <= 0 {
		minutes = 30
	}
	return time.Now().UTC().Add(-time.Duration(minutes) * time.Minute).Format(time.RFC3339)
}

// ConflictKind unterscheidet die drei Lagen aus v1 §7 und v2 §A9. Die
// Unterscheidung ist der eigentliche Inhalt einer Warnung: "gleicher
// Checkout" heißt, ihr überschreibt euch gerade; "anderer Worktree" heißt,
// ihr merged später; und wer das gleich formuliert, wird ignoriert.
const (
	ConflictSameCheckout  = "same-checkout"
	ConflictOtherWorktree = "other-worktree"
	ConflictUnknownScope  = "unknown-scope"
)

// ClassifyConflict sagt, welche der drei Lagen vorliegt.
//
// Ein leerer Checkout auf einer der beiden Seiten ergibt NICHT "kein
// Konflikt", sondern "unbekannt". Fehlende Beobachtung ist keine
// Konfliktfreiheit — das ist derselbe Grundsatz wie bei der Zustellung.
func ClassifyConflict(mine, theirs string) string {
	switch {
	case mine == "" || theirs == "":
		return ConflictUnknownScope
	case mine == theirs:
		return ConflictSameCheckout
	default:
		return ConflictOtherWorktree
	}
}

// DescribeConflict formuliert die Lage für ein Modell. Der Text sagt, was zu
// tun wäre, nicht nur was der Fall ist: eine Warnung ohne Handlungsweg wird
// zur Kenntnis genommen und dann ignoriert.
func DescribeConflict(kind string) string {
	switch kind {
	case ConflictSameCheckout:
		return "same checkout — you may overwrite each other right now"
	case ConflictOtherWorktree:
		return "different worktree — no direct overwrite, but a merge conflict later"
	default:
		return "scope unknown — this is missing observation, not proof that nothing collides"
	}
}

// SummarizeActivity fasst zusammen, was eine Session angefasst hat. Bewusst
// deterministisch und ohne Modell: v1 §6 hält fest, dass die Auswahl eines
// Zeitfensters kein dauerhaft mitlaufendes LLM erfordert, und eine erzählte
// Zusammenfassung wäre unprüfbar.
func SummarizeActivity(events []PathActivity) string {
	if len(events) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var paths []string
	wrote := 0
	for _, e := range events {
		if !seen[e.Path] {
			seen[e.Path] = true
			paths = append(paths, e.Path)
		}
		if e.Writes && e.Quality != ActivityIntent {
			wrote++
		}
	}
	head := paths
	if len(head) > 3 {
		head = head[:3]
	}
	out := strings.Join(head, ", ")
	if len(paths) > len(head) {
		out += fmt.Sprintf(" and %d more", len(paths)-len(head))
	}
	if wrote > 0 {
		out += fmt.Sprintf(" (%d confirmed writes)", wrote)
	}
	return out
}
