package store

import (
	"fmt"
	"testing"
	"time"
)

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
	// The word becomes the reader's everyday word, behind the back of the cache
	// (a direct write: appendChunks would forget it on purpose).
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
