package store

import (
	"fmt"
	"slices"
)

// notifySchema: was ein Konto im Posteingang und an der Glocke sehen will. Es gibt
// keinen Versandweg außerhalb der Oberfläche (weder E-Mail noch Web-Push); die
// Einstellung betrifft deshalb genau das, was die Oberfläche selbst anzeigt: die
// Zeilen des Posteingangs und, für die Arten, die auf die Person warten, die Zahl an
// der Glocke (Erwähnungen zählen dort nie, siehe internal/web/notify_ui.go). Eine Art ist an, solange keine
// Zeile sie ausschaltet; gespeichert wird nur, was das Konto abgeschaltet hat.
const notifySchema = `
CREATE TABLE IF NOT EXISTS account_notify_off(
  account_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  set_at TEXT NOT NULL,
  PRIMARY KEY(account_id, kind)) WITHOUT ROWID;
`

// Arten von Benachrichtigungen, in der Reihenfolge, in der das Profil sie zeigt.
const (
	NotifyMention  = "mention"
	NotifyQuestion = "question"
	NotifyDecision = "decision"
	NotifyTask     = "task"
	NotifyGardener = "gardener"
)

// NotifyKinds sind alle Arten.
var NotifyKinds = []string{NotifyMention, NotifyQuestion, NotifyDecision, NotifyTask, NotifyGardener}

// NotifyOff nennt die Arten, die das Konto abgeschaltet hat. Ein Konto ohne
// Zeilen (und eine Person, die es nicht gibt) hat nichts abgeschaltet.
func (s *Store) NotifyOff(principalID string) (map[string]bool, error) {
	if s.reader != nil {
		return s.reader.NotifyOff(principalID)
	}
	out := map[string]bool{}
	id, ok := accountNumericID(principalID)
	if !ok {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT kind FROM account_notify_off WHERE account_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// SetNotify schaltet eine Art für das Konto an oder aus. Eine unbekannte Art und
// ein Konto, das es nicht gibt, sind ErrInvalidInput.
func (s *Store) SetNotify(principalID, kind string, on bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{principalID, kind, on}, func(d *Store, p []any) error {
			return d.SetNotify(p[0].(string), p[1].(string), p[2].(bool))
		})
	}
	id, ok := accountNumericID(principalID)
	if !ok || !slices.Contains(NotifyKinds, kind) {
		return fmt.Errorf("%w: notification kind", ErrInvalidInput)
	}
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM persons WHERE id=?`, id).Scan(&one); err != nil {
		return fmt.Errorf("%w: account", ErrInvalidInput)
	}
	if on {
		_, err := s.db.Exec(`DELETE FROM account_notify_off WHERE account_id=? AND kind=?`, id, kind)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO account_notify_off(account_id,kind,set_at) VALUES(?,?,?) ON CONFLICT(account_id,kind) DO NOTHING`, id, kind, now())
	return err
}
