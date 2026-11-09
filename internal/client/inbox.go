package client

import (
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// InboxEntry is one entry of the caller's inbox with the address it leads to.
type InboxEntry struct {
	store.InboxEntry
	Link string `json:"link,omitempty"`
}

// Inbox reads what waits for the caller: human tasks, decisions that need a
// person, and questions or approvals addressed to people (GET /api/inbox).
// project empty means every project the caller sees; state is open (the
// default), done or all; mine limits tasks to the caller's.
func (c *Client) Inbox(project, state string, mine bool, limit int) ([]InboxEntry, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	if state != "" {
		q.Set("state", state)
	}
	if mine {
		q.Set("mine", "1")
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []InboxEntry
	err := c.do("GET", "/api/inbox", q, nil, &out)
	return out, err
}
