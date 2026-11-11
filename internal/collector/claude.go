// Package collector tails harness transcripts and uploads them.
// The line parsers live in internal/chunktext; what stays here is the session
// identity and the upload.
package collector

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type claudeLine struct {
	CWD       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
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
