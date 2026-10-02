// Package transcript turns the stored JSONL lines of an agent session into one
// display model. Claude Code and Codex write different shapes; both end up as
// the same blocks (message, prompt, thinking, tool call with input and result,
// diff), and the search index is built from the same blocks, so what can be
// found is what can be read.
//
// Parsers never fail: a line that is not understood yields no block, and the
// store keeps the raw line anyway.
package transcript

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxIndexBytes caps each indexed field per chunk. The index is for finding,
// not for reading: the full text stays in the raw line.
const MaxIndexBytes = 8 * 1024

type Kind string

const (
	KindUser      Kind = "user"
	KindAssistant Kind = "assistant"
	KindThinking  Kind = "thinking"
	// KindTool is a tool call; Pair fills in its result.
	KindTool Kind = "tool"
	// KindResult is a tool result that has not been paired with its call.
	KindResult Kind = "result"
)

// DiffLine.Op is '+', '-', ' ' or '@' (a hunk header).
type DiffLine struct {
	Op   byte
	Text string
}

type DiffFile struct {
	Path  string
	Lines []DiffLine
}

type Block struct {
	Seq  int
	Kind Kind
	Time string
	// Text is a message or thinking text.
	Text string
	// Tool, Input and Detail describe a call. Input is the one-line form
	// (command, path, pattern); Detail is the remaining input, if any.
	Tool   string
	Input  string
	Detail string
	Diff   []DiffFile
	CallID string
	// Output is the tool result; HasOutput tells an empty result from none.
	Output    string
	HasOutput bool
	Failed    bool
	// Status is "exit 1" or "error" for a failed result, "" otherwise.
	Status    string
	ResultSeq int
}

// Parsed is what one stored line contributes.
type Parsed struct {
	Blocks []Block
	// Title is set by a Claude ai-title line.
	Title string
}

// Parse reads one stored JSONL line of the given harness (claude-code|codex).
func Parse(harness string, seq int, raw string) Parsed {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return Parsed{}
	}
	switch harness {
	case "claude-code":
		return parseClaude(seq, raw)
	case "codex":
		return parseCodex(seq, raw)
	}
	return Parsed{}
}

