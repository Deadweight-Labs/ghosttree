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
	Sidebar    coordSidebarView
	Active     *coordRoomDetailView
	Recipients []coordRecipientView
}

type coordRecipientView struct{ ID, Label, Kind string }

type coordSidebarView struct {
	Attention []coordRoomView
	Machines  []coordRoomView
	Projects  []coordRoomView
	Private   []coordRoomView
}

type coordRoomView struct {
	Key, Kind, Label, URL string
	Unread, Mentions      int64
	Active                bool
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

func buildCoordSidebar(summaries []store.CoordRoomSummary, principalID, activeKey string) coordSidebarView {
	var out coordSidebarView
	for _, summary := range summaries {
		room := coordRoomView{
			Key: summary.Room.Key, Kind: summary.Room.Kind,
			Label:  coordRoomLabel(summary.Room, principalID),
			URL:    coordRoomURL(summary.Room.Key, "", 0),
			Unread: summary.Unread, Mentions: summary.MentionUnread,
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
		if room.Mentions > 0 {
			out.Attention = append(out.Attention, room)
		}
	}
	return out
}

func coordRoomURL(room, cursor string, sequence int64) string {
	query := url.Values{"room": {room}}
	if cursor != "" && sequence > 0 {
		query.Set(cursor, strconv.FormatInt(sequence, 10))
	}
	return "/ui/coord?" + query.Encode()
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
