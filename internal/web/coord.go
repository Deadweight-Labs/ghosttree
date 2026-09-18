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

func newCoordFormID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "coord-form"
	}
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
	case errors.Is(err, store.ErrInvalidGroup):
		http.Error(w, "coordination group change forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrLastGroupManager), errors.Is(err, store.ErrLegacyGroupReadOnly):
		http.Error(w, "coordination group change conflicts with its current state", http.StatusConflict)
	case errors.Is(err, store.ErrCoordInvalidSequence):
		http.Error(w, "invalid coordination sequence", http.StatusBadRequest)
	case errors.Is(err, store.ErrCoordUnknownRecipient):
		http.Error(w, "coordination recipient is not available", http.StatusBadRequest)
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
	summaries, err := a.browserCoord(r).RoomSummaries()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recipients, err := a.browserCoord(r).Recipients()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view := coordPageView{
		Sidebar:    buildCoordSidebar(summaries, humanMember(r), room),
		Recipients: buildCoordRecipientViews(recipients),
	}
	if room == "" {
		a.renderBrowser(w, r, "coord", pageData{Title: "Coordination", Coord: view})
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
	access := a.browserCoord(r)
	activeRoom, err := access.Room(room)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	page, presentations, err := access.MessagePresentationWindow(store.DestinationRoom, room, window)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	if aroundText := strings.TrimSpace(r.URL.Query().Get("around")); aroundText != "" {
		around, _ := strconv.ParseInt(aroundText, 10, 64)
		found := false
		for _, message := range page.Messages {
			if message.Sequence == around {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "around must name a retained message sequence", http.StatusBadRequest)
			return
		}
	}
	peers, err := access.Peers(room, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	standing, err := access.Standing(room)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var memberships []store.RoomMembership
	if activeRoom.Kind == store.RoomGroup || activeRoom.Kind == store.RoomDirect {
		memberships, err = access.RoomMemberships(room)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	detail := &coordRoomDetailView{
		Room: coordRoomView{Key: activeRoom.Key, Kind: activeRoom.Kind,
			Label: coordRoomLabel(activeRoom, humanMember(r)), URL: coordRoomURL(activeRoom.Key, "", 0), Active: true},
		Messages:     buildCoordMessageViews(presentations, room),
		Participants: buildCoordParticipants(activeRoom, peers, memberships, humanMember(r)),
		Standing:     buildCoordStandingViews(standing), HighWater: page.HighWater,
		HasOlder: page.HasOlder, HasNewer: page.HasNewer,
		CanLeave: activeRoom.Kind == store.RoomGroup,
		FormID:   newCoordFormID(), StandingFormID: newCoordFormID(),
	}
	for _, membership := range memberships {
		if membership.PrincipalID == humanMember(r) && membership.LeftAt == "" && membership.Manager {
			detail.CanManage = true
		}
	}
	view.Active = detail
	data := pageData{Title: "Coordination", Coord: view}
	var firstSequence, lastSequence, before, after int64
	if len(page.Messages) > 0 {
		firstSequence = page.Messages[0].Sequence
		lastSequence = page.Messages[len(page.Messages)-1].Sequence
		before = firstSequence
		after = lastSequence
	} else if page.HasOlder {
		// A forward cursor at the high-water has no message from which to derive
		// a boundary. The durable high-water is still a valid before cursor,
		// including when retention removed that exact row.
		before = page.HighWater
	}
	detail.FirstSequence = firstSequence
	detail.LastSequence = lastSequence
	if detail.HasOlder {
		detail.OlderURL = coordRoomURL(room, "before", before)
	}
	if detail.HasNewer {
		detail.NewerURL = coordRoomURL(room, "after", after)
	}
	a.renderBrowser(w, r, "coord", data)
}

func coordWindowFromRequest(r *http.Request) (store.MessageWindow, error) {
	beforeText := strings.TrimSpace(r.URL.Query().Get("before"))
	afterText := strings.TrimSpace(r.URL.Query().Get("after"))
	aroundText := strings.TrimSpace(r.URL.Query().Get("around"))
	set := 0
	for _, value := range []string{beforeText, afterText, aroundText} {
		if value != "" {
			set++
		}
	}
	if set > 1 {
		return store.MessageWindow{}, errors.New("before, after and around are mutually exclusive")
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
	if aroundText != "" {
		sequence, err := strconv.ParseInt(aroundText, 10, 64)
		if err != nil || sequence <= 0 {
			return store.MessageWindow{}, errors.New("around must be a positive sequence")
		}
		start := sequence - 25
		if start < 0 {
			start = 0
		}
		return store.AfterWindow(start, 50), nil
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
	msg.Mentions = splitCoordPrincipals(r.Form["mentions"]...)

	_, err := a.browserCoord(r).Send(msg)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room), http.StatusSeeOther)
}

func (a *app) coordCreateStanding(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room, body := strings.TrimSpace(r.FormValue("room")), strings.TrimSpace(r.FormValue("body"))
	if room == "" || body == "" || r.FormValue("confirm_scope") != "1" {
		http.Error(w, "room, body and scope confirmation are required", http.StatusBadRequest)
		return
	}
	mentions := splitCoordPrincipals(r.Form["mentions"]...)
	_, err := a.browserCoord(r).CreateStanding(store.StandingInput{RoomKey: room, ClientID: newFormClientID(r), Body: body, ExpiresAt: strings.TrimSpace(r.FormValue("expires_at")), Mentions: mentions})
	if err != nil {
		coordHTTPError(w, err)
		return
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
	if err := a.browserCoord(r).EndStanding(room, id); err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room), http.StatusSeeOther)
}

func splitCoordPrincipals(values ...string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, value := range values {
		for _, principal := range strings.Fields(value) {
			if !seen[principal] {
				seen[principal] = true
				out = append(out, principal)
			}
		}
	}
	return out
}

func (a *app) coordStartDirect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actor := humanMember(r)
	target := strings.TrimSpace(r.FormValue("principal_id"))
	if target == "" || target == actor || len(strings.Fields(target)) != 1 {
		http.Error(w, "one distinct principal id is required", http.StatusBadRequest)
		return
	}
	members := []string{actor, target}
	room := store.CoordRoom{Key: store.RoomKeyForDirect(members), Kind: store.RoomDirect, Members: members}
	if err := a.browserCoord(r).EnsureDirect(room); err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room.Key), http.StatusSeeOther)
}

