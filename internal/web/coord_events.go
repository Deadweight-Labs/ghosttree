package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const coordEventPollInterval = 250 * time.Millisecond

// sessionRecheckInterval begrenzt, wie oft ein offener Stream Token und Konto
// gegen die Datenbank prüft. Variable, damit ein Test es verkürzen kann.
var sessionRecheckInterval = 20 * time.Second

func parseCoordEventCursor(r *http.Request) (int64, error) {
	value := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if value == "" {
		value = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, store.ErrCoordEventCursor
	}
	return cursor, nil
}

func (a *app) coordEvents(w http.ResponseWriter, r *http.Request) {
	after, err := parseCoordEventCursor(r)
	if err != nil {
		http.Error(w, "bad coordination event cursor", http.StatusBadRequest)
		return
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
	principal := browserPrincipal(r)
	replay, err := a.store.CoordEventsAfter(principal, after, 100)
	if errors.Is(err, store.ErrCoordEventCursor) {
		http.Error(w, "bad coordination event cursor", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "coordination stream unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(w, "retry: 1000\n\n")
	if err := writeCoordReplay(w, flusher, replay); err != nil {
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
		if err := writeCoordReplay(w, flusher, replay); err != nil {
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

func writeCoordReplay(w http.ResponseWriter, flusher http.Flusher, replay store.CoordEventReplay) error {
	if replay.Resync {
		_, err := fmt.Fprintf(w, "id: %d\nevent: resync\ndata: {}\n\n", replay.Latest)
		return err
	}
	for _, event := range replay.Events {
		if event.Kind == store.CoordEventVisibility {
			if _, err := fmt.Fprintf(w, "id: %d\nevent: resync\ndata: {}\n\n", event.Sequence); err != nil {
				return err
			}
			flusher.Flush()
			continue
		}
		var payload bytes.Buffer
		if err := json.NewEncoder(&payload).Encode(event); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: coord.changed\ndata: %s\n", event.Sequence, payload.String()); err != nil {
			return err
		}
		flusher.Flush()
	}
	return nil
}
