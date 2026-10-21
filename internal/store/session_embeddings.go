package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// The vectors of the sessions: one per text that says what a session was about,
// the prompts a person typed and the answer each turn ended with (see
// transcript.PickTurnTexts), not the tool calls, their results and the messages
// in between. The job embeds them one chunk at a time, in its window at night.
//
// A session needs a look when it has a chunk it was not read with, when its mark
// was made under another model, text format or version of the rules, or when it
// has never been looked at (session_embedding_sync, see the schema). A look picks
// the texts, makes the document of each from the stored line, and compares its
// hash with the vector that is there. What comes out is EmbedDue like for the other
// kinds, with the id of a chunk as the id of an item; Unchanged is different: it
// holds sessions, the ones that were found complete; and Stale names the vectors
// that have no text any more. The look only reads (the reader of a runtime store
// answers it); the writer stores, marks and drops.

// SessionSettle is how long a session has to be quiet before the answer that ends
// its last turn counts as final. A turn is closed when a prompt follows it; the
// last one of a session has no such end, and while an agent works it writes a
// message every few minutes, each of which is the last until the next one comes.
// Embedding them would spend three seconds of the embedding server on every
// message of a running session and keep a vector for a text that is not the
// answer. An agent that is working uploads at least every few minutes, even
// during a long build or while it waits for a review; half an hour of silence is
// a person who has gone, or a session that is over.
const SessionSettle = 30 * time.Minute

var (
	sessionPlaceholderRe = regexp.MustCompile(`\[ghosttree: [^\]]{0,300}\]`)
	sessionImageMarkRe   = regexp.MustCompile(`\[Image(?: #\d+)?(?:: [^\]]{0,300})?\]`)
	sessionBlankRunRe    = regexp.MustCompile(`\n{3,}`)
	sessionSpaceRunRe    = regexp.MustCompile(`[ \t]{2,}`)
	sessionSpanRes       = func() []*regexp.Regexp {
		var out []*regexp.Regexp
		for _, tag := range []string{"system-reminder", "ide_opened_file", "ide_selection", "local-command-caveat",
			"local-command-stdout", "command-name", "command-message", "command-args", "user-prompt-submit-hook"} {
			out = append(out, regexp.MustCompile(`(?s)<`+regexp.QuoteMeta(tag)+`>.*?</`+regexp.QuoteMeta(tag)+`>`))
		}
		return out
	}()
)

// sessionDocument is the text of one chunk as the embedding server gets it, and
// false for a chunk that has nothing worth embedding. It is made from the stored
// line, by the way every text takes into the database: the images are replaced
// (blobstrip), secrets are redacted in the line and in the text (redact), the
// placeholders and image marks that are left are removed, and so are the blocks
// a harness puts into a prompt. A text that this would change was changed on
// arrival already; the chain is a second lock, for what reaches the database
// another way, and what a later version of the rules finds. Then the text is cut
// and given its prefix like every document. Its hash is the hash that decides
// whether a vector is current.
func sessionDocument(harness string, seq int, raw string) (string, bool) {
	t := cleanSessionText(harness, seq, raw)
	if utf8.RuneCountInString(t) < embed.MinSessionTextRunes {
		return "", false
	}
	return embed.SessionText(t), true
}

// cleanSessionText is the text of a chunk after the chain of sessionDocument,
// before its length is judged and before it is cut. A hit that shows the text
// of a session found by meaning shows this one.
func cleanSessionText(harness string, seq int, raw string) string {
	line, _ := blobstrip.Strip(raw)
	line = redact.RedactJSON(line)
	t := redact.Redact(transcript.Fields(transcript.Parse(harness, seq, line)).Text)
	t = sessionPlaceholderRe.ReplaceAllString(t, "")
	t = sessionImageMarkRe.ReplaceAllString(t, "")
	for _, re := range sessionSpanRes {
		t = re.ReplaceAllString(t, "")
	}
	t = strings.ReplaceAll(t, "\r\n", "\n")
	t = sessionSpaceRunRe.ReplaceAllString(t, " ")
	t = sessionBlankRunRe.ReplaceAllString(t, "\n\n")
	return strings.TrimSpace(strings.ToValidUTF8(t, "�"))
}

