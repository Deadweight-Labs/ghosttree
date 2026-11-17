package store

import (
	"errors"
	"slices"
	"testing"
)

// REQ-688 (N15, N06, N26): a question needs no explicit addressee. An agent
// that names nobody asks the person it works for; an @name in the text wins;
// and nobody ever addresses themselves.
func TestAgentQuestionWithoutAddresseeGoesToItsOwner(t *testing.T) {
	e := authorityFixture(t)
	anna := e.agent("a-anna") // works for person:2
	id, err := anna.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "q-owner",
		Body: "Postgres oder SQLite?", Intent: IntentQuestion})
	if err != nil {
		t.Fatalf("question without addressee: %v", err)
	}
	if got := storedMentions(t, e, id); !slices.Equal(got, []string{"person:2"}) {
		t.Fatalf("a question without addressee goes to the owner, got %v", got)
	}
	items, err := e.human("person:2").Attention()
	if err != nil || len(items) != 1 || items[0].MessageID != id {
		t.Fatalf("the owner must find the question waiting: %+v %v", items, err)
	}
}

func TestAgentQuestionNamingItselfReachesTheOthersOnly(t *testing.T) {
	e := authorityFixture(t)
	anna := e.agent("a-anna")
	id, err := anna.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "q-self",
		Body: "@anna und @ben: Soll das Timeout 30 oder 60 Sekunden sein?", Intent: IntentQuestion, Mentions: []string{"a-anna"}})
	if err != nil {
		t.Fatal(err)
	}
	got := storedMentions(t, e, id)
	if slices.Contains(got, "a-anna") {
		t.Fatalf("the sender is in its own addressees: %v", got)
	}
	if !slices.Contains(got, "person:3") {
		t.Fatalf("@ben must be an addressee: %v", got)
	}
}

func TestHumanQuestionStillNeedsAnAddressee(t *testing.T) {
	e := authorityFixture(t)
	if _, err := e.human("person:2").Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: e.room, ClientID: "q-human",
		Body: "Wer macht das?", Intent: IntentQuestion}); !errors.Is(err, ErrAttentionRecipientRequired) {
		t.Fatalf("human question without addressee: %v", err)
	}
}
