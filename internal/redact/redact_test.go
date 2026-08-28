package redact

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	githubToken := "ghp_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"
	githubOAuth := "gho_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"
	awsKey := "AKIA" + "IOSFODNN7EXAMPLE"
	privateKey := "-----BEGIN OPENSSH " + "PRIVATE KEY-----\nb3BlbnNzaA==\n-----END OPENSSH " + "PRIVATE KEY-----"
	cases := []struct{ in, label string }{
		{"key is sk-ant-api03-AbCdEfGh123456789012345678901234", "anthropic"},
		{"token " + githubToken, "github"},
		{"oauth " + githubOAuth, "github"},
		{"aws " + awsKey, "aws"},
		{"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "jwt"},
		{privateKey, "privatekey"},
		{"slack xoxb-fake-token-for-testing-0", "slack"},
	}
	for _, c := range cases {
		got := Redact(c.in)
		if !strings.Contains(got, "[REDACTED:"+c.label+"]") {
			t.Errorf("Redact(%q) = %q, want label %q", c.in, got, c.label)
		}
	}
}

func TestRedactLeavesNormalTextAlone(t *testing.T) {
	in := "package main // skeleton code, nothing secret. ghost tree eyJustText"
	if got := Redact(in); got != in {
		t.Errorf("false positive: %q -> %q", in, got)
	}
}

func TestFindSecretsReportsLabelAndLineWithoutChangingText(t *testing.T) {
	in := "safe first line\ntoken ghp_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890\n"
	matches := FindSecrets(in)
	if len(matches) != 1 || matches[0].Label != "github" || matches[0].Line != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	if in != "safe first line\ntoken ghp_"+"AbCdEfGhIjKlMnOpQrStUvWxYz1234567890\n" {
		t.Fatal("secret detection modified its input")
	}
}
