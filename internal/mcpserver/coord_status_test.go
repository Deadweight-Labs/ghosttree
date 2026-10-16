package mcpserver

import (
	"context"
	"strings"
	"testing"
)

// coord_status setzt die Arbeitsnotiz; die anderen sehen sie in coord_peers, als
// selbstgemeldet gekennzeichnet, zusammen mit Maschine und Branch, und neben der
// Nachricht des Absenders.
func TestCoordStatusShowsInPeersAndNextToMessagesAsSelfReported(t *testing.T) {
	a, b, _ := twoSessions(t)
	ctx := context.Background()
	a.ctxAxes.Branch = "feat/mac-sync"

	res, _, err := a.handleCoordStatus(ctx, nil, CoordStatusInput{Note: "PR #77 feat/mac-sync,\nroom sync"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if got := text(t, res); !strings.Contains(got, "PR #77 feat/mac-sync, room sync") {
		t.Fatalf("status answer = %q", got)
	}
	if _, _, err := a.handleCoordSend(ctx, nil, CoordSendInput{Body: "ich nehme #77"}); err != nil {
		t.Fatal(err)
	}

	peers, _, err := b.handleCoordPeers(ctx, nil, CoordPeersInput{})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, peers)
	for _, want := range []string{"mainex", "feat/mac-sync", "working on (self-reported, unverified): PR #77 feat/mac-sync, room sync"} {
		if !strings.Contains(got, want) {
			t.Errorf("peers lacks %q:\n%s", want, got)
		}
	}
	inbox, _, err := b.handleCoordInbox(ctx, nil, CoordInboxInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, inbox); !strings.Contains(got, "[working on, self-reported: PR #77 feat/mac-sync, room sync]") || !strings.Contains(got, "mainex") {
		t.Errorf("sender block lacks label or note:\n%s", got)
	}

	if _, _, err := a.handleCoordStatus(ctx, nil, CoordStatusInput{}); err != nil {
		t.Fatal(err)
	}
	peers, _, _ = b.handleCoordPeers(ctx, nil, CoordPeersInput{})
	if strings.Contains(text(t, peers), "working on") {
		t.Errorf("note not cleared:\n%s", text(t, peers))
	}
	if _, _, err := a.handleCoordStatus(ctx, nil, CoordStatusInput{Note: "token ghp_Ab1Ab1Ab1Ab1Ab1"}); err == nil {
		t.Error("a secret in the note must be refused")
	}
}
