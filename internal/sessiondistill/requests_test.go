package sessiondistill

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The mode exists because a wish said in passing is lost when the session ends.
// It must read the person, not the agent: once both are in a transcript they
// look alike, and only one of them is a requirement.
func TestUserChunksKeepOnlyWhatAPersonTyped(t *testing.T) {
	got := UserChunks([]store.Chunk{
		{Seq: 1, Role: "user", Text: "wär cool wenn man das auch exportieren könnte"},
		{Seq: 2, Role: "assistant", Text: "Ich schlage vor, einen Export zu bauen."},
		{Seq: 3, Role: "tool", Text: "PASS"},
		{Seq: 4, Role: "user", Text: "<task-notification>a03</task-notification>"},
		{Seq: 5, Role: "user", Text: "   "},
	})
	if len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("kept %+v, want only the typed message", got)
	}
}

// A slash command or a skill expands into a plain user turn: first person, no
// marker, indistinguishable from typed text by its wording alone. REQ-128 was
// filed from the /init boilerplate as if somebody had asked for it. The raw
// lines below are the shape a real transcript has — the launch carries
// toolUseResult.commandName, the expansion hangs off it by parentUuid.
func TestUserChunksDropAnExpandedCommand(t *testing.T) {
	launch := `{"uuid":"u-launch","parentUuid":"u-assistant","type":"user",` +
		`"message":{"role":"user","content":[{"type":"tool_result","content":"Launching skill: init"}]},` +
		`"toolUseResult":{"success":true,"commandName":"init"}}`
	expansion := `{"uuid":"u-expansion","parentUuid":"u-launch","type":"user",` +
		`"message":{"role":"user","content":[{"type":"text","text":"Please analyze this codebase…"}]}}`
	typed := `{"uuid":"u-typed","parentUuid":"u-expansion","type":"user",` +
		`"message":{"role":"user","content":[{"type":"text","text":"und exportieren wär nice"}]}}`

	got := UserChunks([]store.Chunk{
		{Seq: 1, Role: "user", Text: "", Raw: launch},
		{Seq: 2, Role: "user", Text: "Please analyze this codebase and create a CLAUDE.md file, which will be given to future instances of Claude Code.", Raw: expansion},
		{Seq: 3, Role: "user", Text: "und exportieren wär nice", Raw: typed},
	})
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("kept %+v, want only the typed message", got)
	}
}

// The signal has to be the parent, not the prompt the turn belongs to. Measured
// on 40,763 user turns: matching by promptId flagged 187 turns and swept up
// genuinely typed sentences that merely preceded a skill launch in the same
// prompt; matching by parentUuid flagged 95, every one of them machine text.
func TestATypedMessageBeforeACommandSurvives(t *testing.T) {
	typed := `{"uuid":"u-typed","parentUuid":"u-earlier","promptId":"p1","type":"user"}`
	launch := `{"uuid":"u-launch","parentUuid":"u-typed","promptId":"p1","type":"user",` +
		`"toolUseResult":{"commandName":"superpowers:brainstorming"}}`
	expansion := `{"uuid":"u-expansion","parentUuid":"u-launch","promptId":"p1","type":"user"}`

	got := UserChunks([]store.Chunk{
		{Seq: 1, Role: "user", Text: "main als user, vom pve müsste das per pubkey gehen", Raw: typed},
		{Seq: 2, Role: "user", Text: "", Raw: launch},
		{Seq: 3, Role: "user", Text: "Base directory for this skill: /home/user/.claude/plugins/…", Raw: expansion},
	})
	if len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("kept %+v, want the typed sentence and nothing else", got)
	}
}

// A transcript line ghosttree cannot parse, or one whose toolUseResult is a
// bare string, must not take a real message down with it.
func TestUnparsableRawKeepsTheMessage(t *testing.T) {
	got := UserChunks([]store.Chunk{
		{Seq: 1, Role: "user", Text: "bitte das noch bauen", Raw: "not json at all"},
		{Seq: 2, Role: "user", Text: "und das hier auch", Raw: `{"uuid":"u2","toolUseResult":"plain string"}`},
	})
	if len(got) != 2 {
		t.Fatalf("kept %+v, want both messages", got)
	}
}

func TestParseRequestsDropsAnUngroundedWish(t *testing.T) {
	chunks := []store.Chunk{{Seq: 1, Role: "user", Text: "und exportieren wär auch nice"}}
	got, err := ParseRequests(`{"items":[
		{"type":"feature","title":"Export","body":"b","chunk_seq":1,"quote":"exportieren wär auch nice"},
		{"type":"feature","title":"Erfunden","body":"b","chunk_seq":1,"quote":"das hat niemand gesagt"}]}`, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Export" {
		t.Fatalf("got %+v, want only the grounded wish", got)
	}
}

// Nothing grounding at all is a different failure from a partial one: the model
// spoke and none of it traces back to the person.
func TestParseRequestsRejectsAReplyWithNoGrounding(t *testing.T) {
	chunks := []store.Chunk{{Seq: 1, Role: "user", Text: "mach mal weiter"}}
	if _, err := ParseRequests(`{"items":[{"type":"feature","title":"X","body":"b","chunk_seq":1,"quote":"nope"}]}`, chunks); err == nil {
		t.Fatal("ungrounded reply accepted")
	}
}

func TestRequestPromptCarriesBacklogNumbers(t *testing.T) {
	p := RequestPrompt(
		[]store.Chunk{{Seq: 2, Role: "user", Text: "export wär nice"}},
		[]string{"#7 [open] Export als CSV", "#9 [dropped] Dark Mode"})
	for _, want := range []string{"#7 [open]", "#9 [dropped]", "[chunk 2]", "export wär nice"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestParseRequestsCapsOneTranscript(t *testing.T) {
	chunks := []store.Chunk{{Seq: 1, Role: "user", Text: "a b c d e f"}}
	var items []string
	for _, q := range []string{"a", "b", "c", "d", "e", "f"} {
		items = append(items, `{"type":"feature","title":"`+q+`","body":"b","chunk_seq":1,"quote":"`+q+`"}`)
	}
	got, err := ParseRequests(`{"items":[`+strings.Join(items, ",")+`]}`, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxRequestItems {
		t.Errorf("kept %d, want the cap of %d", len(got), MaxRequestItems)
	}
}
