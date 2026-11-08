package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// PullEntry ist eine Zeile der PR-Liste mit ihrer Adresse.
type PullEntry struct {
	store.PullSummary
	Link string `json:"link,omitempty"`
}

// PullListOut ist eine Seite der PR-Liste.
type PullListOut struct {
	Project string           `json:"project"`
	Items   []PullEntry      `json:"items"`
	Total   int              `json:"total"`
	Counts  store.PullCounts `json:"counts"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

// PullDetailOut ist ein PR mit Checkliste und Brief, samt Adresse und Karte.
type PullDetailOut struct {
	store.PullDetail
	Link string     `json:"link,omitempty"`
	Card links.Card `json:"card"`
}

func pageQuery(project string, limit, offset int) url.Values {
	q := url.Values{"project": {project}}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return q
}

// PullList liest die PRs eines Projekts (state open, merged, closed oder leer für alle).
func (c *Client) PullList(project, state, branch, author string, limit, offset int) (PullListOut, error) {
	q := pageQuery(project, limit, offset)
	for k, v := range map[string]string{"state": state, "branch": branch, "author": author} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var out PullListOut
	err := c.do("GET", "/api/pulls", q, nil, &out)
	return out, err
}

// PullGet liest einen PR mit Checkliste, Paket und Brief.
func (c *Client) PullGet(project string, n int64) (PullDetailOut, error) {
	var out PullDetailOut
	err := c.do("GET", "/api/pulls/"+strconv.FormatInt(n, 10), url.Values{"project": {project}}, nil, &out)
	return out, err
}
