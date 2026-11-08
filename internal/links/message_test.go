package links

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestMessageCardLeadsToItsRoomAndCarriesNoLogin(t *testing.T) {
	b := New("https://ghost.example", "github.com/dw/p")
	c := b.MessageCard(store.MessageHit{ID: 7, RoomKey: "project:github.com/dw/p", Sender: "agent-1", Body: "Der Rollout\nist durch"})
	if c.Kind != "Message" || c.Ref != "#7" || c.Who != "agent-1" || c.Status != "in project:github.com/dw/p" || c.Title != "Der Rollout ist durch" {
		t.Fatalf("card = %+v", c)
	}
	if c.URL != "https://ghost.example/ui/coord?room=project%3Agithub.com%2Fdw%2Fp" {
		t.Fatalf("url = %s", c.URL)
	}
	if strings.Contains(strings.ToLower(c.URL), "token") {
		t.Fatal("a link must not carry a login")
	}
	// Ohne Server gibt es weder Link noch Karte.
	if u := (Builder{}).MessageCard(store.MessageHit{ID: 1, RoomKey: "project:x"}).URL; u != "" {
		t.Fatalf("url without a server = %q", u)
	}
	// Das Label des Raums ersetzt den Schlüssel in der Karte.
	if got := b.MessageCard(store.MessageHit{ID: 2, RoomKey: "group:abc", RoomLabel: "Planung"}).Status; got != "in Planung" {
		t.Fatalf("status = %q", got)
	}
}
