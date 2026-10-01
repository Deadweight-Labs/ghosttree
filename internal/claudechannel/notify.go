package claudechannel

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"

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
	// Ready sagt, ob eine Notification jetzt rausgehen kann. Der Poller claimt
	// erst, wenn das stimmt.
	Ready() bool
	Notify(ctx context.Context, n Notification) error
}

// Transport umhüllt einen mcp.Transport und schreibt Notifications an der
// SDK-Schicht vorbei direkt auf dieselbe Verbindung. Connection.Write darf
// nebenläufig aufgerufen werden, so dass das den Verkehr des SDK nicht stört.
//
// Bereit ist der Transport erst, wenn der Client notifications/initialized
// geschickt hat, und nicht mehr nach Close oder einem Lesefehler.
type Transport struct {
	inner mcp.Transport
	mu    sync.Mutex
	conn  *gate
}

var _ mcp.Transport = (*Transport)(nil)
var _ Notifier = (*Transport)(nil)

// NewTransport umhüllt inner, etwa &mcp.StdioTransport{}.
func NewTransport(inner mcp.Transport) *Transport { return &Transport{inner: inner} }

// gate beobachtet den Verkehr, um die Bereitschaft abzuleiten.
type gate struct {
	mcp.Connection
	ready atomic.Bool
}

// methodDiscover ist die Anfrage, mit der Claude Code eine moderne Verbindung
// (MCP 2026-07-28) probiert. Auf einer solchen Verbindung registriert Claude
// Code keinen Channel; nur die alte initialize-Verbindung trägt Channels.
const methodDiscover = "server/discover"

// Read liest die nächste Nachricht für das SDK. Ein server/discover mit ID
// beantwortet die Lese-Seite selbst mit -32601 und gibt es nicht ans SDK
// weiter: das SDK würde 2026-07-28 aushandeln. Claude Code fällt dann auf
// initialize zurück. Bereit wird die Verbindung erst mit
// notifications/initialized.
func (g *gate) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		msg, err := g.Connection.Read(ctx)
		if err != nil {
			g.ready.Store(false)
			return msg, err
		}
		req, ok := msg.(*jsonrpc.Request)
		if !ok {
			return msg, nil
		}
		if req.Method == methodDiscover && req.IsCall() {
			rejection := &jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "Method not found"}}
			if err := g.Connection.Write(ctx, rejection); err != nil {
				g.ready.Store(false)
				return nil, err
			}
			continue
		}
		if req.Method == "notifications/initialized" {
			g.ready.Store(true)
		}
		return msg, nil
	}
}

func (g *gate) Close() error {
	g.ready.Store(false)
	return g.Connection.Close()
}

// Connect verbindet den inneren Transport und merkt sich die Verbindung.
func (t *Transport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	g := &gate{Connection: conn}
	t.mu.Lock()
	t.conn = g
	t.mu.Unlock()
	return g, nil
}

func (t *Transport) current() *gate {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn
}

// Ready sagt, ob der Handshake abgeschlossen und die Verbindung offen ist.
func (t *Transport) Ready() bool {
	g := t.current()
	return g != nil && g.ready.Load()
}

// Notify schreibt notifications/claude/channel mit params {content, meta}.
func (t *Transport) Notify(ctx context.Context, n Notification) error {
	g := t.current()
	if g == nil || !g.ready.Load() {
		return ErrNotConnected
	}
	req, err := n.request()
	if err != nil {
		return err
	}
	return g.Write(ctx, req)
}
