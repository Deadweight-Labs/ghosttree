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
	return ParsedLine{Role: l.Message.Role, Text: contentText(l.Message.Content, "text")}
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
