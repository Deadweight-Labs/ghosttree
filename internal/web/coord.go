package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
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

// coordCandidateLabel makes one candidate of an ambiguous @name readable: a bare
// identifier in front of the bracket becomes a neutral name.
func coordCandidateLabel(l lang, candidate string) string {
	name, rest, hasRest := strings.Cut(candidate, " (")
	if looksLikeIdentifier(name, "") || strings.Contains(name, ":") {
		name = neutralIdentityLabel(l, name)
	}
	if hasRest {
		return name + " (" + rest
	}
	return name
}

func coordHTTPError(l lang, w http.ResponseWriter, err error) {
	switch {
	case requestdomain.Code(err) == "secret_detected":
		http.Error(w, l.msg("err.write_secret"), http.StatusUnprocessableEntity)
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
		http.Error(w, l.msg("coord.expiry_invalid"), http.StatusBadRequest)
	case errors.Is(err, store.ErrCoordBodyTooLong):
		http.Error(w, l.msg("coord.body_too_long", store.CoordMaxBody>>10), http.StatusRequestEntityTooLarge)
	case errors.Is(err, store.ErrCoordInvalidSequence):
		http.Error(w, "invalid coordination sequence", http.StatusBadRequest)
	case errors.Is(err, store.ErrCoordUnknownRecipient):
		http.Error(w, l.msg("coord.err.recipient_unknown"), http.StatusBadRequest)
	case errors.Is(err, store.ErrCoordAmbiguousMention):
		var ambiguous *store.AmbiguousMentionError
		errors.As(err, &ambiguous)
		names := make([]string, len(ambiguous.Candidates))
		for i, c := range ambiguous.Candidates {
			names[i] = coordCandidateLabel(l, c)
		}
		http.Error(w, l.msg("coord.mention_ambiguous", ambiguous.Token, strings.Join(names, ", ")), http.StatusBadRequest)
	case errors.Is(err, store.ErrBroadcastLimited):
		var limited *store.BroadcastLimitedError
		errors.As(err, &limited)
		http.Error(w, l.msg("coord.broadcast_limited", int(limited.Wait.Minutes())+1), http.StatusTooManyRequests)
	case errors.Is(err, store.ErrInvalidAttentionAction):
		http.Error(w, l.msg("coord.err.action_invalid"), http.StatusBadRequest)
	case errors.Is(err, store.ErrAttentionRecipientRequired):
		http.Error(w, l.msg("coord.err.recipient_required"), http.StatusBadRequest)
	case errors.Is(err, store.ErrAttentionClosed):
		http.Error(w, l.msg("coord.err.closed"), http.StatusConflict)
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
	l := a.langFor(r)
	var status int
	v := &errorView{BackHref: "/ui/inbox", BackLabel: l.msg("err.back_inbox")}
	switch {
	case errors.Is(err, store.ErrCoordForbidden):
		status = http.StatusForbidden
		v.Icon, v.Title, v.Text = "lock", l.msg("coord.forbidden.title"), l.msg("coord.forbidden.body")
	case errors.Is(err, store.ErrCoordNotFound):
		status = http.StatusNotFound
		v.Icon, v.Title, v.Text = "search", l.msg("coord.notfound.title"), l.msg("coord.notfound.body")
	default:
		coordHTTPError(l, w, err)
		return
	}
	v.Code = codeText(status)
	// A way out that leads somewhere: the rooms when the viewer has any, the
	// inbox otherwise (a guest has no rooms to go back to).
	if summaries, serr := a.browserCoord(r).RoomSummaries(); serr == nil && len(summaries) > 0 {
		v.BackHref, v.BackLabel = "/ui/coord", l.msg("coord.forbidden.back")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	a.renderBrowser(w, r, "errorpage", pageData{Title: v.Title, ErrV: v})
}

func (a *app) mayEnter(w http.ResponseWriter, r *http.Request, room string) bool {
	if _, err := a.browserCoord(r).Room(room); err != nil {
		a.roomPageError(w, r, err)
		return false
	}
	return true
}

func (a *app) coordRoomPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	room := r.URL.Query().Get("room")
	// The project choice is read before the band replaces the project selector.
	selected := selectedProject(a.shellFor(r, "coord"))
	// The room of a project stands in the band of that project.
	if remote, ok := strings.CutPrefix(room, "project:"); ok {
		r = a.repoScoped(r, remote, "rooms")
	}
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
	summaries = coordOrderByActivity(summaries)
	recipients, err := a.browserCoord(r).Recipients()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	attention, err := a.browserCoord(r).Attention()
	if err != nil {
		coordHTTPError(l, w, err)
		return
	}
	current := browserPrincipal(r)
	labels := coordIdentityLabels(l, current, recipients)
	// Agents that wrote an attention item are named through the peers of the
	// room it came from; Peers is ACL-checked, so this adds no hidden identity.
	attentionLabels := coordIdentityLabels(l, current, recipients)
	seenRooms := map[string]bool{}
	for _, item := range attention {
		if item.HomeRoomKey == "" || seenRooms[item.HomeRoomKey] {
			continue
		}
		seenRooms[item.HomeRoomKey] = true
		if peers, err := a.browserCoord(r).Peers(item.HomeRoomKey, ""); err == nil {
			for id, label := range coordIdentityLabels(l, current, nil, peers...) {
				attentionLabels[id] = label
			}
		}
	}
	incomingAttention, outgoingAttention := buildCoordAttentionViews(l, attention, csrfOf(r), attentionLabels)
	sidebar := buildCoordSidebar(l, summaries, humanMember(r), room, labels)
	annotateCoordAttention(l, incomingAttention, attention, sidebar, attentionLabels)
	annotateCoordAttention(l, outgoingAttention, attention, sidebar, attentionLabels)
	a.explainAttention(l, incomingAttention, attention)
	a.explainAttention(l, outgoingAttention, attention)
	// Badge und Aufmerksamkeit zählen alle Räume; nur die Liste folgt dem Projekt.
	sidebar = sidebar.inProject(selected, room)
	if rv := repoFrom(r); rv != nil {
		// In the band of a repository its room is simply "General".
		for i := range sidebar.Projects {
			if sidebar.Projects[i].Key == "project:"+rv.Project {
				sidebar.Projects[i].Name = l.msg("coord.general")
			}
		}
	}
	view := coordPageView{
		Texts:             coordClientTexts(l),
		EventCursor:       a.sealCoordCursor(browserPrincipal(r), eventCursor, time.Now()),
		Sidebar:           sidebar,
		Recipients:        buildCoordRecipientViews(l, recipients),
		IncomingAttention: incomingAttention,
		OutgoingAttention: outgoingAttention,
	}
	if room == "" {
		// The landing is the room with the latest event, never a picker.
		if landing := coordLandingRoom(summaries, sidebar); landing != "" {
			target := coordRoomURL(landing, "", 0)
			if r.URL.Query().Has("project") {
				target = coordRoomProjectURL(landing, strings.TrimSpace(r.URL.Query().Get("project")))
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		a.renderBrowser(w, r, "coord", pageData{Title: l.msg("coord.title"), Coord: view})
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
		coordHTTPError(l, w, err)
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
		coordHTTPError(l, w, err)
		return
	}
	labels = coordIdentityLabels(l, current, recipients, peers...)
	standing, err := access.Standing(room)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	roomThreads, err := access.RoomThreads(room)
	if err != nil {
		coordHTTPError(l, w, err)
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
			Label: coordRoomLabel(l, activeRoom, humanMember(r), labels), URL: coordRoomURL(activeRoom.Key, "", 0), Active: true},
		Messages:     buildCoordMessageViews(l, presentations, room, labels),
		Zone:         time.Now().Format("MST"),
		Participants: othersWaitNeutral(l, buildCoordParticipants(l, activeRoom, peers, memberships, current, labels), askedViewer(attention)),
		WaitCycles:   buildCoordWaitCycles(peers, labels),
		Standing:     buildCoordStandingViews(l, standing, labels), HighWater: page.HighWater,
		HasOlder: page.HasOlder, HasNewer: page.HasNewer,
		CanLeave: activeRoom.Kind == store.RoomGroup,
		FormID:   newCoordFormID(), StandingFormID: newCoordFormID(), Threads: buildCoordThreadViews(l, roomThreads),
	}
	detail.Room.Name = coordShortRoomName(detail.Room.Kind, detail.Room.Label)
	for _, tab := range sidebar.Projects {
		if tab.Key == detail.Room.Key {
			detail.Room.Name = tab.Name
		}
	}
	// Who has a role in the project, for the viewers who may see it; a guest
	// gets nothing here, not even an empty marker.
	if people, perr := access.RoomPeople(room); perr == nil {
		detail.Participants = addRoomPeople(l, detail.Participants, people, current)
	}
	applyParticipantHandles(access, activeRoom.Key, detail.Participants)
	applyParticipantAsks(detail.Participants, attention, activeRoom.Key)
	detail.Participants = orderParticipantsHere(detail.Participants)
	detail.Broadcast = coordCanBroadcast(activeRoom.Kind, detail.Participants)
	applyParticipantRoles(l, a.store, activeRoom.Key, detail.Participants)
	a.applyParticipantControls(r, activeRoom.Key, detail.Participants)
	detail.Here = groupParticipantsHere(l, detail.Participants)
	detail.Stack = stackHere(detail.Here)
	applyMessageRoles(l, a.store, activeRoom.Key, current.ID, detail.Messages, presentations)
	markViewerMentions(detail.Messages, presentations, current.ID)
	detail.CanDirect = true
	detail.CanPost = access.CanPost(room)
	detail.UnreachedNote = coordUnreachedNote(l, r.URL.Query().Get("unreached"), r.URL.Query().Get("unsent") == "1")
	roleRoom := false
	if remote, ok := strings.CutPrefix(activeRoom.Key, "project:"); ok {
		if _, claimed := a.store.ProjectByRemote(remote); claimed {
			roleRoom = true
			detail.CanDirect = coordCanDirect(a.store.ProjectRole(remote, current.ID).Role)
		}
	}
	for i := range detail.Standing {
		detail.Standing[i].Scope = coordStandingScope(l, detail.Standing[i].Targets, detail.Room.Label)
		detail.Standing[i].CanEnd = access.CanEndStanding(activeRoom.Key, detail.Standing[i].MessageID)
	}
	decorateCoordMessages(l, detail.Messages, presentations, current.ID, activeRoom.Key, detail.Room.Label, detail.Standing, append(append([]coordAttentionView{}, incomingAttention...), outgoingAttention...), roleRoom, time.Now())
	a.explainMessages(l, detail.Messages)
	applyRequestStates(l, detail.Messages, presentations, activeRoom.Key, attention, attentionLabels)
	for _, summary := range summaries {
		if summary.Room.Key == room {
			detail.UnreadFrom = firstUnreadSequence(detail.Messages, summary.ReadThrough, summary.ManualUnreadFrom)
		}
	}
	for i := range detail.Messages {
		detail.Messages[i].NewFrom = detail.UnreadFrom > 0 && detail.Messages[i].Sequence == detail.UnreadFrom
	}
	detail.ReplyTo, detail.ReplyTarget, err = coordReplyTarget(l, presentations, r.URL.Query().Get("reply_to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if detail.ReplyTo > 0 {
		for _, presentation := range presentations {
			if presentation.Message.ID != detail.ReplyTo {
				continue
			}
			sender := presentation.Message.SenderExternalID
			if presentation.Message.AuthorKind == store.AuthorHuman {
				sender = presentation.Message.AuthorPrincipalID
			}
			for _, participant := range detail.Participants {
				if participant.ID == sender && !participant.Current && !participant.ListOnly {
					detail.ToDefault = sender
				}
			}
		}
	}
	threadByAnchor := make(map[int64]coordThreadView, len(detail.Threads))
	for _, thread := range detail.Threads {
		threadByAnchor[thread.AnchorMessageID] = thread
	}
	for i := range detail.Messages {
		detail.Messages[i].CSRFToken = csrfOf(r)
		detail.Messages[i].CanReply = detail.CanPost
		if thread, ok := threadByAnchor[detail.Messages[i].ID]; ok && thread.URL != "" {
			detail.Messages[i].ThreadURL = thread.URL
			detail.Messages[i].ThreadTitle = thread.Title
			detail.Messages[i].ThreadMeta = thread.StateText
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
			coordHTTPError(l, w, store.ErrCoordNotFound)
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
			coordHTTPError(l, w, loadErr)
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
			coordThreadView: *selected, Messages: buildCoordThreadMessageViews(l, presentations, room, selectedID, labels), FormID: newCoordFormID(),
			HighWater: threadPage.HighWater, HasOlder: threadPage.HasOlder, HasNewer: threadPage.HasNewer,
			ClearReplyURL: coordThreadComposerURL(room, selectedID),
		}
		markViewerMentions(threadDetail.Messages, presentations, current.ID)
		applyMessageRoles(l, a.store, activeRoom.Key, current.ID, threadDetail.Messages, presentations)
		allAttention := append(append([]coordAttentionView{}, incomingAttention...), outgoingAttention...)
		decorateCoordMessages(l, threadDetail.Messages, presentations, current.ID, activeRoom.Key, detail.Room.Label, detail.Standing, allAttention, roleRoom, time.Now(), selectedID)
		a.explainMessages(l, threadDetail.Messages)
		applyRequestStatesIn(l, threadDetail.Messages, presentations, store.DestinationDiscussion, store.ThreadDestinationID(selectedID), attention, attentionLabels)
		for i := range threadDetail.Messages {
			threadDetail.Messages[i].CanReply = detail.CanPost
			threadDetail.Messages[i].CSRFToken = csrfOf(r)
		}
		threadDetail.Anchor = a.coordThreadAnchor(r, l, access, room, selected, detail, labels, attention, attentionLabels, allAttention, roleRoom)
		threadDetail.ReplyTo, threadDetail.ReplyTarget, parseErr = coordReplyTarget(l, presentations, r.URL.Query().Get("thread_reply_to"))
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
		detail.PrivatePeers = coordJoinNames(l, peers)
		detail.PrivateNote = l.msg("coord.private_note")
		if detail.PrivatePeers != "" {
			detail.PrivateNote = l.msg("coord.private_note_with", detail.PrivatePeers)
		}
	}
	for _, membership := range memberships {
		if membership.PrincipalID == humanMember(r) && membership.LeftAt == "" && membership.Manager {
			detail.CanManage = true
		}
	}
	if detail.Thread != nil && detail.CanPost {
		for _, item := range roomThreads {
			if item.Thread.ID == detail.Thread.ID {
				detail.Thread.CanSetState = a.browserCoord(r).CanSetThreadState(item.Thread.ID)
			}
		}
	}
	detail.Draft = shortWords(r.URL.Query().Get("draft"), 500)
	view.Active = detail
	data := pageData{Title: l.msg("page.coordination"), Coord: view}
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
	l := a.langFor(r)
	threadID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || threadID <= 0 {
		http.Error(w, "coordination target not found", http.StatusNotFound)
		return
	}
	home, err := a.browserCoord(r).ThreadHome(threadID)
	if err != nil {
		coordHTTPError(l, w, err)
		return
	}
	http.Redirect(w, r, coordThreadURL(home.RoomKey, threadID), http.StatusSeeOther)
}

func (a *app) coordCreateThread(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
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
		coordHTTPError(l, w, err)
		return
	}
	home, err := a.browserCoord(r).ThreadHome(threadID)
	if err != nil {
		coordHTTPError(l, w, err)
		return
	}
	http.Redirect(w, r, coordThreadURL(home.RoomKey, threadID), http.StatusSeeOther)
}

func (a *app) coordPostThread(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	threadID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("thread_id")), 10, 64)
	body := strings.TrimSpace(r.FormValue("body"))
	// The status row of the composer: a change of state may come with a reply or alone.
	state := strings.TrimSpace(r.FormValue("state"))
	if state == strings.TrimSpace(r.FormValue("state_was")) {
		state = ""
	}
	if err != nil || threadID <= 0 || (body == "" && state == "") {
		http.Error(w, l.msg("coord.thread_need_text"), http.StatusBadRequest)
		return
	}
	// A state change comes after the reply: a reply that fails must not leave
	// the thread already closed. Reply and intent are checked first, the
	// state is set only once the post went through.
	var (
		replyTo  int64
		intent   string
		mentions []string
	)
	if body != "" {
		replyTo, err = coordOptionalReplyTo(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		intent = strings.TrimSpace(r.FormValue("intent"))
		if !validCoordComposerIntent(intent) {
			http.Error(w, "a valid intent is required", http.StatusBadRequest)
			return
		}
		mentions, body = coordBroadcastChip(splitCoordPrincipals(r.Form["mentions"]...), body)
	}
	unsent := false
	if body != "" {
		_, err = a.browserCoord(r).ThreadPost(threadID, store.CoordMessage{ClientID: newFormClientID(r), Body: body, Intent: intent, Mentions: mentions, ReplyTo: replyTo})
		unsent = errors.Is(err, store.ErrAttentionRecipientRequired) && store.BodyHasBroadcast(body)
		if err != nil && !unsent {
			coordHTTPError(l, w, err)
			return
		}
	}
	if state != "" && !unsent {
		if err := a.browserCoord(r).SetThreadState(threadID, state, gitanchor.Web()); err != nil {
			coordHTTPError(l, w, err)
			return
		}
	}
	if body == "" {
		a.redirectToThread(w, r, l, threadID)
		return
	}
	home, err := a.browserCoord(r).ThreadHome(threadID)
	if err != nil {
		coordHTTPError(l, w, err)
		return
	}
	target := coordThreadURL(home.RoomKey, threadID)
	if unsent {
		// A question to "all agents" while none is active stores nothing; the
		// thread says so instead of showing an error page.
		target = strings.TrimSuffix(target, "#coord-thread") + "&unreached=agents&unsent=1#coord-thread"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *app) coordSetThreadState(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	threadID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("thread_id")), 10, 64)
	if err != nil || threadID <= 0 {
		http.Error(w, "thread is required", http.StatusBadRequest)
		return
	}
	if err := a.browserCoord(r).SetThreadState(threadID, strings.TrimSpace(r.FormValue("state")), gitanchor.Web()); err != nil {
		coordHTTPError(l, w, err)
		return
	}
	a.redirectToThread(w, r, l, threadID)
}

