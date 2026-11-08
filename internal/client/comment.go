package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// AddComment schreibt einen Kommentar an ein Issue (request), eine Idee (idea)
// oder einen Pain (pain).
func (c *Client) AddComment(kind string, id int64, body string, sessionID int64) (store.Comment, error) {
	in := map[string]any{"kind": kind, "id": id, "body": body}
	if sessionID > 0 {
		in["session_id"] = sessionID
	}
	var out store.Comment
	err := c.do("POST", "/api/comments", nil, in, &out)
	return out, err
}

// Comments listet die Kommentare eines Objekts, die ältesten zuerst.
func (c *Client) Comments(kind string, id int64) ([]store.Comment, error) {
	var out []store.Comment
	err := c.do("GET", "/api/comments", url.Values{"kind": {kind}, "id": {strconv.FormatInt(id, 10)}}, nil, &out)
	return out, err
}

// EditComment ersetzt den Text eines eigenen Kommentars durch eine neue Fassung.
func (c *Client) EditComment(id int64, body string) (store.Comment, error) {
	var out store.Comment
	err := c.do("PATCH", "/api/comments/"+strconv.FormatInt(id, 10), nil, map[string]any{"body": body}, &out)
	return out, err
}

// DeleteComment löscht einen Kommentar weich.
func (c *Client) DeleteComment(id int64) (store.Comment, error) {
	var out store.Comment
	err := c.do("DELETE", "/api/comments/"+strconv.FormatInt(id, 10), nil, nil, &out)
	return out, err
}

// CommentVersions liefert die Fassungen eines Kommentars.
func (c *Client) CommentVersions(id int64) ([]store.CommentVersion, error) {
	var out []store.CommentVersion
	err := c.do("GET", "/api/comments/"+strconv.FormatInt(id, 10)+"/versions", nil, nil, &out)
	return out, err
}
