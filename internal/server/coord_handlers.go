package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// validDestinationKind hält erfundene Ziele draußen. Ein unbekanntes Ziel
// wäre kein leerer Raum, sondern ein Ablageort, den kein Leser je abfragt —
// die Nachricht wäre gespeichert und trotzdem für immer unsichtbar.
func validDestinationKind(kind string) bool {
	return kind == store.DestinationRoom || kind == store.DestinationDiscussion
}

func (a *api) coordAccess(r *http.Request, agentID string) store.CoordAccess {
	return a.st.CoordinationFor(principalOf(r), agentID)
}

func writeCoordAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrCoordNotFound):
		writeErr(w, http.StatusNotFound, "coordination target not found")
	case errors.Is(err, store.ErrCoordForbidden):
		writeErr(w, http.StatusForbidden, "coordination target forbidden")
	case errors.Is(err, store.ErrInvalidAttentionAction), errors.Is(err, store.ErrAttentionRecipientRequired):
		writeErr(w, http.StatusBadRequest, "invalid coordination attention action")
	case errors.Is(err, store.ErrAttentionClosed):
		writeErr(w, http.StatusConflict, "coordination attention item is already closed")
	default:
		writeStoreError(w, http.StatusInternalServerError, err)
	}
}

func (a *api) coordAttention(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_external_id")
	if agentID == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id is required")
		return
	}
	items, err := a.coordAccess(r, agentID).Attention()
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if items == nil {
		items = []store.AttentionItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

type coordAttentionActionInput struct {
	AgentExternalID string `json:"agent_external_id"`
	AttentionID     int64  `json:"attention_id"`
	Action          string `json:"action"`
}

func (a *api) coordAttentionAction(w http.ResponseWriter, r *http.Request) {
	var in coordAttentionActionInput
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.AgentExternalID == "" || in.AttentionID <= 0 || in.Action == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id, attention_id and action are required")
		return
	}
	if err := a.coordAccess(r, in.AgentExternalID).ResolveAttention(in.AttentionID, in.Action); err != nil {
		writeCoordAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if !registered {
		return true, nil
	}
	return owner != "" && owner == principalOf(r).ID, nil
}

func (a *api) mayActAsRegistered(r *http.Request, externalID string) (bool, error) {
	owner, registered, err := a.st.CoordAgentOwner(externalID)
	if err != nil {
		return false, err
	}
	return registered && owner != "" && owner == principalOf(r).ID, nil
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
	id, err := a.coordAccess(r, in.SenderExternalID).Send(in)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	// Gespeichert ist der einzige Zustand, den dieser Aufruf belegen kann.
	// Alles Weitere setzt der Adapter, wenn er es beobachtet.
	if err := a.coordAccess(r, in.SenderExternalID).MarkDelivery(id, store.DeliveryStored); err != nil {
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
	asker := q.Get("agent_external_id")
	publicOnly := q.Get("public_only") == "1"
	if asker != "" && publicOnly {
		writeErr(w, http.StatusBadRequest, "choose agent_external_id or public_only")
		return
	}
	if asker == "" && !publicOnly {
		writeErr(w, http.StatusBadRequest, "agent_external_id is required")
		return
	}
	if publicOnly && kind != store.DestinationDiscussion {
		writeErr(w, http.StatusBadRequest, "public_only is only available for discussions")
		return
	}
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	access := a.coordAccess(r, asker)
	if publicOnly {
		access = a.st.CoordinationPublicFor(principalOf(r))
	}
	out, err := access.Messages(kind, q.Get("destination_id"), after, limit)
	if err != nil {
		writeCoordAccessError(w, err)
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
	// Ein Projektraum gehört zu einem Projekt: eine fremde Remote gibt es für
	// diesen Aufrufer nicht, eine unbekannte wird wie bei jedem Schreiben
	// zugeordnet.
	if project, ok := strings.CutPrefix(in.RoomKey, "project:"); ok && !a.gateProject(w, r, project) {
		return
	}
	if _, ok := accountOf(principalOf(r)); !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !store.ValidAgentRole(in.Role) {
		writeCoded(w, http.StatusBadRequest, "invalid_role", "agent role must be lead, member or guest")
		return
	}
	if owner, registered, err := a.st.CoordAgentOwner(in.ExternalID); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if registered && owner != "" && owner != principalOf(r).ID {
		writeErr(w, http.StatusForbidden, "that session belongs to someone else")
		return
	}
	// Eine Maschine nennt der Raum (machine:<host>) oder die CLI-Identität
	// (cli:<host>); beide stehen unter der Maschinenregel.
	machine := strings.TrimPrefix(in.RoomKey, "machine:")
	if machine == in.RoomKey {
		machine = ""
	}
	if host, ok := strings.CutPrefix(in.ExternalID, "cli:"); ok && machine == "" {
		machine = host
	}
	if !a.gateMachine(w, r, machine, true) {
		return
	}
	in.Person = personOf(r)
	in.PrincipalID = principalOf(r).ID
	id, err := a.st.RegisterCoordAgent(in)
	if err != nil {
		if errors.Is(err, store.ErrCoordAgentOwned) {
			writeErr(w, http.StatusForbidden, "that session belongs to someone else")
			return
		}
		if errors.Is(err, store.ErrCoordAgentScopeChanged) {
			writeErr(w, http.StatusBadRequest, "that session is already registered in another room of this kind")
			return
		}
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (a *api) coordPeers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("room_key") == "" || q.Get("agent_external_id") == "" {
		writeErr(w, http.StatusBadRequest, "room_key and agent_external_id are required")
		return
	}
	out, err := a.coordAccess(r, q.Get("agent_external_id")).Peers(q.Get("room_key"), q.Get("since"))
	if err != nil {
		writeCoordAccessError(w, err)
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
	if !validDestinationKind(kind) || !validCursorDestination(kind, q.Get("destination_id")) {
		writeErr(w, http.StatusBadRequest, "invalid cursor destination")
		return
	}
	last, err := a.coordAccess(r, q.Get("agent_external_id")).Cursor(kind, q.Get("destination_id"))
	if err != nil {
		writeCoordAccessError(w, err)
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
	if !validDestinationKind(in.DestinationKind) || !validCursorDestination(in.DestinationKind, in.DestinationID) || in.LastMessageID < 0 {
		writeErr(w, http.StatusBadRequest, "invalid cursor destination or range")
		return
	}
	if err := a.coordAccess(r, in.AgentExternalID).SetCursor(in.DestinationKind, in.DestinationID, in.LastMessageID); err != nil {
		writeCoordAccessError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func validCursorDestination(kind, id string) bool {
	if kind != store.DestinationDiscussion {
		return id != ""
	}
	threadID, err := strconv.ParseInt(id, 10, 64)
	return err == nil && threadID > 0
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
	if in.Kind != store.RoomDirect {
		writeErr(w, http.StatusBadRequest, "only canonical direct rooms use this endpoint")
		return
	}
	agent := r.URL.Query().Get("agent_external_id")
	if agent == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id is required")
		return
	}
	if ok, err := a.mayActAsRegistered(r, agent); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeCoordAccessError(w, store.ErrCoordForbidden)
		return
	}
	if ok, err := a.coordMembersRegistered(in.Members); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeErr(w, http.StatusForbidden, "every private-room participant must be a registered session")
		return
	}
	if err := a.coordAccess(r, agent).EnsureDirect(in); err != nil {
		if errors.Is(err, store.ErrCoordForbidden) || errors.Is(err, store.ErrCoordNotFound) {
			writeCoordAccessError(w, err)
			return
		}
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"room_key": in.Key})
}

func (a *api) createCoordGroup(w http.ResponseWriter, r *http.Request) {
	var in store.GroupInput
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	agent := r.URL.Query().Get("agent_external_id")
	if agent == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id is required")
		return
	}
	if ok, err := a.mayActAsRegistered(r, agent); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeCoordAccessError(w, store.ErrCoordForbidden)
		return
	}
	if ok, err := a.coordMembersRegistered(in.Members); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeErr(w, http.StatusForbidden, "every private-room participant must be a registered session")
		return
	}
	room, err := a.coordAccess(r, agent).CreateGroup(in)
	if err != nil {
		if errors.Is(err, store.ErrCoordForbidden) || errors.Is(err, store.ErrCoordNotFound) {
			writeCoordAccessError(w, err)
			return
		}
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, room)
}

func (a *api) coordMembersRegistered(members []string) (bool, error) {
	for _, member := range members {
		owner, registered, err := a.st.CoordAgentOwner(member)
		if err != nil {
			return false, err
		}
		if !registered || owner == "" {
			return false, nil
		}
	}
	return true, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	out, err := a.coordAccess(r, agent).Rooms()
	if err != nil {
		writeCoordAccessError(w, err)
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
	agent := r.URL.Query().Get("agent_external_id")
	if agent == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id is required")
		return
	}
	out, err := a.coordAccess(r, agent).MessageMentions(id)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if out == nil {
		out = []string{}
	}
	writeJSON(w, 200, out)
}

// pathActivity beantwortet "wer hat in den letzten N Minuten an diesem Pfad
// gearbeitet". Der Fragende schließt sich selbst aus: die häufigste Datei, an
// der jemand arbeitet, ist die eigene.
func (a *api) pathActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("path") == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	// Wer im Projekt arbeitet, ist Metadaten-Sicht: ab member. Der Hook fragt in
	// jedem Repository, also kommt ohne Recht eine leere Liste statt eines Fehlers.
	if !a.access(r).Allow(scope.NormalizeRemote(q.Get("project")), store.ResSessionMeta, store.ActRead, store.Object{}) {
		writeJSON(w, 200, []store.PathActivity{})
		return
	}
	minutes, _ := strconv.Atoi(q.Get("minutes"))
	out, err := a.st.PathActivitySince(q.Get("project"), q.Get("path"),
		store.ActivityWindow(minutes), q.Get("exclude_session"))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		out = []store.PathActivity{}
	}
	writeJSON(w, 200, out)
}

