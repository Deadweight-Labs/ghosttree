package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	case errors.Is(err, store.ErrAnchorAlreadyThreaded):
		http.Error(w, "coordination message already has a different task thread", http.StatusConflict)
	case errors.Is(err, store.ErrCoordInvalidExpiry):
		http.Error(w, errCoordExpiryInvalid.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrCoordInvalidSequence):
		http.Error(w, "invalid coordination sequence", http.StatusBadRequest)
	case errors.Is(err, store.ErrCoordUnknownRecipient):
		http.Error(w, "coordination recipient is not available", http.StatusBadRequest)
	case errors.Is(err, store.ErrInvalidAttentionAction):
		http.Error(w, "invalid coordination attention action", http.StatusBadRequest)
	case errors.Is(err, store.ErrAttentionRecipientRequired):
		http.Error(w, "coordination attention requires a recipient", http.StatusBadRequest)
	case errors.Is(err, store.ErrAttentionClosed):
		http.Error(w, "coordination attention item is already closed", http.StatusConflict)
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
// roomPageError answers a room page that may not be read with a designed
// error page; every other failure keeps its plain text. The not-found page is
// one fixed text for a room that does not exist and for one in a project the
// viewer may not see (#2447): nothing on it depends on the room.
func (a *app) roomPageError(w http.ResponseWriter, r *http.Request, err error) {
	page, status, title := "", 0, ""
	switch {
	case errors.Is(err, store.ErrCoordForbidden):
		page, status, title = "roomforbidden", http.StatusForbidden, msg("coord.forbidden.title")
	case errors.Is(err, store.ErrCoordNotFound):
		page, status, title = "roomnotfound", http.StatusNotFound, msg("coord.notfound.title")
	default:
		coordHTTPError(w, err)
		return
	}
	// A way out that leads somewhere: the rooms when the viewer has any, the
	// overview otherwise (a guest has no rooms to go back to).
	back := "/ui/overview"
	if summaries, serr := a.browserCoord(r).RoomSummaries(); serr == nil && len(summaries) > 0 {
		back = "/ui/coord"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	a.renderBrowser(w, r, page, pageData{Title: title, BackURL: back})
}

func (a *app) mayEnter(w http.ResponseWriter, r *http.Request, room string) bool {
	if _, err := a.browserCoord(r).Room(room); err != nil {
		a.roomPageError(w, r, err)
		return false
	}
	return true
}

func (a *app) coordRoomPage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	eventCursor, err := a.store.LatestCoordEventSequence()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	attention, err := a.browserCoord(r).Attention()
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	current := browserPrincipal(r)
	labels := coordIdentityLabels(current, recipients)
	// Agents that wrote an attention item are named through the peers of the
	// room it came from; Peers is ACL-checked, so this adds no hidden identity.
	attentionLabels := coordIdentityLabels(current, recipients)
	seenRooms := map[string]bool{}
	for _, item := range attention {
		if item.HomeRoomKey == "" || seenRooms[item.HomeRoomKey] {
			continue
		}
		seenRooms[item.HomeRoomKey] = true
		if peers, err := a.browserCoord(r).Peers(item.HomeRoomKey, ""); err == nil {
			for id, label := range coordIdentityLabels(current, nil, peers...) {
				attentionLabels[id] = label
			}
		}
	}
	incomingAttention, outgoingAttention := buildCoordAttentionViews(attention, csrfOf(r), attentionLabels)
	sidebar := buildCoordSidebar(summaries, humanMember(r), room, labels)
	annotateCoordAttention(incomingAttention, attention, sidebar, attentionLabels)
	annotateCoordAttention(outgoingAttention, attention, sidebar, attentionLabels)
	view := coordPageView{
		Texts:             coordClientTexts(),
		EventCursor:       a.sealCoordCursor(browserPrincipal(r), eventCursor, time.Now()),
		Sidebar:           sidebar,
		Recipients:        buildCoordRecipientViews(recipients),
		IncomingAttention: incomingAttention,
		OutgoingAttention: outgoingAttention,
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
		a.roomPageError(w, r, err)
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
	if errors.Is(err, store.ErrCoordNotFound) {
		// Der Raum ist lesbar (mayEnter), die Agentenliste nicht: ein Gast sieht
		// die Nachrichten ohne Mitglieder.
		peers, err = nil, nil
	}
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	labels = coordIdentityLabels(current, recipients, peers...)
	standing, err := access.Standing(room)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	roomThreads, err := access.RoomThreads(room)
	if err != nil {
		coordHTTPError(w, err)
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
			Label: coordRoomLabel(activeRoom, humanMember(r), labels), URL: coordRoomURL(activeRoom.Key, "", 0), Active: true},
		Messages:     buildCoordMessageViews(presentations, room, labels),
		Zone:         time.Now().Format("MST"),
		Participants: buildCoordParticipants(activeRoom, peers, memberships, current, labels),
		WaitCycles:   buildCoordWaitCycles(peers, labels),
		Standing:     buildCoordStandingViews(standing, labels), HighWater: page.HighWater,
		HasOlder: page.HasOlder, HasNewer: page.HasNewer,
		CanLeave: activeRoom.Kind == store.RoomGroup,
		FormID:   newCoordFormID(), StandingFormID: newCoordFormID(), Threads: buildCoordThreadViews(roomThreads),
	}
	detail.Room.Name = coordShortRoomName(detail.Room.Kind, detail.Room.Label)
	applyParticipantRoles(a.store, activeRoom.Key, detail.Participants)
	a.applyParticipantControls(r, activeRoom.Key, detail.Participants)
	applyMessageRoles(a.store, activeRoom.Key, detail.Messages, presentations)
	markViewerMentions(detail.Messages, presentations, current.ID)
	detail.CanDirect = true
	detail.CanPost = access.CanPost(room)
	roleRoom := false
	if remote, ok := strings.CutPrefix(activeRoom.Key, "project:"); ok {
		if _, claimed := a.store.ProjectByRemote(remote); claimed {
			roleRoom = true
			detail.CanDirect = coordCanDirect(a.store.ProjectRole(remote, current.ID).Role)
		}
	}
	for i := range detail.Standing {
		detail.Standing[i].Scope = coordStandingScope(detail.Standing[i].Targets, detail.Room.Label)
		detail.Standing[i].CanEnd = access.CanEndStanding(activeRoom.Key, detail.Standing[i].MessageID)
	}
	decorateCoordMessages(detail.Messages, presentations, current.ID, activeRoom.Key, detail.Room.Label, detail.Standing, append(append([]coordAttentionView{}, incomingAttention...), outgoingAttention...), roleRoom, time.Now())
	applyRequestStates(detail.Messages, presentations, activeRoom.Key, attention, attentionLabels)
	detail.ReplyTo, detail.ReplyTarget, err = coordReplyTarget(presentations, r.URL.Query().Get("reply_to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	threadByAnchor := make(map[int64]coordThreadView, len(detail.Threads))
	for _, thread := range detail.Threads {
		threadByAnchor[thread.AnchorMessageID] = thread
	}
	for i := range detail.Messages {
		detail.Messages[i].CSRFToken = csrfOf(r)
		if thread, ok := threadByAnchor[detail.Messages[i].ID]; ok && thread.URL != "" {
			detail.Messages[i].ThreadURL = thread.URL
			detail.Messages[i].ThreadTitle = thread.Title
			detail.Messages[i].ThreadMeta = thread.State
		} else {
			detail.Messages[i].CanPromote = detail.CanPost
		}
	}
	if selectedText := strings.TrimSpace(r.URL.Query().Get("thread")); selectedText != "" {
		selectedID, parseErr := strconv.ParseInt(selectedText, 10, 64)
		if parseErr != nil || selectedID <= 0 {
			http.Error(w, "thread must be a positive id", http.StatusBadRequest)
			return
		}
		var selected *coordThreadView
		for i := range detail.Threads {
			if detail.Threads[i].ID == selectedID {
				selected = &detail.Threads[i]
				break
			}
		}
		if selected == nil {
			coordHTTPError(w, store.ErrCoordNotFound)
			return
		}
		threadWindow, parseErr := coordThreadWindowFromRequest(r)
		if parseErr != nil {
			http.Error(w, parseErr.Error(), http.StatusBadRequest)
			return
		}
		threadAround, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("thread_around")), 10, 64)
		threadPage, presentations, loadErr := access.MessagePresentationWindow(store.DestinationDiscussion, store.ThreadDestinationID(selectedID), threadWindow)
		if loadErr != nil {
			coordHTTPError(w, loadErr)
			return
		}
		if threadAround > 0 {
			found := false
			for _, message := range threadPage.Messages {
				if message.Sequence == threadAround {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "thread_around must name a retained message sequence", http.StatusBadRequest)
				return
			}
		}
		threadDetail := &coordThreadDetailView{
			coordThreadView: *selected, Messages: buildCoordThreadMessageViews(presentations, room, selectedID, labels), FormID: newCoordFormID(),
			HighWater: threadPage.HighWater, HasOlder: threadPage.HasOlder, HasNewer: threadPage.HasNewer,
			ClearReplyURL: coordThreadComposerURL(room, selectedID),
		}
		markViewerMentions(threadDetail.Messages, presentations, current.ID)
		for i := range threadDetail.Messages {
			message := presentations[i].Message
			threadDetail.Messages[i].Own = message.AuthorKind == store.AuthorHuman && message.AuthorPrincipalID == current.ID
		}
		threadDetail.ReplyTo, threadDetail.ReplyTarget, parseErr = coordReplyTarget(presentations, r.URL.Query().Get("thread_reply_to"))
		if parseErr != nil {
			http.Error(w, parseErr.Error(), http.StatusBadRequest)
			return
		}
		var threadBefore, threadAfter int64
		if len(threadPage.Messages) > 0 {
			threadDetail.FirstSequence = threadPage.Messages[0].Sequence
			threadDetail.LastSequence = threadPage.Messages[len(threadPage.Messages)-1].Sequence
			threadBefore = threadDetail.FirstSequence
			threadAfter = threadDetail.LastSequence
		} else if threadPage.HasOlder {
			threadBefore = threadPage.HighWater
		}
		if threadDetail.HasOlder {
			threadDetail.OlderURL = coordThreadPageURL(room, selectedID, "thread_before", threadBefore)
		}
		if threadDetail.HasNewer {
			threadDetail.NewerURL = coordThreadPageURL(room, selectedID, "thread_after", threadAfter)
		}
		detail.Thread = threadDetail
	}
	if activeRoom.Kind == store.RoomDirect || activeRoom.Kind == store.RoomGroup {
		var peers []string
		for _, participant := range detail.Participants {
			if !participant.Current {
				peers = append(peers, participant.Label)
			}
		}
		detail.PrivatePeers = coordJoinNames(peers)
		detail.PrivateNote = msg("coord.private_note")
		if detail.PrivatePeers != "" {
			detail.PrivateNote = msg("coord.private_note_with", detail.PrivatePeers)
		}
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

func (a *app) coordThreadPage(w http.ResponseWriter, r *http.Request) {
	threadID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || threadID <= 0 {
		http.Error(w, "coordination target not found", http.StatusNotFound)
		return
	}
	home, err := a.browserCoord(r).ThreadHome(threadID)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, coordThreadURL(home.RoomKey, threadID), http.StatusSeeOther)
}

func (a *app) coordCreateThread(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	anchorText := strings.TrimSpace(r.FormValue("anchor_message_id"))
	var anchor int64
	var err error
	if anchorText != "" {
		anchor, err = strconv.ParseInt(anchorText, 10, 64)
		if err != nil || anchor <= 0 {
			http.Error(w, "anchor message is invalid", http.StatusBadRequest)
			return
		}
	}
	var threadID int64
	if anchor > 0 {
		threadID, err = a.browserCoord(r).PromoteRoomMessageToTaskThread(anchor, r.FormValue("title"), r.FormValue("question"), r.FormValue("request_id"))
	} else {
		threadID, err = a.browserCoord(r).CreateTaskThreadInRoom(strings.TrimSpace(r.FormValue("room")), r.FormValue("title"), r.FormValue("question"), r.FormValue("request_id"))
	}
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	home, err := a.browserCoord(r).ThreadHome(threadID)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, coordThreadURL(home.RoomKey, threadID), http.StatusSeeOther)
}

