package collector

import (
	"bufio"
	"os"
	"strings"
	"testing"
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
