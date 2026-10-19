package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// ownChunks is enough text of their own for an account to have a language gate.
const ownChunks = minOwnChunks + 1000

// seedChunks files n session chunks of one account whose text is given by text.
// The words are that account's language for the second gate: the more of its
// chunks carry a word, the more ordinary the word is for it. It returns the
// session id so that a caller can append to the same session later.
func seedChunks(t *testing.T, s *Store, session string, account int64, n int, text func(i int) string) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: session, AccountID: account})
	if err != nil {
		t.Fatal(err)
	}
	appendChunks(t, s, id, 0, n, text)
	return id
}

// appendChunks adds chunks seq from..from+n-1 to a session.
func appendChunks(t *testing.T, s *Store, session int64, from, n int, text func(i int) string) {
	t.Helper()
	chunks := make([]Chunk, n)
	for i := range chunks {
		chunks[i] = Chunk{Seq: from + i, Role: "user", Text: text(from + i), Raw: "{}"}
	}
	if err := s.AppendChunks(session, chunks); err != nil {
		t.Fatal(err)
	}
}

// prose is text with no word of interest, for the chunks around the ones a test
// is about.
func prose(i int) string { return fmt.Sprintf("das war ein langes gespräch über den server %d", i) }

// reader is the account whose language the tests measure.
const reader = "person:1"

func addEntry(t *testing.T, s *Store, k Knowledge) int64 {
	t.Helper()
	if k.Type == "" {
		k.Type = "pitfall"
	}
	if k.Confidence == "" {
		k.Confidence = "trusted"
	}
	id, err := s.InsertKnowledge(k)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func relevantFor(t *testing.T, s *Store, text, account, session string) Relevance {
	t.Helper()
	res, err := s.RelevantFor(RelevanceQuery{Text: text, Limit: 3, Account: account, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func hitTitles(res Relevance) []string {
	var out []string
	for _, h := range res.Hits {
		out = append(out, h.Title)
	}
	return out
}

// "Stand der Dinge" fetched an unrelated entry because "dinge" is rare among
// the entries — nobody files a pitfall about things. It is not rare in the
// reader's language. The second gate measures that on the reader's own session
// text, so the same sentence that used to deliver now says nothing, and no word
// list of anybody's was consulted.
func TestRelevantForDropsAWordThatIsOrdinaryInTheReadersOwnSessions(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Title: "Agentbench: manche Dinge laufen nur einmal",
		Body: "Der Lauf ist nicht wiederholbar."})
	const prompt = "was ist stand der dinge? wo stehen wir"

	// An account with no text of its own has nothing to measure against, so the
	// gate is off and the word still looks distinctive. This is the behaviour
	// the gate replaces, and the reason it needs the text.
	if hits := hitTitles(relevantFor(t, s, prompt, reader, "")); len(hits) != 1 {
		t.Fatalf("without session text the gate must stay open, hits = %v", hits)
	}

	// Every third chunk says "dinge": the word of any German conversation.
	seedChunks(t, s, "talk", 1, ownChunks, func(i int) string {
		if i%3 == 0 {
			return fmt.Sprintf("die dinge laufen, nummer %d", i)
		}
		return prose(i)
	})
	if hits := hitTitles(relevantFor(t, s, prompt, reader, "")); len(hits) != 0 {
		t.Errorf("a word that is in a third of the reader's session text delivered %v", hits)
	}
}

// A made-up word is the opposite case, and the one the feature is for: it is in
// almost none of the reader's session text, so the gate lets it through, and it
// reaches an entry that names it only in the body.
func TestRelevantForFindsACodenameThatOnlyTheBodyNames(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Type: "note", Title: "Reverse-Proxy auf der VM",
		Body: "Der Codename hlpvpngw steht für den Proxy vor dem Dienst."})
	seedChunks(t, s, "talk", 1, ownChunks, func(i int) string {
		if i < 2 {
			return "hlpvpngw läuft wieder"
		}
		return prose(i)
	})

	res := relevantFor(t, s, "was weißt du über hlpvpngw", reader, "")
	if len(res.Hits) != 1 || res.Hits[0].Title != "Reverse-Proxy auf der VM" {
		t.Fatalf("hits = %v, want the proxy note", hitTitles(res))
	}
	if res.Hits[0].Term != "hlpvpngw" {
		t.Errorf("trigger = %q, want the word that was typed", res.Hits[0].Term)
	}
}

