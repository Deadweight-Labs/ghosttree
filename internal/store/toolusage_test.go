package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// The question this exists to answer is "is ghosttree used outside the
// repository that builds it", and before tool calls were archived it could only
// be approximated by grepping for the tool's name — which counts a session that
// discusses the tool the same as one that calls it.
func TestToolCallsPerProjectCountsCallsNotMentions(t *testing.T) {
	s := openTest(t)
	user, _ := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "a",
		Scope: scope.Axes{Project: "github.com/x/user"}, StartedAt: "2026-08-23T00:00:00Z"})
	talker, _ := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "b",
		Scope: scope.Axes{Project: "github.com/x/talker"}, StartedAt: "2026-08-23T00:00:00Z"})

	if err := s.AppendChunks(user, []Chunk{
		toolCallChunk(1, "mcp__ghosttree__context_search", `{"query":"ufw"}`),
		toolCallChunk(2, "mcp__ghosttree__context_remember", `{"type":"pitfall"}`),
		toolCallChunk(3, "Bash", `{"command":"ls"}`),
	}); err != nil {
		t.Fatal(err)
	}
	// Mentions the tool without calling it: a grep would count this session.
	if err := s.AppendChunks(talker, []Chunk{
		{Seq: 1, Role: "assistant", Text: "We could use mcp__ghosttree__context_search here.",
			Raw: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"We could use mcp__ghosttree__context_search here."}]}}`},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ToolCallsPerProject("mcp__ghosttree__")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %+v, want only the project that called", got)
	}
	if got[0].Project != "github.com/x/user" || got[0].Calls != 2 || got[0].Sessions != 1 {
		t.Errorf("row = %+v, want 2 calls in 1 session of github.com/x/user", got[0])
	}
}

// toolCallChunk is an assistant line with one tool call, as the collector
// uploads it: the line, and the text the collector derived from it.
func toolCallChunk(seq int, name, input string) Chunk {
	raw := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"` + name + `","input":` + input + `}]}}`
	_, text := chunktext.Derive("claude-code", raw)
	return Chunk{Seq: seq, Role: "assistant", Text: text, Raw: raw}
}
