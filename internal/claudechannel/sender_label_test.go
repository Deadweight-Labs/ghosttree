package claudechannel

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// An agent sender carries its label and its self-reported note; a human sender
// never does, so a person's label cannot be forged through the agent fields.
func TestNotificationCarriesAgentLabelAndNoteButNotForHumans(t *testing.T) {
	agent := store.CoordMessage{ID: 1, SenderExternalID: "a-1", AuthorKind: store.AuthorAgent,
		SenderLabel: "anna · mac-lab · feat/x", SenderWorkNote: "PR #77"}
	meta := NewNotification(store.CoordRoom{Key: "r", Kind: "project"}, agent, "hi").Meta
	if meta["sender_label"] != "anna · mac-lab · feat/x" || meta["sender_work_note"] != "PR #77" {
		t.Errorf("meta = %v", meta)
	}
	human := agent
	human.AuthorKind = store.AuthorHuman
	meta = NewNotification(store.CoordRoom{Key: "r", Kind: "project"}, human, "hi").Meta
	if _, ok := meta["sender_label"]; ok {
		t.Errorf("a human sender must not carry an agent label: %v", meta)
	}
}
