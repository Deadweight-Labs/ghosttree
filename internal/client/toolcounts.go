package client

import "github.com/Deadweight-Labs/ghosttree/internal/store"

// RecordToolUses reports what one session called of ghosttree's tools.
func (c *Client) RecordToolUses(b store.ToolUseBatch) error {
	return c.do("POST", "/api/tool-uses", nil, b, nil)
}
