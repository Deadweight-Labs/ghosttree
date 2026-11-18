package store

import (
	"database/sql"
	"time"
)

// tokenTouchEvery is how often at most a token's last use is written. A token
// that calls every second costs one write per interval, not one per call. The
// interval is the one the machine's last sight is written at (a minute) and a
// little more than the beat of a collector (30 s): the account page and the
// machine row read "used 1 min ago" for a device that is talking, not "12 min".
const tokenTouchEvery = time.Minute

// tokenUseSinceKey is the org_state key holding when the first token use was
// recorded. Tokens used before that moment have no last use although they were
// in use, so "never used" only means something once tracking has run a while.
const tokenUseSinceKey = "token_use_tracking_since"

// TouchToken records that a token authenticated. Callers invoke it after a
// successful AuthenticatePrincipal; authentication itself stays a pure read.
// In runtime mode the write is coalesced and queued off the request path, and
// a lost write only makes the last use a little older.
func (s *Store) TouchToken(id int64) {
	if s.bookkeeper != nil {
		s.bookkeeper.coalesceToken(id)
		return
	}
	if id == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if writeTokenUse(tx, now(), map[int64]struct{}{id: {}}) == nil {
		_ = tx.Commit()
	}
}

func writeTokenUse(tx *sql.Tx, at string, ids map[int64]struct{}) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO org_state(key, value) VALUES(?, ?)`, tokenUseSinceKey, at); err != nil {
		return err
	}
	for id := range ids {
		if _, err := tx.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, at, id); err != nil {
			return err
		}
	}
	return nil
}

// TokenUseTrackingSince says since when token use is recorded; empty while no
// token has been used yet.
func (s *Store) TokenUseTrackingSince() string {
	if s.reader != nil {
		return s.reader.TokenUseTrackingSince()
	}
	var v string
	if s.db.QueryRow(`SELECT value FROM org_state WHERE key = ?`, tokenUseSinceKey).Scan(&v) != nil {
		return ""
	}
	return v
}

// DeviceTokenIdle is how long a device or legacy token may go unused before it
// stops working. Every use moves the end, so a token that is in use never runs out;
// one that nobody holds any more does not stay a key for ever.
const DeviceTokenIdle = 90 * 24 * time.Hour

// tokenIdleFromKey names the day the idle rule began for this database. A token
// counts as used at the latest then, so introducing the rule revokes nothing at once.
const tokenIdleFromKey = "token_idle_from"

// tokenIdleCond is the part of a token query that applies the idle rule; it needs
// the token as t and takes the earliest day a token may have been used on.
const tokenIdleCond = ` AND (t.kind NOT IN ('device','legacy') OR max(t.created_at, t.last_used_at,
	COALESCE((SELECT value FROM org_state WHERE key = '` + tokenIdleFromKey + `'), '')) > ?)`

// tokenIdleCutoff is the argument of tokenIdleCond.
func tokenIdleCutoff() string {
	return time.Now().UTC().Add(-DeviceTokenIdle).Format(time.RFC3339)
}

func ensureTokenIdle(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO org_state(key, value) VALUES(?, ?)`, tokenIdleFromKey, now())
	return err
}

// TokenIdleEnd says when a device or legacy token stops working if it is not used
// again; empty for any other token. The page of the tokens shows it.
func (s *Store) TokenIdleEnd(t TokenInfo) string {
	if s.reader != nil {
		return s.reader.TokenIdleEnd(t)
	}
	if t.Kind != "device" && t.Kind != "legacy" || t.ExpiresAt != "" || t.RevokedAt != "" {
		return ""
	}
	var from string
	_ = s.db.QueryRow(`SELECT value FROM org_state WHERE key = ?`, tokenIdleFromKey).Scan(&from)
	base := max(t.CreatedAt, t.LastUsedAt, from)
	at, err := time.Parse(time.RFC3339, base)
	if err != nil {
		return ""
	}
	return at.Add(DeviceTokenIdle).UTC().Format(time.RFC3339)
}