// Pair merges each result into the call with the same id. A result whose call
// is not among the blocks (it lies outside the loaded window) stays visible as
// its own block.
func Pair(blocks []Block) []Block {
	out := make([]Block, 0, len(blocks))
	open := map[string]int{}
	for _, b := range blocks {
		switch b.Kind {
		case KindTool:
			out = append(out, b)
			if b.CallID != "" {
				open[b.CallID] = len(out) - 1
			}
			continue
		case KindResult:
			if i, ok := open[b.CallID]; ok && b.CallID != "" && !out[i].HasOutput {
				out[i].Output, out[i].HasOutput = b.Output, true
				out[i].Failed, out[i].Status = b.Failed, b.Status
				out[i].ResultSeq = b.Seq
				delete(open, b.CallID)
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// IndexFields is what the search index stores for one chunk.
type IndexFields struct {
	// Role is "user" or "assistant" when the chunk carries message text.
	Role       string
	Text       string
	Thinking   string
	ToolInput  string
	ToolOutput string
	// Prompt: the chunk is something the user typed.
	Prompt    bool
	ToolCalls int
	Messages  int
	Time      string
}

// Fields derives the index fields of a parsed chunk, each cut to MaxIndexBytes.
func Fields(p Parsed) IndexFields {
	var f IndexFields
	var text, thinking, in, out []string
	for _, b := range p.Blocks {
		if f.Time == "" {
			f.Time = b.Time
		}
		switch b.Kind {
		case KindUser:
			f.Prompt = true
			f.Role = "user"
			f.Messages++
			text = append(text, b.Text)
		case KindAssistant:
			if f.Role == "" {
				f.Role = "assistant"
			}
			f.Messages++
			text = append(text, b.Text)
		case KindThinking:
			thinking = append(thinking, b.Text)
		case KindTool:
			f.ToolCalls++
			in = append(in, toolIndexText(b))
			if b.HasOutput {
				out = append(out, b.Output)
			}
		case KindResult:
			out = append(out, b.Output)
		}
	}
	f.Text = cutBytes(strings.Join(text, "\n"), MaxIndexBytes)
	f.Thinking = cutBytes(strings.Join(thinking, "\n"), MaxIndexBytes)
	f.ToolInput = cutBytes(strings.Join(in, "\n"), MaxIndexBytes)
	f.ToolOutput = cutBytes(strings.Join(out, "\n"), MaxIndexBytes)
	return f
}

func toolIndexText(b Block) string {
	parts := []string{b.Tool, b.Input}
	if b.Detail != "" {
		parts = append(parts, b.Detail)
	}
	for _, d := range b.Diff {
		for _, l := range d.Lines {
			if l.Op != '@' {
				parts = append(parts, l.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func cutBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// OutlineRow is the per-chunk data the prompt list is built from.
type OutlineRow struct {
	Seq       int
	Prompt    bool
	ToolCalls int
	Time      string
	Text      string
}

// Prompt is a jump mark: a user prompt with the tool calls that followed it.
type Prompt struct {
	Seq       int
	Time      string
	Text      string
	ToolCalls int
}

// Outline lists the user prompts of a session with the number of tool calls
// until the next prompt. Rows need not be sorted.
func Outline(rows []OutlineRow) []Prompt {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
	var out []Prompt
	for _, r := range rows {
		if r.Prompt {
			out = append(out, Prompt{Seq: r.Seq, Time: r.Time, Text: OneLine(r.Text, 200)})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].ToolCalls += r.ToolCalls
		}
	}
	return out
}

// OneLine collapses whitespace and cuts to max runes.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// noiseText: harness-injected text that the user did not type.
func noiseText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	for _, p := range []string{"<local-command", "<command-name>", "<command-message>", "<command-args>",
		"<system-reminder>", "<environment_context>", "<user-prompt-submit-hook>"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------- Claude Code

type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	IsMeta    bool   `json:"isMeta"`
	AITitle   string `json:"aiTitle"`
	Message   *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func parseClaude(seq int, raw string) Parsed {
	var l claudeLine
	if json.Unmarshal([]byte(raw), &l) != nil {
		return Parsed{}
	}
	if l.Type == "ai-title" {
		return Parsed{Title: strings.TrimSpace(l.AITitle)}
	}
	if (l.Type != "user" && l.Type != "assistant") || l.Message == nil || l.IsMeta {
		return Parsed{}
	}
	var out Parsed
	add := func(b Block) {
		b.Seq, b.Time = seq, l.Timestamp
		out.Blocks = append(out.Blocks, b)
	}
	kind := KindUser
	if l.Type == "assistant" {
		kind = KindAssistant
	}
	var s string
	if json.Unmarshal(l.Message.Content, &s) == nil {
		if !noiseText(s) {
			add(Block{Kind: kind, Text: strings.TrimSpace(s)})
		}
		return out
	}
	var blocks []claudeBlock
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return Parsed{}
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if !noiseText(b.Text) {
				add(Block{Kind: kind, Text: strings.TrimSpace(b.Text)})
			}
		case "thinking":
			if strings.TrimSpace(b.Thinking) != "" {
				add(Block{Kind: KindThinking, Text: strings.TrimSpace(b.Thinking)})
			}
		case "tool_use":
			blk := claudeTool(b)
			add(blk)
		case "tool_result":
			r := Block{Kind: KindResult, CallID: b.ToolUseID, Output: resultText(b.Content), HasOutput: true}
			if b.IsError {
				r.Failed, r.Status = true, "error"
			}
			add(r)
		}
	}
	return out
}

func claudeTool(b claudeBlock) Block {
	blk := Block{Kind: KindTool, Tool: b.Name, CallID: b.ID}
	var in map[string]json.RawMessage
	_ = json.Unmarshal(b.Input, &in)
	str := func(key string) string {
		var s string
		_ = json.Unmarshal(in[key], &s)
		return s
	}
	switch b.Name {
	case "Bash":
		blk.Input = str("command")
		blk.Detail = str("description")
	case "Read", "NotebookEdit":
		blk.Input = firstNonEmpty(str("file_path"), str("notebook_path"))
	case "Write":
		blk.Input = str("file_path")
		blk.Diff = []DiffFile{{Path: blk.Input, Lines: textLines('+', str("content"))}}
	case "Edit":
		blk.Input = str("file_path")
		blk.Diff = []DiffFile{{Path: blk.Input, Lines: replaceLines(str("old_string"), str("new_string"))}}
	case "MultiEdit":
		blk.Input = str("file_path")
		var edits []struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		_ = json.Unmarshal(in["edits"], &edits)
		d := DiffFile{Path: blk.Input}
		for i, e := range edits {
			if i > 0 {
				d.Lines = append(d.Lines, DiffLine{Op: '@'})
			}
			d.Lines = append(d.Lines, replaceLines(e.Old, e.New)...)
		}
		blk.Diff = []DiffFile{d}
	case "Grep", "Glob":
		blk.Input = str("pattern")
		if p := str("path"); p != "" {
			blk.Detail = p
		}
	case "WebFetch":
		blk.Input = str("url")
	case "WebSearch":
		blk.Input = str("query")
	case "Task", "Agent":
		blk.Input = firstNonEmpty(str("description"), str("prompt"))
	}
	if blk.Input == "" && len(b.Input) > 0 && string(b.Input) != "null" && string(b.Input) != "{}" {
		blk.Input = string(b.Input)
	}
	return blk
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func textLines(op byte, s string) []DiffLine {
	if s == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	out := make([]DiffLine, len(lines))
	for i, l := range lines {
		out[i] = DiffLine{Op: op, Text: l}
	}
	return out
}

func replaceLines(old, new string) []DiffLine {
	return append(textLines('-', old), textLines('+', new)...)
}

// resultText flattens a tool_result content: a string, or an array of text
// parts.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// -------------------------------------------------------------------- Codex

type codexLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type      string `json:"type"`
	Role      string `json:"role"`
	Message   string `json:"message"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Input     string `json:"input"`
	CallID    string `json:"call_id"`
	Output    string `json:"output"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Action struct {
		Query string `json:"query"`
	} `json:"action"`
}

var (
	exitCodeRe = regexp.MustCompile(`(?m)^(?:Process exited with code|Exit code:) (-?\d+)`)
	patchFile  = regexp.MustCompile(`^\*\*\* (?:Update|Add|Delete) File: (.+)$`)
)

func parseCodex(seq int, raw string) Parsed {
	var l codexLine
	if json.Unmarshal([]byte(raw), &l) != nil {
		return Parsed{}
	}
	var p codexPayload
	if json.Unmarshal(l.Payload, &p) != nil {
		return Parsed{}
	}
	var out Parsed
	add := func(b Block) {
		b.Seq, b.Time = seq, l.Timestamp
		out.Blocks = append(out.Blocks, b)
	}
	switch l.Type {
	case "event_msg":
		// user_message is the prompt as typed; the response_item copy of it
		// carries harness context. agent_message duplicates the assistant
		// response_item, token_count and patch_apply_end are bookkeeping.
		if p.Type == "user_message" && !noiseText(p.Message) {
			add(Block{Kind: KindUser, Text: strings.TrimSpace(p.Message)})
		}
	case "response_item":
		switch p.Type {
		case "message":
			if p.Role != "assistant" {
				break
			}
			var texts []string
			for _, c := range p.Content {
				if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
					texts = append(texts, strings.TrimSpace(c.Text))
				}
			}
			if len(texts) > 0 {
				add(Block{Kind: KindAssistant, Text: strings.Join(texts, "\n")})
			}
		case "reasoning":
			var texts []string
			for _, s := range p.Summary {
				if strings.TrimSpace(s.Text) != "" {
					texts = append(texts, strings.TrimSpace(s.Text))
				}
			}
			for _, c := range p.Content {
				if strings.TrimSpace(c.Text) != "" {
					texts = append(texts, strings.TrimSpace(c.Text))
				}
			}
			if len(texts) > 0 {
				add(Block{Kind: KindThinking, Text: strings.Join(texts, "\n")})
			}
		case "function_call":
			add(codexCall(p))
		case "custom_tool_call":
			b := Block{Kind: KindTool, Tool: p.Name, CallID: p.CallID}
			if p.Name == "apply_patch" {
				b.Diff = parsePatch(p.Input)
				var paths []string
				for _, d := range b.Diff {
					paths = append(paths, d.Path)
				}
				b.Input = strings.Join(paths, ", ")
			} else {
				b.Input = p.Input
			}
			add(b)
		case "web_search_call":
			if p.Action.Query != "" {
				add(Block{Kind: KindTool, Tool: "web_search", Input: p.Action.Query})
			}
		case "function_call_output", "custom_tool_call_output":
			add(codexResult(p))
		}
	}
	return out
}

func codexCall(p codexPayload) Block {
	b := Block{Kind: KindTool, Tool: p.Name, CallID: p.CallID}
	var args map[string]json.RawMessage
	_ = json.Unmarshal([]byte(p.Arguments), &args)
	var cmd string
	if json.Unmarshal(args["cmd"], &cmd) != nil || cmd == "" {
		_ = json.Unmarshal(args["command"], &cmd)
	}
	switch {
	case cmd != "":
		b.Input = cmd
	case strings.TrimSpace(p.Arguments) != "" && p.Arguments != "{}":
		b.Input = p.Arguments
	}
	return b
}

func codexResult(p codexPayload) Block {
	out := p.Output
	// Older rollouts wrap the output in {"output": "...", "metadata": {...}}.
	if strings.HasPrefix(out, "{") {
		var wrapped struct {
			Output   string `json:"output"`
			Metadata struct {
				ExitCode *int `json:"exit_code"`
			} `json:"metadata"`
		}
		if json.Unmarshal([]byte(out), &wrapped) == nil && wrapped.Output != "" {
			out = wrapped.Output
			if c := wrapped.Metadata.ExitCode; c != nil {
				out = "Process exited with code " + strconv.Itoa(*c) + "\nOutput:\n" + out
			}
		}
	}
	b := Block{Kind: KindResult, CallID: p.CallID, HasOutput: true}
	if m := exitCodeRe.FindStringSubmatch(out); m != nil && m[1] != "0" {
		b.Failed, b.Status = true, "exit "+m[1]
	}
	if i := strings.Index(out, "\nOutput:\n"); i >= 0 {
		out = out[i+len("\nOutput:\n"):]
	}
	b.Output = strings.TrimRight(out, "\n")
	return b
}

// parsePatch reads Codex's apply_patch format into per-file diffs.
func parsePatch(s string) []DiffFile {
	var files []DiffFile
	for _, line := range strings.Split(s, "\n") {
		if m := patchFile.FindStringSubmatch(line); m != nil {
			files = append(files, DiffFile{Path: strings.TrimSpace(m[1])})
			continue
		}
		if len(files) == 0 || strings.HasPrefix(line, "*** ") {
			continue
		}
		f := &files[len(files)-1]
		switch {
		case strings.HasPrefix(line, "@@"):
			f.Lines = append(f.Lines, DiffLine{Op: '@', Text: strings.TrimSpace(strings.TrimPrefix(line, "@@"))})
		case line == "":
		case line[0] == '+' || line[0] == '-' || line[0] == ' ':
			f.Lines = append(f.Lines, DiffLine{Op: line[0], Text: line[1:]})
		}
	}
	return files
}
