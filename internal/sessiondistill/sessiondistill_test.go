package sessiondistill

import (
	"context"
	"strings"
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

// Seven of the nine items produced by the first production run came from a
// single 7287-character summary message. A summary restates what the session
// already did, so mining it repeatedly yields the same fact under different
// titles — and it was those replies that ran into the output cap.
func TestParseLimitsItemsPerChunkAndPerTranscript(t *testing.T) {
	chunks := []store.Chunk{
		{Seq: 1, Role: "assistant", Text: "alpha beta gamma delta epsilon zeta"},
		{Seq: 2, Role: "assistant", Text: "eta theta"},
	}
	var items []string
	for _, quote := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		items = append(items, `{"type":"note","title":"`+quote+`","body":"b","chunk_seq":1,"quote":"`+quote+`"}`)
	}
	items = append(items, `{"type":"note","title":"eta","body":"b","chunk_seq":2,"quote":"eta"}`)
	got, err := Parse(`{"items":[`+strings.Join(items, ",")+`]}`, chunks)
	if err != nil {
		t.Fatal(err)
	}
	perChunk := map[int]int{}
	for _, item := range got {
		perChunk[item.ChunkSeq]++
	}
	if perChunk[1] != MaxItemsPerChunk {
		t.Fatalf("kept %d items from chunk 1, want at most %d", perChunk[1], MaxItemsPerChunk)
	}
	if len(got) != MaxItemsPerChunk+1 {
		t.Fatalf("kept %d items, want the two from chunk 1 plus the one from chunk 2", len(got))
	}
}

// Grounding is a property of one item. Rejecting the whole reply over a single
// ungrounded quote threw away the sound items alongside it — and it threw away
// the session with them, because a rejected reply is recorded as a distillation
// of zero items. Measured on the first production run: 17 of 50 sessions were
// discarded this way, every one of them for an ungrounded quote, and several of
// those replies had a valid item 0.
func TestParseDropsUngroundedItemsWithoutDiscardingTheRest(t *testing.T) {
	chunks := []store.Chunk{{Seq: 4, Role: "assistant", Text: "The retry loop was removed because the queue already redelivers."}}
	raw := `{"items":[
		{"type":"decision","title":"Drop the retry loop","body":"The queue redelivers.","chunk_seq":4,"quote":"the queue already redelivers"},
		{"type":"note","title":"Invented","body":"x","chunk_seq":4,"quote":"a sentence nobody wrote"}]}`
	got, err := Parse(raw, chunks)
	if err != nil {
		t.Fatalf("Parse rejected a reply with one sound item: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Drop the retry loop" {
		t.Fatalf("items = %+v, want only the grounded one kept", got)
	}
}

// A reply in which nothing grounds is not a partial result, it is a wrong
// answer, and recording it as "this session had nothing to say" would be a lie
// about a model that said plenty.
func TestParseFailsWhenNoItemGrounds(t *testing.T) {
	chunks := []store.Chunk{{Seq: 4, Role: "assistant", Text: "The retry loop was removed."}}
	raw := `{"items":[{"type":"note","title":"Invented","body":"x","chunk_seq":4,"quote":"nobody wrote this"}]}`
	if _, err := Parse(raw, chunks); err == nil {
		t.Fatal("a reply with no grounded item was accepted")
	}
}
