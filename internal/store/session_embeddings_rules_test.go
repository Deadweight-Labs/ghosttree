package store

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

func storeVector(t *testing.T, st *Store, c *embed.Client, item EmbedItem, readAt string) error {
	t.Helper()
	vecs, err := c.Documents(context.Background(), []string{item.Text})
	if err != nil {
		t.Fatal(err)
	}
	q, scale, err := embed.Quantize(vecs[0])
	if err != nil {
		t.Fatal(err)
	}
	return st.UpsertEmbedding(Embedding{Kind: KindSession, ObjectID: item.ID, Model: c.Model(), PrefixVersion: c.PrefixVersion(),
		TextHash: item.Hash, Dim: len(q), Scale: float64(scale), Vec: q, ReadAt: readAt, Source: item.Source})
}

func setRaw(t *testing.T, st *Store, session int64, seq int, raw string) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE session_chunks SET raw = ? WHERE session_id = ? AND seq = ?`, raw, session, seq); err != nil {
		t.Fatal(err)
	}
}

func hashOfChunk(t *testing.T, st *Store, chunk int64) string {
	t.Helper()
	var hash string
	if err := st.db.QueryRow(`SELECT text_hash FROM session_embeddings WHERE chunk_id = ?`, chunk).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}

// ---------------------------------------------------------------- the text changes under the job

// The job reads a chunk, the text is redacted meanwhile (the redaction finds no
// vector yet to drop), and the vector of the text before arrives. It is not stored:
// the text is made again in the transaction that would store it, and the vector
// goes only with the hash of that text.
func TestSessionVectorOfATextThatChangedWhileItWasEmbeddedIsNotStored(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "please keep the value Kakadu77Fake in mind for the s1 job"),
		promptLine(1, "and a second prompt that stays as it is, s2 s3"),
	))
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 2 {
		t.Fatalf("%+v %v", due, err)
	}
	old := due.Items[0]

	// The redaction comes after the read. The new text is another text.
	setRaw(t, st, s.ID, 0, promptLine(0, "please keep the value [REDACTED:literal] in mind for the s1 job"))
	if err := storeVector(t, st, c, old, due.ReadAt); err != nil {
		t.Fatal(err)
	}
	if n := sessionVectors(t, st); n != 0 {
		t.Fatalf("the vector of the text before the redaction was stored: %d", n)
	}
	// The unchanged text of the same read goes in as it always did.
	if err := storeVector(t, st, c, due.Items[1], due.ReadAt); err != nil {
		t.Fatal(err)
	}
	if n := sessionVectors(t, st); n != 1 {
		t.Fatalf("%d vectors", n)
	}
	// The look makes the vector of the text that is there.
	again, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(again.Items) != 1 || again.Items[0].Hash == old.Hash || again.Items[0].ID != old.ID {
		t.Fatalf("%+v %v", again, err)
	}
	if err := storeVector(t, st, c, again.Items[0], again.ReadAt); err != nil {
		t.Fatal(err)
	}
	if got := hashOfChunk(t, st, old.ID); got != again.Items[0].Hash {
		t.Fatalf("the stored hash is %s", got)
	}
}

// The text that is left after the redaction is too short to be embedded: the
// vector of the old one must not stay for good (the look skips a text that is too
// short, the purge keeps a chunk that is a prompt, the status would count it as
// current).
func TestSessionVectorOfATextThatBecameTooShortWhileItWasEmbeddedIsNotStored(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(promptLine(0, "Kakadu77Fake Kakadu77Fake Kakadu77Fake")))
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 1 {
		t.Fatalf("%+v %v", due, err)
	}
	setRaw(t, st, s.ID, 0, promptLine(0, "[REDACTED] [REDACTED]"))
	if err := storeVector(t, st, c, due.Items[0], due.ReadAt); err != nil {
		t.Fatal(err)
	}
	if n := sessionVectors(t, st); n != 0 {
		t.Fatalf("the vector of a text that is now too short was stored: %d", n)
	}
	// And the chunk that is gone altogether: no error, no vector.
	if _, err := st.db.Exec(`DELETE FROM session_chunks WHERE session_id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := storeVector(t, st, c, due.Items[0], due.ReadAt); err != nil {
		t.Fatalf("a chunk that is gone is no error of the job: %v", err)
	}
	if n := sessionVectors(t, st); n != 0 {
		t.Fatalf("%d vectors", n)
	}
}

