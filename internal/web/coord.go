package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Der menschliche Composer. Bis hierhin war die Operator-Oberfläche
// schreibfrei — außer Login und Logout gab es keine POST-Route.
//
// Warum das überhaupt eine eigene Datei ist und nicht ein Formular neben den
// anderen Ansichten: hier entsteht die einzige Stelle im System, an der ein
// Beitrag mit author_kind=human gespeichert wird. Über die Agentenroute geht
// das nicht, und zwar absichtlich (Spec §9). Der Unterschied zwischen "ein
// Agent behauptet, Robin habe etwas gesagt" und "Robin hat etwas gesagt"
// verläuft genau an dieser Grenze, und sie ist die Sitzungskennung im
// Cookie — nicht ein Feld im Rumpf.

// newFormClientID macht das Absenden eines Formulars wiederholbar. Ein
// Doppelklick oder ein Reload darf keine zweite Vorgabe erzeugen — beim
// Menschen ist das häufiger als beim Agenten, und die Folge wäre dieselbe
// Anweisung zweimal im Raum.
func newFormClientID(r *http.Request) string {
	if v := strings.TrimSpace(r.FormValue("form_id")); v != "" {
		return v
	}
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// humanMember ist die Kennung, unter der ein angemeldeter Mensch in Räumen
// steht. Dieselbe wie beim Schreiben — sonst dürfte jemand schreiben, was er
// hinterher nicht lesen kann.
func humanMember(r *http.Request) string { return browserPrincipal(r).ID }

func (a *app) browserCoord(r *http.Request) store.CoordAccess {
	return a.store.CoordinationFor(browserPrincipal(r), "")
}

func coordHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrCoordNotFound):
		http.Error(w, "coordination target not found", http.StatusNotFound)
	case errors.Is(err, store.ErrCoordForbidden):
		http.Error(w, "coordination target forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrCoordInvalidSequence):
		http.Error(w, "invalid coordination sequence", http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// mayEnter prüft die Raummitgliedschaft mit derselben Funktion, die auch die
// API benutzt.
//
// Angemeldet zu sein ist NICHT dasselbe wie in einem Raum zu sein. Ein
// Mensch sieht öffentliche Räume über seine direkte Mitgliedschaft oder
// eine ihm gehörende aktive Agent-Session. Das ist eine Navigationsgrenze,
// keine Mandantentrennung: Agent-Scope wird vom Client beobachtet und
// selbst gemeldet. Ein privates Gespräch zwischen zwei Agenten sieht er nicht,
// nur weil er dessen Schlüssel in die URL schreibt. Spec §9: private DMs
// dürfen nicht über Suche, Zusammenfassung oder Verknüpfung sichtbar
// werden — eine URL ist keine Ausnahme davon.
func (a *app) mayEnter(w http.ResponseWriter, r *http.Request, room string) bool {
	if _, err := a.browserCoord(r).Room(room); err != nil {
		coordHTTPError(w, err)
		return false
	}
	return true
}

func (a *app) coordRoomPage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		summaries, err := a.browserCoord(r).RoomSummaries()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.renderBrowser(w, r, "coord", pageData{Title: "Coordination", CoordRoomSummaries: summaries})
		return
	}
	if !a.mayEnter(w, r, room) {
		return
	}
	window, err := coordWindowFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	page, err := a.browserCoord(r).MessageWindow(store.DestinationRoom, room, window)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	peers, err := a.browserCoord(r).Peers(room, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	standing, err := a.store.StandingInstructions(room)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Coordination",
		CoordRoom: room, CoordMessages: page.Messages, CoordPeers: peers,
		CoordStanding: standing, CoordHighWater: page.HighWater,
		CoordHasOlder: page.HasOlder, CoordHasNewer: page.HasNewer}
	if len(page.Messages) > 0 {
		data.CoordFirstSequence = page.Messages[0].Sequence
		data.CoordLastSequence = page.Messages[len(page.Messages)-1].Sequence
		data.CoordBefore = data.CoordFirstSequence
		data.CoordAfter = data.CoordLastSequence
	} else if page.HasOlder {
		// A forward cursor at the high-water has no message from which to derive
		// a boundary. The durable high-water is still a valid before cursor,
		// including when retention removed that exact row.
		data.CoordBefore = page.HighWater
	}
	a.renderBrowser(w, r, "coord", data)
}

