package web

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// coordOrderByActivity sorts rooms by the time of their last message, newest
// first; rooms without a message keep their order at the end. The tab bar and
// the landing both read this order.
func coordOrderByActivity(in []store.CoordRoomSummary) []store.CoordRoomSummary {
	out := append([]store.CoordRoomSummary(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastMessageAt > out[j].LastMessageAt
	})
	return out
}

// coordLandingRoom is the room /ui/coord opens: the one with the latest event
// among the rooms the viewer sees in the tab bar. Empty when there is none.
func coordLandingRoom(ordered []store.CoordRoomSummary, sidebar coordSidebarView) string {
	for _, summary := range ordered {
		if _, ok := sidebar.room(summary.Room.Key); ok {
			return summary.Room.Key
		}
	}
	return ""
}

// applyParticipantAsks marks the agents that hold an open question for the
// viewer in this room, with the link to that message.
func applyParticipantAsks(parts []coordParticipantView, items []store.AttentionItem, roomKey string) {
	asks := map[string]string{}
	for _, item := range items {
		if !item.IsRecipient || item.State != store.AttentionOpen || item.SenderID == "" || item.HomeRoomKey != roomKey || item.Sequence <= 0 {
			continue
		}
		seq := strconv.FormatInt(item.Sequence, 10)
		asks[item.SenderID] = coordRoomURL(roomKey, "around", item.Sequence) + "#message-" + seq
	}
	for i := range parts {
		parts[i].AskURL = asks[parts[i].ID]
	}
}

// orderParticipantsHere is the order of "who is here": agents that wait for the
// viewer first, then the other agents, then the people with the viewer first.
func orderParticipantsHere(parts []coordParticipantView) []coordParticipantView {
	rank := func(p coordParticipantView) int {
		switch {
		case p.AskURL != "":
			return 0
		case !p.Human:
			return 1
		case p.Current:
			return 2
		}
		return 3
	}
	sort.SliceStable(parts, func(i, j int) bool { return rank(parts[i]) < rank(parts[j]) })
	return parts
}

// dedupeAgents drops an agent that shows twice with the same label and tool: a
// session that registered again under a new ID. The one seen last stays. People
// are keyed by their ID alone, which the caller's map already guarantees.
func dedupeAgents(in []coordParticipantView) []coordParticipantView {
	best := map[string]int{}
	for i, p := range in {
		if p.Human || p.Current {
			continue
		}
		key := strings.ToLower(p.Label + "|" + p.Provider)
		if j, ok := best[key]; !ok || in[j].LastSeen < p.LastSeen {
			best[key] = i
		}
	}
	out := in[:0:0]
	for i, p := range in {
		if !p.Human && !p.Current {
			if best[strings.ToLower(p.Label+"|"+p.Provider)] != i {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// RoomLead is the first sentence of the explanation, the preview next to the
// fold's title. Empty while the text is still being generated.
func (e *explainView) RoomLead() string {
	if e == nil {
		return ""
	}
	text := strings.TrimSpace(e.Text)
	for i, r := range text {
		if (r == '.' || r == '!' || r == '?') && (i+1 == len(text) || unicode.IsSpace(rune(text[i+1]))) {
			return text[:i+1]
		}
	}
	return text
}

// coordPeerOnly reports a message between agents: sent by an agent to named
// agents only, no question and no standing instruction. People read it as
// background, so it is shown without a bubble.
func coordPeerOnly(authorKind, intent string, mentions []string) bool {
	if authorKind != store.AuthorAgent || intent != "" || len(mentions) == 0 {
		return false
	}
	for _, m := range mentions {
		if strings.HasPrefix(m, "person:") {
			return false
		}
	}
	return true
}
