package claudechannel

import (
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Weckregel selbst liegt in store (ShouldWake); hier nur kurze Namen.
func isOwn(self string, m store.CoordMessage) bool { return store.WakeOwn(self, m) }

func needsMentions(roomKind string) bool { return store.WakeNeedsMentions(roomKind) }

func wakeCandidate(self, roomKind string, m store.CoordMessage, now time.Time) bool {
	return store.WakeCandidate(self, roomKind, m, now)
}

func attentionIntent(m store.CoordMessage) bool { return store.WakeAttentionIntent(m) }

// ParentKind sagt, worauf eine Nachricht antwortet, soweit es für den
// Wake-Filter zählt.
type ParentKind = store.WakeParent

const (
	// ParentNotOwn: keine Antwort, oder eine Antwort auf Fremdes.
	ParentNotOwn = store.WakeParentNotOwn
	// ParentOwnRequest: Antwort auf eine eigene Anfrage (siehe ClassifyParent).
	ParentOwnRequest = store.WakeParentOwnRequest
	// ParentOwnOther: Antwort auf eine eigene Nachricht, die keine Anfrage war.
	ParentOwnOther = store.WakeParentOwnOther
)

// ClassifyParent ordnet die Ursprungsnachricht einer Antwort ein. self ist der
// Empfänger, parent die Nachricht, auf die geantwortet wird, replier der
// Absender der Antwort, parentMentions die Erwähnungen von parent.
//
// Eine Anfrage ist eine Nachricht mit Attention-Intent (Frage, Freigabe,
// Blocker, Übergabe) oder eine Nachricht, die selbst keine Antwort ist und den
// Antwortenden ausdrücklich erwähnt. In Direkt- und Gruppenräumen gibt es keine
// Erwähnungen; dort ist jede eigene Nachricht, die selbst keine Antwort ist,
// eine Anfrage. Das reply-Tool erwähnt in Raumverkehr den
// Absender automatisch; diese Erwähnung steht immer an einer Antwort und macht
// sie deshalb nie zur Anfrage. So bleibt der Schleifenschutz: eine Antwort auf
// eine Antwort (etwa ein Dank) weckt nicht, und eine Kette endet nach einer
// Antwort.
func ClassifyParent(self, roomKind string, parent store.CoordMessage, replier string, parentMentions []string) ParentKind {
	if !isOwn(self, parent) {
		return ParentNotOwn
	}
	if attentionIntent(parent) {
		return ParentOwnRequest
	}
	if parent.ReplyTo == 0 && !needsMentions(roomKind) {
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

// ShouldWake ist der Wake-Filter; die Regel steht in store.ShouldWake und
// gilt für jeden Adapter gleich.
func ShouldWake(self, roomKind string, m store.CoordMessage, mentions []string, parent ParentKind, now time.Time) bool {
	return store.ShouldWake(self, roomKind, m, mentions, parent, now)
}
