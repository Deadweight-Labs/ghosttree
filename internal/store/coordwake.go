package store

import (
	"strings"
	"time"
)

// WakeParent says what a message replies to, as far as the wake rule cares.
type WakeParent int

const (
	// WakeParentNotOwn: not a reply, or a reply to someone else's message.
	WakeParentNotOwn WakeParent = iota
	// WakeParentOwnRequest: a reply to a request of the recipient.
	WakeParentOwnRequest
	// WakeParentOwnOther: a reply to a message of the recipient that was not
	// a request, for instance an answer.
	WakeParentOwnOther
)

// WakeOwn reports whether m was sent by self or one of its named subagents
// ("<session>/<name>"). A session woken by its own message would talk to itself.
func WakeOwn(self string, m CoordMessage) bool {
	return m.SenderExternalID == self || strings.HasPrefix(m.SenderExternalID, self+"/")
}

// WakeExpired reports whether m is expired at now.
func WakeExpired(m CoordMessage, now time.Time) bool {
	if m.Expired {
		return true
	}
	if m.ExpiresAt == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, m.ExpiresAt)
	return err == nil && !at.After(now)
}

// WakeNeedsMentions says whether the room requires a mention to wake.
// Project and machine rooms are traffic for everyone and wake only whoever is
// meant. Direct and group rooms address people and wake without one.
func WakeNeedsMentions(roomKind string) bool {
	return roomKind == RoomProject || roomKind == RoomMachine
}

// WakeAttentionIntent says whether m explicitly asks for attention: question,
// approval, blocker or handoff. An ack never does.
func WakeAttentionIntent(m CoordMessage) bool {
	_, ok := attentionReasonForIntent(m.Intent)
	return ok
}

// WakeCandidate is the part of the wake rule that needs no mentions, so a
// caller can ask for mentions only once it says true. An acknowledgement
// (intent or kind "ack") is never a candidate.
func WakeCandidate(self, roomKind string, m CoordMessage, now time.Time) bool {
	if WakeOwn(self, m) || WakeExpired(m, now) {
		return false
	}
	if strings.TrimSpace(m.Intent) == IntentAck || strings.TrimSpace(m.Kind) == IntentAck {
		return false
	}
	switch roomKind {
	case RoomDirect, RoomGroup, RoomProject, RoomMachine:
		return true
	}
	return false // unknown room kind: rather do not wake
}

// WakePlainReply says whether m is a bare reply to a message of the recipient
// that was no request. Without this, two channel agents answer each other
// endlessly. Humans are exempt: no loop starts from them. The message stays
// readable by pull; it is only not pushed.
func WakePlainReply(m CoordMessage, parent WakeParent) bool {
	return m.ReplyTo != 0 && parent == WakeParentOwnOther && !WakeAttentionIntent(m) && m.AuthorKind != AuthorHuman
}

// ShouldWake is the one deterministic wake rule, shared by every adapter. It
// describes intent, not delivery: an adapter still checks recipient access,
// rate limits and loop protection. Mentions of self wake in project and
// machine rooms; direct and group rooms wake without. Never own messages,
// never acknowledgements, nothing expired. Attention intents wake even as a
// reply; a reply to an own request wakes; a reply to an own non-request does
// not. mentions are only read for rooms that need them.
func ShouldWake(self, roomKind string, m CoordMessage, mentions []string, parent WakeParent, now time.Time) bool {
	if !WakeCandidate(self, roomKind, m, now) || WakePlainReply(m, parent) {
		return false
	}
	if !WakeNeedsMentions(roomKind) {
		return true
	}
	for _, who := range mentions {
		if who == self {
			return true
		}
	}
	return false
}
