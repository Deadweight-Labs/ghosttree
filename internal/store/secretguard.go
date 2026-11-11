package store

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// maxSecretSpots is how many places a refusal names. A writer who pasted a
// block with several passwords fixes them in one go; more than a handful would
// only make the message long.
const maxSecretSpots = 5

// secretHint is what an agent does next. The message carries it as well as the
// resolution field, because the MCP tools show an agent only the message.
const secretHint = "Do not repeat the value. Rewrite that passage: say where the secret lives (file path, vault entry, environment variable) instead of what it is, or write a placeholder such as <password> if it is only an example, and send the text again."

// refuseSecrets is the one check every free text goes through before it is
// stored (PAIN-17): the same scan as for documents (redact.FindSecrets), the
// same answer, never the value. The arguments are pairs of a field name and its
// text. A hit refuses the whole write: nothing is stored, so there is no
// half-redacted entry, and the writer can reword, which a silent replacement of
// the value by a marker would not let them do.
//
// Refusing and not redacting is the choice for every write path: knowledge,
// requests, pains, ideas, comments, messages and tasks are written by an
// agent that is still there to answer, and a stored marker where a password
// stood would leave an entry that no longer says what it was meant to say.
func refuseSecrets(pairs ...string) error {
	return refuseSecretsChanged(nil, pairs...)
}

// refuseSecretsChanged is refuseSecrets for an edit: a field whose text is the
// one that is stored already is not looked at, so that correcting the title of
// an old entry is not refused for a body written before the check existed.
func refuseSecretsChanged(stored map[string]string, pairs ...string) error {
	var spots []string
	total := 0
	for i := 0; i+1 < len(pairs); i += 2 {
		field, text := pairs[i], pairs[i+1]
		if text == "" {
			continue
		}
		if prev, ok := stored[field]; ok && prev == text {
			continue
		}
		for _, m := range redact.FindSecrets(text) {
			total++
			if len(spots) < maxSecretSpots {
				spots = append(spots, secretSpot(field, m))
			}
		}
	}
	if total == 0 {
		return nil
	}
	first := spots[0]
	msg := fmt.Sprintf("refused, nothing was stored: possible secret in %s.", first)
	if total > 1 {
		msg = fmt.Sprintf("refused, nothing was stored: %d possible secrets: %s.", total, strings.Join(spots, "; "))
		if total > len(spots) {
			msg = strings.TrimSuffix(msg, ".") + fmt.Sprintf("; %d more.", total-len(spots))
		}
	}
	return requestdomain.NewRuleError("secret_detected", msg+" "+secretHint, secretHint, nil)
}

// secretSpot names a hit: the field, the place in it, the kind of secret and,
// for a value behind a label, the label as written ("pw" or "Passwort", so
// that the writer sees at once whether a command name was meant).
func secretSpot(field string, m redact.SecretMatch) string {
	return fmt.Sprintf("%s, %s (%s)", field, m.Where(), m.Label)
}

// indexed names the n-th entry of a list field: criteria[2].
func indexed(field string, n int) string { return fmt.Sprintf("%s[%d]", field, n+1) }
