package collector

import (
	"encoding/json"
	"testing"
)

// Der Pfad muss aus dem Werkzeugaufruf herauskommen. Im Archivtext steht er
// nur als abgeschnittenes Argument — der Werkzeugname ist damit auswertbar,
// der Pfad nicht.
func TestToolPathTouchesExtractsTheFileNotThePattern(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"tool_use","name":"Edit","input":{"file_path":"./internal/store/x.go"}},
		{"type":"tool_use","name":"Read","input":{"file_path":"internal/store/y.go"}},
		{"type":"tool_use","name":"Grep","input":{"pattern":"*.go","path":"internal"}},
		{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.go"}},
		{"type":"tool_use","name":"Bash","input":{"command":"rm -rf /tmp/x"}},
		{"type":"text","text":"nur Prosa"}
	]`)
	got := ToolPathTouches(raw)

	byPath := map[string]PathTouch{}
	for _, tch := range got {
		byPath[tch.Path] = tch
	}
	if _, ok := byPath["internal/store/x.go"]; !ok {
		t.Errorf("a relative path must be normalised: %+v", got)
	}
	if _, ok := byPath["internal/store/y.go"]; !ok {
		t.Errorf("a read must be recorded too: %+v", got)
	}
	if _, ok := byPath["internal"]; !ok {
		t.Errorf("a Grep path is a real path: %+v", got)
	}
	// Ein Suchmuster ist kein Pfad. Es als Datei zu verbuchen erzeugt
	// Warnungen über Dateien, die es nicht gibt.
	for p := range byPath {
		if p == "**/*.go" || p == "*.go" {
			t.Errorf("a glob pattern was recorded as a file: %+v", got)
		}
	}
	// Ein unbekanntes Werkzeug liefert lieber nichts als einen geratenen
	// Pfad: eine Konfliktwarnung auf einer erfundenen Datei ist schlimmer
	// als keine.
	for _, tch := range got {
		if tch.Tool == "Bash" {
			t.Errorf("an unknown tool must not yield a guessed path: %+v", tch)
		}
	}
}

// Was der Aufruf belegt, ist ABSICHT. Ein gestarteter Edit ist keine
// erfolgte Änderung.
func TestATouchFromACallIsIntentNotResult(t *testing.T) {
	got := ToolPathTouches(json.RawMessage(
		`[{"type":"tool_use","name":"Write","input":{"file_path":"a.go"}}]`))
	if len(got) != 1 {
		t.Fatalf("want one touch, got %+v", got)
	}
	if got[0].Quality != QualityIntent {
		t.Fatalf("a tool call is an intent, got %q", got[0].Quality)
	}
	if !TouchWrites("Write") || TouchWrites("Read") {
		t.Error("reading and writing the same path are different risks")
	}
}

// Kaputte Eingaben dürfen nichts erfinden.
func TestBrokenInputYieldsNothing(t *testing.T) {
	for _, raw := range []string{`nicht json`, `[]`, `[{"type":"tool_use","name":"Edit"}]`,
		`[{"type":"tool_use","name":"Edit","input":{"file_path":""}}]`} {
		if got := ToolPathTouches(json.RawMessage(raw)); len(got) != 0 {
			t.Errorf("%s yielded %+v", raw, got)
		}
	}
}

// AC-5 von REQ-348: ein Pfad darf nicht an der Kürzung verlorengehen.
//
// Der Archivtext kürzt Argumente auf 600 Zeichen (maxToolCallArgs). Bei einem
// Write mit großem content steht der Pfad danach nicht mehr drin — wer die
// Aktivität aus dem TEXT zöge, verlöre ihn stillschweigend. Die Extraktion
// liest deshalb das rohe JSON, nicht den gekürzten Text.
func TestAPathSurvivesEvenWhenTheArchivedTextWouldTruncateIt(t *testing.T) {
	huge := make([]byte, 5000)
	for i := range huge {
		huge[i] = 'x'
	}
	input, err := json.Marshal(map[string]string{
		"content":   string(huge),
		"file_path": "internal/store/wichtig.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	block, err := json.Marshal([]map[string]any{
		{"type": "tool_use", "name": "Write", "input": json.RawMessage(input)},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Der archivierte Text verliert den Pfad — das ist der Ausgangszustand.
	archived := claudeToolCallText(block)
	if len(archived) > maxToolCallArgs+64 {
		t.Fatalf("the archived text should be truncated, got %d chars", len(archived))
	}
	// Belegen, dass er wirklich weg ist: sonst prüft der Test nichts.
	if contains(archived, "wichtig.go") {
		t.Fatal("precondition failed: the path still fits in the archived text")
	}

	// Die Extraktion findet ihn trotzdem.
	got := ToolPathTouches(block)
	if len(got) != 1 || got[0].Path != "internal/store/wichtig.go" {
		t.Fatalf("the path must survive truncation: %+v", got)
	}
	if got[0].Quality != QualityIntent {
		t.Fatalf("a write call is still only an intent: %q", got[0].Quality)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
