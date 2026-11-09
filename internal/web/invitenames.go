package web

import (
	"sync"
	"time"
)

const (
	// inviteNameMisses: so viele abgelehnte Namen verträgt eine Einladung, dann
	// ist sie für Namensversuche gesperrt.
	inviteNameMisses = 5
	// inviteNameLockFor: so lange bleibt sie gesperrt. Die Einladung selbst bleibt
	// gültig; wer nur vertippt hat, versucht es danach noch einmal.
	inviteNameLockFor = 15 * time.Minute
	// inviteNameTrackMax begrenzt die Tabelle. Aufgenommen werden nur Codes, die
	// eine echte Einladung sind.
	inviteNameTrackMax = 10000
)

type inviteNameCount struct {
	misses int
	until  time.Time // gesperrt bis; Null: nicht gesperrt
	last   time.Time
}

// inviteNameGuard zählt abgelehnte Namen je Einladungscode. Die Namensprüfung
// der Einladungsseite verrät sonst, welche Kontonamen die Instanz kennt. Anders
// als joinLimiter gehört der Zähler dem Code: wer ihn sperren kann, hält den Code
// ohnehin in der Hand.
type inviteNameGuard struct {
	mu     sync.Mutex
	now    func() time.Time
	counts map[string]*inviteNameCount
}

func newInviteNameGuard() *inviteNameGuard {
	return &inviteNameGuard{now: time.Now, counts: map[string]*inviteNameCount{}}
}

// locked sagt, ob für den Code gerade keine Namen mehr geprüft werden.
func (g *inviteNameGuard) locked(code string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.counts[code]
	if c == nil || c.until.IsZero() {
		return false
	}
	if g.now().Before(c.until) {
		return true
	}
	delete(g.counts, code) // Sperre abgelaufen: neues Konto an Versuchen
	return false
}

// miss zählt einen abgelehnten Namen und gibt zurück, ob der Code damit gesperrt ist.
func (g *inviteNameGuard) miss(code string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	at := g.now()
	c := g.counts[code]
	if c == nil {
		if len(g.counts) >= inviteNameTrackMax {
			g.evict(at)
		}
		c = &inviteNameCount{}
		g.counts[code] = c
	}
	if !c.last.IsZero() && at.Sub(c.last) >= inviteNameLockFor {
		c.misses = 0 // alte Fehlversuche zählen nicht ewig
	}
	c.misses++
	c.last = at
	if c.misses >= inviteNameMisses {
		c.until = at.Add(inviteNameLockFor)
		return true
	}
	return false
}

// accepted vergisst die Fehlversuche nach einem Beitritt.
func (g *inviteNameGuard) accepted(code string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.counts, code)
}

// evict wirft abgelaufene Sperren und alte Zähler raus, sonst den ältesten.
func (g *inviteNameGuard) evict(at time.Time) {
	var oldest string
	var oldestAt time.Time
	for k, c := range g.counts {
		if (!c.until.IsZero() && !at.Before(c.until)) || at.Sub(c.last) >= inviteNameLockFor {
			delete(g.counts, k)
			continue
		}
		if oldest == "" || c.last.Before(oldestAt) {
			oldest, oldestAt = k, c.last
		}
	}
	if len(g.counts) >= inviteNameTrackMax && oldest != "" {
		delete(g.counts, oldest)
	}
}
