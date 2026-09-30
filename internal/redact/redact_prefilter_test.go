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
		if got, want := FindSecrets(input), referenceFindSecrets(input); !reflect.DeepEqual(got, want) {
			t.Errorf("fixture %d: got %#v, want %#v", i, got, want)
		}
	}
}

func FuzzFindSecretsMatchesUnfilteredReference(f *testing.F) {
	for _, input := range secretFilterFixtures() {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if got, want := FindSecrets(input), referenceFindSecrets(input); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
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
