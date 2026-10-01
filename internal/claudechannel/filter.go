package claudechannel

import (
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// isOwn erkennt die eigene Post, einschließlich der benannten Subagenten
// dieser Session (Referenz "<session>/<name>"). Eine Session, die ihre eigene
// Nachricht geweckt bekommt, schriebe sich im Kreis.
func isOwn(self string, m store.CoordMessage) bool {
	return m.SenderExternalID == self || strings.HasPrefix(m.SenderExternalID, self+"/")
}

func isExpired(m store.CoordMessage, now time.Time) bool {
	if m.Expired {
		return true
	}
	if m.ExpiresAt == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, m.ExpiresAt)
	return err == nil && !at.After(now)
}

// needsMentions sagt, ob der Raum eine Erwähnung verlangt. Projekt- und
// Maschinenraum sind Raumverkehr für alle; dort weckt nur, wer gemeint ist.
// Direkt- und Gruppenräume sind an Personen gerichtet und wecken ohne.
func needsMentions(roomKind string) bool {
	return roomKind == store.RoomProject || roomKind == store.RoomMachine
}

// wakeCandidate ist der Teil der Weckregel, der ohne Mentions auskommt. Der
// Poller fragt Erwähnungen erst, wenn er true sagt.
func wakeCandidate(self, roomKind string, m store.CoordMessage, now time.Time) bool {
	if isOwn(self, m) || isExpired(m, now) {
		return false
	}
	switch roomKind {
	case store.RoomDirect, store.RoomGroup, store.RoomProject, store.RoomMachine:
		return true
	}
	return false // unbekannte Raumart: lieber nicht wecken
}

// attentionIntent sagt, ob die Nachricht ausdrücklich Aufmerksamkeit verlangt:
// Frage, Freigabe, Blocker, Übergabe. Ein ack ist keine.
func attentionIntent(m store.CoordMessage) bool {
	switch strings.TrimSpace(m.Intent) {
	case store.IntentQuestion, store.IntentApproval, store.IntentBlocker, store.IntentHandoff:
		return true
	}
	return false
}

// isPlainReply sagt, ob die Nachricht eine bloße Antwort auf eine eigene ist
// und deshalb nicht wecken soll. Ohne das antworten sich zwei Channel-Agenten
// endlos: jede reply weckt den anderen, der wieder antwortet. Menschen sind
// ausgenommen, denn von ihnen geht keine Schleife aus. Die Nachricht bleibt im
// Pull-Pfad sichtbar; sie wird nur nicht gepusht.
func isPlainReply(m store.CoordMessage, replyToOwn bool) bool {
	return m.ReplyTo != 0 && replyToOwn && !attentionIntent(m) && m.AuthorKind != store.AuthorHuman
}

// ShouldWake ist der Wake-Filter v1: Erwähnungen der eigenen Identität sowie
// Direkt- und Gruppenräume. Nie eigene Nachrichten, nichts Abgelaufenes.
// Attention-Intents wecken erst in v2. mentions sind die Erwähnungen der
// Nachricht und werden nur für Projekt- und Maschinenräume gelesen.
// replyToOwn sagt, dass m.ReplyTo auf eine Nachricht von self zeigt; eine
// bloße Antwort darauf weckt nicht (siehe isPlainReply).
func ShouldWake(self, roomKind string, m store.CoordMessage, mentions []string, replyToOwn bool, now time.Time) bool {
	if !wakeCandidate(self, roomKind, m, now) || isPlainReply(m, replyToOwn) {
		return false
	}
	if !needsMentions(roomKind) {
		return true
	}
	for _, who := range mentions {
		if who == self {
			return true
		}
	}
	return false
}
