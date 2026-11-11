package store

import (
	"fmt"
	"testing"
	"time"
	"unsafe"
)

func unsafeStringData(s string) *byte { return unsafe.StringData(s) }

// A verdict on a word is kept for a short while, per account: a second prompt
// with the word does not count it again. The count is a verdict, so a change in
// the text of the sessions shows once the cache is forgotten (or has aged).
func TestLanguageGateKeepsItsVerdictPerAccount(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)
	addEntry(t, s, Knowledge{Title: "Agentbench: manche Dinge laufen nur einmal", Body: "Der Lauf ist nicht wiederholbar."})
	const prompt = "was ist stand der dinge? wo stehen wir"
	seedChunks(t, s, "talk", 1, ownChunks, prose)
	if hits := hitTitles(relevantFor(t, s, prompt, reader, "")); len(hits) != 1 {
		t.Fatalf("a word nobody says must deliver, hits = %v", hits)
	}
	// The word becomes the reader's everyday word. Uploads do not forget verdicts
	// (see languageVerdictTTL), only a different sample or a reset does.
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "talk2", AccountID: 1})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]Chunk, ownChunks)
	for i := range chunks {
		text := fmt.Sprintf("die dinge laufen, nummer %d", i)
		chunks[i] = Chunk{Seq: i, Role: "user", Text: text, Raw: userLine("2026-10-01T10:00:00Z", text)}
	}
	if err := s.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	if hits := hitTitles(relevantFor(t, s, prompt, reader, "")); len(hits) != 1 {
		t.Errorf("the verdict was not kept: %v", hits)
	}
	s.ResetRelevanceCache()
	if hits := hitTitles(relevantFor(t, s, prompt, reader, "")); len(hits) != 0 {
		t.Errorf("after the cache was forgotten the word still delivered: %v", hits)
	}
}

func TestLanguageCacheAgesAndIsPerAccount(t *testing.T) {
	var c languageCache
	now := time.Now()
	c.storeVerdict(1, "wort", now, true)
	if v, ok := c.verdict(1, "wort", now.Add(languageVerdictTTL-time.Second)); !ok || !v {
		t.Fatalf("fresh verdict = %v, %v", v, ok)
	}
	if _, ok := c.verdict(1, "wort", now.Add(languageVerdictTTL+time.Second)); ok {
		t.Fatal("an old verdict was kept")
	}
	if _, ok := c.verdict(2, "wort", now); ok {
		t.Fatal("the verdict of one account answered for another")
	}
	c.storeSample(1, now, 7, true, 20, []sessionSpan{{session: 1, lo: 1, hi: 5}})
	if _, on, ceiling, spans, ok := c.sample(1, now.Add(time.Second)); !ok || !on || ceiling != 20 || len(spans) != 1 {
		t.Fatalf("sample = %v %v %v %v", on, ceiling, spans, ok)
	}
	if _, _, _, _, ok := c.sample(1, now.Add(languageSampleTTL+time.Second)); ok {
		t.Fatal("an old sample was kept")
	}
	// A sample that differs drops the verdicts made against the other one.
	c.storeVerdict(1, "wort", now, true)
	c.storeSample(1, now, 7, true, 40, nil)
	if _, ok := c.verdict(1, "wort", now); ok {
		t.Fatal("a verdict outlived the sample it was made against")
	}
}

// The event that drops the verdicts is the sample changing: other sessions in it,
// even when there are as many as before (a new session pushing the oldest out of
// a full sample), or another ceiling. A range that only grew, because chunks came
// in for a sampled session, is the same sample.
func TestLanguageCacheDropsVerdictsWhenTheSampleChanges(t *testing.T) {
	var c languageCache
	now := time.Now()
	spans := []sessionSpan{{session: 1, lo: 1, hi: 50}, {session: 2, lo: 51, hi: 90}}
	c.storeSample(1, now, 7, true, 20, spans)
	c.storeVerdict(1, "wort", now, true)

	grown := []sessionSpan{{session: 1, lo: 1, hi: 80}, {session: 2, lo: 51, hi: 120}}
	c.storeSample(1, now.Add(time.Minute), 7, true, 20, grown)
	if _, ok := c.verdict(1, "wort", now.Add(time.Minute)); !ok {
		t.Fatal("chunks that came in for the sampled sessions dropped the verdicts")
	}

	swapped := []sessionSpan{{session: 1, lo: 1, hi: 80}, {session: 3, lo: 200, hi: 230}}
	c.storeSample(1, now.Add(2*time.Minute), 7, true, 20, swapped)
	if _, ok := c.verdict(1, "wort", now.Add(2*time.Minute)); ok {
		t.Fatal("a verdict outlived a sample with another session in it")
	}
}

// A verdict's key is a piece cut out of the prompt; the cache keeps a copy, so
// that it does not hold the prompt in memory.
func TestLanguageCacheKeysDoNotPinThePrompt(t *testing.T) {
	var c languageCache
	prompt := "ein langer prompt " + string(make([]byte, 4096))
	term := prompt[3:7]
	c.storeVerdict(1, term, time.Now(), true)
	for key := range c.accounts[1].verdicts {
		if unsafeStringData(key) == unsafeStringData(term) {
			t.Fatal("the key shares its memory with the prompt")
		}
	}
}
