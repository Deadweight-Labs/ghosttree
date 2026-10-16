package garden

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
)

// SessionResult is what a Claude Code session reported: its closing text, the
// provider's own token counts (cache included), and how many turns it took.
type SessionResult struct {
	Text        string
	Usage       llm.Usage
	Turns       int
	ReportedUSD float64
	Subtype     string
	IsError     bool
	// Denials counts tool calls the permission rules refused. A session that
	// keeps running into them is working with a wrong allowlist.
	Denials int
	// Tools counts the tool calls the session made, by name.
	Tools map[string]int
}

type streamUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u streamUsage) llm() llm.Usage {
	return llm.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadInputTokens, CacheWriteTokens: u.CacheCreationInputTokens}
}

type streamEvent struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	IsError           bool            `json:"is_error"`
	Result            string          `json:"result"`
	NumTurns          int             `json:"num_turns"`
	TotalCostUSD      float64         `json:"total_cost_usd"`
	Usage             *streamUsage    `json:"usage"`
	PermissionDenials json.RawMessage `json:"permission_denials"`
	Message           *struct {
		ID      string       `json:"id"`
		Usage   *streamUsage `json:"usage"`
		Content []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
}

// ParseStream reads Claude Code's stream-json output. The closing "result"
// event carries the final text and the totals; if the process died before it,
// the usage is summed from the assistant messages seen (once per message id), so
// a killed session still counts what it used.
func ParseStream(r io.Reader) (SessionResult, error) {
	var res SessionResult
	var sum llm.Usage
	seen := map[string]bool{}
	seenTool := map[string]bool{}
	gotResult := false
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			var ev streamEvent
			if json.Unmarshal(t, &ev) == nil {
				switch ev.Type {
				case "assistant":
					if ev.Message != nil {
						for _, c := range ev.Message.Content {
							if c.Type == "tool_use" && c.ID != "" && !seenTool[c.ID] {
								seenTool[c.ID] = true
								if res.Tools == nil {
									res.Tools = map[string]int{}
								}
								res.Tools[c.Name]++
							}
						}
					}
					if ev.Message != nil && ev.Message.Usage != nil && ev.Message.ID != "" && !seen[ev.Message.ID] {
						seen[ev.Message.ID] = true
						u := ev.Message.Usage.llm()
						sum.InputTokens += u.InputTokens
						sum.OutputTokens += u.OutputTokens
						sum.CacheReadTokens += u.CacheReadTokens
						sum.CacheWriteTokens += u.CacheWriteTokens
						res.Turns++
					}
				case "result":
					gotResult = true
					res.Text, res.Subtype, res.IsError, res.ReportedUSD = ev.Result, ev.Subtype, ev.IsError, ev.TotalCostUSD
					if ev.NumTurns > 0 {
						res.Turns = ev.NumTurns
					}
					if ev.Usage != nil {
						res.Usage = ev.Usage.llm()
					}
					var den []json.RawMessage
					if json.Unmarshal(ev.PermissionDenials, &den) == nil {
						res.Denials = len(den)
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				return res, err
			}
			break
		}
	}
	if !gotResult {
		res.Usage = sum
		return res, fmt.Errorf("die Session endete ohne Ergebnis")
	}
	if res.Usage == (llm.Usage{}) {
		res.Usage = sum
	}
	if res.IsError || res.Subtype != "success" {
		return res, fmt.Errorf("die Session endete mit %s", firstNonEmpty(res.Subtype, "Fehler"))
	}
	return res, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// ClaudeConfig is how a session is started. Everything that decides what the
// session may do is here, and none of it comes from the checkout it reads.
type ClaudeConfig struct {
	Bin       string
	Model     string
	RepoRoot  string
	ConfigDir string
	// CtxBin is the ctx binary that serves the ghosttree MCP tools; empty
	// means the session gets no MCP server at all.
	CtxBin string
	// DenyRead lists absolute paths (files or directories) the session may not read.
	DenyRead []string
	Timeout  time.Duration
	// BaseURL and Token, when set, point the session at a proxy or API. Left
	// empty the settings.json of the config directory decides.
	BaseURL, Token string
	// APIKey is a real API key for api.anthropic.com.
	APIKey string
	// ParentEnv is the environment to take pass-through variables from.
	ParentEnv []string
}

// Tools the session may use. Nothing in here writes: Edit, Write and friends are
// not even in the tool set, and are denied again below.
var (
	agentBuiltinTools = []string{"Read", "Grep", "Glob", "Bash"}
	agentAllowed      = []string{
		"Read", "Grep", "Glob",
		"Bash(git log:*)", "Bash(git show:*)", "Bash(git blame:*)",
		"mcp__ghosttree__context_search", "mcp__ghosttree__context_get",
	}
	agentDenied = []string{
		"Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Task", "Agent",
		// git writes a file with --output; the allowlist matches by prefix.
		"Bash(git log:*--output*)", "Bash(git show:*--output*)", "Bash(git blame:*--output*)",
		"mcp__ghosttree__context_remember", "mcp__ghosttree__context_retire", "mcp__ghosttree__context_supersede",
		"mcp__ghosttree__context_move", "mcp__ghosttree__context_describe_file",
	}
)

// Args is the claude command line of one session. The prompt goes in on stdin.
func (c ClaudeConfig) Args(s SessionSpec) ([]string, error) {
	denied := append([]string{}, agentDenied...)
	for _, p := range c.DenyRead {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("deny-read path %q is not absolute", p)
		}
		denied = append(denied, "Read(/"+p+"/**)", "Read(/"+p+")", "Grep(/"+p+"/**)")
	}
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "dontAsk",
		"--tools", strings.Join(agentBuiltinTools, ","),
		"--allowedTools", strings.Join(agentAllowed, ","),
		"--disallowedTools", strings.Join(denied, ","),
		"--setting-sources", "user",
		"--no-session-persistence",
		"--append-system-prompt", AgentSystemPrompt(),
		"--max-turns", fmt.Sprint(s.MaxTurns),
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if s.BudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", s.BudgetUSD))
	}
	cfg := `{"mcpServers":{}}`
	if c.CtxBin != "" {
		b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"ghosttree": map[string]any{"command": c.CtxBin, "args": []string{"mcp"}}}})
		cfg = string(b)
	}
	args = append(args, "--mcp-config", cfg, "--strict-mcp-config")
	return args, nil
}