// sessionRef is a session as the job looks at it.
type sessionRef struct {
	id       int64
	harness  string
	lastSeen string
}

// sessionPicked reads the chunk index rows of a session that can carry a text and
// picks its texts. The text of a row is read only where the selection looks at it,
// for the prompts: the answers are not read here, and nor are the 8 KB of a tool
// call.
func (s *Store) sessionPicked(id int64) ([]transcript.Picked, error) {
	rows, err := s.db.Query(`SELECT chunk_id, seq, role, is_prompt, tool_calls, CASE WHEN role = 'user' AND is_prompt = 1 THEN text ELSE '' END
		FROM chunk_index WHERE session_id = ? AND (role != '' OR tool_calls > 0) ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var in []transcript.TurnRow
	for rows.Next() {
		var r transcript.TurnRow
		var prompt int
		if err := rows.Scan(&r.Chunk, &r.Seq, &r.Role, &prompt, &r.ToolCalls, &r.Text); err != nil {
			return nil, err
		}
		r.Prompt = prompt == 1
		in = append(in, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return transcript.PickTurnTexts(in), nil
}

// settledPicks drops the answer of the open turn while the session has not been
// quiet for SessionSettle, and says so.
func settledPicks(picks []transcript.Picked, lastSeen string, at time.Time) (wanted []transcript.Picked, waiting bool) {
	settled := true
	if t, err := time.Parse(time.RFC3339, lastSeen); err == nil {
		settled = at.Sub(t) >= SessionSettle
	}
	for _, p := range picks {
		if p.Open && !settled {
			waiting = true
			continue
		}
		wanted = append(wanted, p)
	}
	return wanted, waiting
}

// chunkRaws reads the stored lines of chunks.
func (s *Store) chunkRaws(ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	err := s.eachChunk(ids, func(part []any) error {
		rows, err := s.db.Query(`SELECT id, raw FROM session_chunks WHERE id IN (`+placeholders(len(part))+`)`, part...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				return err
			}
			out[id] = raw
		}
		return rows.Err()
	})
	return out, err
}

type storedVector struct{ model, prefix, hash string }

// sessionVectors lists the vectors of the chunks of a session that the index
// knows. A vector of a chunk without a row in the index says nothing that the
// look can judge, and is left alone.
func (s *Store) sessionVectors(session int64) (map[int64]storedVector, error) {
	rows, err := s.db.Query(`SELECT e.chunk_id, e.model, e.prefix_version, e.text_hash
		FROM session_embeddings e JOIN session_chunks c ON c.id = e.chunk_id
		JOIN chunk_index ci ON ci.chunk_id = c.id WHERE c.session_id = ?`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]storedVector{}
	for rows.Next() {
		var id int64
		var v storedVector
		if err := rows.Scan(&id, &v.model, &v.prefix, &v.hash); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

// sessionFailures lists the parked texts of the chunks of a session that the
// index knows.
func (s *Store) sessionFailures(session int64) (map[int64]parkedFailure, error) {
	rows, err := s.db.Query(`SELECT f.chunk_id, f.model, f.prefix_version, f.text_hash, f.attempts, f.permanent, f.retry_at
		FROM session_embedding_failures f JOIN session_chunks c ON c.id = f.chunk_id
		JOIN chunk_index ci ON ci.chunk_id = c.id WHERE c.session_id = ?`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]parkedFailure{}
	for rows.Next() {
		var id int64
		var f parkedFailure
		var permanent int
		if err := rows.Scan(&id, &f.model, &f.prefix, &f.hash, &f.attempts, &permanent, &f.retryAt); err != nil {
			return nil, err
		}
		f.permanent = permanent != 0
		out[id] = f
	}
	return out, rows.Err()
}

// sessionLook is what a look at a session found.
type sessionLook struct {
	// items are the texts to embed.
	items []EmbedItem
	// waiting: the session is not complete yet and has to be looked at again. Its
	// last answer has not stood long enough, or a text of it is parked for a
	// time. A text the embedding server refuses for good is not waited for: it
	// stays as it is until the text, the model or the rules change, and each of
	// those brings the session back (a redaction drops the failure and the mark of
	// the session, a new model or a new version of the rules is in the mark).
	waiting bool
	// stale are the chunks whose vector or parked failure has no text any more: the
	// chunk is no prompt and no last answer of a turn (the answer a session
	// outgrew, a prompt that the rules now take for injected), or its text is now
	// too short or empty. The job drops them.
	stale []int64
}

// lookAtSession says what is due for a session and what is left of the texts it
// had.
func (s *Store) lookAtSession(ref sessionRef, m EmbedModel, at time.Time) (sessionLook, error) {
	var look sessionLook
	picks, err := s.sessionPicked(ref.id)
	if err != nil {
		return look, err
	}
	wanted, waiting := settledPicks(picks, ref.lastSeen, at)
	look.waiting = waiting
	stored, err := s.sessionVectors(ref.id)
	if err != nil {
		return look, err
	}
	failures, err := s.sessionFailures(ref.id)
	if err != nil {
		return look, err
	}
	// What is kept: the vector of a chunk that is still picked, whether or not the
	// session lets it count yet (the answer of an open turn that the session
	// touched again is not lost for that).
	picked := make(map[int64]bool, len(picks))
	for _, p := range picks {
		picked[p.Chunk] = true
	}
	stale := map[int64]bool{}
	for id := range stored {
		stale[id] = !picked[id]
	}
	for id := range failures {
		stale[id] = stale[id] || !picked[id]
	}
	if len(wanted) > 0 {
		ids := make([]int64, len(wanted))
		for i, p := range wanted {
			ids[i] = p.Chunk
		}
		raws, err := s.chunkRaws(ids)
		if err != nil {
			return look, err
		}
		stamp := at.UTC().Format(time.RFC3339)
		for _, p := range wanted {
			raw, ok := raws[p.Chunk]
			if !ok {
				continue
			}
			doc, ok := sessionDocument(ref.harness, p.Seq, raw)
			if !ok {
				// Nothing to embed any more, and what was made of it goes.
				stale[p.Chunk] = true
				continue
			}
			hash := embed.TextHash(doc)
			if v, ok := stored[p.Chunk]; ok && v.model == m.Model && v.prefix == m.PrefixVersion && v.hash == hash {
				continue
			}
			attempts := 0
			if f, ok := failures[p.Chunk]; ok && f.model == m.Model && f.prefix == m.PrefixVersion && f.hash == hash {
				if !f.permanent && f.retryAt > stamp {
					look.waiting = true
				}
				if f.permanent || f.retryAt > stamp {
					continue
				}
				attempts = f.attempts
			}
			look.items = append(look.items, EmbedItem{ID: p.Chunk, Text: doc, Hash: hash, Attempts: attempts})
		}
	}
	for id, gone := range stale {
		if gone {
			look.stale = append(look.stale, id)
		}
	}
	slices.Sort(look.stale)
	return look, nil
}

// sessionTextVersion is the version of the rules that make the texts of a session:
// which prompts are typed (transcript.ClassifyPrompt), which chunk is the last
// answer of a turn (transcript.PickTurnTexts), how the text is cleaned
// (cleanSessionText, sessionDocument) and cut. It is kept in the mark of a
// session that was found complete. Change it with any of those rules: the sessions
// that were marked under the old rules are looked at again, which makes the
// vectors that the new rules do not call for go (and, for a changed text, be made
// again), and no session stays as the old rules left it. A new model or a new
// text format does the same on its own. It is a variable only so that a test can
// change it.
var sessionTextVersion = 1

// sessionCandidates are the sessions that need a look, newest first, so that what
// was written last is searchable first while the stock is still being caught up
// on: the ones that were never looked at, that have a chunk since the look
// (last_seen_at reaches read_at: same-second changes count, as for the other
// kinds), and the ones whose mark was made under another model, text format or
// version of the rules. A session without a message in the index has nothing to
// look at.
func (s *Store) sessionCandidates(m EmbedModel) ([]sessionRef, error) {
	rows, err := s.db.Query(`SELECT se.id, se.harness, se.last_seen_at FROM sessions se
		LEFT JOIN session_embedding_sync y ON y.session_id = se.id
		WHERE EXISTS (SELECT 1 FROM chunk_index ci WHERE ci.session_id = se.id AND ci.role != '')
		  AND (y.session_id IS NULL OR se.last_seen_at >= y.read_at
		       OR y.version != ? OR y.model != ? OR y.prefix_version != ?)
		ORDER BY se.last_seen_at DESC, se.id DESC`, sessionTextVersion, m.Model, m.PrefixVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionRef
	for rows.Next() {
		var r sessionRef
		if err := rows.Scan(&r.id, &r.harness, &r.lastSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sessionEmbeddingDue is EmbeddingDue for sessions. It looks at the sessions that
// need it, newest first, until it has limit texts. A session that turns out to be
// complete is reported in Unchanged, and the job records it with
// TouchSessionEmbeddings so that it is not looked at again; the vectors that the
// look found without a text are reported in Stale, and the job drops them. It
// only reads: the reader of a runtime store can answer it. While the index of the
// chunks is still being built nothing is due: a session whose chunks are not all
// in it would be taken for complete.
func (s *Store) sessionEmbeddingDue(m EmbedModel, limit int, at time.Time) (EmbedDue, error) {
	due := EmbedDue{ReadAt: now()}
	if !s.IndexProgress().Done {
		return due, nil
	}
	cands, err := s.sessionCandidates(m)
	if err != nil {
		return due, err
	}
	for _, c := range cands {
		if len(due.Items) >= limit {
			break
		}
		look, err := s.lookAtSession(c, m, at)
		if err != nil {
			return due, err
		}
		due.Stale = append(due.Stale, look.stale...)
		if len(look.items) == 0 && !look.waiting {
			due.Unchanged = append(due.Unchanged, c.id)
			continue
		}
		due.Items = append(due.Items, look.items[:min(len(look.items), limit-len(due.Items))]...)
	}
	return due, nil
}

// TouchSessionEmbeddings records that these sessions were looked at as of readAt
// (EmbedDue.ReadAt) with the model and text format m and found complete. The
// mark also holds the version of the rules that make the texts, so that the
// session is looked at again when any of the three changes.
func (s *Store) TouchSessionEmbeddings(m EmbedModel, ids []int64, readAt string) error {
	if s.writer != nil {
		return queueWrite(s, []any{m, ids, readAt}, func(d *Store, p []any) error {
			return d.TouchSessionEmbeddings(p[0].(EmbedModel), p[1].([]int64), p[2].(string))
		})
	}
	if len(ids) == 0 || readAt == "" {
		return nil
	}
	return s.eachChunk(ids, func(part []any) error {
		_, err := s.db.Exec(`INSERT INTO session_embedding_sync(session_id, read_at, version, model, prefix_version)
			SELECT id, ?, ?, ?, ? FROM sessions WHERE id IN (`+placeholders(len(part))+`)
			ON CONFLICT(session_id) DO UPDATE SET read_at = excluded.read_at, version = excluded.version,
				model = excluded.model, prefix_version = excluded.prefix_version`,
			append([]any{readAt, sessionTextVersion, m.Model, m.PrefixVersion}, part...)...)
		return err
	})
}

// DropEmbeddings removes the vectors and the parked failures of objects of a
// kind, by id: what EmbedDue.Stale names. The ids of sessions are chunks. It
// returns the number of vectors removed. The statements are small and the writer
// gets a turn between them: the job does not hold it for the whole of a stock.
func (s *Store) DropEmbeddings(kind string, ids []int64) (int, error) {
	if s.writer != nil {
		removed := 0
		for start := 0; start < len(ids); start += dropBatch {
			part := ids[start:min(start+dropBatch, len(ids))]
			n, err := queueValue(s, []any{kind, part}, func(d *Store, p []any) (int, error) {
				return d.DropEmbeddings(p[0].(string), p[1].([]int64))
			})
			removed += n
			if err != nil {
				return removed, err
			}
		}
		return removed, nil
	}
	k, err := embedKindOf(kind)
	if err != nil {
		return 0, err
	}
	removed := 0
	err = s.eachChunk(ids, func(part []any) error {
		res, err := s.db.Exec(`DELETE FROM `+k.emb+` WHERE `+k.keyWhere(k.emb)+k.idCol+` IN (`+placeholders(len(part))+`)`, part...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		removed += int(n)
		_, err = s.db.Exec(`DELETE FROM `+k.fail+` WHERE `+k.keyWhere(k.fail)+k.idCol+` IN (`+placeholders(len(part))+`)`, part...)
		return err
	})
	return removed, err
}

// dropBatch is how many objects one writer job of DropEmbeddings removes.
const dropBatch = 200

// sessionEmbeddingStatus counts the texts of the sessions that can be embedded
// now (the answer of a session that is still going does not count yet), and how
// many of them have a vector of the configured model and text format. It counts
// by chunk and does not read the text of a chunk that has such a vector, so a
// text that changed is not seen here, only by the job; the text of the others is
// read, to leave out what is too short to be embedded. Like the other kinds it
// reads what it needs and is meant for a command.
func (s *Store) sessionEmbeddingStatus(k embedKind, m EmbedModel) (EmbedStatus, error) {
	var st EmbedStatus
	stored := map[int64]storedVector{}
	rows, err := s.db.Query(`SELECT chunk_id, model, prefix_version, text_hash FROM session_embeddings`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var id int64
		var v storedVector
		if err := rows.Scan(&id, &v.model, &v.prefix, &v.hash); err != nil {
			rows.Close()
			return st, err
		}
		stored[id] = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return st, err
	}
	failed := map[int64]parkedFailure{}
	frows, err := s.db.Query(`SELECT chunk_id, model, prefix_version, text_hash, attempts, permanent, retry_at FROM session_embedding_failures`)
	if err != nil {
		return st, err
	}
	for frows.Next() {
		var id int64
		var f parkedFailure
		var permanent int
		if err := frows.Scan(&id, &f.model, &f.prefix, &f.hash, &f.attempts, &permanent, &f.retryAt); err != nil {
			frows.Close()
			return st, err
		}
		f.permanent = permanent != 0
		failed[id] = f
	}
	err = frows.Err()
	frows.Close()
	if err != nil {
		return st, err
	}
	srows, err := s.db.Query(`SELECT id, harness, last_seen_at FROM sessions se
		WHERE EXISTS (SELECT 1 FROM chunk_index ci WHERE ci.session_id = se.id AND ci.role != '') ORDER BY id`)
	if err != nil {
		return st, err
	}
	var sessions []sessionRef
	for srows.Next() {
		var r sessionRef
		if err := srows.Scan(&r.id, &r.harness, &r.lastSeen); err != nil {
			srows.Close()
			return st, err
		}
		sessions = append(sessions, r)
	}
	err = srows.Err()
	srows.Close()
	if err != nil {
		return st, err
	}
	at := time.Now()
	stamp := at.UTC().Format(time.RFC3339)
	for _, ref := range sessions {
		picks, err := s.sessionPicked(ref.id)
		if err != nil {
			return st, err
		}
		wanted, _ := settledPicks(picks, ref.lastSeen, at)
		for _, p := range wanted {
			v, has := stored[p.Chunk]
			if has && v.model == m.Model && v.prefix == m.PrefixVersion {
				st.Active++
				st.Current++
				continue
			}
			raws, err := s.chunkRaws([]int64{p.Chunk})
			if err != nil {
				return st, err
			}
			doc, ok := sessionDocument(ref.harness, p.Seq, raws[p.Chunk])
			if !ok {
				continue
			}
			st.Active++
			if has {
				st.OtherModel++
			}
			if f, ok := failed[p.Chunk]; ok && f.model == m.Model && f.prefix == m.PrefixVersion && f.hash == embed.TextHash(doc) &&
				(f.permanent || f.retryAt > stamp) {
				st.Parked++
			}
		}
	}
	st.Due = st.Active - st.Current
	err = s.db.QueryRow(`SELECT last_ok_at,last_error,last_error_at,failures FROM knowledge_embedding_state WHERE id=1`).
		Scan(&st.Job.LastOKAt, &st.Job.LastError, &st.Job.LastErrorAt, &st.Job.Failures)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return st, err
}

// purgeSessionEmbeddings removes the vectors, failures and marks of chunks and
// sessions that are gone: what a database written without foreign keys left
// behind (with them the cascade has done it). Three statements on keys, no look at
// any session, so that it is quick in the writer. The vectors of chunks that have
// no text any more (the answer a session outgrew, a text that is now too short, a
// prompt the rules now take for injected) are not found here: the look at a
// session finds them (EmbedDue.Stale), and only sessions that changed are looked
// at. It returns the number of vectors removed.
func (s *Store) purgeSessionEmbeddings() (int, error) {
	removed := 0
	res, err := s.db.Exec(`DELETE FROM session_embeddings WHERE chunk_id NOT IN (SELECT id FROM session_chunks)`)
	if err != nil {
		return removed, err
	}
	n, _ := res.RowsAffected()
	removed += int(n)
	if _, err := s.db.Exec(`DELETE FROM session_embedding_failures WHERE chunk_id NOT IN (SELECT id FROM session_chunks)`); err != nil {
		return removed, err
	}
	if _, err := s.db.Exec(`DELETE FROM session_embedding_sync WHERE session_id NOT IN (SELECT id FROM sessions)`); err != nil {
		return removed, err
	}
	return removed, nil
}

// verifySessionVector says whether a vector with the hash of a text is still the
// vector of the chunk: the chunk exists, its stored line still gives a text that
// is long enough, and that text has the hash. It is called inside the transaction
// that would store the vector, and first takes the write lock (a statement that
// writes nothing does), so that the line it reads is the one that stays until the
// vector is in. A vector that fails the test is not stored: it was made from a
// text that a redaction, a removal of an image or a change of the rules has since
// replaced, and the job makes the vector of the text that is there.
func verifySessionVector(tx *sql.Tx, chunk int64, hash string) (bool, error) {
	if _, err := tx.Exec(`UPDATE session_embedding_sync SET read_at = read_at WHERE session_id = -1`); err != nil {
		return false, err
	}
	var harness, raw string
	var seq int
	err := tx.QueryRow(`SELECT se.harness, c.seq, c.raw FROM session_chunks c JOIN sessions se ON se.id = c.session_id WHERE c.id = ?`, chunk).
		Scan(&harness, &seq, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc, ok := sessionDocument(harness, seq, raw)
	return ok && embed.TextHash(doc) == hash, nil
}

// hasSessionEmbeddingTables says whether a database has the tables of the session
// vectors. The maintenance commands open a database without the migrations, so
// the tables of a database that no server has started with this version are
// missing, and there is nothing to drop.
func hasSessionEmbeddingTables(ctx context.Context, q maintQueryer) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name IN ('session_embeddings','session_embedding_failures','session_embedding_sync')`).Scan(&n)
	return n == 3, err
}

