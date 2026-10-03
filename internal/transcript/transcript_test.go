package transcript

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// load reads a testdata transcript and parses every line as the chunk with
// that line number, the way the store sees them.
func load(t *testing.T, harness, file string) ([]Block, []Parsed) {
	t.Helper()
	f, err := os.Open("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var all []Block
	var parsed []Parsed
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for seq := 0; sc.Scan(); seq++ {
		p := Parse(harness, seq, sc.Text())
		parsed = append(parsed, p)
		all = append(all, p.Blocks...)
	}
	return all, parsed
}

func kinds(blocks []Block) string {
	var out []string
	for _, b := range blocks {
		out = append(out, string(b.Kind))
	}
	return strings.Join(out, ",")
}

func TestClaudeBlocksInOrderWithoutNoise(t *testing.T) {
	blocks, _ := load(t, "claude-code", "claude.jsonl")
	// queue-operation, attachment, isMeta caveat, ai-title, last-prompt: no block.
	want := "user,thinking,assistant,tool,result,tool,result,tool,result,assistant,user"
	if got := kinds(blocks); got != want {
		t.Fatalf("kinds = %s\nwant    %s", got, want)
	}
	if blocks[0].Text != "Fix the 403 on the code login in Chromium" || blocks[0].Seq != 1 {
		t.Errorf("prompt block = %+v", blocks[0])
	}
	if !strings.Contains(blocks[1].Text, "Origin: null") {
		t.Errorf("thinking text lost: %q", blocks[1].Text)
	}
	if blocks[0].Time != "2026-10-01T10:00:01.000Z" {
		t.Errorf("time = %q", blocks[0].Time)
	}
}

func TestClaudeToolCallsPairWithTheirResults(t *testing.T) {
	blocks, _ := load(t, "claude-code", "claude.jsonl")
	paired := Pair(blocks)
	var tools []Block
	for _, b := range paired {
		if b.Kind == KindTool {
			tools = append(tools, b)
		}
		if b.Kind == KindResult {
			t.Errorf("result %q left unpaired", b.CallID)
		}
	}
	if len(tools) != 3 {
		t.Fatalf("%d tool blocks, want 3", len(tools))
	}
	bash := tools[0]
	if bash.Tool != "Bash" || bash.Input != "grep -rn 'Origin' internal/web/login.go" {
		t.Errorf("bash = %q / %q", bash.Tool, bash.Input)
	}
	if !bash.HasOutput || !strings.Contains(bash.Output, "cross-origin request rejected") || bash.Failed {
		t.Errorf("bash result = %+v", bash)
	}
	edit := tools[1]
	if edit.Tool != "Edit" || edit.Input != "internal/web/login.go" {
		t.Errorf("edit = %q / %q", edit.Tool, edit.Input)
	}
	if len(edit.Diff) != 1 || edit.Diff[0].Path != "internal/web/login.go" {
		t.Fatalf("edit diff = %+v", edit.Diff)
	}
	var minus, plus int
	for _, l := range edit.Diff[0].Lines {
		switch l.Op {
		case '-':
			minus++
		case '+':
			plus++
		}
	}
	if minus != 1 || plus != 1 {
		t.Errorf("diff -%d +%d, want -1 +1", minus, plus)
	}
	if edit.Output != "The file internal/web/login.go has been updated." {
		t.Errorf("array-shaped tool result = %q", edit.Output)
	}
	failed := tools[2]
	if !failed.Failed || failed.Status != "error" {
		t.Errorf("is_error result not marked failed: %+v", failed)
	}
}

func TestClaudeTitleAndMetaLines(t *testing.T) {
	_, parsed := load(t, "claude-code", "claude.jsonl")
	var title string
	for _, p := range parsed {
		if p.Title != "" {
			title = p.Title
		}
	}
	if title != "Fix 403 on code login in Chromium" {
		t.Errorf("title = %q", title)
	}
	for i, p := range parsed {
		if (i == 0 || i == 9 || i == 11 || i == 13 || i == 14) && len(p.Blocks) != 0 {
			t.Errorf("line %d produced blocks: %+v", i, p.Blocks)
		}
	}
}

func TestCodexBlocksUseTheSameModel(t *testing.T) {
	blocks, _ := load(t, "codex", "codex.jsonl")
	// environment context, empty reasoning, token_count, patch_apply_end and
	// the duplicate agent_message produce nothing.
	want := "user,thinking,tool,result,tool,result,tool,result,assistant"
	if got := kinds(blocks); got != want {
		t.Fatalf("kinds = %s\nwant    %s", got, want)
	}
	if blocks[0].Text != "Why does the kafka consumer lag after restart?" {
		t.Errorf("prompt = %q", blocks[0].Text)
	}
	if blocks[1].Text != "Looking at the consumer group offsets first." {
		t.Errorf("reasoning summary = %q", blocks[1].Text)
	}
}

func TestCodexToolResultsCarryExitStatus(t *testing.T) {
	blocks, _ := load(t, "codex", "codex.jsonl")
	var tools []Block
	for _, b := range Pair(blocks) {
		if b.Kind == KindTool {
			tools = append(tools, b)
		}
	}
	if len(tools) != 3 {
		t.Fatalf("%d tools", len(tools))
	}
	ok, bad, patch := tools[0], tools[1], tools[2]
	if ok.Tool != "exec_command" || ok.Input != "kafka-consumer-groups --describe --group billing" {
		t.Errorf("exec = %q / %q", ok.Tool, ok.Input)
	}
	if ok.Failed || !strings.HasPrefix(ok.Output, "GROUP TOPIC") {
		t.Errorf("ok output = %q failed=%v", ok.Output, ok.Failed)
	}
	if !bad.Failed || bad.Status != "exit 1" || !strings.Contains(bad.Output, "No such file") {
		t.Errorf("failing exec = %+v", bad)
	}
	if patch.Tool != "apply_patch" || len(patch.Diff) != 2 {
		t.Fatalf("patch = %+v", patch)
	}
	if patch.Diff[0].Path != "internal/consumer/offsets.go" || patch.Diff[1].Path != "notes.md" {
		t.Errorf("patch paths = %q, %q", patch.Diff[0].Path, patch.Diff[1].Path)
	}
	if patch.Input != "internal/consumer/offsets.go, notes.md" {
		t.Errorf("patch input = %q", patch.Input)
	}
}

func TestOrphanResultStaysVisible(t *testing.T) {
	out := Pair([]Block{{Kind: KindResult, CallID: "gone", Output: "late output", HasOutput: true}})
	if len(out) != 1 || out[0].Kind != KindResult || out[0].Output != "late output" {
		t.Fatalf("orphan = %+v", out)
	}
}

func TestIndexFieldsCoverEveryKind(t *testing.T) {
	_, parsed := load(t, "claude-code", "claude.jsonl")
	var text, thinking, in, out []string
	prompts, calls := 0, 0
	for _, p := range parsed {
		f := Fields(p)
		text = append(text, f.Text)
		thinking = append(thinking, f.Thinking)
		in = append(in, f.ToolInput)
		out = append(out, f.ToolOutput)
		if f.Prompt {
			prompts++
		}
		calls += f.ToolCalls
	}
	join := func(s []string) string { return strings.Join(s, "\n") }
	for _, c := range []struct{ name, hay, needle string }{
		{"text", join(text), "regression test"},
		{"assistant text", join(text), "old header"},
		{"thinking", join(thinking), "opaque origins"},
		{"tool input command", join(in), "grep -rn"},
		{"tool input edit", join(in), `origin != "null"`},
		{"tool output", join(out), "cross-origin request rejected"},
		{"failing output", join(out), "exit status 1"},
	} {
		if !strings.Contains(c.hay, c.needle) {
			t.Errorf("%s: %q not indexed", c.name, c.needle)
		}
	}
	if strings.Contains(join(text), "opaque origins") || strings.Contains(join(text), "cross-origin request") {
		t.Error("thinking or tool output leaked into the message field")
	}
	if prompts != 2 || calls != 3 {
		t.Errorf("prompts=%d toolcalls=%d, want 2 and 3", prompts, calls)
	}
}

func TestIndexFieldsAreCutAtEightKilobytesOnARuneBoundary(t *testing.T) {
	huge := strings.Repeat("ä", 20000)
	raw := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"` + huge + `"}]}}`
	f := Fields(Parse("claude-code", 0, raw))
	if len(f.ToolOutput) > MaxIndexBytes || len(f.ToolOutput) < MaxIndexBytes-4 {
		t.Fatalf("len = %d", len(f.ToolOutput))
	}
	if !utf8.ValidString(f.ToolOutput) {
		t.Error("cut inside a rune")
	}
	// The display model keeps the whole result; only the index is cut.
	p := Parse("claude-code", 0, raw)
	if len(p.Blocks) != 1 || len(p.Blocks[0].Output) != len(huge) {
		t.Errorf("display block output was cut: %d", len(p.Blocks[0].Output))
	}
}

func TestGarbageNeverFails(t *testing.T) {
	for _, raw := range []string{"", "{}", "not json", `{"type":"user"}`, `{"type":"assistant","message":{"content":42}}`, `[]`} {
		for _, h := range []string{"claude-code", "codex", "other"} {
			if p := Parse(h, 3, raw); len(p.Blocks) != 0 {
				t.Errorf("%s %q produced blocks", h, raw)
			}
		}
	}
}

func TestPromptOutlineCountsToolCallsUntilTheNextPrompt(t *testing.T) {
	_, parsed := load(t, "claude-code", "claude.jsonl")
	var rows []OutlineRow
	for seq, p := range parsed {
		f := Fields(p)
		rows = append(rows, OutlineRow{Seq: seq, Prompt: f.Prompt, ToolCalls: f.ToolCalls, Time: f.Time, Text: f.Text})
	}
	out := Outline(rows)
	if len(out) != 2 {
		t.Fatalf("%d prompts", len(out))
	}
	if out[0].Seq != 1 || out[0].ToolCalls != 3 || out[1].ToolCalls != 0 {
		t.Errorf("outline = %+v", out)
	}
}
