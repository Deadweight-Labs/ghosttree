package server

import (
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// validDestinationKind hält erfundene Ziele draußen. Ein unbekanntes Ziel
// wäre kein leerer Raum, sondern ein Ablageort, den kein Leser je abfragt —
// die Nachricht wäre gespeichert und trotzdem für immer unsichtbar.
func validDestinationKind(kind string) bool {
	return kind == store.DestinationRoom || kind == store.DestinationDiscussion
}

// mayActAs schließt die Lücke zwischen Authentifizierung und Autorisierung.
//
// Ein Token weist eine PERSON aus; Räume gehören SESSIONS. Ohne diese Prüfung
// könnte jeder Tokeninhaber eine fremde Session-Referenz in Query oder Rumpf
// schreiben und damit deren private Räume lesen oder in sie schreiben — der
// Tokeninhaber ist nicht dasselbe wie der Teilnehmer.
//
// Eine noch nicht angemeldete Referenz wird durchgelassen: sie gehört
// niemandem, und sie kann auch niemandem etwas wegnehmen, weil
// Raummitgliedschaft an angemeldeten Sessions hängt. Sobald sich eine Session
// anmeldet, gehört ihre Referenz ihrer Person.
//
// Was hier ausdrücklich NICHT getrennt wird: zwei Sessions derselben Person.
// Spec §9 hält fest, dass Prozesse unter demselben Systemnutzer ohne weitere
// Isolation keine belastbare Sicherheitsgrenze sind. Die Grenze verläuft
// zwischen Personen, und das ist eine bewusste Entscheidung, keine Lücke.
func (a *api) mayActAs(r *http.Request, externalID string) (bool, error) {
	owner, registered, err := a.st.CoordAgentOwner(externalID)
	if err != nil {
		return false, err
	}
	if !registered || owner == "" {
		return true, nil
	}
	return owner == personOf(r), nil
}

// sendCoordMessage nimmt einen Beitrag von einem Agenten an.
//
// Herkunft wird hier gesetzt und nicht gelesen: über diesen Weg kommen
// ausschließlich Agenten herein. Ein im Rumpf behaupteter author_kind=human
// oder ein fremder Principal wird überschrieben, nicht abgelehnt — die
// Nachricht ist ja gültig, nur ihre Herkunftsbehauptung nicht. Spec §9:
// "Ein sender_kind=human im Modellargument wird abgewiesen." Menschliche
// Beiträge kommen über die angemeldete Oberfläche und tragen ihren Typ aus
// der Sitzung; eine Behauptung im Rumpf ist keine Herkunft.
func (a *api) sendCoordMessage(w http.ResponseWriter, r *http.Request) {
	var in store.CoordMessage
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.DestinationKind == "" {
		in.DestinationKind = store.DestinationRoom
	}
	if !validDestinationKind(in.DestinationKind) {
		writeErr(w, http.StatusBadRequest, "destination_kind must be room or discussion")
		return
	}
	if in.DestinationID == "" || in.SenderExternalID == "" || in.ClientID == "" {
		writeErr(w, http.StatusBadRequest, "destination_id, sender_external_id and client_id are required")
		return
	}
	// Zuerst: gehört die behauptete Absender-Session überhaupt zu diesem
	// Token? Sonst wäre die Raumprüfung darunter wertlos — man gäbe einfach
	// die Referenz eines Mitglieds an.
	if ok, err := a.mayActAs(r, in.SenderExternalID); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeErr(w, http.StatusForbidden, "that session belongs to someone else")
		return
	}
	// Die Zugriffsgrenze wird auf dem Schreibweg genauso geprüft wie auf dem
	// Leseweg. Ein Unbeteiligter darf in einen fremden DM nicht schreiben —
	// sonst steht dort plötzlich eine Nachricht von jemandem, der den Raum
	// nicht sehen kann.
	if ok, err := a.st.MayReadCoordRoom(in.DestinationID, in.SenderExternalID); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if !ok && in.DestinationKind == store.DestinationRoom {
		writeErr(w, http.StatusForbidden, "not a member of this room")
		return
	}
	in.AuthorKind = store.AuthorAgent
	in.AuthorPrincipalID = principalOf(r).ID
	id, err := a.st.AppendCoordMessage(in)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	// Gespeichert ist der einzige Zustand, den dieser Aufruf belegen kann.
	// Alles Weitere setzt der Adapter, wenn er es beobachtet.
	if err := a.st.MarkCoordDelivery(id, in.SenderExternalID, store.DeliveryStored); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (a *api) coordInbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("destination_kind")
	if kind == "" {
		kind = store.DestinationRoom
	}
	if !validDestinationKind(kind) {
		writeErr(w, http.StatusBadRequest, "destination_kind must be room or discussion")
		return
	}
	if q.Get("destination_id") == "" {
		writeErr(w, http.StatusBadRequest, "destination_id is required")
		return
	}
	// Ein Raum, den der Fragende nicht lesen darf, liefert nichts — und zwar
	// bevor irgendetwas gelesen wird. Spec §9: private DMs dürfen nicht über
	// Suche, Zusammenfassung oder Verknüpfung sichtbar werden, und der
	// direkte Abruf ist der offensichtlichste dieser Wege.
	if kind == store.DestinationRoom {
		asker := q.Get("agent_external_id")
		if ok, err := a.mayActAs(r, asker); err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		} else if !ok {
			writeErr(w, http.StatusForbidden, "that session belongs to someone else")
			return
		}
		if ok, err := a.st.MayReadCoordRoom(q.Get("destination_id"), asker); err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		} else if !ok {
			writeErr(w, http.StatusForbidden, "not a member of this room")
			return
		}
	}
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := a.st.CoordMessagesSince(kind, q.Get("destination_id"), after, limit)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		// [] und nicht null: der Aufrufer iteriert darüber, und null liest
		// sich wie ein Fehler statt wie "nichts".
		out = []store.CoordMessage{}
	}
	writeJSON(w, 200, out)
}

