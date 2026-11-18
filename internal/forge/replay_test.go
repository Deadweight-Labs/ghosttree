package forge

import (
	"net/http"
	"testing"
	"time"
)

// Ein mitgeschnittener, gültig signierter Aufruf wird nicht noch einmal
// ausgeführt, auch nicht mit einer anderen oder gelöschten Delivery-Kennung.
func TestWebhookDoesNotRunAReplayedDeliveryTwice(t *testing.T) {
	f := newFakeApp(t)
	var calls int
	h := f.app().WebhookHandler(func(int64, []string, []string) error { calls++; return nil }, nil)
	body := `{"action":"created","installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/new","owner":{"login":"Deadweight-Labs"}}}`
	if rec := deliver(t, h, "repository", "whsec", body); rec.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("first delivery: %d, %d calls", rec.Code, calls)
	}
	for i := 0; i < 3; i++ {
		if rec := deliver(t, h, "repository", "whsec", body); rec.Code != http.StatusNoContent {
			t.Fatalf("replay answer: %d", rec.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("the replay ran %d times, want once in all", calls)
	}
}

// Schlägt die Verarbeitung fehl, darf GitHub dieselbe Lieferung noch einmal senden.
func TestWebhookRunsAFailedDeliveryAgain(t *testing.T) {
	f := newFakeApp(t)
	var calls int
	h := f.app().WebhookHandler(func(int64, []string, []string) error {
		calls++
		if calls == 1 {
			return http.ErrAbortHandler
		}
		return nil
	}, nil)
	body := `{"action":"created","installation":{"id":5},"repository":{"full_name":"Deadweight-Labs/new","owner":{"login":"Deadweight-Labs"}}}`
	if rec := deliver(t, h, "repository", "whsec", body); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failing delivery: %d", rec.Code)
	}
	if rec := deliver(t, h, "repository", "whsec", body); rec.Code != http.StatusNoContent || calls != 2 {
		t.Fatalf("redelivery: %d, %d calls", rec.Code, calls)
	}
}

func TestReplayGuardIsBoundedAndExpires(t *testing.T) {
	g := newReplayGuard()
	now := time.Now()
	g.now = func() time.Time { return now }
	g.remember([]string{"a"})
	if !g.seenBefore([]string{"a"}) {
		t.Fatal("a remembered key is not found")
	}
	now = now.Add(replayWindow + time.Minute)
	if g.seenBefore([]string{"a"}) {
		t.Fatal("a key older than the window still counts")
	}
	for i := 0; i < replayMax+50; i++ {
		g.remember([]string{string(rune('a'+i%26)) + string(rune(i))})
	}
	if len(g.seen) > replayMax || len(g.order) > replayMax {
		t.Fatalf("memory holds %d entries, want at most %d", len(g.seen), replayMax)
	}
}