// A vector or a parked failure whose text is now too short or empty goes with the
// look at the session, however it got there.
func TestSessionLookDropsAVectorAndAFailureWhoseTextIsNowTooShort(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "the first prompt, with a vector that was made of it"),
		promptLine(1, "the second prompt, which the server fails on, s1 s2"),
	))
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 2 {
		t.Fatalf("%+v %v", due, err)
	}
	if err := storeVector(t, st, c, due.Items[0], due.ReadAt); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEmbedFailure(EmbedFailure{Kind: KindSession, ObjectID: due.Items[1].ID, Model: c.Model(), PrefixVersion: c.PrefixVersion(),
		TextHash: due.Items[1].Hash, Attempts: 1, RetryAt: "2099-01-01T00:00:00Z", Error: "x"}); err != nil {
		t.Fatal(err)
	}
	// Both texts are cut down (a redaction that did not know of the vectors), and the
	// session goes on.
	setRaw(t, st, s.ID, 0, promptLine(0, "[REDACTED]"))
	setRaw(t, st, s.ID, 1, promptLine(1, ""))
	if err := st.AppendChunks(s.ID, []Chunk{{Seq: 2, Raw: promptLine(2, "a third prompt, long enough to be embedded, s3 s4")}}); err != nil {
		t.Fatal(err)
	}
	look, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(look.Stale) != 2 || len(look.Items) != 1 {
		t.Fatalf("%+v %v", look, err)
	}
	if n, err := st.DropEmbeddings(KindSession, look.Stale); err != nil || n != 1 {
		t.Fatalf("dropped %d vectors: %v", n, err)
	}
	var failures int
	if err := st.db.QueryRow(`SELECT count(*) FROM session_embedding_failures`).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("%d failures left, %v", failures, err)
	}
	if got := embedSessionTexts(t, st, c, time.Now()); got != 1 || sessionVectors(t, st) != 1 {
		t.Fatalf("%d made, %d vectors", got, sessionVectors(t, st))
	}
}

// ---------------------------------------------------------------- the writer compares lines

// The vector of a session carries the hash of the line its document was made from,
// and the writer keeps it only when the stored line is that one. A vector without
// the hash is no vector the writer can judge, and is refused; a vector of another
// line is not stored, and is no error.
func TestSessionVectorIsStoredOnlyWithTheHashOfTheLineThatIsStored(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	addSession(t, st, "s", 1, "", "m", "", chunksOf(promptLine(0, "please look into the s1 s2 problem for me")))
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 1 || due.Items[0].Source == "" {
		t.Fatalf("the look gives the hash of the line with the item: %+v %v", due, err)
	}
	item := due.Items[0]

	noHash := item
	noHash.Source = ""
	if err := storeVector(t, st, c, noHash, due.ReadAt); err == nil {
		t.Fatal("a vector of a session without the hash of its line was taken")
	}
	other := item
	other.Source = sessionSource("another line")
	if err := storeVector(t, st, c, other, due.ReadAt); err != nil {
		t.Fatalf("a vector of another line is not stored, and is no error of the job: %v", err)
	}
	if n := sessionVectors(t, st); n != 0 {
		t.Fatalf("%d vectors of a line that is not stored", n)
	}
	if err := storeVector(t, st, c, item, due.ReadAt); err != nil || sessionVectors(t, st) != 1 {
		t.Fatalf("%v, %d vectors", err, sessionVectors(t, st))
	}
}

// The writer holds every other write while it checks a vector. For a prompt with a
// log pasted into it, the chain that makes the document (images, secrets, parsing,
// the expressions) takes many times as long as the check, which is one read of the
// line and one hash. Times are the least of three, against a slow machine.
func TestSessionWriterDoesNotMakeTheDocumentAgain(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(promptLine(0, "a prompt that is replaced below, for the s1 s2 job")))
	// Secret-like tokens, so that the redaction has work; set straight into the line,
	// since what arrives through the upload is redacted already.
	raw := promptLine(0, "please look at this: "+strings.Repeat("Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnopqrstuvwxyz0123456789 ", 20000))
	setRaw(t, st, s.ID, 0, raw)
	chunk := chunkID(t, st, s.ID, 0)
	source := sessionSource(raw)

	best := func(run func()) time.Duration {
		var least time.Duration
		for i := range 3 {
			start := time.Now()
			run()
			if took := time.Since(start); i == 0 || took < least {
				least = took
			}
		}
		return least
	}
	chain := best(func() {
		if _, ok := sessionDocument("claude-code", 0, raw); !ok {
			t.Fatal("the line gives no document")
		}
	})
	check := best(func() {
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if keep, err := verifySessionSource(tx, chunk, source); err != nil || !keep {
			t.Fatalf("%v %v", keep, err)
		}
	})
	if check*4 > chain {
		t.Fatalf("the check in the writer takes %v, the chain that makes the document %v: the writer is doing the work of the reader", check, chain)
	}
}

