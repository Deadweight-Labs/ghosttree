package redact

import (
	"reflect"
	"strings"
	"testing"
)

func referenceFindSecrets(s string) []SecretMatch {
	var matches []SecretMatch
	for _, pattern := range patterns {
		for _, location := range pattern.re.FindAllStringIndex(s, -1) {
			matches = append(matches, SecretMatch{Label: pattern.label, Line: strings.Count(s[:location[0]], "\n") + 1})
		}
	}
	return matches
}

// tokenMatches keeps the findings of the fixed-prefix patterns, the ones the
// literal prefilter stands in front of.
func tokenMatches(all []SecretMatch) []SecretMatch {
	var out []SecretMatch
	for _, m := range all {
		if m.Label != "url" && m.Label != "labeled" {
			out = append(out, m)
		}
	}
	return out
}

func secretFilterFixtures() []string {
	fixtures := []string{"", "ordinary\r\nUnicode ä日 document", strings.Repeat("Synthetic document orchard.\n", 3000)}
	for _, prefix := range []string{"sk-ant-", "sk-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "AKIA", "ASIA", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-"} {
		token := prefix + strings.Repeat("A", 40)
		if prefix == "AKIA" || prefix == "ASIA" {
			token = prefix + strings.Repeat("A", 16)
		}
		for _, surround := range [][2]string{{"", ""}, {"x", ""}, {"_", ""}, {"é", "日"}, {"\n\r\n", "\n"}, {"", "_"}} {
			fixtures = append(fixtures, surround[0]+token+surround[1])
		}
	}
	fixtures = append(fixtures,
		"first\neyJabcdefgh.abcdefgh.abcdefgh\r\nlast",
		"-----BEGIN MANY UPPERCASE WORDS "+"PRIVATE KEY-----\n\r\nsk-ant-"+strings.Repeat("A", 40)+"\n-----END DIFFERENT "+"PRIVATE KEY-----",
		"first\nxoxb-abcdefghijk\nsecond\nghp_"+strings.Repeat("A", 40)+"\nthird\nsk-ant-"+strings.Repeat("A", 40),
		"sk-ant- eyJ xox AKIA ASIA ghp_ github_pat_ -----BEGIN ",
	)
	return fixtures
}

func TestFindSecretsMatchesUnfilteredReference(t *testing.T) {
	for i, input := range secretFilterFixtures() {
		if got, want := tokenMatches(FindSecrets(input)), referenceFindSecrets(input); !reflect.DeepEqual(got, want) {
			t.Errorf("fixture %d: got %#v, want %#v", i, got, want)
		}
	}
}

func FuzzFindSecretsMatchesUnfilteredReference(f *testing.F) {
	for _, input := range secretFilterFixtures() {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if got, want := tokenMatches(FindSecrets(input)), referenceFindSecrets(input); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
}

func labeledFixtures() []string {
	return []string{
		"", "plain text without anything", "Passwort: Hx7!kQ2pz", "PW=Sommer2026!", "tOkEn: 5n5n5n5n7p7p7p7p9q9q",
		"Zugangsdaten: Tr0ub4dor&3 Kennwort = Gr0ssb4um#7 Credentials: Tr0ub4dor&3 Bearer 5n5n5n5n7p7p7p7p9q9q",
		"API_KEY=k7k7k7k7m2m2m2m2 apikey: k7k7k7k7m2m2m2m2 api key: k7k7k7k7m2m2m2m2 Api-Secret: k7k7k7k7m2m2m2m2",
		"client secret: Ab3Ab3Ab3Ab3 SECRET_KEY=Ab3Ab3Ab3Ab3 secret access key: Ab3Ab3Ab3Ab3",
		"\"password\":\"Hx7!kQ2pz\"\\n\\\"pw\\\": \\\"Hx7!kQ2pz\\\"",
		"Mailpasswort Hx7!kQ2pz\nDB_PASSWORD=Hx7!kQ2pz\nxApiKey: k7k7k7k7m2m2m2m2",
		"pass pass pass pw pw pw token token token", strings.Repeat("pw ", 5000), strings.Repeat("a", 5000) + " Passwort Hx7!kQ2pz",
		"Passwort äöü ist „Hx7!kQ2pz“", "\xff\xfePasswort: Hx7!kQ2pz\xff",
	}
}

// The table of first letters in front of the label scan must not hide a
// label: the scan without it finds the same values.
func allBytes() *[256]bool {
	var all [256]bool
	for i := range all {
		all[i] = true
	}
	return &all
}

func TestLabeledScanWithoutFirstLetterTableFindsTheSame(t *testing.T) {
	for i, input := range labeledFixtures() {
		for _, mode := range []Mode{Text, JSON} {
			if got, want := scanLabeled(input, mode), scanLabeledWith(input, mode, allBytes()); !reflect.DeepEqual(got, want) {
				t.Errorf("fixture %d mode %d: got %#v, want %#v", i, mode, got, want)
			}
		}
	}
}

func FuzzLabeledScanWithoutFirstLetterTableFindsTheSame(f *testing.F) {
	for _, input := range labeledFixtures() {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		for _, mode := range []Mode{Text, JSON} {
			if got, want := scanLabeled(input, mode), scanLabeledWith(input, mode, allBytes()); !reflect.DeepEqual(got, want) {
				t.Fatalf("mode %d: got %#v, want %#v", mode, got, want)
			}
		}
	})
}

func BenchmarkFindSecretsCleanDocument(b *testing.B) {
	input := strings.Repeat("Synthetic document orchard.\n", 3000)[:65536]
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		if len(FindSecrets(input)) != 0 {
			b.Fatal("unexpected match")
		}
	}
}
