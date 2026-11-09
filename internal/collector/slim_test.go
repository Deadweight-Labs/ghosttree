package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Zeilen ohne Leser verlassen die Maschine nicht. Die übrigen behalten ihre
// Nummer, die Folge hat Lücken, und der Offset wandert auch über einen Schluss
// aus lauter weggelassenen Zeilen.
func TestSyncFileLeavesOutCopiesAndKeepsNumbers(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "slim.jsonl")
	lines := []string{
		`{"type":"user","cwd":"/tmp","gitBranch":"","sessionId":"slim","message":{"role":"user","content":"fix it"},"uuid":"u1","timestamp":"2026-11-01T10:00:00.000Z"}`,
		`{"type":"api-request","params":{"model":"m"},"sessionId":"slim","timestamp":"2026-11-01T10:00:01.000Z"}`,
		`{"type":"assistant","cwd":"/tmp","gitBranch":"","sessionId":"slim","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"plan","signature":"c2ln"},{"type":"text","text":"done"}]},"wireToolInputs":{},"uuid":"a1","timestamp":"2026-11-01T10:00:02.000Z"}`,
		`{"type":"api-request-blob","message":{"role":"assistant","content":[]},"sessionId":"slim","timestamp":"2026-11-01T10:00:03.000Z"}`,
	}
	if err := os.WriteFile(fp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := &fakeUp{}
	st := newTestState(dir)
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	got := up.chunks[1]
	if len(got) != 2 || got[0].Seq != 0 || got[1].Seq != 2 {
		t.Fatalf("uploaded %+v, want seq 0 and 2", got)
	}
	if strings.Contains(got[1].Raw, "signature") || strings.Contains(got[1].Raw, "wireToolInputs") || !strings.Contains(got[1].Raw, `"thinking":"plan"`) {
		t.Errorf("assistant line not slimmed: %s", got[1].Raw)
	}
	fi, _ := os.Stat(fp)
	if fs := st.file(fp); fs.Offset != fi.Size() || fs.Seq != 4 {
		t.Errorf("offset %d of %d, seq %d", fs.Offset, fi.Size(), fs.Seq)
	}
	// Nur weggelassene Zeilen dazu: nichts geht hoch, der Offset wandert.
	f, _ := os.OpenFile(fp, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(lines[1] + "\n")
	f.Close()
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(fp)
	if fs := st.file(fp); fs.Offset != fi.Size() || fs.Seq != 5 || len(up.chunks[1]) != 2 {
		t.Errorf("after dropped tail: offset %d of %d, seq %d, chunks %d", fs.Offset, fi.Size(), fs.Seq, len(up.chunks[1]))
	}
	// Der nächste echte Prompt bekommt die nächste Nummer.
	f, _ = os.OpenFile(fp, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(lines[0] + "\n")
	f.Close()
	if err := SyncFile(fp, "claude-code", up, st, "m"); err != nil {
		t.Fatal(err)
	}
	if c := up.chunks[1]; len(c) != 3 || c[2].Seq != 5 {
		t.Errorf("next chunk %+v", c[len(c)-1])
	}
}

// Die Schwärzung sieht die ganze Ausgabe, bevor gekürzt wird: ein privater
// Schlüssel, dessen END hinter dem Schnitt läge, geht trotzdem nicht hoch.
func TestSyncFileRedactsBeforeCutting(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "key.jsonl")
	body := "MIIEowIBAAKCAQEAfakekeymaterial"
	var b strings.Builder
	b.WriteString(strings.Repeat("log line before the key\n", 200))
	b.WriteString("-----BEGIN RSA " + "PRIVATE KEY-----\n")
	for b.Len() < 40_000 {
		b.WriteString(body + "\n")
	}
	b.WriteString("-----END RSA " + "PRIVATE KEY-----\n")
	b.WriteString(strings.Repeat("log line after the key\n", 200))
	enc, _ := json.Marshal(b.String())
	line := `{"type":"user","cwd":"/tmp","gitBranch":"","sessionId":"key","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + string(enc) + `}]},"uuid":"u1","timestamp":"2026-11-01T10:00:00.000Z"}`
	if err := os.WriteFile(fp, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := &fakeUp{}
	if err := SyncFile(fp, "claude-code", up, newTestState(dir), "m"); err != nil {
		t.Fatal(err)
	}
	got := up.chunks[1]
	if len(got) != 1 || strings.Contains(got[0].Raw, body) || !strings.Contains(got[0].Raw, "log line before the key") {
		t.Fatalf("uploaded %d chunks; key material present: %v", len(got), len(got) == 1 && strings.Contains(got[0].Raw, body))
	}
}
