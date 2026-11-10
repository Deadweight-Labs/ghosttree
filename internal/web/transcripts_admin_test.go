package web

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestAdminSeesTranscriptBytesPerLineTypeAndOthersDoNot(t *testing.T) {
	e := ovEnv(t)
	id, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "bytes", StartedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []store.Chunk
	for i := 0; i < 20; i++ {
		chunks = append(chunks, store.Chunk{Seq: i, Role: "assistant", Text: "ok",
			Raw: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("the build is green ", 10) + `"}]},"timestamp":"2026-11-01T10:00:00Z"}`})
	}
	if err := e.St.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	_, admin := fetchPage(t, e.Owner, e.Base+"/ui/orgs")
	for _, want := range []string{`id="transcripts"`, "<code>claude:assistant</code>", "ctx db slim-sessions"} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin block lacks %q", want)
		}
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/orgs")
	if strings.Contains(member, `id="transcripts"`) {
		t.Error("a member sees the transcript block")
	}
}
