package collector

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
)

func lines(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	return out
}

func TestParseClaude(t *testing.T) {
	ls := lines(t, "testdata/claude.jsonl")
	if p := ParseClaudeLine(ls[0]); p.Role != "" || p.Text != "" {
		t.Errorf("queue-operation must parse empty, got %+v", p)
	}
	if p := ParseClaudeLine(ls[1]); p.Role != "user" || p.Text != "fix the auth bug" {
		t.Errorf("user line: %+v", p)
	}
	if p := ParseClaudeLine(ls[2]); p.Role != "assistant" || p.Text != "Looking at the oauth flow now." {
		t.Errorf("assistant line: %+v", p)
	}
	id, cwd, branch := ClaudeSessionMeta("/home/x/.claude/projects/-home-main/9708008b.jsonl", ls)
	if id != "9708008b" || cwd != "/home/user/Projects/demo" || branch != "main" {
		t.Errorf("meta: %q %q %q", id, cwd, branch)
	}
}

func TestParseCodex(t *testing.T) {
	ls := lines(t, "testdata/codex.jsonl")
	if p := ParseCodexLine(ls[1]); p.Role != "user" || p.Text != "why does the build fail" {
		t.Errorf("user line: %+v", p)
	}
	if p := ParseCodexLine(ls[3]); p.Role != "" {
		t.Errorf("event_msg must parse empty, got %+v", p)
	}
	// Codex also emits role "developer"; keep the text rather than drop it.
	if p := ParseCodexLine(ls[4]); p.Role != "developer" || p.Text != "tool instructions follow" {
		t.Errorf("developer line: %+v", p)
	}
	id, cwd, _, _ := CodexSessionMeta(ls)
	if id != "019f6d31-504b-7b50-b171-8d89cc3e9c49" || cwd != "/home/user/Projects/ExampleProject" {
		t.Errorf("meta: %q %q", id, cwd)
	}
	if p := ParseCodexLine([]byte("not json at all")); p.Role != "" || p.Text != "" {
		t.Errorf("garbage must parse empty, got %+v", p)
	}
}

func TestCodexSessionMetaPrefersEmbeddedGitContext(t *testing.T) {
	line := []byte(`{"type":"session_meta","payload":{"session_id":"s","cwd":"/missing","git":{"repository_url":"https://GitHub.com/Deadweight-Labs/Ghosttree.git","branch":"main"}}}`)
	id, cwd, project, branch := CodexSessionMeta([][]byte{line})
	if id != "s" || cwd != "/missing" || project != "github.com/deadweight-labs/ghosttree" || branch != "main" {
		t.Fatalf("meta=%q %q %q %q", id, cwd, project, branch)
	}
}

func TestParseStructuredToolResults(t *testing.T) {
	codex := []byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":{"stdout":"migration complete","result":{"path":"docs/plan.md","count":3}}}}`)
	if p := ParseCodexLine(codex); p.Role != "tool" || !strings.Contains(p.Text, "migration complete") || !strings.Contains(p.Text, "docs/plan.md") {
		t.Fatalf("codex tool result=%+v", p)
	}
	claude := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":[{"type":"text","text":"tests passed"},{"type":"text","text":"commit abc123"}]}]}}`)
	if p := ParseClaudeLine(claude); p.Role != "tool" || p.Text != "tests passed\ncommit abc123" {
		t.Fatalf("claude tool result=%+v", p)
	}
}

func TestGitInfoOutsideRepo(t *testing.T) {
	project, branch := GitInfo(t.TempDir())
	if project != "" || branch != "" {
		t.Errorf("outside a repo both axes must be empty, got %q %q", project, branch)
	}
}

