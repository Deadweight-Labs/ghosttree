package web

import (
	"encoding/base64"
	"math/rand"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Ein Chunk mit Bild wird mit Platzhalter gespeichert; die Sitzungsansicht und
// die Suche zeigen dasselbe wie vorher: sie lesen nur Text.
func TestSessionDetailAndSearchWorkWithStrippedImages(t *testing.T) {
	e := seedSessions(t)
	id := e.id["alice-private"]
	pixels := make([]byte, 300_000)
	rand.New(rand.NewSource(7)).Read(pixels)
	b64 := base64.StdEncoding.EncodeToString(pixels)
	shot := jl(map[string]any{"type": "user", "timestamp": "2026-10-01T10:00:03Z", "message": map[string]any{"role": "user", "content": []map[string]any{{
		"type": "tool_result", "tool_use_id": "shot1", "content": []map[string]any{
			{"type": "text", "text": "narwhal screenshot of the login page"},
			{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": b64}},
		}}}}})
	if err := e.St.AppendChunks(id, []store.Chunk{
		{Seq: 60, Raw: aLine(bash("shot1", "take-screenshot login"))},
		{Seq: 61, Role: "tool", Text: "narwhal screenshot of the login page", Raw: shot},
		{Seq: 62, Raw: aLine(tb("The page shows the narwhal banner."))},
	}); err != nil {
		t.Fatal(err)
	}
	cid, _ := e.St.UpsertSession(store.Session{Harness: "codex", ExternalID: "cx-img", AccountID: 1, Scope: scope.Axes{Project: shellProject}})
	codex := func(typ string, payload map[string]any) string {
		return jl(map[string]any{"timestamp": "2026-10-01T11:00:00Z", "type": typ, "payload": payload})
	}
	if err := e.St.AppendChunks(cid, chunks(
		codex("event_msg", map[string]any{"type": "user_message", "message": "look at the screenshot"}),
		codex("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "c9", "output": []map[string]any{
			{"type": "input_image", "image_url": "data:image/png;base64," + b64, "detail": "high"}}}),
		codex("response_item", map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "the screenshot shows a login form"}}}),
	)); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := e.St.DB().QueryRow(`SELECT sum(length(raw)) FROM session_chunks WHERE (session_id = ? AND seq >= 60) OR session_id = ?`, id, cid).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored > 6000 {
		t.Fatalf("the images were stored: %d bytes", stored)
	}

	code, page := e.get(t, e.Owner, "/ui/sessions/"+e.pid["alice-private"]+"?at=61")
	if code != 200 {
		t.Fatalf("detail status %d", code)
	}
	for _, want := range []string{"take-screenshot login", "The page shows the narwhal banner."} {
		if !strings.Contains(page, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if strings.Contains(page, b64[:100]) {
		t.Error("image data in the page")
	}
	code, page = e.get(t, e.Owner, "/ui/sessions?q=narwhal")
	if code != 200 || !strings.Contains(page, "<mark>narwhal</mark>") {
		t.Fatalf("search status %d: %.300s", code, page)
	}
	csess, _ := e.St.SessionByID(cid)
	code, page = e.get(t, e.Owner, "/ui/sessions/"+csess.PublicID)
	if code != 200 || !strings.Contains(page, "look at the screenshot") || !strings.Contains(page, "the screenshot shows a login form") {
		t.Fatalf("codex detail status %d: %.300s", code, page)
	}
}
