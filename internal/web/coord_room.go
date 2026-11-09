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

// coordHereView is one line of "who is here": a person, or an agent with all its
// sessions in this room. Sessions are listed beneath the agent, live ones first.
type coordHereView struct {
	// Human is the person (Sessions is empty); else Agent is the one agent line.
	Human    bool
	Person   coordParticipantView
	Provider string
	Title    string
	Owner    string
	// Sub says whose agent and on which machine, "Robin · mainex".
	Sub      string
	Sessions []coordParticipantView
	// Dot and Status are the most pressing state among the sessions; Count is the
	// number of sessions. Ask is the link to an open question one of them has.
	Dot, Status string
	Count       int
	AskURL      string
	Key         string
}

// coordAgentKey says which sessions are one agent: same owner, same tool, same
// machine. A session without an owner stands alone.
func coordAgentKey(p coordParticipantView) string {
	owner, machine := p.agentOwnerMachine()
	if owner == "" {
		return "id:" + p.ID
	}
	return strings.ToLower(p.Provider + "|" + owner + "|" + machine)
}

// agentOwnerMachine reads the owner and machine of an agent from what the peer
// reported, else from its label "Claude · Robin · mainex · feat/x".
func (p coordParticipantView) agentOwnerMachine() (owner, machine string) {
	owner, machine = p.Person, p.Machine
	parts := strings.Split(p.Label, " · ")
	if owner == "" && len(parts) > 1 {
		owner = strings.TrimSpace(parts[1])
	}
	if machine == "" && len(parts) > 2 {
		machine = strings.TrimSpace(parts[2])
	}
	return owner, machine
}

var coordDotRank = map[string]int{"wait": 0, "bad": 1, "active": 2, "quiet": 3, "off": 4, "": 5}

// groupParticipantsHere turns the ordered participants into the lines of the
// list. The order of the input is kept: the group stands where its first member stood.
func groupParticipantsHere(l lang, parts []coordParticipantView) []coordHereView {
	var out []coordHereView
	at := map[string]int{}
	for _, p := range parts {
		if p.Human {
			out = append(out, coordHereView{Human: true, Person: p, Key: p.ID})
			continue
		}
		key := coordAgentKey(p)
		i, ok := at[key]
		if !ok {
			owner, machine := p.agentOwnerMachine()
			title := p.Provider
			if kind := harnessKind(p.Provider); kind == avClaude || kind == avCodex {
				title = l.msg(avatarKeys[kind])
			} else if first, _, _ := strings.Cut(p.Label, " · "); first != "" {
				title = first
			}
			sub := owner
			if machine != "" {
				if sub != "" {
					sub += " · "
				}
				sub += machine
			}
			out = append(out, coordHereView{Provider: p.Provider, Title: title, Owner: owner, Sub: sub, Key: key})
			i = len(out) - 1
			at[key] = i
		}
		out[i].Sessions = append(out[i].Sessions, p)
	}
	for i := range out {
		if out[i].Human {
			continue
		}
		g := &out[i]
		sort.SliceStable(g.Sessions, func(a, b int) bool {
			ra, rb := coordDotRank[g.Sessions[a].Dot], coordDotRank[g.Sessions[b].Dot]
			if ra != rb {
				return ra < rb
			}
			return g.Sessions[a].LastSeen > g.Sessions[b].LastSeen
		})
		g.Count = len(g.Sessions)
		best := g.Sessions[0]
		g.Dot, g.Status = best.Dot, best.StatusLabel
		for _, s := range g.Sessions {
			if s.AskURL != "" {
				g.AskURL = s.AskURL
				break
			}
		}
		if g.Count > 1 {
			g.Status = l.msg("coord.sessions", g.Count)
		}
	}
	return out
}

// stackHere puts the people first and the agents after them, for the few
// avatars in the head of a room.
func stackHere(in []coordHereView) []coordHereView {
	out := make([]coordHereView, 0, len(in))
	for _, h := range in {
		if h.Human {
			out = append(out, h)
		}
	}
	for _, h := range in {
		if !h.Human {
			out = append(out, h)
		}
	}
	return out
}

// firstUnreadSequence is the sequence of the first message in the window that
// the viewer has not read and did not write: the divider "New" stands above it.
// Zero when everything on the page is read, or when only the viewer's own
// messages follow the read mark.
func firstUnreadSequence(messages []coordMessageView, readThrough, manualFrom int64) int64 {
	from := readThrough
	if manualFrom > 0 && manualFrom-1 < from {
		from = manualFrom - 1
	}
	for _, m := range messages {
		if m.Sequence > from && !m.Own && !m.System {
			return m.Sequence
		}
	}
	return 0
}
