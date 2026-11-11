package redact

import (
	"strings"
	"testing"
)

// PAIN-22: "pw" is the name of a command-line tool. Text that names it in
// Markdown code, with the next command or punctuation right behind it, is not a
// password.
func TestLabelThatIsACommandNameIsNoSecret(t *testing.T) {
	for _, in := range []string{
		"Run `pw export`/`pw deploy` before the release.",
		"Everything except `pw export`/`pw deploy`/`pw live`.",
		"The `pw`-command other than `pw export`/`pw deploy` stays.",
		"then `pw live`.",
		"first `pw export`/`pw deploy`, then `pw status`",
		"use `pw export`, `pw deploy` or `pw live`!",
		"ci: pw build && pw deploy",
		"(`pw export`/`pw deploy`)",
		"`pw export`/`pw deploy`/`pw live`/`pw status`",
	} {
		if got := Redact(in); got != in {
			t.Errorf("command text rewritten:\n in  %q\n got %q", in, got)
		}
		if got := FindSecrets(in); len(got) != 0 {
			t.Errorf("FindSecrets reports %+v for %q", got, in)
		}
	}
}

// What the command-name rule must not let through: a value tied to the label
// with ":" or "=", in code or not, and a bare "Passwort " followed by a real
// password with a code span behind it.
func TestLabelWithARealValueIsStillASecret(t *testing.T) {
	for _, tc := range []struct{ in, label string }{
		{"pw: Xy7#abcQ9", "pw"},
		{"`pw: Xy7#abcQ9`", "pw"},
		{"use pw=Xy7#abcQ9 here", "pw"},
		{"`password=Sommer2026!` in the shell", "password"},
		{"`password: Hx7!kQ2pz`", "password"},
		{"password=Hx7!kQ2pz`/`pw deploy`", "password"},
		{"Passwort Hx7!kQ2pz", "Passwort"},
		{"the `pw Hx7!kQ2pz` call", "pw"},
		{"the `pw Hx7!kQ2pz`/`pw deploy` calls", "pw"},
		// Outside of a code span a backtick and a shell operator are characters
		// of the password like any other: nothing is cut, nothing is skipped.
		{"Passwort ist Ab&&cd123!", "Passwort"},
		{"pw Ab&&cd123!", "pw"},
		{"Passwort ist Ab||cd123!", "Passwort"},
		{"Passwort Hx`7!kQ2pz", "Passwort"},
		{"pw Hx`7!kQ2pz", "pw"},
		{"Passwort ist `Hx7!kQ2pz`", "Passwort"},
	} {
		got := FindSecrets(tc.in)
		if len(got) != 1 || got[0].Label != "labeled" || got[0].Keyword != tc.label {
			t.Errorf("%q: matches %+v, want one labeled hit on %q", tc.in, got, tc.label)
			continue
		}
		if out := Redact(tc.in); !strings.Contains(out, labeledMarker) {
			t.Errorf("%q: nothing redacted: %q", tc.in, out)
		}
	}
}

// A value inside a code span is cut at the closing backtick: the backtick
// stays, and only the password is replaced. Outside of a code span the whole
// run is the value.
func TestLabeledValueEndsAtTheClosingBacktick(t *testing.T) {
	for in, want := range map[string]string{
		"run `pw Hx7!kQ2pz` now":    "run `pw " + labeledMarker + "` now",
		"run pw Hx`7!kQ2pz now":     "run pw " + labeledMarker + " now",
		"a `b` and Passwort Hx`7!k": "a `b` and Passwort " + labeledMarker,
	} {
		if got := Redact(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// The same in a JSON string: an escaped line break ends the line for the code
// span, and the rule does not depend on the mode.
func TestCodeSpanRuleInJSONStrings(t *testing.T) {
	// "`" opened on the line before does not make the label of the next line
	// part of a code span.
	raw := `{"text":"a ` + "`" + `b\nPasswort Hx` + "`" + `7!kQ2pz"}`
	if got := FindSecretsIn(raw, JSON); len(got) != 1 || got[0].Keyword != "Passwort" {
		t.Fatalf("matches = %+v", got)
	}
	cmd := `{"text":"run ` + "`pw export`/`pw deploy`" + `"}`
	if got := FindSecretsIn(cmd, JSON); len(got) != 0 {
		t.Fatalf("matches = %+v", got)
	}
}

// The finding carries line, column and label, and never the value.
func TestFindSecretsNamesLabelAndColumn(t *testing.T) {
	in := "intro\n\nÜber: pw: Xy7#abcQ9 und mehr\n"
	got := FindSecrets(in)
	if len(got) != 1 {
		t.Fatalf("matches = %+v", got)
	}
	m := got[0]
	// "Über: " is six characters, the label starts in column 7.
	if m.Label != "labeled" || m.Keyword != "pw" || m.Line != 3 || m.Column != 7 {
		t.Fatalf("match = %+v", m)
	}
	if strings.Contains(strings.Join([]string{m.Label, m.Keyword}, " "), "Xy7") {
		t.Fatalf("the value is in the match: %+v", m)
	}
}

func TestFindSecretsColumnOfAPatternMatch(t *testing.T) {
	in := "ok\n  see " + "ghp_" + strings.Repeat("A", 40)
	got := FindSecrets(in)
	if len(got) != 1 || got[0].Line != 2 || got[0].Column != 7 || got[0].Keyword != "" {
		t.Fatalf("matches = %+v", got)
	}
}