func (a *app) coordPostThread(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	threadID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("thread_id")), 10, 64)
	body := strings.TrimSpace(r.FormValue("body"))
	if err != nil || threadID <= 0 || body == "" {
		http.Error(w, "thread and body are required", http.StatusBadRequest)
		return
	}
	replyTo, err := coordOptionalReplyTo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	intent := strings.TrimSpace(r.FormValue("intent"))
	mentions := splitCoordPrincipals(r.Form["mentions"]...)
	if !validCoordComposerIntent(intent) {
		http.Error(w, "a valid intent is required", http.StatusBadRequest)
		return
	}
	if _, err := a.browserCoord(r).ThreadPost(threadID, store.CoordMessage{ClientID: newFormClientID(r), Body: body, Intent: intent, Mentions: mentions, ReplyTo: replyTo}); err != nil {
		coordHTTPError(w, err)
		return
	}
	home, err := a.browserCoord(r).ThreadHome(threadID)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, coordThreadURL(home.RoomKey, threadID), http.StatusSeeOther)
}

func (a *app) coordSetThreadState(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	threadID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("thread_id")), 10, 64)
	if err != nil || threadID <= 0 {
		http.Error(w, "thread is required", http.StatusBadRequest)
		return
	}
	if err := a.browserCoord(r).SetThreadState(threadID, strings.TrimSpace(r.FormValue("state"))); err != nil {
		coordHTTPError(w, err)
		return
	}
	home, err := a.browserCoord(r).ThreadHome(threadID)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, coordThreadURL(home.RoomKey, threadID), http.StatusSeeOther)
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

