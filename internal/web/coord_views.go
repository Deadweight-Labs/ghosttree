package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type coordPageView struct {
	EventCursor string
	// Texts is the JSON the script reads its status lines from.
	Texts             string
	Sidebar           coordSidebarView
	Active            *coordRoomDetailView
	Recipients        []coordRecipientView
	IncomingAttention []coordAttentionView
	OutgoingAttention []coordAttentionView
}

const coordAttentionVisible = 3

// FirstRoom is the room the empty page offers: the first tab.
func (v coordPageView) FirstRoom() *coordRoomView {
	for _, section := range [][]coordRoomView{v.Sidebar.Projects, v.Sidebar.Machines, v.Sidebar.Private} {
		if len(section) > 0 {
			return &section[0]
		}
	}
	return nil
}

// IncomingHead is what "Needs you" shows at once; IncomingRest folds away.
func (v coordPageView) IncomingHead() []coordAttentionView {
	if len(v.IncomingAttention) <= coordAttentionVisible {
		return v.IncomingAttention
	}
	return v.IncomingAttention[:coordAttentionVisible]
}

func (v coordPageView) IncomingRest() []coordAttentionView {
	if len(v.IncomingAttention) <= coordAttentionVisible {
		return nil
	}
	return v.IncomingAttention[coordAttentionVisible:]
}

type coordRecipientView struct{ ID, Label, Kind, Option string }

type coordSidebarView struct {
	Attention []coordRoomView
	Machines  []coordRoomView
	Projects  []coordRoomView
	Private   []coordRoomView
}

// BadgeCount is the one number shown on the narrow-layout rooms button:
// every message that needs the viewer (open attention or unread mention, each
// counted once) wins over plain unread messages.
func (s coordSidebarView) BadgeCount() int64 {
	var needs, unread int64
	for _, room := range s.Attention {
		needs += room.NeedsYou
		unread += room.Unread
	}
	if needs > 0 {
		return needs
	}
	return unread
}

func (s coordSidebarView) BadgeLabel() string {
	for _, room := range s.Attention {
		if room.NeedsYou > 0 {
			return msg("coord.badge_needs")
		}
	}
	return msg("coord.badge_unread")
}

// room finds a room by key across every sidebar section.
func (s coordSidebarView) room(key string) (coordRoomView, bool) {
	for _, section := range [][]coordRoomView{s.Machines, s.Projects, s.Private} {
		for _, room := range section {
			if room.Key == key {
				return room, true
			}
		}
	}
	return coordRoomView{}, false
}

type coordRoomView struct {
	Key, Kind, Label, URL string
	// Name is what the sidebar shows; Label stays the full title.
	Name                        string
	Unread, Mentions, Attention int64
	// NeedsYou counts each message once that mentions or awaits the viewer.
	NeedsYou int64
	Active   bool
}

type coordRoomDetailView struct {
	Room         coordRoomView
	Messages     []coordMessageView
	Participants []coordParticipantView
	// WaitCycles: je Kreis gegenseitigen Wartens eine Zeile, aus der Presence
	// der Peers (Peers ist ACL-geprüft, ein Gast bekommt keine Liste).
	WaitCycles          []string
	Standing            []coordStandingView
	HighWater           int64
	FirstSequence       int64
	LastSequence        int64
	OlderURL, NewerURL  string
	HasOlder, HasNewer  bool
	CanManage, CanLeave bool
	// CanDirect: the viewer's rank lets a standing instruction be a directive;
	// below it the same gesture is a standing request.
	CanDirect              bool
	FormID, StandingFormID string
	Zone                   string
	ReplyTo                int64
	ReplyTarget            *coordReplyView
	Threads                []coordThreadView
	Thread                 *coordThreadDetailView
	PrivateNote            string
	PrivatePeers           string
}

type coordThreadView struct {
	ID, AnchorMessageID                               int64
	Title, Question, State, URL, AnchorURL            string
	Archived                                          bool
	RequestID, RequestTitle, RequestState, RequestURL string
}

type coordThreadDetailView struct {
	coordThreadView
	Messages                               []coordMessageView
	FirstSequence, LastSequence, HighWater int64
	OlderURL, NewerURL                     string
	ClearReplyURL                          string
	HasOlder, HasNewer                     bool
	FormID                                 string
	ReplyTo                                int64
	ReplyTarget                            *coordReplyView
}

type coordMessageView struct {
	ID, Sequence, ReplyCount      int64
	Author, AuthorKind, Timestamp string
	// SenderRole ist die Rolle des Absenders im Projekt des Raums, live vom
	// Server berechnet; leer außerhalb von Projekträumen.
	SenderRole       string
	DisplayTimestamp string
	Body, Intent     string
	Expired          bool
	Reply            *coordReplyView
	Mentions         []string
	Refs             []coordRefView
	Delivery         string
	MentionsViewer   bool
	ThreadURL        string
	ReplyURL         string
	CanPromote       bool
	CSRFToken        string
	GroupStart       bool
	// Own marks the viewer's own human posts (right-aligned blue bubbles).
	Own, System bool
	// Name and Machine split an agent label "name@machine" for display;
	// Initials stand in for a person's avatar.
	Name, Machine, Initials string
	// Clock is the local time of day; Timestamp stays the exact value.
	Clock        string
	MentionsText string
	// ThreadTitle and ThreadMeta describe the task thread anchored here.
	ThreadTitle, ThreadMeta string
	Directive               *coordDirectiveView
	Request                 *coordRequestView
}

// coordDirectiveView is a standing instruction shown as a pinned plate. Active
// is false once it was ended (or is not part of the room's standing list).
type coordDirectiveView struct {
	Active bool
	// CanEnd: the viewer may end it (its author, or at least the author's rank).
	// Request: the author's rank is below directive rank, so it only asks.
	CanEnd, Request    bool
	MessageID, RoomKey string
	Scope              string
}

