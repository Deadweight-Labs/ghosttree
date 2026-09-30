package store

import (
	"testing"
	"time"
)

func recent(minutesAgo int) string {
	return time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute).Format(time.RFC3339)
}

// AC-2 von REQ-348, die Frage, um die es geht: wer hat in den letzten N
// Minuten an diesem Pfad gearbeitet — ohne ein Transkript zu lesen.
func TestWhoTouchedThisPathRecently(t *testing.T) {
	s := openTest(t)
	if err := s.RecordPathActivity([]PathActivity{
		{Project: "p", SessionExternalID: "sess-codex", Checkout: "/repo",
			Tool: "Edit", Path: "internal/store/x.go", Writes: true,
			Quality: ActivityReported, At: recent(2)},
		{Project: "p", SessionExternalID: "sess-alt", Checkout: "/repo",
			Tool: "Edit", Path: "internal/store/x.go", Writes: true,
			Quality: ActivityReported, At: recent(300)},
		{Project: "p", SessionExternalID: "sess-codex", Checkout: "/repo",
			Tool: "Read", Path: "internal/store/y.go",
			Quality: ActivityIntent, At: recent(1)},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	got, err := s.PathActivitySince("p", "internal/store/x.go", ActivityWindow(30), "")
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 1 || got[0].SessionExternalID != "sess-codex" {
		t.Fatalf("want only the recent touch by sess-codex, got %+v", got)
	}
}

// Ohne Selbstausschluss meldet ein Agent sich selbst als Konfliktpartner —
// die häufigste Datei, an der jemand arbeitet, ist die eigene.
func TestAnAgentIsNotItsOwnConflict(t *testing.T) {
	s := openTest(t)
	if err := s.RecordPathActivity([]PathActivity{
		{Project: "p", SessionExternalID: "ich", Checkout: "/repo",
			Tool: "Edit", Path: "a.go", Writes: true, Quality: ActivityReported, At: recent(1)},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PathActivitySince("p", "a.go", ActivityWindow(30), "ich")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("an agent must not be reported as its own conflict: %+v", got)
	}
}

// AC-3 und das Kriterium aus v1 §6: die vier Aussagequalitäten sind benannt
// und werden unterschieden gespeichert. Eine Absicht ist kein Ergebnis.
func TestTheFourActivityQualitiesAreDistinct(t *testing.T) {
	s := openTest(t)
	for _, q := range []string{ActivityIntent, ActivityReported, ActivityObserved, ActivityUnattached} {
		if err := s.RecordPathActivity([]PathActivity{
			{Project: "p", SessionExternalID: "sess-a", Tool: "Edit", Path: "a.go",
				Quality: q, At: recent(1)},
		}); err != nil {
			t.Fatalf("record %s: %v", q, err)
		}
	}
	got, err := s.PathActivitySince("p", "a.go", ActivityWindow(30), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("the four qualities must be stored apart, got %d rows", len(got))
	}
	seen := map[string]bool{}
	for _, a := range got {
		seen[a.Quality] = true
	}
	if len(seen) != 4 {
		t.Fatalf("qualities collapsed into %v", seen)
	}
}

// Eine erfundene Qualität wird abgewiesen, statt still neben den gültigen zu
// landen — sonst steht dort irgendwann "maybe" und niemand weiß, was es heißt.
func TestAnInventedQualityIsRejected(t *testing.T) {
	s := openTest(t)
	if err := s.RecordPathActivity([]PathActivity{
		{SessionExternalID: "sess-a", Tool: "Edit", Path: "a.go", Quality: "vielleicht"},
	}); err == nil {
		t.Fatal("an invented activity quality must be rejected")
	}
}

// AC-4: derselbe Pfad im selben Checkout ist ein anderes Risiko als derselbe
// Pfad in zwei Worktrees, und ein FEHLENDER Checkout ist keine
// Konfliktfreiheit.
func TestConflictClassificationSeparatesCheckoutFromWorktree(t *testing.T) {
	cases := []struct{ mine, theirs, want string }{
		{"/repo", "/repo", ConflictSameCheckout},
		{"/repo", "/repo/.worktrees/feat", ConflictOtherWorktree},
		{"", "/repo", ConflictUnknownScope},
		{"/repo", "", ConflictUnknownScope},
	}
	for _, c := range cases {
		if got := ClassifyConflict(c.mine, c.theirs); got != c.want {
			t.Errorf("ClassifyConflict(%q,%q) = %q, want %q", c.mine, c.theirs, got, c.want)
		}
	}
	// Und die Beschreibung sagt, was zu tun wäre — eine Warnung ohne
	// Handlungsweg wird zur Kenntnis genommen und dann ignoriert.
	if d := DescribeConflict(ConflictUnknownScope); d == "" ||
		!contains(d, "not proof that nothing collides") {
		t.Errorf("unknown scope must not read like safety: %q", d)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// Wiederholtes Einlesen desselben Transkripts darf die Historie nicht
// verdoppeln — der Collector liest Dateien mehrfach.
func TestRecordingTheSameTouchTwiceKeepsOneRow(t *testing.T) {
	s := openTest(t)
	e := PathActivity{Project: "p", SessionExternalID: "sess-a", Tool: "Edit",
		Path: "a.go", Quality: ActivityIntent, At: recent(1)}
	for i := 0; i < 2; i++ {
		if err := s.RecordPathActivity([]PathActivity{e}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	got, err := s.PathActivitySince("p", "a.go", ActivityWindow(30), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("re-reading the same transcript line doubled the history: %d rows", len(got))
	}
}

// Die Zusammenfassung ist deterministisch und ohne Modell. v1 §6: die Auswahl
// eines Zeitfensters erfordert kein mitlaufendes LLM, und eine erzählte
// Zusammenfassung wäre unprüfbar.
func TestActivitySummaryIsDerivedNotNarrated(t *testing.T) {
	got := SummarizeActivity([]PathActivity{
		{Path: "a.go", Writes: true, Quality: ActivityReported},
		{Path: "b.go", Writes: true, Quality: ActivityReported},
		{Path: "c.go", Quality: ActivityIntent},
		{Path: "d.go", Quality: ActivityIntent},
		// Eine blosse Absicht zählt nicht als bestätigte Änderung.
		{Path: "e.go", Writes: true, Quality: ActivityIntent},
	})
	if !contains(got, "a.go") || !contains(got, "and 2 more") {
		t.Errorf("summary should name the first paths and count the rest: %q", got)
	}
	if !contains(got, "2 confirmed writes") {
		t.Errorf("an intent must not count as a confirmed write: %q", got)
	}
	if SummarizeActivity(nil) != "" {
		t.Error("no activity must summarise to nothing, not to a reassuring sentence")
	}
}