func coordThreadWindowFromRequest(r *http.Request) (store.MessageWindow, error) {
	beforeText := strings.TrimSpace(r.URL.Query().Get("thread_before"))
	afterText := strings.TrimSpace(r.URL.Query().Get("thread_after"))
	aroundText := strings.TrimSpace(r.URL.Query().Get("thread_around"))
	set := 0
	for _, value := range []string{beforeText, afterText, aroundText} {
		if value != "" {
			set++
		}
	}
	if set > 1 {
		return store.MessageWindow{}, errors.New("thread_before, thread_after and thread_around are mutually exclusive")
	}
	parsePositive := func(name, value string) (int64, error) {
		sequence, err := strconv.ParseInt(value, 10, 64)
		if err != nil || sequence <= 0 {
			return 0, errors.New(name + " must be a positive sequence")
		}
		return sequence, nil
	}
	if beforeText != "" {
		sequence, err := parsePositive("thread_before", beforeText)
		if err != nil {
			return store.MessageWindow{}, err
		}
		return store.BeforeWindow(sequence, 50), nil
	}
	if afterText != "" {
		sequence, err := parsePositive("thread_after", afterText)
		if err != nil {
			return store.MessageWindow{}, err
		}
		return store.AfterWindow(sequence, 50), nil
	}
	if aroundText != "" {
		sequence, err := parsePositive("thread_around", aroundText)
		if err != nil {
			return store.MessageWindow{}, err
		}
		start := sequence - 25
		if start < 0 {
			start = 0
		}
		return store.AfterWindow(start, 50), nil
	}
	return store.LatestWindow(50), nil
}