// coordRequestView is a question, approval, blocker or handoff. Items are the
// open attention entries for this very message that the viewer may act on or
// withdraw; Waiting is set while any is open.
type coordRequestView struct {
	Title, Waiting string
	// State names how a closed request ended, e.g. "Answered · robin".
	State string
	Items []coordAttentionView
}

type coordReplyView struct {
	Sequence          int64
	Author, Body, URL string
	Missing           bool
}

// Head reads "Reply to #12 · Mia".
func (r coordReplyView) Head() string {
	return msg("coord.reply_to", r.Sequence) + " · " + r.Author
}

type coordRefView struct {
	Kind, ID, Revision string
	Mutable            bool
}

// Text names a reference: kind:id, the pinned revision or "(current state)".
func (r coordRefView) Text() string {
	text := r.Kind + ":" + r.ID
	if r.Revision != "" {
		return text + "@" + r.Revision
	}
	if r.Mutable {
		return text + " " + msg("coord.ref_head")
	}
	return text
}

type coordStandingView struct {
	MessageID, Person, Body, CreatedAt string
	DisplayTimestamp                   string
	Targets                            []string
	// Scope reads "All agents in <room>, until ended" or the addressees.
	Scope  string
	CanEnd bool
}

type coordParticipantView struct {
	ID, Label, Provider, Worktree, LastSeen, Branch string
	DisplayTimestamp                                string
	Reachability, WorkState                         string
	// ReachabilityText und WorkStateText tragen Wert, Herkunft und Alter
	// ("verbunden (beobachtet, vor 12 s)"); leer bei Menschen, für die es keine
	// Presence gibt. Ein Agent ohne Beleg zeigt "unbekannt (keine Beobachtung)".
	ReachabilityText, WorkStateText string
	Manager, Current                bool
	// Role ist die Projektrolle (owner, lead, member, guest), leer außerhalb
	// eines Projektraums; CanReview das Prüfer-Flag.
	Role      string
	CanReview bool
	// Control is the pause control of an agent, nil for people.
	Control *coordControlView
	// Human, Initials, Dot and StatusLabel drive the compact list: Dot is one of
	// active, wait, bad, idle, off; StatusLabel is the short state in words.
	Human                      bool
	Initials, Dot, StatusLabel string
	// ReachKey and WorkKey are the raw store values; empty without evidence.
	ReachKey, WorkKey string
}

// coordParticipantUnknown is the neutral value for a state nobody reported.
// It is the normal case and says nothing about idleness.
const coordParticipantUnknown = "unknown"

type coordAttentionActionView struct{ Value, Label string }

type coordAttentionView struct {
	ID, Sequence                                                   int64
	RecipientID, RecipientLabel, Reason, State, Body, URL, RoomKey string
	CSRFToken                                                      string
	Incoming, CanWithdraw                                          bool
	// Thread marks an item whose message lives in a task thread, not the room.
	Thread                            bool
	SenderLabel, RoomLabel, RoomTitle string
	ReasonLabel                       string
	// Primary is the one action worth showing without a disclosure; More
	// holds everything else behind "Aktionen".
	Primary *coordAttentionActionView
	// Inline holds the approval verdicts, which stay visible on the card.
	Inline                   []coordAttentionActionView
	Preview                  string
	More                     []coordAttentionActionView
	Private                  bool
	CoordinationOnlyApproval bool
	Actions                  []coordAttentionActionView
}

func buildCoordSidebar(summaries []store.CoordRoomSummary, principalID, activeKey string, labels map[string]string) coordSidebarView {
	var out coordSidebarView
	for _, summary := range summaries {
		room := coordRoomView{
			Key: summary.Room.Key, Kind: summary.Room.Kind,
			Label:  coordRoomLabel(summary.Room, principalID, labels),
			URL:    coordRoomURL(summary.Room.Key, "", 0),
			Unread: summary.Unread, Mentions: summary.MentionUnread, Attention: summary.Attention, NeedsYou: summary.NeedsYou,
			Active: summary.Room.Key == activeKey,
		}
		room.Name = coordShortRoomName(room.Kind, room.Label)
		switch summary.Room.Kind {
		case store.RoomMachine:
			out.Machines = append(out.Machines, room)
		case store.RoomProject:
			out.Projects = append(out.Projects, room)
		case store.RoomDirect, store.RoomGroup:
			out.Private = append(out.Private, room)
		}
		if room.Attention > 0 || room.Mentions > 0 || room.Unread > 0 {
			out.Attention = append(out.Attention, room)
		}
	}
	return out
}

