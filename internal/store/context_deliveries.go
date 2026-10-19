package store

import (
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// contextDeliveryRetention is how long a session is remembered. A harness
	// session that is still being used after a week is rare, and one that is
	// resumed after that has lost most of what it was shown anyway, so being
	// shown the entry again is the right outcome rather than a repeat.
	contextDeliveryRetention = 7 * 24 * time.Hour
	// maxContextSessionKey bounds what an authenticated caller can make the
	// table store per row. Harness session ids are UUIDs; anything much longer
	// is not one.
	maxContextSessionKey = 200
	// maxContextDeliveryItems bounds one claim. A prompt delivers a handful of
	// entries and a bootstrap, which is cut to a character budget, a few dozen;
	// the cap only stops a caller from turning one request into an unbounded
	// write.
	maxContextDeliveryItems = 1024
)

// Why an entry reached a session. Kept on the row so a later question — did the
// start-up briefing already carry this, or did the word in a prompt bring it? —
// can be answered from the data.
const (
	DeliveredByBootstrap = "bootstrap"
	DeliveredByRelevance = "relevance"
	DeliveredByHint      = "hint"
)

// ContextDelivery is one thing a session was shown without asking: an entry, or
// a hint line about a word. Exactly one of the two is set.
type ContextDelivery struct {
	KnowledgeID int64
	Term        string
}

// CleanSessionKey returns the harness session id in the form the delivery
// memory stores it, or "" when it cannot be used. An empty key is how every
// caller that has no session behaves: nothing is remembered and nothing is
// suppressed, which is exactly what delivery did before it had a memory.
func CleanSessionKey(key string) string {
	if key == "" || len(key) > maxContextSessionKey || !utf8.ValidString(key) {
		return ""
	}
	for _, r := range key {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ""
		}
	}
	return key
}

// ClaimContextDeliveries records that these items are being delivered to the
// session and returns the ones that were new to it. The rest were delivered
// before and must not be delivered again.
//
// Claiming and recording are one step on purpose. Asking first and recording
// after would let two prompts of one session, handled at the same moment, both
// be told an entry is new. The writer runs claims one after another, so of any
// number of concurrent callers exactly one gets each item.
//
// Without a usable session key nothing is written and every item counts as new:
// a caller that cannot say which session it is in gets the behaviour it had
// before there was a memory.
func (s *Store) ClaimContextDeliveries(account, session, via string, items []ContextDelivery) ([]ContextDelivery, error) {
	session = CleanSessionKey(session)
	if session == "" || len(items) == 0 {
		return items, nil
	}
	if len(items) > maxContextDeliveryItems {
		items = items[:maxContextDeliveryItems]
	}
	if s.writer != nil {
		return queueValue(s, []any{account, session, via, items}, func(d *Store, p []any) ([]ContextDelivery, error) {
			return d.ClaimContextDeliveries(p[0].(string), p[1].(string), p[2].(string), p[3].([]ContextDelivery))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	at := now()
	var fresh []ContextDelivery
	for _, item := range items {
		res, err := tx.Exec(`INSERT OR IGNORE INTO context_deliveries(account_id, session_key, knowledge_id, term, via, delivered_at)
			VALUES(?,?,?,?,?,?)`, account, session, item.KnowledgeID, item.Term, via, at)
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			fresh = append(fresh, item)
		}
	}
	// Ageing out happens where the rows are written, so no maintenance job has
	// to exist for the table to stay bounded. The range sits on an index and
	// holds only what expired since the previous claim.
	cutoff := time.Now().UTC().Add(-contextDeliveryRetention).Format(time.RFC3339)
	if _, err := tx.Exec(`DELETE FROM context_deliveries WHERE delivered_at < ?`, cutoff); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return fresh, nil
}

// deliveredToSessionSQL is the condition, on a knowledge row aliased k, that the
// session was already shown the entry. Callers add it as NOT EXISTS so a
// delivered entry does not take one of the few slots a prompt may fill.
const deliveredToSessionSQL = `EXISTS (SELECT 1 FROM context_deliveries d
	WHERE d.account_id = ? AND d.session_key = ? AND d.knowledge_id = k.id)`

// hintedTerms returns which of the words already had a hint line in the session.
func (s *Store) hintedTerms(account, session string, terms []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, term := range terms {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_deliveries
			WHERE account_id = ? AND session_key = ? AND knowledge_id = 0 AND term = ?`, account, session, term).Scan(&n); err != nil {
			return nil, err
		}
		out[term] = n > 0
	}
	return out, nil
}