func (a *api) registerCoordAgent(w http.ResponseWriter, r *http.Request) {
	var in store.CoordAgent
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.ExternalID == "" || in.RoomKey == "" {
		writeErr(w, http.StatusBadRequest, "external_id and room_key are required")
		return
	}
	in.Person = personOf(r)
	id, err := a.st.RegisterCoordAgent(in)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (a *api) coordPeers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("room_key") == "" {
		writeErr(w, http.StatusBadRequest, "room_key is required")
		return
	}
	out, err := a.st.CoordPeers(q.Get("room_key"), q.Get("since"))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		out = []store.CoordAgent{}
	}
	writeJSON(w, 200, out)
}

// coordCursor liest und schreibt den Lesestand eines Empfängers. Getrennt
// von der Inbox, weil "ich habe das gelesen" eine andere Aussage ist als
// "gib mir Neues" — und weil ein Client nach einem Absturz seinen Stand
// erfragen können muss, ohne dabei Nachrichten abzuholen.
func (a *api) coordCursorGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("destination_kind")
	if kind == "" {
		kind = store.DestinationRoom
	}
	if q.Get("agent_external_id") == "" || q.Get("destination_id") == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id and destination_id are required")
		return
	}
	last, err := a.st.CoordCursor(q.Get("agent_external_id"), kind, q.Get("destination_id"))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"last_message_id": last})
}

type coordCursorInput struct {
	AgentExternalID string `json:"agent_external_id"`
	DestinationKind string `json:"destination_kind"`
	DestinationID   string `json:"destination_id"`
	LastMessageID   int64  `json:"last_message_id"`
}

func (a *api) coordCursorSet(w http.ResponseWriter, r *http.Request) {
	var in coordCursorInput
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.DestinationKind == "" {
		in.DestinationKind = store.DestinationRoom
	}
	if in.AgentExternalID == "" || in.DestinationID == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id and destination_id are required")
		return
	}
	if err := a.st.SetCoordCursor(in.AgentExternalID, in.DestinationKind, in.DestinationID, in.LastMessageID); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *api) ensureCoordRoom(w http.ResponseWriter, r *http.Request) {
	var in store.CoordRoom
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.Key == "" {
		writeErr(w, http.StatusBadRequest, "room_key is required")
		return
	}
	if err := a.st.EnsureCoordRoom(in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"room_key": in.Key})
}

func (a *api) coordRooms(w http.ResponseWriter, r *http.Request) {
	agent := r.URL.Query().Get("agent_external_id")
	if agent == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id is required")
		return
	}
	if ok, err := a.mayActAs(r, agent); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeErr(w, http.StatusForbidden, "that session belongs to someone else")
		return
	}
	out, err := a.st.CoordRoomsFor(agent)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		out = []store.CoordRoom{}
	}
	writeJSON(w, 200, out)
}

// coordMessageMentions liefert die ausdrücklich erwähnten Empfänger einer
// Nachricht. Eigener Endpunkt statt eines Feldes im Fenster: eine Inbox
// braucht die Erwähnungen selten, und ein Join lüde sie jedem Aufruf auf.
func (a *api) coordMessageMentions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "message id is required")
		return
	}
	out, err := a.st.CoordMessageMentions(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		out = []string{}
	}
	writeJSON(w, 200, out)
}