func buildCoordAttentionViews(items []store.AttentionItem, csrfToken string, labelIndexes ...map[string]string) (incoming, outgoing []coordAttentionView) {
	var labels map[string]string
	if len(labelIndexes) > 0 {
		labels = labelIndexes[0]
	}
	for _, item := range items {
		if item.State != store.AttentionOpen {
			continue
		}
		recipientLabel := coordIdentityLabel(item.RecipientID, labels)
		view := coordAttentionView{
			ID: item.ID, Sequence: item.Sequence, Reason: item.Reason, State: item.State,
			Body: item.Body, Preview: coordAttentionPreview(item.Body), RecipientID: item.RecipientID, RecipientLabel: recipientLabel,
			Incoming: item.IsRecipient, CanWithdraw: item.CanWithdraw,
			RoomKey: item.HomeRoomKey, CSRFToken: csrfToken,
			CoordinationOnlyApproval: item.Reason == store.AttentionApproval,
		}
		if item.DestinationKind == store.DestinationDiscussion {
			view.Thread = true
			threadID, err := strconv.ParseInt(item.DestinationID, 10, 64)
			if err == nil && threadID > 0 && item.HomeRoomKey != "" {
				view.URL = coordThreadMessageURL(item.HomeRoomKey, threadID, item.Sequence)
			}
		} else {
			view.URL = coordRoomURL(item.DestinationID, "around", item.Sequence) + "#message-" + strconv.FormatInt(item.Sequence, 10)
		}
		if item.State == store.AttentionOpen && item.IsRecipient {
			switch item.Reason {
			case store.AttentionQuestion:
				view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionAnswer, Label: msg("coord.action.answer")})
			case store.AttentionApproval:
				view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionApprove, Label: msg("coord.action.approve")}, coordAttentionActionView{Value: store.AttentionActionReject, Label: msg("coord.action.reject")})
			case store.AttentionBlocker:
				view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionResolve, Label: msg("coord.action.resolve")})
			case store.AttentionHandoff:
				view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionAccept, Label: msg("coord.action.accept")})
			}
			view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionDismiss, Label: msg("coord.action.dismiss")})
		}
		view.ReasonLabel = coordAttentionReasonLabel(item.Reason)
		view.Primary, view.Inline, view.More = splitCoordAttentionActions(item.Reason, view.Actions)
		if view.Incoming {
			incoming = append(incoming, view)
		}
		if view.CanWithdraw {
			outgoing = append(outgoing, view)
		}
	}
	return incoming, outgoing
}

