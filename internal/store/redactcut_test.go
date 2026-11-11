package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/slim"
)

// secretCut ist eine lange Werkzeugausgabe mit einem Geheimnis genau an einer
// Schnittstelle der Kürzung (CapHead oder Länge-CapTail). Würde vor dem
// Schwärzen gekürzt, stünde ein Stück davon im behaltenen Teil, das die
// Schwärzung allein nicht mehr erkennt (ein Schlüssel ohne END, ein Token
// unter der Mindestlänge).
type secretCut struct {
	name      string
	out       string
	fragments []string
}

const cutTotal = 20_000

func cutFiller(n int) string {
	if n <= 0 {
		return ""
	}
	s := strings.Repeat("log line\n", n/9+1)[:n]
	return s[:n-1] + "\n"
}

func secretCuts() []secretCut {
	token := "ghp_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"
	var key strings.Builder
	key.WriteString("-----BEGIN RSA " + "PRIVATE KEY-----\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&key, "MIIEkey%04dmaterialAAAA\n", i)
	}
	key.WriteString("-----END RSA " + "PRIVATE KEY-----\n")
	secrets := []struct {
		name, value string
		deltas      []int
		fragments   []string
	}{
		{"token", token, []int{-15, -1, 0}, []string{token[4:12], token[len(token)-8:]}},
		{"key", key.String(), []int{-500, -1, 0}, []string{"MIIEkey"}},
	}
	var out []secretCut
	for _, sec := range secrets {
		for _, boundary := range []struct {
			name string
			at   int
		}{{"head", slim.CapHead}, {"tail", cutTotal - slim.CapTail}} {
			for _, d := range sec.deltas {
				pos := boundary.at + d
				rest := cutTotal - pos - len(sec.value)
				out = append(out, secretCut{
					name:      fmt.Sprintf("%s at %s%+d", sec.name, boundary.name, d),
					out:       cutFiller(pos) + sec.value + cutFiller(rest),
					fragments: sec.fragments,
				})
			}
		}
	}
	return out
}

// toolResultLine ist eine Claude-Zeile mit out als tool_result.
func toolResultLine(out string) string {
	enc, _ := json.Marshal(out)
	return `{"type":"user","cwd":"/tmp","gitBranch":"","sessionId":"cut","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + string(enc) + `}]},"uuid":"u1","timestamp":"2026-11-01T10:00:00.000Z"}`
}

// cutProblems prüft eine gespeicherte Zeile: kein Stück des Geheimnisses, und
// der Hash im Marker gehört zum geschwärzten Text, nicht zum ursprünglichen.
func cutProblems(c secretCut, stored string) []string {
	var bad []string
	for _, f := range c.fragments {
		if strings.Contains(stored, f) {
			bad = append(bad, "fragment "+f+" stored")
		}
	}
	var line struct {
		Message struct {
			Content []struct {
				Content string `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(redact.RedactJSON(toolResultLine(c.out))), &line); err != nil || len(line.Message.Content) != 1 {
		return append(bad, "cannot read the redacted line")
	}
	redacted := line.Message.Content[0].Content
	sum := sha256.Sum256([]byte(redacted))
	if !strings.Contains(stored, "sha256:"+hex.EncodeToString(sum[:8])) {
		bad = append(bad, "marker does not carry the hash of the redacted text")
	}
	orig := sha256.Sum256([]byte(c.out))
	if strings.Contains(stored, hex.EncodeToString(orig[:8])) {
		bad = append(bad, "marker carries the hash of the unredacted text")
	}
	return bad
}

func TestAppendChunksRedactsSecretsAtTheCut(t *testing.T) {
	s := openTest(t)
	for i, c := range secretCuts() {
		id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: fmt.Sprintf("cut-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendChunks(id, []Chunk{{Seq: 0, Raw: toolResultLine(c.out)}}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ReadSession(id, 0, 1)
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: chunks %d err %v", c.name, len(got), err)
		}
		for _, p := range cutProblems(c, got[0].Raw) {
			t.Errorf("%s: %s", c.name, p)
		}
	}
}
