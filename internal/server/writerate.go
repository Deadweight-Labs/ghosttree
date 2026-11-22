package server

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Schreibrate je Konto. Der Store hat einen einzigen Schreiber mit einer
// gemeinsamen Warteschlange; ohne Grenze je Konto füllt ein Token, besonders
// das eines Gastes, die Schlange und alle anderen bekommen 503. Die Werte
// liegen weit über dem, was Hooks, Collector und Gärtner eines Kontos
// schreiben: dauerhaft writeRate Einheiten je Sekunde, kurzfristig writeBurst.
// Eine Anfrage kostet eine Einheit plus eine je writeByteUnit Körperbytes, damit
// große Uploads schwerer wiegen als Hook-Meldungen.
const (
	writeRate     = 50.0
	writeBurst    = 500.0
	writeByteUnit = 512 << 10
	// writeInflight ist die Zahl gleichzeitig laufender Schreibanfragen je Konto.
	writeInflight = 32
	// writeUnknownCost ist der Preis einer Anfrage ohne angekündigte Länge.
	writeUnknownCost = 8.0
	// writeIdle: so lange ohne Verkehr, dann wird der Eimer vergessen.
	writeIdle = 10 * time.Minute
)

type writeBucket struct {
	tokens   float64
	last     time.Time
	inflight int
}

// writeLimiter ist ein Token-Bucket je Konto.
type writeLimiter struct {
	mu      sync.Mutex
	buckets map[string]*writeBucket
	now     func() time.Time
	swept   time.Time
}

func newWriteLimiter() *writeLimiter {
	return &writeLimiter{buckets: map[string]*writeBucket{}, now: time.Now}
}

// writeCost ist der Preis einer Anfrage.
func writeCost(r *http.Request) float64 {
	if r.ContentLength < 0 {
		return writeUnknownCost
	}
	return 1 + math.Floor(float64(r.ContentLength)/writeByteUnit)
}

// admit nimmt eine Anfrage an oder nennt die Wartezeit. Bei Annahme muss der
// Aufrufer release rufen.
func (l *writeLimiter) admit(key string, cost float64) (release func(), wait time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	b := l.buckets[key]
	if b == nil {
		b = &writeBucket{tokens: writeBurst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(writeBurst, b.tokens+now.Sub(b.last).Seconds()*writeRate)
	b.last = now
	if cost > writeBurst {
		cost = writeBurst
	}
	if b.inflight >= writeInflight {
		return nil, time.Second, false
	}
	if b.tokens < cost {
		return nil, time.Duration((cost - b.tokens) / writeRate * float64(time.Second)), false
	}
	b.tokens -= cost
	b.inflight++
	return func() {
		l.mu.Lock()
		b.inflight--
		l.mu.Unlock()
	}, 0, true
}

// sweepLocked vergisst Eimer, die lange nichts getan haben und nichts laufen
// haben, damit die Tabelle nicht mit den Konten der Instanz wächst.
func (l *writeLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.swept) < time.Minute {
		return
	}
	l.swept = now
	for key, b := range l.buckets {
		if b.inflight == 0 && now.Sub(b.last) > writeIdle {
			delete(l.buckets, key)
		}
	}
}

// writeMethod sagt, ob eine Anfrage schreiben kann.
func writeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// limitWrites hält die Schreibrate eines Kontos ein: 429 mit Retry-After, wenn
// das Konto mehr schreibt, als es soll.
func (a *api) limitWrites(w http.ResponseWriter, r *http.Request, accountID string) (release func(), ok bool) {
	if !writeMethod(r.Method) || a.writeLimit == nil {
		return func() {}, true
	}
	release, wait, ok := a.writeLimit.admit(accountID, writeCost(r))
	if ok {
		return release, true
	}
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeCoded(w, http.StatusTooManyRequests, "rate_limited", "this account writes too fast; retry after "+strconv.Itoa(seconds)+" seconds")
	return nil, false
}
