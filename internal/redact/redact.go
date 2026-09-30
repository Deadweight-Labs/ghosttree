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
	label    string
	re       *regexp.Regexp
	literals []string
}{
	{"privatekey", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), []string{"-----BEGIN "}},
	{"anthropic", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`), []string{"sk-ant-"}},
	{"openai", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{28,}\b`), []string{"sk-"}},
	{"github", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}\b|\bgithub_pat_[A-Za-z0-9_]{30,}\b`), []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}},
	{"aws", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), []string{"AKIA", "ASIA"}},
	{"slack", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), []string{"xox"}},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), []string{"eyJ"}},
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
		possible := len(pattern.literals) == 0
		for _, literal := range pattern.literals {
			if strings.Contains(s, literal) {
				possible = true
				break
			}
		}
		if !possible {
			continue
		}
		for _, location := range pattern.re.FindAllStringIndex(s, -1) {
			matches = append(matches, SecretMatch{
				Label: pattern.label,
				Line:  strings.Count(s[:location[0]], "\n") + 1,
			})
		}
	}
	return matches
}