// ---------------------------------------------------------------- the rules change

// The marks hold the version of the rules that make the texts, the model and the
// text format: a session that was marked under other ones is looked at again, and
// what the new rules no longer call for goes. Without it a session that nobody
// writes to would keep what the old rules made of it for ever.
func TestSessionRulesVersionBringsMarkedSessionsBack(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "please look into the s1 s2 problem for me"),
		promptLine(1, "and the second one, which is about s3 s4"),
	))
	if _, err := st.db.Exec(`UPDATE sessions SET last_seen_at = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	embedSessionTexts(t, st, c, time.Now())
	var version int
	var model, prefix string
	if err := st.db.QueryRow(`SELECT version, model, prefix_version FROM session_embedding_sync WHERE session_id = ?`, s.ID).Scan(&version, &model, &prefix); err != nil ||
		version != sessionTextVersion || model != c.Model() || prefix != c.PrefixVersion() {
		t.Fatalf("the mark holds %d %q %q: %v", version, model, prefix, err)
	}
	// A later release takes the second prompt for a notice of a harness (here: the
	// index says so).
	if _, err := st.db.Exec(`UPDATE chunk_index SET text = '<task-notification>done</task-notification>' WHERE session_id = ? AND seq = 1`, s.ID); err != nil {
		t.Fatal(err)
	}
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Stale) != 0 || len(due.Unchanged) != 0 {
		t.Fatalf("without a reason nothing is looked at: %+v %v", due, err)
	}

	old := sessionTextVersion
	sessionTextVersion = old + 1
	defer func() { sessionTextVersion = old }()
	due, err = st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Stale) != 1 || due.Stale[0] != chunkID(t, st, s.ID, 1) || len(due.Unchanged) != 1 || len(due.Items) != 0 {
		t.Fatalf("a new version of the rules: %+v %v", due, err)
	}
	if n, err := st.DropEmbeddings(KindSession, due.Stale); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if err := st.TouchSessionEmbeddings(modelOf(c), due.Unchanged, due.ReadAt); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT version FROM session_embedding_sync WHERE session_id = ?`, s.ID).Scan(&version); err != nil || version != old+1 {
		t.Fatalf("the mark was not renewed: %d %v", version, err)
	}
	if due, err = st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false); err != nil || len(due.Unchanged) != 0 || len(due.Stale) != 0 {
		t.Fatalf("looked at again without a reason: %+v %v", due, err)
	}
	// The model and the text format are in the mark too.
	for _, m := range []EmbedModel{{Model: "other-model", PrefixVersion: c.PrefixVersion()}, {Model: c.Model(), PrefixVersion: "gemma2-v2"}} {
		if due, err = st.EmbeddingDue(KindSession, m, 10, time.Now(), false); err != nil || len(due.Items) != 1 {
			t.Fatalf("%+v: %+v %v", m, due, err)
		}
	}
}

