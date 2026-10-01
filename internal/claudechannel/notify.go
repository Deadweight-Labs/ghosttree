package claudechannel

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Methode und Fähigkeit des Claude-Channel-Protokolls.
const (
	Method         = "notifications/claude/channel"
	CapabilityName = "claude/channel"
)

// ErrNotConnected: der Transport hat noch keine Verbindung. Vor dem
// Verbindungsaufbau gibt es nichts, wohin eine Notification gehen könnte.
var ErrNotConnected = errors.New("claude channel: transport is not connected")

// ServerOptions deklariert capabilities.experimental["claude/channel"]. Ohne
// diese Fähigkeit behandelt Claude Code den Server nicht als Channel.
func ServerOptions() *mcp.ServerOptions {
	return &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{
		Experimental: map[string]any{CapabilityName: map[string]any{}},
	}}
}

// Notification ist der Inhalt einer Channel-Notification. Claude zeigt ihn als
// <channel source=... k=v>content</channel>; Meta-Schlüssel dürfen deshalb nur
// aus Buchstaben, Ziffern und Unterstrich bestehen.
type Notification struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta"`
}

// NewNotification baut die Notification zu einer Nachricht. meta trägt
// message_id und origin_event_id, über die eine Antwort ihre causation_id
// setzt, dazu Raum und Absender.
func NewNotification(room store.CoordRoom, m store.CoordMessage, content string) Notification {
	meta := map[string]string{
		"message_id": strconv.FormatInt(m.ID, 10),
		"room":       room.Key,
		"room_kind":  room.Kind,
		"sender":     m.SenderExternalID,
	}
	if m.OriginEventID != "" {
		meta["origin_event_id"] = m.OriginEventID
	}
	return Notification{Content: content, Meta: meta}
}

// request ist eine jsonrpc-Notification: eine Request OHNE ID. Das go-sdk
// v1.7.0 hat keine öffentliche API für eigene Notifications; eine Request mit
// leerer ID ist auf dem Draht genau das.
func (n Notification) request() (*jsonrpc.Request, error) {
	params, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	return &jsonrpc.Request{Method: Method, Params: params}, nil
}

// Notifier sendet Notifications. Der Poller kennt nur dieses Interface.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Transport umhüllt einen mcp.Transport und schreibt Notifications an der
// SDK-Schicht vorbei direkt auf dieselbe Verbindung. Connection.Write darf
// nebenläufig aufgerufen werden, so dass das den Verkehr des SDK nicht stört.
type Transport struct {
	inner mcp.Transport
	mu    sync.Mutex
	conn  mcp.Connection
}

var _ mcp.Transport = (*Transport)(nil)
var _ Notifier = (*Transport)(nil)

// NewTransport umhüllt inner, etwa &mcp.StdioTransport{}.
func NewTransport(inner mcp.Transport) *Transport { return &Transport{inner: inner} }

// Connect verbindet den inneren Transport und merkt sich die Verbindung.
func (t *Transport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
	return conn, nil
}

// Notify schreibt notifications/claude/channel mit params {content, meta}.
func (t *Transport) Notify(ctx context.Context, n Notification) error {
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	req, err := n.request()
	if err != nil {
		return err
	}
	return conn.Write(ctx, req)
}
