package store

import (
	"database/sql"
	"strings"
)

// The second gate of relevance delivery asks a different question from the
// first. The first asks whether a word is rare among the knowledge entries,
// which says what the archive is about. That is not the same as the word being
// unusual: "dinge" is in few entries because nobody writes a pitfall about
// "things", and a delivery that fires on it hands a stranger's entry to a
// sentence that only said "what is the state of things". How common a word is
// in the language the reader actually uses is not a property of what was filed,
// so it is measured on what the reader has said and read: the session text
// collected from the reader's own sessions.
//
// No outside word list stands in for it. A list ships with a language and a
// licence, and the reader's German and English come with a vocabulary of their
// own. The words that are ordinary here are the ones this reader keeps
// repeating, and counting them costs a bounded number of indexed lookups.
const (
	// ownChunkShare is the share of the reader's own sampled chunks a word may
	// appear in and still count as unusual. One in two thousand: twenty chunks in
	// a full sample of forty thousand. An ordinary word of either language is in
	// a percent of them, twenty times above it; a codename that came up in a few
	// sessions is below it. Words in between are the reader's own vocabulary,
	// and a vocabulary is not what a sentence is about when it merely uses it.
	ownChunkShare = 0.0005
	// minOwnCeiling keeps the ceiling from collapsing on a small sample, where
	// the share of a few thousand chunks is less than one.
	minOwnCeiling = 8
	// minOwnChunks is how much text of their own an account needs before the
	// gate says anything. A word cannot be unusual in a handful of sentences, so
	// a new account behaves as delivery did before the gate existed.
	minOwnChunks = 5000
	// maxLanguageSessions and languageSampleChunks bound the sample: the reader's
	// most recent sessions, as many as it takes to reach the chunk target and
	// never more than the session cap. Each session costs one indexed lookup per
	// word that needs counting, so the cap is what keeps a prompt of many such
	// words cheap however many sessions an account has accumulated. Twenty-four
	// sessions are a few weeks of work: recent enough to be the reader's present
	// language, long enough that one project's vocabulary does not stand for all
	// of it.
	maxLanguageSessions  = 24
	languageSampleChunks = 40000
)

// languageGate decides, per word, whether it is unusual in the reader's own
// session text. It is a gate and nothing else: it compares a count with a
// ceiling, never returns text or the count itself, and a word that fails it is
// dropped without a trace.
//
// It counts the reader's own chunks and nothing else, on purpose. A count over
// every account's text would be an oracle about other people's sessions: a
// prompt typed to test one word would show, by whether a delivery appeared or
// was held back, whether that word is frequent in sessions its author may not
// open. Counted over the reader's own chunks the answer depends on nothing the
// reader could not read, and it is the better measure besides — "dinge" is a
// word this reader says all the time, "hlpvpngw" is not.
//
// The count is taken per session, over the span of chunk ids the session
// occupies, and a chunk counts only if it belongs to that very session. The
// full-text index is not asked about anyone else's chunks as a matter of
// result; chunks of other accounts that happen to sit between two of the
// reader's own are read past and never counted.
//
// What this costs is bounded by the sample and not by the archive: at most
// maxLanguageSessions lookups for a word, and none at all for a word that is
// rare everywhere (see unusual). Nothing here caps the count to save time in a
// way that could change a verdict. A cap on how many index entries are read
// would be exceeded sooner by a word that other accounts use often, so it
// would let the verdict follow their usage — the oracle this gate exists to not
// be. What remains is the time itself, which grows with the chunks of other
// accounts inside the reader's spans. That is the same kind of residue as the
// timing of session search (knowledge #2483), accepted for the same reason: it
// is a duration, with no content, no identifier and no count in it.
type languageGate struct {
	s       *Store
	account int64
	owner   int64
	// prepared is set after the sample was drawn. on is false while the account
	// has too little text of its own, or no way to be told apart (no account).
	prepared bool
	on       bool
	ceiling  int
	spans    []sessionSpan
}

// sessionSpan is where one session's chunks sit in the full-text index: every
// chunk of the session has an id between lo and hi, together with whatever
// other sessions wrote in between.
type sessionSpan struct {
	session int64
	lo, hi  int64
}

// newLanguageGate prepares a gate for the account named by its principal id
// ("person:12"). Nothing is read until a word needs checking, so a prompt with
// no word that reaches the gate costs nothing here.
func (s *Store) newLanguageGate(principal string) *languageGate {
	account, ok := accountNumericID(principal)
	return &languageGate{s: s, account: account, prepared: !ok}
}

