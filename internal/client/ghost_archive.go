package client

import (
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"net/url"
)

func (c *Client) PrepareGhostArchive(project, path string) (store.GhostArchiveCandidate, error) {
	var out store.GhostArchiveCandidate
	err := c.do("GET", "/api/ghosts/archive-candidate", url.Values{"project": {project}, "path": {path}}, nil, &out)
	return out, err
}

func (c *Client) ArchiveGhosts(in store.GhostArchiveInput) (store.GhostArchiveResult, error) {
	var out store.GhostArchiveResult
	err := c.do("POST", "/api/ghosts/archive", nil, in, &out)
	return out, err
}