// The body only counts for a word that almost nothing names. A word that three
// entries mention is the subject of none of them, and delivering all three
// because a sentence said it would be the title-only rule undone.
func TestRelevantForReadsTheBodyOnlyForVeryRareWords(t *testing.T) {
	s := openTest(t)
	for i := range 100 {
		addEntry(t, s, Knowledge{Type: "note", Title: fmt.Sprintf("Betriebsnotiz %d", i),
			Body: "Der Server läuft, der Test war grün."})
	}
	for i := range 3 {
		addEntry(t, s, Knowledge{Type: "note", Title: fmt.Sprintf("Lieferkette %d", i),
			Body: "Beim Aufbau tauchte zebrafish im Log auf."})
	}
	addEntry(t, s, Knowledge{Type: "note", Title: "Quokka im Titel", Body: "Beim Aufbau tauchte quokka im Log auf."})
	addEntry(t, s, Knowledge{Type: "note", Title: "Anderes Thema", Body: "quokka noch einmal im Text."})
	addEntry(t, s, Knowledge{Type: "note", Title: "Drittes Thema", Body: "und quokka ein drittes Mal."})

	if hits := hitTitles(relevantFor(t, s, "was ist mit zebrafish", "a", "")); len(hits) != 0 {
		t.Errorf("a word three entries mention in their bodies delivered %v", hits)
	}
	// Three entries name quokka, but only one in its title: the title still decides.
	res := relevantFor(t, s, "was ist mit quokka", "a", "")
	if len(res.Hits) != 1 || res.Hits[0].Title != "Quokka im Titel" {
		t.Errorf("hits = %v, want only the entry that has the word in its title", hitTitles(res))
	}
}

// Unconfirmed material and the gardener's notes never interrupt a sentence. The
// gardener files its suggestions staged, titled "[Gärtner] Veraltet? ...", and a
// confirmed one is still a comment on another entry.
func TestRelevantForLeavesOutStagedEntriesAndTheGardener(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Title: "Quelzor hängt nach dem Neustart", Body: "b", Confidence: "staged"})
	addEntry(t, s, Knowledge{Title: "[Gärtner] Veraltet? Vexilla ist erledigt", Body: "b",
		Origin: "distilled", Confidence: "staged", Harness: "gardener", SessionRef: "gardener:check-knowledge:1"})
	addEntry(t, s, Knowledge{Title: "[Gärtner] Veraltet? Brindle ist erledigt", Body: "b",
		Confidence: "verified", Harness: "gardener"})
	addEntry(t, s, Knowledge{Title: "Mondrix braucht einen Neustart", Body: "b"})

	for _, word := range []string{"quelzor", "vexilla", "brindle"} {
		if hits := hitTitles(relevantFor(t, s, "wie läuft "+word, "a", "")); len(hits) != 0 {
			t.Errorf("%s delivered %v", word, hits)
		}
	}
	if hits := hitTitles(relevantFor(t, s, "wie läuft mondrix", "a", "")); len(hits) != 1 {
		t.Errorf("a confirmed entry by a person was held back: %v", hits)
	}
	// The same holds for the plain entry point the replay and old callers use.
	ks, err := s.RelevantKnowledge("wie läuft quelzor", scope.Axes{}, 3)
	if err != nil || len(ks) != 0 {
		t.Errorf("RelevantKnowledge delivered a staged entry: %v %v", ks, err)
	}
}

// Once per session: what was delivered is not delivered again, another session
// gets it afresh, and so does another account that happens to share a key.
func TestRelevantForDeliversAnEntryOncePerSession(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	const prompt = "welches modell soll ich in ollama nehmen"

	first := relevantFor(t, s, prompt, "person:1", "sess-a")
	if len(first.Hits) != 1 {
		t.Fatalf("first mention: hits = %v", hitTitles(first))
	}
	claimed, err := s.ClaimContextDeliveries("person:1", "sess-a", DeliveredByRelevance, []ContextDelivery{{KnowledgeID: first.Hits[0].ID}})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v %v", claimed, err)
	}
	if again := relevantFor(t, s, prompt, "person:1", "sess-a"); len(again.Hits) != 0 {
		t.Errorf("second mention in the same session delivered %v", hitTitles(again))
	}
	if other := relevantFor(t, s, prompt, "person:1", "sess-b"); len(other.Hits) != 1 {
		t.Errorf("another session must get the entry again: %v", hitTitles(other))
	}
	if other := relevantFor(t, s, prompt, "person:2", "sess-a"); len(other.Hits) != 1 {
		t.Errorf("another account with the same session key must get it again: %v", hitTitles(other))
	}
	// No session id: nothing remembered, nothing held back — delivery as it was.
	if bare := relevantFor(t, s, prompt, "person:1", ""); len(bare.Hits) != 1 {
		t.Errorf("without a session the entry must always come: %v", hitTitles(bare))
	}
}