// prepare draws the sample: the account's most recent sessions, newest first,
// until there is enough text or the session cap is reached. Everything it reads
// is the account's own rows. Sessions without an account of their own (legacy
// rows with account 0) belong to the instance owner, as everywhere else.
func (g *languageGate) prepare() error {
	g.prepared = true
	g.owner = instanceOwnerID(g.s.db)
	rows, err := g.s.db.Query(`SELECT id FROM sessions
		WHERE account_id = ? OR (account_id = 0 AND ? = ?)
		ORDER BY last_seen_at DESC, id DESC LIMIT ?`, g.account, g.account, g.owner, maxLanguageSessions)
	if err != nil {
		return err
	}
	var sessions []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	total := 0
	for _, id := range sessions {
		span, size, ok, err := g.s.sessionSpanOf(id)
		if err != nil {
			if missingTable(err) {
				return nil
			}
			return err
		}
		if !ok {
			continue
		}
		g.spans = append(g.spans, span)
		if total += size; total >= languageSampleChunks {
			break
		}
	}
	if total < minOwnChunks {
		return nil
	}
	g.on, g.ceiling = true, max(minOwnCeiling, int(float64(total)*ownChunkShare))
	return nil
}

// sessionSpanOf reads where a session's chunks sit and how many there are from
// the two ends of its (session, seq) index: two lookups, whatever its length.
// The count is the last sequence number plus one, which is what the collector
// writes; it is a size for the ceiling, not a figure anyone reads.
func (s *Store) sessionSpanOf(session int64) (span sessionSpan, size int, ok bool, err error) {
	var firstID, lastID, lastSeq int64
	err = s.db.QueryRow(`SELECT id FROM session_chunks WHERE session_id = ? ORDER BY seq LIMIT 1`, session).Scan(&firstID)
	if err == sql.ErrNoRows {
		return span, 0, false, nil
	}
	if err != nil {
		return span, 0, false, err
	}
	if err = s.db.QueryRow(`SELECT id, seq FROM session_chunks WHERE session_id = ? ORDER BY seq DESC LIMIT 1`, session).Scan(&lastID, &lastSeq); err != nil {
		return span, 0, false, err
	}
	span = sessionSpan{session: session, lo: min(firstID, lastID), hi: max(firstID, lastID)}
	return span, int(lastSeq) + 1, true, nil
}

// unusual says whether few enough of the reader's own chunks carry the word. A
// word that is everywhere in the reader's sessions is ruled out as soon as the
// ceiling is passed, which costs a few sessions' worth of lookups however large
// the archive is. Only the verdict leaves this function: the count is never
// returned, ordered by or shown.
func (g *languageGate) unusual(term string) (bool, error) {
	if !g.prepared {
		if err := g.prepare(); err != nil {
			return false, err
		}
	}
	if !g.on {
		return true, nil
	}
	phrase := `"` + term + `"`
	// A word that few chunks carry anywhere cannot be common in the reader's
	// own: the reader's chunks are among them. This settles the words that
	// matter here — a codename, a typo, a rare product name — with one short
	// look at the index, and it settles them exactly as counting the reader's
	// chunks would. It is a shortcut to the same verdict, not a second criterion:
	// no word is ever judged by what the others say, only spared the count.
	var anywhere int
	err := g.s.db.QueryRow(`SELECT count(*) FROM (SELECT 1 FROM chunks_fts WHERE chunks_fts MATCH ? LIMIT ?)`,
		phrase, g.ceiling+1).Scan(&anywhere)
	if err != nil {
		if missingTable(err) {
			return true, nil
		}
		return false, err
	}
	if anywhere <= g.ceiling {
		return true, nil
	}
	seen := 0
	for _, span := range g.spans {
		// The chunk must belong to this session: the span can enclose chunks of
		// other accounts, and those are read past, never counted. The limit
		// counts matching own chunks only, so what lies between cannot move it.
		var n int
		err := g.s.db.QueryRow(`SELECT count(*) FROM (
			SELECT 1 FROM chunks_fts CROSS JOIN session_chunks c ON c.id = chunks_fts.rowid
			WHERE chunks_fts MATCH ? AND chunks_fts.rowid BETWEEN ? AND ? AND c.session_id = ? LIMIT ?)`,
			phrase, span.lo, span.hi, span.session, g.ceiling+1-seen).Scan(&n)
		if err != nil {
			if missingTable(err) {
				return true, nil
			}
			return false, err
		}
		if seen += n; seen > g.ceiling {
			return false, nil
		}
	}
	return true, nil
}

func missingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