func coordWindowFromRequest(r *http.Request) (store.MessageWindow, error) {
	beforeText := strings.TrimSpace(r.URL.Query().Get("before"))
	afterText := strings.TrimSpace(r.URL.Query().Get("after"))
	if beforeText != "" && afterText != "" {
		return store.MessageWindow{}, errors.New("before and after are mutually exclusive")
	}
	if beforeText != "" {
		sequence, err := strconv.ParseInt(beforeText, 10, 64)
		if err != nil || sequence <= 0 {
			return store.MessageWindow{}, errors.New("before must be a positive sequence")
		}
		return store.BeforeWindow(sequence, 50), nil
	}
	if afterText != "" {
		sequence, err := strconv.ParseInt(afterText, 10, 64)
		if err != nil || sequence < 0 {
			return store.MessageWindow{}, errors.New("after must be a non-negative sequence")
		}
		return store.AfterWindow(sequence, 50), nil
	}
	return store.LatestWindow(50), nil
}

// coordSend speichert einen menschlichen Beitrag.
//
// Die Herkunft kommt aus der angemeldeten Sitzung, nicht aus dem Formular.
// Ein Feld "ich bin Robin" gibt es nicht und darf es nicht geben — sonst
// wäre die ganze Unterscheidung zwischen Agenten- und Menschenbeitrag ein
// Textfeld, und "Robin hat gesagt, du sollst deployen" eine Autorisierung.
func (a *app) coordSend(w http.ResponseWriter, r *http.Request) {
	person := personOf(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room := strings.TrimSpace(r.FormValue("room"))
	body := strings.TrimSpace(r.FormValue("body"))
	if room == "" || body == "" {
		http.Error(w, "room and body are required", http.StatusBadRequest)
		return
	}
	if !a.mayEnter(w, r, room) {
		return
	}

	msg := store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		ClientID:  newFormClientID(r),
		Body:      body,
		ExpiresAt: strings.TrimSpace(r.FormValue("expires_at")),
	}
	if mentions := strings.Fields(r.FormValue("mentions")); len(mentions) > 0 {
		msg.Mentions = mentions
	}

	// Eine gezielte Vorgabe ist etwas anderes als eine Mitteilung. Spec §A4
	// verlangt die Unterscheidung, und §11 verlangt, dass eine weiter
	// geltende Einschränkung nicht durch Chat-Retention oder eine
	// Zusammenfassung unbemerkt verschwindet. Deshalb bekommt sie einen
	// eigenen Zustand statt nur eine besonders laut formulierte Nachricht.
	if r.FormValue("standing") != "" {
		msg.Intent = store.IntentStanding
	}

	id, err := a.browserCoord(r).Send(msg)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	if msg.Intent == store.IntentStanding {
		if err := a.store.PutStandingInstruction(store.StandingInstruction{
			RoomKey: room, MessageID: store.FormatMessageID(id), Person: person, Body: body,
			Targets: msg.Mentions,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room), http.StatusSeeOther)
}

func (a *app) coordMarkRead(w http.ResponseWriter, r *http.Request) {
	a.coordSetReadState(w, r, false)
}

func (a *app) coordMarkUnread(w http.ResponseWriter, r *http.Request) {
	a.coordSetReadState(w, r, true)
}

func (a *app) coordSetReadState(w http.ResponseWriter, r *http.Request, unread bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room := strings.TrimSpace(r.FormValue("room"))
	sequence, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("sequence")), 10, 64)
	if room == "" || err != nil || sequence < 0 || (unread && sequence == 0) {
		http.Error(w, "room and valid sequence are required", http.StatusBadRequest)
		return
	}
	access := a.browserCoord(r)
	if unread {
		err = access.MarkUnread(store.DestinationRoom, room, sequence)
	} else {
		err = access.MarkRead(store.DestinationRoom, room, sequence)
	}
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room), http.StatusSeeOther)
}

// coordEndStanding beendet eine Vorgabe. Ausdrücklich und von einem
// angemeldeten Menschen — eine geltende Einschränkung endet mit ihrer
// Aufgabe oder durch diese Geste, nicht dadurch, dass genug Nachrichten
// darüber hinweggelaufen sind.
func (a *app) coordEndStanding(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room := r.FormValue("room")
	id := r.FormValue("message_id")
	if room == "" || id == "" {
		http.Error(w, "room and message_id are required", http.StatusBadRequest)
		return
	}
	if !a.mayEnter(w, r, room) {
		return
	}
	if err := a.store.EndStandingInstruction(room, id, personOf(r)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room), http.StatusSeeOther)
}
