package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

// ---------------------------------------------------------------- helpers

func promptLine(i int, text string) string {
	return userLine(fmt.Sprintf("2026-10-01T10:%02d:00Z", i%60), text)
}

func answerLine(i int, text string) string {
	return assistantLine(fmt.Sprintf("2026-10-01T10:%02d:30Z", i%60), textBlock(text))
}

// embedSessionTexts does what the background job does for sessions, to the end:
// look, embed, store, record the sessions that are complete. at is the time the
// job looks at.
func embedSessionTexts(t *testing.T, st *Store, c *embed.Client, at time.Time) (embedded int) {
	t.Helper()
	for range 400 {
		due, err := st.EmbeddingDue(KindSession, modelOf(c), 16, at, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(due.Stale) > 0 {
			if _, err := st.DropEmbeddings(KindSession, due.Stale); err != nil {
				t.Fatal(err)
			}
		}
		if len(due.Unchanged) > 0 {
			if err := st.TouchSessionEmbeddings(modelOf(c), due.Unchanged, due.ReadAt); err != nil {
				t.Fatal(err)
			}
		}
		if len(due.Items) == 0 {
			return embedded
		}
		texts := make([]string, len(due.Items))
		for i, d := range due.Items {
			texts[i] = d.Text
		}
		vecs, err := c.Documents(context.Background(), texts)
		if err != nil {
			t.Fatal(err)
		}
		for i, d := range due.Items {
			q, scale, err := embed.Quantize(vecs[i])
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertEmbedding(Embedding{Kind: KindSession, ObjectID: d.ID, Model: c.Model(), PrefixVersion: c.PrefixVersion(),
				TextHash: d.Hash, Dim: len(q), Scale: float64(scale), Vec: q, ReadAt: due.ReadAt}); err != nil {
				t.Fatal(err)
			}
			embedded++
		}
	}
	t.Fatal("embedSessionTexts did not converge")
	return 0
}

func dueTexts(t *testing.T, st *Store, c *embed.Client, at time.Time) []string {
	t.Helper()
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 100, at, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range due.Items {
		out = append(out, d.Text)
	}
	return out
}

func sessionVectors(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM session_embeddings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func later(d time.Duration) time.Time { return time.Now().Add(d) }

// ---------------------------------------------------------------- the texts

// A session is not its transcript: the texts are what was typed and the answer
// each turn ended with. Calls, results, messages between two calls, and what a
// harness injects are not.
func TestSessionTextsArePromptsAndTheFinalAnswerOfEachTurn(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "please check the s1 s2 setup of the cache layer"),
		assistantLine("2026-10-01T10:00:10Z", textBlock("looking at the code now"), bashBlock("t1", "grep -rn cache .")),
		resultLine("2026-10-01T10:00:11Z", "t1", "./cache.go:3: cache is keyed by path"),
		assistantLine("2026-10-01T10:00:12Z", thinkBlock("private reasoning about the cache")),
		answerLine(1, "the cache holds s3 s4 per path, that is the answer for today"),
		promptLine(2, "<task-notification>\n<task-id>b1</task-id>\n</task-notification>"),
		answerLine(3, "the job finished and I carried on, so this is the last word of that turn"),
		promptLine(4, "[Request interrupted by user]"),
		promptLine(5, "and what about the second layer please, s3"),
		answerLine(6, "the second layer is fine and uses s4 s3 as well"),
	))
	want := []string{
		embed.SessionText("please check the s1 s2 setup of the cache layer"),
		embed.SessionText("the job finished and I carried on, so this is the last word of that turn"),
		embed.SessionText("and what about the second layer please, s3"),
		embed.SessionText("the second layer is fine and uses s4 s3 as well"),
	}
	got := dueTexts(t, st, c, later(SessionSettle+time.Minute))
	if !slices.Equal(got, want) {
		t.Fatalf("texts\n%q\nwant\n%q", got, want)
	}
	for _, text := range got {
		for _, noise := range []string{"looking at the code", "keyed by path", "private reasoning", "task-notification", "interrupted", "holds s3 s4"} {
			if strings.Contains(text, noise) {
				t.Errorf("%q is not a text of the session: %q", noise, text)
			}
		}
	}
	// The item is the chunk, and the vector is stored with the chunk.
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 100, later(SessionSettle+time.Minute), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due.Items {
		var seq int
		if err := st.db.QueryRow(`SELECT seq FROM session_chunks WHERE id = ?`, d.ID).Scan(&seq); err != nil {
			t.Fatalf("item %d is no chunk: %v", d.ID, err)
		}
	}
}

