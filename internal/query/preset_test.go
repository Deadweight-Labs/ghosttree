package query

import (
	"strings"
	"testing"
)

func TestBuiltinPresetsAreValidAndNamedForTheirArguments(t *testing.T) {
	want := map[string][]string{
		"pr-ergebnis":  {"pr"},
		"branch-offen": {"branch"},
		"ordner":       {"area"},
		"seit":         {"since"},
		"fuer-mich":    nil,
	}
	if len(Builtin) != len(want) {
		t.Fatalf("builtins: %d", len(Builtin))
	}
	for _, p := range Builtin {
		args, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected builtin %s", p.Name)
			continue
		}
		if strings.Join(p.Args, ",") != strings.Join(args, ",") {
			t.Errorf("%s args = %v, want %v", p.Name, p.Args, args)
		}
		if err := checkPreset(p.Name, p.Queries); err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
		if p.Description == "" {
			t.Errorf("%s has no description", p.Name)
		}
	}
}

func TestExpandFillsPlaceholdersAndRejectsInjection(t *testing.T) {
	lines, err := Expand([]string{"pr:{pr} kind:pain"}, map[string]string{"pr": "12"})
	if err != nil || lines[0] != "pr:12 kind:pain" {
		t.Fatalf("expand: %v %v", lines, err)
	}
	for name, args := range map[string]map[string]string{
		"missing":    {},
		"empty":      {"pr": "  "},
		"unknown":    {"pr": "1", "branch": "x"},
		"injection":  {"pr": "1 is:open"},
		"quote":      {"pr": `1"`},
		"newline":    {"pr": "1\nis:open"},
		"controlchr": {"pr": "1\x07"},
	} {
		if _, err := Expand([]string{"pr:{pr}"}, args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Ein Wert mit Doppelpunkt bleibt ein Wert, kein neuer Qualifier.
	lines, err = Expand([]string{"branch:{branch}"}, map[string]string{"branch": "feat/x:y"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse(lines[0], testNow)
	if err != nil || len(q.Terms) != 1 || q.Terms[0].Values[0] != "feat/x:y" {
		t.Fatalf("branch value: %+v %v", q, err)
	}
}

func TestCheckPresetRejectsWhatCannotRun(t *testing.T) {
	for name, c := range map[string]struct {
		name  string
		lines []string
	}{
		"bad name":            {"Bad Name", []string{"is:open"}},
		"empty name":          {"", []string{"is:open"}},
		"no lines":            {"x", nil},
		"too many":            {"x", []string{"a", "b", "c", "d", "e", "f"}},
		"empty line":          {"x", []string{" "}},
		"unknown qualifier":   {"x", []string{"flavor:{pr}"}},
		"unknown value":       {"x", []string{"is:oen"}},
		"unknown placeholder": {"x", []string{"area:{folder}"}},
		"control":             {"x", []string{"is:open\x07"}},
		"too long":            {"x", []string{strings.Repeat("a", MaxPresetLine+1)}},
	} {
		if err := CheckPreset(c.name, c.lines); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := CheckPreset("ok-1", []string{"is:open label:bug", "pr:{pr} since:{since}"}); err != nil {
		t.Errorf("valid: %v", err)
	}
}

func TestCheckPresetRefusesTheNamesOfTheSuppliedSearches(t *testing.T) {
	for _, p := range Builtin {
		if err := CheckPreset(p.Name, p.Queries); err == nil {
			t.Errorf("a saved search may not be named %s", p.Name)
		}
	}
	if err := CheckPreset("pr-ergebnis-2", []string{"is:open"}); err != nil {
		t.Errorf("a name next to a supplied one is refused: %v", err)
	}
}
