package collector

import (
	"encoding/json"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type codexLine struct {
	Type    string `json:"type"`
	Payload *struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Output    json.RawMessage `json:"output"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     json.RawMessage `json:"input"`
		SessionID string          `json:"session_id"`
		CWD       string          `json:"cwd"`
		Git       *struct {
			RepositoryURL string `json:"repository_url"`
			Branch        string `json:"branch"`
		} `json:"git"`
	} `json:"payload"`
}

func ParseCodexLine(line []byte) ParsedLine {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil || l.Payload == nil {
		return ParsedLine{}
	}
	if l.Type != "response_item" {
		return ParsedLine{}
	}
	if l.Payload.Type == "function_call_output" || l.Payload.Type == "custom_tool_call_output" {
		return ParsedLine{Role: "tool", Text: structuredResultText(l.Payload.Output)}
	}
	// The call, not only its result. Codex puts it on its own line, so it was
	// dropped as an unknown type and the archive could say what a tool returned
	// but never which tool was reached for.
	if l.Payload.Type == "function_call" || l.Payload.Type == "custom_tool_call" {
		text := "[tool call: " + l.Payload.Name + "]"
		if args := codexCallArgs(l.Payload.Arguments, l.Payload.Input); args != "" {
			text += " " + truncateRunes(args, maxToolCallArgs)
		}
		return ParsedLine{Role: "tool", Text: text}
	}
	if l.Payload.Type != "message" {
		return ParsedLine{}
	}
	return ParsedLine{
		Role: l.Payload.Role,
		Text: contentText(l.Payload.Content, "input_text", "output_text", "text"),
	}
}

// codexCallArgs reads whichever field carries the call's arguments. A
// function_call encodes them as a JSON string; a custom_tool_call puts raw
// input in its place, which may or may not be JSON.
func codexCallArgs(arguments string, input json.RawMessage) string {
	if a := strings.TrimSpace(arguments); a != "" && a != "{}" && a != "null" {
		return a
	}
	if len(input) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(input, &s) == nil {
		return strings.TrimSpace(s)
	}
	if raw := strings.TrimSpace(string(input)); raw != "{}" && raw != "null" {
		return raw
	}
	return ""
}

func CodexSessionMeta(firstLines [][]byte) (externalID, cwd, project, branch string) {
	for _, line := range firstLines {
		var l codexLine
		if err := json.Unmarshal(line, &l); err != nil || l.Payload == nil {
			continue
		}
		if l.Type == "session_meta" {
			if l.Payload.Git != nil {
				project, branch = scope.NormalizeRemote(l.Payload.Git.RepositoryURL), l.Payload.Git.Branch
			}
			return l.Payload.SessionID, l.Payload.CWD, project, branch
		}
	}
	return "", "", "", ""
}
