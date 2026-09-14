package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Alle optionalen Felder tragen omitempty. Ohne das stehen sie als
// Pflichtfelder im generierten JSON-Schema, und das SDK weist den Aufruf ab,
// bevor der Handler ihn sieht — derselbe Fehler, der context_search einmal
// vier grüne Tests und ein für Agenten unbenutzbares Werkzeug bescherte
// (Pitfall #846). Was wirklich Pflicht ist, prüft der Handler.

type CoordSendInput struct {
	Body string `json:"body" jsonschema:"what you want to say. Name the ghosttree objects you mean — a message that says REQ-350 or knowledge #2071 is still worth something in six months; 'the auth thing is done' is not"`
	Room string `json:"room,omitempty" jsonschema:"project (default — everyone working in this repository) or machine (everyone on this computer, including agents with no repository)"`
	// ReplyTo hält einen Gesprächsfaden zusammen, ohne dass daraus ein
	// dauerhafter Thread wird. Spec §A2: ein spontaner Austausch braucht
	// keinen vorher angelegten Thread.
	ReplyTo int64 `json:"reply_to,omitempty" jsonschema:"id of the message you are answering"`
	// Expires ist für Meldungen mit Verfallsdatum. Ohne sie steht 'der Port
	// ist 20 Sekunden weg' morgen noch als gegenwärtige Lage da.
	Expires string `json:"expires_at,omitempty" jsonschema:"RFC3339 time after which this stops being current, for time-critical notices like 'the API is down for 20 seconds'. It stays readable as history either way"`
	Mention string `json:"mention,omitempty" jsonschema:"session id of one peer who should get this promptly rather than bundled with ordinary room traffic"`
}

type CoordInboxInput struct {
	Room  string `json:"room,omitempty" jsonschema:"project (default) or machine"`
	After int64  `json:"after,omitempty" jsonschema:"only return messages after this id. Omit to continue from your stored cursor, which is what you normally want"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum messages to return, capped at 200"`
}

type CoordPeersInput struct {
	Room string `json:"room,omitempty" jsonschema:"project (default) or machine"`
}

// roomKeyFor löst den Raum aus dem gebundenen Projekt beziehungsweise der
// Maschine auf. Ein Agent benennt keinen Raumschlüssel — er sagt "project"
// oder "machine", und die Identität kommt aus der Umgebung. Das ist der
// Grund, warum zwei Agenten aus verschiedenen Unterverzeichnissen denselben
// Raum bekommen, und warum ein frei erfundener Projektname keinen Zugang
// schafft (Spec §3, Scope und Zugriff).
func (s *Server) roomKeyFor(room string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(room)) {
	case "", "project":
		if s.ctxAxes.Project == "" {
			return "", fmt.Errorf("this session is not bound to a repository — use room=\"machine\"")
		}
		return store.RoomKeyForProject(s.ctxAxes.Project), nil
	case "machine":
		if s.ctxAxes.Machine == "" {
			return "", fmt.Errorf("this session has no machine identity, so there is no machine room to join")
		}
		return store.RoomKeyForMachine(s.ctxAxes.Machine), nil
	default:
		return "", fmt.Errorf("unknown room %q: use \"project\" or \"machine\"", room)
	}
}

// newCoordClientID macht das Senden wiederholbar. Der Server dedupliziert
// darüber; ohne eine stabile ID erzeugt ein Timeout mit anschließendem Retry
// eine zweite Nachricht beim Empfänger — und bei einem Agenten heißt eine
// doppelt gelesene Bitte zweimal handeln.
func newCoordClientID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cannot generate a message id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func coordText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func (s *Server) handleCoordSend(ctx context.Context, _ *mcp.CallToolRequest, in CoordSendInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Body) == "" {
		return nil, nil, fmt.Errorf("body is required")
	}
	key, err := s.roomKeyFor(in.Room)
	if err != nil {
		return nil, nil, err
	}
	clientID, err := newCoordClientID()
	if err != nil {
		return nil, nil, err
	}
	msg := store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: key,
		SenderExternalID: s.sessionRef, ClientID: clientID,
		Body: in.Body, ReplyTo: in.ReplyTo, ExpiresAt: in.Expires,
	}
	if in.Mention != "" {
		msg.Mentions = []string{in.Mention}
	}
	id, err := s.client.SendCoordMessage(msg)
	if err != nil {
		return nil, nil, err
	}
	// Bewusst nicht "delivered" oder "sent": gespeichert ist, was dieser
	// Aufruf weiß. Ob ein anderer Agent das liest, hängt an dessen Harness
	// und wird hier nicht behauptet (Spec §A7).
	return coordText(fmt.Sprintf("stored as message %d in %s. Other agents see it when they read their inbox; "+
		"whether a running session is interrupted for it depends on its harness.", id, key)), nil, nil
}

