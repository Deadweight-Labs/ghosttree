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
	// DeniedCalls names what was refused (tool and the start of its input).
	DeniedCalls []string
	// Tools counts the tool calls the session made, by name.
	Tools map[string]int
	// Looked is what the session looked at. Nil for a runner that does not track it.
	Looked *Looked
}

// Looked is what a session actually looked at: the files it read and everything
// its tools printed. A citation only counts if the session got to see it.
type Looked struct {
	Read map[string]bool
	Out  strings.Builder
}

const maxSeenOut = 16 << 20

// Has says whether a citation (a "path:line" or a commit hash) turned up in
// the session: a file it read or that a tool listed, a hash a tool printed.
func (s *Looked) Has(ref string) bool {
	if s == nil {
		return true
	}
	ref = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "commit "))
	if m := lineRef.FindStringSubmatch(ref); m != nil {
		f := strings.TrimPrefix(m[1], "./")
		for p := range s.Read {
			if p == f || strings.HasSuffix(p, "/"+f) {
				return true
			}
		}
		return strings.Contains(s.Out.String(), f)
	}
	if len(ref) >= 8 {
		return strings.Contains(strings.ToLower(s.Out.String()), strings.ToLower(ref[:8]))
	}
	return false
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

type streamBlock struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Text    string          `json:"text"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// blocks reads a message's content, which is a string or a list of blocks.
func blocks(raw json.RawMessage) []streamBlock {
	var out []streamBlock
	if json.Unmarshal(raw, &out) == nil {
		return out
	}
	var str string
	if json.Unmarshal(raw, &str) == nil && str != "" {
		return []streamBlock{{Type: "text", Text: str}}
	}
	return nil
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
		ID      string          `json:"id"`
		Usage   *streamUsage    `json:"usage"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// ParseStream reads Claude Code's stream-json output. The closing "result"