func (a *app) coordCreateGroup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	members := splitCoordPrincipals(r.Form["members"]...)
	members = append(members, humanMember(r))
	room, err := a.browserCoord(r).CreateGroup(store.GroupInput{
		Label: strings.TrimSpace(r.FormValue("label")), Members: members,
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidGroup) {
			http.Error(w, "a group needs at least one other participant", http.StatusBadRequest)
			return
		}
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room.Key), http.StatusSeeOther)
}

func (a *app) coordUpdateGroup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room := strings.TrimSpace(r.FormValue("room"))
	if room == "" {
		http.Error(w, "room is required", http.StatusBadRequest)
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	err := a.browserCoord(r).UpdateGroup(store.GroupUpdate{
		RoomKey: room, Actor: humanMember(r), Label: &label,
		Add: splitCoordPrincipals(r.Form["add"]...), Remove: splitCoordPrincipals(r.Form["remove"]...),
		AddManagers:    splitCoordPrincipals(r.Form["add_managers"]...),
		RemoveManagers: splitCoordPrincipals(r.Form["remove_managers"]...),
	})
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room), http.StatusSeeOther)
}

func (a *app) coordLeaveGroup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room := strings.TrimSpace(r.FormValue("room"))
	if room == "" {
		http.Error(w, "room is required", http.StatusBadRequest)
		return
	}
	if err := a.browserCoord(r).LeaveRoom(room, humanMember(r)); err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord", http.StatusSeeOther)
}
