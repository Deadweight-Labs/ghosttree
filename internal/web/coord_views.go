package web

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type coordPageView struct {
	EventCursor       int64
	Sidebar           coordSidebarView
	Active            *coordRoomDetailView
	Recipients        []coordRecipientView
	IncomingAttention []coordAttentionView
	OutgoingAttention []coordAttentionView
}

type coordRecipientView struct{ ID, Label, Kind string }

type coordSidebarView struct {
	Attention []coordRoomView
	Machines  []coordRoomView
	Projects  []coordRoomView
	Private   []coordRoomView
}

type coordRoomView struct {
	Key, Kind, Label, URL       string
	Unread, Mentions, Attention int64
	Active                      bool
}

type coordRoomDetailView struct {
	Room                   coordRoomView
	Messages               []coordMessageView
	Participants           []coordParticipantView
	Standing               []coordStandingView
	HighWater              int64
	FirstSequence          int64
	LastSequence           int64
	OlderURL, NewerURL     string
	HasOlder, HasNewer     bool
	CanManage, CanLeave    bool
	FormID, StandingFormID string
	ReplyTo                int64
	ReplyTarget            *coordReplyView
	Threads                []coordThreadView
	Thread                 *coordThreadDetailView
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
	DisplayTimestamp              string
	Body, Intent                  string
	Expired                       bool
	Reply                         *coordReplyView
	Mentions                      []string
	Refs                          []coordRefView
	Delivery                      string
	ThreadURL                     string
	ReplyURL                      string
	CanPromote                    bool
	CSRFToken                     string
	GroupStart                    bool
}

type coordReplyView struct {
	Sequence          int64
	Author, Body, URL string
	Missing           bool
}
type coordRefView struct {
	Kind, ID, Revision string
	Mutable            bool
}
type coordStandingView struct {
	MessageID, Person, Body, CreatedAt string
	DisplayTimestamp                   string
	Targets                            []string
}

type coordParticipantView struct {
	ID, Label, Provider, Worktree, LastSeen, Branch string
	DisplayTimestamp                                string
	Reachability, WorkState                         string
	Manager, Current                                bool
}

type coordAttentionActionView struct{ Value, Label string }

type coordAttentionView struct {
	ID, Sequence                                                   int64
	RecipientID, RecipientLabel, Reason, State, Body, URL, RoomKey string
	CSRFToken                                                      string
	Incoming, CanWithdraw                                          bool
	CoordinationOnlyApproval                                       bool
	Actions                                                        []coordAttentionActionView
}

func buildCoordSidebar(summaries []store.CoordRoomSummary, principalID, activeKey string, labels map[string]string) coordSidebarView {
	var out coordSidebarView
	for _, summary := range summaries {
		room := coordRoomView{
			Key: summary.Room.Key, Kind: summary.Room.Kind,
			Label:  coordRoomLabel(summary.Room, principalID, labels),
			URL:    coordRoomURL(summary.Room.Key, "", 0),
			Unread: summary.Unread, Mentions: summary.MentionUnread, Attention: summary.Attention,
			Active: summary.Room.Key == activeKey,
		}
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
			Body: item.Body, RecipientID: item.RecipientID, RecipientLabel: recipientLabel,
			Incoming: item.IsRecipient, CanWithdraw: item.CanWithdraw,
			RoomKey: item.HomeRoomKey, CSRFToken: csrfToken,
			CoordinationOnlyApproval: item.Reason == store.AttentionApproval,
		}
		if item.DestinationKind == store.DestinationDiscussion {
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
				view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionAnswer, Label: "Beantwortet"})
			case store.AttentionApproval:
				view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionApprove, Label: "Zustimmen"}, coordAttentionActionView{Value: store.AttentionActionReject, Label: "Ablehnen"})
			case store.AttentionBlocker:
				view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionResolve, Label: "Gelöst"})
			case store.AttentionHandoff:
				view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionAccept, Label: "Übernehmen"})
			}
			view.Actions = append(view.Actions, coordAttentionActionView{Value: store.AttentionActionDismiss, Label: "Verwerfen"})
		}
		if view.Incoming {
			incoming = append(incoming, view)
		}
		if view.CanWithdraw {
			outgoing = append(outgoing, view)
		}
	}
	return incoming, outgoing
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
	previousAuthorID, previousAuthorKind := "", ""
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
		groupStart := len(out) == 0 || previousAuthorID != authorID || previousAuthorKind != message.AuthorKind || presentation.Reply != nil
		view := coordMessageView{
			ID: message.ID, Sequence: message.Sequence, Author: author,
			AuthorKind: message.AuthorKind, Timestamp: message.CreatedAt, DisplayTimestamp: coordDisplayTimestamp(message.CreatedAt),
			Body: message.Body, Intent: message.Intent, Expired: message.Expired,
			ReplyURL:   coordRoomReplyURL(roomKey, message.ID, message.Sequence),
			ReplyCount: presentation.ReplyCount,
			GroupStart: groupStart,
		}
		for _, mention := range presentation.Mentions {
			view.Mentions = append(view.Mentions, coordIdentityLabel(mention, labels))
		}
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
			parts = append(parts, fmt.Sprintf("%d bestätigt", d.Acked))
		}
		if d.Injected > 0 {
			parts = append(parts, fmt.Sprintf("%d eingebracht", d.Injected))
		}
		if d.Fetched > 0 {
			parts = append(parts, fmt.Sprintf("%d abgeholt", d.Fetched))
		}
		if d.Stored > 0 {
			parts = append(parts, fmt.Sprintf("%d gespeichert", d.Stored))
		}
		view.Delivery = strings.Join(parts, " · ")
		out = append(out, view)
		previousAuthorID, previousAuthorKind = authorID, message.AuthorKind
	}
	return out
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
		out = append(out, coordRecipientView{ID: recipient.PrincipalID, Label: recipient.Label, Kind: recipient.Kind})
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
	return "Unbekannter Teilnehmer"
}

func buildCoordParticipants(room store.CoordRoom, peers []store.CoordAgent, memberships []store.RoomMembership, current store.Principal, labels map[string]string) []coordParticipantView {
	byID := make(map[string]coordParticipantView)
	for _, member := range room.Members {
		byID[member] = coordParticipantView{ID: member, Label: coordIdentityLabel(member, labels), Current: member == current.ID, Reachability: "unbekannt", WorkState: "unbekannt"}
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
			Current: peer.ExternalID == current.ID, Reachability: "unbekannt", WorkState: "unbekannt",
		}
	}
	currentParticipant := byID[current.ID]
	currentParticipant.ID = current.ID
	currentParticipant.Current = true
	currentParticipant.Reachability = "unbekannt"
	currentParticipant.WorkState = "unbekannt"
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
		participant.Reachability = "unbekannt"
		participant.WorkState = "unbekannt"
		participant.Manager = membership.Manager
		participant.Current = membership.PrincipalID == current.ID
		byID[membership.PrincipalID] = participant
	}
	out := make([]coordParticipantView, 0, len(byID))
	for _, participant := range byID {
		out = append(out, participant)
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
