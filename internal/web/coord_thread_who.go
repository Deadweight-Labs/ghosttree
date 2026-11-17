package web

import (
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Who is in a thread (REQ-701). The list of the room names everybody who ever
// registered there, with every session; the thread names those who wrote in it:
// a person once, an agent once with the person it belongs to. Machine, branch
// and sessions are the tooltip.

// ownerName is whose agent it is as a person reads it ("Robin"): the second part
// of the label, else the owner the agent reported.
func (p coordParticipantView) ownerName() string {
	parts := strings.Split(p.Label, " · ")
	if len(parts) > 1 {
		if s := strings.TrimSpace(parts[1]); s != "" && s != strings.TrimSpace(p.Machine) {
			return s
		}
	}
	return strings.TrimSpace(p.Person)
}

// threadTitle is what an agent is called in a thread: the tool, and "Agent" when
// the only name it has is its owner's ("robin" of "robin"), as in the lists of agents.
func threadTitle(l lang, p coordParticipantView) string {
	title := hereTitle(l, p)
	owner, _ := p.agentOwnerMachine()
	for _, same := range []string{owner, p.ownerName(), p.Person} {
		if same != "" && strings.EqualFold(title, same) {
			return l.msg("agent.provider.unknown")
		}
	}
	return title
}

// threadAgentKey says which sessions are one agent in a thread: the same tool of
// the same person. Where it ran does not make a second agent.
func threadAgentKey(l lang) func(coordParticipantView) string {
	return func(p coordParticipantView) string {
		owner, _ := p.agentOwnerMachine()
		return strings.ToLower(threadTitle(l, p) + "|" + owner)
	}
}

// threadAgentName is "Claude (Robin)", or the tool alone when nobody owns it.
func threadAgentName(l lang, p coordParticipantView) string {
	if owner := p.ownerName(); owner != "" {
		return threadTitle(l, p) + " (" + owner + ")"
	}
	return threadTitle(l, p)
}

// agentLabelOf is "Claude · Robin": the name of an agent in a picker, without the
// machine and the branch that tell its sessions apart.
func agentLabelOf(l lang, p coordParticipantView) string {
	if owner := p.ownerName(); owner != "" {
		return threadTitle(l, p) + " · " + owner
	}
	return threadTitle(l, p)
}

// threadAuthorParticipant is the participant of an author the room no longer
// lists: a session that was never registered here or has left.
func threadAuthorParticipant(l lang, au store.ThreadAuthor, labels map[string]string) coordParticipantView {
	if au.Kind != store.AuthorAgent {
		return coordParticipantView{ID: au.PrincipalID, Label: coordIdentityLabel(l, au.PrincipalID, labels), Human: true}
	}
	p := coordParticipantView{ID: au.SenderID, Label: coordIdentityLabel(l, au.SenderID, labels)}
	if prefix, _, ok := strings.Cut(au.SenderID, ":"); ok {
		p.Provider = prefix
	}
	if au.PrincipalID != "" {
		p.Person = strings.TrimSpace(labels[au.PrincipalID])
	}
	return p
}

// buildThreadWho lists who wrote in the thread, people first (the viewer at
// the head), then the agents, each in the order of their first message.
func buildThreadWho(l lang, authors []store.ThreadAuthor, room []coordParticipantView, labels map[string]string) []coordHereView {
	byID := make(map[string]coordParticipantView, len(room))
	for _, p := range room {
		byID[p.ID] = p
	}
	var parts []coordParticipantView
	for _, au := range authors {
		id := au.PrincipalID
		if au.Kind == store.AuthorAgent {
			id = au.SenderID
		}
		p, ok := byID[id]
		if !ok {
			p = threadAuthorParticipant(l, au, labels)
		}
		p.Human = au.Kind != store.AuthorAgent
		parts = append(parts, p)
	}
	here := groupParticipants(l, parts, threadAgentKey(l))
	for i := range here {
		h := &here[i]
		switch {
		case h.Human && h.Person.Current:
			h.Sub = l.msg("coord.you")
		case !h.Human:
			h.Title = threadTitle(l, h.Sessions[0])
			if owner := h.Sessions[0].ownerName(); owner != "" {
				h.Sub = l.msg("coord.mention.agent_of", owner)
			} else {
				h.Sub = ""
			}
			h.Tip = threadTip(l, *h)
		}
	}
	rank := func(h coordHereView) int {
		switch {
		case h.Human && h.Person.Current:
			return 0
		case h.Human:
			return 1
		}
		return 2
	}
	sort.SliceStable(here, func(i, j int) bool { return rank(here[i]) < rank(here[j]) })
	return here
}

// threadTip is where an agent ran and how many sessions wrote for it: the
// machines, the branches, the sessions ("mainex · feat/x · 2 Sessions").
func threadTip(l lang, h coordHereView) string {
	var bits []string
	seen := map[string]bool{}
	add := func(v string) {
		if v = strings.TrimSpace(v); v != "" && !seen[v] {
			seen[v] = true
			bits = append(bits, v)
		}
	}
	for _, s := range h.Sessions {
		_, machine := s.agentOwnerMachine()
		add(machine)
	}
	for _, s := range h.Sessions {
		add(s.Branch)
	}
	tip := strings.Join(bits, " · ")
	if h.Count > 1 {
		if tip != "" {
			tip += " · "
		}
		tip += l.msg("coord.sessions", h.Count)
	}
	return tip
}

// threadAddressees are the ones the composer of a thread offers after @ and in
// "To": the people and agents of the thread first, then the rest of the room, an
// agent once per tool and person. Of an agent's sessions the one named by
// preferred (the one being answered) stands for it, else the one seen last.
func threadAddressees(l lang, who []coordHereView, room []coordParticipantView, preferred string) []coordParticipantView {
	key := threadAgentKey(l)
	sessions := map[string][]coordParticipantView{}
	for _, p := range room {
		if !p.Human {
			sessions[key(p)] = append(sessions[key(p)], p)
		}
	}
	better := func(a, b coordParticipantView) bool {
		if (a.Handle != "") != (b.Handle != "") {
			return a.Handle != ""
		}
		return a.LastSeen > b.LastSeen
	}
	rep := func(k string) (coordParticipantView, bool) {
		var best coordParticipantView
		found := false
		for _, s := range sessions[k] {
			if s.ID == preferred {
				return decorateAddressee(l, s), true
			}
			if !found || better(s, best) {
				best, found = s, true
			}
		}
		return decorateAddressee(l, best), found
	}
	var people, agents []coordParticipantView
	covered := map[string]bool{}
	seenPerson := map[string]bool{}
	for _, h := range who {
		if h.Human {
			if !h.Person.Current {
				people = append(people, h.Person)
			}
			seenPerson[h.Person.ID] = true
			continue
		}
		k := key(h.Sessions[0])
		covered[k] = true
		// An agent the room no longer lists cannot be addressed.
		if p, ok := rep(k); ok {
			agents = append(agents, p)
		}
	}
	for _, p := range room {
		if p.Human && !p.Current && !seenPerson[p.ID] {
			people = append(people, p)
			seenPerson[p.ID] = true
		}
	}
	var rest []string
	for _, p := range room {
		if k := key(p); !p.Human && !p.Current && !covered[k] {
			covered[k] = true
			rest = append(rest, k)
		}
	}
	for _, k := range rest {
		if p, ok := rep(k); ok {
			agents = append(agents, p)
		}
	}
	return append(people, agents...)
}

// decorateAddressee names an agent as a person and a tool, not as a session.
func decorateAddressee(l lang, p coordParticipantView) coordParticipantView {
	if !p.Human {
		p.Label = agentLabelOf(l, p)
	}
	return p
}

// buildThreadWaitLines says in one sentence per cycle who waits on each other in
// this thread. Sessions of one agent are one agent: a cycle that is left with
// fewer than two different ones is no waiting on each other and says nothing.
func buildThreadWaitLines(l lang, cycles []store.WaitCycle, room []coordParticipantView, labels map[string]string) []string {
	byID := make(map[string]coordParticipantView, len(room))
	for _, p := range room {
		byID[p.ID] = p
	}
	key := threadAgentKey(l)
	var out []string
	for _, c := range cycles {
		seen := map[string]bool{}
		var names []string
		for _, id := range c.Members {
			// The room list drops a session that registered twice under one label; it is still the same agent.
			p, ok := byID[id]
			if !ok {
				p = threadAuthorParticipant(l, store.ThreadAuthor{Kind: store.AuthorAgent, SenderID: id}, labels)
			}
			k := key(p)
			if seen[k] {
				continue
			}
			seen[k] = true
			names = append(names, threadAgentName(l, p))
		}
		if len(names) < 2 {
			continue
		}
		out = append(out, l.msg("coord.wait_pair", coordJoinNames(l, names), coordShortWhen(c.Since)))
	}
	return out
}

// coordShortWhen is a moment in few words: the time of day when it is today,
// else day, month and time.
func coordShortWhen(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	t = t.In(time.Local)
	if now := time.Now(); t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("02.01. 15:04")
}
