package runbook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

func baseSpec() Spec {
	return Spec{
		Purpose:       "ausrollen",
		Params:        []Param{{Name: "sha", Type: TypeSHA}, {Name: "env", Type: TypeEnum, Values: []string{"prod", "dev"}, Default: "dev"}, {Name: "n", Type: TypeInt, Optional: true}},
		Preconditions: []string{"Schluessel in ssh-add -l"},
		Steps: []Step{
			{Title: "bauen", Command: "VERSION={{sha}} scripts/build-dist.sh", Check: "Binary vorhanden"},
			{Title: "starten auf {{env}}", Command: "systemctl start ghosttree"},
		},
		Rollback: []Step{{Title: "zurueck", Command: "ln -sfn releases/old current"}},
		Triggers: Triggers{Keywords: []string{"ausrollen", "deploy"}, Commands: []string{"ssh prod", "ssh *prod.example.com*"}, Paths: []string{"deploy/**"}},
		Targets:  []Target{{Name: "build", Comment: "baut das Binary", Steps: []ExecStep{{Argv: []string{"make", "dist", "SHA={{sha}}"}}, {Argv: []string{"echo", "{{env}}"}}}}},
	}
}

func TestNormalizeAcceptsAGoodRunbook(t *testing.T) {
	if _, err := Normalize(baseSpec()); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeRejectsUnsafeContent(t *testing.T) {
	mutate := map[string]func(*Spec){
		"newline in purpose":      func(s *Spec) { s.Purpose = "a\nb" },
		"escape in step title":    func(s *Spec) { s.Steps[0].Title = "x\x1b[31m" },
		"newline in command":      func(s *Spec) { s.Steps[0].Command = "a\nrm -rf /" },
		"undeclared placeholder":  func(s *Spec) { s.Steps[0].Command = "run {{nope}}" },
		"malformed placeholder":   func(s *Spec) { s.Steps[0].Command = "run {{ sha }}" },
		"shell with parameter":    func(s *Spec) { s.Targets[0].Steps[0].Argv = []string{"bash", "-c", "echo {{sha}}"} },
		"program is a parameter":  func(s *Spec) { s.Targets[0].Steps[0].Argv = []string{"{{sha}}", "x"} },
		"empty argv":              func(s *Spec) { s.Targets[0].Steps[0].Argv = nil },
		"control in argv":         func(s *Spec) { s.Targets[0].Steps[0].Argv = []string{"echo", "a\nb"} },
		"nul in argv":             func(s *Spec) { s.Targets[0].Steps[0].Argv = []string{"echo", "a\x00b"} },
		"broad command trigger":   func(s *Spec) { s.Triggers.Commands = []string{"*"} },
		"short keyword":           func(s *Spec) { s.Triggers.Keywords = []string{"a"} },
		"path escapes":            func(s *Spec) { s.Triggers.Paths = []string{"../etc"} },
		"duplicate parameter":     func(s *Spec) { s.Params = append(s.Params, s.Params[0]) },
		"bad target name":         func(s *Spec) { s.Targets[0].Name = "Bad Name" },
		"unsafe default":          func(s *Spec) { s.Params[0].Default = "$(id)" },
		"enum value with a space": func(s *Spec) { s.Params[1].Values = []string{"a b"} },
	}
	for name, f := range mutate {
		s := baseSpec()
		f(&s)
		if _, err := Normalize(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseSpec([]byte(`{"purpose":"x","stepz":[]}`)); err == nil {
		t.Error("an unknown field was silently dropped")
	}
}

// Injektion über Parameter: jeder Wert, der eine Shell, eine Option oder eine
// Zeile verwechseln könnte, wird abgelehnt, bevor ein Plan entsteht.
func TestParametersRejectInjection(t *testing.T) {
	s, _ := Normalize(baseSpec())
	for _, bad := range []string{
		"abc1234; touch /tmp/pwned", "abc1234 && id", "$(id)", "`id`", "abc1234|nc", "abc 1234", "abc1234\nrm", "--upload-pack=x",
		"abc1234'", `abc1234"`, "abc1234>out", "abc1234*", "~root", "", "ABC1234xyz", "abc12",
	} {
		if _, err := s.Bind(map[string]string{"sha": bad}); err == nil {
			t.Errorf("sha %q accepted", bad)
		}
	}
	if _, err := s.Bind(map[string]string{"sha": "abc1234", "env": "staging"}); err == nil {
		t.Error("an enum value outside the list accepted")
	}
	if _, err := s.Bind(map[string]string{"sha": "abc1234", "n": "-1"}); err == nil {
		t.Error("a negative int accepted")
	}
	if _, err := s.Bind(map[string]string{"sha": "abc1234", "zzz": "1"}); err == nil {
		t.Error("an unknown parameter accepted")
	}
	if _, err := s.Bind(map[string]string{}); err == nil {
		t.Error("a missing required parameter accepted")
	}
	str := Param{Name: "p", Type: TypePath}
	for _, bad := range []string{"../x", "a/../b", "-rf", "a b", "a;b"} {
		if _, err := CheckValue(str, bad); err == nil {
			t.Errorf("path %q accepted", bad)
		}
	}
}

type recorder struct{ calls [][]string }

func (r *recorder) Run(_ context.Context, argv []string) error {
	r.calls = append(r.calls, append([]string(nil), argv...))
	return nil
}

// Parameter bleiben einzelne Argumente: die Anzahl ändert sich nie, und es gibt
// keine Shell, die etwas neu zerlegt.
func TestPlanKeepsEveryParameterInsideOneArgument(t *testing.T) {
	s, _ := Normalize(baseSpec())
	values, err := s.Bind(map[string]string{"sha": "AbC1234"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.Plan("build", values)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || len(plan[0].Argv) != 3 || plan[0].Argv[2] != "SHA=abc1234" || plan[1].Argv[1] != "dev" {
		t.Fatalf("plan = %+v", plan)
	}
	rec := &recorder{}
	if n, err := Execute(context.Background(), rec, plan, nil); err != nil || n != 2 || len(rec.calls) != 2 {
		t.Fatalf("execute: %d %v", n, err)
	}
	if _, err := s.Plan("nope", values); err == nil {
		t.Error("an unknown target planned")
	}
	// Ein nicht gesetzter Platzhalter ist ein Fehler, kein leeres Argument.
	if _, err := s.Plan("build", map[string]string{}); err == nil {
		t.Error("an unset placeholder produced an argument")
	}
}

// Ausführung ohne Shell, am echten Prozess: ein Wert wie ein Dateiname mit
// Metazeichen kommt gar nicht erst durch, und ein erlaubter Wert erreicht das
// Programm unverändert.
func TestOSRunnerRunsWithoutAShell(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	spec := Spec{Purpose: "p", Params: []Param{{Name: "v", Type: TypeString}},
		Targets: []Target{{Name: "t", Comment: "c", Steps: []ExecStep{{Argv: []string{"printf", "%s", "{{v}}"}}}}}}
	s, err := Normalize(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(map[string]string{"v": "x;touch " + marker}); err == nil {
		t.Fatal("metacharacters accepted")
	}
	values, _ := s.Bind(map[string]string{"v": "a=b/c"})
	plan, _ := s.Plan("t", values)
	var out strings.Builder
	if _, err := Execute(context.Background(), OSRunner{Stdout: &out, Stderr: &out, Stdin: strings.NewReader("")}, plan, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a=b/c" {
		t.Fatalf("output %q", out.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("injected command ran")
	}
	// Ein Fehler hält die Folge an.
	failing := []Exec{{Argv: []string{"false"}}, {Argv: []string{"true"}}}
	if n, err := Execute(context.Background(), OSRunner{Stdout: &out, Stderr: &out}, failing, nil); err == nil || n != 0 {
		t.Fatalf("failure did not stop: %d %v", n, err)
	}
}

func TestHashCoversNameScopeAndContent(t *testing.T) {
	s, _ := Normalize(baseSpec())
	h := Hash("p", "", "rollout", s)
	if h == Hash("p", "", "other", s) || h == Hash("q", "", "rollout", s) || h == Hash("p", "m", "rollout", s) {
		t.Fatal("hash ignores name or scope")
	}
	s2 := s
	s2.Steps = append([]Step(nil), s.Steps...)
	s2.Steps[0].Command += " "
	s2, _ = Normalize(s2)
	s3 := s
	s3.Steps = append([]Step(nil), s.Steps...)
	s3.Steps[0].Command = "other"
	if h == Hash("p", "", "rollout", s3) {
		t.Fatal("hash ignores content")
	}
	if h != Hash("p", "", "rollout", s2) {
		t.Fatal("trailing whitespace changes the hash after normalisation")
	}
}

func TestTriggers(t *testing.T) {
	s, _ := Normalize(baseSpec())
	yes := []Input{
		{Prompt: "Bitte jetzt ausrollen"},
		{Prompt: "kannst du das deployen?"},
		{Command: "ssh prod.example.com uptime"},
		{Command: "cd /tmp && ssh prod 'ls'"},
		{Command: "FOO=1 sudo ssh prod"},
		{Command: "ssh -i key main@prod.example.com"},
		{Paths: []string{"deploy/unit.service"}},
	}
	no := []Input{
		{Prompt: "redeploy"}, {Prompt: "nichts davon"}, {Command: "ssh proden"}, {Command: "echo ssh prod"},
		{Command: "ls"}, {Paths: []string{"src/a.go"}}, {},
	}
	for _, in := range yes {
		if _, ok := s.Match(in); !ok {
			t.Errorf("no match for %+v", in)
		}
	}
	for _, in := range no {
		if reason, ok := s.Match(in); ok {
			t.Errorf("unexpected match for %+v: %s", in, reason)
		}
	}
}

func testView(approved bool) View {
	s, _ := Normalize(baseSpec())
	v := View{ID: 1, Name: "rollout-prod", Project: "github.com/dw/p", Version: 3, Head: 3, Versions: 3, Spec: s, Person: "robin"}
	v.Approved = approved
	if approved {
		v.ApprovedBy, v.ApprovedVersion = "robin", 3
	}
	return v
}

func TestChecklistSubstitutesParametersAndShowsLastSuccess(t *testing.T) {
	v := testView(true)
	v.LastSuccess = &Run{CreatedAt: "2026-11-07T10:00:00Z", Person: "robin", Version: 2, GitAnchor: &gitanchor.Anchor{Commit: "abcdef1234567890abcdef", Branch: "main"}}
	out := Checklist(v, map[string]string{"sha": "abc1234", "env": "prod"})
	for _, want := range []string{"VERSION=abc1234 scripts/build-dist.sh", "starten auf prod", "last success: 2026-11-07 on commit abcdef123456 (main)", "Status: approved", "- build: baut das Binary", "ctx runbook report rollout-prod"} {
		if !strings.Contains(out, want) {
			t.Errorf("checklist misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{sha}}") {
		t.Error("a bound placeholder stayed")
	}
	if !strings.Contains(Checklist(v, nil), "{{sha}}") {
		t.Error("an unbound placeholder should stay visible")
	}
	if !strings.Contains(Checklist(testView(false), nil), "NOT APPROVED") {
		t.Error("an unapproved version must say so")
	}
	if !strings.Contains(testView(true).LastSuccess.LastSuccessLine(), "none reported yet") {
		t.Error("no run must say so")
	}
}

func TestBriefingStaysWithinTheLimit(t *testing.T) {
	v := testView(true)
	for i := 0; i < 40; i++ {
		v.Spec.Steps = append(v.Spec.Steps, Step{Title: strings.Repeat("schritt ", 10), Command: strings.Repeat("x", 300)})
	}
	out := Briefing(v, "keyword deploy", 1500)
	if len(out) > 1500 || !strings.Contains(out, "ctx run rollout-prod") || !strings.Contains(out, "triggered by keyword deploy") {
		t.Fatalf("briefing %d bytes:\n%s", len(out), out)
	}
}

// REQ-643 review: the shortened form of a runbook asks for the report like the
// full one, at every limit that leaves room for any step, and says where the
// full checklist is.
func TestShortBriefingKeepsTheReportLine(t *testing.T) {
	s, _ := Normalize(baseSpec())
	v := View{Name: "rollout-prod", Version: 3, Spec: s}
	for i := 0; i < 40; i++ {
		v.Spec.Steps = append(v.Spec.Steps, Step{Title: strings.Repeat("schritt ", 10), Command: strings.Repeat("x", 300)})
	}
	for _, limit := range []int{500, 700, 1000, 1500, 1800} {
		out := Briefing(v, "keyword deploy", limit)
		if len(out) > limit {
			t.Errorf("limit %d: %d bytes", limit, len(out))
		}
		if !strings.Contains(out, "\nWhen done, report the outcome: ctx runbook report rollout-prod ok|failed") {
			t.Errorf("limit %d: the shortened runbook does not ask for the report:\n%s", limit, out)
		}
		if !strings.HasSuffix(out, ".\n") || !strings.Contains(out, "ctx run rollout-prod") || !strings.Contains(out, "...\n") {
			t.Errorf("limit %d: the short form has lost its ending:\n%s", limit, out)
		}
	}
	// A runbook that fits is the checklist, which ends with the same line.
	short := View{Name: "tiny", Version: 1, Spec: s}
	short.Spec.Steps = short.Spec.Steps[:1]
	if out := Briefing(short, "keyword x", 4000); !strings.Contains(out, "ctx runbook report tiny ok|failed") {
		t.Errorf("the full form lost the report line:\n%s", out)
	}
}

func TestDiffShowsOnlyWhatChanged(t *testing.T) {
	a, _ := Normalize(baseSpec())
	b := a
	b.Steps = append([]Step(nil), a.Steps...)
	b.Steps[1].Command = "systemctl restart ghosttree"
	b, _ = Normalize(b)
	d := Diff(&a, b)
	if !strings.Contains(d, "- step 2 command: systemctl start ghosttree") || !strings.Contains(d, "+ step 2 command: systemctl restart ghosttree") {
		t.Fatalf("diff:\n%s", d)
	}
	if strings.Contains(d, "purpose") {
		t.Fatalf("diff shows unchanged lines:\n%s", d)
	}
	if Diff(&a, a) != "(no change)\n" {
		t.Fatal("equal specs differ")
	}
	if !strings.Contains(Diff(nil, a), "+ target build step 1: make dist 'SHA={{sha}}'") {
		t.Fatal("first diff must list everything as added")
	}
}

func TestApplyDeviationsOnlyTouchesTheGuide(t *testing.T) {
	a, _ := Normalize(baseSpec())
	out, err := ApplyDeviations(a, []Deviation{
		{Kind: DevAdd, Step: 2, Title: "Cache leeren", Command: "make clean"},
		{Kind: DevChange, Step: 1, Command: "scripts/build-dist.sh --fast"},
		{Kind: DevRemove, Step: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Steps) != 2 || out.Steps[1].Title != "Cache leeren" || out.Steps[0].Command != "scripts/build-dist.sh --fast" {
		t.Fatalf("steps: %+v", out.Steps)
	}
	if Hash("p", "", "x", out) == Hash("p", "", "x", a) {
		t.Fatal("deviation changed nothing")
	}
	if len(out.Targets) != len(a.Targets) || out.Targets[0].Steps[0].Argv[0] != "make" {
		t.Fatal("targets changed")
	}
	for _, bad := range []Deviation{{Kind: DevChange, Step: 9, Command: "x"}, {Kind: "weird"}, {Kind: DevAdd}} {
		if _, err := ApplyDeviations(a, []Deviation{bad}); err == nil {
			t.Errorf("deviation %+v accepted", bad)
		}
	}
	// Eine Abweichung kann keinen Befehl mit Zeilenumbruch einschleusen.
	if _, err := NormalizeDeviations([]Deviation{{Kind: DevAdd, Command: "a\nb"}}); err == nil {
		t.Error("multi-line deviation accepted")
	}
	d, err := ParseDeviation("add:3:Cache leeren:make clean:all")
	if err != nil || d.Step != 3 || d.Command != "make clean:all" {
		t.Fatalf("parse: %+v %v", d, err)
	}
}

// Unsichtbare Format-Zeichen (Bidi, Nullbreite) lassen einen Befehl im Vergleich
// anders aussehen, als er ausgeführt wird: sie sind in jedem Text verboten.
func TestInvisibleFormattingCharactersAreRejected(t *testing.T) {
	invisible := []string{"\u202e", "\u200b", "\u200c", "\u200d", "\ufeff", "\u200e", "\u200f", "\u202a", "\u2066", "\u2069", "\u2028", "\u2029"}
	for _, ch := range invisible {
		mutate := map[string]func(*Spec){
			"purpose":        func(s *Spec) { s.Purpose = "a" + ch + "b" },
			"step title":     func(s *Spec) { s.Steps[0].Title = "a" + ch + "b" },
			"step command":   func(s *Spec) { s.Steps[0].Command = "echo " + ch + "ok" },
			"check":          func(s *Spec) { s.Steps[0].Check = "a" + ch + "b" },
			"precondition":   func(s *Spec) { s.Preconditions = []string{"a" + ch + "b"} },
			"argv":           func(s *Spec) { s.Targets[0].Steps[0].Argv = []string{"make", "di" + ch + "st"} },
			"keyword":        func(s *Spec) { s.Triggers.Keywords = []string{"aus" + ch + "rollen"} },
			"command":        func(s *Spec) { s.Triggers.Commands = []string{"ssh " + ch + "prod"} },
			"path trigger":   func(s *Spec) { s.Triggers.Paths = []string{"a" + ch + "/**"} },
			"param default":  func(s *Spec) { s.Params[1].Default = "d" + ch + "e" },
			"param describe": func(s *Spec) { s.Params[0].Description = "a" + ch + "b" },
		}
		for name, f := range mutate {
			s := baseSpec()
			f(&s)
			if _, err := Normalize(s); err == nil {
				t.Errorf("U+%04X accepted in %s", []rune(ch)[0], name)
			}
		}
		// Parameterwerte, Namen und Pfade sind reines ASCII.
		for _, p := range []Param{{Name: "v", Type: TypeString}, {Name: "v", Type: TypePath}, {Name: "v", Type: TypeSHA}} {
			if _, err := CheckValue(p, "abc1234"+ch); err == nil {
				t.Errorf("U+%04X accepted in a %s value", []rune(ch)[0], p.Type)
			}
		}
		if ValidName("roll"+ch+"out") == nil || ValidTargetName("bu"+ch+"ild") == nil {
			t.Errorf("U+%04X accepted in a name", []rune(ch)[0])
		}
		if _, err := NormalizeDeviations([]Deviation{{Kind: DevAdd, Command: "a" + ch + "b"}}); err == nil {
			t.Errorf("U+%04X accepted in a deviation", []rune(ch)[0])
		}
	}
}

// Was ein Mensch im Vergleich, im Probelauf und im Lauf-Protokoll liest, zeigt
// jedes Zeichen außerhalb von druckbarem ASCII als Escape.
func TestNonASCIIIsShownAsAnEscape(t *testing.T) {
	if got := Escape("ab\u202ecd\u0430"); got != `ab\u202ecd\u0430` {
		t.Fatalf("Escape = %q", got)
	}
	if got := Escape("\U0001F600"); got != `\U0001f600` {
		t.Fatalf("Escape of an astral character = %q", got)
	}
	if got := QuoteArgv([]string{"echo", "a\u202eb", "p\u0430ss", "plain"}); got != `echo 'a\u202eb' 'p\u0430ss' plain` {
		t.Fatalf("QuoteArgv = %q", got)
	}
	a, _ := Normalize(baseSpec())
	b := a
	b.Steps = append([]Step(nil), a.Steps...)
	b.Steps[1].Command = "systemctl start ghöst"
	d := Diff(&a, b)
	if !strings.Contains(d, `gh\u00f6st`) || strings.Contains(d, "\u00f6") {
		t.Fatalf("diff does not escape non-ASCII:\n%s", d)
	}
	// Eine Fassung mit gleich aussehendem Zeichen im Argument zeigt es im Diff.
	c := a
	c.Targets = []Target{{Name: "build", Comment: "baut das Binary", Steps: []ExecStep{{Argv: []string{"m\u0430ke", "dist"}}}}}
	if d := Diff(&a, c); !strings.Contains(d, `m\u0430ke`) {
		t.Fatalf("a look-alike in argv is not visible:\n%s", d)
	}
}