// sessionActivity zeigt, woran EINE Session gearbeitet hat — die
// Detailansicht hinter einem Teilnehmer, und der Grund, warum die Daten auch
// ohne zweiten Agenten nützen.
func (a *api) sessionActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("session") == "" {
		writeErr(w, http.StatusBadRequest, "session is required")
		return
	}
	if ok, err := a.mayActAs(r, q.Get("session")); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeErr(w, http.StatusForbidden, "that session belongs to someone else")
		return
	}
	minutes, _ := strconv.Atoi(q.Get("minutes"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := a.st.SessionPathActivity(q.Get("session"), store.ActivityWindow(minutes), limit)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		out = []store.PathActivity{}
	}
	writeJSON(w, 200, out)
}

// recordPathActivity nimmt beobachtete Aktivität an.
//
// Jede einzelne Zeile muss zu einer Session gehören, die dem Token gehört.
// Sonst könnte ein Tokeninhaber fremde Aktivität ERFINDEN — und eine
// erfundene Aktivität ist schlimmer als eine fehlende: sie erzeugt
// Konfliktwarnungen, die niemanden betreffen, und macht damit die nächste
// echte Warnung unglaubwürdig.
func (a *api) recordPathActivity(w http.ResponseWriter, r *http.Request) {
	var in []store.PathActivity
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	for _, e := range in {
		if ok, err := a.mayActAs(r, e.SessionExternalID); err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		} else if !ok {
			writeErr(w, http.StatusForbidden, "cannot record activity for another person's session")
			return
		}
	}
	if err := a.st.RecordPathActivity(in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]int{"recorded": len(in)})
}