// The mark of a first version of the table, which had no columns for the model and
// the rules, is brought up to date, and its sessions are looked at once more.
func TestSessionSyncTableOfAFirstVersionIsMigrated(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(promptLine(0, "please look into the s1 s2 problem for me")))
	for _, q := range []string{
		`DROP TABLE session_embedding_sync`,
		`CREATE TABLE session_embedding_sync(session_id INTEGER NOT NULL PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE, read_at TEXT NOT NULL) WITHOUT ROWID`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// The old rule would leave it alone: nothing has changed since this look.
	if _, err := st.db.Exec(`INSERT INTO session_embedding_sync(session_id, read_at) VALUES(?, '2099-01-01T00:00:00Z')`, s.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 { // and it is idempotent
		if err := ensureEmbeddings(st.db); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.db.Query(`PRAGMA table_info(session_embedding_sync)`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	rows.Close()
	if want := []string{"session_id", "read_at", "version", "model", "prefix_version"}; !slices.Equal(cols, want) {
		t.Fatalf("columns %v, want %v", cols, want)
	}
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 1 {
		t.Fatalf("the session of an old mark is looked at: %+v %v", due, err)
	}
}

// A tombstone (what a redaction leaves for a session whose vector it dropped) is
// replaced by a look that is later than it. A look that is earlier, or in the same
// second, leaves it: it was made before the change, or cannot say which came first,
// and the session is looked at once more.
func TestSessionTombstoneIsOnlyReplacedByALaterLook(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(promptLine(0, "please look into the s1 s2 problem for me")))
	const at = "2026-10-21T12:00:00Z"
	if _, err := st.db.Exec(`DELETE FROM session_embedding_sync`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO session_embedding_sync(session_id, read_at, version, model, prefix_version) VALUES(?, ?, 0, '', '')`, s.ID, at); err != nil {
		t.Fatal(err)
	}
	mark := func() (version int, readAt string) {
		t.Helper()
		if err := st.db.QueryRow(`SELECT version, read_at FROM session_embedding_sync WHERE session_id = ?`, s.ID).Scan(&version, &readAt); err != nil {
			t.Fatal(err)
		}
		return version, readAt
	}
	for _, readAt := range []string{"2026-10-21T11:00:00Z", at} {
		if err := st.TouchSessionEmbeddings(modelOf(c), []int64{s.ID}, readAt); err != nil {
			t.Fatal(err)
		}
		if v, got := mark(); v != 0 || got != at {
			t.Fatalf("a look at %s replaced the tombstone: version %d, read_at %s", readAt, v, got)
		}
	}
	// A session with a tombstone is looked at.
	if due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false); err != nil || len(due.Items) != 1 {
		t.Fatalf("%+v %v", due, err)
	}
	const newer = "2026-10-21T12:00:01Z"
	if err := st.TouchSessionEmbeddings(modelOf(c), []int64{s.ID}, newer); err != nil {
		t.Fatal(err)
	}
	if v, got := mark(); v != sessionTextVersion || got != newer {
		t.Fatalf("a later look did not replace the tombstone: version %d, read_at %s", v, got)
	}
}

// A text that was too short all along has no vector and no failure to drop, and the
// look does not name it: it would send the job to the writer at every look.
func TestSessionLookDoesNotNameATextThatWasTooShortAllAlong(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(promptLine(0, "ok"), answerLine(1, "fine"), promptLine(2, "yes please")))
	at := later(2 * SessionSettle)
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, at, false)
	if err != nil || len(due.Stale) != 0 || len(due.Items) != 0 || !slices.Equal(due.Unchanged, []int64{s.ID}) {
		t.Fatalf("%+v %v", due, err)
	}
}

// ---------------------------------------------------------------- parked texts

// A text that the server refuses for good does not keep its session waiting: it is
// left as it is until the text, the model or the rules change, and each of them
// brings the session back. A text that failed for a time does, because nothing else
// brings the session back at its time.
func TestSessionTextRefusedForGoodDoesNotKeepItsSessionWaiting(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "the first prompt, which is embedded as usual, s1 s2"),
		promptLine(1, "the second prompt, which the server refuses, s3 s4"),
		promptLine(2, "the third prompt, on which the server fails for a time"),
	))
	if _, err := st.db.Exec(`UPDATE sessions SET last_seen_at = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 3 {
		t.Fatalf("%+v %v", due, err)
	}
	if err := storeVector(t, st, c, due.Items[0], due.ReadAt); err != nil {
		t.Fatal(err)
	}
	fail := func(item EmbedItem, permanent bool, retry string) {
		t.Helper()
		if err := st.RecordEmbedFailure(EmbedFailure{Kind: KindSession, ObjectID: item.ID, Model: c.Model(), PrefixVersion: c.PrefixVersion(),
			TextHash: item.Hash, Attempts: 1, Permanent: permanent, RetryAt: retry, Error: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	fail(due.Items[1], true, "2099-01-01T00:00:00Z")
	fail(due.Items[2], false, "2099-01-01T00:00:00Z")

	// The one that fails for a time keeps the session waiting.
	look, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(look.Items) != 0 || len(look.Unchanged) != 0 {
		t.Fatalf("a text parked for a time: %+v %v", look, err)
	}
	// Past its time it is tried again, and it is the only one.
	if look, err = st.EmbeddingDue(KindSession, modelOf(c), 10, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), false); err != nil || len(look.Items) != 1 || look.Items[0].ID != due.Items[2].ID {
		t.Fatalf("%+v %v", look, err)
	}
	// Once that one is embedded, the refused one is no reason to wait: the session is
	// complete.
	if err := storeVector(t, st, c, due.Items[2], due.ReadAt); err != nil {
		t.Fatal(err)
	}
	look, err = st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(look.Items) != 0 || !slices.Equal(look.Unchanged, []int64{s.ID}) {
		t.Fatalf("a text refused for good: %+v %v", look, err)
	}
	if err := st.TouchSessionEmbeddings(modelOf(c), look.Unchanged, look.ReadAt); err != nil {
		t.Fatal(err)
	}
	if look, err = st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false); err != nil || len(look.Unchanged) != 0 || len(look.Items) != 0 {
		t.Fatalf("a session with a refused text is looked at at every step: %+v %v", look, err)
	}
	// A new model brings it back, and the old refusal does not hold for it.
	other := EmbedModel{Model: "other-model", PrefixVersion: c.PrefixVersion()}
	if look, err = st.EmbeddingDue(KindSession, other, 10, time.Now(), false); err != nil || len(look.Items) != 3 {
		t.Fatalf("another model: %+v %v", look, err)
	}
}

// ---------------------------------------------------------------- reader and writer apart

// On the runtime store the reader answers what is due and only reads: the writer
// stores, marks and drops, in small jobs, and a purge does not hold it for a walk
// through the stock. No deadlock, and the reader (opened read-only) is never asked to
// write: it would say so.
func TestSessionEmbeddingsOnTheRuntimeStoreReadOnTheReaderWrittenInTheWriter(t *testing.T) {
	st, err := OpenRuntime(filepath.Join(t.TempDir(), "rt.db"), DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.reader == nil || st.writer == nil {
		t.Fatal("not a runtime store")
	}
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	_, c := newFakeEmbedder(t)
	var ids []int64
	for i := range 6 {
		s := addSession(t, st, fmt.Sprintf("s%d", i), 1, "", "m", "", chunksOf(
			promptLine(0, fmt.Sprintf("session number %d starts with a prompt about s1 s2", i)),
			answerLine(1, fmt.Sprintf("and the answer of session number %d is about s3 s4", i)),
			promptLine(2, fmt.Sprintf("a second prompt of session number %d, about q1", i)),
		))
		ids = append(ids, s.ID)
	}
	if _, err := st.reader.db.Exec(`INSERT INTO session_embedding_sync(session_id, read_at) VALUES(1, 'x')`); err == nil {
		t.Fatal("the reader of the runtime store can write")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		// Chunks keep coming while the job works.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 30 {
				if err := st.AppendChunks(ids[i%len(ids)], []Chunk{{Seq: 100 + i, Raw: promptLine(i, fmt.Sprintf("more of the session, prompt number %d, about s4", i))}}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		got := embedSessionTexts(t, st, c, later(2*SessionSettle))
		wg.Wait()
		embedSessionTexts(t, st, c, later(2*SessionSettle))
		if got < 18 {
			t.Errorf("%d texts embedded", got)
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the job and the uploads do not come to rest: a deadlock between reader and writer")
	}

	// One session outgrows an answer: the answer that ended its last turn is embedded,
	// and the turn goes on with another. The look names the first on the reader, and
	// the drop is a writer job.
	if err := st.AppendChunks(ids[0], []Chunk{{Seq: 200, Raw: answerLine(3, "an answer that ends the last turn of this session for now")}}); err != nil {
		t.Fatal(err)
	}
	if got := embedSessionTexts(t, st, c, later(2*SessionSettle)); got != 1 {
		t.Fatalf("%d texts embedded", got)
	}
	outgrown := chunkID(t, st, ids[0], 200)
	if err := st.AppendChunks(ids[0], []Chunk{{Seq: 201, Raw: answerLine(4, "and the turn went on, so this is the answer that ends it")}}); err != nil {
		t.Fatal(err)
	}
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 50, later(2*SessionSettle), false)
	if err != nil || len(due.Items) != 1 || !slices.Equal(due.Stale, []int64{outgrown}) {
		t.Fatalf("the look names the answer that was outgrown: %+v %v", due, err)
	}
	vectors := sessionVectors(t, st)
	if n, err := st.DropEmbeddings(KindSession, due.Stale); err != nil || n != 1 {
		t.Fatalf("dropped %d vectors: %v", n, err)
	}
	if got := sessionVectors(t, st); got != vectors-1 {
		t.Fatalf("%d vectors, want %d", got, vectors-1)
	}
	// Dropping a stock in small jobs: 450 ids are three or more jobs of the writer.
	before := st.RuntimeStats().Writer.Admitted
	var many []int64
	for i := int64(1); i <= 450; i++ {
		many = append(many, 1_000_000+i)
	}
	if n, err := st.DropEmbeddings(KindSession, many); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if jobs := st.RuntimeStats().Writer.Admitted - before; jobs < 3 {
		t.Fatalf("450 ids took %d writer jobs, want small pieces of 200", jobs)
	}
	// The purge is a few statements on keys, quick in the writer.
	start := time.Now()
	if _, err := st.PurgeEmbeddings(KindSession); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the purge took %v", took)
	}
	// The status, a read as well, counts what is embedded; the last answer of the
	// session that was just written has not stood long enough to count.
	if s, err := st.EmbeddingStatus(KindSession, modelOf(c)); err != nil || s.Due != 0 || s.Current < 18 {
		t.Fatalf("%+v %v", s, err)
	}
}