// dropSessionEmbeddings removes the vector and the failure of one chunk, in the
// transaction of the caller, and the mark "looked at" of its session when there
// was one, and returns the number of vectors it removed (with write false it only
// counts them). ctx redact-sessions and ctx strip-blobs call it for every chunk
// they change: a vector made from the text before the change was derived from
// what the change was meant to remove, and the job makes a new one from the text
// as it is now. The session is not looked at again unless its mark goes with it.
func dropSessionEmbeddings(ctx context.Context, q maintQueryer, chunk int64, write bool) (int64, error) {
	if !write {
		var n int64
		err := q.QueryRowContext(ctx, `SELECT count(*) FROM session_embeddings WHERE chunk_id = ?`, chunk).Scan(&n)
		return n, err
	}
	res, err := q.ExecContext(ctx, `DELETE FROM session_embeddings WHERE chunk_id = ?`, chunk)
	if err != nil {
		return 0, err
	}
	vectors, _ := res.RowsAffected()
	fres, err := q.ExecContext(ctx, `DELETE FROM session_embedding_failures WHERE chunk_id = ?`, chunk)
	if err != nil {
		return vectors, err
	}
	failures, _ := fres.RowsAffected()
	if vectors+failures > 0 {
		_, err = q.ExecContext(ctx, `DELETE FROM session_embedding_sync WHERE session_id = (SELECT session_id FROM session_chunks WHERE id = ?)`, chunk)
	}
	return vectors, err
}
