package agentbench

import (
	"strings"
	"testing"
)

func TestRedactorRemovesTheCredentialFromAnything(t *testing.T) {
	redactor := NewRedactor("sk-ant-secret-token-value")

	got := redactor.String(`{"result":"auth failed for sk-ant-secret-token-value"}`)

	if strings.Contains(got, "sk-ant-secret-token-value") {
		t.Fatalf("a published transcript must not carry the credential: %s", got)
	}
	if !strings.Contains(got, redactionMarker) {
		t.Fatalf("the redaction must be visible, not silent: %s", got)
	}
}

func TestRedactorIgnoresValuesTooShortToBeACredential(t *testing.T) {
	// Waere "1" ein Geheimnis, verschwaende jede Zeilennummer im Transkript.
	redactor := NewRedactor("1", "", "true")

	got := redactor.String("read 1 file, true")

	if got != "read 1 file, true" {
		t.Fatalf("short values must not be redacted: %s", got)
	}
}

func TestRedactorWorksOnBytesToo(t *testing.T) {
	redactor := NewRedactor("oauth-token-abcdefghijklmnop")

	got := redactor.Bytes([]byte("Bearer oauth-token-abcdefghijklmnop\n"))

	if strings.Contains(string(got), "abcdefghijklmnop") {
		t.Fatalf("raw output is written to disk verbatim: %s", got)
	}
}

func TestRedactorFromEnvCoversTheModelCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-the-environment")

	got := RedactorFromEnv(ModelCredentialVars...).String("key sk-ant-from-the-environment used")

	if strings.Contains(got, "sk-ant-from-the-environment") {
		t.Fatalf("the forwarded credential must be redacted by default: %s", got)
	}
}