// What the start-up briefing carried counts as delivered. The server records it
// under the same key, so a pitfall already in the session does not arrive a
// second time because a sentence names it.
func TestRelevantForHonoursWhatTheBootstrapDelivered(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	res := relevantFor(t, s, "ollama", "person:1", "sess-a")
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %v", hitTitles(res))
	}
	if _, err := s.ClaimContextDeliveries("person:1", "sess-a", DeliveredByBootstrap, []ContextDelivery{{KnowledgeID: res.Hits[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if again := relevantFor(t, s, "ollama", "person:1", "sess-a"); len(again.Hits) != 0 {
		t.Errorf("an entry from the bootstrap was delivered again: %v", hitTitles(again))
	}
	var via string
	if err := s.db.QueryRow(`SELECT via FROM context_deliveries WHERE knowledge_id = ?`, res.Hits[0].ID).Scan(&via); err != nil || via != DeliveredByBootstrap {
		t.Errorf("via = %q %v", via, err)
	}
}

// A delivered entry must not use up one of the three places: the next entry the
// same word reaches takes it.
func TestRelevantForFillsFreedPlacesWithTheNextEntry(t *testing.T) {
	s := openTest(t)
	for i := range 100 {
		addEntry(t, s, Knowledge{Type: "note", Title: fmt.Sprintf("Betriebsnotiz %d", i), Body: "Der Server läuft."})
	}
	for _, title := range []string{"Narwhal eins", "Narwhal zwei", "Narwhal drei", "Narwhal vier"} {
		addEntry(t, s, Knowledge{Title: title, Body: "b"})
	}
	res := relevantFor(t, s, "narwhal", "a", "s")
	if len(res.Hits) != 3 {
		t.Fatalf("hits = %v, want the limit of three", hitTitles(res))
	}
	var claim []ContextDelivery
	for _, h := range res.Hits {
		claim = append(claim, ContextDelivery{KnowledgeID: h.ID})
	}
	if _, err := s.ClaimContextDeliveries("a", "s", DeliveredByRelevance, claim); err != nil {
		t.Fatal(err)
	}
	next := relevantFor(t, s, "narwhal", "a", "s")
	if len(next.Hits) != 1 {
		t.Fatalf("hits = %v, want the one entry not yet delivered", hitTitles(next))
	}
	for _, h := range res.Hits {
		if h.ID == next.Hits[0].ID {
			t.Errorf("delivered %d twice", h.ID)
		}
	}
}

// A very rare word that no entry names is a candidate for a hint; one that an
// entry names is not, because the entry is the answer; and one the session was
// already pointed at is not offered again.
func TestRelevantForOffersHintsOnlyForWordsWithoutKnowledge(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Type: "note", Title: "Reverse-Proxy auf der VM", Body: "Codename zephyrine für den Proxy."})
	seedChunks(t, s, "talk", 1, ownChunks, func(i int) string {
		switch i {
		case 0, 1:
			return "hlpvpngw läuft wieder"
		case 2, 3, 4:
			return "zephyrine ist der Proxy"
		}
		return prose(i)
	})

	res := relevantFor(t, s, "was ist mit hlpvpngw und zephyrine", "a", "sess")
	hints := map[string]HintTerm{}
	for _, h := range res.Hints {
		hints[h.Term] = h
	}
	if h, ok := hints["hlpvpngw"]; !ok || len(h.Known) != 0 {
		t.Errorf("hlpvpngw: %+v, want a hint candidate with no knowledge", h)
	}
	if h, ok := hints["zephyrine"]; !ok || len(h.Known) != 1 {
		t.Errorf("zephyrine: %+v, want the entry that names it listed as the answer", h)
	}
	// The order is the sentence's, not a measure of how common the words are in
	// the session text: a reader must not be able to compare those counts.
	if len(res.Hints) < 2 || res.Hints[0].Term != "hlpvpngw" || res.Hints[1].Term != "zephyrine" {
		t.Errorf("hints = %v, want the order of the sentence", res.Hints)
	}
	reversed := relevantFor(t, s, "was ist mit zephyrine und hlpvpngw", "a", "sess-reversed")
	if len(reversed.Hints) < 2 || reversed.Hints[0].Term != "zephyrine" {
		t.Errorf("hints = %v, want the order to follow the sentence, not the counts", reversed.Hints)
	}

	if _, err := s.ClaimContextDeliveries("a", "sess", DeliveredByHint, []ContextDelivery{{Term: "hlpvpngw"}}); err != nil {
		t.Fatal(err)
	}
	for _, h := range relevantFor(t, s, "was ist mit hlpvpngw", "a", "sess").Hints {
		if h.Term == "hlpvpngw" {
			t.Errorf("a word already hinted in this session was offered again")
		}
	}
	found := false
	for _, h := range relevantFor(t, s, "was ist mit hlpvpngw", "a", "other-session").Hints {
		found = found || h.Term == "hlpvpngw"
	}
	if !found {
		t.Errorf("another session must be offered the hint again")
	}
}

// A word that is ordinary in the reader's text is never hinted at either: the
// hint is for something specific, and "dinge" names nothing. No entry names it,
// so only the language gate stands between it and a hint.
func TestRelevantForOffersNoHintForAnOrdinaryWord(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	seedChunks(t, s, "talk", 1, ownChunks, func(i int) string { return fmt.Sprintf("die dinge laufen schon %d", i) })
	for _, h := range relevantFor(t, s, "wie stehen die dinge", reader, "s").Hints {
		if h.Term == "dinge" {
			t.Errorf("an ordinary word was offered for a hint: %+v", h)
		}
	}
}

// Where the index of session text is missing, the gate falls away rather than
// failing the prompt: delivery behaves as it did before the gate.
func TestRelevantForWithoutTheChunkIndexKeepsWorking(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	seedChunks(t, s, "talk", 1, ownChunks, prose)
	if _, err := s.db.Exec(`DROP TABLE chunks_fts`); err != nil {
		t.Fatal(err)
	}
	if hits := hitTitles(relevantFor(t, s, "ollama", reader, "s")); len(hits) != 1 {
		t.Errorf("hits = %v", hits)
	}
}

// The gate measures the reader and nobody else. A word that is ordinary in the
// sessions of another account says nothing about this reader: it must neither
// suppress a delivery nor let one through, or a prompt typed to test one word
// would show whether other accounts use it.
func TestLanguageGateIgnoresTheChunksOfOtherAccounts(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Title: "Agentbench: manche Dinge laufen nur einmal", Body: "Der Lauf ist nicht wiederholbar."})
	const prompt = "was ist stand der dinge"
	// Account 2 says "dinge" in every other chunk, in far more text than account
	// 1 has. Account 1 never says it.
	seedChunks(t, s, "mine", 1, ownChunks, prose)
	seedChunks(t, s, "theirs", 2, 4*ownChunks, func(i int) string {
		if i%2 == 0 {
			return fmt.Sprintf("die dinge laufen %d", i)
		}
		return prose(i)
	})

	if hits := hitTitles(relevantFor(t, s, prompt, "person:1", "")); len(hits) != 1 {
		t.Errorf("a word only another account uses held a delivery back from account 1: %v", hits)
	}
	// The same text decides for the account that owns it.
	if hits := hitTitles(relevantFor(t, s, prompt, "person:2", "")); len(hits) != 0 {
		t.Errorf("a word that is in half of an account's own text delivered %v", hits)
	}
}

