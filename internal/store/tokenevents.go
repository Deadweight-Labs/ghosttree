package store

import "database/sql"

// token_events hält fest, wann ein Anmeldetoken ausgestellt und wann es
// widerrufen wurde: für welches Konto, welche Art, welche Maschine, nie das
// Token und nie sein Hash. Die Einträge entstehen durch Trigger an api_tokens,
// damit jeder Weg (Gerätelogin, cli-Token, Gärtner-Sitzung, Widerruf durch den
// Besitzer oder einen Admin) sie schreibt und kein Aufrufer sie vergessen kann.
// Die Tabelle ist angehängt-nur wie role_events.
const tokenEventsSchema = `
CREATE TABLE IF NOT EXISTS token_events(
  id INTEGER PRIMARY KEY, token_id INTEGER NOT NULL, account_id INTEGER NOT NULL,
  action TEXT NOT NULL CHECK(action IN ('issued','revoked')),
  kind TEXT NOT NULL, machine TEXT NOT NULL DEFAULT '', label TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS token_events_account ON token_events(account_id, id);
CREATE TRIGGER IF NOT EXISTS token_events_no_update BEFORE UPDATE ON token_events
  BEGIN SELECT RAISE(ABORT, 'token_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS token_events_no_delete BEFORE DELETE ON token_events
  BEGIN SELECT RAISE(ABORT, 'token_events is append-only'); END;
DROP TRIGGER IF EXISTS api_tokens_issued;
CREATE TRIGGER api_tokens_issued AFTER INSERT ON api_tokens
  BEGIN
    INSERT INTO token_events(token_id, account_id, action, kind, machine, label, expires_at, created_at)
    VALUES(NEW.id, NEW.account_id, 'issued', NEW.kind, NEW.machine, NEW.label, NEW.expires_at, strftime('%Y-%m-%dT%H:%M:%SZ','now'));
  END;
DROP TRIGGER IF EXISTS api_tokens_revoked;
CREATE TRIGGER api_tokens_revoked AFTER UPDATE OF revoked_at ON api_tokens
  WHEN OLD.revoked_at = '' AND NEW.revoked_at <> ''
  BEGIN
    INSERT INTO token_events(token_id, account_id, action, kind, machine, label, expires_at, created_at)
    VALUES(NEW.id, NEW.account_id, 'revoked', NEW.kind, NEW.machine, NEW.label, NEW.expires_at, strftime('%Y-%m-%dT%H:%M:%SZ','now'));
  END;
`

// ensureTokenEvents legt die Tabelle und die Trigger an. Die Trigger werden bei
// jedem Öffnen neu gesetzt: ein Umbau von api_tokens (allowDeviceTokenKind)
// nimmt sie mit.
func ensureTokenEvents(db *sql.DB) error {
	_, err := db.Exec(tokenEventsSchema)
	return err
}

// TokenEvent ist ein Eintrag des Verlaufs der Tokens eines Kontos.
type TokenEvent struct {
	TokenID   int64  `json:"token_id"`
	Action    string `json:"action"`
	Kind      string `json:"kind"`
	Machine   string `json:"machine,omitempty"`
	Label     string `json:"label,omitempty"`
	CreatedAt string `json:"created_at"`
}

// TokenEvents liefert die letzten Einträge zu den Tokens eines Kontos, neueste zuerst.
func (s *Store) TokenEvents(accountID int64, limit int) ([]TokenEvent, error) {
	if s.reader != nil {
		return s.reader.TokenEvents(accountID, limit)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT token_id, action, kind, machine, label, created_at FROM token_events WHERE account_id=? ORDER BY id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenEvent
	for rows.Next() {
		var e TokenEvent
		if err := rows.Scan(&e.TokenID, &e.Action, &e.Kind, &e.Machine, &e.Label, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