// A transcript that records only what a tool returned cannot answer which tool
// was called. Measuring ghosttree's own adoption had to fall back on grepping
// for the string "context_search", which counts a session discussing the tool
// the same as one using it.
func TestParseClaudeKeepsToolCalls(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[
		{"type":"text","text":"Checking what is known here."},
		{"type":"tool_use","name":"mcp__ghosttree__context_search","input":{"query":"ufw drops lan"}}]}}`)
	p := ParseClaudeLine(line)
	if p.Role != "assistant" {
		t.Fatalf("role = %q", p.Role)
	}
	if !strings.Contains(p.Text, "Checking what is known here.") {
		t.Errorf("assistant prose lost: %q", p.Text)
	}
	if !strings.Contains(p.Text, "mcp__ghosttree__context_search") {
		t.Errorf("tool name missing: %q", p.Text)
	}
	if !strings.Contains(p.Text, "ufw drops lan") {
		t.Errorf("tool arguments missing: %q", p.Text)
	}
}

// A call with no prose around it still has to be recorded.
func TestParseClaudeKeepsToolCallWithoutText(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[
		{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`)
	p := ParseClaudeLine(line)
	if p.Role != "assistant" || !strings.Contains(p.Text, "Bash") {
		t.Errorf("bare tool call: %+v", p)
	}
}

// A Write call carries a whole file. The name is the signal; the payload is
// already in the repository.
func TestParseClaudeTruncatesHugeToolArguments(t *testing.T) {
	huge := strings.Repeat("x", 20000)
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[
		{"type":"tool_use","name":"Write","input":{"content":"` + huge + `"}}]}}`)
	p := ParseClaudeLine(line)
	if !strings.Contains(p.Text, "Write") {
		t.Fatalf("tool name lost: %.120q", p.Text)
	}
	if len(p.Text) > 2000 {
		t.Errorf("tool arguments not truncated: %d bytes", len(p.Text))
	}
}

// A tool result still parses as a tool line; adding calls must not change that.
func TestParseClaudeStillSeparatesToolResults(t *testing.T) {
	line := []byte(`{"type":"user","message":{"role":"user","content":[
		{"type":"tool_result","content":[{"type":"text","text":"PASS"}]}]}}`)
	if p := ParseClaudeLine(line); p.Role != "tool" || !strings.Contains(p.Text, "PASS") {
		t.Errorf("tool result: %+v", p)
	}
}

// Codex records the call as its own line, so the same blind spot exists there.
func TestParseCodexKeepsToolCalls(t *testing.T) {
	line := []byte(`{"type":"response_item","payload":{"type":"function_call","name":"exec_command",
		"arguments":"{\"cmd\":\"go test ./...\",\"workdir\":\"/home/user\"}"}}`)
	p := ParseCodexLine(line)
	if p.Role != "tool" {
		t.Fatalf("role = %q, want tool", p.Role)
	}
	if !strings.Contains(p.Text, "exec_command") || !strings.Contains(p.Text, "go test ./...") {
		t.Errorf("call not archived: %q", p.Text)
	}
}

func TestParseCodexKeepsCustomToolCalls(t *testing.T) {
	line := []byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"context_search","input":"ufw"}}`)
	if p := ParseCodexLine(line); !strings.Contains(p.Text, "context_search") {
		t.Errorf("custom call not archived: %+v", p)
	}
}

// Tool arguments are the one place a credential reaches a transcript as an
// argument rather than as prose — an export command, a curl header. They go
// through the same redaction as everything else because the collector redacts
// ParsedLine.Text, but archiving a new kind of content is exactly when that is
// worth proving rather than assuming.
func TestToolCallArgumentsAreRedacted(t *testing.T) {
	secret := "sk-ant-" + strings.Repeat("A", 40)
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[
		{"type":"tool_use","name":"Bash","input":{"command":"export ANTHROPIC_API_KEY=` + secret + `"}}]}}`)
	got := redact.Redact(ParseClaudeLine(line).Text)
	if strings.Contains(got, secret) {
		t.Fatalf("credential survived into the archive: %q", got)
	}
	if !strings.Contains(got, "Bash") {
		t.Errorf("redaction must not cost the tool name: %q", got)
	}
}