// An answer is the final one of its turn when the turn is closed: a prompt
// follows, or the session has been quiet. Until then the next message of a
// running session would make it the wrong text.
func TestSessionFinalAnswerWaitsForTheTurnToClose(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "please explain the q1 q2 cache layer to me"),
		answerLine(1, "the first answer, which a later prompt closes"),
		promptLine(2, "and now the second thing about q3 please"),
		answerLine(3, "the second answer, which nothing has closed yet"),
	))
	prompt1, answer1, prompt2 := embed.SessionText("please explain the q1 q2 cache layer to me"),
		embed.SessionText("the first answer, which a later prompt closes"), embed.SessionText("and now the second thing about q3 please")
	answer2 := embed.SessionText("the second answer, which nothing has closed yet")

	if got := dueTexts(t, st, c, time.Now()); !slices.Equal(got, []string{prompt1, answer1, prompt2}) {
		t.Fatalf("right after the upload: %q", got)
	}
	if got := dueTexts(t, st, c, later(SessionSettle-time.Minute)); slices.Contains(got, answer2) {
		t.Fatalf("29 minutes of quiet are not enough: %q", got)
	}
	if got := dueTexts(t, st, c, later(SessionSettle+time.Minute)); !slices.Equal(got, []string{prompt1, answer1, prompt2, answer2}) {
		t.Fatalf("after half an hour: %q", got)
	}

	// While it waits the session is not recorded as done, so it comes back.
	embedSessionTexts(t, st, c, time.Now())
	if n := sessionVectors(t, st); n != 3 {
		t.Fatalf("%d vectors", n)
	}
	var marked int
	if err := st.db.QueryRow(`SELECT count(*) FROM session_embedding_sync WHERE session_id = ?`, s.ID).Scan(&marked); err != nil || marked != 0 {
		t.Fatalf("a session with a waiting answer was marked complete: %d %v", marked, err)
	}
	if got := embedSessionTexts(t, st, c, later(SessionSettle+time.Minute)); got != 1 {
		t.Fatalf("the answer was embedded %d times", got)
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM session_embedding_sync WHERE session_id = ?`, s.ID).Scan(&marked); err != nil || marked != 1 {
		t.Fatalf("a complete session is marked: %d %v", marked, err)
	}

	// A later prompt closes the turn at once, whatever the time.
	if err := st.AppendChunks(s.ID, []Chunk{{Seq: 4, Raw: promptLine(4, "one more question, about q4 this time")}}); err != nil {
		t.Fatal(err)
	}
	if got := dueTexts(t, st, c, time.Now()); !slices.Equal(got, []string{embed.SessionText("one more question, about q4 this time")}) {
		t.Fatalf("a new prompt: %q", got)
	}
}

// A session that goes on after its answer was embedded has outgrown it: the
// answer is no longer the last of its turn. The look at the session, which the new
// chunk brings about, names the vector (EmbedDue.Stale), and the job drops it.
func TestSessionVectorOfAnAnswerThatWasOutgrownIsNamedByTheLookAndDropped(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "please look into the s1 s2 problem for me"),
		answerLine(1, "an answer after which the session goes on"),
	))
	embedSessionTexts(t, st, c, later(SessionSettle+time.Minute))
	if n := sessionVectors(t, st); n != 2 {
		t.Fatalf("%d vectors", n)
	}
	if err := st.AppendChunks(s.ID, []Chunk{{Seq: 2, Raw: answerLine(2, "the answer that really ends this turn")}}); err != nil {
		t.Fatal(err)
	}
	// The new answer is the last of the turn, and it has not stood long enough: it
	// waits. The old one is named, once, by the look that the chunk brought about.
	old := chunkID(t, st, s.ID, 1)
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 0 || len(due.Stale) != 1 || due.Stale[0] != old {
		t.Fatalf("%+v %v", due, err)
	}
	// Reading does not drop it: the job does, and the session is still looked at.
	if n := sessionVectors(t, st); n != 2 {
		t.Fatalf("the look wrote: %d vectors", n)
	}
	if removed, err := st.DropEmbeddings(KindSession, due.Stale); err != nil || removed != 1 {
		t.Fatalf("dropped %d: %v", removed, err)
	}
	if n := sessionVectors(t, st); n != 1 {
		t.Fatalf("%d vectors after the drop", n)
	}
	if due, err = st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false); err != nil || len(due.Stale) != 0 {
		t.Fatalf("the vector is named twice: %+v %v", due, err)
	}
	// The vector of the prompt stays, and the new answer is embedded when it has
	// stood long enough.
	embedSessionTexts(t, st, c, later(2*SessionSettle))
	if n := sessionVectors(t, st); n != 2 {
		t.Fatalf("%d vectors", n)
	}
}

// An answer that the session touched again but did not outgrow keeps its vector:
// the session is not quiet any more, which makes the answer wait, not vanish.
func TestSessionVectorOfAnAnswerIsKeptWhileTheSessionIsTouchedAgain(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "please look into the s1 s2 problem for me"),
		answerLine(1, "the answer that ends the only turn so far"),
	))
	embedSessionTexts(t, st, c, later(SessionSettle+time.Minute))
	// A heartbeat of the collector, no chunk.
	if _, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "s", AccountID: 1, Scope: s.Scope}); err != nil {
		t.Fatal(err)
	}
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Stale) != 0 || len(due.Items) != 0 {
		t.Fatalf("%+v %v", due, err)
	}
	if n := sessionVectors(t, st); n != 2 {
		t.Fatalf("%d vectors", n)
	}
}

// ---------------------------------------------------------------- what a text is

func TestSessionDocumentIsMadeBySanitizingAndCutting(t *testing.T) {
	secret := "ghp_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"
	line := func(text string) string { return userLine("2026-10-01T10:00:00Z", text) }
	for _, c := range []struct {
		name, raw string
		want      string // "" says: no text
		absent    []string
	}{
		{"plain", line("wann haben wir den Wagen repariert und was kostete es"), embed.SessionText("wann haben wir den Wagen repariert und was kostete es"), nil},
		{"secret in the line", line("my token is " + secret + " please keep it"), embed.SessionText("my token is [REDACTED:github] please keep it"), []string{secret, "ghp_"}},
		{"placeholder of a removed image", line("look at this [ghosttree: Bild entfernt (image/png, 91 KB)] and tell me what is wrong"),
			embed.SessionText("look at this and tell me what is wrong"), []string{"ghosttree", "Bild entfernt"}},
		{"image mark", line("see [Image #3] and [Image] for the broken layout of the page"),
			embed.SessionText("see and for the broken layout of the page"), []string{"[Image"}},
		{"injected block inside the text", line("fix the layout <system-reminder>do not mention this</system-reminder> of the page today"),
			embed.SessionText("fix the layout of the page today"), []string{"mention", "system-reminder"}},
		{"too short", line("ok, weiter"), "", nil},
		{"short after the cleaning", line("[Image #1] ok bitte [Image #2]"), "", nil},
		{"no text at all", resultLine("2026-10-01T10:00:00Z", "t1", "some output of a tool that is long enough"), "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, ok := sessionDocument("claude-code", 0, c.raw)
			if (c.want == "") == ok || doc != c.want {
				t.Fatalf("document %q (%v), want %q", doc, ok, c.want)
			}
			for _, a := range c.absent {
				if strings.Contains(doc, a) {
					t.Fatalf("%q is still in %q", a, doc)
				}
			}
		})
	}
	long := strings.Repeat("kurz und gut ", 400)
	doc, ok := sessionDocument("claude-code", 0, line(long))
	if n := len([]rune(doc)); !ok || n > embed.MaxDocumentRunes || n < embed.MaxDocumentRunes-10 || !strings.HasPrefix(doc, "title: none | text: kurz und gut") {
		t.Fatalf("a long text is cut to the document size: %d runes, %v", n, ok)
	}
	// The same text is the same document: the hash that decides is stable.
	again, _ := sessionDocument("claude-code", 0, line(long))
	if embed.TextHash(again) != embed.TextHash(doc) {
		t.Fatal("the hash of a text changes")
	}
}

// What is embedded is what the chain made of the stored line, even where the
// stored line holds what the rules take out: a database that was written before a
// rule existed, or by something that did not apply it.
func TestSessionEmbeddingsNeverSendASecretOrAPlaceholder(t *testing.T) {
	st := orgStore(t, "robin")
	srv, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", nil)
	secret := "ghp_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"
	legacyChunks(t, st, s.ID, []Chunk{
		{Seq: 0, Raw: promptLine(0, "please keep my token "+secret+" safe for the s1 s2 job [Image #2]")},
		{Seq: 1, Raw: answerLine(1, "noted [ghosttree: Bild entfernt (image/png, 3 KB)] and kept it safe, s3 s4")},
		{Seq: 2, Raw: promptLine(2, "thank you very much, that was all for today")},
	})
	resetBackfill(t, st)
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 10}); err != nil {
		t.Fatal(err)
	}
	if got := embedSessionTexts(t, st, c, time.Now()); got != 3 {
		t.Fatalf("%d texts embedded", got)
	}
	for _, req := range srv.Requests() {
		for _, text := range req {
			for _, bad := range []string{secret, "ghp_", "[ghosttree: ", "[Image", "Bild entfernt"} {
				if strings.Contains(text, bad) {
					t.Fatalf("the embedding server got %q in %q", bad, text)
				}
			}
		}
	}
}

// ---------------------------------------------------------------- when a session is looked at

func TestSessionIsLookedAtAgainOnlyWhenItChanges(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "please look into the s1 s2 problem for me"),
		answerLine(1, "the problem is solved, it was a typo in s3 s4"),
		promptLine(2, "thanks, now something different about q1"),
	))
	idle := addSession(t, st, "tools only", 1, "", "m", "", chunksOf(
		assistantLine("2026-10-01T10:00:00Z", bashBlock("t1", "ls")),
		resultLine("2026-10-01T10:00:01Z", "t1", "a b c"),
	))
	// Sessions that were written a while ago: a chunk in the same second as the look
	// counts as a change (the look is taken before the text is read, and a second is
	// the resolution of the times), which the test must not run into.
	if _, err := st.db.Exec(`UPDATE sessions SET last_seen_at = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if got := embedSessionTexts(t, st, c, later(time.Minute)); got != 3 {
		t.Fatalf("first run: %d", got)
	}
	var marks int
	if err := st.db.QueryRow(`SELECT count(*) FROM session_embedding_sync`).Scan(&marks); err != nil || marks != 1 {
		t.Fatalf("only the session with texts has a mark, the one without any text has none: %d %v", marks, err)
	}
	_ = idle
	// Nothing changed, nothing is due, and nothing is looked at.
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 100, later(time.Minute), false)
	if err != nil || len(due.Items) != 0 || len(due.Unchanged) != 0 {
		t.Fatalf("%+v %v", due, err)
	}
	// A chunk is a reason.
	if err := st.AppendChunks(s.ID, []Chunk{{Seq: 3, Raw: answerLine(3, "something different, here is what q1 stands for")}}); err != nil {
		t.Fatal(err)
	}
	due, err = st.EmbeddingDue(KindSession, modelOf(c), 100, later(time.Minute), false)
	if err != nil || len(due.Items) != 0 || len(due.Unchanged) != 0 {
		t.Fatalf("the new answer is open and waits: %+v %v", due, err)
	}
	if got := embedSessionTexts(t, st, c, later(SessionSettle+time.Minute)); got != 1 {
		t.Fatalf("%d texts after the answer settled", got)
	}
	// Another model makes every vector due again.
	m := EmbedModel{Model: "another-model", PrefixVersion: c.PrefixVersion()}
	due, err = st.EmbeddingDue(KindSession, m, 100, later(2*SessionSettle), false)
	if err != nil || len(due.Items) != 4 {
		t.Fatalf("another model: %d items, %v", len(due.Items), err)
	}
	m = EmbedModel{Model: c.Model(), PrefixVersion: "gemma2-v2"}
	if due, err = st.EmbeddingDue(KindSession, m, 100, later(2*SessionSettle), false); err != nil || len(due.Items) != 4 {
		t.Fatalf("another text format: %d items, %v", len(due.Items), err)
	}
}

