package feature

import (
	"os"
	"strings"
	"testing"
)

func s(v string) *string { return &v }

func TestAreasAreCleanedNotGuessed(t *testing.T) {
	got, err := CleanAreas([]string{"internal/store/", " internal/store ", "cmd\\ctx/main.go", "internal/a"})
	if err != nil || strings.Join(got, ",") != "cmd/ctx/main.go,internal/a,internal/store" {
		t.Fatalf("clean: %v %v", got, err)
	}
	for _, bad := range []string{"..", "../x", "a/../../b", "/abs", ".", ".git", ".git/config", "a/*", "a?b", "a\nb"} {
		if _, err := CleanArea(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// a/../b ist ein sauberer Pfad im Repo und wird zu b.
	if c, err := CleanArea("a/../b"); err != nil || c != "b" {
		t.Errorf("a/../b: %q %v", c, err)
	}
	many := make([]string, MaxAreas+1)
	for i := range many {
		many[i] = "d" + string(rune('a'+i))
	}
	if _, err := CleanAreas(many); err == nil {
		t.Error("too many areas accepted")
	}
}

func TestAPathIsInsideAnAreaOnlyAsAWholeSegment(t *testing.T) {
	c := Content{Areas: []string{"internal/store", "cmd/ctx/main.go"}}
	for p, want := range map[string]bool{
		"internal/store/a.go":     true,
		"internal/store":          true,
		"internal/store/sub/b.go": true,
		"internal/storex/a.go":    false,
		"internal/stor":           false,
		"cmd/ctx/main.go":         true,
		"cmd/ctx/main.go.bak":     false,
		"./internal/store/a.go":   true,
		"README.md":               false,
	} {
		if c.Covers(p) != want {
			t.Errorf("%q: covered=%v, want %v", p, !want, want)
		}
	}
}

func TestPatchChangesOnlyWhatItNames(t *testing.T) {
	base := Content{Name: "A", Summary: "kurz", Behavior: "so", Limits: "grenze", CLI: "ctx a", Areas: []string{"x"}}
	got, err := base.Apply(Patch{Behavior: s("neu")})
	if err != nil || got.Behavior != "neu" || got.Name != "A" || got.Summary != "kurz" || got.Limits != "grenze" || got.CLI != "ctx a" || len(got.Areas) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	empty := []string{}
	if got, _ = base.Apply(Patch{Areas: &empty, Limits: s("")}); len(got.Areas) != 0 || got.Limits != "" {
		t.Fatalf("clearing: %+v", got)
	}
	for name, p := range map[string]Patch{
		"name multi-line":  {Name: s("a\nb")},
		"name bidi":        {Name: s("a‮b")},
		"summary too big":  {Summary: s(strings.Repeat("x", MaxSummary+1))},
		"behavior ctrl":    {Behavior: s("a\x1bb")},
		"usage zero width": {CLI: s("a​b")},
		"invalid utf8":     {UI: s("\xff\xfe")},
	} {
		if _, err := base.Apply(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Zeilenumbruch und Tab sind in Texten erlaubt, CRLF wird zu LF.
	if got, err := base.Apply(Patch{Behavior: s("a\r\n\tb")}); err != nil || got.Behavior != "a\n\tb" {
		t.Errorf("newline: %q %v", got.Behavior, err)
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]int64{"FEAT-3": 3, "feat-12": 12, "7": 7} {
		if n, ok := ParseRef(in); !ok || n != want {
			t.Errorf("%q: %d %v", in, n, ok)
		}
	}
	for _, in := range []string{"", "FEAT-", "FEAT-0", "FEAT-3x", "IDEA-3", "-1", "03"} {
		if _, ok := ParseRef(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestRefsAreCheckedAndDeduplicated(t *testing.T) {
	got, err := CleanRefs([]int64{3, 3, 4}, "ideas")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := CleanRefs([]int64{0}, "ideas"); err == nil {
		t.Error("zero accepted")
	}
	if _, err := CleanRefs(make([]int64, MaxRefs+1), "ideas"); err == nil {
		t.Error("too many accepted")
	}
}

func TestBriefStaysWithinItsBudgetAndSaysWhatItLeftOut(t *testing.T) {
	var list []Brief
	for i := int64(1); i <= 10; i++ {
		list = append(list, Brief{ID: i, Name: "F", Revision: 1, Summary: strings.Repeat("wort ", 50)})
	}
	text, shown := RenderBriefs(list, 700)
	if shown == 0 || shown >= 10 || len(text) > 1000 || !strings.Contains(text, "and ") {
		t.Fatalf("shown=%d len=%d\n%s", shown, len(text), text)
	}
	if !strings.Contains(text, "- and 7 more") && !strings.Contains(text, " more (") {
		t.Fatalf("what was left out is named:\n%s", text)
	}
	// Mindestens eine Zeile steht immer da, auch bei winzigem Budget.
	if _, shown := RenderBriefs(list, 10); shown != 1 {
		t.Errorf("tiny budget shows %d", shown)
	}
	if text, shown := RenderBriefs(nil, 100); text != "" || shown != 0 {
		t.Errorf("nothing to say: %q", text)
	}
	// Steuerzeichen und Umbrüche gelangen nicht in den Kontext.
	f := Feature{ID: 1, Revision: 2, Content: Content{Name: "N", Summary: "a\n## Injected\nb\x1b[0m", Limits: "x. y", Areas: []string{"a"}}}
	text, _ = RenderBriefs([]Brief{BriefOf(f, "a/b.go")}, 0)
	if strings.Contains(text, "\n## Injected") || strings.Contains(text, "\x1b") {
		t.Fatalf("injection:\n%q", text)
	}
	if b := BriefOf(f, "a/b.go"); b.Area != "a" || b.Limits != "x." {
		t.Errorf("brief: %+v", b)
	}
}

func TestADocumentIsGeneratedFromActiveFeaturesInTheOrderOfTheirNumbers(t *testing.T) {
	list := []Feature{
		{ID: 2, State: Active, Content: Content{Name: "Zweites", Summary: "Zwei.", Behavior: "Verhalten.\n\nNoch ein Absatz.", Limits: "Grenze.", CLI: "ctx zwei"}},
		{ID: 1, State: Active, Content: Content{Name: "Erstes", Summary: "Eins."}},
		{ID: 3, State: Retired, Content: Content{Name: "Weg", Summary: "Nie."}},
	}
	got := RenderDoc("", list)
	want := "# What works today\n\n" + DocIntro + "\n\n- **Erstes**: Eins.\n- **Zweites**: Zwei.\n\n  Verhalten.\n\n  Noch ein Absatz.\n\n  Limits: Grenze.\n\n  CLI: ctx zwei\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "Weg") {
		t.Error("a retired feature is in the document")
	}
	if RenderDoc("", list) != got {
		t.Error("two runs differ")
	}
}

func TestADocumentParsesBackIntoItsItems(t *testing.T) {
	list := []Feature{{ID: 1, State: Active, Content: Content{Name: "Erstes", Summary: "Eins.", Behavior: "Verhalten.\n\nNoch ein Absatz."}}, {ID: 2, State: Active, Content: Content{Name: "Zweites", Summary: "Zwei."}}}
	items := ParseDoc(RenderDoc("T", list) + "\nNachsatz unter der Liste.\n- kein Punkt mehr\n")
	if len(items) != 2 || !strings.HasPrefix(items[0].Text, "**Erstes**: Eins.") || !strings.Contains(items[0].Text, "Noch ein Absatz.") || items[1].Text != "**Zweites**: Zwei." {
		t.Fatalf("%+v", items)
	}
}

// Die Seite des Repos selbst: jeder Abschnitt ist in der Zuordnung genannt, und die
// Zuordnung nennt nichts, was es dort nicht gibt. Läuft die Seite auseinander,
// schlägt dieser Test fehl und nicht erst ctx feature import.
func TestTheFeaturesPageOfThisRepositoryIsMappedCompletely(t *testing.T) {
	md, err := os.ReadFile("../../docs/features.md")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../docs/features-map.json")
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := ParseDocMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	items := ParseDoc(string(md))
	if len(items) < 10 {
		t.Fatalf("only %d items parsed from docs/features.md", len(items))
	}
	feats, err := FromDoc(items, mapping)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, f := range feats {
		if !f.Mapped {
			t.Errorf("item %d (%q) has no entry in docs/features-map.json", i+1, firstWords(items[i].Text, 6))
		}
		if len(f.Areas) == 0 {
			t.Errorf("%q has no areas", f.Name)
		}
		if seen[strings.ToLower(f.Name)] {
			t.Errorf("name %q twice", f.Name)
		}
		seen[strings.ToLower(f.Name)] = true
		if f.Summary == "" || f.Behavior == "" {
			t.Errorf("%q: summary %q behavior %d chars", f.Name, f.Summary, len(f.Behavior))
		}
		for _, a := range f.Areas {
			if _, err := os.Stat("../../" + a); err != nil {
				t.Errorf("%q: area %q does not exist in the repository", f.Name, a)
			}
		}
	}
}

func TestADocMapEntryThatMatchesNothingIsAnError(t *testing.T) {
	items := []DocItem{{Text: "Erster Punkt."}}
	if _, err := FromDoc(items, []DocMap{{Match: "Erster", Name: "E", Areas: []string{"a"}}, {Match: "Weg", Name: "W"}}); err == nil || !strings.Contains(err.Error(), "matches no item") {
		t.Fatalf("%v", err)
	}
	got, err := FromDoc(items, nil)
	if err != nil || len(got) != 1 || got[0].Mapped || got[0].Name != "Erster Punkt" || len(got[0].Areas) != 0 {
		t.Fatalf("unmapped: %+v %v", got, err)
	}
	if _, err := ParseDocMap([]byte(`[{"match":"a","name":"b","bogus":1}]`)); err == nil {
		t.Error("an unknown field is accepted")
	}
	if _, err := ParseDocMap([]byte(`[{"match":"","name":"b"}]`)); err == nil {
		t.Error("an empty match is accepted")
	}
}