// event carries the final text and the totals; if the process died before it,
// the usage is summed from the assistant messages seen (once per message id), so
// a killed session still counts what it used.
func ParseStream(r io.Reader) (SessionResult, error) {
	var res SessionResult
	res.Looked = &Looked{Read: map[string]bool{}}
	var sum llm.Usage
	var lastText string
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
				case "user":
					if ev.Message != nil {
						for _, c := range blocks(ev.Message.Content) {
							if c.Type != "tool_result" {
								continue
							}
							for _, p := range blocks(c.Content) {
								if res.Looked.Out.Len() < maxSeenOut {
									res.Looked.Out.WriteString(p.Text)
									res.Looked.Out.WriteByte('\n')
								}
							}
							var str string
							if json.Unmarshal(c.Content, &str) == nil && res.Looked.Out.Len() < maxSeenOut {
								res.Looked.Out.WriteString(str)
								res.Looked.Out.WriteByte('\n')
							}
						}
					}
				case "assistant":
					if ev.Message != nil {
						for _, c := range blocks(ev.Message.Content) {
							if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
								lastText = c.Text
							}
							if c.Type == "tool_use" && c.ID != "" && !seenTool[c.ID] {
								seenTool[c.ID] = true
								if res.Tools == nil {
									res.Tools = map[string]int{}
								}
								res.Tools[c.Name]++
								var in struct {
									FilePath string `json:"file_path"`
								}
								if json.Unmarshal(c.Input, &in) == nil && in.FilePath != "" {
									res.Looked.Read[strings.TrimPrefix(in.FilePath, "./")] = true
								}
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
					var den []struct {
						Tool  string          `json:"tool_name"`
						Input json.RawMessage `json:"tool_input"`
					}
					if json.Unmarshal(ev.PermissionDenials, &den) == nil {
						res.Denials = len(den)
						for _, d := range den {
							in := strings.Join(strings.Fields(string(d.Input)), " ")
							if len(in) > 100 {
								in = in[:100]
							}
							res.DeniedCalls = append(res.DeniedCalls, d.Tool+" "+in)
						}
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
		// A session cut off by a limit still said things on the way; what it
		// wrote last may already be the answer, so it is handed on.
		if res.Text == "" {
			res.Text = lastText
		}
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
	Bin      string
	Model    string
	RepoRoot string
	// ConfigRoot is the directory under which every session gets its own, empty
	// CLAUDE_CONFIG_DIR (and HOME). It must be set: a session that fell back to
	// the settings of whoever runs it could widen its own rights.
	ConfigRoot string
	// CtxBin is the ctx binary that serves the ghosttree MCP tools and the git
	// tool; empty means the session gets neither.
	CtxBin string
	// DenyRead lists absolute paths (files or directories) the session may not read.
	DenyRead []string
	Timeout  time.Duration
	// BaseURL and Token point the session at a proxy or API; Token is the one
	// value that assigns the session to its account.
	BaseURL, Token string
	// APIKey is a real API key for api.anthropic.com.
	APIKey string
	// ParentEnv is the environment to take the few pass-through variables from.
	ParentEnv []string
}

// ErrNoConfigRoot is returned when a session is started without a config root.
var ErrNoConfigRoot = fmt.Errorf("kein Konfigurationsverzeichnis für die Sessions (--claude-dir): der Agent-Modus startet nicht ohne ein eigenes, leeres")

// systemDeny are places a session never reads, wherever the checkout is (unless
// it lies inside one, see Args). Read is only allowed inside the checkout anyway.
var systemDeny = []string{"/proc", "/sys", "/etc", "/run", "/root", "/dev"}

// Tools the session may use. Nothing in here writes or runs a program: there
// is no Bash, and git is reached through a fixed MCP tool (gittool.go).
var (
	agentBuiltinTools = []string{"Read", "Grep", "Glob"}
	agentDenied       = []string{
		"Bash", "PowerShell", "Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Task", "Agent",
		"mcp__ghosttree__context_remember", "mcp__ghosttree__context_retire", "mcp__ghosttree__context_supersede",
		"mcp__ghosttree__context_move", "mcp__ghosttree__context_describe_file",
	}
)

// AgentAllowed is the allowlist of a session for a checkout. Grep and Glob are
// not allowed by name: a bare "Grep" would let them search anywhere. They are
// read-only tools, run in the checkout (the session's working directory), and
// Claude Code applies the Read rules to the directory they search, so they
// reach what Read(//checkout/**) allows and nothing else.
func AgentAllowed(repoRoot string, withMCP bool) []string {
	allowed := []string{"Read(/" + repoRoot + "/**)"}
	allowed = append(allowed, GitToolNames...)
	if withMCP {
		allowed = append(allowed, "mcp__ghosttree__context_search", "mcp__ghosttree__context_get")
	}
	return allowed
}

// Args is the claude command line of one session. The prompt goes in on stdin.
func (c ClaudeConfig) Args(s SessionSpec) ([]string, error) {
	denied := append([]string{}, agentDenied...)
	if c.RepoRoot == "" || !filepath.IsAbs(c.RepoRoot) {
		return nil, fmt.Errorf("repo root %q is not absolute", c.RepoRoot)
	}
	allowed := AgentAllowed(c.RepoRoot, c.CtxBin != "")
	deny := append([]string{}, c.DenyRead...)
	for _, d := range systemDeny {
		if c.RepoRoot != d && !strings.HasPrefix(c.RepoRoot, d+"/") {
			deny = append(deny, d)
		}
	}
	for _, p := range deny {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("deny-read path %q is not absolute", p)
		}
		denied = append(denied, "Read(/"+p+"/**)", "Read(/"+p+")")
	}
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "dontAsk",
		"--tools", strings.Join(agentBuiltinTools, ","),
		"--allowedTools", strings.Join(allowed, ","),
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
	servers := map[string]any{}
	if c.CtxBin != "" {
		// The MCP servers get what they need by their own env entry, so the
		// session's own environment can stay small.
		env := map[string]string{}
		for _, kv := range c.ParentEnv {
			k, v, _ := strings.Cut(kv, "=")
			if mcpEnv[k] {
				env[k] = v
			}
		}
		servers["ghosttree"] = map[string]any{"command": c.CtxBin, "args": []string{"mcp"}, "env": env}
		servers[GitServerName] = map[string]any{"command": c.CtxBin, "args": []string{"garden", "git-mcp", "--repo", c.RepoRoot}, "env": map[string]string{"PATH": envOf(c.ParentEnv, "PATH")}}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": servers})
	args = append(args, "--mcp-config", string(b), "--strict-mcp-config")
	return args, nil
}

func envOf(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// mcpEnv is what the ghosttree MCP server needs to find its client config.
var mcpEnv = map[string]bool{"HOME": true, "XDG_CONFIG_HOME": true, "PATH": true}

// passEnv are the only variables a session inherits from its parent: where
// programs and certificates are, not what is secret. Everything else, the
// garden key, the database path, proxy URLs with credentials, GIT_* and the
// rest, stays behind. HOME is not among them; it is the run's own directory.
var passEnv = map[string]bool{
	"PATH": true, "LANG": true, "LC_ALL": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
}

// Environ builds the session's environment from scratch. runDir is the empty
// directory of this run.
func (c ClaudeConfig) Environ(runDir string) []string {
	var env []string
	for _, kv := range c.ParentEnv {
		k, _, _ := strings.Cut(kv, "=")
		if passEnv[k] {
			env = append(env, kv)
		}
	}
	add := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	add("CLAUDE_CONFIG_DIR", runDir)
	add("HOME", runDir)
	add("XDG_CACHE_HOME", filepath.Join(runDir, "cache"))
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
	if c.ConfigRoot == "" {
		return SessionResult{}, ErrNoConfigRoot
	}
	if !filepath.IsAbs(c.ConfigRoot) {
		return SessionResult{}, fmt.Errorf("config root %q is not absolute", c.ConfigRoot)
	}
	args, err := c.Args(s)
	if err != nil {
		return SessionResult{}, err
	}
	if err := os.MkdirAll(c.ConfigRoot, 0o700); err != nil {
		return SessionResult{}, err
	}
	// A directory of its own, empty at the start and gone at the end: nothing a
	// session leaves behind (settings, hooks, memory) reaches the next one.
	runDir, err := os.MkdirTemp(c.ConfigRoot, "run-")
	if err != nil {
		return SessionResult{}, err
	}
	defer os.RemoveAll(runDir)
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = c.RepoRoot
	cmd.Env = c.Environ(runDir)
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
