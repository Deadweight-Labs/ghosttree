package query

import (
	"errors"
	"testing"
)

// Kriterium 6: by:, harness: und model: (REQ-620).
func TestActorQualifiers(t *testing.T) {
	claude := Facts{Kind: KindPain, Actor: "agent", Harness: "claude-code", Model: "claude-opus-5-5"}
	codex := Facts{Kind: KindPain, Actor: "agent", Harness: "codex", Model: "gpt-6-sol"}
	person := Facts{Kind: KindPain, Actor: "human"}
	unknown := Facts{Kind: KindPain}
	all := map[string]Facts{"claude": claude, "codex": codex, "person": person, "unknown": unknown}
	for line, want := range map[string]string{
		"by:agent":                      "claude codex",
		"by:agents":                     "claude codex",
		"by:human":                      "person",
		"-by:agent":                     "person unknown",
		"by:agent,human":                "claude codex person",
		"harness:claude":                "claude",
		"harness:claude-code":           "claude",
		"harness:codex":                 "codex",
		"-harness:codex":                "claude person unknown",
		"model:opus":                    "claude",
		`model:"Opus 5.5"`:              "claude",
		"model:claude-opus-5-5":         "claude",
		"model:gpt-6":                   "codex",
		"model:sonnet":                  "",
		"by:agent harness:codex":        "codex",
		"by:human model:opus":           "",
		"harness:Claude model:OPUS-5-5": "claude",
	} {
		q := mustParse(t, line)
		var got []string
		for _, name := range []string{"claude", "codex", "person", "unknown"} {
			if q.Match(all[name]) {
				got = append(got, name)
			}
		}
		if joined := join(got); joined != want {
			t.Errorf("%s matches %q, want %q", line, joined, want)
		}
	}
	_, err := Parse("by:robot", testNow)
	var qe *Error
	if !errors.As(err, &qe) || qe.Unknown != "" {
		t.Errorf("by:robot: %v", err)
	}
	for _, k := range []string{"by", "harness", "model"} {
		found := false
		for _, known := range KnownKeys() {
			if known == k {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: is not a known qualifier", k)
		}
	}
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out
}
