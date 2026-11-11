package collector

import (
	"bufio"
	"os"
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

func TestClaudeSessionMeta(t *testing.T) {
	ls := lines(t, "testdata/claude.jsonl")
	id, cwd, branch := ClaudeSessionMeta("/home/x/.claude/projects/-home-main/9708008b.jsonl", ls)
	if id != "9708008b" || cwd != "/home/user/Projects/demo" || branch != "main" {
		t.Errorf("meta: %q %q %q", id, cwd, branch)
	}
}

func TestCodexSessionMeta(t *testing.T) {
	ls := lines(t, "testdata/codex.jsonl")
	id, cwd, _, _ := CodexSessionMeta(ls)
	if id != "019f6d31-504b-7b50-b171-8d89cc3e9c49" || cwd != "/home/user/Projects/ExampleProject" {
		t.Errorf("meta: %q %q", id, cwd)
	}
}

func TestCodexSessionMetaPrefersEmbeddedGitContext(t *testing.T) {
	line := []byte(`{"type":"session_meta","payload":{"session_id":"s","cwd":"/missing","git":{"repository_url":"https://GitHub.com/Deadweight-Labs/Ghosttree.git","branch":"main"}}}`)
	id, cwd, project, branch := CodexSessionMeta([][]byte{line})
	if id != "s" || cwd != "/missing" || project != "github.com/deadweight-labs/ghosttree" || branch != "main" {
		t.Fatalf("meta=%q %q %q %q", id, cwd, project, branch)
	}
}

func TestGitInfoOutsideRepo(t *testing.T) {
	project, branch := GitInfo(t.TempDir())
	if project != "" || branch != "" {
		t.Errorf("outside a repo both axes must be empty, got %q %q", project, branch)
	}
}