// annotateCoordAttention names sender and origin room on each card and marks
// cards from direct or group rooms as private. Only rooms the viewer belongs
// to reach this point: CoordAccess.Attention drops every item whose
// destination the viewer cannot read.
func annotateCoordAttention(views []coordAttentionView, items []store.AttentionItem, sidebar coordSidebarView, labels map[string]string) {
	byID := make(map[int64]store.AttentionItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	for i := range views {
		item := byID[views[i].ID]
		views[i].SenderLabel = coordSenderLabel(item, labels)
		if room, ok := sidebar.room(item.HomeRoomKey); ok {
			views[i].RoomLabel, views[i].RoomTitle = room.Name, room.Label
			views[i].Private = room.Kind == store.RoomDirect || room.Kind == store.RoomGroup
			if room.Kind == store.RoomDirect {
				views[i].RoomLabel, views[i].RoomTitle = msg("coord.direct_message"), ""
			}
		} else {
			full := coordRoomLabel(store.CoordRoom{Key: item.HomeRoomKey}, "", labels)
			views[i].RoomLabel, views[i].RoomTitle = coordShortRoomName(coordRoomKindOf(item.HomeRoomKey), full), full
		}
	}
}

func coordRoomURL(room, cursor string, sequence int64) string {
	query := url.Values{"room": {room}}
	if cursor != "" && sequence > 0 {
		query.Set(cursor, strconv.FormatInt(sequence, 10))
	}
	return "/ui/coord?" + query.Encode()
}

func coordThreadURL(room string, threadID int64) string {
	query := url.Values{"room": {room}, "thread": {strconv.FormatInt(threadID, 10)}}
	return "/ui/coord?" + query.Encode() + "#coord-thread"
}

func coordThreadMessageURL(room string, threadID, sequence int64) string {
	query := url.Values{
		"room": {room}, "thread": {strconv.FormatInt(threadID, 10)},
		"thread_around": {strconv.FormatInt(sequence, 10)},
	}
	return "/ui/coord?" + query.Encode() + "#thread-message-" + strconv.FormatInt(sequence, 10)
}

func coordThreadPageURL(room string, threadID int64, cursor string, sequence int64) string {
	query := url.Values{"room": {room}, "thread": {strconv.FormatInt(threadID, 10)}}
	if cursor != "" && sequence > 0 {
		query.Set(cursor, strconv.FormatInt(sequence, 10))
	}
	return "/ui/coord?" + query.Encode() + "#coord-thread"
}

func coordRoomReplyURL(room string, messageID, sequence int64) string {
	query := url.Values{"room": {room}, "around": {strconv.FormatInt(sequence, 10)}, "reply_to": {strconv.FormatInt(messageID, 10)}}
	return "/ui/coord?" + query.Encode() + "#coord-message-body"
}

func coordThreadReplyURL(room string, threadID, messageID, sequence int64) string {
	query := url.Values{
		"room": {room}, "thread": {strconv.FormatInt(threadID, 10)},
		"thread_around": {strconv.FormatInt(sequence, 10)}, "thread_reply_to": {strconv.FormatInt(messageID, 10)},
	}
	return "/ui/coord?" + query.Encode() + "#coord-thread-body"
}

func coordThreadComposerURL(room string, threadID int64) string {
	return strings.TrimSuffix(coordThreadURL(room, threadID), "#coord-thread") + "#coord-thread-body"
}

func buildCoordThreadViews(in []store.RoomThread) []coordThreadView {
	out := make([]coordThreadView, 0, len(in))
	for _, item := range in {
		view := coordThreadView{
			ID: item.Thread.ID, AnchorMessageID: item.Home.AnchorMessageID,
			Title: item.Thread.Title, Question: item.Thread.Question, State: item.Thread.State,
			Archived: item.Thread.Archived, URL: coordThreadURL(item.Home.RoomKey, item.Thread.ID),
			RequestID: item.RequestID, RequestTitle: item.RequestTitle, RequestState: item.RequestState,
			RequestURL: "/ui/requests/" + strings.TrimPrefix(item.RequestID, "REQ-"),
		}
		if item.AnchorSequence > 0 {
			view.AnchorURL = coordRoomURL(item.Home.RoomKey, "around", item.AnchorSequence) + "#message-" + strconv.FormatInt(item.AnchorSequence, 10)
		}
		out = append(out, view)
	}
	return out
}

func coordRoomLabel(room store.CoordRoom, principalID string, labels map[string]string) string {
	if label := strings.TrimSpace(room.Label); label != "" {
		return label
	}
	switch room.Kind {
	case store.RoomDirect:
		var peers []string
		for _, member := range room.Members {
			if member != principalID {
				peers = append(peers, coordIdentityLabel(member, labels))
			}
		}
		if len(peers) > 0 {
			return strings.Join(peers, ", ")
		}
	case store.RoomProject:
		return strings.TrimPrefix(room.Key, "project:")
	case store.RoomMachine:
		return strings.TrimPrefix(room.Key, "machine:")
	}
	return room.Key
}

func buildCoordMessageViews(messages []store.CoordMessagePresentation, roomKey string, labels map[string]string) []coordMessageView {
	out := make([]coordMessageView, 0, len(messages))
	previousAuthorID, previousAuthorKind, previousCard := "", "", false
	for _, presentation := range messages {
		message := presentation.Message
		author := strings.TrimSpace(presentation.AuthorLabel)
		if author == "" {
			author = strings.TrimSpace(message.SenderExternalID)
		}
		authorID := strings.TrimSpace(message.SenderExternalID)
		if message.AuthorKind == store.AuthorHuman && strings.TrimSpace(message.AuthorPrincipalID) != "" {
			authorID = strings.TrimSpace(message.AuthorPrincipalID)
		}
		if authorID == "" {
			authorID = author
		}
		card := message.AuthorKind == store.AuthorSystem || message.Intent == store.IntentStanding || isAttentionIntent(message.Intent)
		groupStart := len(out) == 0 || previousAuthorID != authorID || previousAuthorKind != message.AuthorKind || presentation.Reply != nil || card || previousCard
		view := coordMessageView{
			ID: message.ID, Sequence: message.Sequence, Author: author,
			AuthorKind: message.AuthorKind, Timestamp: message.CreatedAt, DisplayTimestamp: coordDisplayTimestamp(message.CreatedAt),
			Body: message.Body, Intent: message.Intent, Expired: message.Expired,
			ReplyURL:   coordRoomReplyURL(roomKey, message.ID, message.Sequence),
			ReplyCount: presentation.ReplyCount,
			GroupStart: groupStart,
			System:     message.AuthorKind == store.AuthorSystem,
			Clock:      coordClock(message.CreatedAt),
		}
		shown := author
		if message.AuthorKind == store.AuthorAgent {
			// The stored label turns "@" into "_"; the peer label keeps the machine.
			if label := strings.TrimSpace(labels[message.SenderExternalID]); label != "" {
				shown = label
			}
		}
		view.Name, view.Machine = coordSplitAgentName(shown, message.AuthorKind)
		view.Initials = coordInitials(author)
		for _, mention := range presentation.Mentions {
			view.Mentions = append(view.Mentions, coordIdentityLabel(mention, labels))
		}
		view.MentionsText = coordJoinNames(view.Mentions)
		if presentation.Reply != nil {
			view.Reply = &coordReplyView{Sequence: presentation.Reply.Sequence, Author: presentation.Reply.Author, Body: presentation.Reply.Body, Missing: presentation.Reply.Missing}
			if !view.Reply.Missing {
				view.Reply.URL = coordRoomURL(roomKey, "around", view.Reply.Sequence) + "#message-" + strconv.FormatInt(view.Reply.Sequence, 10)
			}
		}
		for _, ref := range presentation.Refs {
			view.Refs = append(view.Refs, coordRefView{Kind: ref.Kind, ID: ref.ID, Revision: ref.Revision, Mutable: ref.MutableHead})
		}
		d := presentation.Delivery
		parts := []string{}
		if d.Acked > 0 {
			parts = append(parts, msg("coord.delivery.acked", d.Acked))
		}
		if d.Injected > 0 {
			parts = append(parts, msg("coord.delivery.injected", d.Injected))
		}
		if d.Fetched > 0 {
			parts = append(parts, msg("coord.delivery.fetched", d.Fetched))
		}
		if d.Stored > 0 {
			parts = append(parts, msg("coord.delivery.stored", d.Stored))
		}
		// "stored" is where every message starts, so a message that is only
		// stored carries no news; anything past it, or a recipient lagging
		// behind others, is worth showing.
		if d.Acked+d.Injected+d.Fetched > 0 {
			view.Delivery = strings.Join(parts, " · ")
		}
		out = append(out, view)
		previousAuthorID, previousAuthorKind, previousCard = authorID, message.AuthorKind, card
	}
	return out
}

// markViewerMentions flags the messages that mention viewerID. views and
// messages are index-aligned, as produced by buildCoordMessageViews.
func markViewerMentions(views []coordMessageView, messages []store.CoordMessagePresentation, viewerID string) {
	if viewerID == "" {
		return
	}
	for i := range views {
		if i >= len(messages) {
			return
		}
		for _, mention := range messages[i].Mentions {
			if mention == viewerID {
				views[i].MentionsViewer = true
				break
			}
		}
	}
}

var errCoordExpiryInvalid = errors.New("Invalid expiry: enter a date and time like 2026-09-18 18:30.")

// parseCoordExpiry turns a datetime-local value (no zone) into RFC3339 UTC.
// offset is the browser's minutes east of UTC for that very date (set by
// app.js, so DST is right); without it the value is read in server. A value
// that already carries a zone is used as given. Anything else is an error, since
// an unreadable expiry would silently mean "never expires".
func parseCoordExpiry(raw, offset string, server *time.Location) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	loc := server
	if offset = strings.TrimSpace(offset); offset != "" {
		minutes, err := strconv.Atoi(offset)
		if err != nil || minutes < -14*60 || minutes > 14*60 {
			return "", errCoordExpiryInvalid
		}
		loc = time.FixedZone("", minutes*60)
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", errCoordExpiryInvalid
}

func buildCoordThreadMessageViews(messages []store.CoordMessagePresentation, room string, threadID int64, labels map[string]string) []coordMessageView {
	out := buildCoordMessageViews(messages, "", labels)
	for i := range out {
		out[i].ReplyURL = coordThreadReplyURL(room, threadID, out[i].ID, out[i].Sequence)
		if out[i].Reply != nil && !out[i].Reply.Missing {
			out[i].Reply.URL = coordThreadMessageURL(room, threadID, out[i].Reply.Sequence)
		}
	}
	return out
}

func coordReplyTarget(presentations []store.CoordMessagePresentation, raw string) (int64, *coordReplyView, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, nil, fmt.Errorf("reply target must be a positive message id")
	}
	for _, presentation := range presentations {
		if presentation.Message.ID != id {
			continue
		}
		author := strings.TrimSpace(presentation.AuthorLabel)
		if author == "" {
			author = strings.TrimSpace(presentation.Message.SenderExternalID)
		}
		return id, &coordReplyView{Sequence: presentation.Message.Sequence, Author: author, Body: presentation.Message.Body}, nil
	}
	return 0, nil, fmt.Errorf("reply target is not in this destination window")
}

