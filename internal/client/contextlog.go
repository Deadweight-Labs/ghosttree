package client

import (
	"context"
	"net/url"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Delivered is a text the server answered with and the way the server says it
// divides into entries (X-Ghosttree-Delivered). Parts is empty for a server
// that does not send the header; the text is complete either way.
type Delivered struct {
	Text  string
	Parts []deliverylog.Part
}

// TriggeredRunbooksDelivered is TriggeredRunbooks that also returns the parts.
func (c *Client) TriggeredRunbooksDelivered(t RunbookTrigger) (Delivered, error) {
	var d Delivered
	err := c.do("POST", "/api/runbooks/triggered", nil, t, &d)
	return d, err
}

// RecordContextLog reports the deliveries and signals of one session (REQ-636).
func (c *Client) RecordContextLog(b store.ContextLogBatch) error {
	return c.RecordContextLogContext(context.Background(), b)
}

// RecordContextLogContext is RecordContextLog under a context, so that a caller
// that sends several reports can give all of them one deadline.
func (c *Client) RecordContextLogContext(ctx context.Context, b store.ContextLogBatch) error {
	return c.doContext(ctx, "POST", "/api/context-log", nil, b, nil)
}

func usageQuery(project, since string) url.Values {
	q := url.Values{"since": {since}}
	if project != "" {
		q.Set("project", project)
	}
	return q
}

// UsageTools is tool_call_counts per tool since a day.
func (c *Client) UsageTools(project, since string) (store.ToolReport, error) {
	var out store.ToolReport
	err := c.do("GET", "/api/usage/tools", usageQuery(project, since), nil, &out)
	return out, err
}

// UsageDeliveries is the delivery log in sums since a day.
func (c *Client) UsageDeliveries(project, since string) (deliverylog.DeliveryUsage, error) {
	var out deliverylog.DeliveryUsage
	err := c.do("GET", "/api/usage/deliveries", usageQuery(project, since), nil, &out)
	return out, err
}

// UsageSignals is the session signals since a day, held against session length.
func (c *Client) UsageSignals(project, since string) (deliverylog.SignalUsage, error) {
	var out deliverylog.SignalUsage
	err := c.do("GET", "/api/usage/signals", usageQuery(project, since), nil, &out)
	return out, err
}

// UsageContext is the use of the delivered context since a day: what the agent
// picked up or followed, per route and kind and per profile.
func (c *Client) UsageContext(project, since string) (deliverylog.ContextUse, error) {
	var out deliverylog.ContextUse
	err := c.do("GET", "/api/usage/context", usageQuery(project, since), nil, &out)
	return out, err
}
