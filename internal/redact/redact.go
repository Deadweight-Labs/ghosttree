// Package redact strips common credential patterns before data leaves
// the machine. It is a baseline filter, not a guarantee.
package redact

import (
	"regexp"
	"strings"
)

type SecretMatch struct {
	Label string
	Line  int
}

var patterns = []struct {
	label string
	re    *regexp.Regexp
}{
	{"privatekey", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"anthropic", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`)},
	{"openai", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{28,}\b`)},
	{"github", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}\b|\bgithub_pat_[A-Za-z0-9_]{30,}\b`)},
	{"aws", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"slack", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)},
}

func Redact(s string) string {
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, "[REDACTED:"+p.label+"]")
	}
	return s
}

func FindSecrets(s string) []SecretMatch {
	var matches []SecretMatch
	for _, pattern := range patterns {
		for _, location := range pattern.re.FindAllStringIndex(s, -1) {
			matches = append(matches, SecretMatch{
				Label: pattern.label,
				Line:  strings.Count(s[:location[0]], "\n") + 1,
			})
		}
	}
	return matches
}
