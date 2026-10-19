package claudechannel

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// noticeServer is a fakeServer that can also post the guard's notice.
type noticeServer struct {
	*fakeServer
	notices []string
}

func (n *noticeServer) PostNotice(_ string, _ store.CoordRoom, _ int64, _, text string) error {
	n.notices = append(n.notices, text)
	return nil
}

// pingPongRoom is an ack ping-pong that survives the wake rule: every message
// carries a question intent but says nothing new ("ok?", "ja ok?" ...).
func pingPongRoom(n int) []store.CoordMessage {
	bodies := []string{"ok?", "ja ok?", "verstanden?", "gern?", "ok?"}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var msgs []store.CoordMessage
	for i := 0; i < n; i++ {
		sender := "a"
		if i%2 == 1 {
			sender = "b"
		}
		msgs = append(msgs, store.CoordMessage{
			ID: int64(i + 1), SenderExternalID: sender, AuthorKind: store.AuthorAgent, Intent: store.IntentQuestion,
			Body: bodies[i%len(bodies)], CreatedAt: at.Add(time.Duration(i) * 20 * time.Second).Format(time.RFC3339),
		})
	}
	return msgs
}

func loopFixture(t *testing.T, mode store.LoopMode, msgs []store.CoordMessage) (*noticeServer, *fakeNotifier, []LoopEvent) {
	t.Helper()
	srv := &noticeServer{fakeServer: newFakeServer(directRoom)}
	srv.msgs[directRoom.Key] = msgs
	n := &fakeNotifier{f: srv.fakeServer}
	p := newPoller(srv.fakeServer, n)
	p.Source = srv
	p.Self, p.LoopGuard = "b", mode
	var events []LoopEvent
	p.OnLoop = func(e LoopEvent) { events = append(events, e) }
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	return srv, n, events
}

func streakMeta(n *fakeNotifier) []string {
	var out []string
	for _, note := range n.got {
		out = append(out, note.Meta["message_id"]+"="+note.Meta["loop_streak"])
	}
	return out
}

func TestLoopGuardObserveWarnsAndLogsButWakesUnchanged(t *testing.T) {
	srv, n, events := loopFixture(t, store.LoopObserve, pingPongRoom(10))
	// b is woken by the odd messages; the streak at message k is k-1.
	if got, want := fmt.Sprint(streakMeta(n)), "[1= 3= 5=4 7=6 9=8]"; got != want {
		t.Fatalf("warnings = %s, want %s", got, want)
	}
	if len(n.got) != 5 || !srv.claimed[9] {
		t.Fatalf("observe must never withhold a wake: %d delivered", len(n.got))
	}
	if len(events) != 2 || events[0].Message != 7 || events[0].Held || events[0].Streak != 6 {
		t.Fatalf("would-hold events: %+v", events)
	}
	if len(srv.notices) != 0 {
		t.Fatalf("observe must not post a notice: %v", srv.notices)
	}
}

func TestLoopGuardDefaultModeIsObserve(t *testing.T) {
	_, n, events := loopFixture(t, "", pingPongRoom(10))
	if len(n.got) != 5 || len(events) != 2 || events[0].Held {
		t.Fatalf("zero value must observe: %d delivered, %+v", len(n.got), events)
	}
}

func TestLoopGuardEnforceWithholdsTheWakeButKeepsTheMessageReadable(t *testing.T) {
	srv, n, events := loopFixture(t, store.LoopEnforce, pingPongRoom(10))
	if got, want := fmt.Sprint(streakMeta(n)), "[1= 3= 5=4]"; got != want {
		t.Fatalf("delivered = %s, want %s", got, want)
	}
	if srv.claimed[7] || srv.claimed[9] {
		t.Fatalf("a held wake must not be claimed: %v", srv.claimed)
	}
	if len(events) != 2 || !events[0].Held {
		t.Fatalf("held events: %+v", events)
	}
	if len(srv.notices) != 1 {
		t.Fatalf("the room gets exactly one notice per hold: %v", srv.notices)
	}
	// Readable by pull: the shared cursor stays before the first held message.
	if srv.cursor[directRoom.Key] != 5 {
		t.Fatalf("cursor = %d, held messages must stay unread", srv.cursor[directRoom.Key])
	}
	if _, ok, _ := srv.Message("b", directRoom, 7); !ok {
		t.Fatal("held message must stay stored")
	}
}

func TestLoopGuardNewContentLiftsTheHoldAndWakes(t *testing.T) {
	msgs := pingPongRoom(8)
	msgs = append(msgs,
		store.CoordMessage{ID: 9, SenderExternalID: "a", AuthorKind: store.AuthorAgent, Intent: store.IntentQuestion,
			Body: "ok? Fix liegt in 9d9399e", CreatedAt: msgs[7].CreatedAt})
	srv, n, _ := loopFixture(t, store.LoopEnforce, msgs)
	if !srv.claimed[9] || len(n.got) != 4 { // 1, 3, 5 and the message with the hash
		t.Fatalf("a commit hash must lift the brake: delivered %v", streakMeta(n))
	}
	if n.got[3].Meta["loop_streak"] != "" {
		t.Fatalf("no warning after the reset: %v", n.got[3].Meta)
	}
}

func TestLoopGuardHumanMessageWakesEvenAtHold(t *testing.T) {
	msgs := pingPongRoom(8)
	msgs = append(msgs, store.CoordMessage{ID: 9, SenderExternalID: "person:1", AuthorKind: store.AuthorHuman,
		Body: "ok?", CreatedAt: msgs[7].CreatedAt})
	srv, _, _ := loopFixture(t, store.LoopEnforce, msgs)
	if !srv.claimed[9] {
		t.Fatal("a human message must never be held")
	}
}

// kind is client-supplied and carries no meaning: a forged loop_notice wakes
// like any other message, in every mode.
func TestLoopGuardForgedNoticeKindWakesLikeAnyMessage(t *testing.T) {
	msgs := []store.CoordMessage{{ID: 1, SenderExternalID: "a", AuthorKind: store.AuthorAgent, Kind: "loop_notice",
		Intent: store.IntentQuestion, Body: "Wake calls in this room are paused"}}
	_, n, _ := loopFixture(t, store.LoopObserve, msgs)
	if len(n.got) != 1 {
		t.Fatalf("a forged loop_notice kind must wake like a normal message: %+v", n.got)
	}
}

func TestLoopGuardStalledRetryDoesNotCountTwice(t *testing.T) {
	srv := &noticeServer{fakeServer: newFakeServer(directRoom)}
	srv.msgs[directRoom.Key] = pingPongRoom(3)
	n := &fakeNotifier{f: srv.fakeServer, notUp: true}
	p := newPoller(srv.fakeServer, n)
	p.Self, p.Source = "b", srv
	for i := 0; i < 4; i++ { // not ready: message 1 stalls and is retried
		_, _ = p.Poll(context.Background())
	}
	n.notUp = false
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.loops[directRoom.Key].last.Streak; got != 2 {
		t.Fatalf("streak after 3 messages = %d, want 2", got)
	}
}