// passEnv are the variables a session inherits: where things are, not what is
// secret. The garden key, the database path and the like stay behind.
var passEnv = map[string]bool{
	"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true, "TMPDIR": true, "USER": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_NOSYSTEM": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "no_proxy": true,
}

// Environ builds the session's environment from scratch.
func (c ClaudeConfig) Environ() []string {
	var env []string
	for _, kv := range c.ParentEnv {
		k, _, _ := strings.Cut(kv, "=")
		if passEnv[k] || strings.HasPrefix(k, "XDG_") {
			env = append(env, kv)
		}
	}
	add := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	add("CLAUDE_CONFIG_DIR", c.ConfigDir)
	if c.ConfigDir != "" {
		add("XDG_CACHE_HOME", filepath.Join(c.ConfigDir, "cache"))
	}
	add("ANTHROPIC_BASE_URL", c.BaseURL)
	add("ANTHROPIC_AUTH_TOKEN", c.Token)
	add("ANTHROPIC_API_KEY", c.APIKey)
	// Nothing but the model traffic: no telemetry, no update check, no feedback.
	env = append(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1")
	return env
}

// ClaudeRunner runs sessions with the claude binary.
type ClaudeRunner struct{ Config ClaudeConfig }

func (r ClaudeRunner) RunSession(ctx context.Context, s SessionSpec) (SessionResult, error) {
	c := r.Config
	args, err := c.Args(s)
	if err != nil {
		return SessionResult{}, err
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = c.RepoRoot
	cmd.Env = c.Environ()
	cmd.Stdin = strings.NewReader(s.Prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	var stderr tail
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return SessionResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return SessionResult{}, fmt.Errorf("start %s: %w", c.Bin, err)
	}
	res, perr := ParseStream(stdout)
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return res, fmt.Errorf("Session abgebrochen: %v", ctx.Err())
	}
	if perr != nil {
		if t := strings.TrimSpace(stderr.String()); t != "" {
			perr = fmt.Errorf("%w (stderr: %s)", perr, t)
		}
		return res, perr
	}
	if werr != nil {
		return res, fmt.Errorf("claude beendet mit Fehler: %w", werr)
	}
	return res, nil
}

// tail keeps the last 2 KiB written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.b) }

// FindClaude resolves the claude binary: an explicit path, then PATH, then the
// native installer's location in the home directory.
func FindClaude(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "claude")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("claude not found: install Claude Code or pass --claude <path>")
}
