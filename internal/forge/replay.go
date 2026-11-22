package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Die Signatur eines Webhooks deckt nur den Körper, keinen Zeitstempel und
// nicht den Kopf X-GitHub-Delivery. Wer einen gültigen Aufruf mitschneidet,
// kann ihn beliebig oft senden, und die Kennung im Kopf könnte er wechseln.
// Der Handler merkt sich darum, was er schon verarbeitet hat: den Hash des
// signierten Körpers und, wenn vorhanden, die Delivery-Kennung. Eine
// Wiederholung wird beantwortet, aber nicht noch einmal ausgeführt.
//
// Die Erinnerung ist begrenzt: replayWindow lang und höchstens replayMax
// Einträge, die ältesten zuerst. Ein Neustart des Servers vergisst sie; das
// Ereignis selbst ist ohnehin idempotent (Abgleich der Repo-Liste, Stand eines
// PR), die Erinnerung nimmt nur die Wiederholung als Mittel zum Auslösen.
const (
	replayWindow = 24 * time.Hour
	replayMax    = 10000
)

type replayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
	// order hält die Schlüssel in der Reihenfolge des Merkens, für das Verdrängen.
	order []string
	now   func() time.Time
}

func newReplayGuard() *replayGuard {
	return &replayGuard{seen: map[string]time.Time{}, now: time.Now}
}

func replayKeys(body []byte, delivery string) []string {
	sum := sha256.Sum256(body)
	keys := []string{"body:" + hex.EncodeToString(sum[:])}
	if delivery != "" && len(delivery) <= 128 {
		keys = append(keys, "delivery:"+delivery)
	}
	return keys
}

// seenBefore sagt, ob einer der Schlüssel schon verarbeitet wurde.
func (g *replayGuard) seenBefore(keys []string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for _, k := range keys {
		if at, ok := g.seen[k]; ok && now.Sub(at) < replayWindow {
			return true
		}
	}
	return false
}

// remember merkt die Schlüssel eines erfolgreich verarbeiteten Aufrufs. Ein
// fehlgeschlagener wird nicht gemerkt: GitHub liefert ihn mit derselben
// Kennung noch einmal aus, und das soll wirken.
func (g *replayGuard) remember(keys []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for _, k := range keys {
		if _, ok := g.seen[k]; !ok {
			g.order = append(g.order, k)
		}
		g.seen[k] = now
	}
	for len(g.order) > replayMax || (len(g.order) > 0 && now.Sub(g.seen[g.order[0]]) >= replayWindow) {
		delete(g.seen, g.order[0])
		g.order = g.order[1:]
	}
}
