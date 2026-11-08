package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// CommitEntryOut ist ein Commit mit seiner Adresse.
type CommitEntryOut struct {
	store.CommitInfo
	Link string `json:"link,omitempty"`
}

// CommitListOut ist eine Seite der Commits eines Projekts oder Branches.
type CommitListOut struct {
	Project string           `json:"project"`
	Branch  string           `json:"branch,omitempty"`
	Items   []CommitEntryOut `json:"items"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

// PullCommitsOut sind die Commits eines PRs.
type PullCommitsOut struct {
	Project string           `json:"project"`
	PR      int64            `json:"pr"`
	Link    string           `json:"link,omitempty"`
	Items   []CommitEntryOut `json:"items"`
}

// CommitSaid ist ein Eintrag, der an einem Commit hängt, mit Adresse.
type CommitSaid struct {
	store.CommitEntry
	Link string `json:"link,omitempty"`
}

// CommitDetailOut ist ein Commit mit dem, was zu ihm gesagt wurde.
type CommitDetailOut struct {
	store.CommitInfo
	Project string       `json:"project"`
	Said    []CommitSaid `json:"said"`
	Link    string       `json:"link,omitempty"`
	Card    links.Card   `json:"card"`
}

// PullCommits liest die Commits eines PRs.
func (c *Client) PullCommits(project string, n int64) (PullCommitsOut, error) {
	var out PullCommitsOut
	err := c.do("GET", "/api/pulls/"+strconv.FormatInt(n, 10)+"/commits", url.Values{"project": {project}}, nil, &out)
	return out, err
}

// CommitList liest die Commits eines Projekts, mit branch nur die eines Branches.
func (c *Client) CommitList(project, branch string, limit, offset int) (CommitListOut, error) {
	q := pageQuery(project, limit, offset)
	if branch != "" {
		q.Set("branch", branch)
	}
	var out CommitListOut
	err := c.do("GET", "/api/commits", q, nil, &out)
	return out, err
}

// CommitGet liest einen Commit (Hash oder Präfix ab 7 Zeichen) mit dem, was zu ihm gesagt wurde.
func (c *Client) CommitGet(project, sha string) (CommitDetailOut, error) {
	var out CommitDetailOut
	err := c.do("GET", "/api/commits/"+url.PathEscape(sha), url.Values{"project": {project}}, nil, &out)
	return out, err
}