func coordOptionalReplyTo(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.FormValue("reply_to"))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("reply_to must be a positive message id")
	}
	return id, nil
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
	replyTo, err := coordOptionalReplyTo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	expiresAt, err := parseCoordExpiry(r.FormValue("expires_at"), r.FormValue("expires_offset"), time.Local)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	msg := store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		ClientID:  newFormClientID(r),
		Body:      body,
		ReplyTo:   replyTo,
		ExpiresAt: expiresAt,
	}
	msg.Mentions = splitCoordPrincipals(r.Form["mentions"]...)
	msg.Intent = strings.TrimSpace(r.FormValue("intent"))
	if !validCoordComposerIntent(msg.Intent) {
		http.Error(w, "a valid intent is required", http.StatusBadRequest)
		return
	}

	_, err = a.browserCoord(r).Send(msg)
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room), http.StatusSeeOther)
}

func validCoordComposerIntent(intent string) bool {
	return intent == "" || isAttentionIntent(intent)
}

func isAttentionIntent(intent string) bool {
	switch intent {
	case store.IntentQuestion, store.IntentApproval, store.IntentBlocker, store.IntentHandoff:
		return true
	default:
		return false
	}
}

func (a *app) coordAttentionAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("attention_id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "attention item is required", http.StatusBadRequest)
		return
	}
	if err := a.browserCoord(r).ResolveAttention(id, strings.TrimSpace(r.FormValue("action"))); err != nil {
		coordHTTPError(w, err)
		return
	}
	location := "/ui/coord"
	if room := strings.TrimSpace(r.FormValue("room")); room != "" {
		if _, err := a.browserCoord(r).Room(room); err == nil {
			location = coordRoomURL(room, "", 0)
		}
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
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
	expiresAt, err := parseCoordExpiry(r.FormValue("expires_at"), r.FormValue("expires_offset"), time.Local)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err = a.browserCoord(r).CreateStanding(store.StandingInput{RoomKey: room, ClientID: newFormClientID(r), Body: body, ExpiresAt: expiresAt, Mentions: mentions})
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
