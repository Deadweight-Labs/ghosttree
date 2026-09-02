package agentbench

import (
	"errors"
	"testing"
)

func TestParseFormTakesTheLastBlock(t *testing.T) {
	out := "denke nach\n" +
		"```agentbench-form\n{\"slots\":{\"tolerance\":{\"integer\":30}}}\n```\n" +
		"korrigiere mich\n" +
		"```agentbench-form\n{\"slots\":{\"tolerance\":{\"integer\":60}}}\n```\n"
	form, err := ParseForm(out)
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	value, ok := form.Slots["tolerance"]
	if !ok || value.Integer == nil || *value.Integer != 60 {
		t.Fatalf("expected the last block to win, got %+v", form.Slots)
	}
}

func TestParseFormReportsMissingBlock(t *testing.T) {
	if _, err := ParseForm("keine antwort"); !errors.Is(err, ErrNoForm) {
		t.Fatalf("expected ErrNoForm, got %v", err)
	}
}

func TestParseFormReportsUnterminatedBlock(t *testing.T) {
	if _, err := ParseForm("```agentbench-form\n{\"slots\":{}}"); !errors.Is(err, ErrNoForm) {
		t.Fatalf("expected ErrNoForm for an unterminated block, got %v", err)
	}
}

func TestSlotValueAbstentionIsDistinctFromFalse(t *testing.T) {
	no := false
	answered := SlotValue{Boolean: &no}
	if answered.IsAbstention() {
		t.Fatal("a boolean false is an answer, not an abstention")
	}
	if !(SlotValue{}).IsAbstention() {
		t.Fatal("an empty slot value is an abstention")
	}
}

func TestParseFormAcceptsAnEmptySlotMap(t *testing.T) {
	form, err := ParseForm("```agentbench-form\n{\"explanation\":\"weiss nicht\"}\n```")
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if form.Slots == nil {
		t.Fatal("Slots must never be nil, callers index into it")
	}
}

func TestParseFormAcceptsBareValues(t *testing.T) {
	// Genau die Form, in der die Agenten im ersten Piloten geantwortet haben.
	output := "fertig\n```agentbench-form\n" +
		`{"slots":{"still_open":true,"impl_file":"internal/notifier/webhook/webhook.go","count":3}}` +
		"\n```"

	form, err := ParseForm(output)
	if err != nil {
		t.Fatalf("a bare value is a valid answer: %v", err)
	}
	if form.Slots["still_open"].Boolean == nil || !*form.Slots["still_open"].Boolean {
		t.Fatalf("bare true must land in Boolean: %+v", form.Slots["still_open"])
	}
	if form.Slots["impl_file"].Path == nil ||
		*form.Slots["impl_file"].Path != "internal/notifier/webhook/webhook.go" {
		t.Fatalf("a bare string must be readable as a path: %+v", form.Slots["impl_file"])
	}
	if form.Slots["count"].Integer == nil || *form.Slots["count"].Integer != 3 {
		t.Fatalf("a bare number must land in Integer: %+v", form.Slots["count"])
	}
}

func TestParseFormStillAcceptsTheTypedForm(t *testing.T) {
	output := "```agentbench-form\n" +
		`{"slots":{"f":{"boolean":false},"g":{"path":"a/b.go"}}}` + "\n```"

	form, err := ParseForm(output)
	if err != nil {
		t.Fatal(err)
	}
	if form.Slots["f"].Boolean == nil || *form.Slots["f"].Boolean {
		t.Fatalf("the documented form must keep working: %+v", form.Slots["f"])
	}
	if form.Slots["g"].Path == nil || *form.Slots["g"].Path != "a/b.go" {
		t.Fatalf("the documented form must keep working: %+v", form.Slots["g"])
	}
}

func TestParseFormTreatsNullAsAbstention(t *testing.T) {
	form, err := ParseForm("```agentbench-form\n" + `{"slots":{"f":null}}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if !form.Slots["f"].IsAbstention() {
		t.Fatalf("an explicit null is a withheld answer, not an error: %+v", form.Slots["f"])
	}
}

// Der Fall aus run-budget40: Der Agent gibt sein Formular ab, bekommt danach
// das Ergebnis eines Subagenten zurueck und kommentiert es. Die Schlussausgabe
// traegt kein Formular mehr — die Antwort stand aber vollstaendig da. Zwei
// richtige ghosttree-Antworten sind so aus der Wertung gefallen.
func TestFormFromFallsBackToAnEarlierFormWhenTheLastWordIsProse(t *testing.T) {
	transcript := Transcript{
		Output:   "The background agent's findings match what I already verified — no changes needed.",
		FormText: "```agentbench-form\n{\"slots\": {\"func_name\": {\"string\": \"AdoptedArtifactID\"}}}\n```",
	}

	form, err := FormFrom(transcript)
	if err != nil {
		t.Fatalf("das Formular stand im Strom: %v", err)
	}
	if got := form.Slots["func_name"].String; got == nil || *got != "AdoptedArtifactID" {
		t.Fatalf("Slot nicht gelesen: %+v", form.Slots)
	}
}

// Die Reihenfolge ist nicht beliebig: Korrigiert ein Agent seine Antwort, gilt
// das letzte Wort. Der frueher gesehene Block kommt nur zum Zug, wenn die
// Schlussausgabe ueberhaupt keines traegt.
func TestFormFromPrefersTheFinalMessage(t *testing.T) {
	transcript := Transcript{
		Output:   "```agentbench-form\n{\"slots\": {\"func_name\": {\"string\": \"richtig\"}}}\n```",
		FormText: "```agentbench-form\n{\"slots\": {\"func_name\": {\"string\": \"verworfen\"}}}\n```",
	}

	form, err := FormFrom(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if got := form.Slots["func_name"].String; got == nil || *got != "richtig" {
		t.Fatalf("die Schlussausgabe hat Vorrang: %+v", form.Slots)
	}
}

func TestFormFromReportsNoFormWhenTheAgentNeverGaveOne(t *testing.T) {
	if _, err := FormFrom(Transcript{Output: "Ich habe nichts gefunden."}); !errors.Is(err, ErrNoForm) {
		t.Fatalf("eine echte Enthaltung bleibt eine Enthaltung: %v", err)
	}
}