// Foreign chunks that sit between two of the reader's own are read past, never
// counted. The collector uploads sessions in batches, so a session's chunks are
// routinely interleaved with other accounts', and the span a session occupies in
// the index encloses them.
func TestLanguageGateDoesNotCountForeignChunksInsideTheReadersSpan(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Title: "Agentbench: manche Dinge laufen nur einmal", Body: "Der Lauf ist nicht wiederholbar."})
	mine := seedChunks(t, s, "mine", 1, ownChunks/2, prose)
	theirs := seedChunks(t, s, "theirs", 2, ownChunks, func(i int) string { return fmt.Sprintf("die dinge laufen %d", i) })
	appendChunks(t, s, mine, ownChunks/2, ownChunks/2, prose)
	appendChunks(t, s, theirs, ownChunks, 10, func(i int) string { return "dinge" })

	var lo, hi, other int64
	if err := s.db.QueryRow(`SELECT MIN(id), MAX(id) FROM session_chunks WHERE session_id = ?`, mine).Scan(&lo, &hi); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_chunks WHERE session_id = ? AND id BETWEEN ? AND ?`, theirs, lo, hi).Scan(&other); err != nil || other < ownChunks/2 {
		t.Fatalf("the test does not interleave: %d foreign chunks inside the span of the reader's session (%v)", other, err)
	}
	if hits := hitTitles(relevantFor(t, s, "was ist stand der dinge", "person:1", "")); len(hits) != 1 {
		t.Errorf("foreign chunks inside the reader's span were counted: %v", hits)
	}
}

// An account without enough text of its own has no language gate, and other
// accounts' text does not stand in for it.
func TestLanguageGateIsOffForAnAccountWithTooLittleOwnText(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Title: "Agentbench: manche Dinge laufen nur einmal", Body: "Der Lauf ist nicht wiederholbar."})
	seedChunks(t, s, "theirs", 2, 4*ownChunks, func(i int) string { return fmt.Sprintf("die dinge laufen %d", i) })
	seedChunks(t, s, "new", 3, minOwnChunks-1, prose)
	if hits := hitTitles(relevantFor(t, s, "was ist stand der dinge", "person:3", "")); len(hits) != 1 {
		t.Errorf("an account with %d chunks of its own was gated by what the others say: %v", minOwnChunks-1, hits)
	}
	// And it is on again once there is enough.
	seedChunks(t, s, "grown", 3, ownChunks, func(i int) string { return fmt.Sprintf("die dinge laufen %d", i) })
	if hits := hitTitles(relevantFor(t, s, "was ist stand der dinge", "person:3", "")); len(hits) != 0 {
		t.Errorf("the gate stayed off after the account had text of its own: %v", hits)
	}
}

// Sessions from before accounts existed (account 0) belong to the instance
// owner, as they do everywhere else.
func TestLanguageGateCountsLegacySessionsForTheOwner(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPerson("lena"); err != nil {
		t.Fatal(err)
	}
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Title: "Agentbench: manche Dinge laufen nur einmal", Body: "Der Lauf ist nicht wiederholbar."})
	seedChunks(t, s, "old", 1, ownChunks, func(i int) string { return fmt.Sprintf("die dinge laufen %d", i) })
	if _, err := s.db.Exec(`UPDATE sessions SET account_id = 0`); err != nil {
		t.Fatal(err)
	}
	if hits := hitTitles(relevantFor(t, s, "was ist stand der dinge", "person:1", "")); len(hits) != 0 {
		t.Errorf("the owner's legacy sessions were not counted: %v", hits)
	}
	if hits := hitTitles(relevantFor(t, s, "was ist stand der dinge", "person:2", "")); len(hits) != 1 {
		t.Errorf("another account was gated by the owner's legacy sessions: %v", hits)
	}
}

// The verdict is the count of the reader's own chunks against the ceiling, in
// every combination of own and foreign use. The look at the whole index that
// spares a rare word the count must never change an answer: a word is judged by
// the reader's chunks alone, whatever the others do with it.
func TestLanguageGateVerdictDependsOnOwnChunksAlone(t *testing.T) {
	s := openTest(t)
	seedChunks(t, s, "mine", 1, ownChunks, func(i int) string {
		switch {
		case i < 5:
			return "alphaw " + prose(i) // five own chunks, nobody else
		case i >= 100 && i < 200:
			return "gammaw " + prose(i) // a hundred own chunks, nobody else
		case i >= 300 && i < 305:
			return "deltaw " + prose(i) // five own chunks, many foreign
		case i >= 400 && i < 430:
			return "epsilonw " + prose(i) // thirty own chunks, many foreign
		}
		return prose(i)
	})
	seedChunks(t, s, "theirs", 2, ownChunks, func(i int) string {
		if i < 500 {
			// Many foreign chunks: betaw is never the reader's, the other two
			// are the reader's as well.
			return "betaw deltaw epsilonw " + prose(i)
		}
		return prose(i)
	})
	g := s.newLanguageGate("person:1")
	if err := g.prepare(); err != nil || !g.on || g.ceiling != minOwnCeiling {
		t.Fatalf("gate = on %v ceiling %d (%v), want it on with the floor of %d", g.on, g.ceiling, err, minOwnCeiling)
	}
	for word, want := range map[string]bool{
		"alphaw":   true,  // 5 own, 5 anywhere: spared the count, unusual
		"betaw":    true,  // 0 own, 500 anywhere: counted, unusual
		"gammaw":   false, // 100 own: common
		"deltaw":   true,  // 5 own, 505 anywhere: counted, unusual
		"epsilonw": false, // 30 own, 530 anywhere: common
		"nowhere":  true,
	} {
		got, err := g.unusual(word)
		if err != nil || got != want {
			t.Errorf("unusual(%q) = %v %v, want %v", word, got, err, want)
		}
	}
}

// The sample is bounded: the most recent sessions of the account, as many as it
// takes to reach the chunk target and never more than the session cap, so what a
// prompt costs does not grow with how many sessions an account has.
func TestLanguageGateSampleIsBounded(t *testing.T) {
	s := openTest(t)
	for i := range maxLanguageSessions + 20 {
		seedChunks(t, s, fmt.Sprintf("s%03d", i), 1, 300, prose)
	}
	g := s.newLanguageGate(reader)
	if err := g.prepare(); err != nil {
		t.Fatal(err)
	}
	if len(g.spans) != maxLanguageSessions {
		t.Errorf("sampled %d sessions, want the cap of %d", len(g.spans), maxLanguageSessions)
	}
	if !g.on || g.ceiling != minOwnCeiling {
		t.Errorf("gate = on %v ceiling %d, want it on with the floor of %d", g.on, g.ceiling, minOwnCeiling)
	}
	// The newest sessions are the ones sampled.
	var newest int64
	if err := s.db.QueryRow(`SELECT id FROM sessions ORDER BY last_seen_at DESC, id DESC LIMIT 1`).Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if g.spans[0].session != newest {
		t.Errorf("first sampled session %d, want the newest %d", g.spans[0].session, newest)
	}
}

func TestLanguageGateCeilingScalesWithTheSample(t *testing.T) {
	s := openTest(t)
	if g := s.newLanguageGate(reader); func() bool { _ = g.prepare(); return g.on }() {
		t.Fatalf("an account with no text must have no gate")
	}
	if g := s.newLanguageGate(""); func() bool { _ = g.prepare(); return g.on }() {
		t.Fatalf("a caller with no account must have no gate")
	}
	seedChunks(t, s, "big", 1, 40000, prose)
	g := s.newLanguageGate(reader)
	if err := g.prepare(); err != nil {
		t.Fatal(err)
	}
	if want := int(40000 * ownChunkShare); !g.on || g.ceiling != want {
		t.Errorf("gate = on %v ceiling %d, want %d", g.on, g.ceiling, want)
	}
}

func TestClaimContextDeliveriesReturnsOnlyWhatWasNew(t *testing.T) {
	s := openTest(t)
	items := []ContextDelivery{{KnowledgeID: 1}, {KnowledgeID: 2}, {Term: "word"}}
	got, err := s.ClaimContextDeliveries("a", "s", DeliveredByRelevance, items)
	if err != nil || len(got) != 3 {
		t.Fatalf("first claim = %v %v", got, err)
	}
	got, err = s.ClaimContextDeliveries("a", "s", DeliveredByRelevance, append(items, ContextDelivery{KnowledgeID: 3}))
	if err != nil || len(got) != 1 || got[0].KnowledgeID != 3 {
		t.Errorf("second claim = %v %v, want only entry 3", got, err)
	}
	// A different session or account starts empty.
	for _, k := range [][2]string{{"a", "t"}, {"b", "s"}} {
		got, err = s.ClaimContextDeliveries(k[0], k[1], DeliveredByRelevance, items)
		if err != nil || len(got) != 3 {
			t.Errorf("claim for %v = %v %v", k, got, err)
		}
	}
}

// Without a usable session key nothing is written and everything is new.
func TestClaimContextDeliveriesWithoutASessionRemembersNothing(t *testing.T) {
	s := openTest(t)
	items := []ContextDelivery{{KnowledgeID: 1}}
	for _, key := range []string{"", "has space", "tab\tbed", strings.Repeat("x", maxContextSessionKey+1), "bad\xffutf"} {
		for range 2 {
			got, err := s.ClaimContextDeliveries("a", key, DeliveredByRelevance, items)
			if err != nil || len(got) != 1 {
				t.Errorf("key %q: claim = %v %v", key, got, err)
			}
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_deliveries`).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows = %d %v, want none", n, err)
	}
}

