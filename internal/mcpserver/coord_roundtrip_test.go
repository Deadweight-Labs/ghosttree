package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"net/http/httptest"
)

// twoSessions baut zwei MCP-Server, die denselben Ghosttree-Server benutzen
// und sich nur in Session-Referenz und Harness unterscheiden. Genau das ist
// der Fall, um den es geht: keiner hat den anderen gestartet, keiner ist
// Parent, sie teilen nur Projektzustand.
func twoSessions(t *testing.T) (*Server, *Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)

	newSession := func(ref, machine string) *Server {
		c := client.New(config.Config{ServerURL: srv.URL, Token: token, Machine: machine})
		return &Server{client: c, sessionRef: ref,
			ctxAxes: scope.Axes{Project: "github.com/deadweight-labs/ghosttree", Machine: machine}}
	}
	return newSession("sess-claude", "mainex"), newSession("sess-codex", "mainex"), st
}

// AC-1 und AC-2 von REQ-350 in der kleinsten Form, die sie wirklich prüft:
// zwei unabhängig angemeldete Sessions sehen einander und tauschen eine
// Nachricht aus, ohne dass ein Mensch zwischen Terminals kopiert.
func TestTwoIndependentSessionsExchangeAMessage(t *testing.T) {
	a, b, _ := twoSessions(t)
	ctx := context.Background()

	if _, _, err := a.handleCoordSend(ctx, nil, CoordSendInput{
		Body: "ich ändere die Pagination-Antwort, items bleibt"}); err != nil {
		t.Fatalf("a sends: %v", err)
	}

	res, _, err := b.handleCoordInbox(ctx, nil, CoordInboxInput{})
	if err != nil {
		t.Fatalf("b reads: %v", err)
	}
	if !strings.Contains(text(t, res), "items bleibt") {
		t.Fatalf("b did not receive a's message: %s", text(t, res))
	}

	// Und die eigene Nachricht ist keine Post an sich selbst.
	own, _, err := a.handleCoordInbox(ctx, nil, CoordInboxInput{})
	if err != nil {
		t.Fatalf("a reads: %v", err)
	}
	if strings.Contains(text(t, own), "items bleibt") {
		t.Fatal("a received its own message back")
	}
}

// AC-8: der Cursor überlebt, und ein zweiter Abruf wiederholt nichts. Ohne
// das liest ein Agent nach jedem Werkzeugaufruf denselben Raum von vorn und
// hält alte Bitten für neue.
func TestInboxDoesNotRepeatItself(t *testing.T) {
	a, b, _ := twoSessions(t)
	ctx := context.Background()

	if _, _, err := a.handleCoordSend(ctx, nil, CoordSendInput{Body: "einmalig"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	first, _, err := b.handleCoordInbox(ctx, nil, CoordInboxInput{})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if !strings.Contains(text(t, first), "einmalig") {
		t.Fatalf("first read missed the message: %s", text(t, first))
	}
	second, _, err := b.handleCoordInbox(ctx, nil, CoordInboxInput{})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if strings.Contains(text(t, second), "einmalig") {
		t.Fatalf("the same message was served twice: %s", text(t, second))
	}
}

// AC-6: zwei Sessions aus verschiedenen Unterverzeichnissen desselben Repos
// treffen sich, und die Teilnehmerliste sagt nichts über Erreichbarkeit.
func TestPeersMeetInTheProjectRoomAndReachabilityIsNotClaimed(t *testing.T) {
	a, b, _ := twoSessions(t)
	ctx := context.Background()

	for _, s := range []*Server{a, b} {
		key, err := s.roomKeyFor("project")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.client.RegisterCoordAgent(store.CoordAgent{
			ExternalID: s.sessionRef, Provider: "test", RoomKey: key,
			DisplayName: s.sessionRef, Branch: "feat/coordination-threads"}); err != nil {
			t.Fatalf("register %s: %v", s.sessionRef, err)
		}
	}

	res, _, err := a.handleCoordPeers(ctx, nil, CoordPeersInput{})
	if err != nil {
		t.Fatalf("peers: %v", err)
	}
	got := text(t, res)
	if !strings.Contains(got, "sess-codex") {
		t.Fatalf("a does not see b: %s", got)
	}
	if strings.Contains(got, "sess-claude (test)") {
		t.Fatalf("a lists itself as a peer: %s", got)
	}
	if !strings.Contains(got, "not a promise") {
		t.Fatalf("the peer list claims reachability it cannot know: %s", got)
	}
}

// Spec §11: eine abgelaufene Meldung bleibt lesbar und wird als Geschichte
// gekennzeichnet. Ohne die Markierung löst ein Neustart-Hinweis von gestern
// heute einen Neustart aus.
func TestExpiredNoticeArrivesMarkedAsHistory(t *testing.T) {
	a, b, _ := twoSessions(t)
	ctx := context.Background()

	if _, _, err := a.handleCoordSend(ctx, nil, CoordSendInput{
		Body:    "Postgres startet in 10 Sekunden neu",
		Expires: "2020-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	res, _, err := b.handleCoordInbox(ctx, nil, CoordInboxInput{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := text(t, res)
	if !strings.Contains(got, "Postgres") {
		t.Fatalf("an expired notice must stay readable: %s", got)
	}
	if !strings.Contains(got, "expired") {
		t.Fatalf("an expired notice must be marked, not served as current: %s", got)
	}
}

// Der Maschinenraum erreicht auch eine Session ohne Repository und behauptet
// nichts über andere Maschinen. Spec §A1.
func TestMachineRoomReachesAnAgentWithoutARepository(t *testing.T) {
	a, _, st := twoSessions(t)
	ctx := context.Background()
	token, _ := st.AddPerson("robin-2")
	_ = token

	// Eine Session ohne Projekt, aber auf derselben Maschine.
	homeless := &Server{client: a.client, sessionRef: "sess-nogit",
		ctxAxes: scope.Axes{Machine: "mainex"}}

	if _, _, err := a.handleCoordSend(ctx, nil, CoordSendInput{
		Room: "machine", Body: "ich habe den lokalen Postgres neu gestartet"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	res, _, err := homeless.handleCoordInbox(ctx, nil, CoordInboxInput{Room: "machine"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(text(t, res), "Postgres") {
		t.Fatalf("the machine room did not reach an agent without a repository: %s", text(t, res))
	}

	// Und die Meldung ist nicht im Projektraum gelandet.
	inProject, _, err := a.handleCoordInbox(ctx, nil, CoordInboxInput{Room: "project"})
	if err != nil {
		t.Fatalf("project read: %v", err)
	}
	if strings.Contains(text(t, inProject), "Postgres") {
		t.Fatalf("a machine-wide notice leaked into the project room: %s", text(t, inProject))
	}
}
