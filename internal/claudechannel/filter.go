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

// ParentKind sagt, worauf eine Nachricht antwortet, soweit es für den
// Wake-Filter zählt.
type ParentKind int

const (
	// ParentNotOwn: keine Antwort, oder eine Antwort auf Fremdes.
	ParentNotOwn ParentKind = iota
	// ParentOwnRequest: Antwort auf eine eigene Anfrage (siehe ClassifyParent).
	ParentOwnRequest
	// ParentOwnOther: Antwort auf eine eigene Nachricht, die keine Anfrage war.
	ParentOwnOther
)

// ClassifyParent ordnet die Ursprungsnachricht einer Antwort ein. self ist der
// Empfänger, parent die Nachricht, auf die geantwortet wird, replier der
// Absender der Antwort, parentMentions die Erwähnungen von parent.
//
// Eine Anfrage ist eine Nachricht mit Attention-Intent (Frage, Freigabe,
// Blocker, Übergabe) oder eine Nachricht, die selbst keine Antwort ist und den
// Antwortenden ausdrücklich erwähnt. Das reply-Tool erwähnt in Raumverkehr den
// Absender automatisch; diese Erwähnung steht immer an einer Antwort und macht
// sie deshalb nie zur Anfrage. So bleibt der Schleifenschutz: eine Antwort auf
// eine Antwort (etwa ein Dank) weckt nicht, und eine Kette endet nach einer
// Antwort.
func ClassifyParent(self string, parent store.CoordMessage, replier string, parentMentions []string) ParentKind {
	if !isOwn(self, parent) {
		return ParentNotOwn
	}
	if attentionIntent(parent) {
		return ParentOwnRequest
	}
	if parent.ReplyTo == 0 {
		for _, who := range parentMentions {
			if who == replier {
				return ParentOwnRequest
			}
		}
	}
	return ParentOwnOther
}

// isPlainReply sagt, ob die Nachricht eine bloße Antwort auf eine eigene ist,
// die keine Anfrage beantwortet, und deshalb nicht wecken soll. Ohne das
// antworten sich zwei Channel-Agenten endlos: jede reply weckt den anderen, der
// wieder antwortet. Menschen sind ausgenommen, denn von ihnen geht keine
// Schleife aus. Die Nachricht bleibt im Pull-Pfad sichtbar; sie wird nur nicht
// gepusht.
func isPlainReply(m store.CoordMessage, parent ParentKind) bool {
	return m.ReplyTo != 0 && parent == ParentOwnOther && !attentionIntent(m) && m.AuthorKind != store.AuthorHuman
}

// ShouldWake ist der Wake-Filter: Erwähnungen der eigenen Identität sowie
// Direkt- und Gruppenräume, nie eigene Nachrichten, nichts Abgelaufenes.
// Attention-Intents wecken auch als Antwort. Eine Antwort auf eine eigene
// Anfrage weckt (parent == ParentOwnRequest); eine Antwort auf eine eigene
// Nicht-Anfrage, etwa auf eine Antwort, weckt nicht (siehe isPlainReply).
// mentions sind die Erwähnungen der Nachricht und werden nur für Projekt- und
// Maschinenräume gelesen.
func ShouldWake(self, roomKind string, m store.CoordMessage, mentions []string, parent ParentKind, now time.Time) bool {
	if !wakeCandidate(self, roomKind, m, now) || isPlainReply(m, parent) {
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