// A text that changed after its vector was made is noticed by its hash when the
// session is looked at.
func TestSessionVectorOfAChangedTextIsMadeAgain(t *testing.T) {
	st := orgStore(t, "robin")
	srv, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "please look into the s1 s2 problem for me"),
		promptLine(1, "and the second one, which is about s3 s4"),
	))
	// A session written a while ago: a chunk in the second of the look counts as a
	// change, which this test must not run into.
	if _, err := st.db.Exec(`UPDATE sessions SET last_seen_at = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	embedSessionTexts(t, st, c, time.Now())
	before := srv.Inputs()
	var hash string
	if err := st.db.QueryRow(`SELECT e.text_hash FROM session_embeddings e JOIN session_chunks c ON c.id = e.chunk_id WHERE c.session_id = ? AND c.seq = 1`, s.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	// Somebody rewrites the line without a word to the vectors (another tool).
	if _, err := st.db.Exec(`UPDATE session_chunks SET raw = ? WHERE session_id = ? AND seq = 1`, promptLine(1, "and the second one, rewritten and about s4 s3 now"), s.ID); err != nil {
		t.Fatal(err)
	}
	// Nothing brings the session back by itself.
	if got := dueTexts(t, st, c, time.Now()); len(got) != 0 {
		t.Fatalf("a text that changed behind the application's back is noticed without a reason to look: %q", got)
	}
	// The session goes on: the look compares the hashes of all of its texts.
	if err := st.AppendChunks(s.ID, []Chunk{{Seq: 2, Raw: promptLine(2, "and a third prompt, about the s2 s3 cache layer")}}); err != nil {
		t.Fatal(err)
	}
	if got := embedSessionTexts(t, st, c, time.Now()); got != 2 {
		t.Fatalf("%d texts made, want the rewritten one and the new one", got)
	}
	if srv.Inputs() != before+2 {
		t.Fatalf("the unchanged text was sent again: %d -> %d", before, srv.Inputs())
	}
	var now string
	if err := st.db.QueryRow(`SELECT e.text_hash FROM session_embeddings e JOIN session_chunks c ON c.id = e.chunk_id WHERE c.session_id = ? AND c.seq = 1`, s.ID).Scan(&now); err != nil || now == hash {
		t.Fatalf("hash %s -> %s, %v", hash, now, err)
	}
}

// ---------------------------------------------------------------- status

func TestSessionEmbeddingStatusCountsTextsAndWhatIsDue(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	addSession(t, st, "a", 1, "", "m", "", chunksOf(
		promptLine(0, "please look into the s1 s2 problem for me"),
		answerLine(1, "solved, it was a typo in s3 s4 of the file"),
		promptLine(2, "ok"), // too short
	))
	addSession(t, st, "b", 1, "", "m", "", chunksOf(promptLine(0, "a second session about q1 and q2 for you")))
	status := func() EmbedStatus {
		s, err := st.EmbeddingStatus(KindSession, modelOf(c))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status(); s.Active != 3 || s.Current != 0 || s.Due != 3 {
		t.Fatalf("before: %+v", s)
	}
	embedSessionTexts(t, st, c, later(SessionSettle+time.Minute))
	if s := status(); s.Active != 3 || s.Current != 3 || s.Due != 0 {
		t.Fatalf("after: %+v", s)
	}
	if s, err := st.EmbeddingStatus(KindSession, EmbedModel{Model: "other", PrefixVersion: c.PrefixVersion()}); err != nil || s.Current != 0 || s.OtherModel != 3 || s.Due != 3 {
		t.Fatalf("another model: %+v %v", s, err)
	}
}

// ---------------------------------------------------------------- deletion

// A vector lives and dies with its chunk, and a chunk with its session.
func TestSessionVectorsGoWithTheirChunkAndSession(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	keep := addSession(t, st, "keep", 1, "", "m", "", chunksOf(promptLine(0, "a session that stays, about s1 s2 and more")))
	gone := addSession(t, st, "gone", 1, "", "m", "", chunksOf(
		promptLine(0, "a session that goes away, about s3 s4 and more"),
		answerLine(1, "an answer that goes with it, mentioning s1 s2 s3"),
		promptLine(2, "a prompt that goes alone, with q1 q2 q3 inside"),
	))
	embedSessionTexts(t, st, c, later(SessionSettle+time.Minute))
	if n := sessionVectors(t, st); n != 4 {
		t.Fatalf("%d vectors", n)
	}
	// Failures hang on the chunk too.
	if err := st.RecordEmbedFailure(EmbedFailure{Kind: KindSession, ObjectID: chunkID(t, st, gone.ID, 1), Model: c.Model(), PrefixVersion: c.PrefixVersion(),
		TextHash: "h", Attempts: 1, RetryAt: "2099-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	// One chunk: its vector, its failure.
	if _, err := st.db.Exec(`DELETE FROM session_chunks WHERE session_id = ? AND seq = 2`, gone.ID); err != nil {
		t.Fatal(err)
	}
	if n := sessionVectors(t, st); n != 3 {
		t.Fatalf("the vector of a deleted chunk is still there: %d", n)
	}
	if _, err := st.db.Exec(`DELETE FROM session_chunks WHERE session_id = ? AND seq = 1`, gone.ID); err != nil {
		t.Fatal(err)
	}
	var failures int
	if err := st.db.QueryRow(`SELECT count(*) FROM session_embedding_failures`).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("the failure of a deleted chunk is still there: %d %v", failures, err)
	}

	// The session: its chunks are deleted first (session_chunks does not cascade
	// from sessions), then it, and every trace of it goes.
	if _, err := st.db.Exec(`DELETE FROM session_chunks WHERE session_id = ?`, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM sessions WHERE id = ?`, gone.ID); err != nil {
		t.Fatal(err)
	}
	var marks, vectors int
	_ = st.db.QueryRow(`SELECT count(*) FROM session_embedding_sync WHERE session_id = ?`, gone.ID).Scan(&marks)
	_ = st.db.QueryRow(`SELECT count(*) FROM session_embeddings e JOIN session_chunks c ON c.id = e.chunk_id WHERE c.session_id = ?`, gone.ID).Scan(&vectors)
	if marks != 0 || vectors != 0 || sessionVectors(t, st) != 1 {
		t.Fatalf("marks %d, vectors of the deleted session %d, vectors in all %d", marks, vectors, sessionVectors(t, st))
	}
	// What stays is untouched and still complete.
	if due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, later(2*SessionSettle), false); err != nil || len(due.Items) != 0 {
		t.Fatalf("%+v %v", due, err)
	}
	_ = keep
}