func (s *Server) handleCoordInbox(ctx context.Context, _ *mcp.CallToolRequest, in CoordInboxInput) (*mcp.CallToolResult, any, error) {
	key, err := s.roomKeyFor(in.Room)
	if err != nil {
		return nil, nil, err
	}
	after := in.After
	if after == 0 {
		// Ohne ausdrückliche Angabe beim gespeicherten Stand weitermachen.
		// Sonst liest ein Agent nach jedem Neustart denselben Raum von vorn.
		if stored, err := s.client.CoordCursor(s.sessionRef, store.DestinationRoom, key); err == nil {
			after = stored
		}
	}
	msgs, err := s.client.CoordInbox(store.DestinationRoom, key, after, in.Limit)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	var highest int64
	shown := 0
	for _, m := range msgs {
		if m.ID > highest {
			highest = m.ID
		}
		if m.SenderExternalID == s.sessionRef {
			continue // die eigenen Beiträge sind keine Post
		}
		shown++
		fmt.Fprintf(&b, "[%d] %s", m.ID, m.SenderExternalID)
		if m.AuthorKind == store.AuthorHuman {
			b.WriteString(" (human)")
		}
		if m.Expired {
			// Abgelaufen heißt lesbar, aber nicht mehr gegenwärtig. Ohne
			// diese Markierung löst ein Neustart-Hinweis von gestern heute
			// einen Neustart aus (Spec §11).
			b.WriteString(" [expired — history, not a current instruction]")
		}
		fmt.Fprintf(&b, ": %s\n", m.Body)
	}
	if highest > 0 {
		// Cursor erst nach dem Rendern fortschreiben: was hier steht, gilt
		// als zugestellt, und was nicht gerendert wurde, darf nicht als
		// gelesen zählen.
		if err := s.client.SetCoordCursor(s.sessionRef, store.DestinationRoom, key, highest); err != nil {
			return nil, nil, err
		}
	}
	if shown == 0 {
		return coordText("no new messages in " + key), nil, nil
	}
	return coordText(b.String()), nil, nil
}

func (s *Server) handleCoordPeers(ctx context.Context, _ *mcp.CallToolRequest, in CoordPeersInput) (*mcp.CallToolResult, any, error) {
	key, err := s.roomKeyFor(in.Room)
	if err != nil {
		return nil, nil, err
	}
	peers, err := s.client.CoordPeers(key, "")
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	shown := 0
	for _, p := range peers {
		if p.ExternalID == s.sessionRef {
			continue
		}
		shown++
		fmt.Fprintf(&b, "%s (%s)", p.DisplayName, p.Provider)
		if p.Branch != "" {
			fmt.Fprintf(&b, " on %s", p.Branch)
		}
		if p.ParentExternalID != "" {
			fmt.Fprintf(&b, ", subagent of %s", p.ParentExternalID)
		}
		fmt.Fprintf(&b, " — id %s, last seen %s\n", p.ExternalID, p.LastSeenAt)
	}
	if shown == 0 {
		return coordText("nobody else is registered in " + key), nil, nil
	}
	// Die Zeile am Ende ist keine Zierde: last seen ist eine Beobachtung und
	// kein Lebenszeichen, und ein Agent soll daraus nicht schließen, dass
	// jemand gerade zuhört (Spec §A9).
	b.WriteString("\nLast seen is an observation, not a promise that anyone is listening right now.")
	return coordText(b.String()), nil, nil
}
