package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A password behind a label never leaves the machine: the chunk that is
// uploaded holds the marker in text and in raw, and raw is still one JSON line.
// All values are made up.
func TestSyncFileRedactsPasswordsBehindALabel(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "pw.jsonl")
	user := func(text string) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "cwd": "/tmp", "gitBranch": "", "sessionId": "pw",
			"message": map[string]any{"role": "user", "content": text}})
		return string(b)
	}
	lines := []string{
		user("Mailkonto Passwort QDzt#fakeWRD%7kTest! danach wieder Text"),
		user("zeile eins\nPasswort: Hx7!kQ2pz\nzeile drei"),
		user(`mein pw ist "Sommer2026!"`),
		user("Passwort zurücksetzen und password := r.FormValue(\"password\")"),
	}
	if err := os.WriteFile(fp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := &fakeUp{}
	if err := SyncFile(fp, "claude-code", up, newTestState(dir), "m"); err != nil {
		t.Fatal(err)
	}
	got := up.chunks[1]
	if len(got) != 4 {
		t.Fatalf("chunks = %d", len(got))
	}
	for i, c := range got[:3] {
		for _, secret := range []string{"fakeWRD", "Hx7!kQ2pz", "Sommer2026"} {
			if strings.Contains(c.Text, secret) || strings.Contains(c.Raw, secret) {
				t.Errorf("chunk %d still holds %q: text %q raw %s", i, secret, c.Text, c.Raw)
			}
		}
		if !strings.Contains(c.Text, "[REDACTED:labeled]") || !strings.Contains(c.Raw, "[REDACTED:labeled]") {
			t.Errorf("chunk %d lacks the marker: text %q raw %s", i, c.Text, c.Raw)
		}
		if !json.Valid([]byte(c.Raw)) {
			t.Errorf("chunk %d raw is not JSON: %s", i, c.Raw)
		}
	}
	if got[3].Raw != lines[3] || !strings.Contains(got[3].Text, `Passwort zurücksetzen`) {
		t.Errorf("prose and code were changed: %+v", got[3])
	}
}
