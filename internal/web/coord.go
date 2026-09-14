package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
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

func (a *app) coordRoomPage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		rooms, err := a.store.CoordRoomsFor(personOf(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.render(w, "coord", pageData{Title: "Coordination", CoordRooms: rooms})
		return
	}
	msgs, err := a.store.CoordMessagesSince(store.DestinationRoom, room, 0, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	peers, err := a.store.CoordPeers(room, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	standing, err := a.store.StandingInstructions(room)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, "coord", pageData{Title: "Coordination",
		CoordRoom: room, CoordMessages: msgs, CoordPeers: peers,
		CoordStanding: standing})
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

	msg := store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "human:" + person,
		AuthorKind:       store.AuthorHuman,
		ClientID:         newFormClientID(r),
		Body:             body,
		ExpiresAt:        strings.TrimSpace(r.FormValue("expires_at")),
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

	id, err := a.store.AppendCoordMessage(msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	http.Redirect(w, r, "/ui/coord?room="+room, http.StatusSeeOther)
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
	if err := a.store.EndStandingInstruction(room, id, personOf(r)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+room, http.StatusSeeOther)
}