func buildCoordStandingViews(in []store.StandingInstruction, labels map[string]string) []coordStandingView {
	out := make([]coordStandingView, 0, len(in))
	for _, item := range in {
		view := coordStandingView{MessageID: item.MessageID, Person: item.Person, Body: item.Body, CreatedAt: item.CreatedAt, DisplayTimestamp: coordDisplayTimestamp(item.CreatedAt)}
		for _, target := range item.Targets {
			view.Targets = append(view.Targets, coordIdentityLabel(target, labels))
		}
		out = append(out, view)
	}
	return out
}

func buildCoordRecipientViews(recipients []store.CoordRecipient) []coordRecipientView {
	out := make([]coordRecipientView, 0, len(recipients))
	for _, recipient := range recipients {
		out = append(out, coordRecipientView{ID: recipient.PrincipalID, Label: recipient.Label, Kind: recipient.Kind, Option: recipient.Label + " · " + recipient.Kind})
	}
	return out
}

func coordIdentityLabels(current store.Principal, recipients []store.CoordRecipient, peers ...store.CoordAgent) map[string]string {
	labels := make(map[string]string, len(recipients)+len(peers)+1)
	if strings.TrimSpace(current.ID) != "" && strings.TrimSpace(current.Label) != "" {
		labels[current.ID] = strings.TrimSpace(current.Label)
	}
	for _, recipient := range recipients {
		if strings.TrimSpace(recipient.Label) != "" {
			labels[recipient.PrincipalID] = strings.TrimSpace(recipient.Label)
		}
	}
	for _, peer := range peers {
		label := strings.TrimSpace(peer.DisplayName)
		if label == "" {
			label = strings.TrimSpace(peer.Person)
		}
		if label == "" {
			label = "Agent"
		}
		labels[peer.ExternalID] = label
	}
	return labels
}

func coordIdentityLabel(id string, labels map[string]string) string {
	if label := strings.TrimSpace(labels[id]); label != "" {
		return label
	}
	return msg("coord.unknown_participant")
}

// coordSenderLabel: Etikett des Absenders, sonst das des Kontos, sonst die
// bloße Absender-ID (die Agenten-ID, die auch ein Gast sehen darf) statt
// "Unbekannter Teilnehmer".
func coordSenderLabel(item store.AttentionItem, labels map[string]string) string {
	if label := strings.TrimSpace(labels[item.SenderID]); label != "" {
		return label
	}
	if label := strings.TrimSpace(labels[item.AuthorID]); label != "" {
		return label
	}
	if id := strings.TrimSpace(item.SenderID); id != "" {
		return id
	}
	return coordIdentityLabel(item.AuthorID, labels)
}

var presenceKeys = map[string]string{
	store.ReachConnected: "coord.presence.connected", store.ReachUnknown: "coord.presence.unknown", store.ReachEnded: "coord.presence.ended",
	store.WorkWorking: "coord.presence.working", store.WorkWaitingUser: "coord.presence.waiting_user", store.WorkWaitingPeer: "coord.presence.waiting_peer",
	store.WorkBlocked: "coord.presence.blocked", store.WorkPaused: "coord.presence.paused",
}

var originKeys = map[string]string{
	store.OriginObserved: "coord.origin.observed", store.OriginSelfReported: "coord.origin.self_reported", store.OriginDerived: "coord.origin.derived",
}

func coordAge(seconds int64) string {
	switch {
	case seconds < 60:
		return msg("coord.age.seconds", seconds)
	case seconds < 3600:
		return msg("coord.age.minutes", seconds/60)
	default:
		return msg("coord.age.hours", seconds/3600)
	}
}

// presenceText bildet ein Presence-Feld ab. Ohne Herkunft gibt es keinen Beleg:
// das steht dann da, statt eines Werts, der mehr behauptet.
func presenceText(f store.PresenceField) (value, text string) {
	key := presenceKeys[f.Value]
	if key == "" || f.Origin == "" {
		return msg("coord.presence.unknown"), msg("coord.presence.no_evidence")
	}
	value = msg(key)
	return value, fmt.Sprintf("%s (%s, %s)", value, msg(originKeys[f.Origin]), coordAge(f.AgeSeconds))
}

