package actor

import "testing"

func TestFamilyAndHarnessKey(t *testing.T) {
	for harness, want := range map[string][2]string{
		"claude-code": {"Claude", "claude"}, "claude": {"Claude", "claude"}, "codex": {"Codex", "codex"},
		"opencode": {"OpenCode", "opencode"}, "": {"Agent", ""}, "gardener": {"Gardener", "gardener"},
	} {
		if got := Family(harness); got != want[0] {
			t.Errorf("Family(%q) = %q, want %q", harness, got, want[0])
		}
		if got := HarnessKey(harness); got != want[1] {
			t.Errorf("HarnessKey(%q) = %q, want %q", harness, got, want[1])
		}
	}
}

func TestModelLabel(t *testing.T) {
	for model, want := range map[string]string{
		"claude-opus-5-5": "Opus 5.5", "claude-fable-5-1": "Fable 5.1", "claude-sonnet-4-5-20250929": "Sonnet 4.5",
		"claude-3-5-sonnet-20241022": "Sonnet 3.5", "gpt-6-sol": "gpt-6-sol", "": "",
	} {
		if got := ModelLabel(model); got != want {
			t.Errorf("ModelLabel(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestObserveModelReadsClaudeAndCodexLines(t *testing.T) {
	for _, c := range []struct {
		line, model, at string
	}{
		{`{"type":"assistant","timestamp":"2026-11-09T08:00:00.123Z","message":{"model":"claude-opus-5-5","content":[]}}`, "claude-opus-5-5", "2026-11-09T08:00:00Z"},
		{`{"timestamp":"2026-11-09T09:00:00+01:00","type":"turn_context","payload":{"model":"gpt-6-sol","cwd":"/x"}}`, "gpt-6-sol", "2026-11-09T08:00:00Z"},
		// Kein Agent bei der Arbeit: erfundene Antworten, Prüfläufe, Sub-Agenten.
		{`{"type":"assistant","timestamp":"2026-11-09T08:00:00Z","message":{"model":"<synthetic>"}}`, "", ""},
		{`{"timestamp":"2026-11-09T08:00:00Z","type":"turn_context","payload":{"model":"codex-auto-review"}}`, "", ""},
		{`{"type":"assistant","isSidechain":true,"timestamp":"2026-11-09T08:00:00Z","message":{"model":"claude-haiku-5-5"}}`, "", ""},
		// Andere Zeilen, fehlende Zeit, kaputtes JSON.
		{`{"type":"user","timestamp":"2026-11-09T08:00:00Z","message":{"model":"claude-opus-5-5"}}`, "", ""},
		{`{"type":"assistant","message":{"model":"claude-opus-5-5"}}`, "", ""},
		{`{"type":"assistant","model"`, "", ""},
		// Kein Modellname: Leerzeichen, Markup.
		{`{"type":"assistant","timestamp":"2026-11-09T08:00:00Z","message":{"model":"opus <b>x</b>"}}`, "", ""},
	} {
		o, ok := ObserveModel(c.line)
		if ok != (c.model != "") || o.Model != c.model || o.At != c.at {
			t.Errorf("ObserveModel(%s) = %+v %v", c.line, o, ok)
		}
	}
}

// Kriterium 2: ein Modellwechsel in der Session wird mit Zeitpunkten gehalten,
// und ein Eintrag bekommt das Modell, das bei seiner Entstehung aktiv war.
func TestFoldKeepsSwitchesAndModelAtPicksTheActiveOne(t *testing.T) {
	segs := Fold(nil, []Observation{
		{"claude-opus-5-5", "2026-11-09T08:00:00Z"},
		{"claude-opus-5-5", "2026-11-09T08:10:00Z"},
		{"claude-sonnet-5-5", "2026-11-09T09:00:00Z"},
	})
	segs = Fold(segs, []Observation{
		{"claude-sonnet-5-5", "2026-11-09T09:30:00Z"},
		{"claude-opus-5-5", "2026-11-09T10:00:00Z"},
	})
	want := []Segment{
		{"claude-opus-5-5", "2026-11-09T08:00:00Z", "2026-11-09T08:10:00Z"},
		{"claude-sonnet-5-5", "2026-11-09T09:00:00Z", "2026-11-09T09:30:00Z"},
		{"claude-opus-5-5", "2026-11-09T10:00:00Z", "2026-11-09T10:00:00Z"},
	}
	if len(segs) != len(want) {
		t.Fatalf("segments: %+v", segs)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Errorf("segment %d = %+v, want %+v", i, segs[i], want[i])
		}
	}
	for at, model := range map[string]string{
		"2026-11-09T07:00:00Z": "claude-opus-5-5", "2026-11-09T08:30:00Z": "claude-opus-5-5",
		"2026-11-09T09:00:00Z": "claude-sonnet-5-5", "2026-11-09T09:45:00Z": "claude-sonnet-5-5",
		"2026-11-09T11:00:00Z": "claude-opus-5-5",
	} {
		if got := ModelAt(segs, at); got != model {
			t.Errorf("ModelAt(%s) = %q, want %q", at, got, model)
		}
	}
	if ModelAt(nil, "2026-11-09T08:00:00Z") != "" {
		t.Error("model without segments")
	}
}

func TestReplacesAndValidKind(t *testing.T) {
	if !Replaces(Criterion) || !Replaces(RequestDone) || !Replaces(Reaction) || Replaces(Request) || Replaces(Vote) {
		t.Error("Replaces")
	}
	if ValidKind("session") || !ValidKind(Vote) {
		t.Error("ValidKind")
	}
}
