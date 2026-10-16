package garden

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectGitServer(t *testing.T, repo *Repo) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := NewGitServer(repo, "test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callText(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		// a refusal by the schema is as good as one by the tool
		return err.Error(), true
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

// The session sees exactly two tools, both read-only, and they refuse what the
// argument checks refuse, over the real protocol.
func TestGitServerOffersTwoToolsAndRefusesForbiddenArguments(t *testing.T) {
	repo := newRepo(t)
	cs := connectGitServer(t, repo)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, "mcp__"+GitServerName+"__"+tl.Name)
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tl.Name)
		}
	}
	if len(names) != len(GitToolNames) {
		t.Fatalf("tools %v, want %v", names, GitToolNames)
	}
	for _, n := range GitToolNames {
		if !contains(names, n) {
			t.Errorf("missing %s in %v", n, names)
		}
	}

	out, isErr := callText(t, cs, "git_log", map[string]any{"path": "internal/distill.go"})
	if isErr || !strings.Contains(out, "add distill") {
		t.Fatalf("log: %q %v", out, isErr)
	}
	hash := strings.Fields(out)[0]
	if out, isErr := callText(t, cs, "git_show", map[string]any{"commit": hash}); isErr || !strings.Contains(out, "SessionDistillationExists") {
		t.Fatalf("show: %q %v", out, isErr)
	}

	target := filepath.Join(t.TempDir(), "geschrieben")
	refused := []struct {
		tool string
		args map[string]any
	}{
		{"git_show", map[string]any{"commit": "--output=" + target}},
		{"git_show", map[string]any{"commit": "--ou=" + target}},
		{"git_show", map[string]any{"commit": "-c"}},
		{"git_show", map[string]any{"commit": "-C"}},
		{"git_show", map[string]any{"commit": "--exec-path=/tmp"}},
		{"git_show", map[string]any{"commit": "HEAD", "path": "/etc/passwd"}},
		{"git_show", map[string]any{"commit": "HEAD", "path": "../../etc/passwd"}},
		{"git_show", map[string]any{"commit": "HEAD", "path": ".git/config"}},
		{"git_log", map[string]any{"path": "--output=" + target}},
		{"git_log", map[string]any{"path": "/etc"}},
		{"git_log", map[string]any{"max": 100000}},
		// arguments the tools do not have are not passed on
		{"git_log", map[string]any{"args": []string{"-c", "core.pager=sh"}}},
		{"git_show", map[string]any{"commit": "HEAD", "options": "--output=" + target}},
	}
	for _, r := range refused {
		out, isErr := callText(t, cs, r.tool, r.args)
		if !isErr {
			t.Errorf("%s %v was not refused: %s", r.tool, r.args, out)
		}
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("a tool call wrote a file")
	}
}
