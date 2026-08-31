package agentbench

import (
	"strings"
	"testing"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
}

func TestRequireModelCredentialNamesTheOneThatIsSet(t *testing.T) {
	name, err := RequireModelCredential(lookupFrom(map[string]string{"ANTHROPIC_API_KEY": "sk-x"}))
	if err != nil {
		t.Fatalf("one credential is enough: %v", err)
	}
	if name != "ANTHROPIC_API_KEY" {
		t.Fatalf("the report must record which identity ran, got %q", name)
	}
}

func TestRequireModelCredentialRefusesAnEmptyEnvironment(t *testing.T) {
	_, err := RequireModelCredential(lookupFrom(nil))
	if err == nil {
		t.Fatal("without a credential every run fails; the campaign must not start")
	}
	// Die Meldung muss den Ausweg nennen, sonst kostet sie eine Recherche.
	if !strings.Contains(err.Error(), "setup-token") {
		t.Fatalf("the error must say how to get a credential: %v", err)
	}
}

func TestRequireModelCredentialRefusesAnEmptyValue(t *testing.T) {
	_, err := RequireModelCredential(lookupFrom(map[string]string{"ANTHROPIC_API_KEY": "  "}))
	if err == nil {
		t.Fatal("a set but empty variable is not a credential")
	}
}

func TestRequireModelCredentialRefusesTwoIdentities(t *testing.T) {
	_, err := RequireModelCredential(lookupFrom(map[string]string{
		"ANTHROPIC_API_KEY": "sk-x", "CLAUDE_CODE_OAUTH_TOKEN": "oauth-y",
	}))
	if err == nil {
		t.Fatal("with two credentials set, no protocol records which one ran")
	}
}
