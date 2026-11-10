package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// addSessions writes n sessions of robin's in the project whose one chunk says text.
func addSessions(t *testing.T, f *accessAPIFixture, prefix string, n int, text string) {
	t.Helper()
	for i := range n {
		id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: fmt.Sprintf("%s-%d", prefix, i), AccountID: 1, Scope: scope.Axes{Project: accProject}})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: text, Raw: "{}"}}); err != nil {
			t.Fatal(err)
		}
	}
}

// The hint is for codenames. Everyday words that no knowledge entry names — the
// project's entries are technical and mostly English — are not codenames, even
// when a session contains them (PAIN-70: "wohin", "hattest").
func TestRelevantHintSkipsEverydayWords(t *testing.T) {
	f := accessAPI(t, true)
	addSessions(t, f, "alltag", 1, "wohin hattest du die Datei gelegt, trag das ein")
	for _, prompt := range []string{"wohin damit", "hattest du das schon", "wohin hattest du das gelegt", "trag das bitte ein"} {
		if out := f.expect(t, "robin", 200, "GET", relevantURL(url.QueryEscape(prompt), ""), nil); strings.Contains(out, "[hint]") {
			t.Errorf("%q: %s", prompt, out)
		}
	}
}

// A word that many of the reader's sessions use is vocabulary, not a codename:
// no entry names it, and still it gets no hint. A codename that came up in one
// session keeps its hint.
func TestRelevantHintSkipsWordsManySessionsUse(t *testing.T) {
	f := accessAPI(t, true)
	addSessions(t, f, "oft", maxHintSources+1, "die schublade klemmt wieder")
	addSessions(t, f, "einmal", 1, "zorbulax ist der neue Host")
	if out := f.expect(t, "robin", 200, "GET", relevantURL("was+ist+mit+der+schublade", ""), nil); strings.Contains(out, "[hint]") {
		t.Errorf("a word of %d sessions got a hint: %s", maxHintSources+1, out)
	}
	if out := f.expect(t, "robin", 200, "GET", relevantURL("was+ist+zorbulax", ""), nil); !strings.Contains(out, `"zorbulax" also came up in session`) {
		t.Errorf("the codename lost its hint: %q", out)
	}
	// The count is of what the reader may read: mia sees none of robin's
	// sessions, so for her the word is not in many, and not in any she may open.
	if out := f.expect(t, "mia", 200, "GET", relevantURL("was+ist+mit+der+schublade", ""), nil); strings.Contains(out, "[hint]") {
		t.Errorf("mia was pointed at a session she cannot read: %s", out)
	}
}
