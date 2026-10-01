package web

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const coordEventPollInterval = 250 * time.Millisecond

// sessionRecheckInterval begrenzt, wie oft ein offener Stream Token und Konto
// gegen die Datenbank prüft. Variable, damit ein Test es verkürzen kann.
var sessionRecheckInterval = 20 * time.Second

var coordCursorAAD = []byte("ghosttree coord event cursor v1")

// coordCursors liefert den Schlüssel, der Ereignis-Cursor versiegelt. Er lebt
// nur im Prozess (wie der Flow-Schlüssel des OIDC-Logins); nach einem Neustart
// werden alte Cursor als ungültig behandelt und lösen einen Resync aus.
func (a *app) coordCursors() *flowSealer {
	a.cursorOnce.Do(func() {
		a.cursorSeal, _ = newFlowSealer()
	})
	return a.cursorSeal
}

// sealCoordCursor macht aus der globalen Ereignisfolge einen undurchsichtigen
// Wert. Die Nummer selbst wäre ein Seitenkanal: Lücken zwischen den eigenen
// Beiträgen zeigten, wie viele für andere sichtbare Ereignisse (etwa eine
// Zustellung an ein Mitglied) dazwischen lagen. Jeder Leser bekommt die
// versiegelte Form, damit es keinen Zweig gibt, an dem man ihn erkennt; die
// Nonce ist zufällig, zwei Cursor für dieselbe Folge sind nicht vergleichbar.
func (a *app) sealCoordCursor(principal store.Principal, seq int64) string {
	sealer := a.coordCursors()
	if sealer == nil {
		return ""
	}
	plain := make([]byte, 8)
	binary.BigEndian.PutUint64(plain, uint64(seq))
	nonce := make([]byte, sealer.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(sealer.aead.Seal(nonce, nonce, plain, append(append([]byte{}, coordCursorAAD...), principal.ID...)))
}

// openCoordCursor öffnet einen Cursor. ok=false heißt: ungültig, manipuliert
// oder von einem anderen Konto; der Aufrufer antwortet dann wie bei einem zu
// alten Cursor (Resync) und nie mit einem Fehler, der etwas verrät. Leer und
// "0" sind der Anfang.
func (a *app) openCoordCursor(principal store.Principal, value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" {
		return 0, true
	}
	sealer := a.coordCursors()
	if sealer == nil {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < sealer.aead.NonceSize() {
		return 0, false
	}
	plain, err := sealer.aead.Open(nil, raw[:sealer.aead.NonceSize()], raw[sealer.aead.NonceSize():], append(append([]byte{}, coordCursorAAD...), principal.ID...))
	if err != nil || len(plain) != 8 {
		return 0, false
	}
	seq := binary.BigEndian.Uint64(plain)
	if seq > math.MaxInt64 {
		return 0, false
	}
	return int64(seq), true
}

func coordCursorParam(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if value == "" {
		value = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	return value
}

func (a *app) coordEvents(w http.ResponseWriter, r *http.Request) {
	principal := browserPrincipal(r)
	after, cursorOK := a.openCoordCursor(principal, coordCursorParam(r))
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "browser session missing", http.StatusUnauthorized)
		return
	}
	var replay store.CoordEventReplay
	if cursorOK {
		replay, err = a.store.CoordEventsAfter(principal, after, 100)
	}
	if !cursorOK || errors.Is(err, store.ErrCoordEventCursor) {
		// Ungültig, manipuliert oder aus der Zukunft: wie ein zu alter Cursor.
		// Die Seite lädt neu und bekommt einen frischen.
		latest, latestErr := a.store.LatestCoordEventSequence()
		if latestErr != nil {
			http.Error(w, "coordination stream unavailable", http.StatusInternalServerError)
			return
		}
		replay, err = store.CoordEventReplay{Resync: true, Latest: latest, ScannedThrough: latest}, nil
	}
	if err != nil {
		http.Error(w, "coordination stream unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(w, "retry: 1000\n\n")
	if err := a.writeCoordReplay(w, flusher, principal, replay); err != nil {
		return
	}
	if replay.Resync {
		after = replay.Latest
	} else if replay.ScannedThrough > after {
		after = replay.ScannedThrough
	}
	flusher.Flush()

	poll := time.NewTicker(coordEventPollInterval)
	defer poll.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	lastRecheck := time.Now()
	for {
		if replay.ScannedThrough < replay.Latest && !replay.Resync {
			// Drain a backlog without waiting for another poll tick.
		} else {
			select {
			case <-r.Context().Done():
				return
			case <-keepalive.C:
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return
				}
				flusher.Flush()
				continue
			case <-poll.C:
			}
		}
		_, live := a.sessions.get(cookie.Value)
		if live && time.Since(lastRecheck) >= sessionRecheckInterval {
			lastRecheck = time.Now()
			if !a.store.PrincipalValid(principal) {
				a.sessions.remove(cookie.Value)
				live = false
			}
		}
		if !live {
			_, _ = fmt.Fprint(w, "event: session-ended\ndata: {}\n\n")
			flusher.Flush()
			return
		}
		replay, err = a.store.CoordEventsAfter(principal, after, 100)
		if err != nil {
			return
		}
		if err := a.writeCoordReplay(w, flusher, principal, replay); err != nil {
			return
		}
		if replay.Resync {
			after = replay.Latest
		} else if replay.ScannedThrough > after {
			after = replay.ScannedThrough
		}
		flusher.Flush()
	}
}

func (a *app) writeCoordReplay(w http.ResponseWriter, flusher http.Flusher, principal store.Principal, replay store.CoordEventReplay) error {
	if replay.Resync {
		_, err := fmt.Fprintf(w, "id: %s\nevent: resync\ndata: {}\n\n", a.sealCoordCursor(principal, replay.Latest))
		return err
	}
	for _, event := range replay.Events {
		if event.Kind == store.CoordEventVisibility {
			if _, err := fmt.Fprintf(w, "id: %s\nevent: resync\ndata: {}\n\n", a.sealCoordCursor(principal, event.Sequence)); err != nil {
				return err
			}
			flusher.Flush()
			continue
		}
		var payload bytes.Buffer
		// Ohne Folgennummer: sie steckt versiegelt in der id.
		if err := json.NewEncoder(&payload).Encode(struct {
			Kind       string `json:"kind"`
			ObjectKind string `json:"object_kind"`
			ObjectID   string `json:"object_id"`
			CreatedAt  string `json:"created_at"`
		}{event.Kind, event.ObjectKind, event.ObjectID, event.CreatedAt}); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %s\nevent: coord.changed\ndata: %s\n", a.sealCoordCursor(principal, event.Sequence), payload.String()); err != nil {
			return err
		}
		flusher.Flush()
	}
	return nil
}
