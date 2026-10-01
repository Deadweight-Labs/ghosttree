package client

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// RegisterCoordAgent meldet diese Session in ihrem Raum an. Idempotent über
// die external_id: ein Neustart erzeugt keinen zweiten Teilnehmer.
func (c *Client) RegisterCoordAgent(a store.CoordAgent) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do("POST", "/api/coord/agents", nil, a, &out)
	return out.ID, err
}

// CoordPeers listet die Teilnehmer eines Raums. since darf leer sein.
func (c *Client) CoordPeers(roomKey, since, agentExternalID string) ([]store.CoordAgent, error) {
	q := url.Values{}
	q.Set("room_key", roomKey)
	q.Set("agent_external_id", agentExternalID)
	if since != "" {
		q.Set("since", since)
	}
	var out []store.CoordAgent
	err := c.do("GET", "/api/coord/agents", q, nil, &out)
	return out, err
}

func (c *Client) PublicCoordInbox(destinationKind, destinationID string, afterID int64, limit int) ([]store.CoordMessage, error) {
	q := url.Values{}
	q.Set("public_only", "1")
	q.Set("destination_kind", destinationKind)
	q.Set("destination_id", destinationID)
	q.Set("after", strconv.FormatInt(afterID, 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []store.CoordMessage
	err := c.do("GET", "/api/coord/messages", q, nil, &out)
	return out, err
}

// SendCoordMessage schickt einen Beitrag. Der Server bestimmt Herkunft und
// Sequenz; was hier zurückkommt, ist die gespeicherte ID — nicht die Zusage,
// dass jemand sie gelesen hat.
func (c *Client) SendCoordMessage(m store.CoordMessage) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do("POST", "/api/coord/messages", nil, m, &out)
	return out.ID, err
}

// CoordInbox holt das Fenster nach afterID. afterID=0 ist der Anfang.
func (c *Client) CoordInbox(destinationKind, destinationID, asker string, afterID int64, limit int) ([]store.CoordMessage, error) {
	q := url.Values{}
	q.Set("destination_kind", destinationKind)
	q.Set("destination_id", destinationID)
	q.Set("agent_external_id", asker)
	q.Set("after", strconv.FormatInt(afterID, 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []store.CoordMessage
	err := c.do("GET", "/api/coord/messages", q, nil, &out)
	return out, err
}

// CoordCursor liest den gespeicherten Lesestand. Getrennt vom Abholen, damit
// ein Client nach einem Absturz seinen Stand erfragen kann, ohne dabei
// Nachrichten zu konsumieren.
func (c *Client) CoordCursor(agentExternalID, destinationKind, destinationID string) (int64, error) {
	q := url.Values{}
	q.Set("agent_external_id", agentExternalID)
	q.Set("destination_kind", destinationKind)
	q.Set("destination_id", destinationID)
	var out struct {
		Last int64 `json:"last_message_id"`
	}
	err := c.do("GET", "/api/coord/cursor", q, nil, &out)
	return out.Last, err
}

// SetCoordCursor schreibt den Lesestand fort. Der Server hält ihn monoton;
// ein verspäteter älterer Wert zieht ihn nicht zurück.
func (c *Client) SetCoordCursor(agentExternalID, destinationKind, destinationID string, lastMessageID int64) error {
	return c.do("POST", "/api/coord/cursor", nil, map[string]any{
		"agent_external_id": agentExternalID,
		"destination_kind":  destinationKind,
		"destination_id":    destinationID,
		"last_message_id":   lastMessageID,
	}, nil)
}

// EnsureCoordRoom eröffnet einen Raum mit schon feststehendem Schlüssel. Neue
// Gruppen müssen über CreateCoordGroup entstehen, damit ihre ID opak bleibt.
func (c *Client) EnsureCoordRoom(r store.CoordRoom, agentExternalID string) error {
	q := url.Values{"agent_external_id": {agentExternalID}}
	return c.do("POST", "/api/coord/rooms", q, r, nil)
}

func (c *Client) CreateCoordGroup(in store.GroupInput, agentExternalID string) (store.CoordRoom, error) {
	q := url.Values{"agent_external_id": {agentExternalID}}
	var out store.CoordRoom
	err := c.do("POST", "/api/coord/groups", q, in, &out)
	return out, err
}

// CoordRoomsFor listet die Direkt- und Gruppenräume eines Teilnehmers.
func (c *Client) CoordRoomsFor(agentExternalID string) ([]store.CoordRoom, error) {
	q := url.Values{}
	q.Set("agent_external_id", agentExternalID)
	var out []store.CoordRoom
	err := c.do("GET", "/api/coord/rooms", q, nil, &out)
	return out, err
}

// CoordMessageMentions liest die ausdrücklich erwähnten Empfänger einer
// Nachricht. Sie tragen die Zustellregeln: eine Erwähnung wird zeitnah
// geliefert, gewöhnlicher Raumverkehr darf gebündelt werden.
func (c *Client) CoordMessageMentions(messageID int64, agentExternalID string) ([]string, error) {
	q := url.Values{"agent_external_id": {agentExternalID}}
	var out []string
	err := c.do("GET", fmt.Sprintf("/api/coord/messages/%d/mentions", messageID), q, nil, &out)
	return out, err
}

// PathActivitySince fragt, wer zuletzt an einem Pfad gearbeitet hat.
func (c *Client) PathActivitySince(project, path string, minutes int, excludeSession string) ([]store.PathActivity, error) {
	q := url.Values{}
	q.Set("path", path)
	if project != "" {
		q.Set("project", project)
	}
	if minutes > 0 {
		q.Set("minutes", strconv.Itoa(minutes))
	}
	if excludeSession != "" {
		q.Set("exclude_session", excludeSession)
	}
	var out []store.PathActivity
	err := c.do("GET", "/api/activity/path", q, nil, &out)
	return out, err
}

// SessionPathActivity zeigt, woran eine Session gearbeitet hat.
func (c *Client) SessionPathActivity(session string, minutes, limit int) ([]store.PathActivity, error) {
	q := url.Values{}
	q.Set("session", session)
	if minutes > 0 {
		q.Set("minutes", strconv.Itoa(minutes))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []store.PathActivity
	err := c.do("GET", "/api/activity/session", q, nil, &out)
	return out, err
}

// RecordPathActivity meldet beobachtete Aktivität. Der Collector benutzt das;
// ein Fehler hier darf das Archivieren eines Transkripts nicht scheitern
// lassen.
func (c *Client) RecordPathActivity(events []store.PathActivity) error {
	return c.do("POST", "/api/activity", nil, events, nil)
}

// MarkCoordDelivery schreibt den Zustellzustand fort. Ein Adapter meldet
// damit, was er WIRKLICH beobachtet hat — injected, wenn der Harness die
// Eingabe genommen hat, acked erst bei einer gesehenen Antwort.
func (c *Client) MarkCoordDelivery(messageID int64, recipient, state string) error {
	return c.do("POST", "/api/coord/deliveries", nil, map[string]any{
		"message_id": messageID, "recipient_external_id": recipient, "state": state,
	}, nil)
}

// ClaimCoordDelivery fragt, ob dieser Aufrufer die Nachricht einbringen darf.
// Genau ein Aufrufer je Nachricht und Empfänger bekommt true.
func (c *Client) ClaimCoordDelivery(messageID int64, recipient string) (bool, error) {
	return c.ClaimCoordDeliveryContext(context.Background(), messageID, recipient)
}

// ClaimCoordDeliveryContext ist ClaimCoordDelivery mit Abbruch und Frist.
func (c *Client) ClaimCoordDeliveryContext(ctx context.Context, messageID int64, recipient string) (bool, error) {
	var out struct {
		Claimed bool `json:"claimed"`
	}
	err := c.doContext(ctx, "POST", "/api/coord/deliveries/claim", nil, map[string]any{
		"message_id": messageID, "recipient_external_id": recipient,
	}, &out)
	return out.Claimed, err
}

// CoordInjectedMessages nennt die schon eingebrachten unter den angefragten
// Nachrichten dieses Empfängers.
func (c *Client) CoordInjectedMessages(recipient string, messageIDs []int64) ([]int64, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	parts := make([]string, len(messageIDs))
	for i, id := range messageIDs {
		parts[i] = strconv.FormatInt(id, 10)
	}
	q := url.Values{}
	q.Set("agent_external_id", recipient)
	q.Set("message_ids", strings.Join(parts, ","))
	var out struct {
		IDs []int64 `json:"message_ids"`
	}
	err := c.do("GET", "/api/coord/deliveries/injected", q, nil, &out)
	return out.IDs, err
}
