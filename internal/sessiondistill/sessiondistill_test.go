package sessiondistill

import (
	"context"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakeModel struct{ reply string }

func (f fakeModel) Complete(context.Context, string, []llm.Message, int) (string, error) {
	return f.reply, nil
}

func TestDistillRequiresChunkGroundingAndRejectsRequests(t *testing.T) {
	chunks := []store.Chunk{{Seq: 7, Role: "assistant", Text: "SQLite was chosen because one writer is sufficient."}}
	items, err := Distill(context.Background(), fakeModel{`{"items":[{"type":"decision","title":"Use SQLite","body":"One writer is sufficient.","chunk_seq":7,"quote":"SQLite was chosen"}]}`}, chunks, nil)
	if err != nil || len(items) != 1 || items[0].ChunkSeq != 7 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if _, err := Distill(context.Background(), fakeModel{`{"items":[{"type":"request","title":"Build UI","body":"Do it","chunk_seq":7,"quote":"SQLite was chosen"}]}`}, chunks, nil); err == nil {
		t.Fatal("request entered knowledge distillation")
	}
	if _, err := Distill(context.Background(), fakeModel{`{"items":[{"type":"note","title":"Fake","body":"x","chunk_seq":7,"quote":"Postgres"}]}`}, chunks, nil); err == nil {
		t.Fatal("ungrounded quote accepted")
	}
}
