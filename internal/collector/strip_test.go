package collector

import (
	"encoding/base64"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bilder verlassen die Maschine nicht: der Chunk, der hochgeladen wird, trägt
// schon den Platzhalter, und das Transkript dahinter bleibt unberührt.
func TestSyncFileUploadsPlaceholdersInsteadOfImages(t *testing.T) {
	pixels := make([]byte, 200_000)
	rand.New(rand.NewSource(1)).Read(pixels)
	b64 := base64.StdEncoding.EncodeToString(pixels)
	dir := t.TempDir()
	fp := filepath.Join(dir, "img.jsonl")
	claude := `{"type":"user","cwd":"/tmp","gitBranch":"","sessionId":"img","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"screenshot taken"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}}`
	text := `{"type":"assistant","cwd":"/tmp","gitBranch":"","sessionId":"img","message":{"role":"assistant","content":[{"type":"text","text":"looks fine"}]}}`
	if err := os.WriteFile(fp, []byte(claude+"\n"+text+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := &fakeUp{}
	if err := SyncFile(fp, "claude-code", up, newTestState(dir), "m"); err != nil {
		t.Fatal(err)
	}
	got := up.chunks[1]
	if len(got) != 2 {
		t.Fatalf("chunks = %d", len(got))
	}
	if strings.Contains(got[0].Raw, b64[:100]) || len(got[0].Raw) > 2000 {
		t.Fatalf("image went over the wire: %d bytes", len(got[0].Raw))
	}
	if !json.Valid([]byte(got[0].Raw)) || !strings.Contains(got[0].Raw, "[ghosttree: Bild entfernt, image/png, 200000 Bytes, sha256:") {
		t.Fatalf("no placeholder: %.400s", got[0].Raw)
	}
	if !strings.Contains(got[0].Text, "screenshot taken") {
		t.Errorf("text lost: %q", got[0].Text)
	}
	if got[1].Raw != text {
		t.Error("a line without an image was changed")
	}
}
