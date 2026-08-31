package agentbench

import (
	"fmt"
	"os"
	"strings"
)

// ModelCredentialVars are the variables a Claude Code run accepts. They are
// forwarded into the container by name, so the credential reaches the agent
// without mounting the host home — which carries the global agent rules and
// the auto-memory and would contaminate every arm at once.
var ModelCredentialVars = []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}

// RequireModelCredential fails before the first run rather than during it.
// Without a credential every run ends in "Not logged in", and a campaign that
// discovers this on run 400 has spent its wall clock on nothing.
func RequireModelCredential(lookup func(string) (string, bool)) (string, error) {
	var found []string
	for _, name := range ModelCredentialVars {
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf(
			"no model credential in the environment: set %s (an API key) or %s (from `claude setup-token`); "+
				"without it every run fails with \"Not logged in\"",
			ModelCredentialVars[0], ModelCredentialVars[1])
	default:
		// Zwei gesetzte Variablen sind kein Komfort, sondern eine offene
		// Frage: welche der beiden Identitaeten die Kampagne benutzt hat,
		// steht dann in keinem Protokoll.
		return "", fmt.Errorf(
			"several model credentials are set (%s); unset all but one so the campaign records which identity ran it",
			strings.Join(found, ", "))
	}
}

// EnvLookup adapts the process environment to RequireModelCredential.
func EnvLookup(name string) (string, bool) { return os.LookupEnv(name) }
