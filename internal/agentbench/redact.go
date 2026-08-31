package agentbench

import (
	"bytes"
	"os"
	"strings"
)

const redactionMarker = "[redacted]"

// minSecretLength keeps short values out of the redactor. An empty or
// one-character value would match in every transcript and replace ordinary
// text, which destroys the evidence the transcripts exist for.
const minSecretLength = 12

// Redactor removes known credential values from everything the harness
// writes out. The publication plan says raw transcripts are published; a
// token in a published transcript is a leak that no later deletion undoes,
// because the file was already fetched.
type Redactor struct{ secrets []string }

func NewRedactor(values ...string) Redactor {
	var kept []string
	for _, value := range values {
		if value = strings.TrimSpace(value); len(value) >= minSecretLength {
			kept = append(kept, value)
		}
	}
	return Redactor{secrets: kept}
}

// RedactorFromEnv builds a redactor over the values the named variables hold
// right now. The names come from the runtime's PassEnv: exactly the values
// that reach the container are exactly the ones that could come back out.
func RedactorFromEnv(names ...string) Redactor {
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, os.Getenv(name))
	}
	return NewRedactor(values...)
}

func (r Redactor) String(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, redactionMarker)
	}
	return s
}

func (r Redactor) Bytes(b []byte) []byte {
	for _, secret := range r.secrets {
		b = bytes.ReplaceAll(b, []byte(secret), []byte(redactionMarker))
	}
	return b
}
