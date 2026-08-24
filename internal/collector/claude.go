// Package collector tails harness transcripts and uploads them.
// Parsers never fail: anything not understood yields an empty ParsedLine and
// the caller still stores the raw line.
package collector

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type ParsedLine struct {
	Role string // "user"|"assistant"|"" (unknown line types keep "")
	Text string // extracted text, "" if none
}

type claudeLine struct {
	Type      string `json:"type"`
	CWD       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	SessionID string `json:"sessionId"`
	Message   *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func ParseClaudeLine(line []byte) ParsedLine {
	var l claudeLine
	if err := json.Unmarshal(line, &l); err != nil {
		return ParsedLine{}
	}
	if l.Type != "user" && l.Type != "assistant" {
		return ParsedLine{}
	}
	if l.Message == nil {
		return ParsedLine{}
	}
	if toolText := claudeToolResultText(l.Message.Content); toolText != "" {
		return ParsedLine{Role: "tool", Text: toolText}
	}
	text := contentText(l.Message.Content, "text")
	if calls := claudeToolCallText(l.Message.Content); calls != "" {
		if text != "" {
			text += "\n"
		}
		text += calls
	}
	return ParsedLine{Role: l.Message.Role, Text: text}
}

// maxToolCallArgs caps how much of a call's arguments is archived. The name is
// the signal — it says which tool was reached for — while the arguments are
// context, and a Write call carries a whole file whose content is already in
// the repository.
const maxToolCallArgs = 600

// claudeToolCallText renders the tool_use blocks of one assistant turn.
//
// Without this the archive records what a tool returned but not which tool was
// called, so no query can answer how often a tool was used. Measuring
// ghosttree's own adoption had to fall back on grepping transcripts for the
// string "context_search", which counts a session that discusses the tool the
// same as one that calls it.
func claudeToolCallText(raw json.RawMessage) string {
	var blocks []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type != "tool_use" || b.Name == "" {
			continue
		}
		line := "[tool call: " + b.Name + "]"
		if args := strings.TrimSpace(string(b.Input)); args != "" && args != "{}" && args != "null" {
			line += " " + truncateRunes(args, maxToolCallArgs)
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n")
}

// truncateRunes cuts on a rune boundary. Arguments are frequently German and a
// cut mid-rune would put mojibake into the search index.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func claudeToolResultText(raw json.RawMessage) string {
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "tool_result" {
			if text := structuredResultText(block.Content); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func structuredResultText(raw json.RawMessage) string {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	var parts []string
	collectResultStrings(value, "", &parts)
	text := strings.Join(parts, "\n")
	const maxToolText = 64 * 1024
	if len(text) > maxToolText {
		text = text[:maxToolText] + "\n…(tool result truncated)"
	}
	return text
}

func collectResultStrings(value any, key string, parts *[]string) {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			*parts = append(*parts, v)
		}
	case []any:
		for _, item := range v {
			collectResultStrings(item, key, parts)
		}
	case map[string]any:
		if kind, _ := v["type"].(string); kind == "image" || kind == "audio" {
			return
		}
		allowed := map[string]bool{"text": true, "output": true, "content": true, "stdout": true, "stderr": true, "result": true, "error": true, "message": true, "title": true, "url": true, "path": true, "ref": true, "summary": true}
		for _, name := range []string{"title", "message", "text", "stdout", "stderr", "output", "result", "content", "summary", "path", "url", "ref", "error"} {
			if child, ok := v[name]; ok && (key == "" || allowed[name]) {
				collectResultStrings(child, name, parts)
			}
		}
	}
}

// contentText decodes the two shapes both harnesses use: a plain string, or an
// array of typed blocks of which only the given text kinds carry prose.
func contentText(raw json.RawMessage, kinds ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		for _, k := range kinds {
			if b.Type == k && b.Text != "" {
				parts = append(parts, b.Text)
				break
			}
		}
	}
	return strings.Join(parts, "\n")
}

// ClaudeSessionMeta derives session identity from the file name and the first
// line that carries the context fields. A detached HEAD has no useful branch.
func ClaudeSessionMeta(path string, firstLines [][]byte) (externalID, cwd, branch string) {
	externalID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	for _, line := range firstLines {
		var l claudeLine
		if err := json.Unmarshal(line, &l); err != nil {
			continue
		}
		if l.CWD == "" {
			continue
		}
		cwd = l.CWD
		if l.GitBranch != "HEAD" {
			branch = l.GitBranch
		}
		return externalID, cwd, branch
	}
	return externalID, "", ""
}