// The job also removes what a database without foreign keys left behind.
func TestSessionPurgeRemovesVectorsOfChunksThatAreGone(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", chunksOf(
		promptLine(0, "the first prompt, about s1 s2 and some more"),
		promptLine(1, "the second prompt, about s3 s4 and some more"),
	))
	embedSessionTexts(t, st, c, time.Now())
	if _, err := st.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM session_chunks WHERE session_id = ? AND seq = 1`, s.ID); err != nil {
		t.Fatal(err)
	}
	if n := sessionVectors(t, st); n != 2 {
		t.Fatalf("without foreign keys the vector stays: %d", n)
	}
	if _, err := st.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	removed, err := st.PurgeEmbeddings(KindSession)
	if err != nil || removed != 1 || sessionVectors(t, st) != 1 {
		t.Fatalf("purge removed %d (%v), %d vectors left", removed, err, sessionVectors(t, st))
	}
	// A vector of a chunk that the index does not know is left alone: that says
	// nothing about it.
	if _, err := st.db.Exec(`DELETE FROM chunk_index WHERE session_id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	if removed, err := st.PurgeEmbeddings(KindSession); err != nil || removed != 0 || sessionVectors(t, st) != 1 {
		t.Fatalf("purge without an index removed %d (%v)", removed, err)
	}
}

func chunkID(t *testing.T, st *Store, session int64, seq int) int64 {
	t.Helper()
	var id int64
	if err := st.db.QueryRow(`SELECT id FROM session_chunks WHERE session_id = ? AND seq = ?`, session, seq).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// While the index of the chunks is being built, nothing is due: a session whose
// chunks are not all in it would be taken for complete.
func TestSessionNothingIsDueWhileTheIndexIsBeingBuilt(t *testing.T) {
	st := orgStore(t, "robin")
	_, c := newFakeEmbedder(t)
	s := addSession(t, st, "s", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, chunksOf(promptLine(0, "a prompt that is not in the index yet, s1 s2")))
	resetBackfill(t, st)
	due, err := st.EmbeddingDue(KindSession, modelOf(c), 10, time.Now(), false)
	if err != nil || len(due.Items) != 0 || len(due.Unchanged) != 0 {
		t.Fatalf("%+v %v", due, err)
	}
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 10}); err != nil {
		t.Fatal(err)
	}
	if got := dueTexts(t, st, c, time.Now()); len(got) != 1 {
		t.Fatalf("after the index: %q", got)
	}
}