func buildCoordParticipants(room store.CoordRoom, peers []store.CoordAgent, memberships []store.RoomMembership, current store.Principal, labels map[string]string) []coordParticipantView {
	byID := make(map[string]coordParticipantView)
	for _, member := range room.Members {
		byID[member] = coordParticipantView{ID: member, Label: coordIdentityLabel(member, labels), Current: member == current.ID, Reachability: coordParticipantUnknown, WorkState: coordParticipantUnknown}
	}
	for _, peer := range peers {
		label := strings.TrimSpace(labels[peer.ExternalID])
		if label == "" {
			label = strings.TrimSpace(peer.DisplayName)
		}
		if label == "" {
			label = strings.TrimSpace(peer.Person)
		}
		if label == "" {
			label = "Agent"
		}
		byID[peer.ExternalID] = coordParticipantView{
			ID: peer.ExternalID, Label: label, Provider: peer.Provider,
			Worktree: peer.Worktree, LastSeen: peer.LastSeenAt, DisplayTimestamp: coordDisplayTimestamp(peer.LastSeenAt), Branch: peer.Branch,
			Current: peer.ExternalID == current.ID, Reachability: coordParticipantUnknown, WorkState: coordParticipantUnknown,
			Role: peer.Role, CanReview: peer.CanReview,
		}
		view := byID[peer.ExternalID]
		pr := store.Presence{}
		if peer.Presence != nil {
			pr = *peer.Presence
		}
		view.Reachability, view.ReachabilityText = presenceText(pr.Reachability)
		view.WorkState, view.WorkStateText = presenceText(pr.WorkState)
		if pr.Reachability.Origin != "" {
			view.ReachKey = pr.Reachability.Value
		}
		if pr.WorkState.Origin != "" {
			view.WorkKey = pr.WorkState.Value
		}
		byID[peer.ExternalID] = view
	}
	currentParticipant := byID[current.ID]
	currentParticipant.ID = current.ID
	currentParticipant.Current = true
	if label := strings.TrimSpace(current.Label); label != "" {
		currentParticipant.Label = label
	} else if currentParticipant.Label == "" {
		currentParticipant.Label = coordIdentityLabel(current.ID, labels)
	}
	byID[current.ID] = currentParticipant
	for _, membership := range memberships {
		if membership.LeftAt != "" {
			continue
		}
		participant := byID[membership.PrincipalID]
		participant.ID = membership.PrincipalID
		if participant.Label == "" {
			participant.Label = coordIdentityLabel(membership.PrincipalID, labels)
		}
		participant.Manager = membership.Manager
		participant.Current = membership.PrincipalID == current.ID
		byID[membership.PrincipalID] = participant
	}
	out := make([]coordParticipantView, 0, len(byID))
	for _, participant := range byID {
		out = append(out, participant)
	}
	for i := range out {
		decorateCoordParticipant(&out[i])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Current != out[j].Current {
			return out[i].Current
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func coordDisplayTimestamp(raw string) string {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return parsed.In(time.Local).Format("02.01.2006, 15:04")
}

// coordJoinNames joins names as "A", "A and B" or "A, B and C".
func coordJoinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + msg("coord.and") + " " + names[len(names)-1]
}

// coordShortRoomName shows a project room as owner/repo. The host prefix
// pushes the repo name out of a narrow sidebar; the full name stays in title.
func coordShortRoomName(kind, name string) string {
	if kind != store.RoomProject {
		return name
	}
	parts := strings.Split(strings.Trim(name, "/"), "/")
	if len(parts) <= 2 {
		return name
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

func coordRoomKindOf(key string) string {
	if strings.HasPrefix(key, "project:") {
		return store.RoomProject
	}
	return ""
}

func coordAttentionReasonLabel(reason string) string {
	switch reason {
	case store.AttentionQuestion:
		return msg("coord.reason.question")
	case store.AttentionApproval:
		return msg("coord.reason.approval")
	case store.AttentionBlocker:
		return msg("coord.reason.blocker")
	case store.AttentionHandoff:
		return msg("coord.reason.handoff")
	}
	return reason
}

// splitCoordAttentionActions keeps a single unambiguous action inline. An
// approval has two opposite outcomes and needs its explanation first, so it
// and the dismissal stay behind the disclosure.
func splitCoordAttentionActions(reason string, actions []coordAttentionActionView) (primary *coordAttentionActionView, inline, more []coordAttentionActionView) {
	if len(actions) == 0 {
		return nil, nil, nil
	}
	if reason == store.AttentionApproval {
		last := len(actions) - 1
		if actions[last].Value == store.AttentionActionDismiss {
			return nil, actions[:last], actions[last:]
		}
		return nil, actions, nil
	}
	if actions[0].Value == store.AttentionActionDismiss {
		return nil, nil, actions
	}
	first := actions[0]
	return &first, nil, actions[1:]
}

// coordAttentionPreviewRunes bounds the preview text sent per attention item;
// the stylesheet clamps what is shown to two lines.
const coordAttentionPreviewRunes = 240

// coordAttentionPreview shortens body on a rune boundary so a multi-byte
// character is never cut in half.
func coordAttentionPreview(body string) string {
	body = strings.TrimSpace(body)
	if utf8.RuneCountInString(body) <= coordAttentionPreviewRunes {
		return body
	}
	return string([]rune(body)[:coordAttentionPreviewRunes]) + "…"
}

// applyParticipantRoles ergänzt die Rolle der menschlichen Teilnehmer
// (person:<id>) im Projekt des Raums. Agenten tragen ihre effektive Rolle schon
// aus der Peer-Liste. Außerhalb eines Projektraums bleibt alles leer.
func applyParticipantRoles(st *store.Store, roomKey string, participants []coordParticipantView) {
	remote, ok := strings.CutPrefix(roomKey, "project:")
	if !ok {
		return
	}
	for i := range participants {
		if participants[i].Role != "" || !strings.HasPrefix(participants[i].ID, "person:") {
			continue
		}
		info := st.ProjectRole(remote, participants[i].ID)
		participants[i].Role, participants[i].CanReview = info.Role, info.CanReview
	}
}

// applyMessageRoles setzt die aktuelle Projektrolle des Absenders an jede
// Nachricht eines Projektraums. Die Rolle kommt aus dem Store (Konto aus
// author_principal_id bzw. effektive Agentenrolle), nie aus dem Text.
// views und messages sind index-aligned.
func applyMessageRoles(st *store.Store, roomKey string, views []coordMessageView, messages []store.CoordMessagePresentation) {
	remote, ok := strings.CutPrefix(roomKey, "project:")
	if !ok {
		return
	}
	raw := make([]store.CoordMessage, len(messages))
	for i := range messages {
		raw[i] = messages[i].Message
	}
	roles := st.SenderRolesInProject(remote, raw)
	for i := range views {
		if i < len(roles) {
			views[i].SenderRole = roles[i]
		}
	}
}

// buildCoordWaitCycles beschreibt die Wartekreise der Peers, je Kreis einmal.
// Gemeldet, nicht aufgelöst: die Zeile sagt, wer auf wen wartet und bis wann
// die Review fällig ist.
func buildCoordWaitCycles(peers []store.CoordAgent, labels map[string]string) []string {
	name := func(id string) string {
		for _, p := range peers {
			if p.ExternalID == id {
				if l := strings.TrimSpace(labels[id]); l != "" {
					return l
				}
				return strings.TrimSpace(p.DisplayName)
			}
		}
		return ""
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range peers {
		if p.Presence == nil || p.Presence.Cycle == nil || seen[p.Presence.Cycle.Key()] {
			continue
		}
		seen[p.Presence.Cycle.Key()] = true
		c := *p.Presence.Cycle
		c.Since, c.ReviewAt = coordDisplayTimestamp(c.Since), coordDisplayTimestamp(c.ReviewAt)
		out = append(out, c.Describe(name))
	}
	return out
}

// coordSplitAgentName shows an agent label "name@machine" as the name and the
// machine apart; people and labels without a machine are left whole.
func coordSplitAgentName(author, kind string) (name, machine string) {
	if kind != store.AuthorAgent {
		return author, ""
	}
	if i := strings.Index(author, "@"); i > 0 && i < len(author)-1 {
		return author[:i], author[i:]
	}
	return author, ""
}

// coordInitials are the first letters of up to two words, upper case.
func coordInitials(label string) string {
	var out []rune
	for _, word := range strings.Fields(label) {
		r, _ := utf8.DecodeRuneInString(word)
		if r != utf8.RuneError {
			out = append(out, []rune(strings.ToUpper(string(r)))...)
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 1 {
		// A single word gives its first two letters.
		rs := []rune(strings.ToUpper(strings.TrimSpace(label)))
		if len(rs) >= 2 {
			return string(rs[:2])
		}
	}
	return string(out)
}

// coordClock is the local time of day for today and a short date before it.
func coordClock(raw string) string {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	local := parsed.In(time.Local)
	if local.YearDay() == time.Now().YearDay() && local.Year() == time.Now().Year() {
		return local.Format("15:04")
	}
	return local.Format("2 Jan 15:04")
}

// coordWaiting words how long an open request has been waiting.
func coordWaiting(created string, now time.Time) string {
	parsed, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return ""
	}
	d := now.Sub(parsed)
	switch {
	case d < time.Minute:
		return msg("coord.waiting_now")
	case d < time.Hour:
		return msg("coord.waiting", msg("coord.age.min", int64(d/time.Minute)))
	case d < 48*time.Hour:
		return msg("coord.waiting", msg("coord.age.h", int64(d/time.Hour)))
	}
	return msg("coord.waiting", msg("coord.age.d", int64(d/(24*time.Hour))))
}

// decorateCoordParticipant derives the compact status of the participant list
// from the evidence the store reported, never from the absence of evidence.
func decorateCoordParticipant(p *coordParticipantView) {
	p.Human = strings.HasPrefix(p.ID, "person:")
	p.Initials = coordInitials(p.Label)
	switch {
	case p.Current:
		p.Dot, p.StatusLabel = "active", msg("coord.you")
	case p.Human:
		p.Dot = "off"
	case p.ReachKey == store.ReachEnded:
		p.Dot, p.StatusLabel = "off", msg("coord.presence.ended")
	case p.WorkKey == store.WorkWorking:
		p.Dot, p.StatusLabel = "active", msg("ov.work.working")
	case p.WorkKey == store.WorkWaitingUser:
		p.Dot, p.StatusLabel = "wait", msg("ov.work.waiting_user")
	case p.WorkKey == store.WorkWaitingPeer:
		p.Dot, p.StatusLabel = "wait", msg("ov.work.waiting_peer")
	case p.WorkKey == store.WorkBlocked:
		p.Dot, p.StatusLabel = "bad", msg("ov.work.blocked")
	case p.WorkKey == store.WorkPaused:
		p.Dot, p.StatusLabel = "quiet", msg("ov.work.paused")
	case p.ReachKey == store.ReachConnected:
		p.Dot, p.StatusLabel = "quiet", msg("coord.presence.connected")
	default:
		p.Dot = "off"
	}
}

// decorateCoordMessages adds what depends on the viewer and on the room: own
// bubbles, directive plates, request cards with the open attention entries for
// the very message, and the waiting time. views and pres are index-aligned.
func decorateCoordMessages(views []coordMessageView, pres []store.CoordMessagePresentation, viewerID, roomKey, roomLabel string, standing []coordStandingView, attention []coordAttentionView, roleRoom bool, now time.Time) {
	activeStanding := make(map[string]coordStandingView, len(standing))
	for _, item := range standing {
		activeStanding[item.MessageID] = item
	}
	for i := range views {
		if i >= len(pres) {
			return
		}
		message := pres[i].Message
		views[i].Own = message.AuthorKind == store.AuthorHuman && viewerID != "" && message.AuthorPrincipalID == viewerID
		switch {
		case message.Intent == store.IntentStanding:
			id := strconv.FormatInt(message.ID, 10)
			item, ok := activeStanding[id]
			scope := item.Scope
			if !ok {
				scope = coordStandingScope(views[i].Mentions, roomLabel)
			}
			views[i].Directive = &coordDirectiveView{Active: ok, CanEnd: ok && item.CanEnd, MessageID: id, RoomKey: roomKey, Scope: scope,
				Request: roleRoom && views[i].SenderRole != "" && !coordCanDirect(views[i].SenderRole)}
		case isAttentionIntent(message.Intent):
			request := &coordRequestView{Title: coordRequestTitle(message.Intent, views[i].MentionsText)}
			seen := map[int64]bool{}
			for _, item := range attention {
				if item.Thread || item.RoomKey != roomKey || item.Sequence != message.Sequence || seen[item.ID] {
					continue
				}
				seen[item.ID] = true
				request.Items = append(request.Items, item)
			}
			if len(request.Items) > 0 {
				request.Waiting = coordWaiting(message.CreatedAt, now)
			}
			views[i].Request = request
		}
	}
}

// coordCanDirect: only a rank above guest can direct anyone (AuthorityFor needs
// the sender above the recipient, and recipients with a role start at guest).
func coordCanDirect(role string) bool {
	return store.RoleRank(role) > store.RoleRank(store.RoleGuest)
}

var coordClosedKeys = map[string]string{
	"question.resolved":  "coord.closed.question.resolved",
	"question.dismissed": "coord.closed.question.dismissed",
	"question.expired":   "coord.closed.question.expired",
	"approval.resolved":  "coord.closed.approval.resolved",
	"approval.dismissed": "coord.closed.approval.dismissed",
	"approval.expired":   "coord.closed.approval.expired",
	"blocker.resolved":   "coord.closed.blocker.resolved",
	"blocker.dismissed":  "coord.closed.blocker.dismissed",
	"blocker.expired":    "coord.closed.blocker.expired",
	"handoff.resolved":   "coord.closed.handoff.resolved",
	"handoff.dismissed":  "coord.closed.handoff.dismissed",
	"handoff.expired":    "coord.closed.handoff.expired",
}

// applyRequestStates names how closed requests ended, from the viewer's own
// attention entries for that message.
func applyRequestStates(views []coordMessageView, pres []store.CoordMessagePresentation, roomKey string, items []store.AttentionItem, labels map[string]string) {
	for i := range views {
		if i >= len(pres) || views[i].Request == nil || len(views[i].Request.Items) > 0 {
			continue
		}
		var parts []string
		for _, item := range items {
			if item.State == store.AttentionOpen || item.DestinationKind != store.DestinationRoom || item.DestinationID != roomKey || item.Sequence != pres[i].Message.Sequence {
				continue
			}
			if key, ok := coordClosedKeys[item.Reason+"."+item.State]; ok {
				parts = append(parts, msg(key)+" · "+coordIdentityLabel(item.RecipientID, labels))
			}
		}
		views[i].Request.State = strings.Join(parts, ", ")
	}
}

func coordStandingScope(targets []string, roomLabel string) string {
	if len(targets) == 0 {
		return msg("coord.scope_all", roomLabel)
	}
	return msg("coord.scope_targets", coordJoinNames(targets))
}

var coordRequestTitleKeys = map[string][2]string{
	store.IntentQuestion: {"coord.reason.question", "coord.request.question"},
	store.IntentApproval: {"coord.reason.approval", "coord.request.approval"},
	store.IntentBlocker:  {"coord.reason.blocker", "coord.request.blocker"},
	store.IntentHandoff:  {"coord.reason.handoff", "coord.request.handoff"},
}

// coordRequestTitle is the card heading: the kind alone, or the kind with the
// people it is addressed to ("Asks Robin").
func coordRequestTitle(intent, who string) string {
	keys := coordRequestTitleKeys[intent]
	if who == "" {
		return msg(keys[0])
	}
	return msg(keys[1], who)
}

var coordClientTextKeys = []string{
	"coord.js.loading", "coord.js.load_failed", "coord.js.render_failed", "coord.js.updated", "coord.js.offline_page",
	"coord.js.no_uploads", "coord.js.no_token", "coord.js.working", "coord.js.failed", "coord.js.done", "coord.js.offline_post",
	"coord.js.live", "coord.js.refresh_failed", "coord.js.refresh_failed_announce", "coord.js.unsupported", "coord.js.resync",
	"coord.js.session_ended", "coord.js.offline_retry", "coord.js.offline_retry_announce",
}

// coordClientTexts is the catalog slice the page script needs, as JSON for a
// data attribute (the policy forbids inline script, not data).
func coordClientTexts() string {
	texts := make(map[string]string, len(coordClientTextKeys))
	for _, key := range coordClientTextKeys {
		texts[key[strings.LastIndex(key, ".")+1:]] = msg(key)
	}
	raw, err := json.Marshal(texts)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