type coordDeliveryInput struct {
	MessageID int64  `json:"message_id"`
	Recipient string `json:"recipient_external_id"`
	State     string `json:"state"`
}

// markCoordDelivery nimmt die Beobachtung eines Adapters entgegen.
//
// Der Empfänger muss zu diesem Token gehören: sonst könnte ein Tokeninhaber
// für eine fremde Session "gelesen" melden, und der Absender hielte eine
// Nachricht für angekommen, die niemand gesehen hat. Eine erfundene
// Empfangsbestätigung ist schlimmer als gar keine.
func (a *api) markCoordDelivery(w http.ResponseWriter, r *http.Request) {
	var in coordDeliveryInput
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.MessageID == 0 || in.Recipient == "" || in.State == "" {
		writeErr(w, http.StatusBadRequest, "message_id, recipient_external_id and state are required")
		return
	}
	if err := a.coordAccess(r, in.Recipient).MarkDelivery(in.MessageID, in.State); err != nil {
		if errors.Is(err, store.ErrCoordNotFound) || errors.Is(err, store.ErrCoordForbidden) {
			writeCoordAccessError(w, err)
			return
		}
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": in.State})
}

// claimCoordDelivery entscheidet, welcher Kanal eine Nachricht einbringen
// darf. Prüfung und Principal-Zugriff laufen wie bei markCoordDelivery: der
// Empfänger muss zu diesem Token gehören.
func (a *api) claimCoordDelivery(w http.ResponseWriter, r *http.Request) {
	var in coordDeliveryInput
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.MessageID == 0 || in.Recipient == "" {
		writeErr(w, http.StatusBadRequest, "message_id and recipient_external_id are required")
		return
	}
	claimed, err := a.coordAccess(r, in.Recipient).ClaimDelivery(in.MessageID)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"claimed": claimed})
}

// maxInjectedLookup begrenzt eine Abfrage. Der Poller fragt nach einer Seite
// Inbox, nicht nach dem ganzen Verlauf.
const maxInjectedLookup = 500

func (a *api) coordInjectedMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("agent_external_id") == "" {
		writeErr(w, http.StatusBadRequest, "agent_external_id is required")
		return
	}
	var ids []int64
	for _, part := range strings.Split(q.Get("message_ids"), ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid message_ids")
			return
		}
		if len(ids) == maxInjectedLookup {
			writeErr(w, http.StatusBadRequest, "too many message_ids")
			return
		}
		ids = append(ids, id)
	}
	injected, err := a.coordAccess(r, q.Get("agent_external_id")).InjectedMessages(ids)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if injected == nil {
		injected = []int64{}
	}
	writeJSON(w, 200, map[string][]int64{"message_ids": injected})
}
