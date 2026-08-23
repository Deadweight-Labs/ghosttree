// Package installer wires ghosttree into the agent harnesses. Every change is
// idempotent and merges into existing files instead of replacing them.
package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

const (
	markerStart = "<!-- ghosttree:start -->"
	markerEnd   = "<!-- ghosttree:end -->"
)

// ruleText is the section both harnesses get: keep operational history out of
// the source tree.
const ruleText = `## ghosttree

Code comments explain code to humans. Operational history, failed approaches
and session notes go to ghosttree via the ` + "`context_remember`" + ` MCP tool, never
into source comments. Search prior knowledge with ` + "`context_search`" + ` before
re-deriving it. Call ` + "`context_get`" + ` again with repository-relative paths before
working in another subtree, and with an explicit task tag for deploy, security,
review, test or docs work.

For substantial feature, architecture, migration, or multi-session work, use
` + "`request_search`" + ` before implementation. Continue a matching request and
associate the current session, or create a request with explicit acceptance
criteria. Trivial local fixes and routine maintenance do not require a request.
Mark criteria and requests complete only with concrete evidence.`

func section(body string) string {
	return markerStart + "\n" + body + "\n" + markerEnd + "\n"
}

// replaceSection swaps the marked section for body, or appends it.
func replaceSection(content, body string) string {
	block := section(body)
	start := strings.Index(content, markerStart)
	end := strings.Index(content, markerEnd)
	if start >= 0 && end > start {
		return content[:start] + block + content[end+len(markerEnd)+1:]
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if content != "" {
		content += "\n"
	}
	return content + block
}

func writeMarkerFile(path, body string) (Change, error) {
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Change{}, err
	}
	updated := replaceSection(string(old), body)
	if string(old) == updated {
		return Change{Path: path, Action: "unchanged"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Change{}, err
	}
	action := "updated"
	if len(old) == 0 {
		action = "created"
	}
	return Change{Path: path, Action: action}, writeAtomic(path, []byte(updated), 0o644)
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".ghosttree.tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readJSONFile returns an empty object for a missing file so callers can merge
// unconditionally, but refuses to touch a file it cannot parse.
func readJSONFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeJSONFile(path string, m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'), 0o644)
}

// HasMarker reports whether a file already carries the ghosttree section.
func HasMarker(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), markerStart)
}
