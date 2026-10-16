package garden

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests run the real claude binary with the real command line of a
// session (ClaudeConfig.Args) against a fake model API on loopback. The fake
// "model" asks for exactly one tool call per run, and the permission matcher of
// Claude Code, not a copy of it in this repository, decides. They are skipped
// when no claude binary is installed (or with -short).

type fakeAPI struct {
	mu     sync.Mutex
	call   map[string]any // the tool call to make in the next run
	name   string
	result *toolOutcome
	log    []string
	srv    *httptest.Server
}

type toolOutcome struct {
	IsError bool
	Text    string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func sse(w http.ResponseWriter, events ...[2]string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e[0], e[1])
	}
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }

func msgStart(id string) [2]string {
	return [2]string{"message_start", jsonStr(map[string]any{"type": "message_start", "message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": "claude-sonnet-5-5", "content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 3, "output_tokens": 1}}})}
}

func msgEnd(stop string) [][2]string {
	return [][2]string{
		{"message_delta", jsonStr(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 4}})},
		{"message_stop", `{"type":"message_stop"}`},
	}
}

func (f *fakeAPI) text(w http.ResponseWriter, id, text string) {
	ev := [][2]string{msgStart(id),
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", jsonStr(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`}}
	sse(w, append(ev, msgEnd("end_turn")...)...)
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.log = append(f.log, r.Method+" "+r.URL.Path)
	if os.Getenv("GT_DUMP") != "" {
		os.WriteFile(fmt.Sprintf("/tmp/gt491/req%d.json", len(f.log)), body, 0o644)
	}
	f.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/v1/messages") {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
		return
	}
	var req struct {
		Stream   bool              `json:"stream"`
		Tools    []json.RawMessage `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	if strings.HasSuffix(r.URL.Path, "count_tokens") {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"input_tokens":10}`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// a tool result anywhere in the conversation: remember what the matcher decided
	if out, ok := lastToolResult(body); ok {
		f.result = &out
		f.text(w, "msg_done", "fertig")
		return
	}
	if len(req.Tools) == 0 || f.call == nil {
		f.text(w, "msg_side", "ok")
		return
	}
	call := f.call
	f.call = nil
	ev := [][2]string{msgStart("msg_tool"),
		{"content_block_start", jsonStr(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_probe", "name": f.name, "input": map[string]any{}}})},
		{"content_block_delta", jsonStr(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": jsonStr(call)}})},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`}}
	sse(w, append(ev, msgEnd("tool_use")...)...)
}

func resultText(b streamBlock) string {
	var parts []streamBlock
	if json.Unmarshal(b.Content, &parts) == nil {
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(p.Text)
		}
		return sb.String()
	}
	var s string
	_ = json.Unmarshal(b.Content, &s)
	return s
}

// lastToolResult finds the last tool_result of a request.
func lastToolResult(body []byte) (toolOutcome, bool) {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return toolOutcome{}, false
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		var cs []struct {
			Type    string          `json:"type"`
			IsError bool            `json:"is_error"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(req.Messages[i].Content, &cs) != nil {
			continue
		}
		for _, c := range cs {
			if c.Type == "tool_result" {
				return toolOutcome{IsError: c.IsError, Text: resultText(streamBlock{Content: c.Content})}, true
			}
		}
	}
	return toolOutcome{}, false
}

type matcherEnv struct {
	t        *testing.T
	model    *fakeAPI
	claude   string
	checkout string
	outside  string
	cfgRoot  string
}

func newMatcherEnv(t *testing.T) *matcherEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("short")
	}
	bin, err := FindClaude("")
	if err != nil {
		t.Skip("claude is not installed")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	e := &matcherEnv{t: t, model: newFakeAPI(t), claude: bin,
		checkout: filepath.Join(base, "checkout"), outside: filepath.Join(base, "outside"), cfgRoot: filepath.Join(base, "cfg")}
	writeFile(t, filepath.Join(e.checkout, "innen.txt"), "SENTINEL-IN\n")
	writeFile(t, filepath.Join(e.checkout, "sub", "tief.txt"), "SENTINEL-DEEP\n")
	writeFile(t, filepath.Join(e.outside, "aussen.txt"), "SENTINEL-OUT\n")
	if err := os.Symlink(e.outside, filepath.Join(e.checkout, "link")); err != nil {
		t.Fatal(err)
	}
	return e
}

// run lets the fake model make one tool call in a real session and returns what
// the permission matcher made of it.
func (e *matcherEnv) run(tool string, input map[string]any) (toolOutcome, SessionResult) {
	e.t.Helper()
	e.model.mu.Lock()
	e.model.name, e.model.call, e.model.result = tool, input, nil
	e.model.mu.Unlock()
	r := ClaudeRunner{Config: ClaudeConfig{Bin: e.claude, Model: "claude-sonnet-5-5", RepoRoot: e.checkout, ConfigRoot: e.cfgRoot,
		DenyRead: []string{e.cfgRoot}, BaseURL: e.model.srv.URL, Token: "test", ParentEnv: os.Environ(), Timeout: 90 * time.Second}}
	res, err := r.RunSession(context.Background(), SessionSpec{Prompt: "los", MaxTurns: 4})
	if err != nil {
		e.t.Fatalf("%s %v: %v (%+v)", tool, input, err, res)
	}
	e.model.mu.Lock()
	defer e.model.mu.Unlock()
	if e.model.result == nil {
		e.t.Fatalf("%s %v: the fake model got no tool result; session said %q (%+v), requests: %v", tool, input, res.Text, res, e.model.log)
	}
	return *e.model.result, res
}

func TestRealMatcherHoldsReadGrepGlobToTheCheckout(t *testing.T) {
	e := newMatcherEnv(t)
	type probe struct {
		name    string
		tool    string
		input   map[string]any
		want    string // must be in the result
		notWant string // must not be in the result
	}
	probes := []probe{
		{"Read im Checkout", "Read", map[string]any{"file_path": filepath.Join(e.checkout, "innen.txt")}, "SENTINEL-IN", ""},
		{"Grep im Checkout", "Grep", map[string]any{"pattern": "SENTINEL", "output_mode": "content"}, "SENTINEL-IN", "SENTINEL-OUT"},
		{"Grep mit Pfad im Checkout", "Grep", map[string]any{"pattern": "SENTINEL", "path": filepath.Join(e.checkout, "sub"), "output_mode": "content"}, "SENTINEL-DEEP", "SENTINEL-OUT"},
		{"Glob im Checkout", "Glob", map[string]any{"pattern": "**/*.txt"}, "tief.txt", "aussen.txt"},
		{"Read außerhalb", "Read", map[string]any{"file_path": filepath.Join(e.outside, "aussen.txt")}, "", "SENTINEL-OUT"},
		{"Grep außerhalb", "Grep", map[string]any{"pattern": "SENTINEL", "path": e.outside, "output_mode": "content"}, "", "SENTINEL-OUT"},
		{"Grep außerhalb, nur Dateinamen", "Grep", map[string]any{"pattern": "SENTINEL", "path": e.outside}, "", "aussen.txt"},
		{"Glob außerhalb über path", "Glob", map[string]any{"pattern": "*", "path": e.outside}, "", "aussen.txt"},
		{"Glob außerhalb über absolutes Muster", "Glob", map[string]any{"pattern": e.outside + "/*"}, "", "aussen.txt"},
		{"Glob außerhalb über ..", "Glob", map[string]any{"pattern": "../outside/*"}, "", "aussen.txt"},
		{"Read durch Symlink hinaus", "Read", map[string]any{"file_path": filepath.Join(e.checkout, "link", "aussen.txt")}, "", "SENTINEL-OUT"},
		{"Grep durch Symlink hinaus", "Grep", map[string]any{"pattern": "SENTINEL", "path": filepath.Join(e.checkout, "link"), "output_mode": "content"}, "", "SENTINEL-OUT"},
		{"Grep /etc", "Grep", map[string]any{"pattern": "root", "path": "/etc/passwd", "output_mode": "content"}, "", "root:"},
		{"Read /etc/passwd", "Read", map[string]any{"file_path": "/etc/passwd"}, "", "root:"},
	}
	for _, p := range probes {
		t.Run(p.name, func(t *testing.T) {
			out, res := e.run(p.tool, p.input)
			if p.want != "" && !strings.Contains(out.Text, p.want) {
				t.Errorf("expected %q, got (error=%v): %s", p.want, out.IsError, out.Text)
			}
			if p.notWant != "" && strings.Contains(out.Text, p.notWant) {
				t.Errorf("the session saw %q (error=%v): %s", p.notWant, out.IsError, out.Text)
			}
			if p.want == "" && res.Denials == 0 && !out.IsError {
				t.Logf("not refused as such, but nothing leaked: %s", out.Text)
			}
		})
	}
}

// Anything that would run a program or write is not there at all.
func TestRealMatcherHasNoBashAndNoWriting(t *testing.T) {
	e := newMatcherEnv(t)
	marker := filepath.Join(e.outside, "marker")
	for _, p := range []struct {
		tool  string
		input map[string]any
	}{
		{"Bash", map[string]any{"command": "touch " + marker}},
		{"Bash", map[string]any{"command": "git log --output=" + marker}},
		{"Write", map[string]any{"file_path": marker, "content": "x"}},
		{"Write", map[string]any{"file_path": filepath.Join(e.checkout, "neu.txt"), "content": "x"}},
		{"Edit", map[string]any{"file_path": filepath.Join(e.checkout, "innen.txt"), "old_string": "IN", "new_string": "XX"}},
	} {
		out, _ := e.run(p.tool, p.input)
		if !out.IsError {
			t.Errorf("%s %v was not refused: %s", p.tool, p.input, out.Text)
		}
	}
	for _, f := range []string{marker, filepath.Join(e.checkout, "neu.txt")} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s was written", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(e.checkout, "innen.txt")); string(b) != "SENTINEL-IN\n" {
		t.Errorf("innen.txt changed: %q", b)
	}
}
