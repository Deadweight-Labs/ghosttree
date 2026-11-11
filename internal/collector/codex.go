package collector

import (
	"encoding/json"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type codexLine struct {
	Type    string `json:"type"`
	Payload *struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		Git       *struct {
			RepositoryURL string `json:"repository_url"`
			Branch        string `json:"branch"`
		} `json:"git"`
	} `json:"payload"`
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
