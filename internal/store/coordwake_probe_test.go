package store

import (
	"strings"
	"testing"
	"time"
)

// oldShouldWake is a frozen copy of the wake rule as it was on origin/dev
// before the rules were merged (claudechannel.ShouldWake with its helpers).
// It must not be edited: it is the reference the probe table compares against.
func oldShouldWake(self, roomKind string, m CoordMessage, mentions []string, parent WakeParent, now time.Time) bool {
	own := m.SenderExternalID == self || strings.HasPrefix(m.SenderExternalID, self+"/")
	expired := m.Expired
	if !expired && m.ExpiresAt != "" {
		at, err := time.Parse(time.RFC3339, m.ExpiresAt)
		expired = err == nil && !at.After(now)
	}
	if own || expired {
		return false
	}
	switch roomKind {
	case RoomDirect, RoomGroup, RoomProject, RoomMachine:
	default:
		return false
	}
	intent := false
	switch strings.TrimSpace(m.Intent) {
	case IntentQuestion, IntentApproval, IntentBlocker, IntentHandoff:
		intent = true
	}
	if m.ReplyTo != 0 && parent == WakeParentOwnOther && !intent && m.AuthorKind != AuthorHuman {
		return false
	}
	if roomKind == RoomDirect || roomKind == RoomGroup {
		return true
	}
	for _, who := range mentions {
		if who == self {
			return true
		}
	}
	return false
}

// TestWakeRuleDeviatesFromOldRuleOnlyForPlainAgentAck probes every combination
// of the rule's inputs and allows exactly one deviation from the old rule:
// a plain agent ack (not a reply, no attention intent) in a direct or group
// room stops waking.
func TestWakeRuleDeviatesFromOldRuleOnlyForPlainAgentAck(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute).Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)
	rooms := []string{RoomDirect, RoomGroup, RoomProject, RoomMachine, "weird", ""}
	senders := []string{"peer", "me", "me/sub", "me2"}
	intents := []string{"", IntentAck, " ack ", IntentQuestion, IntentApproval, IntentBlocker, IntentHandoff, "other"}
	kinds := []string{"", "ack", " ack ", "chat"}
	replies := []int64{0, 99}
	authors := []string{"", AuthorHuman, "agent"}
	expiries := []struct {
		at      string
		expired bool
	}{{"", false}, {past, false}, {future, false}, {"", true}}
	mentionSets := [][]string{nil, {"other"}, {"me"}, {"other", "me"}}
	parents := []WakeParent{WakeParentNotOwn, WakeParentOwnRequest, WakeParentOwnOther}

	var total, deviations int
	for _, room := range rooms {
		for _, sender := range senders {
			for _, intent := range intents {
				for _, kind := range kinds {
					for _, reply := range replies {
						for _, author := range authors {
							for _, ex := range expiries {
								for _, mentions := range mentionSets {
									for _, parent := range parents {
										m := CoordMessage{ID: 1, SenderExternalID: sender, Intent: intent, Kind: kind,
											ReplyTo: reply, AuthorKind: author, ExpiresAt: ex.at, Expired: ex.expired}
										oldGot := oldShouldWake("me", room, m, mentions, parent, now)
										newGot := ShouldWake("me", room, m, mentions, parent, now)
										total++
										if oldGot == newGot {
											continue
										}
										deviations++
										isAck := strings.TrimSpace(intent) == IntentAck || strings.TrimSpace(kind) == IntentAck
										intended := oldGot && !newGot && isAck && reply == 0 && author != AuthorHuman &&
											(room == RoomDirect || room == RoomGroup) &&
											!WakeAttentionIntent(m)
										if !intended {
											t.Fatalf("unintended deviation old=%v new=%v room=%q sender=%q intent=%q kind=%q reply=%d author=%q expires=%q expired=%v mentions=%v parent=%d",
												oldGot, newGot, room, sender, intent, kind, reply, author, ex.at, ex.expired, mentions, parent)
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if deviations == 0 {
		t.Fatal("the intended deviation never occurred; the probe is not exercising the ack case")
	}
	t.Logf("%d combinations, %d intended deviations", total, deviations)
}