// Rows age out where they are written, so the table stays bounded without a
// maintenance job.
func TestClaimContextDeliveriesForgetsOldSessions(t *testing.T) {
	s := openTest(t)
	old := time.Now().UTC().Add(-contextDeliveryRetention - time.Hour).Format(time.RFC3339)
	fresh := time.Now().UTC().Add(-contextDeliveryRetention + time.Hour).Format(time.RFC3339)
	for _, row := range []struct {
		session, at string
	}{{"old", old}, {"fresh", fresh}} {
		if _, err := s.db.Exec(`INSERT INTO context_deliveries(account_id, session_key, knowledge_id, term, via, delivered_at)
			VALUES('a', ?, 7, '', 'relevance', ?)`, row.session, row.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ClaimContextDeliveries("a", "new", DeliveredByRelevance, []ContextDelivery{{KnowledgeID: 1}}); err != nil {
		t.Fatal(err)
	}
	var sessions []string
	rows, err := s.db.Query(`SELECT session_key FROM context_deliveries ORDER BY session_key`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, k)
	}
	rows.Close()
	if strings.Join(sessions, ",") != "fresh,new" {
		t.Errorf("sessions = %v, want the expired one gone and the others kept", sessions)
	}
}

// Two prompts of one session handled at the same moment must not both be told
// an entry is new. On the runtime store, where writes are queued, exactly one
// caller gets each item.
func TestClaimContextDeliveriesGivesEachItemToOneCaller(t *testing.T) {
	s := runtimeDomainStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := s.ClaimContextDeliveries("a", "s", DeliveredByRelevance, []ContextDelivery{{KnowledgeID: 42}})
				if errors.Is(err, ErrWriterOperationsFull) {
					time.Sleep(time.Millisecond)
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				won += len(got)
				mu.Unlock()
				return
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Errorf("%d callers were told the entry was new, want exactly one", won)
	}
}

// The replay of archived prompts says which word fired each title, and it must
// not move the usage counters it is later judged by.
func TestProbeRelevanceNamesTheWordBehindEachHit(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "talk", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]Chunk, 900)
	for i := range chunks {
		text := fmt.Sprintf("das war ein langes gespräch über den server %d", i)
		if i == 0 {
			text = "wie ist der stand von ollama"
		}
		chunks[i] = Chunk{Seq: i, Role: "user", Text: text, Raw: "{}"}
	}
	if err := s.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	// Every prompt is replayed: the sample step would otherwise skip the one
	// that fires.
	probes, err := s.ProbeRelevance(900, 3)
	if err != nil {
		t.Fatal(err)
	}
	fired := 0
	for _, p := range probes {
		if len(p.Titles) != len(p.Terms) {
			t.Fatalf("titles %v and terms %v differ", p.Titles, p.Terms)
		}
		for _, term := range p.Terms {
			fired++
			if term != "ollama" {
				t.Errorf("term = %q", term)
			}
		}
	}
	if fired != 1 {
		t.Errorf("fired %d times, want once", fired)
	}
}

func TestCleanSessionKey(t *testing.T) {
	for key, want := range map[string]string{
		"01998a2b-0000-7000-8000-000000000000": "01998a2b-0000-7000-8000-000000000000",
		"":                                     "",
		" padded ":                             "",
	} {
		if got := CleanSessionKey(key); got != want {
			t.Errorf("CleanSessionKey(%q) = %q, want %q", key, got, want)
		}
	}
}
