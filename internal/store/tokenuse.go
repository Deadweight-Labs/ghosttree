package store

import (
	"database/sql"
	"time"
)

// tokenTouchEvery is how often at most a token's last use is written. A token
// that calls every second costs one write per interval, not one per call.
const tokenTouchEvery = 10 * time.Minute

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
