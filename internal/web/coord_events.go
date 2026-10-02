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

// Format 2: Version, Folge, Zeit. Frühere Cursor (Format 1, nur die Folge)
// lassen sich nicht öffnen und ergeben einen sauberen Resync.
var coordCursorAAD = []byte("ghosttree coord event cursor v2")

const coordCursorVersion = 2

// coordEventWindow ist die Aufbewahrung des Protokolls (Trigger
// coord_events_age, zehn Minuten); ein Cursor gilt etwas kürzer, damit er seinen
// Verlauf nie überlebt.
const (
	coordEventWindow       = 10 * time.Minute
	coordCursorFreshWindow = 9 * time.Minute
)

// coordCursors liefert den Schlüssel, der Ereignis-Cursor versiegelt. Er lebt
// nur im Prozess (wie der Flow-Schlüssel des OIDC-Logins); nach einem Neustart
// werden alte Cursor als ungültig behandelt und lösen einen Resync aus.
func (a *app) coordCursors() *flowSealer {
	a.cursorOnce.Do(func() {
		a.cursorSeal, _ = newFlowSealer()
	})
	return a.cursorSeal
}

func coordCursorData(principal store.Principal) []byte {
	return append(append([]byte{}, coordCursorAAD...), principal.ID...)
}

// sealCoordCursor macht aus der globalen Ereignisfolge einen undurchsichtigen
// Wert. Die Nummer selbst wäre ein Seitenkanal: Lücken zwischen den eigenen
// Beiträgen zeigten, wie viele für andere sichtbare Ereignisse (etwa eine
// Zustellung an ein Mitglied) dazwischen lagen. Jeder Leser bekommt die
// versiegelte Form, damit es keinen Zweig gibt, an dem man ihn erkennt; die
// Nonce ist zufällig, zwei Cursor für dieselbe Folge sind nicht vergleichbar.
//
// Der Cursor trägt seine Zeit: die des Ereignisses, auf das er zeigt, oder die
// der Ausgabe. Ob er noch gilt, entscheidet allein diese Zeit gegen das
// Aufbewahrungsfenster, nie der aktuelle Inhalt der Tabelle.
func (a *app) sealCoordCursor(principal store.Principal, seq int64, at time.Time) string {
	sealer := a.coordCursors()
	if sealer == nil {
		return ""
	}
	plain := make([]byte, 17)
	plain[0] = coordCursorVersion
	binary.BigEndian.PutUint64(plain[1:9], uint64(seq))
	binary.BigEndian.PutUint64(plain[9:17], uint64(at.UnixMilli()))
	nonce := make([]byte, sealer.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(sealer.aead.Seal(nonce, nonce, plain, coordCursorData(principal)))
}

// openCoordCursor öffnet einen Cursor. ok=false heißt: ungültig, manipuliert,
// von einem anderen Konto oder im alten Format; der Aufrufer antwortet dann wie
// bei einem zu alten Cursor (Resync) und nie mit einem Fehler, der etwas
// verrät. Leer und "0" sind der Anfang (Zeit: jetzt).
func (a *app) openCoordCursor(principal store.Principal, value string) (seq int64, at time.Time, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" {
		return 0, time.Now(), true
	}
	sealer := a.coordCursors()
	if sealer == nil {
		return 0, time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < sealer.aead.NonceSize() {
		return 0, time.Time{}, false
	}
	plain, err := sealer.aead.Open(nil, raw[:sealer.aead.NonceSize()], raw[sealer.aead.NonceSize():], coordCursorData(principal))
	if err != nil || len(plain) != 17 || plain[0] != coordCursorVersion {
		return 0, time.Time{}, false
	}
	u := binary.BigEndian.Uint64(plain[1:9])
	if u > math.MaxInt64 {
		return 0, time.Time{}, false
	}
	return int64(u), time.UnixMilli(int64(binary.BigEndian.Uint64(plain[9:17]))), true
}

// coordCursorStale: der Cursor ist älter als das Fenster minus Rand. Dann kann
// Verlauf dahinter schon gelöscht sein, und die Seite muss neu laden. Die
// Entscheidung hängt nur an der Zeit im Cursor, bei Gast und Mitglied gleich.
func coordCursorStale(at time.Time) bool {
	return time.Since(at) > coordCursorFreshWindow
}

// eventTime ist die Zeit eines Ereignisses; ohne lesbare Zeit gilt "jetzt".
func eventTime(createdAt string) time.Time {
	if at, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		return at
	}
	return time.Now()
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
	after, cursorAt, cursorOK := a.openCoordCursor(principal, coordCursorParam(r))
	if cursorOK && after > 0 && coordCursorStale(cursorAt) {
		cursorOK = false
	}
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
				// Eine frische id ohne Daten hält die Last-Event-ID des Browsers
				// jung: sie zeigt auf die Position, bis zu der dieser Strom
				// gelesen hat, mit der Zeit von jetzt.
				if _, err := fmt.Fprintf(w, "id: %s\n: keepalive\n\n", a.sealCoordCursor(principal, after, time.Now())); err != nil {
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
		_, err := fmt.Fprintf(w, "id: %s\nevent: resync\ndata: {}\n\n", a.sealCoordCursor(principal, replay.Latest, time.Now()))
		return err
	}
	for _, event := range replay.Events {
		if event.Kind == store.CoordEventVisibility {
			if _, err := fmt.Fprintf(w, "id: %s\nevent: resync\ndata: {}\n\n", a.sealCoordCursor(principal, event.Sequence, eventTime(event.CreatedAt))); err != nil {
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
		if _, err := fmt.Fprintf(w, "id: %s\nevent: coord.changed\ndata: %s\n", a.sealCoordCursor(principal, event.Sequence, eventTime(event.CreatedAt)), payload.String()); err != nil {
			return err
		}
		flusher.Flush()
	}
	return nil
}