// redirectToThread sends the browser back to the thread it came from.
func (a *app) redirectToThread(w http.ResponseWriter, r *http.Request, l lang, threadID int64) {
	home, err := a.browserCoord(r).ThreadHome(threadID)
	if err != nil {
		coordHTTPError(l, w, err)
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
	l := a.langFor(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room := strings.TrimSpace(r.FormValue("room"))
	body := strings.TrimSpace(r.FormValue("body"))
	if room == "" || body == "" {
		http.Error(w, l.msg("coord.err.body_required"), http.StatusBadRequest)
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
		coordHTTPError(l, w, store.ErrCoordInvalidExpiry)
		return
	}
	msg := store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room,
		ClientID:  newFormClientID(r),
		Body:      body,
		ReplyTo:   replyTo,
		ExpiresAt: expiresAt,
	}
	msg.Mentions, msg.Body = coordBroadcastChip(splitCoordPrincipals(r.Form["mentions"]...), msg.Body)
	// The form may carry the kind twice: the select and the button "as a request"
	// that submitted it. The button comes last and wins.
	if kinds := r.Form["intent"]; len(kinds) > 0 {
		msg.Intent = strings.TrimSpace(kinds[len(kinds)-1])
	}
	if !validCoordComposerIntent(msg.Intent) {
		http.Error(w, "a valid intent is required", http.StatusBadRequest)
		return
	}

	mid, err := a.browserCoord(r).Send(msg)
	if err == nil {
		a.stampActor(r, actor.Message, mid)
	}
	if errors.Is(err, store.ErrAttentionRecipientRequired) && store.BodyHasBroadcast(msg.Body) {
		// Frage oder Blocker an "alle aktiven Agenten", aber niemand ist aktiv:
		// nichts wurde gesendet. Das ist ein Hinweis unter dem Raum, keine Fehlerseite.
		http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room)+"&unreached=agents&unsent=1", http.StatusSeeOther)
		return
	}
	if err != nil {
		coordHTTPError(l, w, err)
		return
	}
	target := "/ui/coord?room=" + url.QueryEscape(room)
	// A failed lookup only costs the hint; the message is stored.
	if unreached, uerr := a.browserCoord(r).UnreachedMentions(room, body); uerr == nil && len(unreached) > 0 {
		target += "&unreached=" + url.QueryEscape(strings.Join(unreached, ","))
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

var unreachedTokenPattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._:-]{0,63}$`)

// coordUnreachedNote is the hint under the room after a send, built from the
// query so that only well-formed @names are echoed.
func coordUnreachedNote(l lang, raw string, unsent bool) string {
	var names []string
	for _, token := range strings.Split(raw, ",") {
		if unreachedTokenPattern.MatchString(token) && len(names) < 5 {
			names = append(names, "@"+token)
		}
	}
	if len(names) == 0 {
		return ""
	}
	if unsent {
		return l.msg("coord.unreached_unsent", strings.Join(names, ", "))
	}
	return l.msg("coord.unreached", strings.Join(names, ", "))
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

// maxAttentionActionIDs caps how many items one attention action may name.
const maxAttentionActionIDs = 10

func (a *app) coordAttentionAction(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// One card can carry several items (one per addressee); its buttons name all of them.
	// The list is deduplicated and capped: a card never holds more than a few addressees.
	var ids []int64
	seen := map[int64]bool{}
	for _, raw := range r.Form["attention_id"] {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "attention item is required", http.StatusBadRequest)
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > maxAttentionActionIDs {
		http.Error(w, "attention item is required", http.StatusBadRequest)
		return
	}
	access := a.browserCoord(r)
	action := strings.TrimSpace(r.FormValue("action"))
	if len(ids) > 1 {
		// Check every id against what this person may act on before resolving
		// any: a foreign id in the list stops the whole action and answers like
		// an id that does not exist, so nothing is resolved and nothing is told.
		items, err := access.Attention()
		if err != nil {
			coordHTTPError(l, w, err)
			return
		}
		mine := make(map[int64]bool, len(items))
		for _, item := range items {
			// The list also holds items this person only sent: they may withdraw
			// those, but not answer them.
			mine[item.ID] = item.IsRecipient && action != store.AttentionActionWithdraw || item.CanWithdraw && action == store.AttentionActionWithdraw
		}
		for _, id := range ids {
			if !mine[id] {
				coordHTTPError(l, w, store.ErrCoordNotFound)
				return
			}
		}
	}
	for _, id := range ids {
		err := access.ResolveAttention(id, action)
		if errors.Is(err, store.ErrAttentionClosed) && len(ids) > 1 {
			// Another addressee answered first: already done, not an error.
			continue
		}
		if err != nil {
			coordHTTPError(l, w, err)
			return
		}
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
	l := a.langFor(r)
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
		coordHTTPError(l, w, store.ErrCoordInvalidExpiry)
		return
	}
	_, err = a.browserCoord(r).CreateStanding(store.StandingInput{RoomKey: room, ClientID: newFormClientID(r), Body: body, ExpiresAt: expiresAt, Mentions: mentions})
	if err != nil {
		coordHTTPError(l, w, err)
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
	l := a.langFor(r)
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
		coordHTTPError(l, w, err)
		return
	}
	http.Redirect(w, r, backTo(r, "/ui/coord?room="+url.QueryEscape(room)), http.StatusSeeOther)
}

// coordEndStanding beendet eine Vorgabe. Ausdrücklich und von einem
// angemeldeten Menschen — eine geltende Einschränkung endet mit ihrer
// Aufgabe oder durch diese Geste, nicht dadurch, dass genug Nachrichten
// darüber hinweggelaufen sind.
func (a *app) coordEndStanding(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
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
		coordHTTPError(l, w, err)
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
	l := a.langFor(r)
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
		coordHTTPError(l, w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room.Key), http.StatusSeeOther)
}

func (a *app) coordCreateGroup(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
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
		coordHTTPError(l, w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room.Key), http.StatusSeeOther)
}

func (a *app) coordUpdateGroup(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
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
		coordHTTPError(l, w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(room), http.StatusSeeOther)
}

func (a *app) coordLeaveGroup(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
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
		coordHTTPError(l, w, err)
		return
	}
	http.Redirect(w, r, "/ui/coord", http.StatusSeeOther)
}

// applyParticipantHandles gives each participant the @name the composer
// completes. A failure leaves the composer without suggestions; typing a name
// by hand keeps working.
func applyParticipantHandles(access store.CoordAccess, roomKey string, participants []coordParticipantView) {
	handles, err := access.MentionHandles(roomKey)
	if err != nil {
		return
	}
	for i := range participants {
		if !participants[i].Current {
			participants[i].Handle = handles[participants[i].ID]
		}
	}
}

// coordBroadcastToken is the value of the composer chip that addresses every
// active agent of the room. It is not a principal: the server resolves the
// @agents token in the text, so the chip only writes it.
const coordBroadcastToken = "@agents"

// coordBroadcastValue is the form value of that chip.
const coordBroadcastValue = "all-agents"

// coordBroadcastChip takes the broadcast chip out of the mentions and puts the
// token in front of the text, unless the text already carries it.
func coordBroadcastChip(mentions []string, body string) ([]string, string) {
	kept := mentions[:0:0]
	chip := false
	for _, m := range mentions {
		if m == coordBroadcastValue {
			chip = true
			continue
		}
		kept = append(kept, m)
	}
	if chip && !strings.Contains(strings.ToLower(body), coordBroadcastToken) && !strings.Contains(strings.ToLower(body), "@agenten") {
		body = coordBroadcastToken + " " + body
	}
	return kept, body
}

// coordCanBroadcast says whether the composer offers the chip: project and
// machine rooms with at least one agent the viewer may address.
func coordCanBroadcast(kind string, participants []coordParticipantView) bool {
	if kind != store.RoomProject && kind != store.RoomMachine {
		return false
	}
	for _, p := range participants {
		if !p.Current && !p.ListOnly && !p.Human {
			return true
		}
	}
	return false
}

// coordThreadAnchor is the message a thread was started from, shown at its top.
// It is taken from the window of the room when it is there, else loaded on its
// own: an old anchor must not vanish from its thread.
func (a *app) coordThreadAnchor(r *http.Request, l lang, access store.CoordAccess, room string, thread *coordThreadView, detail *coordRoomDetailView, labels map[string]string, attention []store.AttentionItem, attentionLabels map[string]string, allAttention []coordAttentionView, roleRoom bool) *coordMessageView {
	if thread.AnchorMessageID == 0 {
		return nil
	}
	var anchor *coordMessageView
	for i := range detail.Messages {
		if detail.Messages[i].ID == thread.AnchorMessageID {
			copyOf := detail.Messages[i]
			// The "New" divider belongs to the room's stream, not above the anchor.
			copyOf.NewFrom = false
			anchor = &copyOf
		}
	}
	if anchor == nil && thread.AnchorSequence > 0 {
		_, pres, err := access.MessagePresentationWindow(store.DestinationRoom, room, store.BeforeWindow(thread.AnchorSequence+1, 1))
		if err != nil || len(pres) != 1 || pres[0].Message.ID != thread.AnchorMessageID {
			return nil
		}
		views := buildCoordMessageViews(l, pres, room, labels)
		current := browserPrincipal(r)
		applyMessageRoles(l, a.store, room, current.ID, views, pres)
		markViewerMentions(views, pres, current.ID)
		decorateCoordMessages(l, views, pres, current.ID, room, detail.Room.Label, detail.Standing, allAttention, roleRoom, time.Now())
		a.explainMessages(l, views)
		applyRequestStates(l, views, pres, room, attention, attentionLabels)
		views[0].CSRFToken = csrfOf(r)
		anchor = &views[0]
	}
	if anchor == nil {
		return nil
	}
	anchor.ThreadAnchor, anchor.GroupStart = true, true
	anchor.DOMID = "thread-anchor-" + strconv.FormatInt(anchor.Sequence, 10)
	anchor.ReplyCount, anchor.ThreadURL, anchor.CanReply, anchor.CanPromote = 0, "", false, false
	return anchor
}
