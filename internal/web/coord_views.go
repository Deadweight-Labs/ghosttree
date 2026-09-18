package web

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type coordPageView struct {
	Sidebar           coordSidebarView
	Active            *coordRoomDetailView
	Recipients        []coordRecipientView
	IncomingAttention []coordAttentionView
	OutgoingAttention []coordAttentionView
}

type coordRecipientView struct{ ID, Label, Kind string }

type coordSidebarView struct {
	NeedsAttention []coordRoomView
	Mentions       []coordRoomView
	Unread         []coordRoomView
	Machines       []coordRoomView
	Projects       []coordRoomView
	Private        []coordRoomView
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
	Messages []coordMessageView
	FormID   string
}

type coordMessageView struct {
	ID, Sequence                  int64
	Author, AuthorKind, Timestamp string
	Body, Intent                  string
	Expired                       bool
	Reply                         *coordReplyView
	Mentions                      []string
	Refs                          []coordRefView
	Delivery                      string
	ThreadURL                     string
	CanPromote                    bool
	CSRFToken                     string
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
	Targets                            []string
}

type coordParticipantView struct {
	ID, Label, Provider, LastSeen, Branch string
	Manager, Current                      bool
}

type coordAttentionActionView struct{ Value, Label string }

type coordAttentionView struct {
	ID, Sequence                                              int64
	RecipientID, Reason, State, Body, URL, RoomKey, CSRFToken string
	Incoming, CanWithdraw                                     bool
	CoordinationOnlyApproval                                  bool
	Actions                                                   []coordAttentionActionView
}

func buildCoordSidebar(summaries []store.CoordRoomSummary, principalID, activeKey string) coordSidebarView {
	var out coordSidebarView
	for _, summary := range summaries {
		room := coordRoomView{
			Key: summary.Room.Key, Kind: summary.Room.Kind,
			Label:  coordRoomLabel(summary.Room, principalID),
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
		if room.Attention > 0 {
			out.NeedsAttention = append(out.NeedsAttention, room)
		}
		if room.Mentions > 0 {
			out.Mentions = append(out.Mentions, room)
		}
		if room.Unread > 0 {
			out.Unread = append(out.Unread, room)
		}
	}
	return out
}

func buildCoordAttentionViews(items []store.AttentionItem, csrfToken ...string) (incoming, outgoing []coordAttentionView) {
	token := ""
	if len(csrfToken) > 0 {
		token = csrfToken[0]
	}
	for _, item := range items {
		if item.State != store.AttentionOpen {
			continue
		}
		view := coordAttentionView{
			ID: item.ID, Sequence: item.Sequence, Reason: item.Reason, State: item.State,
			Body: item.Body, RecipientID: item.RecipientID, Incoming: item.IsRecipient, CanWithdraw: item.CanWithdraw,
			RoomKey: item.HomeRoomKey, CSRFToken: token,
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

func coordRoomLabel(room store.CoordRoom, principalID string) string {
	if label := strings.TrimSpace(room.Label); label != "" {
		return label
	}
	switch room.Kind {
	case store.RoomDirect:
		var peers []string
		for _, member := range room.Members {
			if member != principalID {
				peers = append(peers, member)
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

func buildCoordMessageViews(messages []store.CoordMessagePresentation, roomKey string) []coordMessageView {
	out := make([]coordMessageView, 0, len(messages))
	for _, presentation := range messages {
		message := presentation.Message
		author := strings.TrimSpace(presentation.AuthorLabel)
		if author == "" {
			author = strings.TrimSpace(message.SenderExternalID)
		}
		view := coordMessageView{
			ID: message.ID, Sequence: message.Sequence, Author: author,
			AuthorKind: message.AuthorKind, Timestamp: message.CreatedAt,
			Body: message.Body, Intent: message.Intent, Expired: message.Expired,
			Mentions: append([]string(nil), presentation.Mentions...),
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
	}
	return out
}

func buildCoordThreadMessageViews(messages []store.CoordMessagePresentation) []coordMessageView {
	out := buildCoordMessageViews(messages, "")
	for i := range out {
		if out[i].Reply != nil && !out[i].Reply.Missing {
			out[i].Reply.URL = "#thread-message-" + strconv.FormatInt(out[i].Reply.Sequence, 10)
		}
	}
	return out
}

func buildCoordStandingViews(in []store.StandingInstruction) []coordStandingView {
	out := make([]coordStandingView, 0, len(in))
	for _, item := range in {
		out = append(out, coordStandingView{MessageID: item.MessageID, Person: item.Person, Body: item.Body, Targets: append([]string(nil), item.Targets...), CreatedAt: item.CreatedAt})
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

func buildCoordParticipants(room store.CoordRoom, peers []store.CoordAgent, memberships []store.RoomMembership, principalID string) []coordParticipantView {
	byID := make(map[string]coordParticipantView)
	for _, member := range room.Members {
		byID[member] = coordParticipantView{ID: member, Label: member, Current: member == principalID}
	}
	for _, peer := range peers {
		label := strings.TrimSpace(peer.DisplayName)
		if label == "" {
			label = peer.ExternalID
		}
		byID[peer.ExternalID] = coordParticipantView{
			ID: peer.ExternalID, Label: label, Provider: peer.Provider,
			LastSeen: peer.LastSeenAt, Branch: peer.Branch, Current: peer.ExternalID == principalID,
		}
	}
	if _, ok := byID[principalID]; !ok {
		byID[principalID] = coordParticipantView{ID: principalID, Label: principalID, Current: true}
	}
	for _, membership := range memberships {
		if membership.LeftAt != "" {
			continue
		}
		participant := byID[membership.PrincipalID]
		participant.ID = membership.PrincipalID
		if participant.Label == "" {
			participant.Label = membership.PrincipalID
		}
		participant.Manager = membership.Manager
		participant.Current = membership.PrincipalID == principalID
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
